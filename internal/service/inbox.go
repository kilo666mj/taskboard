package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

const (
	inboxRunLimit = 20
	// inboxMaxWaitSeconds stays well inside MCP client and gateway call
	// timeouts, so a waiting call ends before any of them cancels it.
	inboxMaxWaitSeconds = 25
	// inboxRecheckInterval catches changes that publish no task event, such
	// as expiring controls, and events dropped by a full subscriber buffer.
	inboxRecheckInterval = 10 * time.Second
	// inboxWaitersPerPrincipal bounds held calls for one principal; callers
	// beyond it are answered at once instead of waiting.
	inboxWaitersPerPrincipal = 32
)

// InboxFor returns the inbox for the named runs. With WaitSeconds it first
// holds the call until the inbox differs from request.Digest, a task event or
// a periodic recheck shows a change, or the wait ends; the result is the
// inbox as it stands then. This gives session adapters near-immediate
// delivery through the session's own MCP connection, without a stream.
func (s *Service) InboxFor(ctx context.Context, request model.InboxRequest, principal Principal) (model.Inbox, error) {
	if request.WaitSeconds < 0 || request.WaitSeconds > inboxMaxWaitSeconds {
		return model.Inbox{}, fmt.Errorf("%w: wait_seconds must be between 0 and %d", ErrValidation, inboxMaxWaitSeconds)
	}
	if request.WaitSeconds == 0 || !s.acquireInboxWaiter(principal.ID) {
		return s.readInbox(ctx, request, principal)
	}
	defer s.releaseInboxWaiter(principal.ID)
	// Subscribe before reading so no change between the read and the wait
	// is missed.
	events, cancel := s.Subscribe()
	defer cancel()
	inbox, err := s.readInbox(ctx, request, principal)
	if err != nil || inbox.Digest != request.Digest {
		return inbox, err
	}
	tasks := map[string]bool{}
	for _, run := range request.Runs {
		tasks[strings.TrimSpace(run.TaskID)] = true
	}
	deadline := time.NewTimer(time.Duration(request.WaitSeconds) * time.Second)
	defer deadline.Stop()
	recheck := time.NewTicker(inboxRecheckInterval)
	defer recheck.Stop()
	for {
		select {
		case <-ctx.Done():
			return inbox, nil
		case <-deadline.C:
			return inbox, nil
		case event, ok := <-events:
			if !ok {
				return inbox, nil
			}
			if !tasks[event.TaskID] {
				continue
			}
		case <-recheck.C:
		}
		if inbox, err = s.readInbox(ctx, request, principal); err != nil || inbox.Digest != request.Digest {
			return inbox, err
		}
	}
}

func (s *Service) acquireInboxWaiter(principalID string) bool {
	s.inboxWaitMu.Lock()
	defer s.inboxWaitMu.Unlock()
	if s.inboxWaiters == nil {
		s.inboxWaiters = map[string]int{}
	}
	if s.inboxWaiters[principalID] >= inboxWaitersPerPrincipal {
		return false
	}
	s.inboxWaiters[principalID]++
	return true
}

func (s *Service) releaseInboxWaiter(principalID string) {
	s.inboxWaitMu.Lock()
	defer s.inboxWaitMu.Unlock()
	if s.inboxWaiters[principalID] <= 1 {
		delete(s.inboxWaiters, principalID)
		return
	}
	s.inboxWaiters[principalID]--
}

// readInbox returns the work waiting on the calling controller for the given
// runs: open controls and session requests targeting them, discussions on
// their tasks that are requested or awaiting a reply, answered or expired
// escalations they raised, and unreceived messages for runs still active.
//
// Several agent sessions can share one principal, so the inbox is scoped to
// the runs a session names rather than to everything the principal owns.
// Answered and expired escalations stay listed; clients remember what they
// have already acted on by ID and update time.
func (s *Service) readInbox(ctx context.Context, request model.InboxRequest, principal Principal) (model.Inbox, error) {
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
	inbox.Digest = inboxDigest(inbox)
	return inbox, nil
}

// inboxDigest identifies what an inbox holds: each item with its status, and
// each run with its own and its task's status. Ordering is ignored, and task
// versions are left out so unrelated task edits do not wake waiting callers.
func inboxDigest(inbox model.Inbox) string {
	keys := []string{}
	for _, run := range inbox.Runs {
		keys = append(keys, fmt.Sprintf("run:%s:%s:%s:%t", run.RunID, run.TaskStatus, run.RunStatus, run.Active))
	}
	for _, control := range inbox.Controls {
		keys = append(keys, fmt.Sprintf("control:%s:%s", control.ID, control.Status))
	}
	for _, request := range inbox.SessionRequests {
		keys = append(keys, fmt.Sprintf("session:%s:%s", request.ID, request.Status))
	}
	for _, discussion := range inbox.Discussions {
		last := ""
		if count := len(discussion.Messages); count > 0 {
			last = discussion.Messages[count-1].ID
		}
		keys = append(keys, fmt.Sprintf("discussion:%s:%s:%s", discussion.ID, discussion.Status, last))
	}
	for _, escalation := range inbox.Escalations {
		keys = append(keys, fmt.Sprintf("escalation:%s:%s", escalation.ID, escalation.Status))
	}
	for _, message := range inbox.Messages {
		keys = append(keys, "message:"+message.ID)
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:16])
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
