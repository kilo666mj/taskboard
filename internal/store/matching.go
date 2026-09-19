package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

func (s *Store) ListTaskRequirements(ctx context.Context, taskID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT requirement FROM task_requirements WHERE task_id=? ORDER BY requirement`, taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []string{}
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func UpsertWorkerAdvertisement(ctx context.Context, tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, item model.WorkerAdvertisement) error {
	encoded, _ := json.Marshal(item.Capabilities)
	_, err := tx.ExecContext(ctx, `INSERT INTO worker_advertisements(principal,capabilities_json,capacity,updated_at,expires_at) VALUES(?,?,?,?,?) ON CONFLICT(principal) DO UPDATE SET capabilities_json=excluded.capabilities_json,capacity=excluded.capacity,updated_at=excluded.updated_at,expires_at=excluded.expires_at`, item.Principal, string(encoded), item.Capacity, formatTime(item.UpdatedAt), formatTime(item.ExpiresAt))
	return err
}
func (s *Store) GetWorkerAdvertisement(ctx context.Context, principal string) (model.WorkerAdvertisement, error) {
	var item model.WorkerAdvertisement
	var encoded, updated, expires string
	err := s.db.QueryRowContext(ctx, `SELECT principal,capabilities_json,capacity,updated_at,expires_at FROM worker_advertisements WHERE principal=?`, principal).Scan(&item.Principal, &encoded, &item.Capacity, &updated, &expires)
	if err == sql.ErrNoRows {
		return item, ErrNotFound
	}
	if err != nil {
		return item, err
	}
	_ = json.Unmarshal([]byte(encoded), &item.Capabilities)
	item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	item.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	if time.Now().UTC().After(item.ExpiresAt) {
		return model.WorkerAdvertisement{}, ErrNotFound
	}
	return item, nil
}
