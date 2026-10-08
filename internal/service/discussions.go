package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
)

// DiscussionCapability is the worker-advertisement token a controller uses to
// say it accepts live discussions on the tasks it owns.
const DiscussionCapability = "discussion"

// DiscussionIdle ends a discussion after this long without activity.
const DiscussionIdle = 30 * time.Minute

const discussionMessageLimit = 200

// discussionController returns the agent principal that owns task when it
// currently advertises discussion support.
func (s *Service) discussionController(ctx context.Context, task model.Task) (string, bool) {
	if task.Owner == "" || task.Status == model.TaskDone || task.Status == model.TaskCancelled {
		return "", false
	}
	advertisement, err := s.store.GetWorkerAdvertisement(ctx, task.Owner)
	if err != nil || !slices.Contains(advertisement.Capabilities, DiscussionCapability) {
		return "", false
	}
	return task.Owner, true
}

// MarkDiscussableFor sets Discussable on tasks whose owning controller
// accepts live discussions. It applies only to people who may write.
func (s *Service) MarkDiscussableFor(ctx context.Context, tasks []model.Task, principal Principal) {
	if principal.Agent || !principal.Can(PermissionTaskWrite) {
		return
	}
	for index := range tasks {
		_, tasks[index].Discussable = s.discussionController(ctx, tasks[index])
	}
}

// StartDiscussionFor asks the task's controller for a live discussion, or
// returns the one already open. An optional first message is added to it.
func (s *Service) StartDiscussionFor(ctx context.Context, taskID string, request model.StartDiscussionRequest, principal Principal) (model.TaskDiscussion, error) {
	if principal.Agent || !principal.Can(PermissionTaskWrite) {
		return model.TaskDiscussion{}, ErrForbidden
	}
	message := strings.TrimSpace(request.Message)
	if len(message) > 4000 {
		return model.TaskDiscussion{}, fmt.Errorf("%w: messages are limited to 4000 characters", ErrValidation)
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.TaskDiscussion{}, err
	}
	controller, ok := s.discussionController(ctx, task)
	if !ok {
		return model.TaskDiscussion{}, fmt.Errorf("%w: this task's controller is not accepting discussions", ErrValidation)
	}
	existing, err := s.store.ListTaskDiscussions(ctx, task.ID, 5)
	if err != nil {
		return model.TaskDiscussion{}, err
	}
	existing, err = s.expireIdleDiscussions(ctx, existing)
	if err != nil {
		return model.TaskDiscussion{}, err
	}
	for _, item := range existing {
		if item.Status != model.DiscussionEnded {
			if message != "" {
				if _, err := s.addDiscussionMessage(ctx, item, principal, "person", message); err != nil {
					return model.TaskDiscussion{}, err
				}
			}
			return s.discussionWithMessages(ctx, item.ID, "")
		}
	}
	now := time.Now().UTC()
	item := model.TaskDiscussion{ID: newID(now), TaskID: task.ID, Controller: controller, Status: model.DiscussionRequested,
		RequestedBy: principal.ID, CreatedAt: now, LastActivityAt: now}
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.TaskDiscussion{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := store.InsertDiscussion(ctx, tx, item); err != nil {
		return model.TaskDiscussion{}, err
	}
	if message != "" {
		if err := store.InsertDiscussionMessage(ctx, tx, model.DiscussionMessage{ID: newID(now), DiscussionID: item.ID, TaskID: task.ID,
			Author: principal.ID, Role: "person", Body: message, CreatedAt: now}); err != nil {
			return model.TaskDiscussion{}, err
		}
	}
	event := discussionEvent(item, "task.discussion_started", principal.ID, "Live discussion requested", now)
	if err := store.InsertEvent(ctx, tx, event); err != nil {
		return model.TaskDiscussion{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.TaskDiscussion{}, err
	}
	s.publish(event)
	return s.discussionWithMessages(ctx, item.ID, "")
}

// ListTaskDiscussionsFor returns a task's recent discussions with messages.
func (s *Service) ListTaskDiscussionsFor(ctx context.Context, taskID string, principal Principal) ([]model.TaskDiscussion, error) {
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return nil, err
	}
	items, err := s.store.ListTaskDiscussions(ctx, task.ID, 5)
	if err != nil {
		return nil, err
	}
	if items, err = s.expireIdleDiscussions(ctx, items); err != nil {
		return nil, err
	}
	for index := range items {
		messages, err := s.store.ListDiscussionMessages(ctx, items[index].ID, "", discussionMessageLimit)
		if err != nil {
			return nil, err
		}
		items[index].Messages = messages
	}
	return items, nil
}

// ListControllerDiscussionsFor returns requested and active discussions
// addressed to the calling controller.
func (s *Service) ListControllerDiscussionsFor(ctx context.Context, principal Principal) ([]model.TaskDiscussion, error) {
	if !principal.Agent {
		return nil, ErrForbidden
	}
	items, err := s.store.ListControllerDiscussions(ctx, principal.ID, 100)
	if err != nil {
		return nil, err
	}
	items, err = s.expireIdleDiscussions(ctx, items)
	if err != nil {
		return nil, err
	}
	open := items[:0]
	for _, item := range items {
		if item.Status == model.DiscussionEnded {
			continue
		}
		task, err := s.GetFor(ctx, item.TaskID, principal)
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, ErrForbidden) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if controllerMayDiscuss(task, principal) {
			open = append(open, item)
		}
	}
	return open, nil
}

// GetDiscussionFor returns a discussion and its messages after the given
// message ID to its controller or to people who can see the task.
func (s *Service) GetDiscussionFor(ctx context.Context, discussionID, after string, principal Principal) (model.TaskDiscussion, error) {
	item, err := s.authorizedDiscussion(ctx, discussionID, principal)
	if err != nil {
		return model.TaskDiscussion{}, err
	}
	expired, err := s.expireIdleDiscussions(ctx, []model.TaskDiscussion{item})
	if err != nil {
		return model.TaskDiscussion{}, err
	}
	item = expired[0]
	if item.Messages, err = s.store.ListDiscussionMessages(ctx, item.ID, strings.TrimSpace(after), discussionMessageLimit); err != nil {
		return model.TaskDiscussion{}, err
	}
	return item, nil
}

// AddDiscussionMessageFor appends a person's message or the controller's reply.
func (s *Service) AddDiscussionMessageFor(ctx context.Context, discussionID string, request model.DiscussionMessageRequest, principal Principal) (model.DiscussionMessage, error) {
	item, err := s.authorizedDiscussion(ctx, discussionID, principal)
	if err != nil {
		return model.DiscussionMessage{}, err
	}
	role := "person"
	if principal.Agent {
		role = "agent"
		if item.Status != model.DiscussionActive {
			return model.DiscussionMessage{}, fmt.Errorf("%w: accept the discussion before replying", ErrValidation)
		}
	} else if !principal.Can(PermissionTaskWrite) {
		return model.DiscussionMessage{}, ErrForbidden
	}
	body := strings.TrimSpace(request.Body)
	if body == "" || len(body) > 4000 {
		return model.DiscussionMessage{}, fmt.Errorf("%w: a 1-4000 character message is required", ErrValidation)
	}
	if request.IdempotencyKey != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
	}
	hash, err := requestHash(struct {
		DiscussionID string
		Body         string
	}{item.ID, body})
	if err != nil {
		return model.DiscussionMessage{}, err
	}
	event, replay, err := s.replayIdempotency(ctx, principal, "task_discussion_message", request.IdempotencyKey, hash)
	if err != nil {
		return model.DiscussionMessage{}, err
	}
	if replay {
		messages, err := s.store.ListDiscussionMessages(ctx, item.ID, "", discussionMessageLimit)
		if err != nil {
			return model.DiscussionMessage{}, err
		}
		id, _ := event.Payload["message_id"].(string)
		for _, message := range messages {
			if message.ID == id {
				return message, nil
			}
		}
		return model.DiscussionMessage{}, store.ErrNotFound
	}
	return s.addDiscussionMessage(ctx, item, principal, role, body, idempotencyPayload("task_discussion_message", request.IdempotencyKey, hash))
}

func (s *Service) addDiscussionMessage(ctx context.Context, item model.TaskDiscussion, principal Principal, role, body string, extra ...map[string]any) (model.DiscussionMessage, error) {
	now := time.Now().UTC()
	if item.Status == model.DiscussionEnded || now.Sub(item.LastActivityAt) >= DiscussionIdle {
		return model.DiscussionMessage{}, fmt.Errorf("%w: this discussion has ended", ErrConflict)
	}
	message := model.DiscussionMessage{ID: newID(now), DiscussionID: item.ID, TaskID: item.TaskID, Author: principal.ID, Role: role, Body: body, CreatedAt: now}
	item.LastActivityAt = now
	if role == "agent" {
		item.AgentStatus = "ready"
	}
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.DiscussionMessage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := store.InsertDiscussionMessage(ctx, tx, message); err != nil {
		return model.DiscussionMessage{}, err
	}
	if err := store.UpdateDiscussion(ctx, tx, item); err != nil {
		return model.DiscussionMessage{}, err
	}
	event := discussionEvent(item, "task.discussion_message", principal.ID, "Discussion message", now)
	event.Payload["message_id"] = message.ID
	event.Payload["role"] = role
	for _, payload := range extra {
		mergePayload(event.Payload, payload)
	}
	if err := store.InsertEvent(ctx, tx, event); err != nil {
		return model.DiscussionMessage{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.DiscussionMessage{}, err
	}
	s.publish(event)
	return message, nil
}

// UpdateDiscussionFor lets the controller accept a discussion, report its
// live state, or end it; people may only end it.
func (s *Service) UpdateDiscussionFor(ctx context.Context, discussionID string, request model.UpdateDiscussionRequest, principal Principal) (model.TaskDiscussion, error) {
	item, err := s.authorizedDiscussion(ctx, discussionID, principal)
	if err != nil {
		return model.TaskDiscussion{}, err
	}
	if !principal.Agent && (!principal.Can(PermissionTaskWrite) || request.Status != model.DiscussionEnded || request.AgentStatus != "") {
		return model.TaskDiscussion{}, ErrForbidden
	}
	if item.Status == model.DiscussionEnded {
		if request.Status == model.DiscussionEnded {
			return item, nil
		}
		return model.TaskDiscussion{}, fmt.Errorf("%w: this discussion has ended", ErrConflict)
	}
	now := time.Now().UTC()
	switch request.Status {
	case "":
	case model.DiscussionActive:
		if item.Status == model.DiscussionRequested {
			item.Status, item.StartedAt = model.DiscussionActive, &now
		}
	case model.DiscussionEnded:
		reason := strings.TrimSpace(request.EndReason)
		if reason == "" {
			reason = "ended by " + principal.ID
		}
		item.Status, item.EndedAt, item.EndReason, item.AgentStatus = model.DiscussionEnded, &now, clipped(reason, 300), ""
	default:
		return model.TaskDiscussion{}, fmt.Errorf("%w: status must be active or ended", ErrValidation)
	}
	if request.AgentStatus != "" {
		if request.AgentStatus != "thinking" && request.AgentStatus != "ready" {
			return model.TaskDiscussion{}, fmt.Errorf("%w: agent_status must be thinking or ready", ErrValidation)
		}
		if item.Status == model.DiscussionActive {
			item.AgentStatus = request.AgentStatus
		}
	}
	item.LastActivityAt = now
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.TaskDiscussion{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := store.UpdateDiscussion(ctx, tx, item); err != nil {
		return model.TaskDiscussion{}, err
	}
	event := discussionEvent(item, "task.discussion_updated", principal.ID, "Discussion "+string(item.Status), now)
	if err := store.InsertEvent(ctx, tx, event); err != nil {
		return model.TaskDiscussion{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.TaskDiscussion{}, err
	}
	s.publish(event)
	return item, nil
}

// authorizedDiscussion loads a discussion for its controller or for a person
// who can see its task.
func (s *Service) authorizedDiscussion(ctx context.Context, discussionID string, principal Principal) (model.TaskDiscussion, error) {
	item, err := s.store.GetDiscussion(ctx, strings.TrimSpace(discussionID))
	if err != nil {
		return model.TaskDiscussion{}, err
	}
	if principal.Agent {
		if item.Controller != principal.ID {
			return model.TaskDiscussion{}, store.ErrNotFound
		}
		task, err := s.GetFor(ctx, item.TaskID, principal)
		if err != nil {
			return model.TaskDiscussion{}, err
		}
		if !controllerMayDiscuss(task, principal) {
			return model.TaskDiscussion{}, ErrForbidden
		}
		return item, nil
	}
	if _, err := s.GetFor(ctx, item.TaskID, principal); err != nil {
		return model.TaskDiscussion{}, err
	}
	return item, nil
}

// controllerMayDiscuss reports whether an agent may still read and answer
// discussions on a task. The controller is fixed when a discussion starts, so
// this is checked on every call: the agent must still see and own the task
// and hold task:message, as for the task's messages.
func controllerMayDiscuss(task model.Task, principal Principal) bool {
	return principal.HasCapability(CapabilityTaskMessage) && canMutate(task, principal)
}

func (s *Service) discussionWithMessages(ctx context.Context, id, after string) (model.TaskDiscussion, error) {
	item, err := s.store.GetDiscussion(ctx, id)
	if err != nil {
		return model.TaskDiscussion{}, err
	}
	item.Messages, err = s.store.ListDiscussionMessages(ctx, id, after, discussionMessageLimit)
	return item, err
}

// expireIdleDiscussions ends open discussions without recent activity.
func (s *Service) expireIdleDiscussions(ctx context.Context, items []model.TaskDiscussion) ([]model.TaskDiscussion, error) {
	now := time.Now().UTC()
	for index, item := range items {
		if item.Status == model.DiscussionEnded || now.Sub(item.LastActivityAt) < DiscussionIdle {
			continue
		}
		item.Status, item.EndedAt, item.EndReason, item.AgentStatus = model.DiscussionEnded, &now, "idle", ""
		tx, err := s.store.DB().BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		event := discussionEvent(item, "task.discussion_updated", "system", "Discussion ended after inactivity", now)
		err = store.UpdateDiscussion(ctx, tx, item)
		if err == nil {
			err = store.InsertEvent(ctx, tx, event)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			_ = tx.Rollback()
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, err
		}
		s.publish(event)
		items[index] = item
	}
	return items, nil
}

func discussionEvent(item model.TaskDiscussion, kind, actor, message string, now time.Time) model.Event {
	return model.Event{ID: newID(now), TaskID: item.TaskID, Kind: kind, Actor: actor, Message: message,
		Payload: map[string]any{"discussion_id": item.ID, "status": item.Status}, CreatedAt: now}
}
