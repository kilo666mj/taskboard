package service

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func TestAgentListFindsReadyPickupTaskBehindFullPageOfUnreadyWork(t *testing.T) {
	tasks := testService(t, time.Minute)
	human := HumanPrincipal("human:owner")
	blocker, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Blocker"}, human)
	if err != nil {
		t.Fatal(err)
	}
	for index := range 205 {
		task, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: fmt.Sprintf("Blocked %d", index), Visibility: model.VisibilityAgent}, human)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tasks.AddTaskDependencyFor(t.Context(), task.ID, model.AddTaskDependencyRequest{BlockedByTaskID: blocker.ID, ExpectedVersion: task.Version}, human); err != nil {
			t.Fatal(err)
		}
	}
	ready, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Runnable", Visibility: model.VisibilityAgent}, human)
	if err != nil {
		t.Fatal(err)
	}
	page, err := tasks.ListFor(t.Context(), model.ListTasksRequest{Statuses: []model.TaskStatus{model.TaskQueued, model.TaskStale}, Visibility: model.VisibilityAgent, Limit: 200}, AgentPrincipal("agent:worker"))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Tasks) != 1 || page.Tasks[0].ID != ready.ID || page.NextCursor != "" {
		t.Fatalf("page = %d tasks, cursor %q; want only %s", len(page.Tasks), page.NextCursor, ready.ID)
	}
}

func TestListCursorPagesEveryTaskOnceAndVisibilityFiltersTeamWork(t *testing.T) {
	tasks := testService(t, time.Minute)
	human := HumanPrincipal("human:owner")
	agent := AgentPrincipal("agent:worker")
	owner := agent.ID
	for index := range 205 {
		task, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: fmt.Sprintf("Team %d", index), Visibility: model.VisibilityTeam}, human)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tasks.UpdateFor(t.Context(), task.ID, model.UpdateRequest{Owner: &owner, ExpectedVersion: task.Version}, human); err != nil {
			t.Fatal(err)
		}
	}
	pickup := map[string]bool{}
	for index := range 5 {
		task, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: fmt.Sprintf("Pickup %d", index), Visibility: model.VisibilityAgent}, human)
		if err != nil {
			t.Fatal(err)
		}
		pickup[task.ID] = true
	}
	queued := []model.TaskStatus{model.TaskQueued}

	page, err := tasks.ListFor(t.Context(), model.ListTasksRequest{Statuses: queued, Visibility: model.VisibilityAgent, Limit: 200}, agent)
	if err != nil || len(page.Tasks) != len(pickup) || page.NextCursor != "" {
		t.Fatalf("pickup lane = %d tasks, cursor %q, %v", len(page.Tasks), page.NextCursor, err)
	}
	for _, item := range page.Tasks {
		if !pickup[item.ID] {
			t.Fatalf("pickup lane returned %s with visibility %s", item.ID, item.Visibility)
		}
	}

	seen := map[string]bool{}
	pages := 0
	request := model.ListTasksRequest{Statuses: queued, Limit: 200}
	for {
		page, err := tasks.ListFor(t.Context(), request, agent)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, item := range page.Tasks {
			if seen[item.ID] {
				t.Fatalf("task %s listed twice", item.ID)
			}
			seen[item.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		request.Cursor = page.NextCursor
	}
	if len(seen) != 210 || pages != 2 {
		t.Fatalf("paged %d tasks over %d pages, want 210 over 2", len(seen), pages)
	}
	for id := range pickup {
		if !seen[id] {
			t.Fatalf("pickup task %s missing from paged listing", id)
		}
	}
}

func TestAgentListReturnsShortPageWithCursorWhenScanBudgetIsSpent(t *testing.T) {
	previous := maxListScan
	maxListScan = 20
	t.Cleanup(func() { maxListScan = previous })
	tasks := testService(t, time.Minute)
	human := HumanPrincipal("human:owner")
	for index := range 25 {
		task, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: fmt.Sprintf("Needs GPU %d", index), Visibility: model.VisibilityAgent}, human)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tasks.SetTaskRequirementsFor(t.Context(), task.ID, model.SetTaskRequirementsRequest{Requirements: []string{"gpu"}, ExpectedVersion: task.Version}, human); err != nil {
			t.Fatal(err)
		}
	}
	ready, err := tasks.CreateFor(t.Context(), model.CreateRequest{Title: "Runnable", Visibility: model.VisibilityAgent}, human)
	if err != nil {
		t.Fatal(err)
	}
	agent := AgentPrincipal("agent:worker")
	request := model.ListTasksRequest{Statuses: []model.TaskStatus{model.TaskQueued}, Limit: 10}
	first, err := tasks.ListFor(t.Context(), request, agent)
	if err != nil || len(first.Tasks) != 0 || first.NextCursor == "" {
		t.Fatalf("first page = %d tasks, cursor %q, %v; want empty page with cursor", len(first.Tasks), first.NextCursor, err)
	}
	request.Cursor = first.NextCursor
	second, err := tasks.ListFor(t.Context(), request, agent)
	if err != nil || len(second.Tasks) != 1 || second.Tasks[0].ID != ready.ID || second.NextCursor != "" {
		t.Fatalf("second page = %+v, %v", second, err)
	}
}

func TestListRejectsInvalidVisibilityAndCursor(t *testing.T) {
	tasks := testService(t, time.Minute)
	human := HumanPrincipal("human:owner")
	for _, request := range []model.ListTasksRequest{{Visibility: "everyone"}, {Cursor: "not a cursor"}, {Cursor: "e30"}} {
		if _, err := tasks.ListFor(t.Context(), request, human); !errors.Is(err, ErrValidation) {
			t.Fatalf("ListFor(%+v) error = %v, want validation", request, err)
		}
	}
}
