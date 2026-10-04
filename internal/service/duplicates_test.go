package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func TestTitleSimilarity(t *testing.T) {
	for _, test := range []struct {
		left, right string
		similar     bool
	}{
		{"Fix HAProxy api-primary backend DOWN", "HAProxy api-primary backend down", true},
		{"Deploy Taskboard v0.14.0", "deploy taskboard v0.14.0", true},
		{"Tintwire: selectable UI themes (Sentinel + Wire)", "Tintwire: fix ?notification= deep links that miss the loaded page", false},
		{"Repo reconciliation: Phase 3 decisions (Forgejo imports, branch drops, codex OAuth fix rebuild)", "Repo reconciliation: rest of Phase 3", false},
		{"Roll out phase 3", "Roll out phase 4", false},
		{"Remediate needs-me-named", "Remediate needs-me-other", false},
		{"Deploy", "Deploy", true},
		{"Deploy", "Deploy dockgate", false},
	} {
		_, similar := titleSimilarity(titleTokens(test.left), titleTokens(test.right))
		if similar != test.similar {
			t.Errorf("titleSimilarity(%q, %q) = %v, want %v", test.left, test.right, similar, test.similar)
		}
	}
}

func TestSameScopeComparesRepositoryNames(t *testing.T) {
	if !sameScope("kilo666mj/taskboard", "taskboard") || !sameScope("", "taskboard") || sameScope("kilo666mj/taskboard", "kilo666mj/tintwire") {
		t.Fatal("unexpected repository scope comparison")
	}
}

func TestAgentStartRefusesSimilarOpenTaskUnlessForced(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	start := model.StartRequest{Title: "Fix HAProxy api-primary backend DOWN", Repository: "example/infra", Checklist: []string{"Diagnose"}}
	existing, err := tasks.StartFor(t.Context(), start, agent)
	if err != nil {
		t.Fatal(err)
	}

	again := model.StartRequest{Title: "HAProxy api-primary backend down", Repository: "infra", Checklist: []string{"Diagnose"}}
	_, err = tasks.StartFor(t.Context(), again, agent)
	var duplicates *DuplicateCandidatesError
	if !errors.As(err, &duplicates) || !errors.Is(err, ErrConflict) {
		t.Fatalf("similar start error = %v", err)
	}
	if len(duplicates.Candidates) != 1 || duplicates.Candidates[0].TaskID != existing.Task.ID || duplicates.Candidates[0].Version != existing.Task.Version {
		t.Fatalf("candidates = %+v", duplicates.Candidates)
	}

	other := again
	other.Repository = "kilo666mj/tintwire"
	if _, err := tasks.StartFor(t.Context(), other, agent); err != nil {
		t.Fatalf("different repository refused: %v", err)
	}

	again.ForceNew = true
	forced, err := tasks.StartFor(t.Context(), again, agent)
	if err != nil {
		t.Fatalf("forced start: %v", err)
	}
	events, err := tasks.store.ListTaskEvents(t.Context(), forced.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids, _ := events[0].Payload["overridden_duplicates"].([]any)
	if len(ids) != 1 || ids[0] != existing.Task.ID {
		t.Fatalf("started event payload = %+v", events[0].Payload)
	}
}

func TestAgentCreateRefusesSimilarOpenTaskButIgnoresClosedAndHidden(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	human := HumanPrincipal("human:owner")
	hidden, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Rotate the backup encryption key"}, human)
	if err != nil {
		t.Fatal(err)
	}
	if hidden.Visibility != model.VisibilityPrivate {
		t.Fatalf("human task visibility = %q", hidden.Visibility)
	}
	if _, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Rotate backup encryption key"}, agent); err != nil {
		t.Fatalf("private task should not be a candidate: %v", err)
	}
	if _, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Rotate the backup encryption key now"}, agent); !errors.Is(err, ErrConflict) {
		t.Fatalf("similar agent-lane create error = %v", err)
	}

	done, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Upgrade Forgejo runner image", Visibility: model.VisibilityAgent}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateFor(t.Context(), done.ID, model.UpdateRequest{ExpectedVersion: done.Version, Status: model.TaskDone}, human); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Upgrade Forgejo runner image"}, agent); err != nil {
		t.Fatalf("done task should not be a candidate: %v", err)
	}
}

func TestHumanStartIsNotCheckedForDuplicates(t *testing.T) {
	tasks := testService(t, time.Minute)
	human := HumanPrincipal("human:owner")
	request := model.StartRequest{Title: "Write release notes", Visibility: model.VisibilityTeam, Checklist: []string{"Draft"}}
	for range 2 {
		if _, err := tasks.StartFor(t.Context(), request, human); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMarkDuplicateCancelsLinksAndRedirectsClaims(t *testing.T) {
	tasks := testService(t, time.Minute)
	human := HumanPrincipal("human:owner")
	agent := AgentPrincipal("agent:worker")
	create := func(title string) model.Task {
		task, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: title, Visibility: model.VisibilityAgent}, human)
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	kept, first, second := create("Kept work"), create("First copy"), create("Second copy")

	if _, err := tasks.UpdateFor(t.Context(), first.ID, model.UpdateRequest{ExpectedVersion: first.Version, DuplicateOf: &first.ID}, human); !errors.Is(err, ErrValidation) {
		t.Fatalf("self duplicate error = %v", err)
	}
	if _, err := tasks.UpdateFor(t.Context(), first.ID, model.UpdateRequest{ExpectedVersion: first.Version, Status: model.TaskDone, DuplicateOf: &kept.ID}, human); !errors.Is(err, ErrValidation) {
		t.Fatalf("duplicate with done status error = %v", err)
	}
	missing := "01M00000000000000000000000"
	if _, err := tasks.UpdateFor(t.Context(), first.ID, model.UpdateRequest{ExpectedVersion: first.Version, DuplicateOf: &missing}, human); !errors.Is(err, ErrValidation) {
		t.Fatalf("missing target error = %v", err)
	}

	// Mark second as a duplicate of first, then first of kept: second follows.
	second, err := tasks.UpdateFor(t.Context(), second.ID, model.UpdateRequest{ExpectedVersion: second.Version, DuplicateOf: &first.ID}, human)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != model.TaskCancelled || second.DuplicateOf == nil || second.DuplicateOf.TaskID != first.ID || second.CurrentNote != "Duplicate of First copy" {
		t.Fatalf("second = %+v", second)
	}
	first, err = tasks.UpdateFor(t.Context(), first.ID, model.UpdateRequest{ExpectedVersion: first.Version, DuplicateOf: &kept.ID}, human)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != model.TaskCancelled || first.DuplicateOf == nil || first.DuplicateOf.TaskID != kept.ID || first.DuplicateOf.Title != "Kept work" {
		t.Fatalf("first = %+v", first)
	}
	kept, err = tasks.GetFor(t.Context(), kept.ID, human)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept.Duplicates) != 2 || kept.Duplicates[0].TaskID != first.ID || kept.Duplicates[1].TaskID != second.ID {
		t.Fatalf("kept duplicates = %+v", kept.Duplicates)
	}
	events, err := tasks.store.ListTaskEvents(t.Context(), kept.ID)
	if err != nil {
		t.Fatal(err)
	}
	if last := events[len(events)-1]; last.Kind != "task.duplicate_linked" || last.Payload["duplicate_task_id"] != first.ID {
		t.Fatalf("kept events = %+v", events)
	}

	// Marking a task as a duplicate of its own duplicate is a cycle.
	if _, err := tasks.UpdateFor(t.Context(), kept.ID, model.UpdateRequest{ExpectedVersion: kept.Version, DuplicateOf: &first.ID}, human); !errors.Is(err, ErrValidation) {
		t.Fatalf("cycle error = %v", err)
	}

	_, err = tasks.ClaimFor(t.Context(), first.ID, model.ClaimRequest{ExpectedVersion: first.Version}, agent)
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), kept.ID) {
		t.Fatalf("claim duplicate error = %v", err)
	}

	// Reopening clears the link.
	first, err = tasks.UpdateFor(t.Context(), first.ID, model.UpdateRequest{ExpectedVersion: first.Version, Status: model.TaskQueued}, human)
	if err != nil {
		t.Fatal(err)
	}
	if first.DuplicateOf != nil {
		t.Fatalf("reopened task still linked: %+v", first.DuplicateOf)
	}
}

func TestAgentsNeedSensitiveCapabilityToMarkDuplicates(t *testing.T) {
	tasks := testService(t, time.Minute)
	agent := AgentPrincipal("agent:worker")
	kept, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Kept work", Checklist: []string{"Do"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Unrelated copy", Checklist: []string{"Do"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateFor(t.Context(), copy.Task.ID, model.UpdateRequest{ExpectedVersion: copy.Task.Version, RunID: copy.Run.ID, DuplicateOf: &kept.Task.ID}, agent); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent duplicate error = %v", err)
	}
}

func TestMarkDuplicateRefusesTaskWithAnotherActiveRun(t *testing.T) {
	tasks := testService(t, time.Minute)
	human := HumanPrincipal("human:owner")
	agent := AgentPrincipal("agent:worker")
	kept, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Kept work", Visibility: model.VisibilityAgent}, human)
	if err != nil {
		t.Fatal(err)
	}
	running, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Running elsewhere", Checklist: []string{"Do"}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateFor(t.Context(), running.Task.ID, model.UpdateRequest{ExpectedVersion: running.Task.Version, DuplicateOf: &kept.ID}, human); !errors.Is(err, ErrValidation) {
		t.Fatalf("active run duplicate error = %v", err)
	}
}

func TestMarkOwnStartedTaskDuplicateEndsOwnRun(t *testing.T) {
	tasks := testService(t, time.Minute)
	human := HumanPrincipal("human:owner")
	kept, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Kept work", Checklist: []string{"Do"}}, human)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := tasks.StartFor(t.Context(), model.StartRequest{Title: "Copy", Checklist: []string{"Do"}}, human)
	if err != nil {
		t.Fatal(err)
	}
	marked, err := tasks.UpdateFor(t.Context(), copy.Task.ID, model.UpdateRequest{ExpectedVersion: copy.Task.Version, DuplicateOf: &kept.Task.ID}, human)
	if err != nil {
		t.Fatal(err)
	}
	if marked.Runs[0].EndedAt == nil || marked.Runs[0].Status != model.TaskCancelled {
		t.Fatalf("own run = %+v", marked.Runs[0])
	}
}
