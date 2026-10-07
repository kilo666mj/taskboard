package service

import (
	"github.com/kilo666mj/taskboard/internal/model"
	"testing"
	"time"
)

func TestWaitingAndBlockedUpdatesEndTheRun(t *testing.T) {
	for _, status := range []model.TaskStatus{model.TaskWaiting, model.TaskBlocked} {
		t.Run(string(status), func(t *testing.T) {
			s := testService(t, time.Minute)
			started, err := s.Start(t.Context(), model.StartRequest{Title: "External job", Checklist: []string{"Verify result"}}, "agent:worker")
			if err != nil {
				t.Fatal(err)
			}
			updated, err := s.Update(t.Context(), started.Task.ID, model.UpdateRequest{ExpectedVersion: started.Task.Version, RunID: started.Run.ID, Status: status, WaitingFor: ptr("External approval"), Blocker: ptr("Needs inspection")}, "agent:worker")
			if err != nil {
				t.Fatal(err)
			}
			run := updated.Runs[0]
			if run.EndedAt == nil || run.LeaseExpires.After(time.Now()) || run.Status != status {
				t.Fatalf("run remains live: %+v", run)
			}
			if updated.CompletedAt != nil {
				t.Fatal("waiting marked incident complete")
			}
		})
	}
}

func TestIncidentCanResumeLegacyCompletedQuestion(t *testing.T) {
	s := testService(t, time.Minute)
	agent := AgentPrincipal("agent:controller")
	started, err := s.StartFor(t.Context(), model.StartRequest{Title: "Incident", Checklist: []string{"Answer question"}, Visibility: model.VisibilityTeam}, agent)
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.UpdateFor(t.Context(), started.Task.ID, model.UpdateRequest{ExpectedVersion: started.Task.Version, RunID: started.Run.ID, Status: model.TaskDone, CompleteItemIDs: []string{started.Task.Items[0].ID}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := s.UpdateFor(t.Context(), done.ID, model.UpdateRequest{ExpectedVersion: done.Version, Status: model.TaskQueued, AddItems: []string{"Verify incident outcome"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := s.ClaimFor(t.Context(), queued.ID, model.ClaimRequest{ExpectedVersion: queued.Version}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Run.ID == started.Run.ID || resumed.Task.Items[0].Status != model.ItemDone || len(resumed.Task.Items) != 2 {
		t.Fatalf("lost incident history: %+v", resumed)
	}
	waiting, err := s.UpdateFor(t.Context(), resumed.Task.ID, model.UpdateRequest{ExpectedVersion: resumed.Task.Version, RunID: resumed.Run.ID, Status: model.TaskWaiting, WaitingFor: ptr("External approval")}, agent)
	if err != nil {
		t.Fatal(err)
	}
	queued, err = s.UpdateFor(t.Context(), waiting.ID, model.UpdateRequest{ExpectedVersion: waiting.Version, Status: model.TaskQueued}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimFor(t.Context(), queued.ID, model.ClaimRequest{ExpectedVersion: queued.Version}, agent); err != nil {
		t.Fatal(err)
	}
}
