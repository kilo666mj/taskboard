package service

import (
	"errors"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
)

func TestPersonalAgentsShareControllerTasks(t *testing.T) {
	tasks := testService(t, time.Minute)
	controller := AgentPrincipal("agent:scheduler")
	worker := AgentPrincipal("agent:worker")
	worker.Operator = "operator"
	started, err := tasks.StartFor(t.Context(), model.StartRequest{
		Title: "Inspect incident", Visibility: model.VisibilityTeam, Checklist: []string{"Verify recovery"},
	}, controller)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.AddMessageFor(t.Context(), started.Task.ID, model.AddMessageRequest{
		AuthorRunID: started.Run.ID, Kind: model.MessageNote, Body: "Recovery evidence",
	}, controller); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.ClaimFor(t.Context(), started.Task.ID, model.ClaimRequest{ExpectedVersion: started.Task.Version}, worker); !errors.Is(err, ErrValidation) {
		t.Fatalf("claim active task = %v, want validation failure", err)
	}
	if _, err := tasks.HeartbeatFor(t.Context(), started.Task.ID, started.Run.ID, worker); !errors.Is(err, ErrForbidden) && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("heartbeat another controller's run = %v", err)
	}
	if _, err := tasks.UpdateFor(t.Context(), started.Task.ID, model.UpdateRequest{
		ExpectedVersion: started.Task.Version, RunID: started.Run.ID, CurrentNote: ptr("Impersonation"),
	}, worker); !errors.Is(err, ErrForbidden) {
		t.Fatalf("update with another controller's run = %v", err)
	}
	waiting, err := tasks.UpdateFor(t.Context(), started.Task.ID, model.UpdateRequest{
		ExpectedVersion: started.Task.Version, RunID: started.Run.ID, Status: model.TaskWaiting, WaitingFor: ptr("Review recovery"),
	}, controller)
	if err != nil {
		t.Fatal(err)
	}
	if messages, err := tasks.ListMessagesFor(t.Context(), waiting.ID, "", 20, worker); err != nil || len(messages) != 1 {
		t.Fatalf("read other controller's messages = %v, %v", messages, err)
	}
	if _, err := tasks.ListEscalationsFor(t.Context(), waiting.ID, worker); err != nil {
		t.Fatalf("read other controller's decisions = %v", err)
	}
	if _, err := tasks.UpdateFor(t.Context(), waiting.ID, model.UpdateRequest{
		ExpectedVersion: waiting.Version, Status: model.TaskDone,
	}, worker); !errors.Is(err, ErrValidation) {
		t.Fatalf("completion without required checklist = %v", err)
	}
	completed, err := tasks.UpdateFor(t.Context(), waiting.ID, model.UpdateRequest{
		ExpectedVersion: waiting.Version, Status: model.TaskDone, CompleteItemIDs: []string{waiting.Items[0].ID},
	}, worker)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Owner != controller.ID || completed.LastEditedBy != worker.ID {
		t.Fatalf("completion lost owner or editor attribution: %+v", completed)
	}
	events, err := tasks.store.ListTaskEvents(t.Context(), waiting.ID)
	if err != nil || len(events) == 0 || events[len(events)-1].Actor != worker.ID {
		t.Fatalf("completion audit = %+v, %v", events, err)
	}
}

func TestPersonalAgentListingIncludesFullBoardButClaimsStillCheckReadiness(t *testing.T) {
	tasks := testService(t, time.Minute)
	operator := HumanPrincipal("operator")
	worker := AgentPrincipal("agent:worker")
	worker.Operator = operator.ID
	team, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Scheduler review", Visibility: model.VisibilityTeam}, operator)
	if err != nil {
		t.Fatal(err)
	}
	team, err = tasks.UpdateFor(t.Context(), team.ID, model.UpdateRequest{ExpectedVersion: team.Version, Owner: ptr("agent:scheduler")}, operator)
	if err != nil {
		t.Fatal(err)
	}
	private, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Personal errand"}, operator)
	if err != nil {
		t.Fatal(err)
	}
	deferred, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Future work", Visibility: model.VisibilityAgent, DeferUntil: "2099-01-01"}, operator)
	if err != nil {
		t.Fatal(err)
	}
	unmatched, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Local runner job", Visibility: model.VisibilityAgent, Requirements: []string{"runner:local"}}, operator)
	if err != nil {
		t.Fatal(err)
	}
	otherPrivate, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Historical other person's private task"}, HumanPrincipal("someone-else"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := tasks.ListFor(t.Context(), model.ListTasksRequest{Limit: 1, Cursor: cursor}, worker)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range page.Tasks {
			seen[task.ID] = true
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	for _, task := range []model.Task{team, private, deferred, unmatched} {
		if !seen[task.ID] {
			t.Errorf("personal listing omitted %q", task.Title)
		}
		if _, err := tasks.GetFor(t.Context(), task.ID, worker); err != nil {
			t.Errorf("personal read %q: %v", task.Title, err)
		}
	}
	if seen[otherPrivate.ID] {
		t.Fatal("personal listing exposed another person's private task")
	}
	if _, err := tasks.GetFor(t.Context(), otherPrivate.ID, worker); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other private read = %v", err)
	}
	if edited, err := tasks.UpdateFor(t.Context(), private.ID, model.UpdateRequest{
		ExpectedVersion: private.Version, CurrentNote: ptr("Reviewed by another agent"),
	}, worker); err != nil || edited.LastEditedBy != worker.ID || edited.Owner != "" {
		t.Fatalf("edit operator's unassigned private task = %+v, %v", edited, err)
	}
	for _, task := range []model.Task{deferred, unmatched} {
		if _, err := tasks.ClaimFor(t.Context(), task.ID, model.ClaimRequest{ExpectedVersion: task.Version}, worker); !errors.Is(err, ErrValidation) {
			t.Errorf("claimed unready or unmatched work %q: %v", task.Title, err)
		}
	}
	// Automated dispatchers explicitly request the pickup lane. Keep that
	// listing runnable so they do not repeatedly try blocked/unmatched work.
	pickup, err := tasks.ListFor(t.Context(), model.ListTasksRequest{Visibility: model.VisibilityAgent}, worker)
	if err != nil || len(pickup.Tasks) != 0 {
		t.Fatalf("personal pickup listing included unrunnable work: %+v, %v", pickup.Tasks, err)
	}
	if _, err := tasks.ClaimFor(t.Context(), team.ID, model.ClaimRequest{ExpectedVersion: team.Version}, worker); err != nil {
		t.Fatalf("personal agent could not claim other controller's queued work: %v", err)
	}
}

func TestPersonalAccessDoesNotGrantCapabilitiesOrMultiuserAccess(t *testing.T) {
	tasks := testService(t, time.Minute)
	controller := AgentPrincipal("agent:scheduler")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Guarded incident", Visibility: model.VisibilityTeam, Checklist: []string{"Verify"}}, controller)
	if err != nil {
		t.Fatal(err)
	}
	other := AgentPrincipal("agent:worker")
	if _, err := tasks.GetFor(t.Context(), started.Task.ID, other); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("multi-user cross-controller read = %v", err)
	}
	other.Operator = "operator"
	if _, err := tasks.UpdateFor(t.Context(), started.Task.ID, model.UpdateRequest{
		ExpectedVersion: started.Task.Version, Status: model.TaskCancelled,
	}, other); !errors.Is(err, ErrForbidden) {
		t.Fatalf("personal cancellation without sensitive capability = %v", err)
	}
	reader := AgentPrincipalWithPolicy("agent:reader", AgentPolicy{Capabilities: map[string]bool{CapabilityTaskRead: true}})
	reader.Operator = "operator"
	if _, err := tasks.GetFor(t.Context(), started.Task.ID, reader); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateFor(t.Context(), started.Task.ID, model.UpdateRequest{
		ExpectedVersion: started.Task.Version, CurrentNote: ptr("Unauthorized edit"),
	}, reader); !errors.Is(err, ErrForbidden) {
		t.Fatalf("personal read-only edit = %v", err)
	}
	if _, err := tasks.ListMessagesFor(t.Context(), started.Task.ID, "", 10, reader); !errors.Is(err, ErrForbidden) {
		t.Fatalf("personal messages without capability = %v", err)
	}
}
