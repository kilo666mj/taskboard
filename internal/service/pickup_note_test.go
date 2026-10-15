package service

import (
	"errors"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
)

func TestPickupRunnerNotesWhyItCouldNotStartAnUnclaimedTask(t *testing.T) {
	tasks := testService(t, time.Minute)
	person := HumanPrincipal("human:operator")
	runner := AgentPrincipal("agent:runner")
	task, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Pickup work", Visibility: model.VisibilityAgent, Checklist: []string{"Do it"}}, person)
	if err != nil {
		t.Fatal(err)
	}

	// Anything beyond the note stays with the claimer and with people.
	for name, request := range map[string]model.UpdateRequest{
		"status":   {CurrentNote: ptr("x"), Status: model.TaskBlocked, Blocker: ptr("x")},
		"summary":  {CurrentNote: ptr("x"), Summary: ptr("rewritten")},
		"title":    {CurrentNote: ptr("x"), Title: ptr("renamed")},
		"priority": {CurrentNote: ptr("x"), Priority: ptr(model.Priority("high"))},
		"owner":    {CurrentNote: ptr("x"), Owner: ptr(runner.ID)},
		"item":     {CurrentNote: ptr("x"), CompleteItemIDs: []string{task.Items[0].ID}},
		"no note":  {Summary: ptr("rewritten")},
	} {
		request.ExpectedVersion = task.Version
		if _, err := tasks.UpdateFor(t.Context(), task.ID, request, runner); !errors.Is(err, ErrForbidden) {
			t.Errorf("runner %s: err = %v, want forbidden", name, err)
		}
	}

	// Without task:claim the agent is not a pickup runner.
	policy := DefaultAgentPolicy()
	delete(policy.Capabilities, CapabilityTaskClaim)
	if _, err := tasks.UpdateFor(t.Context(), task.ID, model.UpdateRequest{ExpectedVersion: task.Version, CurrentNote: ptr("x")},
		AgentPrincipalWithPolicy("agent:reader", policy)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent without task:claim: err = %v, want forbidden", err)
	}

	noted, err := tasks.UpdateFor(t.Context(), task.ID, model.UpdateRequest{ExpectedVersion: task.Version,
		CurrentNote: ptr("Runner couldn't start this: worker capacity full (2 attempts)")}, runner)
	if err != nil {
		t.Fatalf("runner note: %v", err)
	}
	if noted.CurrentNote != "Runner couldn't start this: worker capacity full (2 attempts)" || noted.Status != model.TaskQueued || noted.Owner != "" {
		t.Fatalf("noted task = %q %s owner %q", noted.CurrentNote, noted.Status, noted.Owner)
	}
	if noted.Version != task.Version+1 || noted.LastEditedBy != runner.ID {
		t.Fatalf("noted provenance = v%d %q", noted.Version, noted.LastEditedBy)
	}

	// Once claimed (owned, with a live run), another runner may not note it.
	claimed, err := tasks.ClaimFor(t.Context(), task.ID, model.ClaimRequest{ExpectedVersion: noted.Version}, AgentPrincipal("agent:worker"))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := tasks.UpdateFor(t.Context(), task.ID, model.UpdateRequest{ExpectedVersion: claimed.Task.Version, CurrentNote: ptr("x")}, runner); !errors.Is(err, ErrForbidden) {
		t.Fatalf("runner on claimed task: err = %v, want forbidden", err)
	}
}

func TestPickupRunnerCannotNoteTeamTasks(t *testing.T) {
	tasks := testService(t, time.Minute)
	task, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Team work", Visibility: model.VisibilityTeam}, HumanPrincipal("human:operator"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = tasks.UpdateFor(t.Context(), task.ID, model.UpdateRequest{ExpectedVersion: task.Version, CurrentNote: ptr("x")}, AgentPrincipal("agent:runner"))
	if !errors.Is(err, ErrForbidden) && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("runner on team task: err = %v, want forbidden or not found", err)
	}
}

func TestPickupRunnerMayNoteOnlyWithoutALiveRun(t *testing.T) {
	runner := AgentPrincipal("agent:runner")
	note := model.UpdateRequest{ExpectedVersion: 1, CurrentNote: ptr("x")}
	ended := time.Now()
	task := model.Task{Visibility: model.VisibilityAgent, Status: model.TaskStale, Runs: []model.AgentRun{{EndedAt: &ended}}}
	if !pickupRunnerMayNote(task, runner, note) {
		t.Fatal("stale task with only ended runs: want allowed")
	}
	task.Runs = append(task.Runs, model.AgentRun{})
	if pickupRunnerMayNote(task, runner, note) {
		t.Fatal("task with a live run: want refused")
	}
	task.Runs, task.Status = nil, model.TaskActive
	if pickupRunnerMayNote(task, runner, note) {
		t.Fatal("active task: want refused")
	}
}
