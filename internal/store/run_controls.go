package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

const runControlColumns = `id,task_id,target_run_id,target_agent,kind,status,requested_by,reason,outcome_note,task_version,created_at,updated_at,expires_at,acknowledged_at,decided_at,completed_at`

func InsertRunControl(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, item model.RunControlRequest) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO run_control_requests(`+runControlColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		item.ID, item.TaskID, item.TargetRunID, item.TargetAgent, item.Kind, item.Status, item.RequestedBy, item.Reason, item.OutcomeNote, item.TaskVersion,
		formatTime(item.CreatedAt), formatTime(item.UpdatedAt), formatTime(item.ExpiresAt), nil, nil, nil)
	return err
}

func (s *Store) GetRunControl(ctx context.Context, id string) (model.RunControlRequest, error) {
	return LoadRunControl(ctx, s.db, id)
}

func LoadRunControl(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (model.RunControlRequest, error) {
	item, err := scanRunControl(q.QueryRowContext(ctx, `SELECT `+runControlColumns+` FROM run_control_requests WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.RunControlRequest{}, ErrNotFound
	}
	return item, err
}

func (s *Store) ListTaskRunControls(ctx context.Context, taskID string) ([]model.RunControlRequest, error) {
	return s.listRunControls(ctx, `SELECT `+runControlColumns+` FROM run_control_requests WHERE task_id=? ORDER BY id DESC`, taskID)
}

func (s *Store) ListAgentRunControls(ctx context.Context, agent string, limit int) ([]model.RunControlRequest, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	return s.listRunControls(ctx, `SELECT `+runControlColumns+` FROM run_control_requests WHERE target_agent=? AND status IN (?,?,?) ORDER BY id LIMIT ?`, agent, model.RunControlRequested, model.RunControlAcknowledged, model.RunControlAccepted, limit)
}

func (s *Store) ListAllRunControls(ctx context.Context) ([]model.RunControlRequest, error) {
	return s.listRunControls(ctx, `SELECT `+runControlColumns+` FROM run_control_requests ORDER BY task_id,id`)
}

func (s *Store) listRunControls(ctx context.Context, query string, args ...any) ([]model.RunControlRequest, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []model.RunControlRequest{}
	for rows.Next() {
		item, err := scanRunControl(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanRunControl(row rowScanner) (model.RunControlRequest, error) {
	var item model.RunControlRequest
	var created, updated, expires string
	var acknowledged, decided, completed sql.NullString
	if err := row.Scan(&item.ID, &item.TaskID, &item.TargetRunID, &item.TargetAgent, &item.Kind, &item.Status, &item.RequestedBy, &item.Reason, &item.OutcomeNote, &item.TaskVersion,
		&created, &updated, &expires, &acknowledged, &decided, &completed); err != nil {
		return model.RunControlRequest{}, err
	}
	item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	item.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	item.AcknowledgedAt = parsedNullableTime(acknowledged)
	item.DecidedAt = parsedNullableTime(decided)
	item.CompletedAt = parsedNullableTime(completed)
	return item, nil
}

func parsedNullableTime(value sql.NullString) *time.Time {
	if !value.Valid {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &parsed
}
