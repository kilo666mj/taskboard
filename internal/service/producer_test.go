package service

import (
	"errors"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func ptr[T any](v T) *T { return &v }

func TestProducerMaintainsItsUnclaimedTask(t *testing.T) {
	tasks := testService(t, time.Minute)
	policy := DefaultAgentPolicy()
	policy.Capabilities[CapabilityTaskSensitive] = true
	producer := AgentPrincipalWithPolicy("agent:producer", policy)
	other := AgentPrincipalWithPolicy("agent:other", policy)

	task, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Update redis on alpha", Visibility: model.VisibilityAgent,
		Checklist: []string{"Investigate", "Apply"}}, producer)
	if err != nil {
		t.Fatal(err)
	}

	// The producer may refresh wording, priority and the note.
	task, err = tasks.UpdateFor(t.Context(), task.ID, model.UpdateRequest{ExpectedVersion: task.Version,
		Summary: ptr("now fixes 3 critical"), Priority: ptr(model.Priority("high")), CurrentNote: ptr("refreshed")}, producer)
	if err != nil {
		t.Fatalf("producer refresh: %v", err)
	}
	if task.Summary != "now fixes 3 critical" {
		t.Fatalf("summary = %q", task.Summary)
	}

	// Another agent may not, and the producer may not touch work fields.
	if _, err := tasks.UpdateFor(t.Context(), task.ID, model.UpdateRequest{ExpectedVersion: task.Version, Summary: ptr("x")}, other); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other agent update: err = %v, want forbidden", err)
	}
	for name, request := range map[string]model.UpdateRequest{
		"complete item": {CompleteItemIDs: []string{task.Items[0].ID}},
		"add item":      {AddItems: []string{"More"}},
		"owner":         {Owner: ptr("agent:producer")},
		"status done":   {Status: model.TaskDone},
		"status active": {Status: model.TaskActive},
		"defer":         {DeferUntil: ptr("2030-01-01T00:00:00Z")},
	} {
		request.ExpectedVersion = task.Version
		if _, err := tasks.UpdateFor(t.Context(), task.ID, request, producer); !errors.Is(err, ErrForbidden) {
			t.Errorf("producer %s: err = %v, want forbidden", name, err)
		}
	}

	// Cancelling still needs task:sensitive.
	plain := AgentPrincipal("agent:producer")
	if _, err := tasks.UpdateFor(t.Context(), task.ID, model.UpdateRequest{ExpectedVersion: task.Version, Status: model.TaskCancelled}, plain); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cancel without task:sensitive: err = %v, want forbidden", err)
	}
	cancelled, err := tasks.UpdateFor(t.Context(), task.ID, model.UpdateRequest{ExpectedVersion: task.Version,
		Status: model.TaskCancelled, CurrentNote: ptr("Resolved: the image was updated")}, producer)
	if err != nil {
		t.Fatalf("producer cancel: %v", err)
	}
	if cancelled.Status != model.TaskCancelled {
		t.Fatalf("status = %s", cancelled.Status)
	}
}

func TestProducerLosesRightsOnceClaimed(t *testing.T) {
	tasks := testService(t, time.Minute)
	policy := DefaultAgentPolicy()
	policy.Capabilities[CapabilityTaskSensitive] = true
	producer := AgentPrincipalWithPolicy("agent:producer", policy)
	worker := AgentPrincipal("agent:worker")

	task, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Update postgres on bravo", Visibility: model.VisibilityAgent,
		Checklist: []string{"Investigate"}}, producer)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := tasks.ClaimFor(t.Context(), task.ID, model.ClaimRequest{ExpectedVersion: task.Version}, worker)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	for name, request := range map[string]model.UpdateRequest{
		"summary": {Summary: ptr("changed under the worker")},
		"cancel":  {Status: model.TaskCancelled},
	} {
		request.ExpectedVersion = claimed.Task.Version
		if _, err := tasks.UpdateFor(t.Context(), task.ID, request, producer); !errors.Is(err, ErrForbidden) {
			t.Errorf("producer %s after claim: err = %v, want forbidden", name, err)
		}
	}
}
