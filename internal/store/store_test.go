package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrationAddsGeneralSectionToExistingTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskboard.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE tasks (
        id TEXT PRIMARY KEY, title TEXT NOT NULL, summary TEXT NOT NULL DEFAULT '',
        repository TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, owner TEXT NOT NULL DEFAULT '',
        current_note TEXT NOT NULL DEFAULT '', blocker TEXT NOT NULL DEFAULT '', waiting_for TEXT NOT NULL DEFAULT '',
        version INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, completed_at TEXT
    )`)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := legacy.Exec(`INSERT INTO tasks(id,title,status,created_at,updated_at) VALUES(?,?,?,?,?)`, "legacy", "Existing task", "queued", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	task, err := database.GetTask(t.Context(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if task.Section != "General" {
		t.Fatalf("migrated section = %q, want General", task.Section)
	}
	if task.Type != "personal" {
		t.Fatalf("migrated type = %q, want personal", task.Type)
	}
	if task.SortOrder <= 0 || task.Priority != "normal" {
		t.Fatalf("migrated planning defaults = %+v", task)
	}
}

func TestSchemaCreatesQueryIndexes(t *testing.T) {
	database, err := Open(t.Context(), filepath.Join(t.TempDir(), "taskboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	rows, err := database.DB().QueryContext(t.Context(), `SELECT name FROM sqlite_schema WHERE type='index' AND name LIKE 'idx_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close index rows: %v", err)
		}
	})
	var count int
	for rows.Next() {
		count++
	}
	if count != 8 {
		t.Fatalf("application indexes = %d, want 8", count)
	}
}

func TestBrowserSessionLifecycle(t *testing.T) {
	database, err := Open(t.Context(), filepath.Join(t.TempDir(), "taskboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	identity := BrowserIdentity{Subject: "user-1", Email: "person@example.com", Groups: []string{"operators"}}
	token, _, err := database.CreateBrowserSession(t.Context(), identity, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	loaded, valid, err := database.BrowserSession(t.Context(), token)
	if err != nil || !valid || loaded.Subject != identity.Subject || loaded.Email != identity.Email || len(loaded.Groups) != 1 {
		t.Fatalf("loaded session = %+v, %v, %v", loaded, valid, err)
	}
	if err := database.DeleteBrowserSession(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := database.BrowserSession(t.Context(), token); err != nil || valid {
		t.Fatalf("deleted session valid = %v, error = %v", valid, err)
	}
}

func TestDesktopHandoffIsSingleUse(t *testing.T) {
	database, err := Open(t.Context(), filepath.Join(t.TempDir(), "taskboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	identity := BrowserIdentity{Subject: "user-1", Email: "person@example.com"}
	code := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := database.CreateDesktopHandoff(t.Context(), code, identity, time.Minute); err != nil {
		t.Fatal(err)
	}
	token, _, loaded, err := database.ExchangeDesktopHandoff(t.Context(), code, time.Hour)
	if err != nil || token == "" || loaded.Subject != identity.Subject {
		t.Fatalf("exchange = %q, %+v, %v", token, loaded, err)
	}
	if _, _, _, err := database.ExchangeDesktopHandoff(t.Context(), code, time.Hour); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second exchange error = %v, want not found", err)
	}
	if _, valid, err := database.BrowserSession(t.Context(), token); err != nil || !valid {
		t.Fatalf("exchanged session valid = %v, error = %v", valid, err)
	}
}
