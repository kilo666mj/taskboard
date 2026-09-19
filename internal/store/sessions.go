package store

import (
	"context"
	"database/sql"
	"github.com/kilo666mj/taskboard/internal/model"
	"time"
)

func UpsertSessionBridge(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, item model.SessionBridge) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO session_bridges(run_id,task_id,controller,state,label,can_open,can_resume,updated_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET state=excluded.state,label=excluded.label,can_open=excluded.can_open,can_resume=excluded.can_resume,updated_at=excluded.updated_at,expires_at=excluded.expires_at WHERE session_bridges.controller=excluded.controller`, item.RunID, item.TaskID, item.Controller, item.State, item.Label, item.CanOpen, item.CanResume, formatTime(item.UpdatedAt), formatTime(item.ExpiresAt))
	return err
}
func InsertSessionBridgeRequest(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, item model.SessionBridgeRequest) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO session_bridge_requests(id,task_id,run_id,controller,action,status,requested_by,outcome_note,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, item.ID, item.TaskID, item.RunID, item.Controller, item.Action, item.Status, item.RequestedBy, item.OutcomeNote, formatTime(item.CreatedAt), formatTime(item.UpdatedAt))
	return err
}
func (s *Store) ListTaskSessionBridges(ctx context.Context, taskID string) ([]model.SessionBridge, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id,task_id,controller,state,label,can_open,can_resume,updated_at,expires_at FROM session_bridges WHERE task_id=? ORDER BY run_id`, taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []model.SessionBridge{}
	for rows.Next() {
		var item model.SessionBridge
		var updated, expires string
		if err := rows.Scan(&item.RunID, &item.TaskID, &item.Controller, &item.State, &item.Label, &item.CanOpen, &item.CanResume, &updated, &expires); err != nil {
			return nil, err
		}
		item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		item.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
		if time.Now().UTC().After(item.ExpiresAt) {
			item.State = model.SessionBridgeExpired
			item.CanOpen = false
			item.CanResume = false
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (s *Store) GetSessionBridge(ctx context.Context, runID string) (model.SessionBridge, error) {
	var item model.SessionBridge
	var updated, expires string
	err := s.db.QueryRowContext(ctx, `SELECT run_id,task_id,controller,state,label,can_open,can_resume,updated_at,expires_at FROM session_bridges WHERE run_id=?`, runID).Scan(&item.RunID, &item.TaskID, &item.Controller, &item.State, &item.Label, &item.CanOpen, &item.CanResume, &updated, &expires)
	if err == sql.ErrNoRows {
		return item, ErrNotFound
	}
	if err != nil {
		return item, err
	}
	item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	item.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	if time.Now().UTC().After(item.ExpiresAt) {
		item.State = model.SessionBridgeExpired
		item.CanOpen = false
		item.CanResume = false
	}
	return item, nil
}
func (s *Store) GetSessionBridgeRequest(ctx context.Context, id string) (model.SessionBridgeRequest, error) {
	return scanSessionRequest(s.db.QueryRowContext(ctx, `SELECT id,task_id,run_id,controller,action,status,requested_by,outcome_note,created_at,updated_at FROM session_bridge_requests WHERE id=?`, id))
}
func (s *Store) ListSessionBridgeRequests(ctx context.Context, controller string, all bool) ([]model.SessionBridgeRequest, error) {
	query := `SELECT id,task_id,run_id,controller,action,status,requested_by,outcome_note,created_at,updated_at FROM session_bridge_requests WHERE controller=?`
	if !all {
		query += ` AND status IN ('requested','acknowledged')`
	}
	query += ` ORDER BY id`
	rows, err := s.db.QueryContext(ctx, query, controller)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []model.SessionBridgeRequest{}
	for rows.Next() {
		item, err := scanSessionRequest(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type sessionScanner interface{ Scan(...any) error }

func scanSessionRequest(scanner sessionScanner) (model.SessionBridgeRequest, error) {
	var item model.SessionBridgeRequest
	var created, updated string
	if err := scanner.Scan(&item.ID, &item.TaskID, &item.RunID, &item.Controller, &item.Action, &item.Status, &item.RequestedBy, &item.OutcomeNote, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			return item, ErrNotFound
		}
		return item, err
	}
	item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return item, nil
}
