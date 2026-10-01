package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

const escalationColumns = `id,task_id,run_id,question_message_id,answer_message_id,blocking,options_json,recommendation,selected_option,status,resolved_by,created_at,resolved_at,answerers_json,expires_at,delegated_by`

func InsertTaskEscalation(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, escalation model.TaskEscalation) error {
	options, err := json.Marshal(escalation.Options)
	if err != nil {
		return err
	}
	answerers := escalation.Answerers
	if answerers == nil {
		answerers = []string{}
	}
	encodedAnswerers, err := json.Marshal(answerers)
	if err != nil {
		return err
	}
	var expiresAt any
	if escalation.ExpiresAt != nil {
		expiresAt = formatTime(*escalation.ExpiresAt)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_escalations(`+escalationColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		escalation.ID, escalation.TaskID, escalation.RunID, escalation.QuestionMessageID, nullableText(escalation.AnswerMessageID), escalation.Blocking,
		string(options), escalation.Recommendation, escalation.SelectedOption, escalation.Status, escalation.ResolvedBy, formatTime(escalation.CreatedAt), nil,
		string(encodedAnswerers), expiresAt, escalation.DelegatedBy)
	return err
}

func (s *Store) GetTaskEscalation(ctx context.Context, id string) (model.TaskEscalation, error) {
	return LoadTaskEscalation(ctx, s.db, id)
}

func LoadTaskEscalation(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (model.TaskEscalation, error) {
	return scanTaskEscalation(q.QueryRowContext(ctx, `SELECT `+escalationColumns+` FROM task_escalations WHERE id=?`, id))
}

func (s *Store) ListTaskEscalations(ctx context.Context, taskID string) ([]model.TaskEscalation, error) {
	return s.listEscalations(ctx, `SELECT `+escalationColumns+` FROM task_escalations WHERE task_id=? ORDER BY id DESC`, taskID)
}

// ListOpenTaskEscalations returns unanswered escalations; expired ones
// report the derived expired status.
func (s *Store) ListOpenTaskEscalations(ctx context.Context) ([]model.TaskEscalation, error) {
	return s.listEscalations(ctx, `SELECT `+escalationColumns+` FROM task_escalations WHERE status=? ORDER BY task_id,id`, model.EscalationOpen)
}

func (s *Store) ListAllTaskEscalations(ctx context.Context) ([]model.TaskEscalation, error) {
	return s.listEscalations(ctx, `SELECT `+escalationColumns+` FROM task_escalations ORDER BY task_id,id`)
}

func (s *Store) listEscalations(ctx context.Context, query string, args ...any) ([]model.TaskEscalation, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []model.TaskEscalation{}
	for rows.Next() {
		item, err := scanEscalationValues(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

type rowScanner interface {
	Scan(...any) error
}

func scanTaskEscalation(row rowScanner) (model.TaskEscalation, error) {
	item, err := scanEscalationValues(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.TaskEscalation{}, ErrNotFound
	}
	return item, err
}

func scanEscalationValues(row rowScanner) (model.TaskEscalation, error) {
	var item model.TaskEscalation
	var answerMessageID, resolvedAt, expiresAt sql.NullString
	var options, answerers, created string
	if err := row.Scan(&item.ID, &item.TaskID, &item.RunID, &item.QuestionMessageID, &answerMessageID, &item.Blocking, &options,
		&item.Recommendation, &item.SelectedOption, &item.Status, &item.ResolvedBy, &created, &resolvedAt, &answerers, &expiresAt, &item.DelegatedBy); err != nil {
		return model.TaskEscalation{}, err
	}
	item.AnswerMessageID = answerMessageID.String
	if err := json.Unmarshal([]byte(options), &item.Options); err != nil {
		return model.TaskEscalation{}, err
	}
	if err := json.Unmarshal([]byte(answerers), &item.Answerers); err != nil {
		return model.TaskEscalation{}, err
	}
	if len(item.Answerers) == 0 {
		item.Answerers = nil
	}
	if expiresAt.Valid {
		value, err := time.Parse(time.RFC3339Nano, expiresAt.String)
		if err != nil {
			return model.TaskEscalation{}, err
		}
		item.ExpiresAt = &value
		if item.Status == model.EscalationOpen && !time.Now().Before(value) {
			item.Status = model.EscalationExpired
		}
	}
	item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if resolvedAt.Valid {
		value, _ := time.Parse(time.RFC3339Nano, resolvedAt.String)
		item.ResolvedAt = &value
	}
	return item, nil
}

func ResolveTaskEscalation(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, escalationID, answerMessageID, selectedOption, resolvedBy, delegatedBy string, resolvedAt time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE task_escalations SET answer_message_id=?,selected_option=?,status=?,resolved_by=?,delegated_by=?,resolved_at=? WHERE id=? AND status=?`,
		answerMessageID, selectedOption, model.EscalationAnswered, resolvedBy, delegatedBy, formatTime(resolvedAt), escalationID, model.EscalationOpen)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	return nil
}
