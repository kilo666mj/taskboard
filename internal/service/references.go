package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
)

func (s *Service) ListTaskReferencesFor(ctx context.Context, taskID string, principal Principal) ([]model.TaskReference, error) {
	if principal.Agent && !principal.HasCapability(CapabilityTaskReference) {
		return nil, ErrForbidden
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return nil, err
	}
	if principal.Agent && !canMutate(task, principal) {
		return nil, ErrForbidden
	}
	return s.store.ListTaskReferences(ctx, task.ID)
}

func (s *Service) AddTaskReferenceFor(ctx context.Context, taskID string, request model.AddTaskReferenceRequest, principal Principal) (model.TaskReference, error) {
	if principal.Agent {
		if !principal.HasCapability(CapabilityTaskReference) {
			return model.TaskReference{}, ErrForbidden
		}
	} else if !principal.Can(PermissionTaskWrite) {
		return model.TaskReference{}, ErrForbidden
	}
	taskID, request.RunID = strings.TrimSpace(taskID), strings.TrimSpace(request.RunID)
	request.Label, request.Locator, request.URL = strings.TrimSpace(request.Label), strings.TrimSpace(request.Locator), strings.TrimSpace(request.URL)
	if taskID == "" || !isReferenceKind(request.Kind) {
		return model.TaskReference{}, fmt.Errorf("%w: task_id and a valid reference kind are required", ErrValidation)
	}
	if request.Label == "" || len(request.Label) > 200 || len(request.Locator) > 2000 || len(request.URL) > 2000 {
		return model.TaskReference{}, fmt.Errorf("%w: label must contain 1-200 characters and locator/URL are limited to 2000", ErrValidation)
	}
	if request.Locator == "" && request.URL == "" {
		return model.TaskReference{}, fmt.Errorf("%w: locator or URL is required", ErrValidation)
	}
	if request.URL != "" {
		parsed, err := url.Parse(request.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || strings.ContainsAny(request.URL, "\r\n") {
			return model.TaskReference{}, fmt.Errorf("%w: reference URL must be absolute HTTPS without embedded credentials", ErrValidation)
		}
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.TaskReference{}, err
	}
	if !canMutate(task, principal) {
		return model.TaskReference{}, ErrForbidden
	}
	provenance := model.ReferenceByHuman
	if principal.Agent {
		provenance = model.ReferenceByAgent
		if request.RunID == "" {
			return model.TaskReference{}, fmt.Errorf("%w: agent references require run_id", ErrValidation)
		}
		if _, ok := activeOwnedRun(task, request.RunID, principal.ID); !ok {
			return model.TaskReference{}, fmt.Errorf("%w: reference run is not active for this agent", ErrValidation)
		}
	} else if request.RunID != "" {
		return model.TaskReference{}, fmt.Errorf("%w: human references cannot claim a source run", ErrValidation)
	}

	if principal.Agent || request.IdempotencyKey != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
	}
	hash, err := requestHash(struct {
		TaskID string
		model.AddTaskReferenceRequest
	}{taskID, request})
	if err != nil {
		return model.TaskReference{}, err
	}
	request.IdempotencyHash = hash
	event, replay, err := s.replayIdempotency(ctx, principal, "task_reference_add", request.IdempotencyKey, hash)
	if err != nil {
		return model.TaskReference{}, err
	}
	if replay {
		id, _ := event.Payload["reference_id"].(string)
		return s.store.GetTaskReference(ctx, id)
	}

	now := time.Now().UTC()
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.TaskReference{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := store.LoadTask(ctx, tx, taskID)
	if err != nil {
		return model.TaskReference{}, err
	}
	if principal.Agent {
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_runs WHERE id=? AND task_id=? AND agent=? AND status=? AND ended_at IS NULL`, request.RunID, taskID, principal.ID, model.TaskActive).Scan(&active); err != nil {
			return model.TaskReference{}, err
		}
		if active != 1 {
			return model.TaskReference{}, fmt.Errorf("%w: reference run is not active for this agent", ErrConflict)
		}
	}
	reference := model.TaskReference{ID: newID(now), TaskID: taskID, RunID: request.RunID, Kind: request.Kind, Label: request.Label, Locator: request.Locator, URL: request.URL, CreatedBy: principal.ID, Provenance: provenance, CreatedAt: now}
	if err := store.InsertTaskReference(ctx, tx, reference); err != nil {
		return model.TaskReference{}, err
	}
	payload := map[string]any{"reference_id": reference.ID, "reference_kind": reference.Kind, "run_id": reference.RunID, "version": current.Version}
	mergePayload(payload, idempotencyPayload("task_reference_add", request.IdempotencyKey, request.IdempotencyHash))
	created := model.Event{ID: newID(now), TaskID: taskID, RunID: request.RunID, Kind: "task.reference_added", Actor: principal.ID, Message: "Delivery reference added", Payload: payload, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, created); err != nil {
		return model.TaskReference{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.TaskReference{}, err
	}
	s.publish(created)
	return reference, nil
}

func (s *Service) DeliveryMilestonesFor(ctx context.Context, taskID string, principal Principal) ([]model.DeliveryMilestone, error) {
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return nil, err
	}
	if principal.Agent && (!principal.HasCapability(CapabilityTaskReference) || !canMutate(task, principal)) {
		return nil, ErrForbidden
	}
	references, err := s.store.ListTaskReferences(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	events, err := s.store.ListTaskEvents(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	byKind := map[string]model.DeliveryMilestone{}
	add := func(kind, label string, reached time.Time, referenceID, eventID string) {
		if previous, exists := byKind[kind]; exists && !reached.Before(previous.ReachedAt) {
			return
		}
		byKind[kind] = model.DeliveryMilestone{Kind: kind, Label: label, ReachedAt: reached, ReferenceID: referenceID, EventID: eventID}
	}
	for _, reference := range references {
		kind, label := milestoneForReference(reference.Kind)
		add(kind, label, reference.CreatedAt, reference.ID, "")
	}
	for _, event := range events {
		switch event.Kind {
		case "task.started", "task.claimed":
			add("claimed", "Work claimed", event.CreatedAt, "", event.ID)
		case "task.updated":
			status := fmt.Sprint(event.Payload["status"])
			if status == string(model.TaskDone) {
				add("completed", "Task completed", event.CreatedAt, "", event.ID)
			}
		}
	}
	order := []string{"requirements_linked", "repository_linked", "claimed", "workspace_ready", "branch_created", "implementation", "pull_request_opened", "ci_observed", "review_observed", "validation_evidence", "deployed", "completed"}
	result := make([]model.DeliveryMilestone, 0, len(byKind))
	for _, kind := range order {
		if milestone, ok := byKind[kind]; ok {
			result = append(result, milestone)
		}
	}
	return result, nil
}

func milestoneForReference(kind model.ReferenceKind) (string, string) {
	switch kind {
	case model.ReferenceLinear:
		return "requirements_linked", "Requirements linked"
	case model.ReferenceRepository:
		return "repository_linked", "Repository linked"
	case model.ReferenceWorktree:
		return "workspace_ready", "Worktree ready"
	case model.ReferenceBranch:
		return "branch_created", "Branch created"
	case model.ReferenceCommit:
		return "implementation", "Implementation committed"
	case model.ReferencePullRequest:
		return "pull_request_opened", "Pull request opened"
	case model.ReferenceCIRun:
		return "ci_observed", "CI run linked"
	case model.ReferenceReview:
		return "review_observed", "Review linked"
	case model.ReferenceScreenshot:
		return "validation_evidence", "Validation evidence linked"
	case model.ReferenceDeployment:
		return "deployed", "Deployment linked"
	default:
		return string(kind), string(kind)
	}
}

func isReferenceKind(kind model.ReferenceKind) bool {
	switch kind {
	case model.ReferenceLinear, model.ReferenceRepository, model.ReferenceWorktree, model.ReferenceBranch, model.ReferenceCommit, model.ReferencePullRequest, model.ReferenceCIRun, model.ReferenceDeployment, model.ReferenceScreenshot, model.ReferenceReview:
		return true
	default:
		return false
	}
}
