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
	"net/url"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/kilo666mj/taskboard/internal/agentidentity"
	"github.com/kilo666mj/taskboard/internal/model"
	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

type Store struct {
	db          *DB
	databaseURL string
}

func Open(ctx context.Context, path string) (*Store, error) {
	return open(ctx, "sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", DialectSQLite)
}

func OpenURL(ctx context.Context, databaseURL string) (*Store, error) {
	parsed, err := url.Parse(strings.TrimSpace(databaseURL))
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return nil, fmt.Errorf("database URL scheme must be postgres or postgresql")
	}
	if parsed.Host == "" || parsed.Path == "" || parsed.Path == "/" {
		return nil, fmt.Errorf("database URL must include a host and database name")
	}
	store, err := open(ctx, "pgx", databaseURL, DialectPostgres)
	if err == nil {
		store.databaseURL = databaseURL
	}
	return store, err
}

func open(ctx context.Context, driver, dsn string, dialect Dialect) (*Store, error) {
	raw, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	if dialect == DialectSQLite {
		raw.SetMaxOpenConns(1)
	} else {
		raw.SetMaxOpenConns(20)
		raw.SetMaxIdleConns(5)
		raw.SetConnMaxLifetime(30 * time.Minute)
		raw.SetConnMaxIdleTime(5 * time.Minute)
	}
	store := &Store{db: &DB{raw: raw, dialect: dialect}}
	if err := store.migrate(ctx); err != nil {
		return nil, errors.Join(err, raw.Close())
	}
	return store, nil
}

func (s *Store) Close() error                   { return s.db.raw.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.raw.PingContext(ctx) }
func (s *Store) DB() *DB                        { return s.db }
func (s *Store) Dialect() Dialect               { return s.db.dialect }

type BrowserIdentity struct {
	Subject string
	Email   string
	Groups  []string
	Service bool
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

func (s *Store) CreateDesktopHandoff(ctx context.Context, code, confirmation, verificationCode string, identity BrowserIdentity, lifetime time.Duration) error {
	if len(code) < 8 || confirmation == "" || verificationCode == "" {
		return fmt.Errorf("desktop handoff confirmation is incomplete")
	}
	groups, err := json.Marshal(identity.Groups)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO desktop_handoffs(code_hash,subject,email,groups_json,created_at,expires_at,confirmation_hash,verification_code) VALUES(?,?,?,?,?,?,?,?)`, credentialHash(code), identity.Subject, identity.Email, string(groups), formatTime(now), formatTime(now.Add(lifetime)), credentialHash(confirmation), verificationCode)
	return err
}

func (s *Store) PendingDesktopHandoff(ctx context.Context, confirmation string) (string, error) {
	var verificationCode, expires string
	err := s.db.QueryRowContext(ctx, `SELECT verification_code,expires_at FROM desktop_handoffs WHERE confirmation_hash=? AND confirmed_at IS NULL`, credentialHash(confirmation)).Scan(&verificationCode, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !expiresAt.After(time.Now().UTC()) {
		_, _ = s.db.ExecContext(context.Background(), `DELETE FROM desktop_handoffs WHERE confirmation_hash=?`, credentialHash(confirmation))
		return "", ErrNotFound
	}
	return verificationCode, nil
}

func (s *Store) ConfirmDesktopHandoff(ctx context.Context, confirmation string) error {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE desktop_handoffs SET confirmed_at=?,confirmation_hash=NULL WHERE confirmation_hash=? AND confirmed_at IS NULL AND expires_at>?`, formatTime(now), credentialHash(confirmation), formatTime(now))
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CancelDesktopHandoff(ctx context.Context, confirmation string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM desktop_handoffs WHERE confirmation_hash=?`, credentialHash(confirmation))
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
	err = tx.QueryRowContext(ctx, `SELECT subject,email,groups_json,expires_at FROM desktop_handoffs WHERE code_hash=? AND confirmed_at IS NOT NULL`, credentialHash(code)).Scan(&identity.Subject, &identity.Email, &groups, &expires)
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
	result, err := s.db.ExecContext(ctx, `INSERT INTO push_subscriptions(endpoint,p256dh,auth,owner_id,notify_progress,notify_reminders,notify_summaries,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(endpoint) DO UPDATE SET p256dh=excluded.p256dh,auth=excluded.auth,updated_at=excluded.updated_at
		WHERE push_subscriptions.owner_id=excluded.owner_id`, subscription.Endpoint, subscription.P256DH, subscription.Auth, subscription.OwnerID, subscription.NotifyProgress, subscription.NotifyReminders, subscription.NotifySummaries, now, now)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	return nil
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

func InsertEvent(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, event model.Event) error {
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
		var required bool
		var updated string
		if err := rows.Scan(&item.ID, &item.TaskID, &item.Label, &item.Status, &item.Position, &required, &item.Note, &updated); err != nil {
			return nil, err
		}
		item.Required = required
		item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) listRuns(ctx context.Context, taskID string) ([]model.AgentRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,agent,client,callsign,status,lease_expires_at,last_heartbeat_at,started_at,ended_at FROM agent_runs WHERE task_id=? ORDER BY started_at DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	runs := []model.AgentRun{}
	for rows.Next() {
		var run model.AgentRun
		var lease, heartbeat, started string
		var ended sql.NullString
		if err := rows.Scan(&run.ID, &run.TaskID, &run.Agent, &run.Client, &run.Callsign, &run.Status, &lease, &heartbeat, &started, &ended); err != nil {
			return nil, err
		}
		run.Tone = agentidentity.Tone(run.ID)
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
