package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct{ db *sql.DB }

func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return store, nil
}

func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *Store) DB() *sql.DB                    { return s.db }

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY, title TEXT NOT NULL, summary TEXT NOT NULL DEFAULT '', task_type TEXT NOT NULL DEFAULT 'personal',
			visibility TEXT NOT NULL DEFAULT 'team', created_by TEXT NOT NULL DEFAULT '',
            section TEXT NOT NULL DEFAULT 'General', project TEXT NOT NULL DEFAULT '', repository TEXT NOT NULL DEFAULT '',
            priority TEXT NOT NULL DEFAULT 'normal', due_date TEXT NOT NULL DEFAULT '', defer_until TEXT NOT NULL DEFAULT '',
            recurrence TEXT NOT NULL DEFAULT '', sort_order INTEGER NOT NULL DEFAULT 0, reviewed_at TEXT,
            status TEXT NOT NULL, owner TEXT NOT NULL DEFAULT '',
            current_note TEXT NOT NULL DEFAULT '', blocker TEXT NOT NULL DEFAULT '', waiting_for TEXT NOT NULL DEFAULT '',
            version INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, completed_at TEXT
        )`,
		`CREATE TABLE IF NOT EXISTS checklist_items (
            id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
            label TEXT NOT NULL, status TEXT NOT NULL, position INTEGER NOT NULL, required INTEGER NOT NULL DEFAULT 1,
            note TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS agent_runs (
            id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
            agent TEXT NOT NULL, client TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
            lease_expires_at TEXT NOT NULL, last_heartbeat_at TEXT NOT NULL, started_at TEXT NOT NULL, ended_at TEXT
        )`,
		`CREATE TABLE IF NOT EXISTS events (
            id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
            run_id TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL, actor TEXT NOT NULL,
            message TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS push_subscriptions (
			endpoint TEXT PRIMARY KEY, p256dh TEXT NOT NULL, auth TEXT NOT NULL, owner_id TEXT NOT NULL DEFAULT '',
			notify_progress INTEGER NOT NULL DEFAULT 1, notify_reminders INTEGER NOT NULL DEFAULT 1, notify_summaries INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS browser_sessions (
            token_hash BLOB PRIMARY KEY, subject TEXT NOT NULL, email TEXT NOT NULL DEFAULT '',
            groups_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, expires_at TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS desktop_handoffs (
            code_hash BLOB PRIMARY KEY, subject TEXT NOT NULL, email TEXT NOT NULL DEFAULT '',
            groups_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, expires_at TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS task_templates (
			id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, title TEXT NOT NULL, summary TEXT NOT NULL DEFAULT '', task_type TEXT NOT NULL DEFAULT 'personal',
			section TEXT NOT NULL DEFAULT 'General', project TEXT NOT NULL DEFAULT '', repository TEXT NOT NULL DEFAULT '',
			priority TEXT NOT NULL DEFAULT 'normal', recurrence TEXT NOT NULL DEFAULT '', checklist_json TEXT NOT NULL DEFAULT '[]',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_status_updated ON tasks(status, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_checklist_task_position ON checklist_items(task_id, position)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_task ON agent_runs(task_id, started_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_lease ON agent_runs(status, lease_expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_events_created ON events(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_browser_sessions_expires ON browser_sessions(expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_desktop_handoffs_expires ON desktop_handoffs(expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_templates_name ON task_templates(name)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	if err := s.ensureColumn(ctx, "tasks", "section", `TEXT NOT NULL DEFAULT 'General'`); err != nil {
		return err
	}
	for _, migration := range []struct{ name, definition string }{
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
	} {
		if err := s.ensureColumn(ctx, "tasks", migration.name, migration.definition); err != nil {
			return err
		}
	}
	if err := s.ensureColumn(ctx, "task_templates", "task_type", `TEXT NOT NULL DEFAULT 'personal'`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET sort_order=rowid*1024 WHERE sort_order=0`); err != nil {
		return fmt.Errorf("backfill task ordering: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET created_by=COALESCE((SELECT actor FROM events WHERE events.task_id=tasks.id ORDER BY created_at LIMIT 1),'') WHERE created_by=''`); err != nil {
		return fmt.Errorf("backfill task creators: %w", err)
	}
	for _, migration := range []struct{ name, definition string }{
		{"owner_id", `TEXT NOT NULL DEFAULT ''`},
		{"notify_progress", `INTEGER NOT NULL DEFAULT 1`},
		{"notify_reminders", `INTEGER NOT NULL DEFAULT 1`},
		{"notify_summaries", `INTEGER NOT NULL DEFAULT 0`},
	} {
		if err := s.ensureColumn(ctx, "push_subscriptions", migration.name, migration.definition); err != nil {
			return err
		}
	}
	for _, statement := range []string{
		`CREATE INDEX IF NOT EXISTS idx_tasks_visibility_creator ON tasks(visibility, created_by)`,
		`CREATE INDEX IF NOT EXISTS idx_push_subscriptions_owner ON push_subscriptions(owner_id)`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate visibility indexes: %w", err)
		}
	}
	_, err := s.db.ExecContext(ctx, "PRAGMA optimize")
	return err
}

func (s *Store) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return fmt.Errorf("inspect %s schema: %w", table, err)
	}
	found := false
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return errors.Join(err, rows.Close())
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}

type BrowserIdentity struct {
	Subject string
	Email   string
	Groups  []string
}

func (s *Store) CreateBrowserSession(ctx context.Context, identity BrowserIdentity, lifetime time.Duration) (string, time.Time, error) {
	token, err := randomCredential()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().UTC().Add(lifetime)
	groups, err := json.Marshal(identity.Groups)
	if err != nil {
		return "", time.Time{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO browser_sessions(token_hash,subject,email,groups_json,created_at,expires_at) VALUES(?,?,?,?,?,?)`, credentialHash(token), identity.Subject, identity.Email, string(groups), formatTime(time.Now()), formatTime(expires))
	return token, expires, err
}

func (s *Store) BrowserSession(ctx context.Context, token string) (BrowserIdentity, bool, error) {
	if token == "" {
		return BrowserIdentity{}, false, nil
	}
	var identity BrowserIdentity
	var groups, expires string
	err := s.db.QueryRowContext(ctx, `SELECT subject,email,groups_json,expires_at FROM browser_sessions WHERE token_hash=?`, credentialHash(token)).Scan(&identity.Subject, &identity.Email, &groups, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return BrowserIdentity{}, false, nil
	}
	if err != nil {
		return BrowserIdentity{}, false, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !expiresAt.After(time.Now().UTC()) {
		_, _ = s.db.ExecContext(context.Background(), `DELETE FROM browser_sessions WHERE token_hash=?`, credentialHash(token))
		return BrowserIdentity{}, false, nil
	}
	if err := json.Unmarshal([]byte(groups), &identity.Groups); err != nil {
		return BrowserIdentity{}, false, err
	}
	return identity, true, nil
}

func (s *Store) DeleteBrowserSession(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM browser_sessions WHERE token_hash=?`, credentialHash(token))
	return err
}

func (s *Store) CreateDesktopHandoff(ctx context.Context, code string, identity BrowserIdentity, lifetime time.Duration) error {
	groups, err := json.Marshal(identity.Groups)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO desktop_handoffs(code_hash,subject,email,groups_json,created_at,expires_at) VALUES(?,?,?,?,?,?)`, credentialHash(code), identity.Subject, identity.Email, string(groups), formatTime(now), formatTime(now.Add(lifetime)))
	return err
}

func (s *Store) ExchangeDesktopHandoff(ctx context.Context, code string, sessionLifetime time.Duration) (string, time.Time, BrowserIdentity, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, BrowserIdentity{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var identity BrowserIdentity
	var groups, expires string
	err = tx.QueryRowContext(ctx, `SELECT subject,email,groups_json,expires_at FROM desktop_handoffs WHERE code_hash=?`, credentialHash(code)).Scan(&identity.Subject, &identity.Email, &groups, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, BrowserIdentity{}, ErrNotFound
	}
	if err != nil {
		return "", time.Time{}, BrowserIdentity{}, err
	}
	expiresAt, parseErr := time.Parse(time.RFC3339Nano, expires)
	if parseErr != nil || !expiresAt.After(time.Now().UTC()) {
		_, _ = tx.ExecContext(ctx, `DELETE FROM desktop_handoffs WHERE code_hash=?`, credentialHash(code))
		if commitErr := tx.Commit(); commitErr != nil {
			return "", time.Time{}, BrowserIdentity{}, commitErr
		}
		return "", time.Time{}, BrowserIdentity{}, ErrNotFound
	}
	if err := json.Unmarshal([]byte(groups), &identity.Groups); err != nil {
		return "", time.Time{}, BrowserIdentity{}, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM desktop_handoffs WHERE code_hash=?`, credentialHash(code))
	if err != nil {
		return "", time.Time{}, BrowserIdentity{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return "", time.Time{}, BrowserIdentity{}, ErrNotFound
	}
	token, err := randomCredential()
	if err != nil {
		return "", time.Time{}, BrowserIdentity{}, err
	}
	sessionExpires := time.Now().UTC().Add(sessionLifetime)
	_, err = tx.ExecContext(ctx, `INSERT INTO browser_sessions(token_hash,subject,email,groups_json,created_at,expires_at) VALUES(?,?,?,?,?,?)`, credentialHash(token), identity.Subject, identity.Email, string(groups), formatTime(time.Now()), formatTime(sessionExpires))
	if err != nil {
		return "", time.Time{}, BrowserIdentity{}, err
	}
	if err := tx.Commit(); err != nil {
		return "", time.Time{}, BrowserIdentity{}, err
	}
	return token, sessionExpires, identity, nil
}

func randomCredential() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func credentialHash(value string) []byte {
	hash := sha256.Sum256([]byte(value))
	return hash[:]
}

type PushSubscription struct {
	Endpoint        string
	P256DH          string
	Auth            string
	OwnerID         string
	NotifyProgress  bool
	NotifyReminders bool
	NotifySummaries bool
	PreferencesSet  bool
}

func (s *Store) SavePushSubscription(ctx context.Context, subscription PushSubscription) error {
	now := formatTime(time.Now())
	if !subscription.PreferencesSet {
		subscription.NotifyProgress = true
		subscription.NotifyReminders = true
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO push_subscriptions(endpoint,p256dh,auth,owner_id,notify_progress,notify_reminders,notify_summaries,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(endpoint) DO UPDATE SET p256dh=excluded.p256dh,auth=excluded.auth,owner_id=excluded.owner_id,updated_at=excluded.updated_at`, subscription.Endpoint, subscription.P256DH, subscription.Auth, subscription.OwnerID, subscription.NotifyProgress, subscription.NotifyReminders, subscription.NotifySummaries, now, now)
	return err
}

func (s *Store) UpdatePushPreferences(ctx context.Context, endpoint, ownerID string, progress, reminders, summaries bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE push_subscriptions SET notify_progress=?,notify_reminders=?,notify_summaries=?,updated_at=? WHERE endpoint=? AND owner_id=?`, progress, reminders, summaries, formatTime(time.Now()), endpoint, ownerID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeletePushSubscription(ctx context.Context, endpoint, ownerID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE endpoint=? AND owner_id=?`, endpoint, ownerID)
	return err
}

func (s *Store) ListPushSubscriptions(ctx context.Context) ([]PushSubscription, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT endpoint,p256dh,auth,owner_id,notify_progress,notify_reminders,notify_summaries FROM push_subscriptions ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var subscriptions []PushSubscription
	for rows.Next() {
		var item PushSubscription
		if err := rows.Scan(&item.Endpoint, &item.P256DH, &item.Auth, &item.OwnerID, &item.NotifyProgress, &item.NotifyReminders, &item.NotifySummaries); err != nil {
			return nil, err
		}
		subscriptions = append(subscriptions, item)
	}
	return subscriptions, rows.Err()
}

func InsertEvent(ctx context.Context, tx *sql.Tx, event model.Event) error {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO events(id, task_id, run_id, kind, actor, message, payload, created_at)
        VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, event.ID, event.TaskID, event.RunID, event.Kind, event.Actor, event.Message, string(payload), formatTime(event.CreatedAt))
	return err
}

func LoadTask(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (model.Task, error) {
	var task model.Task
	var created, updated string
	var completed sql.NullString
	var reviewed sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id,title,summary,task_type,visibility,created_by,section,project,repository,priority,due_date,defer_until,recurrence,sort_order,reviewed_at,status,owner,current_note,blocker,waiting_for,version,created_at,updated_at,completed_at FROM tasks WHERE id=?`, id).
		Scan(&task.ID, &task.Title, &task.Summary, &task.Type, &task.Visibility, &task.CreatedBy, &task.Section, &task.Project, &task.Repository, &task.Priority, &task.DueDate, &task.DeferUntil, &task.Recurrence, &task.SortOrder, &reviewed, &task.Status, &task.Owner, &task.CurrentNote, &task.Blocker, &task.WaitingFor, &task.Version, &created, &updated, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Task{}, ErrNotFound
	}
	if err != nil {
		return model.Task{}, err
	}
	task.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	task.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if completed.Valid {
		value, _ := time.Parse(time.RFC3339Nano, completed.String)
		task.CompletedAt = &value
	}
	if reviewed.Valid {
		value, _ := time.Parse(time.RFC3339Nano, reviewed.String)
		task.ReviewedAt = &value
	}
	return task, nil
}

func (s *Store) GetTask(ctx context.Context, id string) (model.Task, error) {
	task, err := LoadTask(ctx, s.db, id)
	if err != nil {
		return task, err
	}
	if task.Items, err = s.listItems(ctx, id); err != nil {
		return model.Task{}, err
	}
	if task.Runs, err = s.listRuns(ctx, id); err != nil {
		return model.Task{}, err
	}
	return task, nil
}

func (s *Store) ListTasks(ctx context.Context, statuses []model.TaskStatus, limit int) ([]model.Task, error) {
	return s.listTasks(ctx, statuses, limit, "", nil)
}

func (s *Store) ListVisibleTasks(ctx context.Context, statuses []model.TaskStatus, limit int, viewer string, agent bool) ([]model.Task, error) {
	if agent {
		return s.listTasks(ctx, statuses, limit, `(visibility=? OR (visibility=? AND owner=?))`, []any{model.VisibilityAgent, model.VisibilityTeam, viewer})
	}
	return s.listTasks(ctx, statuses, limit, `(visibility IN (?,?) OR (visibility=? AND created_by=?))`, []any{model.VisibilityTeam, model.VisibilityAgent, model.VisibilityPrivate, viewer})
}

func (s *Store) listTasks(ctx context.Context, statuses []model.TaskStatus, limit int, visibilityClause string, visibilityArgs []any) ([]model.Task, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `SELECT id,title,summary,task_type,visibility,created_by,section,project,repository,priority,due_date,defer_until,recurrence,sort_order,reviewed_at,status,owner,current_note,blocker,waiting_for,version,created_at,updated_at,completed_at FROM tasks`
	args := make([]any, 0, len(statuses)+len(visibilityArgs)+1)
	where := ""
	if visibilityClause != "" {
		where = visibilityClause
		args = append(args, visibilityArgs...)
	}
	if len(statuses) > 0 {
		if where != "" {
			where += " AND "
		}
		where += "status IN ("
		for i, status := range statuses {
			if i > 0 {
				where += ","
			}
			where += "?"
			args = append(args, status)
		}
		where += ")"
	}
	if where != "" {
		query += " WHERE " + where
	}
	query += " ORDER BY CASE status WHEN 'blocked' THEN 0 WHEN 'active' THEN 1 WHEN 'waiting' THEN 2 WHEN 'queued' THEN 3 ELSE 4 END, section, sort_order, updated_at DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var tasks []model.Task
	for rows.Next() {
		var task model.Task
		var created, updated string
		var reviewed, completed sql.NullString
		if err := rows.Scan(&task.ID, &task.Title, &task.Summary, &task.Type, &task.Visibility, &task.CreatedBy, &task.Section, &task.Project, &task.Repository, &task.Priority, &task.DueDate, &task.DeferUntil, &task.Recurrence, &task.SortOrder, &reviewed, &task.Status, &task.Owner, &task.CurrentNote, &task.Blocker, &task.WaitingFor, &task.Version, &created, &updated, &completed); err != nil {
			return nil, err
		}
		task.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		task.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		if completed.Valid {
			value, _ := time.Parse(time.RFC3339Nano, completed.String)
			task.CompletedAt = &value
		}
		if reviewed.Valid {
			value, _ := time.Parse(time.RFC3339Nano, reviewed.String)
			task.ReviewedAt = &value
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Join(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range tasks {
		tasks[index].Items, err = s.listItems(ctx, tasks[index].ID)
		if err != nil {
			return nil, err
		}
		tasks[index].Runs, err = s.listRuns(ctx, tasks[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return tasks, nil
}

func (s *Store) listItems(ctx context.Context, taskID string) ([]model.ChecklistItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,label,status,position,required,note,updated_at FROM checklist_items WHERE task_id=? ORDER BY position`, taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []model.ChecklistItem{}
	for rows.Next() {
		var item model.ChecklistItem
		var required int
		var updated string
		if err := rows.Scan(&item.ID, &item.TaskID, &item.Label, &item.Status, &item.Position, &required, &item.Note, &updated); err != nil {
			return nil, err
		}
		item.Required = required != 0
		item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) listRuns(ctx context.Context, taskID string) ([]model.AgentRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,agent,client,status,lease_expires_at,last_heartbeat_at,started_at,ended_at FROM agent_runs WHERE task_id=? ORDER BY started_at DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	runs := []model.AgentRun{}
	for rows.Next() {
		var run model.AgentRun
		var lease, heartbeat, started string
		var ended sql.NullString
		if err := rows.Scan(&run.ID, &run.TaskID, &run.Agent, &run.Client, &run.Status, &lease, &heartbeat, &started, &ended); err != nil {
			return nil, err
		}
		run.LeaseExpires, _ = time.Parse(time.RFC3339Nano, lease)
		run.LastHeartbeat, _ = time.Parse(time.RFC3339Nano, heartbeat)
		run.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		if ended.Valid {
			value, _ := time.Parse(time.RFC3339Nano, ended.String)
			run.EndedAt = &value
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
