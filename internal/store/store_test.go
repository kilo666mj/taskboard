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
	if _, err := legacy.Exec(`CREATE TABLE desktop_handoffs (
		code_hash BLOB PRIMARY KEY, subject TEXT NOT NULL, email TEXT NOT NULL DEFAULT '',
		groups_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, expires_at TEXT NOT NULL
	)`); err != nil {
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
	for _, statement := range []string{
		`CREATE TABLE checklist_items (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
			label TEXT NOT NULL, status TEXT NOT NULL, position INTEGER NOT NULL, required INTEGER NOT NULL DEFAULT 1,
			note TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE agent_runs (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
			agent TEXT NOT NULL, client TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
			lease_expires_at TEXT NOT NULL, last_heartbeat_at TEXT NOT NULL, started_at TEXT NOT NULL, ended_at TEXT
		)`,
		`CREATE TABLE events (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
			run_id TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL, actor TEXT NOT NULL,
			message TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL
		)`,
		`CREATE TABLE push_subscriptions (
			endpoint TEXT PRIMARY KEY, p256dh TEXT NOT NULL, auth TEXT NOT NULL,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE browser_sessions (
			token_hash BLOB PRIMARY KEY, subject TEXT NOT NULL, email TEXT NOT NULL DEFAULT '',
			groups_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, expires_at TEXT NOT NULL
		)`,
		`CREATE TABLE task_templates (
			id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, title TEXT NOT NULL, summary TEXT NOT NULL DEFAULT '',
			section TEXT NOT NULL DEFAULT 'General', project TEXT NOT NULL DEFAULT '', repository TEXT NOT NULL DEFAULT '',
			priority TEXT NOT NULL DEFAULT 'normal', recurrence TEXT NOT NULL DEFAULT '', checklist_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
	} {
		if _, err := legacy.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := legacy.Exec(`INSERT INTO tasks(id,title,status,created_at,updated_at) VALUES(?,?,?,?,?)`, "legacy", "Existing task", "queued", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO agent_runs(id,task_id,agent,client,status,lease_expires_at,last_heartbeat_at,started_at) VALUES(?,?,?,?,?,?,?,?)`, "legacy-run", "legacy", "agent:legacy", "legacy-client", "active", stamp, stamp, stamp); err != nil {
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
	if task.Visibility != "team" {
		t.Fatalf("migrated visibility = %q, want team", task.Visibility)
	}
	if task.SortOrder <= 0 || task.Priority != "normal" {
		t.Fatalf("migrated planning defaults = %+v", task)
	}
	if len(task.Runs) != 1 || task.Runs[0].SessionID == "" || task.Runs[0].Callsign == "" {
		t.Fatalf("migrated agent run identity = %+v", task.Runs)
	}
	code := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	confirmation := "migration-browser-confirmation-secret"
	if err := database.CreateDesktopHandoff(t.Context(), code, confirmation, "0123-4567", BrowserIdentity{Subject: "user-1"}, time.Minute); err != nil {
		t.Fatalf("create handoff on migrated schema: %v", err)
	}
	if verification, err := database.PendingDesktopHandoff(t.Context(), confirmation); err != nil || verification != "0123-4567" {
		t.Fatalf("migrated handoff = %q, %v", verification, err)
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
	if count != 38 {
		t.Fatalf("application indexes = %d, want 38", count)
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
	confirmation := "single-use-browser-confirmation-secret"
	if err := database.CreateDesktopHandoff(t.Context(), code, confirmation, "0123-4567", identity, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := database.ExchangeDesktopHandoff(t.Context(), code, time.Hour); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unconfirmed exchange error = %v, want not found", err)
	}
	if verification, err := database.PendingDesktopHandoff(t.Context(), confirmation); err != nil || verification != "0123-4567" {
		t.Fatalf("pending handoff = %q, %v", verification, err)
	}
	if err := database.ConfirmDesktopHandoff(t.Context(), confirmation); err != nil {
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

func TestDesktopHandoffConfirmationExpires(t *testing.T) {
	database, err := Open(t.Context(), filepath.Join(t.TempDir(), "taskboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	code := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	confirmation := "expired-browser-confirmation-secret"
	if err := database.CreateDesktopHandoff(t.Context(), code, confirmation, "FEDC-BA98", BrowserIdentity{Subject: "user-1"}, -time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := database.PendingDesktopHandoff(t.Context(), confirmation); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired pending handoff error = %v, want not found", err)
	}
	if err := database.ConfirmDesktopHandoff(t.Context(), confirmation); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired confirmation error = %v, want not found", err)
	}
}
