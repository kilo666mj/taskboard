package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
)

const runControlLifetime = 24 * time.Hour

type ReviewRequeueResult struct {
	Task    model.Task
	Control model.RunControlRequest
}

func (s *Service) ReviewAndRequeueFor(ctx context.Context, taskID string, request model.ReviewRequeueRequest, principal Principal) (ReviewRequeueResult, error) {
	if principal.Agent || principal.Role != RoleOwner && principal.Role != RoleAdmin {
		return ReviewRequeueResult{}, ErrForbidden
	}
	taskID, request.TargetRunID, request.ReviewNote = strings.TrimSpace(taskID), strings.TrimSpace(request.TargetRunID), strings.TrimSpace(request.ReviewNote)
	if taskID == "" || request.TargetRunID == "" || request.ExpectedVersion < 1 || request.ReviewNote == "" {
		return ReviewRequeueResult{}, fmt.Errorf("%w: task_id, target_run_id, expected_version, and review_note are required", ErrValidation)
	}
	if len(request.ReviewNote) > 1000 {
		return ReviewRequeueResult{}, fmt.Errorf("%w: review_note is limited to 1000 characters", ErrValidation)
	}
	if _, err := s.GetFor(ctx, taskID, principal); err != nil {
		return ReviewRequeueResult{}, err
	}

	now := time.Now().UTC()
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return ReviewRequeueResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := store.LoadTask(ctx, tx, taskID)
	if err != nil {
		return ReviewRequeueResult{}, err
	}
	if current.Version != request.ExpectedVersion {
		return ReviewRequeueResult{}, ErrConflict
	}
	if current.Status != model.TaskStale {
		return ReviewRequeueResult{}, fmt.Errorf("%w: only stale work can be reviewed and requeued", ErrConflict)
	}
	var runStatus model.TaskStatus
	var targetAgent, ended string
	if err := tx.QueryRowContext(ctx, `SELECT status,agent,COALESCE(ended_at,'') FROM agent_runs WHERE id=? AND task_id=?`, request.TargetRunID, taskID).Scan(&runStatus, &targetAgent, &ended); err != nil {
		if err == sql.ErrNoRows {
			return ReviewRequeueResult{}, fmt.Errorf("%w: stale run not found", ErrValidation)
		}
		return ReviewRequeueResult{}, err
	}
	if runStatus != model.TaskStale || ended == "" {
		return ReviewRequeueResult{}, fmt.Errorf("%w: target run is not the ended stale run", ErrConflict)
	}

	owner := current.Owner
	if current.Visibility == model.VisibilityAgent {
		owner = ""
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,owner=?,current_note=?,waiting_for='',blocker='',last_edited_by=?,version=version+1,updated_at=?,completed_at=NULL WHERE id=? AND version=? AND status=?`, model.TaskQueued, owner, clipped(request.ReviewNote, 1000), principal.ID, stamp(now), taskID, current.Version, model.TaskStale)
	if err := oneRow(result, err); err != nil {
		return ReviewRequeueResult{}, err
	}

	controlID := ""
	err = tx.QueryRowContext(ctx, `SELECT id FROM run_control_requests WHERE task_id=? AND target_run_id=? AND kind=? AND status IN (?,?,?) ORDER BY id DESC LIMIT 1`, taskID, request.TargetRunID, model.RunControlRetry, model.RunControlRequested, model.RunControlAcknowledged, model.RunControlAccepted).Scan(&controlID)
	switch {
	case err == nil:
		result, err = tx.ExecContext(ctx, `UPDATE run_control_requests SET status=?,outcome_note=?,updated_at=?,decided_at=?,completed_at=? WHERE id=? AND status IN (?,?,?)`, model.RunControlCompleted, request.ReviewNote, stamp(now), stamp(now), stamp(now), controlID, model.RunControlRequested, model.RunControlAcknowledged, model.RunControlAccepted)
		if err := oneRow(result, err); err != nil {
			return ReviewRequeueResult{}, err
		}
	case err == sql.ErrNoRows:
		stampNow := now
		control := model.RunControlRequest{ID: newID(now), TaskID: taskID, TargetRunID: request.TargetRunID, TargetAgent: targetAgent, Kind: model.RunControlRetry, Status: model.RunControlCompleted, RequestedBy: principal.ID, Reason: request.ReviewNote, OutcomeNote: request.ReviewNote, TaskVersion: current.Version, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(runControlLifetime), DecidedAt: &stampNow, CompletedAt: &stampNow}
		if err := store.InsertRunControl(ctx, tx, control); err != nil {
			return ReviewRequeueResult{}, err
		}
		controlID = control.ID
	default:
		return ReviewRequeueResult{}, err
	}

	event := model.Event{ID: newID(now), TaskID: taskID, RunID: request.TargetRunID, Kind: "task.reviewed_requeued", Actor: principal.ID, Message: request.ReviewNote, Payload: map[string]any{"control_id": controlID, "control_kind": model.RunControlRetry, "control_status": model.RunControlCompleted, "target_run_id": request.TargetRunID, "previous_last_edited_by": current.LastEditedBy, "last_edited_by": principal.ID, "version": current.Version + 1}, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, event); err != nil {
		return ReviewRequeueResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReviewRequeueResult{}, err
	}
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return ReviewRequeueResult{}, err
	}
	control, err := s.store.GetRunControl(ctx, controlID)
	if err != nil {
		return ReviewRequeueResult{}, err
	}
	s.publish(event)
	return ReviewRequeueResult{Task: task, Control: control}, nil
}

func (s *Service) ListTaskRunControlsFor(ctx context.Context, taskID string, principal Principal) ([]model.RunControlRequest, error) {
	if err := s.expireRunControls(ctx); err != nil {
		return nil, err
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return nil, err
	}
	if principal.Agent && (!principal.HasCapability(CapabilityTaskControl) || !canMutate(task, principal)) {
		return nil, ErrForbidden
	}
	items, err := s.store.ListTaskRunControls(ctx, task.ID)
	if err != nil || !principal.Agent {
		return items, err
	}
	filtered := items[:0]
	for _, item := range items {
		if item.TargetAgent == principal.ID {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

func (s *Service) ListPendingRunControlsFor(ctx context.Context, limit int, principal Principal) ([]model.RunControlRequest, error) {
	if !principal.Agent || !principal.HasCapability(CapabilityTaskControl) {
		return nil, ErrForbidden
	}
	if err := s.expireRunControls(ctx); err != nil {
		return nil, err
	}
	return s.store.ListAgentRunControls(ctx, principal.ID, limit)
}

func (s *Service) CreateRunControlFor(ctx context.Context, taskID string, request model.CreateRunControlRequest, principal Principal) (model.RunControlRequest, error) {
	if principal.Agent || !principal.Can(PermissionTaskWrite) {
		return model.RunControlRequest{}, ErrForbidden
	}
	taskID, request.TargetRunID = strings.TrimSpace(taskID), strings.TrimSpace(request.TargetRunID)
	request.Reason = strings.TrimSpace(request.Reason)
	if taskID == "" || request.TargetRunID == "" || request.ExpectedVersion < 1 || !isRunControlKind(request.Kind) {
		return model.RunControlRequest{}, fmt.Errorf("%w: task_id, target_run_id, expected_version, and a valid control kind are required", ErrValidation)
	}
	if len(request.Reason) > 1000 {
		return model.RunControlRequest{}, fmt.Errorf("%w: control reason is limited to 1000 characters", ErrValidation)
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.RunControlRequest{}, err
	}
	if !canMutate(task, principal) {
		return model.RunControlRequest{}, ErrForbidden
	}
	run, ok := taskRun(task, request.TargetRunID)
	if !ok || !validControlTarget(task, run, request.Kind) {
		return model.RunControlRequest{}, fmt.Errorf("%w: control does not match the target task and run state", ErrValidation)
	}
	if request.IdempotencyKey != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
	}
	hash, err := requestHash(struct {
		TaskID string
		model.CreateRunControlRequest
	}{taskID, request})
	if err != nil {
		return model.RunControlRequest{}, err
	}
	request.IdempotencyHash = hash
	event, replay, err := s.replayIdempotency(ctx, principal, "run_control_create", request.IdempotencyKey, hash)
	if err != nil {
		return model.RunControlRequest{}, err
	}
	if replay {
		id, _ := event.Payload["control_id"].(string)
		return s.store.GetRunControl(ctx, id)
	}

	now := time.Now().UTC()
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.RunControlRequest{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := store.LoadTask(ctx, tx, taskID)
	if err != nil {
		return model.RunControlRequest{}, err
	}
	if current.Version != request.ExpectedVersion {
		return model.RunControlRequest{}, ErrConflict
	}
	var duplicate int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_control_requests WHERE task_id=? AND target_run_id=? AND kind=? AND status IN (?,?,?)`, taskID, request.TargetRunID, request.Kind, model.RunControlRequested, model.RunControlAcknowledged, model.RunControlAccepted).Scan(&duplicate); err != nil {
		return model.RunControlRequest{}, err
	}
	if duplicate > 0 {
		return model.RunControlRequest{}, fmt.Errorf("%w: an open control of this kind already targets the run", ErrConflict)
	}
	item := model.RunControlRequest{ID: newID(now), TaskID: taskID, TargetRunID: run.ID, TargetAgent: run.Agent, Kind: request.Kind, Status: model.RunControlRequested, RequestedBy: principal.ID, Reason: request.Reason, TaskVersion: current.Version, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(runControlLifetime)}
	if err := store.InsertRunControl(ctx, tx, item); err != nil {
		return model.RunControlRequest{}, err
	}
	payload := map[string]any{"control_id": item.ID, "control_kind": item.Kind, "control_status": item.Status, "target_run_id": item.TargetRunID, "version": current.Version}
	mergePayload(payload, idempotencyPayload("run_control_create", request.IdempotencyKey, request.IdempotencyHash))
	created := model.Event{ID: newID(now), TaskID: taskID, RunID: run.ID, Kind: "task.control_requested", Actor: principal.ID, Message: "Run control requested", Payload: payload, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, created); err != nil {
		return model.RunControlRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.RunControlRequest{}, err
	}
	s.publish(created)
	return item, nil
}

func (s *Service) UpdateRunControlFor(ctx context.Context, controlID string, request model.UpdateRunControlRequest, principal Principal) (model.RunControlRequest, error) {
	if !principal.Agent || !principal.HasCapability(CapabilityTaskControl) {
		return model.RunControlRequest{}, ErrForbidden
	}
	controlID, request.OutcomeNote = strings.TrimSpace(controlID), strings.TrimSpace(request.OutcomeNote)
	if controlID == "" || len(request.OutcomeNote) > 1000 {
		return model.RunControlRequest{}, fmt.Errorf("%w: control_id is required and outcome_note is limited to 1000 characters", ErrValidation)
	}
	if request.Status == model.RunControlRejected && request.OutcomeNote == "" {
		return model.RunControlRequest{}, fmt.Errorf("%w: rejected controls require an outcome note", ErrValidation)
	}
	if request.Status == model.RunControlCompleted && request.ExpectedVersion < 1 {
		return model.RunControlRequest{}, fmt.Errorf("%w: completing a control requires expected_version", ErrValidation)
	}
	s.idempotencyMu.Lock()
	defer s.idempotencyMu.Unlock()
	hash, err := requestHash(struct {
		ControlID string
		model.UpdateRunControlRequest
	}{controlID, request})
	if err != nil {
		return model.RunControlRequest{}, err
	}
	request.IdempotencyHash = hash
	event, replay, err := s.replayIdempotency(ctx, principal, "run_control_update", request.IdempotencyKey, hash)
	if err != nil {
		return model.RunControlRequest{}, err
	}
	if replay {
		id, _ := event.Payload["control_id"].(string)
		return s.store.GetRunControl(ctx, id)
	}

	now := time.Now().UTC()
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.RunControlRequest{}, err
	}
	defer func() { _ = tx.Rollback() }()
	item, err := store.LoadRunControl(ctx, tx, controlID)
	if err != nil {
		return model.RunControlRequest{}, err
	}
	if item.TargetAgent != principal.ID {
		return model.RunControlRequest{}, ErrForbidden
	}
	if !validControlTransition(item.Status, request.Status) {
		return model.RunControlRequest{}, fmt.Errorf("%w: invalid control transition from %s to %s", ErrValidation, item.Status, request.Status)
	}
	if request.Status == model.RunControlExpired && now.Before(item.ExpiresAt) {
		return model.RunControlRequest{}, fmt.Errorf("%w: control has not expired", ErrValidation)
	}
	if request.Status != model.RunControlExpired && !now.Before(item.ExpiresAt) {
		return model.RunControlRequest{}, fmt.Errorf("%w: control has expired", ErrConflict)
	}
	current, err := store.LoadTask(ctx, tx, item.TaskID)
	if err != nil {
		return model.RunControlRequest{}, err
	}
	version := current.Version
	if request.Status == model.RunControlCompleted {
		if current.Version != request.ExpectedVersion {
			return model.RunControlRequest{}, ErrConflict
		}
		if err := completeRunControl(ctx, tx, current, item, request.OutcomeNote, now); err != nil {
			return model.RunControlRequest{}, err
		}
		version++
	}
	acknowledged, decided, completed := "acknowledged_at", "decided_at", "completed_at"
	setColumn := map[model.RunControlStatus]string{model.RunControlAcknowledged: acknowledged, model.RunControlAccepted: decided, model.RunControlRejected: decided, model.RunControlCompleted: completed, model.RunControlExpired: decided}[request.Status]
	result, err := tx.ExecContext(ctx, `UPDATE run_control_requests SET status=?,outcome_note=?,updated_at=?,`+setColumn+`=? WHERE id=? AND status=?`, request.Status, request.OutcomeNote, stamp(now), stamp(now), item.ID, item.Status)
	if err != nil {
		return model.RunControlRequest{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return model.RunControlRequest{}, ErrConflict
	}
	payload := map[string]any{"control_id": item.ID, "control_kind": item.Kind, "control_status": request.Status, "target_run_id": item.TargetRunID, "version": version}
	mergePayload(payload, idempotencyPayload("run_control_update", request.IdempotencyKey, request.IdempotencyHash))
	updated := model.Event{ID: newID(now), TaskID: item.TaskID, RunID: item.TargetRunID, Kind: "task.control_updated", Actor: principal.ID, Message: "Run control " + string(request.Status), Payload: payload, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, updated); err != nil {
		return model.RunControlRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.RunControlRequest{}, err
	}
	s.publish(updated)
	return s.store.GetRunControl(ctx, item.ID)
}

func completeRunControl(ctx context.Context, tx *store.Tx, task model.Task, item model.RunControlRequest, note string, now time.Time) error {
	var runStatus model.TaskStatus
	var ended string
	if err := tx.QueryRowContext(ctx, `SELECT status,COALESCE(ended_at,'') FROM agent_runs WHERE id=? AND task_id=? AND agent=?`, item.TargetRunID, item.TaskID, item.TargetAgent).Scan(&runStatus, &ended); err != nil {
		return err
	}
	owner := task.Owner
	if task.Visibility == model.VisibilityAgent {
		owner = ""
	}
	switch item.Kind {
	case model.RunControlPause:
		if task.Status != model.TaskActive || runStatus != model.TaskActive || ended != "" {
			return fmt.Errorf("%w: pause target is no longer active", ErrConflict)
		}
		waitingFor := "Paused by request"
		if note != "" {
			waitingFor = clipped(note, 1000)
		}
		result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status=?,lease_expires_at=?,last_heartbeat_at=?,ended_at=? WHERE id=? AND status=? AND ended_at IS NULL`, model.TaskWaiting, stamp(now), stamp(now), stamp(now), item.TargetRunID, model.TaskActive)
		if err != nil {
			return err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return ErrConflict
		}
		result, err = tx.ExecContext(ctx, `UPDATE tasks SET status=?,waiting_for=?,version=version+1,updated_at=? WHERE id=? AND version=? AND status=?`, model.TaskWaiting, waitingFor, stamp(now), task.ID, task.Version, model.TaskActive)
		return oneRow(result, err)
	case model.RunControlCancel:
		if task.Status != model.TaskActive || runStatus != model.TaskActive || ended != "" {
			return fmt.Errorf("%w: cancel target is no longer active", ErrConflict)
		}
		result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status=?,lease_expires_at=?,last_heartbeat_at=?,ended_at=? WHERE id=? AND status=? AND ended_at IS NULL`, model.TaskCancelled, stamp(now), stamp(now), stamp(now), item.TargetRunID, model.TaskActive)
		if err != nil {
			return err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return ErrConflict
		}
		result, err = tx.ExecContext(ctx, `UPDATE tasks SET status=?,current_note=?,waiting_for='',blocker='',version=version+1,updated_at=?,completed_at=? WHERE id=? AND version=? AND status=?`, model.TaskCancelled, clipped(note, 1000), stamp(now), stamp(now), task.ID, task.Version, model.TaskActive)
		return oneRow(result, err)
	case model.RunControlResume:
		if task.Status != model.TaskWaiting && task.Status != model.TaskBlocked || ended == "" {
			return fmt.Errorf("%w: resume target is no longer waiting or blocked", ErrConflict)
		}
		result, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,owner=?,current_note=?,waiting_for='',blocker='',version=version+1,updated_at=?,completed_at=NULL WHERE id=? AND version=?`, model.TaskQueued, owner, clipped(note, 1000), stamp(now), task.ID, task.Version)
		return oneRow(result, err)
	case model.RunControlRetry:
		if task.Status != model.TaskStale && task.Status != model.TaskCancelled || ended == "" {
			return fmt.Errorf("%w: retry target is no longer stale or cancelled", ErrConflict)
		}
		result, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,owner=?,current_note=?,waiting_for='',blocker='',version=version+1,updated_at=?,completed_at=NULL WHERE id=? AND version=?`, model.TaskQueued, owner, clipped(note, 1000), stamp(now), task.ID, task.Version)
		return oneRow(result, err)
	default:
		return fmt.Errorf("%w: unknown control kind", ErrValidation)
	}
}

func oneRow(result interface{ RowsAffected() (int64, error) }, err error) error {
	if err != nil {
		return err
	}
	count, countErr := result.RowsAffected()
	if countErr != nil {
		return countErr
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Service) expireRunControls(ctx context.Context) error {
	now := time.Now().UTC()
	items, err := s.store.ListAllRunControls(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if now.Before(item.ExpiresAt) || item.Status == model.RunControlCompleted || item.Status == model.RunControlRejected || item.Status == model.RunControlExpired {
			continue
		}
		tx, err := s.store.DB().BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE run_control_requests SET status=?,updated_at=?,decided_at=? WHERE id=? AND status IN (?,?,?) AND expires_at<=?`, model.RunControlExpired, stamp(now), stamp(now), item.ID, model.RunControlRequested, model.RunControlAcknowledged, model.RunControlAccepted, stamp(now))
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if count, _ := result.RowsAffected(); count == 1 {
			event := model.Event{ID: newID(now), TaskID: item.TaskID, RunID: item.TargetRunID, Kind: "task.control_updated", Actor: "taskboard", Message: "Run control expired", Payload: map[string]any{"control_id": item.ID, "control_kind": item.Kind, "control_status": model.RunControlExpired}, CreatedAt: now}
			if err := store.InsertEvent(ctx, tx, event); err != nil {
				_ = tx.Rollback()
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			s.publish(event)
		} else {
			_ = tx.Rollback()
		}
	}
	return nil
}

func isRunControlKind(kind model.RunControlKind) bool {
	return kind == model.RunControlPause || kind == model.RunControlCancel || kind == model.RunControlResume || kind == model.RunControlRetry
}

func validControlTransition(from, to model.RunControlStatus) bool {
	return from == model.RunControlRequested && (to == model.RunControlAcknowledged || to == model.RunControlExpired) ||
		from == model.RunControlAcknowledged && (to == model.RunControlAccepted || to == model.RunControlRejected || to == model.RunControlExpired) ||
		from == model.RunControlAccepted && (to == model.RunControlCompleted || to == model.RunControlExpired)
}

func validControlTarget(task model.Task, run model.AgentRun, kind model.RunControlKind) bool {
	switch kind {
	case model.RunControlPause, model.RunControlCancel:
		return task.Status == model.TaskActive && run.Status == model.TaskActive && run.EndedAt == nil
	case model.RunControlResume:
		return (task.Status == model.TaskWaiting || task.Status == model.TaskBlocked) && run.EndedAt != nil
	case model.RunControlRetry:
		return (task.Status == model.TaskStale || task.Status == model.TaskCancelled) && run.EndedAt != nil
	default:
		return false
	}
}

func taskRun(task model.Task, runID string) (model.AgentRun, bool) {
	for _, run := range task.Runs {
		if run.ID == runID {
			return run, true
		}
	}
	return model.AgentRun{}, false
}
