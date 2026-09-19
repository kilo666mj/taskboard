package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func InsertRunHandoff(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, item model.RunHandoff) error {
	commits, _ := json.Marshal(item.Commits)
	prs, _ := json.Marshal(item.PullRequests)
	validation, _ := json.Marshal(item.Validation)
	findings, _ := json.Marshal(item.ReviewFindings)
	_, err := tx.ExecContext(ctx, `INSERT INTO run_handoffs(id,task_id,run_id,kind,last_completed_step,worktree,branch,commits_json,pull_requests_json,validation_json,review_findings_json,blocker,next_action,created_by,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		item.ID, item.TaskID, item.RunID, item.Kind, item.LastCompletedStep, item.Worktree, item.Branch, string(commits), string(prs), string(validation), string(findings), item.Blocker, item.NextAction, item.CreatedBy, formatTime(item.CreatedAt))
	return err
}

func (s *Store) GetRunHandoff(ctx context.Context, id string) (model.RunHandoff, error) {
	return scanRunHandoff(s.db.QueryRowContext(ctx, `SELECT id,task_id,run_id,kind,last_completed_step,worktree,branch,commits_json,pull_requests_json,validation_json,review_findings_json,blocker,next_action,created_by,created_at FROM run_handoffs WHERE id=?`, id))
}

func (s *Store) ListTaskHandoffs(ctx context.Context, taskID string) ([]model.RunHandoff, error) {
	return s.listRunHandoffs(ctx, `SELECT id,task_id,run_id,kind,last_completed_step,worktree,branch,commits_json,pull_requests_json,validation_json,review_findings_json,blocker,next_action,created_by,created_at FROM run_handoffs WHERE task_id=? ORDER BY id DESC`, taskID)
}

func (s *Store) ListAllRunHandoffs(ctx context.Context) ([]model.RunHandoff, error) {
	return s.listRunHandoffs(ctx, `SELECT id,task_id,run_id,kind,last_completed_step,worktree,branch,commits_json,pull_requests_json,validation_json,review_findings_json,blocker,next_action,created_by,created_at FROM run_handoffs ORDER BY task_id,id`)
}

func (s *Store) listRunHandoffs(ctx context.Context, query string, args ...any) ([]model.RunHandoff, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []model.RunHandoff{}
	for rows.Next() {
		item, err := scanRunHandoff(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type handoffScanner interface{ Scan(...any) error }

func scanRunHandoff(scanner handoffScanner) (model.RunHandoff, error) {
	var item model.RunHandoff
	var commits, prs, validation, findings, created string
	if err := scanner.Scan(&item.ID, &item.TaskID, &item.RunID, &item.Kind, &item.LastCompletedStep, &item.Worktree, &item.Branch, &commits, &prs, &validation, &findings, &item.Blocker, &item.NextAction, &item.CreatedBy, &created); err != nil {
		if err == sql.ErrNoRows {
			return model.RunHandoff{}, ErrNotFound
		}
		return model.RunHandoff{}, err
	}
	_ = json.Unmarshal([]byte(commits), &item.Commits)
	_ = json.Unmarshal([]byte(prs), &item.PullRequests)
	_ = json.Unmarshal([]byte(validation), &item.Validation)
	_ = json.Unmarshal([]byte(findings), &item.ReviewFindings)
	item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return item, nil
}
