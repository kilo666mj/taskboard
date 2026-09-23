package service

import (
	"errors"
	"github.com/kilo666mj/taskboard/internal/model"
	"testing"
	"time"
)

func TestWorkerMatchingIsSeparateFromAuthorization(t *testing.T) {
	tasks := testService(t, time.Minute)
	creator := HumanPrincipal("human:owner")
	reviewer := HumanPrincipal("human:reviewer")
	task, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Kubernetes work", Visibility: model.VisibilityAgent}, creator)
	if err != nil {
		t.Fatal(err)
	}
	task, err = tasks.SetTaskRequirementsFor(t.Context(), task.ID, model.SetTaskRequirementsRequest{Requirements: []string{"repo:org/app", "kubernetes"}, ExpectedVersion: task.Version}, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	if task.LastEditedBy != reviewer.ID {
		t.Fatalf("requirements editor = %q, want %q", task.LastEditedBy, reviewer.ID)
	}
	worker := AgentPrincipal("agent:worker")
	items, err := tasks.ListFor(t.Context(), []model.TaskStatus{model.TaskQueued}, 10, worker)
	if err != nil || len(items) != 0 {
		t.Fatalf("unadvertised items = %+v, %v", items, err)
	}
	if _, err = tasks.AdvertiseWorkerFor(t.Context(), model.AdvertiseWorkerRequest{Capabilities: []string{"repo:org/app"}, Capacity: 2, TTLSeconds: 60}, worker); err != nil {
		t.Fatal(err)
	}
	if _, err = tasks.ClaimFor(t.Context(), task.ID, model.ClaimRequest{ExpectedVersion: task.Version, IdempotencyKey: "missing-match"}, worker); !errors.Is(err, ErrValidation) {
		t.Fatalf("missing match claim = %v", err)
	}
	if _, err = tasks.AdvertiseWorkerFor(t.Context(), model.AdvertiseWorkerRequest{Capabilities: []string{"repo:org/app", "kubernetes", "task:claim"}, Capacity: 2, TTLSeconds: 60}, worker); err != nil {
		t.Fatal(err)
	}
	items, err = tasks.ListFor(t.Context(), []model.TaskStatus{model.TaskQueued}, 10, worker)
	if err != nil || len(items) != 1 {
		t.Fatalf("matched items = %+v, %v", items, err)
	}
	policy := DefaultAgentPolicy()
	delete(policy.Capabilities, CapabilityTaskClaim)
	unauthorized := AgentPrincipalWithPolicy("agent:no-claim", policy)
	if _, err = tasks.AdvertiseWorkerFor(t.Context(), model.AdvertiseWorkerRequest{Capabilities: []string{"repo:org/app", "kubernetes"}, Capacity: 2, TTLSeconds: 60}, unauthorized); err != nil {
		t.Fatal(err)
	}
	if _, err = tasks.ClaimFor(t.Context(), task.ID, model.ClaimRequest{ExpectedVersion: task.Version, IdempotencyKey: "no-claim-auth"}, unauthorized); !errors.Is(err, ErrForbidden) {
		t.Fatalf("security capability error = %v", err)
	}
	if _, err = tasks.ClaimFor(t.Context(), task.ID, model.ClaimRequest{ExpectedVersion: task.Version, IdempotencyKey: "matched-claim"}, worker); err != nil {
		t.Fatal(err)
	}
}
