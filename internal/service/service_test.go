package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
)

func testService(t *testing.T, lease time.Duration) *Service {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "taskboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	return New(database, lease)
}

func TestChecklistLifecycleAndCompletionGate(t *testing.T) {
	service := testService(t, time.Minute)
	started, err := service.Start(t.Context(), model.StartRequest{
		Title: "Ship taskboard", Agent: "codex", Client: "test",
		Checklist: []string{"Build service", "Verify service"},
	}, "test-client")
	if err != nil {
		t.Fatal(err)
	}
	if started.Task.Items[0].Status != model.ItemActive || started.Task.Items[1].Status != model.ItemTodo {
		t.Fatalf("initial checklist = %+v", started.Task.Items)
	}
	events, cancel := service.Subscribe()
	defer cancel()

	updated, err := service.Update(t.Context(), started.Task.ID, model.UpdateRequest{
		ExpectedVersion: started.Task.Version,
		RunID:           started.Run.ID,
		CompleteItemIDs: []string{started.Task.Items[0].ID},
		CurrentItemID:   started.Task.Items[1].ID,
	}, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Items[0].Status != model.ItemDone || updated.Items[1].Status != model.ItemActive {
		t.Fatalf("advanced task = %+v", updated)
	}
	event := <-events
	completedItemIDs, ok := event.Payload["completed_item_ids"].([]string)
	if !ok || len(completedItemIDs) != 1 || completedItemIDs[0] != started.Task.Items[0].ID {
		t.Fatalf("completed item IDs = %#v", event.Payload["completed_item_ids"])
	}

	_, err = service.Update(t.Context(), updated.ID, model.UpdateRequest{
		ExpectedVersion: updated.Version, RunID: started.Run.ID, Status: model.TaskDone,
	}, "codex")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("completion with open items error = %v, want validation", err)
	}

	completed, err := service.Update(t.Context(), updated.ID, model.UpdateRequest{
		ExpectedVersion: updated.Version,
		RunID:           started.Run.ID,
		Status:          model.TaskDone,
		CompleteItemIDs: []string{updated.Items[1].ID},
	}, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != model.TaskDone || completed.CompletedAt == nil {
		t.Fatalf("completed task = %+v", completed)
	}
}

func TestCreateQueuesTitleOnlyTaskWithoutRun(t *testing.T) {
	service := testService(t, time.Minute)
	task, err := service.Create(t.Context(), model.CreateRequest{Title: "  Buy groceries  "}, "person@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if task.Title != "Buy groceries" || task.Type != model.TaskPersonal || task.Section != model.DefaultSection || task.Status != model.TaskQueued {
		t.Fatalf("created task = %+v", task)
	}
	if task.Owner != "" || len(task.Items) != 0 || len(task.Runs) != 0 {
		t.Fatalf("queued task should be unassigned without items or runs: %+v", task)
	}
}

func TestTaskTypeCanBeSetAndUpdated(t *testing.T) {
	tasks := testService(t, time.Minute)
	task, err := tasks.Create(t.Context(), model.CreateRequest{Title: "Prepare release", Type: model.TaskWork}, "person")
	if err != nil {
		t.Fatal(err)
	}
	if task.Type != model.TaskWork {
		t.Fatalf("created type = %q, want work", task.Type)
	}
	personal := model.TaskPersonal
	task, err = tasks.Update(t.Context(), task.ID, model.UpdateRequest{ExpectedVersion: task.Version, Type: &personal}, "person")
	if err != nil {
		t.Fatal(err)
	}
	if task.Type != model.TaskPersonal {
		t.Fatalf("updated type = %q, want personal", task.Type)
	}
	invalid := model.TaskType("other")
	if _, err := tasks.Update(t.Context(), task.ID, model.UpdateRequest{ExpectedVersion: task.Version, Type: &invalid}, "person"); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid type error = %v, want validation", err)
	}
}

func TestQueuedTaskCanStartWithoutAgentRunOrBeClaimed(t *testing.T) {
	service := testService(t, time.Minute)
	created, err := service.Create(t.Context(), model.CreateRequest{
		Title: "Plan release", Section: " Work ", Checklist: []string{"Draft notes", "Publish"},
	}, "person@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if created.Section != "Work" || created.Items[0].Status != model.ItemTodo {
		t.Fatalf("queued task = %+v", created)
	}

	owner := "person@example.com"
	started, err := service.Update(t.Context(), created.ID, model.UpdateRequest{
		ExpectedVersion: created.Version, Status: model.TaskActive, Owner: &owner,
	}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != model.TaskActive || started.Owner != owner || len(started.Runs) != 0 || started.Items[0].Status != model.ItemActive {
		t.Fatalf("self-started task = %+v", started)
	}

	other, err := service.Create(t.Context(), model.CreateRequest{Title: "Agent work", Checklist: []string{"Inspect"}}, owner)
	if err != nil {
		t.Fatal(err)
	}
	agent := "codex"
	assigned, err := service.Update(t.Context(), other.ID, model.UpdateRequest{ExpectedVersion: other.Version, Owner: &agent}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if assigned.Status != model.TaskQueued || assigned.Owner != agent || len(assigned.Runs) != 0 {
		t.Fatalf("assigned task = %+v", assigned)
	}
	claimed, err := service.Claim(t.Context(), assigned.ID, model.ClaimRequest{ExpectedVersion: assigned.Version, Agent: agent}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Task.Status != model.TaskActive || claimed.Run.Agent != "codex" || claimed.Task.Items[0].Status != model.ItemActive {
		t.Fatalf("claimed task = %+v", claimed)
	}
}

func TestPrivateTaskIsVisibleOnlyToItsCreator(t *testing.T) {
	tasks := testService(t, time.Minute)
	created, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Private plan"}, HumanPrincipal("alice@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Visibility != model.VisibilityPrivate || created.CreatedBy != "alice@example.com" {
		t.Fatalf("created task = %+v", created)
	}
	if _, err := tasks.GetFor(t.Context(), created.ID, HumanPrincipal("bob@example.com")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other user get error = %v, want not found", err)
	}
	if _, err := tasks.GetFor(t.Context(), created.ID, AgentPrincipal("codex")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("agent get error = %v, want not found", err)
	}
	visible, err := tasks.ListFor(t.Context(), nil, 100, HumanPrincipal("alice@example.com"))
	if err != nil || len(visible) != 1 || visible[0].ID != created.ID {
		t.Fatalf("creator list = %+v, %v", visible, err)
	}
	hidden, err := tasks.ListFor(t.Context(), nil, 100, HumanPrincipal("bob@example.com"))
	if err != nil || len(hidden) != 0 {
		t.Fatalf("other user list = %+v, %v", hidden, err)
	}
}

func TestCreatorCanPublishAndReprivatizeTask(t *testing.T) {
	tasks := testService(t, time.Minute)
	alice := HumanPrincipal("alice@example.com")
	bob := HumanPrincipal("bob@example.com")
	created, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Draft"}, alice)
	if err != nil {
		t.Fatal(err)
	}
	team := model.VisibilityTeam
	published, err := tasks.UpdateFor(t.Context(), created.ID, model.UpdateRequest{ExpectedVersion: created.Version, Visibility: &team}, alice)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.GetFor(t.Context(), created.ID, bob); err != nil {
		t.Fatalf("team task hidden from teammate: %v", err)
	}
	private := model.VisibilityPrivate
	if _, err := tasks.UpdateFor(t.Context(), created.ID, model.UpdateRequest{ExpectedVersion: published.Version, Visibility: &private}, bob); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-creator reprivatize error = %v, want forbidden", err)
	}
	reprivatized, err := tasks.UpdateFor(t.Context(), created.ID, model.UpdateRequest{ExpectedVersion: published.Version, Visibility: &private}, alice)
	if err != nil {
		t.Fatal(err)
	}
	if reprivatized.Visibility != model.VisibilityPrivate {
		t.Fatalf("visibility = %q, want private", reprivatized.Visibility)
	}
}

func TestAgentPickupIsClaimableAndOwnedByTheClaimingAgent(t *testing.T) {
	tasks := testService(t, time.Minute)
	created, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Pickup", Checklist: []string{"Do it"}}, HumanPrincipal("alice@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	agentLane := model.VisibilityAgent
	created, err = tasks.UpdateFor(t.Context(), created.ID, model.UpdateRequest{ExpectedVersion: created.Version, Visibility: &agentLane}, HumanPrincipal("alice@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := tasks.ClaimFor(t.Context(), created.ID, model.ClaimRequest{ExpectedVersion: created.Version}, AgentPrincipal("codex"))
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Task.Owner != "codex" || claimed.Task.Status != model.TaskActive {
		t.Fatalf("claimed task = %+v", claimed.Task)
	}
	if _, err := tasks.UpdateFor(t.Context(), created.ID, model.UpdateRequest{ExpectedVersion: claimed.Task.Version, CurrentNote: stringPointer("intrude")}, AgentPrincipal("other-agent")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other agent update error = %v, want forbidden", err)
	}
	team := model.VisibilityTeam
	if _, err := tasks.UpdateFor(t.Context(), created.ID, model.UpdateRequest{ExpectedVersion: claimed.Task.Version, Visibility: &team}, HumanPrincipal("alice@example.com")); !errors.Is(err, ErrValidation) {
		t.Fatalf("active-run visibility change error = %v, want validation", err)
	}
}

func TestActiveAgentRunPreventsReassignment(t *testing.T) {
	tasks := testService(t, time.Minute)
	started, err := tasks.StartFor(t.Context(), model.StartRequest{
		Title: "Assigned team work", Visibility: model.VisibilityTeam, Checklist: []string{"Work"},
	}, AgentPrincipal("codex"))
	if err != nil {
		t.Fatal(err)
	}
	owner := "other-agent"
	if _, err := tasks.UpdateFor(t.Context(), started.Task.ID, model.UpdateRequest{ExpectedVersion: started.Task.Version, Owner: &owner}, HumanPrincipal("alice@example.com")); !errors.Is(err, ErrValidation) {
		t.Fatalf("active-run reassignment error = %v, want validation", err)
	}
}

func stringPointer(value string) *string { return &value }

func TestTitleOnlySelfTaskCanComplete(t *testing.T) {
	service := testService(t, time.Minute)
	created, err := service.Create(t.Context(), model.CreateRequest{Title: "Call the dentist"}, "person")
	if err != nil {
		t.Fatal(err)
	}
	owner := "person"
	started, err := service.Update(t.Context(), created.ID, model.UpdateRequest{ExpectedVersion: created.Version, Status: model.TaskActive, Owner: &owner}, owner)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := service.Update(t.Context(), started.ID, model.UpdateRequest{ExpectedVersion: started.Version, Status: model.TaskDone}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != model.TaskDone || completed.CompletedAt == nil {
		t.Fatalf("completed task = %+v", completed)
	}
}

func TestBlockedAndWaitingRequireExplanations(t *testing.T) {
	service := testService(t, time.Minute)
	started, err := service.Start(t.Context(), model.StartRequest{Title: "Investigate", Checklist: []string{"Inspect"}}, "codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []model.TaskStatus{model.TaskBlocked, model.TaskWaiting} {
		_, err := service.Update(t.Context(), started.Task.ID, model.UpdateRequest{ExpectedVersion: 1, Status: status}, "codex")
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("status %q error = %v, want validation", status, err)
		}
	}
}

func TestCompletingAnAlreadyDoneItemDoesNotReportAnotherCompletion(t *testing.T) {
	service := testService(t, time.Minute)
	started, err := service.Start(t.Context(), model.StartRequest{Title: "Notify once", Checklist: []string{"Step"}}, "codex")
	if err != nil {
		t.Fatal(err)
	}
	events, cancel := service.Subscribe()
	defer cancel()

	updated, err := service.Update(t.Context(), started.Task.ID, model.UpdateRequest{
		ExpectedVersion: started.Task.Version,
		CompleteItemIDs: []string{started.Task.Items[0].ID},
	}, "codex")
	if err != nil {
		t.Fatal(err)
	}
	<-events

	_, err = service.Update(t.Context(), started.Task.ID, model.UpdateRequest{
		ExpectedVersion: updated.Version,
		CompleteItemIDs: []string{started.Task.Items[0].ID},
	}, "codex")
	if err != nil {
		t.Fatal(err)
	}
	event := <-events
	if _, exists := event.Payload["completed_item_ids"]; exists {
		t.Fatalf("repeated completion payload = %#v", event.Payload)
	}
}

func TestVersionConflictDoesNotOverwrite(t *testing.T) {
	service := testService(t, time.Minute)
	started, err := service.Start(t.Context(), model.StartRequest{Title: "Concurrent task", Checklist: []string{"Step"}}, "codex")
	if err != nil {
		t.Fatal(err)
	}
	note := "first update"
	if _, err := service.Update(t.Context(), started.Task.ID, model.UpdateRequest{ExpectedVersion: 1, CurrentNote: &note}, "codex"); err != nil {
		t.Fatal(err)
	}
	staleNote := "stale update"
	_, err = service.Update(t.Context(), started.Task.ID, model.UpdateRequest{ExpectedVersion: 1, CurrentNote: &staleNote}, "other-agent")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error = %v, want conflict", err)
	}
}

func TestExpiredRunBecomesStale(t *testing.T) {
	service := testService(t, time.Millisecond)
	started, err := service.Start(context.Background(), model.StartRequest{Title: "Long task", Checklist: []string{"Work"}}, "codex")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	count, err := service.SweepStale(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stale runs = %d, want 1", count)
	}
	task, err := service.Get(t.Context(), started.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != model.TaskStale || task.Runs[0].Status != model.TaskStale {
		t.Fatalf("stale task/run = %s/%s", task.Status, task.Runs[0].Status)
	}
}

func TestClaimRecoversStaleTaskAndOldRunCannotHeartbeat(t *testing.T) {
	service := testService(t, time.Millisecond)
	started, err := service.Start(context.Background(), model.StartRequest{Title: "Recover me", Checklist: []string{"Work"}}, "first-agent")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := service.SweepStale(t.Context()); err != nil {
		t.Fatal(err)
	}
	stale, err := service.Get(t.Context(), started.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := service.Claim(t.Context(), stale.ID, model.ClaimRequest{ExpectedVersion: stale.Version, Agent: "second-agent", Client: "test"}, "second-agent")
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Task.Status != model.TaskActive || claimed.Task.Owner != "second-agent" || claimed.Run.Agent != "second-agent" {
		t.Fatalf("claimed task = %+v", claimed)
	}
	if _, err := service.Heartbeat(t.Context(), stale.ID, started.Run.ID, "first-agent"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old stale run heartbeat error = %v, want not found", err)
	}
}

func TestOneExpiredRunDoesNotStaleTaskWithAnotherActiveRun(t *testing.T) {
	service := testService(t, time.Minute)
	started, err := service.Start(context.Background(), model.StartRequest{Title: "Shared task", Checklist: []string{"Work"}}, "first-agent")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := service.Claim(t.Context(), started.Task.ID, model.ClaimRequest{ExpectedVersion: started.Task.Version, Agent: "second-agent"}, "second-agent")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := service.store.DB().ExecContext(t.Context(), `UPDATE agent_runs SET lease_expires_at=? WHERE id=?`, stamp(now.Add(-time.Minute)), started.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.store.DB().ExecContext(t.Context(), `UPDATE agent_runs SET lease_expires_at=?,last_heartbeat_at=? WHERE id=?`, stamp(now.Add(time.Minute)), stamp(now), claimed.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SweepStale(t.Context()); err != nil {
		t.Fatal(err)
	}
	task, err := service.Get(t.Context(), started.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != model.TaskActive {
		t.Fatalf("task status = %s, want active", task.Status)
	}
}

func TestPlanningMetadataAndQueuedEditing(t *testing.T) {
	tasks := testService(t, time.Minute)
	created, err := tasks.Create(t.Context(), model.CreateRequest{
		Title: "Plan a trip", Section: "Personal", Project: "Holiday", Repository: "notes",
		Priority: model.PriorityHigh, DueDate: "2026-10-10", DeferUntil: "2026-09-20",
		Checklist: []string{"Choose dates"},
	}, "person")
	if err != nil {
		t.Fatal(err)
	}
	if created.Project != "Holiday" || created.Priority != model.PriorityHigh || created.DueDate != "2026-10-10" || created.DeferUntil != "2026-09-20" {
		t.Fatalf("planning metadata = %+v", created)
	}
	title, project, due := "Plan autumn trip", "Travel", "2026-10-12"
	priority := model.PriorityUrgent
	checklist := []string{"Choose dates", "Book travel"}
	updated, err := tasks.Update(t.Context(), created.ID, model.UpdateRequest{
		ExpectedVersion: created.Version, Title: &title, Project: &project, Priority: &priority, DueDate: &due, Checklist: &checklist,
	}, "person")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != title || updated.Project != project || updated.Priority != priority || updated.DueDate != due || len(updated.Items) != 2 {
		t.Fatalf("updated task = %+v", updated)
	}
}

func TestCompletingRecurringTaskCreatesNextOccurrence(t *testing.T) {
	tasks := testService(t, time.Minute)
	created, err := tasks.Create(t.Context(), model.CreateRequest{
		Title: "Weekly review", DueDate: "2026-09-16", Recurrence: "weekly", Checklist: []string{"Clear inbox"},
	}, "person")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := tasks.Update(t.Context(), created.ID, model.UpdateRequest{
		ExpectedVersion: created.Version, Status: model.TaskDone, CompleteItemIDs: []string{created.Items[0].ID},
	}, "person")
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != model.TaskDone {
		t.Fatalf("completed task status = %q", completed.Status)
	}
	summary := "Reviewed after completion"
	edited, err := tasks.Update(t.Context(), completed.ID, model.UpdateRequest{ExpectedVersion: completed.Version, Summary: &summary}, "person")
	if err != nil {
		t.Fatal(err)
	}
	if edited.CompletedAt == nil || !edited.CompletedAt.Equal(*completed.CompletedAt) {
		t.Fatalf("completion time changed while editing: before=%v after=%v", completed.CompletedAt, edited.CompletedAt)
	}
	listed, err := tasks.List(t.Context(), nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("task count = %d, want 2", len(listed))
	}
	var next model.Task
	for _, task := range listed {
		if task.ID != created.ID {
			next = task
		}
	}
	if next.Status != model.TaskQueued || next.DueDate != "2026-09-23" || next.Recurrence != "weekly" || len(next.Items) != 1 || next.Items[0].Status != model.ItemTodo {
		t.Fatalf("next occurrence = %+v", next)
	}
}

func TestTemplateLifecycle(t *testing.T) {
	tasks := testService(t, time.Minute)
	saved, err := tasks.SaveTemplate(t.Context(), model.TemplateRequest{
		Name: "Release", Title: "Ship release", Section: "Work", Project: "Product",
		Priority: model.PriorityHigh, Recurrence: "monthly", Checklist: []string{"Test", "Publish"},
	})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := tasks.ListTemplates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != saved.ID || len(listed[0].Checklist) != 2 {
		t.Fatalf("templates = %+v", listed)
	}
	updated, err := tasks.SaveTemplate(t.Context(), model.TemplateRequest{Name: "Release", Title: "Publish release", Checklist: []string{"Publish"}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != saved.ID || updated.Title != "Publish release" || len(updated.Checklist) != 1 {
		t.Fatalf("updated template = %+v", updated)
	}
	if err := tasks.DeleteTemplate(t.Context(), saved.ID); err != nil {
		t.Fatal(err)
	}
	listed, err = tasks.ListTemplates(t.Context())
	if err != nil || len(listed) != 0 {
		t.Fatalf("templates after delete = %+v, %v", listed, err)
	}
}

func TestMoveReordersTasksWithinSection(t *testing.T) {
	tasks := testService(t, time.Minute)
	first, err := tasks.Create(t.Context(), model.CreateRequest{Title: "First", Section: "Work"}, "person")
	if err != nil {
		t.Fatal(err)
	}
	second, err := tasks.Create(t.Context(), model.CreateRequest{Title: "Second", Section: "Work"}, "person")
	if err != nil {
		t.Fatal(err)
	}
	moved, err := tasks.Move(t.Context(), second.ID, model.MoveRequest{ExpectedVersion: second.Version, Direction: "up"}, "person")
	if err != nil {
		t.Fatal(err)
	}
	if moved.SortOrder != first.SortOrder {
		t.Fatalf("moved order = %d, want %d", moved.SortOrder, first.SortOrder)
	}
	listed, err := tasks.List(t.Context(), []model.TaskStatus{model.TaskQueued}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].ID != second.ID || listed[1].ID != first.ID {
		t.Fatalf("ordered tasks = %+v", listed)
	}
	if listed[1].Version != first.Version {
		t.Fatalf("moving a neighbor changed its version from %d to %d", first.Version, listed[1].Version)
	}
}
