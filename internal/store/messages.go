package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func InsertTaskMessage(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, message model.TaskMessage) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO task_messages(id,task_id,author,author_run_id,target_run_id,kind,body,reply_to_id,supersedes_id,requires_ack,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		message.ID, message.TaskID, message.Author, nullableText(message.AuthorRunID), nullableText(message.TargetRunID), message.Kind, message.Body,
		nullableText(message.ReplyToID), nullableText(message.SupersedesID), message.RequiresAck, formatTime(message.CreatedAt))
	return err
}

func (s *Store) GetTaskMessage(ctx context.Context, id string) (model.TaskMessage, error) {
	return LoadTaskMessage(ctx, s.db, id)
}

func LoadTaskMessage(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (model.TaskMessage, error) {
	var message model.TaskMessage
	var authorRunID, targetRunID, replyToID, supersedesID sql.NullString
	var createdAt string
	err := q.QueryRowContext(ctx, `SELECT id,task_id,author,author_run_id,target_run_id,kind,body,reply_to_id,supersedes_id,requires_ack,created_at FROM task_messages WHERE id=?`, id).
		Scan(&message.ID, &message.TaskID, &message.Author, &authorRunID, &targetRunID, &message.Kind, &message.Body, &replyToID, &supersedesID, &message.RequiresAck, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.TaskMessage{}, ErrNotFound
	}
	if err != nil {
		return model.TaskMessage{}, err
	}
	message.AuthorRunID, message.TargetRunID = authorRunID.String, targetRunID.String
	message.ReplyToID, message.SupersedesID = replyToID.String, supersedesID.String
	message.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return message, nil
}

func (s *Store) ListTaskMessages(ctx context.Context, taskID, before string, limit int) ([]model.TaskMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `SELECT id,task_id,author,author_run_id,target_run_id,kind,body,reply_to_id,supersedes_id,requires_ack,created_at FROM task_messages WHERE task_id=?`
	args := []any{taskID}
	if before != "" {
		query += ` AND id<?`
		args = append(args, before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	messages, err := scanTaskMessages(rows)
	if err != nil || len(messages) == 0 {
		return messages, err
	}
	return s.attachMessageReceipts(ctx, messages, false)
}

func (s *Store) ListAllTaskMessages(ctx context.Context) ([]model.TaskMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,author,author_run_id,target_run_id,kind,body,reply_to_id,supersedes_id,requires_ack,created_at FROM task_messages ORDER BY task_id,id`)
	if err != nil {
		return nil, err
	}
	messages, err := scanTaskMessages(rows)
	if err != nil || len(messages) == 0 {
		return messages, err
	}
	return s.attachMessageReceipts(ctx, messages, true)
}

func (s *Store) attachMessageReceipts(ctx context.Context, messages []model.TaskMessage, all bool) ([]model.TaskMessage, error) {
	byID := make(map[string]*model.TaskMessage, len(messages))
	for index := range messages {
		byID[messages[index].ID] = &messages[index]
	}
	query := `SELECT message_id,run_id,observer,observed_at,acknowledged_at FROM message_receipts`
	var args []any
	if !all {
		query += ` WHERE message_id IN (` + placeholders(len(messages)) + `)`
		args = messageIDs(messages)
	}
	query += ` ORDER BY observed_at`
	receiptRows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = receiptRows.Close() }()
	for receiptRows.Next() {
		var receipt model.MessageReceipt
		var observed string
		var acknowledged sql.NullString
		if err := receiptRows.Scan(&receipt.MessageID, &receipt.RunID, &receipt.Observer, &observed, &acknowledged); err != nil {
			return nil, err
		}
		receipt.ObservedAt, _ = time.Parse(time.RFC3339Nano, observed)
		if acknowledged.Valid {
			value, _ := time.Parse(time.RFC3339Nano, acknowledged.String)
			receipt.AcknowledgedAt = &value
		}
		if message := byID[receipt.MessageID]; message != nil {
			message.Receipts = append(message.Receipts, receipt)
		}
	}
	return messages, receiptRows.Err()
}

func (s *Store) ListPendingTaskMessages(ctx context.Context, taskID, runID, observer string, limit int) ([]model.TaskMessage, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT message.id,message.task_id,message.author,message.author_run_id,message.target_run_id,message.kind,message.body,message.reply_to_id,message.supersedes_id,message.requires_ack,message.created_at
		FROM task_messages message
		LEFT JOIN message_receipts receipt ON receipt.message_id=message.id AND receipt.run_id=?
		WHERE message.task_id=? AND message.author<>? AND (message.target_run_id IS NULL OR message.target_run_id=?)
		AND ((message.requires_ack=TRUE AND receipt.acknowledged_at IS NULL) OR (message.requires_ack=FALSE AND receipt.observed_at IS NULL))
		ORDER BY message.id LIMIT ?`, runID, taskID, observer, runID, limit)
	if err != nil {
		return nil, err
	}
	return scanTaskMessages(rows)
}

func scanTaskMessages(rows *sql.Rows) ([]model.TaskMessage, error) {
	defer func() { _ = rows.Close() }()
	messages := []model.TaskMessage{}
	for rows.Next() {
		var message model.TaskMessage
		var authorRunID, targetRunID, replyToID, supersedesID sql.NullString
		var createdAt string
		if err := rows.Scan(&message.ID, &message.TaskID, &message.Author, &authorRunID, &targetRunID, &message.Kind, &message.Body, &replyToID, &supersedesID, &message.RequiresAck, &createdAt); err != nil {
			return nil, err
		}
		message.AuthorRunID, message.TargetRunID = authorRunID.String, targetRunID.String
		message.ReplyToID, message.SupersedesID = replyToID.String, supersedesID.String
		message.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func RecordMessageReceipt(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, messageID, runID, observer string, acknowledged bool, now time.Time) error {
	stamp := formatTime(now)
	if _, err := tx.ExecContext(ctx, `INSERT INTO message_receipts(message_id,run_id,observer,observed_at,acknowledged_at) VALUES(?,?,?,?,?) ON CONFLICT(message_id,run_id) DO NOTHING`, messageID, runID, observer, stamp, nullableTime(acknowledged, stamp)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE message_receipts SET observer=?,observed_at=COALESCE(observed_at,?),acknowledged_at=CASE WHEN ? THEN COALESCE(acknowledged_at,?) ELSE acknowledged_at END WHERE message_id=? AND run_id=?`, observer, stamp, acknowledged, stamp, messageID, runID)
	return err
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(include bool, value string) any {
	if !include {
		return nil
	}
	return value
}

func placeholders(count int) string {
	result := "?"
	for index := 1; index < count; index++ {
		result += ",?"
	}
	return result
}

func messageIDs(messages []model.TaskMessage) []any {
	ids := make([]any, len(messages))
	for index := range messages {
		ids[index] = messages[index].ID
	}
	return ids
}
