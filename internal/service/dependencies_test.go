package service

import (
	"errors"
	"github.com/kilo666mj/taskboard/internal/model"
	"testing"
	"time"
)

func TestDependenciesGatePickupPreventCyclesAndDeriveReadiness(t *testing.T) {
	tasks := testService(t, time.Minute)
	human := HumanPrincipal("human:owner")
	agent := AgentPrincipal("agent:worker")
	blocker, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "First", Visibility: model.VisibilityAgent}, human)
	if err != nil {
		t.Fatal(err)
	}
	dependent, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Second", Visibility: model.VisibilityAgent}, human)
	if err != nil {
		t.Fatal(err)
	}
	dependent, err = tasks.AddTaskDependencyFor(t.Context(), dependent.ID, model.AddTaskDependencyRequest{BlockedByTaskID: blocker.ID, ExpectedVersion: dependent.Version}, human)
	if err != nil {
		t.Fatal(err)
	}
	if dependent.Ready || len(dependent.Dependencies) != 1 {
		t.Fatalf("dependent = %+v", dependent)
	}
	if _, err = tasks.ClaimFor(t.Context(), dependent.ID, model.ClaimRequest{ExpectedVersion: dependent.Version, IdempotencyKey: "blocked-claim"}, agent); !errors.Is(err, ErrValidation) {
		t.Fatalf("claim error = %v", err)
	}
	if _, err = tasks.AddTaskDependencyFor(t.Context(), blocker.ID, model.AddTaskDependencyRequest{BlockedByTaskID: dependent.ID, ExpectedVersion: blocker.Version}, human); !errors.Is(err, ErrValidation) {
		t.Fatalf("cycle error = %v", err)
	}
	blocker, err = tasks.UpdateFor(t.Context(), blocker.ID, model.UpdateRequest{ExpectedVersion: blocker.Version, Status: model.TaskCancelled}, human)
	if err != nil {
		t.Fatal(err)
	}
	dependent, err = tasks.GetFor(t.Context(), dependent.ID, human)
	if err != nil || dependent.Ready {
		t.Fatalf("cancelled blocker readiness = %+v, %v", dependent, err)
	}
	blocker, err = tasks.UpdateFor(t.Context(), blocker.ID, model.UpdateRequest{ExpectedVersion: blocker.Version, Status: model.TaskDone}, human)
	if err != nil {
		t.Fatal(err)
	}
	dependent, err = tasks.GetFor(t.Context(), dependent.ID, human)
	if err != nil || !dependent.Ready {
		t.Fatalf("done blocker readiness = %+v, %v", dependent, err)
	}
	if _, err = tasks.RemoveTaskDependencyFor(t.Context(), dependent.ID, blocker.ID, dependent.Version-1, human); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale remove error = %v", err)
	}
}
