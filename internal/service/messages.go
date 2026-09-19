package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
)

func (s *Service) ListMessagesFor(ctx context.Context, taskID, before string, limit int, principal Principal) ([]model.TaskMessage, error) {
	if principal.Agent && !principal.HasCapability(CapabilityTaskMessage) {
		return nil, ErrForbidden
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return nil, err
	}
	if principal.Agent && !canMutate(task, principal) {
		return nil, ErrForbidden
	}
	return s.store.ListTaskMessages(ctx, task.ID, strings.TrimSpace(before), limit)
}

func (s *Service) AddMessageFor(ctx context.Context, taskID string, request model.AddMessageRequest, principal Principal) (model.TaskMessage, error) {
	if !principal.Can(PermissionTaskMessage) {
		return model.TaskMessage{}, ErrForbidden
	}
	request.AuthorRunID = strings.TrimSpace(request.AuthorRunID)
	request.TargetRunID = strings.TrimSpace(request.TargetRunID)
	request.ReplyToID = strings.TrimSpace(request.ReplyToID)
	request.SupersedesID = strings.TrimSpace(request.SupersedesID)
	request.Body = strings.TrimSpace(request.Body)
	if !model.IsMessageKind(request.Kind) {
		return model.TaskMessage{}, fmt.Errorf("%w: unknown message kind", ErrValidation)
	}
	if request.Body == "" || len(request.Body) > 4000 {
		return model.TaskMessage{}, fmt.Errorf("%w: message body must contain 1-4000 characters", ErrValidation)
	}
	if principal.Agent {
		if request.AuthorRunID == "" || request.TargetRunID != "" || request.RequiresAck || request.Kind == model.MessageInstruction {
			return model.TaskMessage{}, fmt.Errorf("%w: agent messages require author_run_id and cannot target a run, require acknowledgement, or issue instructions", ErrValidation)
		}
	} else if request.AuthorRunID != "" {
		return model.TaskMessage{}, fmt.Errorf("%w: human messages cannot claim an author run", ErrValidation)
	}
	if request.Kind == model.MessageInstruction {
		request.RequiresAck = true
	}

	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.TaskMessage{}, err
	}
	if principal.Agent && !canMutate(task, principal) {
		return model.TaskMessage{}, ErrForbidden
	}
	if principal.Agent {
		run, ok := activeOwnedRun(task, request.AuthorRunID, principal.ID)
		if !ok || run.ID == "" {
			return model.TaskMessage{}, fmt.Errorf("%w: author run is not active for this agent", ErrValidation)
		}
	} else if request.TargetRunID != "" {
		if _, ok := activeTaskRun(task, request.TargetRunID); !ok {
			return model.TaskMessage{}, fmt.Errorf("%w: target run is not active", ErrValidation)
		}
	}

	if principal.Agent || request.IdempotencyKey != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
	}
	hash, err := requestHash(struct {
		TaskID string
		model.AddMessageRequest
	}{task.ID, request})
	if err != nil {
		return model.TaskMessage{}, err
	}
	request.IdempotencyHash = hash
	event, replay, err := s.replayIdempotency(ctx, principal, "task_message_add", request.IdempotencyKey, hash)
	if err != nil {
		return model.TaskMessage{}, err
	}
	if replay {
		messageID, _ := event.Payload["message_id"].(string)
		return s.store.GetTaskMessage(ctx, messageID)
	}

	now := time.Now().UTC()
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.TaskMessage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	for name, id := range map[string]string{"reply_to_id": request.ReplyToID, "supersedes_id": request.SupersedesID} {
		if id == "" {
			continue
		}
		linked, err := store.LoadTaskMessage(ctx, tx, id)
		if err != nil || linked.TaskID != task.ID {
			return model.TaskMessage{}, fmt.Errorf("%w: %s does not reference this task", ErrValidation, name)
		}
		if name == "supersedes_id" && linked.Author != principal.ID {
			return model.TaskMessage{}, fmt.Errorf("%w: a message can only supersede the same author's message", ErrValidation)
		}
	}
	message := model.TaskMessage{
		ID: newID(now), TaskID: task.ID, Author: principal.ID, AuthorRunID: request.AuthorRunID, TargetRunID: request.TargetRunID,
		Kind: request.Kind, Body: request.Body, ReplyToID: request.ReplyToID, SupersedesID: request.SupersedesID, RequiresAck: request.RequiresAck, CreatedAt: now,
	}
	if err := store.InsertTaskMessage(ctx, tx, message); err != nil {
		if request.SupersedesID != "" && strings.Contains(strings.ToLower(err.Error()), "unique") {
			return model.TaskMessage{}, fmt.Errorf("%w: message was already superseded", ErrConflict)
		}
		return model.TaskMessage{}, err
	}
	payload := map[string]any{
		"message_id": message.ID, "kind": message.Kind, "target_run_id": message.TargetRunID, "requires_ack": message.RequiresAck,
	}
	mergePayload(payload, idempotencyPayload("task_message_add", request.IdempotencyKey, request.IdempotencyHash))
	created := model.Event{ID: newID(now), TaskID: task.ID, RunID: request.AuthorRunID, Kind: "task.message_added", Actor: principal.ID, Message: "Task message added", Payload: payload, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, created); err != nil {
		return model.TaskMessage{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.TaskMessage{}, err
	}
	s.publish(created)
	return message, nil
}

func (s *Service) RecordMessageReceiptsFor(ctx context.Context, taskID, runID string, request model.MessageReceiptRequest, principal Principal) ([]model.TaskMessage, error) {
	if !principal.Agent || !principal.HasCapability(CapabilityTaskMessage) {
		return nil, ErrForbidden
	}
	taskID, runID = strings.TrimSpace(taskID), strings.TrimSpace(runID)
	observed := unique(request.ObservedMessageIDs)
	acknowledged := unique(request.AcknowledgedMessageIDs)
	if len(observed)+len(acknowledged) == 0 || len(observed)+len(acknowledged) > 100 {
		return nil, fmt.Errorf("%w: 1-100 message receipt IDs are required", ErrValidation)
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return nil, err
	}
	if _, ok := activeOwnedRun(task, runID, principal.ID); !ok {
		return nil, fmt.Errorf("%w: run is not active for this agent", ErrValidation)
	}

	s.idempotencyMu.Lock()
	defer s.idempotencyMu.Unlock()
	hash, err := requestHash(struct {
		TaskID string
		RunID  string
		model.MessageReceiptRequest
	}{taskID, runID, request})
	if err != nil {
		return nil, err
	}
	request.IdempotencyHash = hash
	_, replay, err := s.replayIdempotency(ctx, principal, "task_message_receipt", request.IdempotencyKey, hash)
	if err != nil {
		return nil, err
	}
	if replay {
		return s.store.ListPendingTaskMessages(ctx, taskID, runID, principal.ID, 50)
	}

	now := time.Now().UTC()
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	ackSet := make(map[string]bool, len(acknowledged))
	for _, id := range acknowledged {
		ackSet[id] = true
	}
	all := unique(append(append([]string{}, observed...), acknowledged...))
	for _, id := range all {
		message, err := store.LoadTaskMessage(ctx, tx, id)
		if err != nil || message.TaskID != taskID || message.Author == principal.ID || message.TargetRunID != "" && message.TargetRunID != runID {
			return nil, fmt.Errorf("%w: message %q is not receivable by this run", ErrValidation, id)
		}
		if err := store.RecordMessageReceipt(ctx, tx, id, runID, principal.ID, ackSet[id], now); err != nil {
			return nil, err
		}
	}
	payload := map[string]any{"observed_message_ids": observed, "acknowledged_message_ids": acknowledged}
	mergePayload(payload, idempotencyPayload("task_message_receipt", request.IdempotencyKey, request.IdempotencyHash))
	received := model.Event{ID: newID(now), TaskID: taskID, RunID: runID, Kind: "task.messages_received", Actor: principal.ID, Message: "Task messages received", Payload: payload, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, received); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.publish(received)
	return s.store.ListPendingTaskMessages(ctx, taskID, runID, principal.ID, 50)
}

func activeTaskRun(task model.Task, runID string) (model.AgentRun, bool) {
	for _, run := range task.Runs {
		if run.ID == runID && run.Status == model.TaskActive && run.EndedAt == nil {
			return run, true
		}
	}
	return model.AgentRun{}, false
}

func activeOwnedRun(task model.Task, runID, principal string) (model.AgentRun, bool) {
	run, ok := activeTaskRun(task, runID)
	return run, ok && run.Agent == principal
}
