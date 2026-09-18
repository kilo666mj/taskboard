package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestFreshSchemaRecordsSealedBaseline(t *testing.T) {
	database, err := Open(t.Context(), filepath.Join(t.TempDir(), "taskboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	var version int
	var name, checksum string
	if err := database.DB().QueryRowContext(t.Context(), `SELECT version,name,checksum FROM schema_migrations`).Scan(&version, &name, &checksum); err != nil {
		t.Fatal(err)
	}
	if version != latestSchemaVersion || name != "baseline" || len(checksum) != 64 {
		t.Fatalf("migration metadata = %d, %q, %q", version, name, checksum)
	}
}

func TestExistingVersionOneSchemaIsSealed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskboard.db")
	database, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE schema_migrations SET name='',checksum=''`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	var name, checksum string
	if err := database.DB().QueryRowContext(t.Context(), `SELECT name,checksum FROM schema_migrations WHERE version=1`).Scan(&name, &checksum); err != nil {
		t.Fatal(err)
	}
	if name != "baseline" || len(checksum) != 64 {
		t.Fatalf("sealed metadata = %q, %q", name, checksum)
	}
}

func TestMigrationRejectsUnknownChecksum(t *testing.T) {
	path := createMigratedSQLite(t)
	mutateSQLite(t, path, `UPDATE schema_migrations SET checksum='changed' WHERE version=1`)
	if _, err := Open(t.Context(), path); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("Open error = %v, want immutable migration error", err)
	}
}

func TestMigrationRejectsNewerSchema(t *testing.T) {
	path := createMigratedSQLite(t)
	mutateSQLite(t, path, `UPDATE schema_migrations SET version=2 WHERE version=1`)
	if _, err := Open(t.Context(), path); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("Open error = %v, want newer schema error", err)
	}
}

func TestMigrationRejectsPartialVersionedSchema(t *testing.T) {
	path := createMigratedSQLite(t)
	mutateSQLite(t, path, `DROP INDEX idx_runs_lease`)
	if _, err := Open(t.Context(), path); err == nil || !strings.Contains(err.Error(), "partial") {
		t.Fatalf("Open error = %v, want partial schema error", err)
	}
}

func TestMigrationRejectsPartialUnversionedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskboard.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE tasks (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), path); err == nil || !strings.Contains(err.Error(), "partial unversioned schema") {
		t.Fatalf("Open error = %v, want partial unversioned schema error", err)
	}
}

func createMigratedSQLite(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "taskboard.db")
	database, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func mutateSQLite(t *testing.T, path, statement string) {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(statement); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
}
