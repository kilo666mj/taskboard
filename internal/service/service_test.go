package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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

func TestPrincipalPermissionMatrix(t *testing.T) {
	tests := []struct {
		role            Role
		writeTasks      bool
		manageTemplates bool
	}{
		{RoleOwner, true, true},
		{RoleAdmin, true, true},
		{RoleMember, true, false},
		{RoleViewer, false, false},
	}
	for _, test := range tests {
		t.Run(string(test.role), func(t *testing.T) {
			principal := HumanPrincipalWithRole("person", test.role)
			for _, permission := range []Permission{PermissionTaskRead, PermissionTemplateRead, PermissionEventStream, PermissionPushManage} {
				if !principal.Can(permission) {
					t.Errorf("%s cannot %s", test.role, permission)
				}
			}
			if got := principal.Can(PermissionTaskWrite); got != test.writeTasks {
				t.Errorf("%s task write = %v, want %v", test.role, got, test.writeTasks)
			}
			if got := principal.Can(PermissionTaskMessage); got != test.writeTasks {
				t.Errorf("%s task message = %v, want %v", test.role, got, test.writeTasks)
			}
			if got := principal.Can(PermissionTemplateManage); got != test.manageTemplates {
				t.Errorf("%s template manage = %v, want %v", test.role, got, test.manageTemplates)
			}
		})
	}

	agent := AgentPrincipal("agent:build")
	if !agent.Can(PermissionTaskRead) || !agent.Can(PermissionTaskWrite) || !agent.Can(PermissionTaskMessage) || agent.Can(PermissionPushManage) {
		t.Fatalf("unexpected agent permissions: %+v", agent)
	}
}

func TestViewerCannotMutateVisibleTaskOrTemplates(t *testing.T) {
	tasks := testService(t, time.Minute)
	admin := HumanPrincipalWithRole("admin@example.com", RoleAdmin)
	viewer := HumanPrincipalWithRole("viewer@example.com", RoleViewer)
	created, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Team plan", Visibility: model.VisibilityTeam}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.GetFor(t.Context(), created.ID, viewer); err != nil {
		t.Fatalf("viewer read: %v", err)
	}
	if _, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Viewer write"}, viewer); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer create error = %v, want forbidden", err)
	}
	if _, err := tasks.UpdateFor(t.Context(), created.ID, model.UpdateRequest{ExpectedVersion: created.Version}, viewer); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer update error = %v, want forbidden", err)
	}
	if _, err := tasks.SaveTemplateFor(t.Context(), model.TemplateRequest{Name: "Viewer template", Title: "No"}, viewer); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer template save error = %v, want forbidden", err)
	}
	if _, err := tasks.ListTemplatesFor(t.Context(), viewer); err != nil {
		t.Fatalf("viewer template list: %v", err)
	}
}

func TestAgentCapabilitiesAndSensitiveApprovalGate(t *testing.T) {
	tasks := testService(t, time.Minute)
	readOnly := AgentPrincipalWithPolicy("agent:reader", AgentPolicy{Capabilities: map[string]bool{CapabilityTaskRead: true}})
	if _, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Denied"}, readOnly); !errors.Is(err, ErrForbidden) {
		t.Fatalf("read-only agent create error = %v, want forbidden", err)
	}

	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Guarded work", Checklist: []string{"Sensitive step"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateFor(t.Context(), started.Task.ID, model.UpdateRequest{
		ExpectedVersion: started.Task.Version,
		RunID:           started.Run.ID,
		SkipItemIDs:     []string{started.Task.Items[0].ID},
		SkipReason:      "irreversible",
	}, agent); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ungated sensitive update error = %v, want forbidden", err)
	}

	policy := DefaultAgentPolicy()
	policy.Capabilities[CapabilityTaskSensitive] = true
	approved := AgentPrincipalWithPolicy("agent:worker", policy)
	if _, err := tasks.UpdateFor(t.Context(), started.Task.ID, model.UpdateRequest{
		ExpectedVersion: started.Task.Version,
		RunID:           started.Run.ID,
		SkipItemIDs:     []string{started.Task.Items[0].ID},
		SkipReason:      "approved by policy",
	}, approved); err != nil {
		t.Fatalf("approved sensitive update: %v", err)
	}
}

func TestAgentIdempotencyReplaysMutationAndRejectsKeyReuse(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	request := model.CreateRequest{Title: "Idempotent task", IdempotencyKey: "request-12345678"}
	first, err := tasks.CreateFor(t.Context(), request, agent)
	if err != nil {
		t.Fatal(err)
	}
	second, err := tasks.CreateFor(t.Context(), request, agent)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("replay task ID = %q, want %q", second.ID, first.ID)
	}
	request.Title = "Different mutation"
	if _, err := tasks.CreateFor(t.Context(), request, agent); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused idempotency key error = %v, want conflict", err)
	}

	requiredPolicy := DefaultAgentPolicy()
	requiredPolicy.RequireIdempotency = true
	required := AgentPrincipalWithPolicy("agent:strict", requiredPolicy)
	if _, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Missing key"}, required); !errors.Is(err, ErrValidation) {
		t.Fatalf("missing required idempotency key error = %v, want validation", err)
	}
}

func TestAgentIdempotencyReplayRechecksCurrentVisibility(t *testing.T) {
	t.Run("create reassigned to another agent", func(t *testing.T) {
		tasks := testService(t, time.Minute)
		agent := AgentPrincipal("agent:worker")
		request := model.CreateRequest{Title: "Idempotent create", IdempotencyKey: "create-private-1234"}
		created, err := tasks.CreateFor(t.Context(), request, agent)
		if err != nil {
			t.Fatal(err)
		}
		team, other := model.VisibilityTeam, "agent:other"
		if _, err := tasks.UpdateFor(t.Context(), created.ID, model.UpdateRequest{ExpectedVersion: created.Version, Visibility: &team, Owner: &other}, HumanPrincipal("operator@example.com")); err != nil {
			t.Fatal(err)
		}
		if _, err := tasks.CreateFor(t.Context(), request, agent); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("create replay error = %v, want not found", err)
		}
	})

	t.Run("start reassigned after completion", func(t *testing.T) {
		tasks := testService(t, time.Minute)
		agent := AgentPrincipal("agent:worker")
		request := model.StartRequest{Title: "Idempotent start", Visibility: model.VisibilityTeam, Checklist: []string{"Work"}, IdempotencyKey: "start-private-1234"}
		started, err := tasks.StartFor(t.Context(), request, agent)
		if err != nil {
			t.Fatal(err)
		}
		completed, err := tasks.UpdateFor(t.Context(), started.Task.ID, model.UpdateRequest{ExpectedVersion: started.Task.Version, RunID: started.Run.ID, Status: model.TaskDone, CompleteItemIDs: []string{started.Task.Items[0].ID}}, agent)
		if err != nil {
			t.Fatal(err)
		}
		other := "agent:other"
		if _, err := tasks.UpdateFor(t.Context(), completed.ID, model.UpdateRequest{ExpectedVersion: completed.Version, Owner: &other}, HumanPrincipal("operator@example.com")); err != nil {
			t.Fatal(err)
		}
		if _, err := tasks.StartFor(t.Context(), request, agent); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("start replay error = %v, want not found", err)
		}
	})

	t.Run("claim made private after completion", func(t *testing.T) {
		tasks := testService(t, time.Minute)
		owner := HumanPrincipal("owner@example.com")
		agent := AgentPrincipal("agent:worker")
		created, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Human task"}, owner)
		if err != nil {
			t.Fatal(err)
		}
		agentVisibility := model.VisibilityAgent
		published, err := tasks.UpdateFor(t.Context(), created.ID, model.UpdateRequest{ExpectedVersion: created.Version, Visibility: &agentVisibility}, owner)
		if err != nil {
			t.Fatal(err)
		}
		request := model.ClaimRequest{ExpectedVersion: published.Version, IdempotencyKey: "claim-private-1234"}
		claimed, err := tasks.ClaimFor(t.Context(), published.ID, request, agent)
		if err != nil {
			t.Fatal(err)
		}
		completed, err := tasks.UpdateFor(t.Context(), claimed.Task.ID, model.UpdateRequest{ExpectedVersion: claimed.Task.Version, RunID: claimed.Run.ID, Status: model.TaskDone}, agent)
		if err != nil {
			t.Fatal(err)
		}
		privateVisibility := model.VisibilityPrivate
		if _, err := tasks.UpdateFor(t.Context(), completed.ID, model.UpdateRequest{ExpectedVersion: completed.Version, Visibility: &privateVisibility, CurrentNote: stringPointer("private follow-up")}, owner); err != nil {
			t.Fatal(err)
		}
		if _, err := tasks.ClaimFor(t.Context(), published.ID, request, agent); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("claim replay error = %v, want not found", err)
		}
	})
}

func TestConcurrentAgentRetriesCreateOnlyOneTask(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	request := model.CreateRequest{Title: "Concurrent retry", IdempotencyKey: "concurrent-12345678"}
	type result struct {
		task model.Task
		err  error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			task, err := tasks.CreateFor(t.Context(), request, agent)
			results <- result{task, err}
		}()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.task.ID != second.task.ID {
		t.Fatalf("concurrent retry results = %+v / %+v", first, second)
	}
	all, err := tasks.List(t.Context(), nil, 10)
	if err != nil || len(all) != 1 {
		t.Fatalf("created tasks = %+v, %v", all, err)
	}
}

func TestAgentConcurrencyAndRunDurationLimits(t *testing.T) {
	tasks := testService(t, time.Minute)
	policy := DefaultAgentPolicy()
	policy.MaxConcurrentRuns = 1
	agent := AgentPrincipalWithPolicy("agent:limited", policy)
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "First", Checklist: []string{"Run"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Second", Checklist: []string{"Run"}}, agent); !errors.Is(err, ErrRateLimit) {
		t.Fatalf("concurrency error = %v, want rate limit", err)
	}

	policy.MaxRunDuration = time.Nanosecond
	expired := AgentPrincipalWithPolicy("agent:limited", policy)
	if _, err := tasks.HeartbeatFor(t.Context(), started.Task.ID, started.Run.ID, expired); !errors.Is(err, ErrRateLimit) {
		t.Fatalf("run duration error = %v, want rate limit", err)
	}
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

func TestHeartbeatReportsProgressWithoutInferringCompletion(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Long checklist", Checklist: []string{"First", "Second"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-11 * time.Minute)
	if _, err := tasks.store.DB().ExecContext(t.Context(), `UPDATE agent_runs SET started_at=? WHERE id=?`, stamp(old), started.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.store.DB().ExecContext(t.Context(), `UPDATE checklist_items SET updated_at=? WHERE task_id=?`, stamp(old), started.Task.ID); err != nil {
		t.Fatal(err)
	}
	heartbeat, err := tasks.HeartbeatFor(t.Context(), started.Task.ID, started.Run.ID, agent)
	if err != nil {
		t.Fatal(err)
	}
	if !heartbeat.Progress.Stale || heartbeat.Progress.AgeSeconds < 10*60 || heartbeat.Progress.Hint == "" {
		t.Fatalf("stale progress = %+v", heartbeat.Progress)
	}
	unchanged, err := tasks.Get(t.Context(), started.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Items[0].Status != model.ItemActive || unchanged.Items[1].Status != model.ItemTodo {
		t.Fatalf("heartbeat inferred checklist state: %+v", unchanged.Items)
	}

	updated, err := tasks.UpdateFor(t.Context(), unchanged.ID, model.UpdateRequest{
		ExpectedVersion: unchanged.Version,
		RunID:           started.Run.ID,
		CompleteItemIDs: []string{unchanged.Items[0].ID},
		CurrentItemID:   unchanged.Items[1].ID,
	}, agent)
	if err != nil {
		t.Fatal(err)
	}
	heartbeat, err = tasks.HeartbeatFor(t.Context(), updated.ID, started.Run.ID, agent)
	if err != nil {
		t.Fatal(err)
	}
	if heartbeat.Progress.Stale || heartbeat.Progress.CompletedItems != 1 || heartbeat.Progress.CurrentItemID != updated.Items[1].ID {
		t.Fatalf("fresh progress = %+v", heartbeat.Progress)
	}
}

func TestTaskConversationDeliveryAndAcknowledgement(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	operator := HumanPrincipal("operator@example.com")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Directed work", Checklist: []string{"Work"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	version := started.Task.Version
	instruction, err := tasks.AddMessageFor(t.Context(), started.Task.ID, model.AddMessageRequest{
		TargetRunID: started.Run.ID, Kind: model.MessageInstruction, Body: "Run the focused tests", IdempotencyKey: "instruction-001",
	}, operator)
	if err != nil {
		t.Fatal(err)
	}
	if !instruction.RequiresAck || instruction.TargetRunID != started.Run.ID {
		t.Fatalf("instruction = %+v", instruction)
	}
	var eventPayload string
	if err := tasks.store.DB().QueryRowContext(t.Context(), `SELECT payload FROM events WHERE task_id=? AND kind='task.message_added'`, started.Task.ID).Scan(&eventPayload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(eventPayload, instruction.Body) {
		t.Fatalf("message body leaked into event payload: %s", eventPayload)
	}
	replayed, err := tasks.AddMessageFor(t.Context(), started.Task.ID, model.AddMessageRequest{
		TargetRunID: started.Run.ID, Kind: model.MessageInstruction, Body: "Run the focused tests", IdempotencyKey: "instruction-001",
	}, operator)
	if err != nil || replayed.ID != instruction.ID {
		t.Fatalf("replayed instruction = %+v, %v", replayed, err)
	}
	heartbeat, err := tasks.HeartbeatFor(t.Context(), started.Task.ID, started.Run.ID, agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeat.PendingMessages) != 1 || heartbeat.PendingMessages[0].ID != instruction.ID {
		t.Fatalf("heartbeat pending = %+v", heartbeat.PendingMessages)
	}
	pending, err := tasks.RecordMessageReceiptsFor(t.Context(), started.Task.ID, started.Run.ID, model.MessageReceiptRequest{
		ObservedMessageIDs: []string{instruction.ID}, IdempotencyKey: "receipt-observed-001",
	}, agent)
	if err != nil || len(pending) != 1 {
		t.Fatalf("observed instruction pending = %+v, %v", pending, err)
	}
	pending, err = tasks.RecordMessageReceiptsFor(t.Context(), started.Task.ID, started.Run.ID, model.MessageReceiptRequest{
		AcknowledgedMessageIDs: []string{instruction.ID}, IdempotencyKey: "receipt-acknowledged-001",
	}, agent)
	if err != nil || len(pending) != 0 {
		t.Fatalf("acknowledged instruction pending = %+v, %v", pending, err)
	}
	question, err := tasks.AddMessageFor(t.Context(), started.Task.ID, model.AddMessageRequest{
		AuthorRunID: started.Run.ID, Kind: model.MessageQuestion, Body: "Which environment should I validate?", IdempotencyKey: "question-001",
	}, agent)
	if err != nil || question.AuthorRunID != started.Run.ID {
		t.Fatalf("agent question = %+v, %v", question, err)
	}
	thread, err := tasks.ListMessagesFor(t.Context(), started.Task.ID, "", 10, operator)
	if err != nil || len(thread) != 2 {
		t.Fatalf("thread = %+v, %v", thread, err)
	}
	var received model.TaskMessage
	for _, message := range thread {
		if message.ID == instruction.ID {
			received = message
		}
	}
	if len(received.Receipts) != 1 || received.Receipts[0].AcknowledgedAt == nil {
		t.Fatalf("instruction receipts = %+v", received.Receipts)
	}
	unchanged, err := tasks.GetFor(t.Context(), started.Task.ID, operator)
	if err != nil || unchanged.Version != version {
		t.Fatalf("message changed task version: %d -> %d, %v", version, unchanged.Version, err)
	}
}

func TestTaskConversationAuthorizationAndValidation(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Guarded messages", Checklist: []string{"Work"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	viewer := HumanPrincipalWithRole("viewer@example.com", RoleViewer)
	if _, err := tasks.AddMessageFor(t.Context(), started.Task.ID, model.AddMessageRequest{Kind: model.MessageNote, Body: "No"}, viewer); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer message error = %v, want forbidden", err)
	}
	if _, err := tasks.AddMessageFor(t.Context(), started.Task.ID, model.AddMessageRequest{AuthorRunID: started.Run.ID, Kind: model.MessageInstruction, Body: "No"}, agent); !errors.Is(err, ErrValidation) {
		t.Fatalf("agent instruction error = %v, want validation", err)
	}
	other := AgentPrincipal("agent:other")
	if _, err := tasks.ListMessagesFor(t.Context(), started.Task.ID, "", 10, other); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other agent list error = %v, want forbidden", err)
	}
	withoutMessaging := DefaultAgentPolicy()
	delete(withoutMessaging.Capabilities, CapabilityTaskMessage)
	restricted := AgentPrincipalWithPolicy("agent:worker", withoutMessaging)
	if _, err := tasks.ListMessagesFor(t.Context(), started.Task.ID, "", 10, restricted); !errors.Is(err, ErrForbidden) {
		t.Fatalf("restricted agent list error = %v, want forbidden", err)
	}
	heartbeat, err := tasks.HeartbeatFor(t.Context(), started.Task.ID, started.Run.ID, restricted)
	if err != nil || len(heartbeat.PendingMessages) != 0 {
		t.Fatalf("restricted heartbeat messages = %+v, %v", heartbeat.PendingMessages, err)
	}
}

func TestAgentRunCallsignsAreFriendlyUniqueAndOperatorRenameIsDisplayOnly(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	first, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "First run", Checklist: []string{"Work"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	second, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Second run", Checklist: []string{"Work"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if first.Run.Callsign == "" || second.Run.Callsign == "" || strings.EqualFold(first.Run.Callsign, second.Run.Callsign) {
		t.Fatalf("run callsigns = %q / %q", first.Run.Callsign, second.Run.Callsign)
	}
	if _, err := tasks.RenameRunFor(t.Context(), second.Task.ID, second.Run.ID, model.RenameRunRequest{ExpectedVersion: second.Task.Version, Callsign: strings.ToLower(first.Run.Callsign)}, HumanPrincipal("operator@example.com")); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate rename error = %v, want conflict", err)
	}
	if _, err := tasks.RenameRunFor(t.Context(), first.Task.ID, first.Run.ID, model.RenameRunRequest{ExpectedVersion: first.Task.Version, Callsign: "North Star"}, agent); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent rename error = %v, want forbidden", err)
	}
	renamed, err := tasks.RenameRunFor(t.Context(), first.Task.ID, first.Run.ID, model.RenameRunRequest{ExpectedVersion: first.Task.Version, Callsign: "North Star"}, HumanPrincipal("operator@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Runs[0].Callsign != "North Star" || renamed.Runs[0].Agent != first.Run.Agent || renamed.Runs[0].Client != first.Run.Client || renamed.Runs[0].ID != first.Run.ID || renamed.Runs[0].Tone != first.Run.Tone {
		t.Fatalf("renamed run changed trusted identity: before=%+v after=%+v", first.Run, renamed.Runs[0])
	}
	if renamed.Version != first.Task.Version+1 {
		t.Fatalf("renamed task version = %d, want %d", renamed.Version, first.Task.Version+1)
	}
}

func TestAgentSessionReusesCallsignAcrossTaskRuns(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	first, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "First task", Checklist: []string{"Work"}, AgentSessionKey: "switchboard-session-1"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	second, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Second task", Checklist: []string{"Work"}, AgentSessionKey: "switchboard-session-1"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if first.Run.ID == second.Run.ID || first.Run.TaskID == second.Run.TaskID {
		t.Fatalf("task runs were not independent: first=%+v second=%+v", first.Run, second.Run)
	}
	if first.Run.SessionID == "" || first.Run.SessionID != second.Run.SessionID {
		t.Fatalf("agent session IDs = %q / %q", first.Run.SessionID, second.Run.SessionID)
	}
	if first.Run.Callsign != second.Run.Callsign || first.Run.Tone != second.Run.Tone {
		t.Fatalf("agent session presentation differs: first=%+v second=%+v", first.Run, second.Run)
	}

	third, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Other session", Checklist: []string{"Work"}, AgentSessionKey: "switchboard-session-2"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if third.Run.SessionID == first.Run.SessionID || strings.EqualFold(third.Run.Callsign, first.Run.Callsign) {
		t.Fatalf("distinct agent sessions were conflated: first=%+v third=%+v", first.Run, third.Run)
	}
	otherPrincipal, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Other principal", Checklist: []string{"Work"}, AgentSessionKey: "switchboard-session-1"}, AgentPrincipal("agent:other"))
	if err != nil {
		t.Fatal(err)
	}
	if otherPrincipal.Run.SessionID == first.Run.SessionID {
		t.Fatalf("session key crossed authenticated principals: first=%+v other=%+v", first.Run, otherPrincipal.Run)
	}
	var storedKeyHash string
	if err := tasks.store.DB().QueryRowContext(t.Context(), `SELECT key_hash FROM agent_sessions WHERE id=?`, first.Run.SessionID).Scan(&storedKeyHash); err != nil {
		t.Fatal(err)
	}
	if storedKeyHash == "switchboard-session-1" || len(storedKeyHash) != 64 {
		t.Fatalf("stored session key hash = %q", storedKeyHash)
	}

	renamed, err := tasks.RenameRunFor(t.Context(), first.Task.ID, first.Run.ID, model.RenameRunRequest{ExpectedVersion: first.Task.Version, Callsign: "North Star"}, HumanPrincipal("operator@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	otherTask, err := tasks.Get(t.Context(), second.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Runs[0].Callsign != "North Star" || otherTask.Runs[0].Callsign != "North Star" {
		t.Fatalf("session rename did not propagate: renamed=%q other=%q", renamed.Runs[0].Callsign, otherTask.Runs[0].Callsign)
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
	visible, err := tasks.ListFor(t.Context(), model.ListTasksRequest{Limit: 100}, HumanPrincipal("alice@example.com"))
	if err != nil || len(visible.Tasks) != 1 || visible.Tasks[0].ID != created.ID {
		t.Fatalf("creator list = %+v, %v", visible, err)
	}
	hidden, err := tasks.ListFor(t.Context(), model.ListTasksRequest{Limit: 100}, HumanPrincipal("bob@example.com"))
	if err != nil || len(hidden.Tasks) != 0 {
		t.Fatalf("other user list = %+v, %v", hidden, err)
	}
}

func TestSharedTaskEditsRecordAuthenticatedPrincipal(t *testing.T) {
	tasks := testService(t, time.Minute)
	alice := HumanPrincipal("alice@example.com")
	bob := HumanPrincipal("bob@example.com")
	created, err := tasks.CreateFor(t.Context(), model.CreateRequest{
		Title: "Pickup", Visibility: model.VisibilityAgent, Checklist: []string{"Do it"},
	}, alice)
	if err != nil {
		t.Fatal(err)
	}
	if created.CreatedBy != alice.ID || created.LastEditedBy != alice.ID {
		t.Fatalf("creation provenance = creator %q editor %q", created.CreatedBy, created.LastEditedBy)
	}

	summary := "Updated by a teammate"
	edited, err := tasks.UpdateFor(t.Context(), created.ID, model.UpdateRequest{
		ExpectedVersion: created.Version,
		Summary:         &summary,
	}, bob)
	if err != nil {
		t.Fatal(err)
	}
	if edited.CreatedBy != alice.ID || edited.LastEditedBy != bob.ID {
		t.Fatalf("edit provenance = creator %q editor %q", edited.CreatedBy, edited.LastEditedBy)
	}

	claimed, err := tasks.ClaimFor(t.Context(), edited.ID, model.ClaimRequest{ExpectedVersion: edited.Version}, AgentPrincipal("codex"))
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Task.LastEditedBy != bob.ID {
		t.Fatalf("claim changed last editor to %q", claimed.Task.LastEditedBy)
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
