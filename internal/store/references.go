package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func InsertTaskReference(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, reference model.TaskReference) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO task_references(id,task_id,run_id,kind,label,locator,url,created_by,provenance,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		reference.ID, reference.TaskID, nullableText(reference.RunID), reference.Kind, reference.Label, reference.Locator, reference.URL, reference.CreatedBy, reference.Provenance, formatTime(reference.CreatedAt))
	return err
}

func (s *Store) GetTaskReference(ctx context.Context, id string) (model.TaskReference, error) {
	var item model.TaskReference
	var runID sql.NullString
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id,task_id,run_id,kind,label,locator,url,created_by,provenance,created_at FROM task_references WHERE id=?`, id).
		Scan(&item.ID, &item.TaskID, &runID, &item.Kind, &item.Label, &item.Locator, &item.URL, &item.CreatedBy, &item.Provenance, &created)
	if err == sql.ErrNoRows {
		return model.TaskReference{}, ErrNotFound
	}
	if err != nil {
		return model.TaskReference{}, err
	}
	item.RunID = runID.String
	item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return item, nil
}

func (s *Store) ListTaskReferences(ctx context.Context, taskID string) ([]model.TaskReference, error) {
	return s.listTaskReferences(ctx, `SELECT id,task_id,run_id,kind,label,locator,url,created_by,provenance,created_at FROM task_references WHERE task_id=? ORDER BY id`, taskID)
}

func (s *Store) ListAllTaskReferences(ctx context.Context) ([]model.TaskReference, error) {
	return s.listTaskReferences(ctx, `SELECT id,task_id,run_id,kind,label,locator,url,created_by,provenance,created_at FROM task_references ORDER BY task_id,id`)
}

func (s *Store) listTaskReferences(ctx context.Context, query string, args ...any) ([]model.TaskReference, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []model.TaskReference{}
	for rows.Next() {
		var item model.TaskReference
		var runID sql.NullString
		var created string
		if err := rows.Scan(&item.ID, &item.TaskID, &runID, &item.Kind, &item.Label, &item.Locator, &item.URL, &item.CreatedBy, &item.Provenance, &created); err != nil {
			return nil, err
		}
		item.RunID = runID.String
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		items = append(items, item)
	}
	return items, rows.Err()
}
