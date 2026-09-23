package store

import (
	"database/sql"
	"fmt"
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
	if err := database.DB().QueryRowContext(t.Context(), `SELECT version,name,checksum FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version, &name, &checksum); err != nil {
		t.Fatal(err)
	}
	if version != latestSchemaVersion || name != schemaMigrations[len(schemaMigrations)-1].Name || len(checksum) != 64 {
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
	if _, err := raw.Exec(`UPDATE schema_migrations SET name='',checksum='' WHERE version=1`); err != nil {
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

func TestUpgradeReleasedBaselineBeforeEditProvenance(t *testing.T) {
	path := createMigratedSQLite(t)
	const released = "9b02436acc5f77fc8f43d198e93ad0947bb5d8d0bc5c54dd32cb17e307fec79c"
	mutateSQLite(t, path, `ALTER TABLE tasks DROP COLUMN last_edited_by`)
	mutateSQLite(t, path, `DELETE FROM schema_migrations WHERE version=16`)
	mutateSQLite(t, path, `UPDATE schema_migrations SET checksum='`+released+`' WHERE version=1`)
	mutateSQLite(t, path, `INSERT INTO tasks(id,title,status,created_at,updated_at) VALUES('existing','Keep this task','queued','2026-09-23T00:00:00Z','2026-09-23T00:00:00Z')`)
	database, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	var title, editedBy, checksum string
	if err := database.DB().QueryRowContext(t.Context(), `SELECT title,last_edited_by FROM tasks WHERE id='existing'`).Scan(&title, &editedBy); err != nil {
		t.Fatal(err)
	}
	if title != "Keep this task" || editedBy != "" {
		t.Fatalf("existing task changed: %q, %q", title, editedBy)
	}
	if err := database.DB().QueryRowContext(t.Context(), `SELECT checksum FROM schema_migrations WHERE version=1`).Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	if checksum != released {
		t.Fatalf("baseline metadata rewritten: %q", checksum)
	}
}

func TestMigrationRejectsNewerSchema(t *testing.T) {
	path := createMigratedSQLite(t)
	mutateSQLite(t, path, fmt.Sprintf(`INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(%d,'future','future','2026-01-01T00:00:00Z')`, latestSchemaVersion+1))
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

func TestMigrationRejectsPartialAdministrativeSchema(t *testing.T) {
	path := createMigratedSQLite(t)
	mutateSQLite(t, path, `DROP TABLE admin_audit`)
	if _, err := Open(t.Context(), path); err == nil || !strings.Contains(err.Error(), "partial") {
		t.Fatalf("Open error = %v, want partial administrative schema error", err)
	}
}

func TestMigrationRejectsMissingAdministrativeAuditTrigger(t *testing.T) {
	path := createMigratedSQLite(t)
	mutateSQLite(t, path, `DROP TRIGGER admin_audit_no_delete`)
	if _, err := Open(t.Context(), path); err == nil || !strings.Contains(err.Error(), "administrative audit trigger") {
		t.Fatalf("Open error = %v, want missing administrative audit trigger error", err)
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
