package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const latestSchemaVersion = 2

type schemaMigration struct {
	Version  int
	Name     string
	SQLite   []string
	Postgres []string
}

type appliedMigration struct {
	Version  int
	Name     string
	Checksum string
}

var schemaMigrations = []schemaMigration{
	{
		Version:  1,
		Name:     "baseline",
		SQLite:   sqliteBaselineStatements(),
		Postgres: postgresBaselineStatements(),
	},
	{
		Version: 2,
		Name:    "postgres_event_notifications",
		SQLite:  []string{`SELECT 1`},
		Postgres: []string{
			`CREATE OR REPLACE FUNCTION taskboard_notify_event() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				PERFORM pg_notify('taskboard_events', NEW.id);
				RETURN NEW;
			END;
			$$`,
			`DROP TRIGGER IF EXISTS taskboard_event_notify ON events`,
			`CREATE TRIGGER taskboard_event_notify AFTER INSERT ON events FOR EACH ROW EXECUTE FUNCTION taskboard_notify_event()`,
		},
	},
}

var requiredSchema = map[string][]string{
	"tasks": {
		"id", "title", "summary", "task_type", "visibility", "created_by", "section", "project", "repository",
		"priority", "due_date", "defer_until", "recurrence", "sort_order", "reviewed_at", "status", "owner",
		"current_note", "blocker", "waiting_for", "version", "created_at", "updated_at", "completed_at",
	},
	"checklist_items":    {"id", "task_id", "label", "status", "position", "required", "note", "updated_at"},
	"agent_runs":         {"id", "task_id", "agent", "client", "status", "lease_expires_at", "last_heartbeat_at", "started_at", "ended_at"},
	"events":             {"id", "task_id", "run_id", "kind", "actor", "message", "payload", "created_at"},
	"push_subscriptions": {"endpoint", "p256dh", "auth", "owner_id", "notify_progress", "notify_reminders", "notify_summaries", "created_at", "updated_at"},
	"browser_sessions":   {"token_hash", "subject", "email", "groups_json", "created_at", "expires_at"},
	"desktop_handoffs":   {"code_hash", "subject", "email", "groups_json", "created_at", "expires_at", "confirmation_hash", "verification_code", "confirmed_at"},
	"task_templates":     {"id", "name", "title", "summary", "task_type", "section", "project", "repository", "priority", "recurrence", "checklist_json", "created_at", "updated_at"},
}

var requiredIndexes = []string{
	"idx_tasks_status_updated",
	"idx_checklist_task_position",
	"idx_runs_task",
	"idx_runs_lease",
	"idx_events_created",
	"idx_browser_sessions_expires",
	"idx_desktop_handoffs_expires",
	"idx_templates_name",
	"idx_tasks_visibility_creator",
	"idx_push_subscriptions_owner",
	"idx_desktop_handoffs_confirmation",
}

func (s *Store) migrate(ctx context.Context) error {
	if err := validateDefinedMigrations(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if s.db.dialect == DialectPostgres {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(724187452910)`); err != nil {
			return fmt.Errorf("lock PostgreSQL migrations: %w", err)
		}
	}
	if err := ensureMigrationTable(ctx, tx, s.db.dialect); err != nil {
		return err
	}

	applied, err := loadAppliedMigrations(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateMigrationSequence(applied); err != nil {
		return err
	}

	if len(applied) == 0 {
		hasSchema, err := hasApplicationSchema(ctx, tx, s.db.dialect)
		if err != nil {
			return err
		}
		if hasSchema {
			if err := adoptLegacySchema(ctx, tx, s.db.dialect); err != nil {
				return err
			}
		} else if err := executeStatements(ctx, tx, statementsFor(schemaMigrations[0], s.db.dialect)); err != nil {
			return fmt.Errorf("apply migration 1 baseline: %w", err)
		}
		if err := validateCurrentSchema(ctx, tx, s.db.dialect); err != nil {
			return fmt.Errorf("validate migration 1 baseline: %w", err)
		}
		if err := recordMigration(ctx, tx, schemaMigrations[0], s.db.dialect); err != nil {
			return err
		}
		applied = []appliedMigration{{
			Version:  schemaMigrations[0].Version,
			Name:     schemaMigrations[0].Name,
			Checksum: migrationChecksum(schemaMigrations[0], s.db.dialect),
		}}
	}

	for index, record := range applied {
		migration := schemaMigrations[index]
		expected := migrationChecksum(migration, s.db.dialect)
		if record.Checksum == "" && record.Version == 1 {
			// Releases before versioned migrations recorded only version 1. Bring
			// that known schema forward once, validate it, and seal the baseline.
			if err := adoptLegacySchema(ctx, tx, s.db.dialect); err != nil {
				return err
			}
			if err := validateCurrentSchema(ctx, tx, s.db.dialect); err != nil {
				return fmt.Errorf("validate adopted migration 1 baseline: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE schema_migrations SET name=?,checksum=? WHERE version=? AND checksum=''`, migration.Name, expected, migration.Version); err != nil {
				return fmt.Errorf("seal migration 1 baseline: %w", err)
			}
			continue
		}
		if record.Name != migration.Name || record.Checksum != expected {
			return fmt.Errorf("schema migration %d metadata does not match the immutable %q migration", record.Version, migration.Name)
		}
	}

	for index := len(applied); index < len(schemaMigrations); index++ {
		migration := schemaMigrations[index]
		if err := executeStatements(ctx, tx, statementsFor(migration, s.db.dialect)); err != nil {
			return fmt.Errorf("apply migration %d %s: %w", migration.Version, migration.Name, err)
		}
		if err := recordMigration(ctx, tx, migration, s.db.dialect); err != nil {
			return err
		}
	}
	if err := validateCurrentSchema(ctx, tx, s.db.dialect); err != nil {
		return err
	}
	if s.db.dialect == DialectPostgres {
		var notificationTriggerExists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM pg_trigger trigger
			JOIN pg_class relation ON relation.oid=trigger.tgrelid
			JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
			WHERE namespace.nspname=current_schema() AND relation.relname='events'
			AND trigger.tgname='taskboard_event_notify' AND NOT trigger.tgisinternal
		)`).Scan(&notificationTriggerExists); err != nil {
			return fmt.Errorf("validate PostgreSQL event notification trigger: %w", err)
		}
		if !notificationTriggerExists {
			return fmt.Errorf("schema version %d is partial: PostgreSQL event notification trigger is missing", latestSchemaVersion)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema migrations: %w", err)
	}
	if s.db.dialect == DialectSQLite {
		if _, err := s.db.ExecContext(ctx, "PRAGMA optimize"); err != nil {
			return fmt.Errorf("optimize SQLite schema: %w", err)
		}
	}
	return nil
}

func validateDefinedMigrations() error {
	if len(schemaMigrations) != latestSchemaVersion {
		return fmt.Errorf("migration registry ends at %d but latest schema version is %d", len(schemaMigrations), latestSchemaVersion)
	}
	for index, migration := range schemaMigrations {
		if migration.Version != index+1 || strings.TrimSpace(migration.Name) == "" || len(migration.SQLite) == 0 || len(migration.Postgres) == 0 {
			return fmt.Errorf("invalid schema migration registry entry at position %d", index+1)
		}
	}
	return nil
}

func ensureMigrationTable(ctx context.Context, tx *Tx, dialect Dialect) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		checksum TEXT NOT NULL DEFAULT '',
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema migration table: %w", err)
	}
	if dialect == DialectPostgres {
		for _, statement := range []string{
			`ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum TEXT NOT NULL DEFAULT ''`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("upgrade schema migration metadata: %w", err)
			}
		}
		return nil
	}
	for _, column := range []struct{ name, definition string }{
		{"name", `TEXT NOT NULL DEFAULT ''`},
		{"checksum", `TEXT NOT NULL DEFAULT ''`},
	} {
		if err := ensureSQLiteColumn(ctx, tx, "schema_migrations", column.name, column.definition); err != nil {
			return fmt.Errorf("upgrade schema migration metadata: %w", err)
		}
	}
	return nil
}

func loadAppliedMigrations(ctx context.Context, tx *Tx) ([]appliedMigration, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version,name,checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("load schema migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var migrations []appliedMigration
	for rows.Next() {
		var migration appliedMigration
		if err := rows.Scan(&migration.Version, &migration.Name, &migration.Checksum); err != nil {
			return nil, err
		}
		migrations = append(migrations, migration)
	}
	return migrations, rows.Err()
}

func validateMigrationSequence(applied []appliedMigration) error {
	if len(applied) > 0 && applied[len(applied)-1].Version > latestSchemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", applied[len(applied)-1].Version, latestSchemaVersion)
	}
	for index, migration := range applied {
		expected := index + 1
		if migration.Version != expected {
			return fmt.Errorf("schema migration history is non-contiguous: expected version %d, found %d", expected, migration.Version)
		}
		if migration.Version > latestSchemaVersion {
			return fmt.Errorf("database schema version %d is newer than supported version %d", migration.Version, latestSchemaVersion)
		}
	}
	return nil
}

func recordMigration(ctx context.Context, tx *Tx, migration schemaMigration, dialect Dialect) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`,
		migration.Version, migration.Name, migrationChecksum(migration, dialect), formatTime(time.Now()))
	if err != nil {
		return fmt.Errorf("record migration %d %s: %w", migration.Version, migration.Name, err)
	}
	return nil
}

func migrationChecksum(migration schemaMigration, dialect Dialect) string {
	content := fmt.Sprintf("%d\n%s\n%s\n%s", migration.Version, migration.Name, dialect, strings.Join(statementsFor(migration, dialect), "\n-- statement --\n"))
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func statementsFor(migration schemaMigration, dialect Dialect) []string {
	if dialect == DialectPostgres {
		return migration.Postgres
	}
	return migration.SQLite
}

func executeStatements(ctx context.Context, tx *Tx, statements []string) error {
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func hasApplicationSchema(ctx context.Context, tx *Tx, dialect Dialect) (bool, error) {
	count := 0
	for table := range requiredSchema {
		exists, err := tableExists(ctx, tx, dialect, table)
		if err != nil {
			return false, err
		}
		if exists {
			count++
		}
	}
	if count != 0 && count != len(requiredSchema) {
		return false, fmt.Errorf("partial unversioned schema: found %d of %d required application tables", count, len(requiredSchema))
	}
	return count == len(requiredSchema), nil
}

func adoptLegacySchema(ctx context.Context, tx *Tx, dialect Dialect) error {
	complete, err := hasApplicationSchema(ctx, tx, dialect)
	if err != nil {
		return err
	}
	if !complete {
		return errors.New("cannot adopt an empty schema as an existing baseline")
	}
	if dialect == DialectPostgres {
		return upgradeLegacyPostgres(ctx, tx)
	}
	return upgradeLegacySQLite(ctx, tx)
}

func upgradeLegacySQLite(ctx context.Context, tx *Tx) error {
	columns := map[string][]struct{ name, definition string }{
		"tasks": {
			{"section", `TEXT NOT NULL DEFAULT 'General'`},
			{"task_type", `TEXT NOT NULL DEFAULT 'personal'`},
			{"visibility", `TEXT NOT NULL DEFAULT 'team'`},
			{"created_by", `TEXT NOT NULL DEFAULT ''`},
			{"project", `TEXT NOT NULL DEFAULT ''`},
			{"priority", `TEXT NOT NULL DEFAULT 'normal'`},
			{"due_date", `TEXT NOT NULL DEFAULT ''`},
			{"defer_until", `TEXT NOT NULL DEFAULT ''`},
			{"recurrence", `TEXT NOT NULL DEFAULT ''`},
			{"sort_order", `INTEGER NOT NULL DEFAULT 0`},
			{"reviewed_at", `TEXT`},
		},
		"desktop_handoffs": {
			{"confirmed_at", `TEXT`},
			{"confirmation_hash", `BLOB`},
			{"verification_code", `TEXT NOT NULL DEFAULT ''`},
		},
		"task_templates": {{"task_type", `TEXT NOT NULL DEFAULT 'personal'`}},
		"push_subscriptions": {
			{"owner_id", `TEXT NOT NULL DEFAULT ''`},
			{"notify_progress", `INTEGER NOT NULL DEFAULT 1`},
			{"notify_reminders", `INTEGER NOT NULL DEFAULT 1`},
			{"notify_summaries", `INTEGER NOT NULL DEFAULT 0`},
		},
	}
	for table, additions := range columns {
		for _, column := range additions {
			if err := ensureSQLiteColumn(ctx, tx, table, column.name, column.definition); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET sort_order=rowid*1024 WHERE sort_order=0`); err != nil {
		return fmt.Errorf("backfill task ordering: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET created_by=COALESCE((SELECT actor FROM events WHERE events.task_id=tasks.id ORDER BY created_at LIMIT 1),'') WHERE created_by=''`); err != nil {
		return fmt.Errorf("backfill task creators: %w", err)
	}
	return executeStatements(ctx, tx, sqliteIndexStatements())
}

func upgradeLegacyPostgres(ctx context.Context, tx *Tx) error {
	statements := []string{
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS section TEXT NOT NULL DEFAULT 'General'`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS task_type TEXT NOT NULL DEFAULT 'personal'`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS visibility TEXT NOT NULL DEFAULT 'team'`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS project TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS priority TEXT NOT NULL DEFAULT 'normal'`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS due_date TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS defer_until TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS recurrence TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS sort_order BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS reviewed_at TEXT`,
		`ALTER TABLE desktop_handoffs ADD COLUMN IF NOT EXISTS confirmed_at TEXT`,
		`ALTER TABLE desktop_handoffs ADD COLUMN IF NOT EXISTS confirmation_hash BYTEA`,
		`ALTER TABLE desktop_handoffs ADD COLUMN IF NOT EXISTS verification_code TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE task_templates ADD COLUMN IF NOT EXISTS task_type TEXT NOT NULL DEFAULT 'personal'`,
		`ALTER TABLE push_subscriptions ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE push_subscriptions ADD COLUMN IF NOT EXISTS notify_progress BOOLEAN NOT NULL DEFAULT TRUE`,
		`ALTER TABLE push_subscriptions ADD COLUMN IF NOT EXISTS notify_reminders BOOLEAN NOT NULL DEFAULT TRUE`,
		`ALTER TABLE push_subscriptions ADD COLUMN IF NOT EXISTS notify_summaries BOOLEAN NOT NULL DEFAULT FALSE`,
		`WITH ordered AS (SELECT id,row_number() OVER (ORDER BY created_at,id) AS position FROM tasks WHERE sort_order=0) UPDATE tasks SET sort_order=ordered.position*1024 FROM ordered WHERE tasks.id=ordered.id`,
		`UPDATE tasks SET created_by=COALESCE((SELECT actor FROM events WHERE events.task_id=tasks.id ORDER BY created_at LIMIT 1),'') WHERE created_by=''`,
	}
	statements = append(statements, postgresIndexStatements()...)
	if err := executeStatements(ctx, tx, statements); err != nil {
		return fmt.Errorf("upgrade legacy PostgreSQL baseline: %w", err)
	}
	return nil
}

func ensureSQLiteColumn(ctx context.Context, tx *Tx, table, column, definition string) error {
	columns, err := tableColumns(ctx, tx, DialectSQLite, table)
	if err != nil {
		return err
	}
	if columns[column] {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}

func validateCurrentSchema(ctx context.Context, tx *Tx, dialect Dialect) error {
	tables := make([]string, 0, len(requiredSchema))
	for table := range requiredSchema {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		exists, err := tableExists(ctx, tx, dialect, table)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("schema version %d is partial: required table %s is missing", latestSchemaVersion, table)
		}
		columns, err := tableColumns(ctx, tx, dialect, table)
		if err != nil {
			return err
		}
		for _, column := range requiredSchema[table] {
			if !columns[column] {
				return fmt.Errorf("schema version %d is partial: required column %s.%s is missing", latestSchemaVersion, table, column)
			}
		}
	}
	for _, index := range requiredIndexes {
		exists, err := indexExists(ctx, tx, dialect, index)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("schema version %d is partial: required index %s is missing", latestSchemaVersion, index)
		}
	}
	return nil
}

func tableExists(ctx context.Context, tx *Tx, dialect Dialect, table string) (bool, error) {
	var exists bool
	if dialect == DialectPostgres {
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name=?)`, table).Scan(&exists)
		return exists, err
	}
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count)
	return count == 1, err
}

func tableColumns(ctx context.Context, tx *Tx, dialect Dialect, table string) (map[string]bool, error) {
	columns := make(map[string]bool)
	if dialect == DialectPostgres {
		rows, err := tx.QueryContext(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=?`, table)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				return nil, err
			}
			columns[column] = true
		}
		return columns, rows.Err()
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func indexExists(ctx context.Context, tx *Tx, dialect Dialect, index string) (bool, error) {
	var count int
	if dialect == DialectPostgres {
		err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname=?`, index).Scan(&count)
		return count == 1, err
	}
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count)
	return count == 1, err
}

func sqliteBaselineStatements() []string {
	statements := []string{
		`CREATE TABLE tasks (
			id TEXT PRIMARY KEY, title TEXT NOT NULL, summary TEXT NOT NULL DEFAULT '', task_type TEXT NOT NULL DEFAULT 'personal',
			visibility TEXT NOT NULL DEFAULT 'team', created_by TEXT NOT NULL DEFAULT '', section TEXT NOT NULL DEFAULT 'General',
			project TEXT NOT NULL DEFAULT '', repository TEXT NOT NULL DEFAULT '', priority TEXT NOT NULL DEFAULT 'normal',
			due_date TEXT NOT NULL DEFAULT '', defer_until TEXT NOT NULL DEFAULT '', recurrence TEXT NOT NULL DEFAULT '',
			sort_order INTEGER NOT NULL DEFAULT 0, reviewed_at TEXT, status TEXT NOT NULL, owner TEXT NOT NULL DEFAULT '',
			current_note TEXT NOT NULL DEFAULT '', blocker TEXT NOT NULL DEFAULT '', waiting_for TEXT NOT NULL DEFAULT '',
			version INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, completed_at TEXT
		)`,
		`CREATE TABLE checklist_items (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, label TEXT NOT NULL,
			status TEXT NOT NULL, position INTEGER NOT NULL, required INTEGER NOT NULL DEFAULT 1, note TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE agent_runs (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, agent TEXT NOT NULL,
			client TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, lease_expires_at TEXT NOT NULL, last_heartbeat_at TEXT NOT NULL,
			started_at TEXT NOT NULL, ended_at TEXT
		)`,
		`CREATE TABLE events (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, run_id TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL, actor TEXT NOT NULL, message TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL
		)`,
		`CREATE TABLE push_subscriptions (
			endpoint TEXT PRIMARY KEY, p256dh TEXT NOT NULL, auth TEXT NOT NULL, owner_id TEXT NOT NULL DEFAULT '',
			notify_progress INTEGER NOT NULL DEFAULT 1, notify_reminders INTEGER NOT NULL DEFAULT 1,
			notify_summaries INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE browser_sessions (
			token_hash BLOB PRIMARY KEY, subject TEXT NOT NULL, email TEXT NOT NULL DEFAULT '', groups_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL, expires_at TEXT NOT NULL
		)`,
		`CREATE TABLE desktop_handoffs (
			code_hash BLOB PRIMARY KEY, subject TEXT NOT NULL, email TEXT NOT NULL DEFAULT '', groups_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL, expires_at TEXT NOT NULL, confirmation_hash BLOB, verification_code TEXT NOT NULL DEFAULT '', confirmed_at TEXT
		)`,
		`CREATE TABLE task_templates (
			id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, title TEXT NOT NULL, summary TEXT NOT NULL DEFAULT '',
			task_type TEXT NOT NULL DEFAULT 'personal', section TEXT NOT NULL DEFAULT 'General', project TEXT NOT NULL DEFAULT '',
			repository TEXT NOT NULL DEFAULT '', priority TEXT NOT NULL DEFAULT 'normal', recurrence TEXT NOT NULL DEFAULT '',
			checklist_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
	}
	return append(statements, sqliteIndexStatements()...)
}

func postgresBaselineStatements() []string {
	statements := []string{
		`CREATE TABLE tasks (
			id TEXT PRIMARY KEY, title TEXT NOT NULL, summary TEXT NOT NULL DEFAULT '', task_type TEXT NOT NULL DEFAULT 'personal',
			visibility TEXT NOT NULL DEFAULT 'team', created_by TEXT NOT NULL DEFAULT '', section TEXT NOT NULL DEFAULT 'General',
			project TEXT NOT NULL DEFAULT '', repository TEXT NOT NULL DEFAULT '', priority TEXT NOT NULL DEFAULT 'normal',
			due_date TEXT NOT NULL DEFAULT '', defer_until TEXT NOT NULL DEFAULT '', recurrence TEXT NOT NULL DEFAULT '',
			sort_order BIGINT NOT NULL DEFAULT 0, reviewed_at TEXT, status TEXT NOT NULL, owner TEXT NOT NULL DEFAULT '',
			current_note TEXT NOT NULL DEFAULT '', blocker TEXT NOT NULL DEFAULT '', waiting_for TEXT NOT NULL DEFAULT '',
			version BIGINT NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, completed_at TEXT
		)`,
		`CREATE TABLE checklist_items (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, label TEXT NOT NULL,
			status TEXT NOT NULL, position INTEGER NOT NULL, required BOOLEAN NOT NULL DEFAULT TRUE, note TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE agent_runs (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, agent TEXT NOT NULL,
			client TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, lease_expires_at TEXT NOT NULL, last_heartbeat_at TEXT NOT NULL,
			started_at TEXT NOT NULL, ended_at TEXT
		)`,
		`CREATE TABLE events (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, run_id TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL, actor TEXT NOT NULL, message TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL
		)`,
		`CREATE TABLE push_subscriptions (
			endpoint TEXT PRIMARY KEY, p256dh TEXT NOT NULL, auth TEXT NOT NULL, owner_id TEXT NOT NULL DEFAULT '',
			notify_progress BOOLEAN NOT NULL DEFAULT TRUE, notify_reminders BOOLEAN NOT NULL DEFAULT TRUE,
			notify_summaries BOOLEAN NOT NULL DEFAULT FALSE, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE browser_sessions (
			token_hash BYTEA PRIMARY KEY, subject TEXT NOT NULL, email TEXT NOT NULL DEFAULT '', groups_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL, expires_at TEXT NOT NULL
		)`,
		`CREATE TABLE desktop_handoffs (
			code_hash BYTEA PRIMARY KEY, subject TEXT NOT NULL, email TEXT NOT NULL DEFAULT '', groups_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL, expires_at TEXT NOT NULL, confirmation_hash BYTEA, verification_code TEXT NOT NULL DEFAULT '', confirmed_at TEXT
		)`,
		`CREATE TABLE task_templates (
			id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, title TEXT NOT NULL, summary TEXT NOT NULL DEFAULT '',
			task_type TEXT NOT NULL DEFAULT 'personal', section TEXT NOT NULL DEFAULT 'General', project TEXT NOT NULL DEFAULT '',
			repository TEXT NOT NULL DEFAULT '', priority TEXT NOT NULL DEFAULT 'normal', recurrence TEXT NOT NULL DEFAULT '',
			checklist_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
	}
	return append(statements, postgresIndexStatements()...)
}

func sqliteIndexStatements() []string { return commonIndexStatements() }

func postgresIndexStatements() []string { return commonIndexStatements() }

func commonIndexStatements() []string {
	return []string{
		`CREATE INDEX IF NOT EXISTS idx_tasks_status_updated ON tasks(status, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_checklist_task_position ON checklist_items(task_id, position)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_task ON agent_runs(task_id, started_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_lease ON agent_runs(status, lease_expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_events_created ON events(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_browser_sessions_expires ON browser_sessions(expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_desktop_handoffs_expires ON desktop_handoffs(expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_templates_name ON task_templates(name)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_visibility_creator ON tasks(visibility, created_by)`,
		`CREATE INDEX IF NOT EXISTS idx_push_subscriptions_owner ON push_subscriptions(owner_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_desktop_handoffs_confirmation ON desktop_handoffs(confirmation_hash)`,
	}
}
