package service

import (
	"errors"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func TestTaskReferenceLifecycleAndSafeURLValidation(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Reference delivery", Checklist: []string{"Ship"}, IdempotencyKey: "reference-start"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	request := model.AddTaskReferenceRequest{RunID: started.Run.ID, Kind: model.ReferencePullRequest, Label: "PR #42", Locator: "org/repo#42", URL: "https://github.com/org/repo/pull/42", IdempotencyKey: "reference-pr"}
	created, err := tasks.AddTaskReferenceFor(t.Context(), started.Task.ID, request, agent)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := tasks.AddTaskReferenceFor(t.Context(), started.Task.ID, request, agent)
	if err != nil || replayed.ID != created.ID {
		t.Fatalf("replayed reference = %+v, %v", replayed, err)
	}
	if created.Provenance != model.ReferenceByAgent || created.CreatedBy != agent.ID || created.RunID != started.Run.ID {
		t.Fatalf("reference provenance = %+v", created)
	}
	items, err := tasks.ListTaskReferencesFor(t.Context(), started.Task.ID, agent)
	if err != nil || len(items) != 1 {
		t.Fatalf("references = %+v, %v", items, err)
	}
	milestones, err := tasks.DeliveryMilestonesFor(t.Context(), started.Task.ID, agent)
	if err != nil || len(milestones) != 2 || milestones[0].Kind != "claimed" || milestones[1].Kind != "pull_request_opened" || milestones[1].ReferenceID != created.ID {
		t.Fatalf("milestones = %+v, %v", milestones, err)
	}
	for _, unsafe := range []string{"javascript:alert(1)", "http://github.com/org/repo", "https://user:pass@example.com/item", "file:///tmp/output"} {
		_, err := tasks.AddTaskReferenceFor(t.Context(), started.Task.ID, model.AddTaskReferenceRequest{RunID: started.Run.ID, Kind: model.ReferenceReview, Label: "Unsafe", URL: unsafe, IdempotencyKey: "unsafe-" + unsafe}, agent)
		if !errors.Is(err, ErrValidation) {
			t.Errorf("URL %q error = %v, want validation", unsafe, err)
		}
	}
}

func TestHumanReferenceCannotClaimAgentProvenance(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Human reference", Checklist: []string{"Ship"}, IdempotencyKey: "human-reference-start"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.AddTaskReferenceFor(t.Context(), started.Task.ID, model.AddTaskReferenceRequest{RunID: started.Run.ID, Kind: model.ReferenceLinear, Label: "ENG-42", Locator: "ENG-42"}, HumanPrincipal("human:operator")); !errors.Is(err, ErrValidation) {
		t.Fatalf("human run provenance error = %v, want validation", err)
	}
	created, err := tasks.AddTaskReferenceFor(t.Context(), started.Task.ID, model.AddTaskReferenceRequest{Kind: model.ReferenceLinear, Label: "ENG-42", Locator: "ENG-42"}, HumanPrincipal("human:operator"))
	if err != nil || created.Provenance != model.ReferenceByHuman || created.RunID != "" {
		t.Fatalf("human reference = %+v, %v", created, err)
	}
}
