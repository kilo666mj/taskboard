package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
)

func (s *Service) ListEscalationsFor(ctx context.Context, taskID string, principal Principal) ([]model.TaskEscalation, error) {
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return nil, err
	}
	if principal.Agent && (!principal.HasCapability(CapabilityTaskEscalate) || !canMutate(task, principal)) {
		return nil, ErrForbidden
	}
	return s.store.ListTaskEscalations(ctx, task.ID)
}

func (s *Service) CreateEscalationFor(ctx context.Context, taskID string, request model.CreateEscalationRequest, principal Principal) (model.TaskEscalation, error) {
	if !principal.Agent || !principal.HasCapability(CapabilityTaskEscalate) || !principal.HasCapability(CapabilityTaskMessage) {
		return model.TaskEscalation{}, ErrForbidden
	}
	taskID, request.RunID = strings.TrimSpace(taskID), strings.TrimSpace(request.RunID)
	request.Question, request.Recommendation = strings.TrimSpace(request.Question), strings.TrimSpace(request.Recommendation)
	if taskID == "" || request.RunID == "" || request.ExpectedVersion < 1 {
		return model.TaskEscalation{}, fmt.Errorf("%w: task_id, run_id, and expected_version are required", ErrValidation)
	}
	if request.Question == "" || len(request.Question) > 4000 {
		return model.TaskEscalation{}, fmt.Errorf("%w: question must contain 1-4000 characters", ErrValidation)
	}
	if len(request.Recommendation) > 1000 || len(request.Options) > 10 {
		return model.TaskEscalation{}, fmt.Errorf("%w: recommendation is limited to 1000 characters and choices to 10", ErrValidation)
	}
	seen := map[string]bool{}
	for index := range request.Options {
		request.Options[index] = strings.TrimSpace(request.Options[index])
		key := strings.ToLower(request.Options[index])
		if request.Options[index] == "" || len(request.Options[index]) > 200 || seen[key] {
			return model.TaskEscalation{}, fmt.Errorf("%w: choices must be unique and contain 1-200 characters", ErrValidation)
		}
		seen[key] = true
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	if !canMutate(task, principal) {
		return model.TaskEscalation{}, ErrForbidden
	}
	if _, ok := activeOwnedRun(task, request.RunID, principal.ID); !ok {
		return model.TaskEscalation{}, fmt.Errorf("%w: escalation requires this agent's active run", ErrValidation)
	}

	s.idempotencyMu.Lock()
	defer s.idempotencyMu.Unlock()
	hash, err := requestHash(struct {
		TaskID string
		model.CreateEscalationRequest
	}{taskID, request})
	if err != nil {
		return model.TaskEscalation{}, err
	}
	request.IdempotencyHash = hash
	event, replay, err := s.replayIdempotency(ctx, principal, "task_escalation_create", request.IdempotencyKey, hash)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	if replay {
		id, _ := event.Payload["escalation_id"].(string)
		return s.store.GetTaskEscalation(ctx, id)
	}

	now := time.Now().UTC()
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := store.LoadTask(ctx, tx, taskID)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	if current.Version != request.ExpectedVersion || current.Status != model.TaskActive {
		return model.TaskEscalation{}, ErrConflict
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_runs WHERE id=? AND task_id=? AND agent=? AND status=? AND ended_at IS NULL`, request.RunID, taskID, principal.ID, model.TaskActive).Scan(&active); err != nil {
		return model.TaskEscalation{}, err
	}
	if active != 1 {
		return model.TaskEscalation{}, fmt.Errorf("%w: escalation requires this agent's active run", ErrValidation)
	}
	question := model.TaskMessage{ID: newID(now), TaskID: taskID, Author: principal.ID, AuthorRunID: request.RunID, Kind: model.MessageQuestion, Body: request.Question, CreatedAt: now}
	if err := store.InsertTaskMessage(ctx, tx, question); err != nil {
		return model.TaskEscalation{}, err
	}
	escalation := model.TaskEscalation{ID: newID(now), TaskID: taskID, RunID: request.RunID, QuestionMessageID: question.ID, Blocking: request.Blocking, Options: request.Options, Recommendation: request.Recommendation, Status: model.EscalationOpen, CreatedAt: now}
	if err := store.InsertTaskEscalation(ctx, tx, escalation); err != nil {
		return model.TaskEscalation{}, err
	}
	version := current.Version
	if request.Blocking {
		result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status=?,lease_expires_at=?,last_heartbeat_at=?,ended_at=? WHERE id=? AND task_id=? AND status=? AND ended_at IS NULL`, model.TaskWaiting, stamp(now), stamp(now), stamp(now), request.RunID, taskID, model.TaskActive)
		if err != nil {
			return model.TaskEscalation{}, err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return model.TaskEscalation{}, ErrConflict
		}
		result, err = tx.ExecContext(ctx, `UPDATE tasks SET status=?,waiting_for=?,version=version+1,updated_at=? WHERE id=? AND version=? AND status=?`, model.TaskWaiting, clipped(request.Question, 1000), stamp(now), taskID, current.Version, model.TaskActive)
		if err != nil {
			return model.TaskEscalation{}, err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return model.TaskEscalation{}, ErrConflict
		}
		version++
	}
	payload := map[string]any{"escalation_id": escalation.ID, "question_message_id": question.ID, "blocking": request.Blocking, "status": map[bool]model.TaskStatus{true: model.TaskWaiting, false: model.TaskActive}[request.Blocking], "version": version}
	mergePayload(payload, idempotencyPayload("task_escalation_create", request.IdempotencyKey, request.IdempotencyHash))
	created := model.Event{ID: newID(now), TaskID: taskID, RunID: request.RunID, Kind: "task.escalated", Actor: principal.ID, Message: "Agent requested a decision", Payload: payload, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, created); err != nil {
		return model.TaskEscalation{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.TaskEscalation{}, err
	}
	s.publish(created)
	return escalation, nil
}

func (s *Service) ResolveEscalationFor(ctx context.Context, taskID, escalationID string, request model.ResolveEscalationRequest, principal Principal) (model.TaskEscalation, error) {
	if principal.Agent || !principal.Can(PermissionTaskWrite) {
		return model.TaskEscalation{}, ErrForbidden
	}
	taskID, escalationID = strings.TrimSpace(taskID), strings.TrimSpace(escalationID)
	request.Answer, request.SelectedOption = strings.TrimSpace(request.Answer), strings.TrimSpace(request.SelectedOption)
	if taskID == "" || escalationID == "" || request.ExpectedVersion < 1 || request.Answer == "" || len(request.Answer) > 4000 {
		return model.TaskEscalation{}, fmt.Errorf("%w: task_id, escalation_id, expected_version, and a 1-4000 character answer are required", ErrValidation)
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	if !canMutate(task, principal) {
		return model.TaskEscalation{}, ErrForbidden
	}

	if request.IdempotencyKey != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
	}
	hash, err := requestHash(struct {
		TaskID       string
		EscalationID string
		model.ResolveEscalationRequest
	}{taskID, escalationID, request})
	if err != nil {
		return model.TaskEscalation{}, err
	}
	request.IdempotencyHash = hash
	event, replay, err := s.replayIdempotency(ctx, principal, "task_escalation_resolve", request.IdempotencyKey, hash)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	if replay {
		id, _ := event.Payload["escalation_id"].(string)
		return s.store.GetTaskEscalation(ctx, id)
	}

	now := time.Now().UTC()
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := store.LoadTask(ctx, tx, taskID)
	if err != nil {
		return model.TaskEscalation{}, err
	}
	if current.Version != request.ExpectedVersion {
		return model.TaskEscalation{}, ErrConflict
	}
	escalation, err := store.LoadTaskEscalation(ctx, tx, escalationID)
	if err != nil || escalation.TaskID != taskID {
		return model.TaskEscalation{}, store.ErrNotFound
	}
	if escalation.Status != model.EscalationOpen {
		return model.TaskEscalation{}, ErrConflict
	}
	if request.SelectedOption != "" && !containsExact(escalation.Options, request.SelectedOption) {
		return model.TaskEscalation{}, fmt.Errorf("%w: selected_option must match one of the escalation choices", ErrValidation)
	}
	if escalation.Blocking && current.Status != model.TaskWaiting {
		return model.TaskEscalation{}, fmt.Errorf("%w: blocking escalation can only be answered while the task is waiting", ErrValidation)
	}
	answer := model.TaskMessage{ID: newID(now), TaskID: taskID, Author: principal.ID, Kind: model.MessageAnswer, Body: request.Answer, ReplyToID: escalation.QuestionMessageID, CreatedAt: now}
	if err := store.InsertTaskMessage(ctx, tx, answer); err != nil {
		return model.TaskEscalation{}, err
	}
	if err := store.ResolveTaskEscalation(ctx, tx, escalationID, answer.ID, request.SelectedOption, principal.ID, now); err != nil {
		return model.TaskEscalation{}, err
	}
	version := current.Version
	if escalation.Blocking {
		owner := current.Owner
		if current.Visibility == model.VisibilityAgent {
			owner = ""
		}
		result, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,owner=?,waiting_for='',version=version+1,updated_at=? WHERE id=? AND version=? AND status=?`, model.TaskQueued, owner, stamp(now), taskID, current.Version, model.TaskWaiting)
		if err != nil {
			return model.TaskEscalation{}, err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return model.TaskEscalation{}, ErrConflict
		}
		version++
	}
	payload := map[string]any{"escalation_id": escalation.ID, "answer_message_id": answer.ID, "blocking": escalation.Blocking, "status": map[bool]model.TaskStatus{true: model.TaskQueued, false: current.Status}[escalation.Blocking], "version": version}
	mergePayload(payload, idempotencyPayload("task_escalation_resolve", request.IdempotencyKey, request.IdempotencyHash))
	resolved := model.Event{ID: newID(now), TaskID: taskID, RunID: escalation.RunID, Kind: "task.escalation_answered", Actor: principal.ID, Message: "Escalation answered", Payload: payload, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, resolved); err != nil {
		return model.TaskEscalation{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.TaskEscalation{}, err
	}
	s.publish(resolved)
	return s.store.GetTaskEscalation(ctx, escalationID)
}

func containsExact(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
