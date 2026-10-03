package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

const inboxRunLimit = 20

// InboxFor returns the work waiting on the calling controller for the given
// runs: open controls and session requests targeting them, discussions on
// their tasks that are requested or awaiting a reply, answered or expired
// escalations they raised, and unreceived messages for runs still active.
//
// Several agent sessions can share one principal, so the inbox is scoped to
// the runs a session names rather than to everything the principal owns.
// Answered and expired escalations stay listed; clients remember what they
// have already acted on by ID and update time.
func (s *Service) InboxFor(ctx context.Context, request model.InboxRequest, principal Principal) (model.Inbox, error) {
	if !principal.Agent {
		return model.Inbox{}, ErrForbidden
	}
	if len(request.Runs) == 0 || len(request.Runs) > inboxRunLimit {
		return model.Inbox{}, fmt.Errorf("%w: 1-%d runs are required", ErrValidation, inboxRunLimit)
	}
	inbox := model.Inbox{AsOf: time.Now().UTC(), Runs: []model.InboxRunState{}, Controls: []model.RunControlRequest{}, SessionRequests: []model.SessionBridgeRequest{},
		Discussions: []model.TaskDiscussion{}, Escalations: []model.TaskEscalation{}, Messages: []model.TaskMessage{}}
	runs, tasks, taskOrder := map[string]bool{}, map[string]bool{}, []string{}
	for _, entry := range request.Runs {
		taskID, runID := strings.TrimSpace(entry.TaskID), strings.TrimSpace(entry.RunID)
		if taskID == "" || runID == "" {
			return model.Inbox{}, fmt.Errorf("%w: task_id and run_id are required", ErrValidation)
		}
		if runs[runID] {
			continue
		}
		task, err := s.GetFor(ctx, taskID, principal)
		if err != nil {
			return model.Inbox{}, err
		}
		run, ok := taskRun(task, runID)
		if !ok || run.Agent != principal.ID {
			return model.Inbox{}, fmt.Errorf("%w: run %q is not a run of this agent on task %q", ErrValidation, runID, taskID)
		}
		_, active := activeTaskRun(task, runID)
		if !tasks[taskID] {
			taskOrder = append(taskOrder, taskID)
		}
		runs[runID], tasks[taskID] = true, true
		inbox.Runs = append(inbox.Runs, model.InboxRunState{TaskID: taskID, RunID: runID, TaskStatus: task.Status, TaskVersion: task.Version, RunStatus: run.Status, Active: active})
		if active && principal.HasCapability(CapabilityTaskMessage) {
			messages, err := s.store.ListPendingTaskMessages(ctx, taskID, runID, principal.ID, 50)
			if err != nil {
				return model.Inbox{}, err
			}
			inbox.Messages = append(inbox.Messages, messages...)
		}
	}
	if principal.HasCapability(CapabilityTaskControl) {
		if err := s.expireRunControls(ctx); err != nil {
			return model.Inbox{}, err
		}
		controls, err := s.store.ListAgentRunControls(ctx, principal.ID, 200)
		if err != nil {
			return model.Inbox{}, err
		}
		for _, control := range controls {
			if runs[control.TargetRunID] {
				inbox.Controls = append(inbox.Controls, control)
			}
		}
	}
	if principal.HasCapability(CapabilityTaskSession) {
		requests, err := s.store.ListSessionBridgeRequests(ctx, principal.ID, false)
		if err != nil {
			return model.Inbox{}, err
		}
		for _, item := range requests {
			if runs[item.RunID] {
				inbox.SessionRequests = append(inbox.SessionRequests, item)
			}
		}
	}
	discussions, err := s.ListControllerDiscussionsFor(ctx, principal)
	if err != nil {
		return model.Inbox{}, err
	}
	for _, item := range discussions {
		if !tasks[item.TaskID] {
			continue
		}
		if item.Messages, err = s.store.ListDiscussionMessages(ctx, item.ID, "", discussionMessageLimit); err != nil {
			return model.Inbox{}, err
		}
		item.Messages = unansweredDiscussionMessages(item.Messages)
		if item.Status == model.DiscussionRequested || len(item.Messages) > 0 {
			inbox.Discussions = append(inbox.Discussions, item)
		}
	}
	if principal.HasCapability(CapabilityTaskEscalate) {
		for _, taskID := range taskOrder {
			escalations, err := s.store.ListTaskEscalations(ctx, taskID)
			if err != nil {
				return model.Inbox{}, err
			}
			for _, item := range escalations {
				if runs[item.RunID] && (item.Status == model.EscalationAnswered || item.Status == model.EscalationExpired) {
					inbox.Escalations = append(inbox.Escalations, item)
				}
			}
		}
	}
	inbox.Count = len(inbox.Controls) + len(inbox.SessionRequests) + len(inbox.Discussions) + len(inbox.Escalations) + len(inbox.Messages)
	return inbox, nil
}

// unansweredDiscussionMessages returns the people's messages after the
// controller's latest reply.
func unansweredDiscussionMessages(messages []model.DiscussionMessage) []model.DiscussionMessage {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != "person" {
			return messages[index+1:]
		}
	}
	return messages
}
