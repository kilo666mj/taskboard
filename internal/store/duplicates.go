package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

// loadDuplicateLinks fills the task's duplicate_of link and the tasks that
// were cancelled as duplicates of it.
func (s *Store) loadDuplicateLinks(ctx context.Context, task *model.Task) error {
	var keptID, keptTitle, keptStatus sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT task.duplicate_of,kept.title,kept.status FROM tasks task LEFT JOIN tasks kept ON kept.id=task.duplicate_of WHERE task.id=?`, task.ID).Scan(&keptID, &keptTitle, &keptStatus)
	if err != nil {
		return err
	}
	task.DuplicateOf = nil
	if keptID.Valid && keptID.String != "" {
		task.DuplicateOf = &model.TaskLink{TaskID: keptID.String, Title: keptTitle.String, Status: model.TaskStatus(keptStatus.String)}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,status FROM tasks WHERE duplicate_of=? ORDER BY created_at,id`, task.ID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	task.Duplicates = nil
	for rows.Next() {
		var link model.TaskLink
		if err := rows.Scan(&link.TaskID, &link.Title, &link.Status); err != nil {
			return err
		}
		task.Duplicates = append(task.Duplicates, link)
	}
	return rows.Err()
}

// DuplicateOf returns the ID of the task this task was cancelled as a
// duplicate of, or an empty string.
func DuplicateOf(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, taskID string) (string, error) {
	var keptID sql.NullString
	err := q.QueryRowContext(ctx, `SELECT duplicate_of FROM tasks WHERE id=?`, taskID).Scan(&keptID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return keptID.String, err
}

// ListOpenTaskHeads returns the identifying fields of recently updated open
// tasks that are not themselves duplicates, for similarity checks. It does not
// load checklists or runs.
func (s *Store) ListOpenTaskHeads(ctx context.Context, limit int) ([]model.Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,visibility,created_by,owner,project,repository,status,version,updated_at FROM tasks WHERE status IN (?,?,?,?,?) AND duplicate_of IS NULL ORDER BY updated_at DESC,id LIMIT ?`,
		model.TaskQueued, model.TaskActive, model.TaskWaiting, model.TaskBlocked, model.TaskStale, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var tasks []model.Task
	for rows.Next() {
		var task model.Task
		var updated string
		if err := rows.Scan(&task.ID, &task.Title, &task.Visibility, &task.CreatedBy, &task.Owner, &task.Project, &task.Repository, &task.Status, &task.Version, &updated); err != nil {
			return nil, err
		}
		task.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// TaskAccess returns the fields that decide who may see a task: its ID,
// visibility, creator and owner.
func (s *Store) TaskAccess(ctx context.Context, id string) (model.Task, error) {
	var task model.Task
	err := s.db.QueryRowContext(ctx, `SELECT id,visibility,created_by,owner FROM tasks WHERE id=?`, id).Scan(&task.ID, &task.Visibility, &task.CreatedBy, &task.Owner)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Task{}, ErrNotFound
	}
	return task, err
}
