package service

import (
	"context"
	"fmt"
	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
	"strings"
	"time"
)

func (s *Service) RegisterSessionBridgeFor(ctx context.Context, taskID string, request model.RegisterSessionBridgeRequest, principal Principal) (model.SessionBridge, error) {
	if !principal.Agent || !principal.HasCapability(CapabilityTaskSession) {
		return model.SessionBridge{}, ErrForbidden
	}
	taskID, request.RunID, request.Label = strings.TrimSpace(taskID), strings.TrimSpace(request.RunID), strings.TrimSpace(request.Label)
	if request.State != model.SessionBridgeAvailable && request.State != model.SessionBridgeUnavailable {
		return model.SessionBridge{}, fmt.Errorf("%w: state must be available or unavailable", ErrValidation)
	}
	if len(request.Label) > 200 || request.ExpiresAt.Before(time.Now().UTC()) || request.ExpiresAt.After(time.Now().UTC().Add(30*24*time.Hour)) {
		return model.SessionBridge{}, fmt.Errorf("%w: label is limited to 200 characters and expiry must be within 30 days", ErrValidation)
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.SessionBridge{}, err
	}
	if _, ok := activeOwnedRun(task, request.RunID, principal.ID); !ok {
		return model.SessionBridge{}, fmt.Errorf("%w: active owned run is required", ErrValidation)
	}
	now := time.Now().UTC()
	item := model.SessionBridge{RunID: request.RunID, TaskID: taskID, Controller: principal.ID, State: request.State, Label: request.Label, CanOpen: request.CanOpen, CanResume: request.CanResume, UpdatedAt: now, ExpiresAt: request.ExpiresAt}
	if request.State != model.SessionBridgeAvailable {
		item.CanOpen = false
		item.CanResume = false
	}
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return item, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = store.UpsertSessionBridge(ctx, tx, item); err != nil {
		return item, err
	}
	event := model.Event{ID: newID(now), TaskID: taskID, RunID: request.RunID, Kind: "task.session_bridge_updated", Actor: principal.ID, Message: "Session bridge availability updated", Payload: map[string]any{"state": item.State, "can_open": item.CanOpen, "can_resume": item.CanResume, "expires_at": item.ExpiresAt}, CreatedAt: now}
	if err = store.InsertEvent(ctx, tx, event); err != nil {
		return item, err
	}
	if err = tx.Commit(); err == nil {
		s.publish(event)
	}
	return item, err
}
func (s *Service) ListTaskSessionBridgesFor(ctx context.Context, taskID string, principal Principal) ([]model.SessionBridge, error) {
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return nil, err
	}
	if principal.Agent && (!principal.HasCapability(CapabilityTaskSession) || !canMutate(task, principal)) {
		return nil, ErrForbidden
	}
	return s.store.ListTaskSessionBridges(ctx, task.ID)
}
func (s *Service) CreateSessionBridgeRequestFor(ctx context.Context, taskID string, request model.CreateSessionBridgeRequest, principal Principal) (model.SessionBridgeRequest, error) {
	if principal.Agent || !principal.Can(PermissionTaskWrite) {
		return model.SessionBridgeRequest{}, ErrForbidden
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.SessionBridgeRequest{}, err
	}
	if !canMutate(task, principal) {
		return model.SessionBridgeRequest{}, ErrForbidden
	}
	bridge, err := s.store.GetSessionBridge(ctx, strings.TrimSpace(request.RunID))
	if err != nil || bridge.TaskID != task.ID {
		return model.SessionBridgeRequest{}, store.ErrNotFound
	}
	if bridge.State != model.SessionBridgeAvailable {
		return model.SessionBridgeRequest{}, fmt.Errorf("%w: session bridge is %s", ErrValidation, bridge.State)
	}
	if request.Action != model.SessionActionOpen && request.Action != model.SessionActionResume {
		return model.SessionBridgeRequest{}, fmt.Errorf("%w: action must be open or resume", ErrValidation)
	}
	if request.Action == model.SessionActionOpen && !bridge.CanOpen || request.Action == model.SessionActionResume && !bridge.CanResume {
		return model.SessionBridgeRequest{}, fmt.Errorf("%w: requested session action is unavailable", ErrValidation)
	}
	now := time.Now().UTC()
	item := model.SessionBridgeRequest{ID: newID(now), TaskID: task.ID, RunID: bridge.RunID, Controller: bridge.Controller, Action: request.Action, Status: model.SessionRequestRequested, RequestedBy: principal.ID, CreatedAt: now, UpdatedAt: now}
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return item, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = store.InsertSessionBridgeRequest(ctx, tx, item); err != nil {
		return item, err
	}
	event := model.Event{ID: newID(now), TaskID: task.ID, RunID: bridge.RunID, Kind: "task.session_requested", Actor: principal.ID, Message: "Controller session action requested", Payload: map[string]any{"request_id": item.ID, "action": item.Action}, CreatedAt: now}
	if err = store.InsertEvent(ctx, tx, event); err != nil {
		return item, err
	}
	if err = tx.Commit(); err == nil {
		s.publish(event)
	}
	return item, err
}
func (s *Service) ListSessionBridgeRequestsFor(ctx context.Context, principal Principal) ([]model.SessionBridgeRequest, error) {
	if !principal.Agent || !principal.HasCapability(CapabilityTaskSession) {
		return nil, ErrForbidden
	}
	return s.store.ListSessionBridgeRequests(ctx, principal.ID, false)
}
func (s *Service) UpdateSessionBridgeRequestFor(ctx context.Context, id string, request model.UpdateSessionBridgeRequest, principal Principal) (model.SessionBridgeRequest, error) {
	if !principal.Agent || !principal.HasCapability(CapabilityTaskSession) {
		return model.SessionBridgeRequest{}, ErrForbidden
	}
	item, err := s.store.GetSessionBridgeRequest(ctx, strings.TrimSpace(id))
	if err != nil {
		return item, err
	}
	if item.Controller != principal.ID {
		return item, ErrForbidden
	}
	valid := item.Status == model.SessionRequestRequested && request.Status == model.SessionRequestAcknowledged || item.Status == model.SessionRequestAcknowledged && (request.Status == model.SessionRequestCompleted || request.Status == model.SessionRequestRejected)
	if !valid {
		return item, fmt.Errorf("%w: invalid session request transition", ErrValidation)
	}
	request.OutcomeNote = strings.TrimSpace(request.OutcomeNote)
	if len(request.OutcomeNote) > 1000 {
		return item, fmt.Errorf("%w: outcome note is limited to 1000 characters", ErrValidation)
	}
	now := time.Now().UTC()
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return item, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE session_bridge_requests SET status=?,outcome_note=?,updated_at=? WHERE id=? AND controller=? AND status=?`, request.Status, request.OutcomeNote, stamp(now), item.ID, principal.ID, item.Status)
	if err != nil {
		return item, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return item, ErrConflict
	}
	event := model.Event{ID: newID(now), TaskID: item.TaskID, RunID: item.RunID, Kind: "task.session_request_updated", Actor: principal.ID, Message: "Controller session request updated", Payload: map[string]any{"request_id": item.ID, "action": item.Action, "status": request.Status}, CreatedAt: now}
	if err = store.InsertEvent(ctx, tx, event); err != nil {
		return item, err
	}
	if err = tx.Commit(); err != nil {
		return item, err
	}
	updated, err := s.store.GetSessionBridgeRequest(ctx, item.ID)
	if err == nil {
		s.publish(event)
	}
	return updated, err
}
