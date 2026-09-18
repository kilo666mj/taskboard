package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/oklog/ulid/v2"
)

type AgentCredential struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Principal  string     `json:"principal_id"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type AdminAudit struct {
	ID        string         `json:"id"`
	Actor     string         `json:"actor"`
	Action    string         `json:"action"`
	Target    string         `json:"target,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type WebhookDelivery struct {
	ID          string    `json:"id"`
	EventID     string    `json:"event_id"`
	Status      string    `json:"status"`
	Attempts    int       `json:"attempts"`
	NextAttempt time.Time `json:"next_attempt_at"`
	LastError   string    `json:"last_error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func newAgentToken() (string, string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", "", err
	}
	token := "tb_agent_" + base64.RawURLEncoding.EncodeToString(random)
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateAgentCredential(ctx context.Context, name, principal string, expiresAt *time.Time, actor string) (AgentCredential, string, error) {
	name, principal = strings.TrimSpace(name), strings.TrimSpace(principal)
	if name == "" || len(name) > 100 || !strings.HasPrefix(principal, "agent:") || len(principal) > 200 {
		return AgentCredential{}, "", fmt.Errorf("invalid credential name or principal")
	}
	token, hash, err := newAgentToken()
	if err != nil {
		return AgentCredential{}, "", err
	}
	now := time.Now().UTC()
	var expires any
	if expiresAt != nil {
		value := expiresAt.UTC()
		expiresAt, expires = &value, formatTime(value)
	}
	credential := AgentCredential{ID: ulid.Make().String(), Name: name, Principal: principal, CreatedAt: now, ExpiresAt: expiresAt}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentCredential{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_credentials(id,name,principal_id,token_hash,created_at,expires_at) VALUES(?,?,?,?,?,?)`, credential.ID, name, principal, hash, formatTime(now), expires); err != nil {
		return AgentCredential{}, "", err
	}
	if err := insertAdminAudit(ctx, tx, actor, "credential.created", credential.ID, map[string]any{"principal_id": credential.Principal, "expires_at": expiresAt}); err != nil {
		return AgentCredential{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return AgentCredential{}, "", err
	}
	return credential, token, nil
}

func (s *Store) RotateAgentCredential(ctx context.Context, id, actor string) (AgentCredential, string, error) {
	token, hash, err := newAgentToken()
	if err != nil {
		return AgentCredential{}, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentCredential{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE agent_credentials SET token_hash=?,revoked_at=NULL,last_used_at=NULL WHERE id=?`, hash, strings.TrimSpace(id))
	if err != nil {
		return AgentCredential{}, "", err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return AgentCredential{}, "", ErrNotFound
	}
	credential, err := scanAgentCredential(tx.QueryRowContext(ctx, `SELECT id,name,principal_id,created_at,expires_at,revoked_at,last_used_at FROM agent_credentials WHERE id=?`, strings.TrimSpace(id)))
	if err != nil {
		return AgentCredential{}, "", err
	}
	if err := insertAdminAudit(ctx, tx, actor, "credential.rotated", credential.ID, map[string]any{"principal_id": credential.Principal}); err != nil {
		return AgentCredential{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return AgentCredential{}, "", err
	}
	return credential, token, nil
}

func (s *Store) RevokeAgentCredential(ctx context.Context, id, actor string) error {
	now := formatTime(time.Now().UTC())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE agent_credentials SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, now, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	if err := insertAdminAudit(ctx, tx, actor, "credential.revoked", strings.TrimSpace(id), nil); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AgentCredential(ctx context.Context, id string) (AgentCredential, error) {
	return scanAgentCredential(s.db.QueryRowContext(ctx, `SELECT id,name,principal_id,created_at,expires_at,revoked_at,last_used_at FROM agent_credentials WHERE id=?`, strings.TrimSpace(id)))
}

func (s *Store) ListAgentCredentials(ctx context.Context) ([]AgentCredential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,principal_id,created_at,expires_at,revoked_at,last_used_at FROM agent_credentials ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []AgentCredential
	for rows.Next() {
		credential, err := scanAgentCredential(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, credential)
	}
	return result, rows.Err()
}

type credentialScanner interface{ Scan(...any) error }

func scanAgentCredential(scanner credentialScanner) (AgentCredential, error) {
	var credential AgentCredential
	var created string
	var expires, revoked, lastUsed *string
	if err := scanner.Scan(&credential.ID, &credential.Name, &credential.Principal, &created, &expires, &revoked, &lastUsed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AgentCredential{}, ErrNotFound
		}
		return AgentCredential{}, err
	}
	credential.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	credential.ExpiresAt = parseOptionalTime(expires)
	credential.RevokedAt = parseOptionalTime(revoked)
	credential.LastUsedAt = parseOptionalTime(lastUsed)
	return credential, nil
}

func parseOptionalTime(value *string) *time.Time {
	if value == nil || *value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return nil
	}
	return &parsed
}

func (s *Store) AuthenticateAgentCredential(ctx context.Context, token string) (string, bool, error) {
	if !strings.HasPrefix(token, "tb_agent_") || len(token) > 128 {
		return "", false, nil
	}
	var principal string
	var expires *string
	err := s.db.QueryRowContext(ctx, `SELECT principal_id,expires_at FROM agent_credentials WHERE token_hash=? AND revoked_at IS NULL`, tokenHash(token)).Scan(&principal, &expires)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	now := time.Now().UTC()
	if parsed := parseOptionalTime(expires); parsed != nil && !parsed.After(now) {
		return "", false, nil
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE agent_credentials SET last_used_at=? WHERE token_hash=?`, formatTime(now), tokenHash(token))
	return principal, true, nil
}

func (s *Store) OffboardPrincipal(ctx context.Context, principal, actor, reason string) error {
	principal, actor, reason = strings.TrimSpace(principal), strings.TrimSpace(actor), strings.TrimSpace(reason)
	if principal == "" || actor == "" || len(principal) > 200 || len(reason) > 500 {
		return fmt.Errorf("invalid offboarding request")
	}
	now := formatTime(time.Now().UTC())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO revoked_principals(principal_id,reason,revoked_by,revoked_at) VALUES(?,?,?,?) ON CONFLICT(principal_id) DO UPDATE SET reason=excluded.reason,revoked_by=excluded.revoked_by,revoked_at=excluded.revoked_at`, principal, reason, actor, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM browser_sessions WHERE subject=?`, principal); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_credentials SET revoked_at=? WHERE principal_id=? AND revoked_at IS NULL`, now, principal); err != nil {
		return err
	}
	if err := insertAdminAudit(ctx, tx, actor, "principal.offboarded", principal, map[string]any{"reason": reason}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PrincipalRevoked(ctx context.Context, principal string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM revoked_principals WHERE principal_id=?`, strings.TrimSpace(principal)).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Store) ReinstatePrincipal(ctx context.Context, principal, actor string) error {
	principal = strings.TrimSpace(principal)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `DELETE FROM revoked_principals WHERE principal_id=?`, principal)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	if err := insertAdminAudit(ctx, tx, actor, "principal.reinstated", principal, nil); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) InsertAdminAudit(ctx context.Context, actor, action, target string, detail map[string]any) error {
	return insertAdminAudit(ctx, s.db, actor, action, target, detail)
}

type adminAuditExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertAdminAudit(ctx context.Context, executor adminAuditExecutor, actor, action, target string, detail map[string]any) error {
	actor, action = strings.TrimSpace(actor), strings.TrimSpace(action)
	if actor == "" || action == "" {
		return fmt.Errorf("admin audit actor and action are required")
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = executor.ExecContext(ctx, `INSERT INTO admin_audit(id,actor,action,target,detail,created_at) VALUES(?,?,?,?,?,?)`, ulid.Make().String(), actor, action, target, string(encoded), formatTime(time.Now().UTC()))
	return err
}

func (s *Store) ListAdminAudit(ctx context.Context, limit int) ([]AdminAudit, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	return s.listAdminAudit(ctx, `SELECT id,actor,action,target,detail,created_at FROM admin_audit ORDER BY created_at DESC,id DESC LIMIT ?`, limit)
}

func (s *Store) ListAllAdminAudit(ctx context.Context) ([]AdminAudit, error) {
	return s.listAdminAudit(ctx, `SELECT id,actor,action,target,detail,created_at FROM admin_audit ORDER BY created_at,id`)
}

func (s *Store) listAdminAudit(ctx context.Context, query string, args ...any) ([]AdminAudit, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []AdminAudit
	for rows.Next() {
		var item AdminAudit
		var detail, created string
		if err := rows.Scan(&item.ID, &item.Actor, &item.Action, &item.Target, &detail, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(detail), &item.Detail)
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) ListAllEvents(ctx context.Context) ([]model.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,run_id,kind,actor,message,payload,created_at FROM events ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []model.Event
	for rows.Next() {
		var event model.Event
		var payload, created string
		if err := rows.Scan(&event.ID, &event.TaskID, &event.RunID, &event.Kind, &event.Actor, &event.Message, &payload, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(payload), &event.Payload)
		event.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, event)
	}
	return result, rows.Err()
}

func (s *Store) ListAllTasks(ctx context.Context) ([]model.Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM tasks ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	result := make([]model.Task, 0, len(ids))
	for _, id := range ids {
		task, err := s.GetTask(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, task)
	}
	return result, nil
}

func (s *Store) DeleteTask(ctx context.Context, id, actor string) error {
	id = strings.TrimSpace(id)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE id=?`, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	if err := insertAdminAudit(ctx, tx, actor, "task.deleted", id, nil); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ApplyRetention(ctx context.Context, before time.Time, actor string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE status IN (?,?) AND completed_at IS NOT NULL AND completed_at<?`, model.TaskDone, model.TaskCancelled, formatTime(before.UTC()))
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := insertAdminAudit(ctx, tx, actor, "retention.applied", "workspace", map[string]any{"before": before.UTC(), "deleted_tasks": count}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) QueueWebhookEvents(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM events WHERE NOT EXISTS (SELECT 1 FROM webhook_deliveries WHERE webhook_deliveries.event_id=events.id) ORDER BY created_at,id LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var eventIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		eventIDs = append(eventIDs, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	now := formatTime(time.Now().UTC())
	queued := 0
	for _, eventID := range eventIDs {
		result, err := s.db.ExecContext(ctx, `INSERT INTO webhook_deliveries(id,event_id,status,attempts,next_attempt_at,created_at,updated_at) VALUES(?,?,?,0,?,?,?) ON CONFLICT(event_id) DO NOTHING`, ulid.Make().String(), eventID, "pending", now, now, now)
		if err != nil {
			return queued, err
		}
		if count, _ := result.RowsAffected(); count > 0 {
			queued++
		}
	}
	return queued, nil
}

func (s *Store) NextWebhookDelivery(ctx context.Context) (WebhookDelivery, model.Event, error) {
	row := s.db.QueryRowContext(ctx, `SELECT delivery.id,delivery.event_id,delivery.status,delivery.attempts,delivery.next_attempt_at,delivery.last_error,delivery.created_at,delivery.updated_at,
		event.task_id,event.run_id,event.kind,event.actor,event.message,event.payload,event.created_at
		FROM webhook_deliveries delivery JOIN events event ON event.id=delivery.event_id
		WHERE delivery.status IN ('pending','retry') AND delivery.next_attempt_at<=?
		ORDER BY delivery.next_attempt_at,delivery.id LIMIT 1`, formatTime(time.Now().UTC()))
	var delivery WebhookDelivery
	var event model.Event
	var nextAttempt, deliveryCreated, deliveryUpdated, payload, eventCreated string
	if err := row.Scan(&delivery.ID, &delivery.EventID, &delivery.Status, &delivery.Attempts, &nextAttempt, &delivery.LastError, &deliveryCreated, &deliveryUpdated,
		&event.TaskID, &event.RunID, &event.Kind, &event.Actor, &event.Message, &payload, &eventCreated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return WebhookDelivery{}, model.Event{}, ErrNotFound
		}
		return WebhookDelivery{}, model.Event{}, err
	}
	delivery.NextAttempt, _ = time.Parse(time.RFC3339Nano, nextAttempt)
	delivery.CreatedAt, _ = time.Parse(time.RFC3339Nano, deliveryCreated)
	delivery.UpdatedAt, _ = time.Parse(time.RFC3339Nano, deliveryUpdated)
	event.ID = delivery.EventID
	event.CreatedAt, _ = time.Parse(time.RFC3339Nano, eventCreated)
	_ = json.Unmarshal([]byte(payload), &event.Payload)
	return delivery, event, nil
}

func (s *Store) CompleteWebhookDelivery(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE webhook_deliveries SET status='delivered',last_error='',updated_at=? WHERE id=?`, formatTime(time.Now().UTC()), id)
	return err
}

func (s *Store) FailWebhookDelivery(ctx context.Context, id string, attempts, maxAttempts int, next time.Time, message string) error {
	status := "retry"
	if attempts >= maxAttempts {
		status = "dead_letter"
	}
	if len(message) > 1000 {
		message = message[:1000]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE webhook_deliveries SET status=?,attempts=?,next_attempt_at=?,last_error=?,updated_at=? WHERE id=?`, status, attempts, formatTime(next.UTC()), message, formatTime(time.Now().UTC()), id)
	return err
}

func (s *Store) ListDeadWebhookDeliveries(ctx context.Context, limit int) ([]WebhookDelivery, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,event_id,status,attempts,next_attempt_at,last_error,created_at,updated_at FROM webhook_deliveries WHERE status='dead_letter' ORDER BY updated_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []WebhookDelivery
	for rows.Next() {
		var item WebhookDelivery
		var next, created, updated string
		if err := rows.Scan(&item.ID, &item.EventID, &item.Status, &item.Attempts, &next, &item.LastError, &created, &updated); err != nil {
			return nil, err
		}
		item.NextAttempt, _ = time.Parse(time.RFC3339Nano, next)
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) RetryWebhookDelivery(ctx context.Context, id, actor string) error {
	id = strings.TrimSpace(id)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE webhook_deliveries SET status='retry',attempts=0,next_attempt_at=?,last_error='',updated_at=? WHERE id=? AND status='dead_letter'`, formatTime(time.Now().UTC()), formatTime(time.Now().UTC()), id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	if err := insertAdminAudit(ctx, tx, actor, "webhook.retried", id, nil); err != nil {
		return err
	}
	return tx.Commit()
}
