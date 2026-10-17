package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Migrations after lastOneWaySchemaVersion must keep the previous release
// running (additive only) and must reverse cleanly.
func TestMigrationsAfterOneWayCutoffAreRollbackSafe(t *testing.T) {
	if compatibleFromSchemaVersion < lastOneWaySchemaVersion || compatibleFromSchemaVersion > latestSchemaVersion {
		t.Fatalf("compatibleFromSchemaVersion = %d, want %d..%d", compatibleFromSchemaVersion, lastOneWaySchemaVersion, latestSchemaVersion)
	}
	for _, migration := range schemaMigrations[lastOneWaySchemaVersion:] {
		if err := checkRollbackSafeMigration(migration); err != nil {
			t.Error(err)
		}
	}
}

func TestRollbackSafeCheckRejectsDestructiveMigrations(t *testing.T) {
	down := []string{`SELECT 1`}
	cases := []struct {
		name      string
		statement string
		retires   []string
		want      string
	}{
		{"add nullable column", `ALTER TABLE tasks ADD COLUMN compat_note TEXT`, nil, ""},
		{"add defaulted column", `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS compat_note TEXT NOT NULL DEFAULT ''`, nil, ""},
		{"create table", `CREATE TABLE compat_notes (id TEXT PRIMARY KEY)`, nil, ""},
		{"drop constraint", `ALTER TABLE tasks DROP CONSTRAINT tasks_title_check`, nil, ""},
		{"drop in-use column", `ALTER TABLE tasks DROP COLUMN title`, nil, "drops tasks.title"},
		{"drop in-use column without keyword", `ALTER TABLE tasks DROP IF EXISTS title`, nil, "drops tasks.title"},
		{"retire in-use column", `ALTER TABLE tasks DROP COLUMN title`, []string{"tasks.title"}, "still requires tasks.title"},
		{"retire unused column", `ALTER TABLE tasks DROP COLUMN compat_note`, []string{"tasks.compat_note"}, ""},
		{"rename column", `ALTER TABLE tasks RENAME COLUMN title TO name`, nil, "renames"},
		{"rename table", `ALTER TABLE tasks RENAME TO work_items`, nil, "renames"},
		{"change column type", `ALTER TABLE tasks ALTER COLUMN version TYPE TEXT`, nil, "alters a column"},
		{"tighten nullability", `ALTER TABLE tasks ALTER COLUMN reviewed_at SET NOT NULL`, nil, "alters a column"},
		{"relax nullability", `ALTER TABLE tasks ALTER COLUMN reviewed_at DROP NOT NULL`, nil, ""},
		{"required column without default", `ALTER TABLE tasks ADD COLUMN compat_note TEXT NOT NULL`, nil, "without a DEFAULT"},
		{"drop in-use table", `DROP TABLE IF EXISTS task_messages`, nil, "drops task_messages"},
		{"drop in-use index", `DROP INDEX idx_runs_lease`, nil, "drops idx_runs_lease"},
		{"retire in-use index", `DROP INDEX idx_runs_lease`, []string{"idx_runs_lease"}, "still requires idx_runs_lease"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			migration := schemaMigration{Version: latestSchemaVersion + 1, Name: "probe", SQLite: []string{`SELECT 1`}, Postgres: []string{test.statement}, SQLiteDown: down, PostgresDown: down, Retires: test.retires}
			err := checkRollbackSafeMigration(migration)
			if test.want == "" && err != nil {
				t.Fatalf("check error = %v, want none", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("check error = %v, want %q", err, test.want)
			}
		})
	}
	missingDown := schemaMigration{Version: latestSchemaVersion + 1, Name: "probe", SQLite: []string{`SELECT 1`}, Postgres: []string{`SELECT 1`}}
	if err := checkRollbackSafeMigration(missingDown); err == nil || !strings.Contains(err.Error(), "down statements") {
		t.Fatalf("check error = %v, want missing down statements", err)
	}
}

func TestPreviousReleaseRunsOnNextReleaseSchema(t *testing.T) {
	forEachCompatDialect(t, func(t *testing.T, connect func() *DB) {
		next := nextReleaseRegistry(currentRegistry(), "compat_next")
		migrateWithRegistry(t, connect(), next)

		database := &Store{db: connect()}
		defer func() { _ = database.Close() }()
		if err := database.migrate(t.Context()); err != nil {
			t.Fatalf("previous release refused the next release's schema: %v", err)
		}
		if _, err := database.DB().ExecContext(t.Context(), `INSERT INTO tasks(id,title,status,version,created_at,updated_at) VALUES('compat','Written by the previous release','queued',1,'2026-10-06T00:00:00Z','2026-10-06T00:00:00Z')`); err != nil {
			t.Fatalf("previous release cannot write on the next release's schema: %v", err)
		}
		task, err := database.GetTask(t.Context(), "compat")
		if err != nil || task.Title != "Written by the previous release" {
			t.Fatalf("GetTask = %+v, %v", task, err)
		}
		var head int
		if err := database.DB().QueryRowContext(t.Context(), `SELECT MAX(version) FROM schema_migrations`).Scan(&head); err != nil {
			t.Fatal(err)
		}
		if head != next.latest() {
			t.Fatalf("schema head = %d, want the next release's %d", head, next.latest())
		}
		// Rolling forward again finds its own schema intact.
		migrateWithRegistry(t, connect(), next)
	})
}

func TestBinaryRefusesSchemaTwoReleasesAhead(t *testing.T) {
	forEachCompatDialect(t, func(t *testing.T, connect func() *DB) {
		afterNext := nextReleaseRegistry(nextReleaseRegistry(currentRegistry(), "compat_next"), "compat_after_next")
		migrateWithRegistry(t, connect(), afterNext)

		database := &Store{db: connect()}
		defer func() { _ = database.Close() }()
		err := database.migrate(t.Context())
		if err == nil || !strings.Contains(err.Error(), "newer than supported") {
			t.Fatalf("migrate error = %v, want newer schema refusal", err)
		}
	})
}

func TestNewMigrationsApplyAndReverse(t *testing.T) {
	registries := map[string]migrationRegistry{
		"current":      currentRegistry(),
		"next release": nextReleaseRegistry(currentRegistry(), "compat_next"),
	}
	for name, registry := range registries {
		t.Run(name, func(t *testing.T) {
			forEachCompatDialect(t, func(t *testing.T, connect func() *DB) {
				assertMigrationsReverse(t, connect, registry)
			})
		})
	}
}

// assertMigrationsReverse applies every rollback-safe migration, reverses and
// re-applies each one from the newest down, and checks the fully reversed
// schema matches a database built from the one-way migrations alone.
func assertMigrationsReverse(t *testing.T, connect func() *DB, registry migrationRegistry) {
	t.Helper()
	migrateWithRegistry(t, connect(), registry)
	db := connect()
	defer func() { _ = db.raw.Close() }()
	for index := registry.latest() - 1; index >= lastOneWaySchemaVersion; index-- {
		migration := registry.migrations[index]
		applied := schemaSnapshot(t, db)
		changeMigration(t, db, migration, true, registry)
		reversed := schemaSnapshot(t, db)
		changeMigration(t, db, migration, false, registry)
		if again := schemaSnapshot(t, db); !slices.Equal(again, applied) {
			t.Fatalf("migration %d %s does not re-apply after reversing:\n%s", migration.Version, migration.Name, snapshotDiff(applied, again))
		}
		changeMigration(t, db, migration, true, registry)
		if again := schemaSnapshot(t, db); !slices.Equal(again, reversed) {
			t.Fatalf("migration %d %s does not reverse twice the same way:\n%s", migration.Version, migration.Name, snapshotDiff(reversed, again))
		}
	}

	baseline := openCompatSQLite(t, filepath.Join(t.TempDir(), "baseline.db"))
	if db.dialect == DialectPostgres {
		baseline = openCompatPostgres(t)
	}
	tx, err := baseline.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureMigrationTable(t.Context(), tx, baseline.dialect); err != nil {
		t.Fatal(err)
	}
	for _, migration := range registry.migrations[:lastOneWaySchemaVersion] {
		if err := executeStatements(t.Context(), tx, statementsFor(migration, baseline.dialect)); err != nil {
			t.Fatalf("apply migration %d: %v", migration.Version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if want, got := schemaSnapshot(t, baseline), schemaSnapshot(t, db); !slices.Equal(want, got) {
		t.Fatalf("reversing every rollback-safe migration does not restore schema %d:\n%s", lastOneWaySchemaVersion, snapshotDiff(want, got))
	}
}

func changeMigration(t *testing.T, db *DB, migration schemaMigration, reverse bool, registry migrationRegistry) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if reverse {
		down := migration.SQLiteDown
		if db.dialect == DialectPostgres {
			down = migration.PostgresDown
		}
		if err := executeStatements(t.Context(), tx, down); err != nil {
			t.Fatalf("reverse migration %d %s: %v", migration.Version, migration.Name, err)
		}
		if _, err := tx.ExecContext(t.Context(), `DELETE FROM schema_migrations WHERE version=?`, migration.Version); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := executeStatements(t.Context(), tx, statementsFor(migration, db.dialect)); err != nil {
			t.Fatalf("apply migration %d %s: %v", migration.Version, migration.Name, err)
		}
		if err := recordMigration(t.Context(), tx, registry, migration, db.dialect); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// schemaSnapshot lists the application's tables, columns, indexes, triggers
// and constraints in a dialect-specific but comparable form.
func schemaSnapshot(t *testing.T, db *DB) []string {
	t.Helper()
	var query string
	if db.dialect == DialectPostgres {
		query = `SELECT 'column '||table_name||'.'||column_name||' '||data_type||' '||is_nullable||' '||COALESCE(column_default,'') FROM information_schema.columns WHERE table_schema=current_schema()
			UNION ALL SELECT 'index '||indexname||' '||replace(indexdef,current_schema()||'.','') FROM pg_indexes WHERE schemaname=current_schema()
			UNION ALL SELECT 'trigger '||relation.relname||'.'||trigger.tgname FROM pg_trigger trigger JOIN pg_class relation ON relation.oid=trigger.tgrelid JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace WHERE namespace.nspname=current_schema() AND NOT trigger.tgisinternal
			UNION ALL SELECT 'constraint '||table_name||'.'||constraint_name||' '||constraint_type FROM information_schema.table_constraints WHERE table_schema=current_schema() AND constraint_type<>'CHECK'`
	} else {
		query = `SELECT 'column '||m.name||'.'||c.name||' '||c.type||' '||c."notnull"||' '||COALESCE(c.dflt_value,'')||' '||c.pk FROM sqlite_master m JOIN pragma_table_info(m.name) c WHERE m.type='table'
			UNION ALL SELECT m.type||' '||m.name||' '||COALESCE(m.sql,'') FROM sqlite_master m WHERE m.type IN ('index','trigger')
			UNION ALL SELECT 'foreign key '||m.name||'.'||f."from"||' '||f."table"||'.'||COALESCE(f."to",'') FROM sqlite_master m JOIN pragma_foreign_key_list(m.name) f WHERE m.type='table'`
	}
	rows, err := db.QueryContext(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var snapshot []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		snapshot = append(snapshot, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(snapshot)
	return snapshot
}

func snapshotDiff(want, got []string) string {
	var lines []string
	for _, line := range want {
		if !slices.Contains(got, line) {
			lines = append(lines, "- "+line)
		}
	}
	for _, line := range got {
		if !slices.Contains(want, line) {
			lines = append(lines, "+ "+line)
		}
	}
	return strings.Join(lines, "\n")
}

func currentRegistry() migrationRegistry {
	return migrationRegistry{migrations: schemaMigrations, compatibleFrom: compatibleFromSchemaVersion}
}

// nextReleaseRegistry simulates the release after base: one additive
// migration, declared readable by base's binary.
func nextReleaseRegistry(base migrationRegistry, column string) migrationRegistry {
	add := `ALTER TABLE tasks ADD COLUMN ` + column + ` TEXT NOT NULL DEFAULT ''`
	drop := `ALTER TABLE tasks DROP COLUMN ` + column
	probe := schemaMigration{
		Version: base.latest() + 1, Name: "probe_" + column,
		SQLite: []string{add}, Postgres: []string{add},
		SQLiteDown: []string{drop}, PostgresDown: []string{drop},
	}
	return migrationRegistry{migrations: append(slices.Clone(base.migrations), probe), compatibleFrom: base.latest()}
}

func migrateWithRegistry(t *testing.T, db *DB, registry migrationRegistry) {
	t.Helper()
	database := &Store{db: db}
	defer func() { _ = database.Close() }()
	if err := database.migrateRegistry(t.Context(), registry); err != nil {
		t.Fatalf("migrate to schema %d: %v", registry.latest(), err)
	}
}

// forEachCompatDialect runs check on SQLite and, when configured, PostgreSQL.
// connect opens a new connection to the same database each time.
func forEachCompatDialect(t *testing.T, check func(t *testing.T, connect func() *DB)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "taskboard.db")
		check(t, func() *DB { return openCompatSQLite(t, path) })
	})
	t.Run("postgres", func(t *testing.T) {
		baseURL := strings.TrimSpace(os.Getenv("TASKBOARD_TEST_POSTGRES_URL"))
		if baseURL == "" {
			t.Skip("TASKBOARD_TEST_POSTGRES_URL is not set")
		}
		databaseURL, _, _ := postgresMigrationFixture(t, baseURL)
		check(t, func() *DB { return openCompatURL(t, "pgx", databaseURL, DialectPostgres) })
	})
}

func openCompatSQLite(t *testing.T, path string) *DB {
	return openCompatURL(t, "sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", DialectSQLite)
}

func openCompatPostgres(t *testing.T) *DB {
	databaseURL, _, _ := postgresMigrationFixture(t, strings.TrimSpace(os.Getenv("TASKBOARD_TEST_POSTGRES_URL")))
	return openCompatURL(t, "pgx", databaseURL, DialectPostgres)
}

func openCompatURL(t *testing.T, driver, dsn string, dialect Dialect) *DB {
	t.Helper()
	raw, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return &DB{raw: raw, dialect: dialect}
}

var (
	renamePattern      = regexp.MustCompile(`(?is)\bRENAME\b`)
	alterColumnPattern = regexp.MustCompile(`(?is)\bALTER\s+COLUMN\s+\w+\s+(\w+(?:\s+\w+)?)`)
	addColumnPattern   = regexp.MustCompile(`(?is)\bADD\s+COLUMN\b`)
	notNullPattern     = regexp.MustCompile(`(?is)\bNOT\s+NULL\b`)
	defaultPattern     = regexp.MustCompile(`(?is)\bDEFAULT\b`)
	dropColumnPattern  = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?(\w+)\s+DROP\s+(?:(COLUMN|CONSTRAINT)\s+)?(?:IF\s+EXISTS\s+)?(\w+)`)
	dropObjectPattern  = regexp.MustCompile(`(?is)^\s*DROP\s+(TABLE|INDEX|TRIGGER|VIEW|FUNCTION)\s+(?:IF\s+EXISTS\s+)?(?:\w+\.)?(\w+)`)
)

// checkRollbackSafeMigration rejects statements the previous release cannot
// run against: renames, column type or nullability changes, required columns
// without a default, and drops of anything not explicitly retired. Retiring an
// object this binary still requires is rejected too; the migration
// compatibility job proves the previous release no longer needs it.
func checkRollbackSafeMigration(migration schemaMigration) error {
	if len(migration.SQLiteDown) == 0 || len(migration.PostgresDown) == 0 {
		return fmt.Errorf("migration %d %s needs SQLite and PostgreSQL down statements", migration.Version, migration.Name)
	}
	required := requiredSchemaObjects()
	for _, object := range migration.Retires {
		if required[object] {
			return fmt.Errorf("migration %d %s retires %s but this release still requires %s", migration.Version, migration.Name, object, object)
		}
	}
	for _, statement := range append(slices.Clone(migration.SQLite), migration.Postgres...) {
		if renamePattern.MatchString(statement) {
			return fmt.Errorf("migration %d %s renames a schema object; add the new name, backfill, and retire the old one a release later: %s", migration.Version, migration.Name, statement)
		}
		if match := alterColumnPattern.FindStringSubmatch(statement); match != nil {
			change := strings.ToUpper(strings.Join(strings.Fields(match[1]), " "))
			if change != "DROP NOT" && change != "SET DEFAULT" {
				return fmt.Errorf("migration %d %s alters a column in place: %s", migration.Version, migration.Name, statement)
			}
		}
		if addColumnPattern.MatchString(statement) && notNullPattern.MatchString(statement) && !defaultPattern.MatchString(statement) {
			return fmt.Errorf("migration %d %s adds a NOT NULL column without a DEFAULT, which the previous release cannot insert: %s", migration.Version, migration.Name, statement)
		}
		var dropped string
		if match := dropColumnPattern.FindStringSubmatch(statement); match != nil && !strings.EqualFold(match[2], "CONSTRAINT") {
			dropped = match[1] + "." + match[3]
		} else if match := dropObjectPattern.FindStringSubmatch(statement); match != nil {
			dropped = match[2]
		}
		if dropped != "" && !slices.Contains(migration.Retires, dropped) {
			return fmt.Errorf("migration %d %s drops %s without listing it in Retires: %s", migration.Version, migration.Name, dropped, statement)
		}
		if dropped != "" && required[dropped] {
			return fmt.Errorf("migration %d %s drops %s but this release still requires %s", migration.Version, migration.Name, dropped, dropped)
		}
	}
	return nil
}

// requiredSchemaObjects is every table ("table"), column ("table.column"),
// index and trigger this binary's schema validators require.
func requiredSchemaObjects() map[string]bool {
	objects := map[string]bool{
		"agent_runs.callsign": true, "agent_runs.session_id": true, "idx_runs_session": true,
		"admin_audit_no_update": true, "admin_audit_no_delete": true, "taskboard_event_notify": true,
	}
	schemas := []map[string][]string{
		requiredSchema, administrativeSchema, messagingSchema, escalationSchema, discussionSchema, duplicateSchema,
		runControlSchema, referenceSchema, handoffSchema, completionSchema, sessionBridgeSchema, dependencySchema,
		workerMatchingSchema, usageSchema, agentSessionSchema, scheduleSchema,
	}
	for _, schema := range schemas {
		for table, columns := range schema {
			objects[table] = true
			for _, column := range columns {
				objects[table+"."+column] = true
			}
		}
	}
	indexes := [][]string{
		requiredIndexes, administrativeIndexes, messagingIndexes, escalationIndexes, discussionIndexes, duplicateIndexes,
		runControlIndexes, referenceIndexes, handoffIndexes, completionIndexes, sessionBridgeIndexes, dependencyIndexes,
		workerMatchingIndexes, usageIndexes, agentSessionIndexes, scheduleIndexes,
	}
	for _, list := range indexes {
		for _, index := range list {
			objects[index] = true
		}
	}
	return objects
}
