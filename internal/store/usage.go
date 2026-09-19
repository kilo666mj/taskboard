package store

import (
	"context"
	"database/sql"
	"github.com/kilo666mj/taskboard/internal/model"
	"time"
)

func InsertUsageRecord(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, item model.UsageRecord) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO usage_records(id,task_id,run_id,provider,model,input_tokens,output_tokens,estimated_cost_micros,recorded_by,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, item.ID, item.TaskID, item.RunID, item.Provider, item.Model, item.InputTokens, item.OutputTokens, item.EstimatedCostMicros, item.RecordedBy, formatTime(item.CreatedAt))
	return err
}
func (s *Store) GetUsageRecord(ctx context.Context, id string) (model.UsageRecord, error) {
	var item model.UsageRecord
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT id,task_id,run_id,provider,model,input_tokens,output_tokens,estimated_cost_micros,recorded_by,created_at FROM usage_records WHERE id=?`, id).Scan(&item.ID, &item.TaskID, &item.RunID, &item.Provider, &item.Model, &item.InputTokens, &item.OutputTokens, &item.EstimatedCostMicros, &item.RecordedBy, &created)
	if err == sql.ErrNoRows {
		return item, ErrNotFound
	}
	if err != nil {
		return item, err
	}
	item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return item, nil
}
func (s *Store) ListAllUsageRecords(ctx context.Context) ([]model.UsageRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,run_id,provider,model,input_tokens,output_tokens,estimated_cost_micros,recorded_by,created_at FROM usage_records ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []model.UsageRecord{}
	for rows.Next() {
		var item model.UsageRecord
		var created string
		if err := rows.Scan(&item.ID, &item.TaskID, &item.RunID, &item.Provider, &item.Model, &item.InputTokens, &item.OutputTokens, &item.EstimatedCostMicros, &item.RecordedBy, &created); err != nil {
			return nil, err
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		items = append(items, item)
	}
	return items, rows.Err()
}
