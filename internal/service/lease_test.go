package service

import (
	"errors"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

// leaseOnlyPolicy drops every capability a live run lease stands in for, so
// these calls succeed only through the lease.
func leaseOnlyPolicy() AgentPolicy {
	policy := DefaultAgentPolicy()
	for _, capability := range []string{CapabilityTaskUpdate, CapabilityTaskComplete, CapabilityTaskMessage, CapabilityTaskEscalate, CapabilityTaskHandoff, CapabilityTaskReference, CapabilityTaskEvidence, CapabilityTaskCreate, CapabilityTaskSkip} {
		delete(policy.Capabilities, capability)
	}
	return policy
}

func startLeaseTask(t *testing.T, tasks *Service, agent Principal, key string) model.StartResult {
	t.Helper()
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: key, Checklist: []string{"Ship"}, IdempotencyKey: key}, agent)
	if err != nil {
		t.Fatal(err)
	}
	return started
}

func note(value string) *string { return &value }

func TestLeaseAuthorizesOwnTaskWritesWithoutCapabilities(t *testing.T) {
	tasks := testService(t, time.Minute)
	full := AgentPrincipal("agent:runner")
	leased := AgentPrincipalWithPolicy(full.ID, leaseOnlyPolicy())
	human := HumanPrincipal("human:owner")
	started := startLeaseTask(t, tasks, full, "lease-allowed")
	taskID, runID := started.Task.ID, started.Run.ID

	updated, err := tasks.UpdateFor(t.Context(), taskID, model.UpdateRequest{RunID: runID, ExpectedVersion: started.Task.Version, CurrentNote: note("Working")}, leased)
	if err != nil {
		t.Fatalf("leased update: %v", err)
	}
	if _, err := tasks.AddMessageFor(t.Context(), taskID, model.AddMessageRequest{AuthorRunID: runID, Kind: model.MessageNote, Body: "Progress"}, leased); err != nil {
		t.Fatalf("leased message: %v", err)
	}
	if _, err := tasks.CreateEscalationFor(t.Context(), taskID, model.CreateEscalationRequest{RunID: runID, ExpectedVersion: updated.Version, Question: "Any preference?", IdempotencyKey: "lease-escalate"}, leased); err != nil {
		t.Fatalf("leased escalation: %v", err)
	}
	if _, err := tasks.AddRunHandoffFor(t.Context(), taskID, model.AddRunHandoffRequest{RunID: runID, NextAction: "Ship it"}, leased); err != nil {
		t.Fatalf("leased handoff: %v", err)
	}
	reference, err := tasks.AddTaskReferenceFor(t.Context(), taskID, model.AddTaskReferenceRequest{RunID: runID, Kind: model.ReferenceBranch, Label: "Branch", Locator: "feat/lease"}, leased)
	if err != nil {
		t.Fatalf("leased reference: %v", err)
	}
	current, err := tasks.GetFor(t.Context(), taskID, full)
	if err != nil {
		t.Fatal(err)
	}
	requirement, err := tasks.CreateCompletionRequirementFor(t.Context(), taskID, model.CreateCompletionRequirementRequest{Kind: model.RequirementCustom, Label: "Branch", ExpectedVersion: current.Version}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.SubmitCompletionEvidenceFor(t.Context(), taskID, requirement.ID, model.SubmitCompletionEvidenceRequest{RunID: runID, ReferenceID: reference.ID}, leased); err != nil {
		t.Fatalf("leased evidence: %v", err)
	}

	// Cancelling, marking a duplicate and skipping stay identity-gated.
	if current, err = tasks.GetFor(t.Context(), taskID, full); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateFor(t.Context(), taskID, model.UpdateRequest{RunID: runID, ExpectedVersion: current.Version, Status: model.TaskCancelled}, leased); !errors.Is(err, ErrForbidden) {
		t.Fatalf("leased cancel error = %v, want forbidden", err)
	}
	if _, err := tasks.UpdateFor(t.Context(), taskID, model.UpdateRequest{RunID: runID, ExpectedVersion: current.Version, SkipItemIDs: []string{current.Items[0].ID}, SkipReason: "n/a"}, leased); !errors.Is(err, ErrForbidden) {
		t.Fatalf("leased skip error = %v, want forbidden", err)
	}
	// So does creating work.
	if _, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "More", IdempotencyKey: "lease-start"}, leased); !errors.Is(err, ErrForbidden) {
		t.Fatalf("start without task:create = %v, want forbidden", err)
	}

	complete := model.UpdateRequest{RunID: runID, ExpectedVersion: current.Version, Status: model.TaskDone, CompleteItemIDs: []string{current.Items[0].ID}, IdempotencyKey: "lease-complete"}
	done, err := tasks.UpdateFor(t.Context(), taskID, complete, leased)
	if err != nil || done.Status != model.TaskDone {
		t.Fatalf("leased complete = %+v, %v", done.Status, err)
	}
	// A retried completion replays even though completing ended the lease.
	if replayed, err := tasks.UpdateFor(t.Context(), taskID, complete, leased); err != nil || replayed.Status != model.TaskDone {
		t.Fatalf("replayed leased complete = %+v, %v", replayed.Status, err)
	}
	// Completing ends the run, and with it the lease.
	if _, err := tasks.UpdateFor(t.Context(), taskID, model.UpdateRequest{RunID: runID, ExpectedVersion: done.Version, CurrentNote: note("After")}, leased); !errors.Is(err, ErrForbidden) {
		t.Fatalf("update after run ended = %v, want forbidden", err)
	}
	if _, err := tasks.AddRunHandoffFor(t.Context(), taskID, model.AddRunHandoffRequest{RunID: runID, NextAction: "Nothing"}, leased); err == nil {
		t.Fatal("handoff after run ended was accepted")
	}
}

func TestLeaseDoesNotReachOtherTasksPrincipalsOrLapsedRuns(t *testing.T) {
	tasks := testService(t, time.Minute)
	full := AgentPrincipal("agent:runner")
	leased := AgentPrincipalWithPolicy(full.ID, leaseOnlyPolicy())
	first := startLeaseTask(t, tasks, full, "lease-first")
	second := startLeaseTask(t, tasks, full, "lease-second")

	// The second task's run does not authorize writes to the first task.
	if _, err := tasks.UpdateFor(t.Context(), first.Task.ID, model.UpdateRequest{RunID: second.Run.ID, ExpectedVersion: first.Task.Version, CurrentNote: note("Wrong task")}, leased); !errors.Is(err, ErrForbidden) {
		t.Fatalf("update with another task's run = %v, want forbidden", err)
	}
	if _, err := tasks.UpdateFor(t.Context(), first.Task.ID, model.UpdateRequest{ExpectedVersion: first.Task.Version, CurrentNote: note("No run")}, leased); !errors.Is(err, ErrForbidden) {
		t.Fatalf("update without a run = %v, want forbidden", err)
	}
	if _, err := tasks.AddMessageFor(t.Context(), first.Task.ID, model.AddMessageRequest{AuthorRunID: second.Run.ID, Kind: model.MessageNote, Body: "Wrong task"}, leased); err == nil {
		t.Fatal("message with another task's run was accepted")
	}

	// Another principal cannot use this principal's run.
	other := AgentPrincipalWithPolicy("agent:other", leaseOnlyPolicy())
	if _, err := tasks.UpdateFor(t.Context(), first.Task.ID, model.UpdateRequest{RunID: first.Run.ID, ExpectedVersion: first.Task.Version, CurrentNote: note("Not mine")}, other); !errors.Is(err, ErrForbidden) {
		t.Fatalf("update with another principal's run = %v, want forbidden", err)
	}
	if _, err := tasks.AddRunHandoffFor(t.Context(), first.Task.ID, model.AddRunHandoffRequest{RunID: first.Run.ID, NextAction: "Not mine"}, other); !errors.Is(err, ErrForbidden) {
		t.Fatalf("handoff with another principal's run = %v, want forbidden", err)
	}

	// A lapsed lease is refused before the sweep marks the run stale.
	if _, err := tasks.store.DB().ExecContext(t.Context(), `UPDATE agent_runs SET lease_expires_at=? WHERE id=?`, stamp(time.Now().Add(-time.Second)), first.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateFor(t.Context(), first.Task.ID, model.UpdateRequest{RunID: first.Run.ID, ExpectedVersion: first.Task.Version, CurrentNote: note("Lapsed")}, leased); !errors.Is(err, ErrForbidden) {
		t.Fatalf("update after lease expiry = %v, want forbidden", err)
	}
	for name, call := range map[string]func() error{
		"message": func() error {
			_, err := tasks.AddMessageFor(t.Context(), first.Task.ID, model.AddMessageRequest{AuthorRunID: first.Run.ID, Kind: model.MessageNote, Body: "Lapsed"}, leased)
			return err
		},
		"escalate": func() error {
			_, err := tasks.CreateEscalationFor(t.Context(), first.Task.ID, model.CreateEscalationRequest{RunID: first.Run.ID, ExpectedVersion: first.Task.Version, Question: "Lapsed?", IdempotencyKey: "lapsed-escalate"}, leased)
			return err
		},
		"handoff": func() error {
			_, err := tasks.AddRunHandoffFor(t.Context(), first.Task.ID, model.AddRunHandoffRequest{RunID: first.Run.ID, NextAction: "Lapsed"}, leased)
			return err
		},
		"reference": func() error {
			_, err := tasks.AddTaskReferenceFor(t.Context(), first.Task.ID, model.AddTaskReferenceRequest{RunID: first.Run.ID, Kind: model.ReferenceBranch, Label: "Lapsed", Locator: "lapsed"}, leased)
			return err
		},
	} {
		if err := call(); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s after lease expiry = %v, want forbidden", name, err)
		}
	}

	// The capability path still works without a live lease.
	if _, err := tasks.UpdateFor(t.Context(), first.Task.ID, model.UpdateRequest{RunID: first.Run.ID, ExpectedVersion: first.Task.Version, CurrentNote: note("By capability")}, full); err != nil {
		t.Fatalf("capability update with a lapsed lease: %v", err)
	}
}

func TestLeaseRenewsOnlyWhileLive(t *testing.T) {
	tasks := testService(t, time.Minute)
	full := AgentPrincipal("agent:runner")
	leased := AgentPrincipalWithPolicy(full.ID, leaseOnlyPolicy())
	started := startLeaseTask(t, tasks, full, "lease-heartbeat")
	taskID, runID := started.Task.ID, started.Run.ID

	renewed, err := tasks.HeartbeatFor(t.Context(), taskID, runID, leased)
	if err != nil {
		t.Fatalf("heartbeat on a live lease without task:update: %v", err)
	}
	if !renewed.Run.LeaseExpires.After(started.Run.LeaseExpires) {
		t.Fatalf("lease not extended: %s -> %s", started.Run.LeaseExpires, renewed.Run.LeaseExpires)
	}
	other := AgentPrincipalWithPolicy("agent:other", leaseOnlyPolicy())
	if _, err := tasks.HeartbeatFor(t.Context(), taskID, runID, other); err == nil {
		t.Fatal("another principal renewed this principal's run")
	}

	if _, err := tasks.store.DB().ExecContext(t.Context(), `UPDATE agent_runs SET lease_expires_at=? WHERE id=?`, stamp(time.Now().Add(-time.Second)), runID); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.HeartbeatFor(t.Context(), taskID, runID, leased); !errors.Is(err, ErrForbidden) {
		t.Fatalf("heartbeat after lease expiry = %v, want forbidden", err)
	}
	if _, err := tasks.UpdateFor(t.Context(), taskID, model.UpdateRequest{RunID: runID, ExpectedVersion: started.Task.Version, CurrentNote: note("Lapsed")}, leased); !errors.Is(err, ErrForbidden) {
		t.Fatalf("update after refused heartbeat = %v, want forbidden", err)
	}
}

func TestLeaseHolderClearsTheMessagesItsHeartbeatDelivers(t *testing.T) {
	tasks := testService(t, time.Minute)
	full := AgentPrincipal("agent:runner")
	leased := AgentPrincipalWithPolicy(full.ID, leaseOnlyPolicy())
	started := startLeaseTask(t, tasks, full, "lease-receipts")
	message, err := tasks.AddMessageFor(t.Context(), started.Task.ID, model.AddMessageRequest{Kind: model.MessageNote, Body: "Heads up"}, HumanPrincipal("human:owner"))
	if err != nil {
		t.Fatal(err)
	}
	beat, err := tasks.HeartbeatFor(t.Context(), started.Task.ID, started.Run.ID, leased)
	if err != nil || len(beat.PendingMessages) != 1 {
		t.Fatalf("pending on heartbeat = %+v, %v", beat.PendingMessages, err)
	}
	if _, err := tasks.RecordMessageReceiptsFor(t.Context(), started.Task.ID, started.Run.ID, model.MessageReceiptRequest{AcknowledgedMessageIDs: []string{message.ID}}, leased); err != nil {
		t.Fatalf("leased receipt: %v", err)
	}
	if beat, err = tasks.HeartbeatFor(t.Context(), started.Task.ID, started.Run.ID, leased); err != nil || len(beat.PendingMessages) != 0 {
		t.Fatalf("pending after receipt = %+v, %v", beat.PendingMessages, err)
	}

	if _, err := tasks.store.DB().ExecContext(t.Context(), `UPDATE agent_runs SET lease_expires_at=? WHERE id=?`, stamp(time.Now().Add(-time.Second)), started.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.RecordMessageReceiptsFor(t.Context(), started.Task.ID, started.Run.ID, model.MessageReceiptRequest{ObservedMessageIDs: []string{message.ID}}, leased); !errors.Is(err, ErrForbidden) {
		t.Fatalf("receipt after lease expiry = %v, want forbidden", err)
	}
}
