package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
)

func (s *Service) ListRunHandoffsFor(ctx context.Context, taskID string, principal Principal) ([]model.RunHandoff, error) {
	task, err := s.GetFor(ctx, strings.TrimSpace(taskID), principal)
	if err != nil {
		return nil, err
	}
	if principal.Agent && (!principal.HasCapability(CapabilityTaskHandoff) || !canMutate(task, principal)) {
		return nil, ErrForbidden
	}
	return s.store.ListTaskHandoffs(ctx, task.ID)
}

func (s *Service) AddRunHandoffFor(ctx context.Context, taskID string, request model.AddRunHandoffRequest, principal Principal) (model.RunHandoff, error) {
	if !principal.Agent || !principal.HasCapability(CapabilityTaskHandoff) {
		return model.RunHandoff{}, ErrForbidden
	}
	taskID, request.RunID = strings.TrimSpace(taskID), strings.TrimSpace(request.RunID)
	if request.Kind == "" {
		request.Kind = model.HandoffCheckpoint
	}
	if taskID == "" || request.RunID == "" || (request.Kind != model.HandoffCheckpoint && request.Kind != model.HandoffFinal) {
		return model.RunHandoff{}, fmt.Errorf("%w: task_id, run_id, and checkpoint or final kind are required", ErrValidation)
	}
	cleanHandoffRequest(&request)
	if err := validateHandoffRequest(request); err != nil {
		return model.RunHandoff{}, err
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.RunHandoff{}, err
	}
	if !canMutate(task, principal) {
		return model.RunHandoff{}, ErrForbidden
	}
	if _, ok := activeOwnedRun(task, request.RunID, principal.ID); !ok {
		return model.RunHandoff{}, fmt.Errorf("%w: handoff run is not active for this agent", ErrValidation)
	}
	if request.IdempotencyKey != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
	}
	hash, err := requestHash(struct {
		TaskID string
		model.AddRunHandoffRequest
	}{taskID, request})
	if err != nil {
		return model.RunHandoff{}, err
	}
	request.IdempotencyHash = hash
	event, replay, err := s.replayIdempotency(ctx, principal, "task_handoff_add", request.IdempotencyKey, hash)
	if err != nil {
		return model.RunHandoff{}, err
	}
	if replay {
		id, _ := event.Payload["handoff_id"].(string)
		return s.store.GetRunHandoff(ctx, id)
	}

	now := time.Now().UTC()
	item := model.RunHandoff{ID: newID(now), TaskID: taskID, RunID: request.RunID, Kind: request.Kind, LastCompletedStep: request.LastCompletedStep, Worktree: request.Worktree, Branch: request.Branch, Commits: request.Commits, PullRequests: request.PullRequests, Validation: request.Validation, ReviewFindings: request.ReviewFindings, Blocker: request.Blocker, NextAction: request.NextAction, CreatedBy: principal.ID, CreatedAt: now}
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.RunHandoff{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_runs WHERE id=? AND task_id=? AND agent=? AND status=? AND ended_at IS NULL`, request.RunID, taskID, principal.ID, model.TaskActive).Scan(&active); err != nil {
		return model.RunHandoff{}, err
	}
	if active != 1 {
		return model.RunHandoff{}, fmt.Errorf("%w: handoff run is no longer active", ErrConflict)
	}
	if err := store.InsertRunHandoff(ctx, tx, item); err != nil {
		return model.RunHandoff{}, err
	}
	payload := map[string]any{"handoff_id": item.ID, "handoff_kind": item.Kind, "run_id": item.RunID}
	mergePayload(payload, idempotencyPayload("task_handoff_add", request.IdempotencyKey, request.IdempotencyHash))
	created := model.Event{ID: newID(now), TaskID: taskID, RunID: item.RunID, Kind: "task.handoff_added", Actor: principal.ID, Message: "Run handoff recorded", Payload: payload, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, created); err != nil {
		return model.RunHandoff{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.RunHandoff{}, err
	}
	s.publish(created)
	return item, nil
}

func cleanHandoffRequest(request *model.AddRunHandoffRequest) {
	request.LastCompletedStep = strings.TrimSpace(request.LastCompletedStep)
	request.Worktree = strings.TrimSpace(request.Worktree)
	request.Branch = strings.TrimSpace(request.Branch)
	request.Blocker = strings.TrimSpace(request.Blocker)
	request.NextAction = strings.TrimSpace(request.NextAction)
	request.Commits = cleanStringList(request.Commits)
	request.PullRequests = cleanStringList(request.PullRequests)
	request.Validation = cleanStringList(request.Validation)
	request.ReviewFindings = cleanStringList(request.ReviewFindings)
}

func cleanStringList(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func validateHandoffRequest(request model.AddRunHandoffRequest) error {
	fields := []string{request.LastCompletedStep, request.Worktree, request.Branch, request.Blocker, request.NextAction}
	for _, value := range fields {
		if len(value) > 2000 {
			return fmt.Errorf("%w: handoff fields are limited to 2000 characters", ErrValidation)
		}
	}
	for _, list := range [][]string{request.Commits, request.PullRequests, request.Validation, request.ReviewFindings} {
		if len(list) > 100 {
			return fmt.Errorf("%w: handoff lists are limited to 100 entries", ErrValidation)
		}
		for _, value := range list {
			if len(value) > 1000 {
				return fmt.Errorf("%w: handoff list entries are limited to 1000 characters", ErrValidation)
			}
		}
	}
	return nil
}

// ensureDerivedHandoff records only facts already held by Taskboard. It is used
// when a run ends without an explicit final checkpoint, including lease expiry.
func (s *Service) ensureDerivedHandoff(ctx context.Context, taskID, runID string, kind model.HandoffKind) error {
	var count int
	if err := s.store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM run_handoffs WHERE run_id=? AND kind IN (?,?)`, runID, model.HandoffFinal, model.HandoffStale).Scan(&count); err != nil || count > 0 {
		return err
	}
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	item := model.RunHandoff{ID: newID(time.Now().UTC()), TaskID: taskID, RunID: runID, Kind: kind, Blocker: task.Blocker, NextAction: task.WaitingFor, CreatedBy: "system", CreatedAt: time.Now().UTC()}
	for _, checklist := range task.Items {
		if checklist.Status == model.ItemDone || checklist.Status == model.ItemSkipped {
			item.LastCompletedStep = checklist.Label
		}
	}
	references, _ := s.store.ListTaskReferences(ctx, taskID)
	for _, reference := range references {
		if reference.RunID != runID {
			continue
		}
		switch reference.Kind {
		case model.ReferenceWorktree:
			item.Worktree = reference.Locator
		case model.ReferenceBranch:
			item.Branch = reference.Locator
		case model.ReferenceCommit:
			item.Commits = append(item.Commits, reference.Locator)
		case model.ReferencePullRequest:
			item.PullRequests = append(item.PullRequests, firstNonEmpty(reference.URL, reference.Locator))
		case model.ReferenceCIRun, model.ReferenceScreenshot:
			item.Validation = append(item.Validation, firstNonEmpty(reference.URL, reference.Locator))
		case model.ReferenceReview:
			item.ReviewFindings = append(item.ReviewFindings, firstNonEmpty(reference.URL, reference.Locator))
		}
	}
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = store.InsertRunHandoff(ctx, tx, item); err != nil {
		return err
	}
	event := model.Event{ID: newID(item.CreatedAt), TaskID: taskID, RunID: runID, Kind: "task.handoff_generated", Actor: "system", Message: "Run handoff generated", Payload: map[string]any{"handoff_id": item.ID, "handoff_kind": kind}, CreatedAt: item.CreatedAt}
	if err = store.InsertEvent(ctx, tx, event); err != nil {
		return err
	}
	if err = tx.Commit(); err == nil {
		s.publish(event)
	}
	return err
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
