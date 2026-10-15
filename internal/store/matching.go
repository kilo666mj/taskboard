package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
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
	descriptions, _ := json.Marshal(item.Descriptions)
	if item.Descriptions == nil {
		descriptions = []byte("{}")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO worker_advertisements(principal,capabilities_json,descriptions_json,capacity,updated_at,expires_at) VALUES(?,?,?,?,?,?) ON CONFLICT(principal) DO UPDATE SET capabilities_json=excluded.capabilities_json,descriptions_json=excluded.descriptions_json,capacity=excluded.capacity,updated_at=excluded.updated_at,expires_at=excluded.expires_at`, item.Principal, string(encoded), string(descriptions), item.Capacity, formatTime(item.UpdatedAt), formatTime(item.ExpiresAt))
	return err
}
func (s *Store) GetWorkerAdvertisement(ctx context.Context, principal string) (model.WorkerAdvertisement, error) {
	var item model.WorkerAdvertisement
	var encoded, descriptions, updated, expires string
	err := s.db.QueryRowContext(ctx, `SELECT principal,capabilities_json,descriptions_json,capacity,updated_at,expires_at FROM worker_advertisements WHERE principal=?`, principal).Scan(&item.Principal, &encoded, &descriptions, &item.Capacity, &updated, &expires)
	if err == sql.ErrNoRows {
		return item, ErrNotFound
	}
	if err != nil {
		return item, err
	}
	_ = json.Unmarshal([]byte(encoded), &item.Capabilities)
	_ = json.Unmarshal([]byte(descriptions), &item.Descriptions)
	item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	item.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	if time.Now().UTC().After(item.ExpiresAt) {
		return model.WorkerAdvertisement{}, ErrNotFound
	}
	return item, nil
}

// ListLiveWorkerCapabilities returns the sorted union of capabilities in
// unexpired advertisements, without saying which worker advertised them. When
// workers describe a capability differently, the most recent advertisement's
// non-empty description wins.
func (s *Store) ListLiveWorkerCapabilities(ctx context.Context) ([]model.WorkerCapability, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT capabilities_json,descriptions_json,updated_at,expires_at FROM worker_advertisements`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type advertisement struct {
		capabilities []string
		descriptions map[string]string
		updated      time.Time
	}
	now := time.Now().UTC()
	live := []advertisement{}
	for rows.Next() {
		var encoded, descriptions, updated, expires string
		if err := rows.Scan(&encoded, &descriptions, &updated, &expires); err != nil {
			return nil, err
		}
		// Compare parsed times: RFC3339Nano strings drop trailing zeros, so they do not sort.
		if expiresAt, _ := time.Parse(time.RFC3339Nano, expires); now.After(expiresAt) {
			continue
		}
		item := advertisement{}
		_ = json.Unmarshal([]byte(encoded), &item.capabilities)
		_ = json.Unmarshal([]byte(descriptions), &item.descriptions)
		item.updated, _ = time.Parse(time.RFC3339Nano, updated)
		live = append(live, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(live, func(i, j int) bool { return live[i].updated.After(live[j].updated) })
	index := map[string]int{}
	items := []model.WorkerCapability{}
	for _, item := range live {
		for _, capability := range item.capabilities {
			at, seen := index[capability]
			if !seen {
				at = len(items)
				index[capability] = at
				items = append(items, model.WorkerCapability{Capability: capability})
			}
			if items[at].Description == "" {
				items[at].Description = item.descriptions[capability]
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Capability < items[j].Capability })
	return items, nil
}
