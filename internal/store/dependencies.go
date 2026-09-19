package store

import (
	"context"
	"github.com/kilo666mj/taskboard/internal/model"
	"time"
)

func (s *Store) ListTaskDependencies(ctx context.Context, taskID string) ([]model.TaskDependency, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT dependency.task_id,dependency.blocked_by_task_id,blocker.title,blocker.status,dependency.created_by,dependency.created_at FROM task_dependencies dependency JOIN tasks blocker ON blocker.id=dependency.blocked_by_task_id WHERE dependency.task_id=? ORDER BY dependency.created_at,dependency.blocked_by_task_id`, taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []model.TaskDependency{}
	for rows.Next() {
		var item model.TaskDependency
		var created string
		if err := rows.Scan(&item.TaskID, &item.BlockedByTaskID, &item.BlockedByTitle, &item.BlockedByStatus, &item.CreatedBy, &created); err != nil {
			return nil, err
		}
		item.Satisfied = item.BlockedByStatus == model.TaskDone
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		items = append(items, item)
	}
	return items, rows.Err()
}
