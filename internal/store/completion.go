package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func InsertCompletionRequirement(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, item model.CompletionRequirement) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO completion_requirements(id,task_id,kind,label,required,status,created_by,verified_by,waiver_reason,created_at,updated_at,verified_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.TaskID, item.Kind, item.Label, item.Required, item.Status, item.CreatedBy, item.VerifiedBy, item.WaiverReason, formatTime(item.CreatedAt), formatTime(item.UpdatedAt), nil)
	return err
}

func InsertCompletionEvidence(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, item model.CompletionEvidence) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO completion_evidence(id,requirement_id,task_id,run_id,reference_id,note,status,submitted_by,reviewed_by,review_note,submitted_at,reviewed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.RequirementID, item.TaskID, nullableText(item.RunID), nullableText(item.ReferenceID), item.Note, item.Status, item.SubmittedBy, item.ReviewedBy, item.ReviewNote, formatTime(item.SubmittedAt), nil)
	return err
}

func (s *Store) GetCompletionRequirement(ctx context.Context, id string) (model.CompletionRequirement, error) {
	item, err := scanCompletionRequirement(s.db.QueryRowContext(ctx, `SELECT id,task_id,kind,label,required,status,created_by,verified_by,waiver_reason,created_at,updated_at,verified_at FROM completion_requirements WHERE id=?`, id))
	if err != nil {
		return item, err
	}
	item.Evidence, err = s.listCompletionEvidence(ctx, item.ID)
	return item, err
}
func (s *Store) ListCompletionRequirements(ctx context.Context, taskID string) ([]model.CompletionRequirement, error) {
	return s.listCompletionRequirements(ctx, `SELECT id,task_id,kind,label,required,status,created_by,verified_by,waiver_reason,created_at,updated_at,verified_at FROM completion_requirements WHERE task_id=? ORDER BY id`, taskID)
}
func (s *Store) ListAllCompletionRequirements(ctx context.Context) ([]model.CompletionRequirement, error) {
	return s.listCompletionRequirements(ctx, `SELECT id,task_id,kind,label,required,status,created_by,verified_by,waiver_reason,created_at,updated_at,verified_at FROM completion_requirements ORDER BY task_id,id`)
}
func (s *Store) listCompletionRequirements(ctx context.Context, query string, args ...any) ([]model.CompletionRequirement, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []model.CompletionRequirement{}
	for rows.Next() {
		item, scanErr := scanCompletionRequirement(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		item.Evidence, scanErr = s.listCompletionEvidence(ctx, item.ID)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type completionScanner interface{ Scan(...any) error }

func scanCompletionRequirement(scanner completionScanner) (model.CompletionRequirement, error) {
	var item model.CompletionRequirement
	var created, updated string
	var verified sql.NullString
	if err := scanner.Scan(&item.ID, &item.TaskID, &item.Kind, &item.Label, &item.Required, &item.Status, &item.CreatedBy, &item.VerifiedBy, &item.WaiverReason, &created, &updated, &verified); err != nil {
		if err == sql.ErrNoRows {
			return item, ErrNotFound
		}
		return item, err
	}
	item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if verified.Valid {
		value, _ := time.Parse(time.RFC3339Nano, verified.String)
		item.VerifiedAt = &value
	}
	return item, nil
}
func (s *Store) listCompletionEvidence(ctx context.Context, requirementID string) ([]model.CompletionEvidence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,requirement_id,task_id,run_id,reference_id,note,status,submitted_by,reviewed_by,review_note,submitted_at,reviewed_at FROM completion_evidence WHERE requirement_id=? ORDER BY id`, requirementID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []model.CompletionEvidence{}
	for rows.Next() {
		var item model.CompletionEvidence
		var runID, referenceID, reviewedAt sql.NullString
		var submitted string
		if err := rows.Scan(&item.ID, &item.RequirementID, &item.TaskID, &runID, &referenceID, &item.Note, &item.Status, &item.SubmittedBy, &item.ReviewedBy, &item.ReviewNote, &submitted, &reviewedAt); err != nil {
			return nil, err
		}
		item.RunID = runID.String
		item.ReferenceID = referenceID.String
		item.SubmittedAt, _ = time.Parse(time.RFC3339Nano, submitted)
		if reviewedAt.Valid {
			value, _ := time.Parse(time.RFC3339Nano, reviewedAt.String)
			item.ReviewedAt = &value
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
