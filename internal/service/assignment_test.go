package service

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func TestReassignAndUnassignPreserveWaitingWork(t *testing.T) {
	for _, status := range []model.TaskStatus{model.TaskWaiting, model.TaskBlocked} {
		t.Run(string(status), func(t *testing.T) {
			s := testService(t, time.Minute)
			agent := AgentPrincipal("agent:original")
			human := HumanPrincipal("human:operator")
			started, err := s.StartFor(t.Context(), model.StartRequest{Title: "Work with history", Visibility: model.VisibilityTeam, Checklist: []string{"Inspect", "Verify"}}, agent)
			if err != nil {
				t.Fatal(err)
			}
			before, err := s.UpdateFor(t.Context(), started.Task.ID, model.UpdateRequest{ExpectedVersion: started.Task.Version, RunID: started.Run.ID, Status: status, CompleteItemIDs: []string{started.Task.Items[0].ID}, CurrentNote: ptr("Inspection complete"), WaitingFor: ptr("External result"), Blocker: ptr("Needs review")}, agent)
			if err != nil {
				t.Fatal(err)
			}
			current := before
			for _, owner := range []string{"agent:replacement", ""} {
				updated, err := s.UpdateFor(t.Context(), current.ID, model.UpdateRequest{ExpectedVersion: current.Version, Owner: &owner}, human)
				if err != nil {
					t.Fatal(err)
				}
				if updated.Owner != owner || updated.Status != before.Status || updated.Visibility != before.Visibility || updated.CurrentNote != before.CurrentNote || updated.WaitingFor != before.WaitingFor || updated.Blocker != before.Blocker || updated.CreatedBy != before.CreatedBy || updated.CompletedAt != nil {
					t.Fatalf("assignment changed unrelated task state: %+v", updated)
				}
				if !reflect.DeepEqual(updated.Items, before.Items) || !reflect.DeepEqual(updated.Runs, before.Runs) {
					t.Fatal("assignment changed checklist or run history")
				}
				if updated.LastEditedBy != human.ID || updated.Version != current.Version+1 {
					t.Fatal("assignment did not record the editor and version")
				}
				current = updated
			}
		})
	}
}

func TestActiveRunRejectsReassignmentAndUnassignment(t *testing.T) {
	s := testService(t, time.Minute)
	started, err := s.StartFor(t.Context(), model.StartRequest{Title: "Running work", Visibility: model.VisibilityTeam, Checklist: []string{"Work"}}, AgentPrincipal("agent:original"))
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"agent:replacement", ""} {
		_, err := s.UpdateFor(t.Context(), started.Task.ID, model.UpdateRequest{ExpectedVersion: started.Task.Version, Owner: &owner}, HumanPrincipal("human:operator"))
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("active ownership change to %q: %v", owner, err)
		}
	}
}
