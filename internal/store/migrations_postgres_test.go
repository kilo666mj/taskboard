package store

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"
)

func TestPostgresMigrationMetadataAndCompatibility(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("TASKBOARD_TEST_POSTGRES_URL"))
	if baseURL == "" {
		t.Skip("TASKBOARD_TEST_POSTGRES_URL is not set")
	}

	t.Run("seals existing version one", func(t *testing.T) {
		databaseURL, admin, schema := postgresMigrationFixture(t, baseURL)
		database, err := OpenURL(t.Context(), databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.ExecContext(t.Context(), `UPDATE `+schema+`.schema_migrations SET name='',checksum='' WHERE version=1`); err != nil {
			t.Fatal(err)
		}
		database, err = OpenURL(t.Context(), databaseURL)
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
	})

	t.Run("rejects future version", func(t *testing.T) {
		databaseURL, admin, schema := postgresMigrationFixture(t, baseURL)
		database, err := OpenURL(t.Context(), databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.ExecContext(t.Context(), `INSERT INTO `+schema+`.schema_migrations(version,name,checksum,applied_at) VALUES($1,'future','future','2026-01-01T00:00:00Z')`, latestSchemaVersion+1); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenURL(t.Context(), databaseURL); err == nil || !strings.Contains(err.Error(), "newer than supported") {
			t.Fatalf("OpenURL error = %v, want newer schema error", err)
		}
	})

	t.Run("rejects partial versioned schema", func(t *testing.T) {
		databaseURL, admin, schema := postgresMigrationFixture(t, baseURL)
		database, err := OpenURL(t.Context(), databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.ExecContext(t.Context(), `DROP INDEX `+schema+`.idx_runs_lease`); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenURL(t.Context(), databaseURL); err == nil || !strings.Contains(err.Error(), "partial") {
			t.Fatalf("OpenURL error = %v, want partial schema error", err)
		}
	})

	t.Run("rejects missing event notification trigger", func(t *testing.T) {
		databaseURL, admin, schema := postgresMigrationFixture(t, baseURL)
		database, err := OpenURL(t.Context(), databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.ExecContext(t.Context(), `DROP TRIGGER taskboard_event_notify ON `+schema+`.events`); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenURL(t.Context(), databaseURL); err == nil || !strings.Contains(err.Error(), "notification trigger is missing") {
			t.Fatalf("OpenURL error = %v, want missing notification trigger error", err)
		}
	})

	t.Run("rejects missing administrative audit trigger", func(t *testing.T) {
		databaseURL, admin, schema := postgresMigrationFixture(t, baseURL)
		database, err := OpenURL(t.Context(), databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.ExecContext(t.Context(), `DROP TRIGGER admin_audit_no_delete ON `+schema+`.admin_audit`); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenURL(t.Context(), databaseURL); err == nil || !strings.Contains(err.Error(), "administrative audit trigger") {
			t.Fatalf("OpenURL error = %v, want missing administrative audit trigger error", err)
		}
	})
}

func postgresMigrationFixture(t *testing.T, baseURL string) (string, *sql.DB, string) {
	t.Helper()
	admin, err := sql.Open("pgx", baseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "migration_test_" + strings.ToLower(ulid.Make().String())
	if _, err := admin.ExecContext(t.Context(), `CREATE SCHEMA `+schema); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("drop PostgreSQL test schema: %v", err)
		}
		if err := admin.Close(); err != nil {
			t.Errorf("close PostgreSQL admin connection: %v", err)
		}
	})
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String(), admin, schema
}
