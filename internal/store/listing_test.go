package store

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func TestListVisibleTasksKeysetPagesMatchFullListing(t *testing.T) {
	database, err := Open(t.Context(), filepath.Join(t.TempDir(), "taskboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	assertKeysetPagesMatchFullListing(t, database)
}

func TestPostgresListVisibleTasksKeysetPagesMatchFullListing(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("TASKBOARD_TEST_POSTGRES_URL"))
	if baseURL == "" {
		t.Skip("TASKBOARD_TEST_POSTGRES_URL is not set")
	}
	databaseURL, _, _ := postgresMigrationFixture(t, baseURL)
	database, err := OpenURL(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	assertKeysetPagesMatchFullListing(t, database)
}

// assertKeysetPagesMatchFullListing seeds tasks that tie on every sort column
// except id, then checks cursor pages reproduce the single-query order exactly.
func assertKeysetPagesMatchFullListing(t *testing.T, database *Store) {
	t.Helper()
	statuses := []model.TaskStatus{model.TaskQueued, model.TaskBlocked, model.TaskStale, model.TaskActive}
	times := []string{formatTime(time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)), formatTime(time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC))}
	for index := range 40 {
		visibility := model.VisibilityAgent
		if index%5 == 0 {
			visibility = model.VisibilityPrivate
		}
		if _, err := database.DB().ExecContext(t.Context(), `INSERT INTO tasks(id,title,status,visibility,section,sort_order,version,created_at,updated_at) VALUES(?,?,?,?,?,?,1,?,?)`,
			fmt.Sprintf("task-%02d", 39-index), "Task", statuses[index%len(statuses)], visibility, []string{"Alpha", "Beta"}[index/3%2], index/7%2, times[0], times[index/2%2]); err != nil {
			t.Fatal(err)
		}
	}
	full, _, err := database.ListVisibleTasks(t.Context(), TaskQuery{Limit: 200}, "agent:worker", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 32 {
		t.Fatalf("full listing = %d tasks, want 32 agent-visible", len(full))
	}
	var paged []string
	var after *TaskCursor
	for range len(full) {
		page, cursors, err := database.ListVisibleTasks(t.Context(), TaskQuery{Limit: 7, After: after}, "agent:worker", true)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range page {
			paged = append(paged, task.ID)
		}
		if len(page) < 7 {
			break
		}
		after = &cursors[len(cursors)-1]
	}
	var want []string
	for _, task := range full {
		want = append(want, task.ID)
	}
	if !slices.Equal(paged, want) {
		t.Fatalf("paged order = %v\nwant %v", paged, want)
	}
	queued, _, err := database.ListVisibleTasks(t.Context(), TaskQuery{Statuses: []model.TaskStatus{model.TaskQueued}, Visibility: model.VisibilityPrivate, Limit: 200}, "agent:worker", true)
	if err != nil || len(queued) != 0 {
		t.Fatalf("private tasks leaked to agent = %d, %v", len(queued), err)
	}
}
