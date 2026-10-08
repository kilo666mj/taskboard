package service

import (
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func TestLinksToTasksTheReaderCannotSeeAreReducedToIDs(t *testing.T) {
	tasks := testService(t, time.Minute)
	author, reader := HumanPrincipal("author@example.com"), HumanPrincipal("reader@example.com")
	team, private := model.VisibilityTeam, model.VisibilityPrivate

	shared, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Shared rollout", Visibility: team}, author)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Secret vendor switch", Visibility: private}, author)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Secret duplicate", Visibility: private}, author)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.GetFor(t.Context(), secret.ID, reader); err == nil {
		t.Fatal("the reader can open the private task directly")
	}
	shared, err = tasks.AddTaskDependencyFor(t.Context(), shared.ID, model.AddTaskDependencyRequest{BlockedByTaskID: secret.ID, ExpectedVersion: shared.Version}, author)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateFor(t.Context(), merged.ID, model.UpdateRequest{ExpectedVersion: merged.Version, DuplicateOf: &shared.ID}, author); err != nil {
		t.Fatal(err)
	}

	// The author sees every link in full.
	full, err := tasks.GetFor(t.Context(), shared.ID, author)
	if err != nil || len(full.Dependencies) != 1 || full.Dependencies[0].BlockedByTitle != "Secret vendor switch" || full.Dependencies[0].Hidden || len(full.Duplicates) != 1 {
		t.Fatalf("author view = %+v, %v", full, err)
	}

	// The reader learns that a blocker exists and whether it is done, nothing more.
	seen, err := tasks.GetFor(t.Context(), shared.ID, reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen.Dependencies) != 1 || !seen.Dependencies[0].Hidden || seen.Dependencies[0].BlockedByTitle != "" || seen.Dependencies[0].BlockedByStatus != "" || seen.Dependencies[0].BlockedByTaskID != secret.ID {
		t.Fatalf("reader dependencies = %+v", seen.Dependencies)
	}
	if len(seen.Duplicates) != 0 {
		t.Fatalf("reader duplicates = %+v", seen.Duplicates)
	}
	listed, err := tasks.ListTaskDependenciesFor(t.Context(), shared.ID, reader)
	if err != nil || len(listed) != 1 || !listed[0].Hidden || listed[0].BlockedByTitle != "" {
		t.Fatalf("reader dependency list = %+v, %v", listed, err)
	}
	page, err := tasks.ListFor(t.Context(), model.ListTasksRequest{}, reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range page.Tasks {
		for _, dependency := range task.Dependencies {
			if dependency.BlockedByTitle == "Secret vendor switch" {
				t.Fatal("the task list shows the private blocker's title")
			}
		}
		if len(task.Duplicates) > 0 {
			t.Fatalf("the task list shows private duplicates: %+v", task.Duplicates)
		}
	}
}
