package service

import (
	"github.com/kilo666mj/taskboard/internal/model"
	"testing"
	"time"
)

func TestAnalyticsAggregatesNumericUsageAndDeletionCascades(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	started, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Measured", Checklist: []string{"Ship"}, IdempotencyKey: "analytics-start"}, agent)
	if err != nil {
		t.Fatal(err)
	}
	request := model.RecordUsageRequest{RunID: started.Run.ID, Provider: "openai", Model: "model-x", InputTokens: 100, OutputTokens: 25, EstimatedCostMicros: 1234, IdempotencyKey: "analytics-usage"}
	record, err := tasks.RecordUsageFor(t.Context(), started.Task.ID, request, agent)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := tasks.RecordUsageFor(t.Context(), started.Task.ID, request, agent)
	if err != nil || replayed.ID != record.ID {
		t.Fatalf("replay = %+v, %v", replayed, err)
	}
	current, err := tasks.GetFor(t.Context(), started.Task.ID, agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tasks.UpdateFor(t.Context(), current.ID, model.UpdateRequest{ExpectedVersion: current.Version, RunID: started.Run.ID, Status: model.TaskDone, CompleteItemIDs: []string{current.Items[0].ID}, IdempotencyKey: "analytics-done"}, agent); err != nil {
		t.Fatal(err)
	}
	summary, err := tasks.AnalyticsFor(t.Context(), 30, HumanPrincipalWithRole("human:owner", RoleOwner))
	if err != nil {
		t.Fatal(err)
	}
	if summary.TasksDone != 1 || summary.Runs != 1 || summary.InputTokens != 100 || summary.OutputTokens != 25 || summary.EstimatedCostMicros != 1234 || summary.CompletionRate != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if _, err = tasks.store.DB().ExecContext(t.Context(), `DELETE FROM tasks WHERE id=?`, started.Task.ID); err != nil {
		t.Fatal(err)
	}
	summary, err = tasks.AnalyticsFor(t.Context(), 30, HumanPrincipalWithRole("human:owner", RoleOwner))
	if err != nil {
		t.Fatal(err)
	}
	if summary.TasksCreated != 0 || summary.InputTokens != 0 {
		t.Fatalf("post-delete summary = %+v", summary)
	}
}
