package service

import (
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func TestRunHandoffLifecycleAndDerivedFinal(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Handoff", Checklist: []string{"Implement"}, IdempotencyKey: "handoff-start"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	request := model.AddRunHandoffRequest{RunID: started.Run.ID, Kind: model.HandoffCheckpoint, LastCompletedStep: "Scaffolded", Branch: "feature/handoff", Validation: []string{"go test ./..."}, NextAction: "Finish API", IdempotencyKey: "handoff-checkpoint"}
	created, err := tasks.AddRunHandoffFor(t.Context(), started.Task.ID, request, agent)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := tasks.AddRunHandoffFor(t.Context(), started.Task.ID, request, agent)
	if err != nil || replayed.ID != created.ID {
		t.Fatalf("replay = %+v, %v", replayed, err)
	}
	updated, err := tasks.UpdateFor(t.Context(), started.Task.ID, model.UpdateRequest{ExpectedVersion: started.Task.Version, RunID: started.Run.ID, Status: model.TaskDone, CompleteItemIDs: []string{started.Task.Items[0].ID}, IdempotencyKey: "handoff-done"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskDone {
		t.Fatalf("status = %s", updated.Status)
	}
	items, err := tasks.ListRunHandoffsFor(t.Context(), started.Task.ID, agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Kind != model.HandoffFinal || items[1].ID != created.ID {
		t.Fatalf("handoffs = %+v", items)
	}
}
