package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

const discussionColumns = `id,task_id,controller,status,agent_status,requested_by,end_reason,created_at,started_at,last_activity_at,ended_at`

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func InsertDiscussion(ctx context.Context, tx execer, item model.TaskDiscussion) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO task_discussions(`+discussionColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		item.ID, item.TaskID, item.Controller, item.Status, item.AgentStatus, item.RequestedBy, item.EndReason,
		formatTime(item.CreatedAt), optionalTime(item.StartedAt), formatTime(item.LastActivityAt), optionalTime(item.EndedAt))
	return err
}

// UpdateDiscussion writes the mutable fields of item.
func UpdateDiscussion(ctx context.Context, tx execer, item model.TaskDiscussion) error {
	result, err := tx.ExecContext(ctx, `UPDATE task_discussions SET status=?,agent_status=?,end_reason=?,started_at=?,last_activity_at=?,ended_at=? WHERE id=?`,
		item.Status, item.AgentStatus, item.EndReason, optionalTime(item.StartedAt), formatTime(item.LastActivityAt), optionalTime(item.EndedAt), item.ID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

func InsertDiscussionMessage(ctx context.Context, tx execer, item model.DiscussionMessage) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO task_discussion_messages(id,discussion_id,task_id,author,role,body,created_at) VALUES(?,?,?,?,?,?,?)`,
		item.ID, item.DiscussionID, item.TaskID, item.Author, item.Role, item.Body, formatTime(item.CreatedAt))
	return err
}

func LoadDiscussion(ctx context.Context, q rowQuerier, id string) (model.TaskDiscussion, error) {
	item, err := scanDiscussion(q.QueryRowContext(ctx, `SELECT `+discussionColumns+` FROM task_discussions WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.TaskDiscussion{}, ErrNotFound
	}
	return item, err
}

func (s *Store) GetDiscussion(ctx context.Context, id string) (model.TaskDiscussion, error) {
	return LoadDiscussion(ctx, s.db, id)
}

func (s *Store) ListTaskDiscussions(ctx context.Context, taskID string, limit int) ([]model.TaskDiscussion, error) {
	return s.listDiscussions(ctx, `SELECT `+discussionColumns+` FROM task_discussions WHERE task_id=? ORDER BY id DESC LIMIT ?`, taskID, limit)
}

// ListControllerDiscussions returns requested and active discussions for a controller.
func (s *Store) ListControllerDiscussions(ctx context.Context, controller string, limit int) ([]model.TaskDiscussion, error) {
	return s.listDiscussions(ctx, `SELECT `+discussionColumns+` FROM task_discussions WHERE controller=? AND status IN (?,?) ORDER BY id LIMIT ?`,
		controller, model.DiscussionRequested, model.DiscussionActive, limit)
}

// ListOpenDiscussions returns every requested or active discussion.
func (s *Store) ListOpenDiscussions(ctx context.Context) ([]model.TaskDiscussion, error) {
	return s.listDiscussions(ctx, `SELECT `+discussionColumns+` FROM task_discussions WHERE status IN (?,?) ORDER BY id`, model.DiscussionRequested, model.DiscussionActive)
}

func (s *Store) listDiscussions(ctx context.Context, query string, args ...any) ([]model.TaskDiscussion, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []model.TaskDiscussion{}
	for rows.Next() {
		item, err := scanDiscussion(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// ListDiscussionMessages returns messages after the given message ID, oldest first.
func (s *Store) ListDiscussionMessages(ctx context.Context, discussionID, after string, limit int) ([]model.DiscussionMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,discussion_id,task_id,author,role,body,created_at FROM task_discussion_messages WHERE discussion_id=? AND id>? ORDER BY id LIMIT ?`, discussionID, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []model.DiscussionMessage{}
	for rows.Next() {
		var item model.DiscussionMessage
		var created string
		if err := rows.Scan(&item.ID, &item.DiscussionID, &item.TaskID, &item.Author, &item.Role, &item.Body, &created); err != nil {
			return nil, err
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		result = append(result, item)
	}
	return result, rows.Err()
}

func scanDiscussion(row rowScanner) (model.TaskDiscussion, error) {
	var item model.TaskDiscussion
	var created, lastActivity string
	var started, ended sql.NullString
	if err := row.Scan(&item.ID, &item.TaskID, &item.Controller, &item.Status, &item.AgentStatus, &item.RequestedBy, &item.EndReason,
		&created, &started, &lastActivity, &ended); err != nil {
		return model.TaskDiscussion{}, err
	}
	item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	item.LastActivityAt, _ = time.Parse(time.RFC3339Nano, lastActivity)
	item.StartedAt = parseNullableTime(started)
	item.EndedAt = parseNullableTime(ended)
	return item, nil
}

func optionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func parseNullableTime(value sql.NullString) *time.Time {
	if !value.Valid {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &parsed
}
