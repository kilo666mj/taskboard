package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kilo666mj/taskboard/internal/model"
)

const (
	eventNotificationChannel = "taskboard_events"
	eventCatchUpBatchSize    = 256
)

type EventCursor struct {
	CreatedAt time.Time
	ID        string
}

// RunEventFanout listens for bounded event-ID notifications and catches up from
// the durable events table after startup and every reconnect. It blocks until
// ctx is cancelled. State is called after catch-up succeeds and whenever the
// listener becomes unhealthy.
func (s *Store) RunEventFanout(ctx context.Context, state func(bool, error), deliver func(model.Event)) {
	if s.Dialect() != DialectPostgres || s.databaseURL == "" {
		return
	}
	cursor, err := s.latestEventCursor(ctx)
	for err != nil && ctx.Err() == nil {
		state(false, fmt.Errorf("initialize event fan-out cursor: %w", err))
		if !waitForRetry(ctx, time.Second) {
			return
		}
		cursor, err = s.latestEventCursor(ctx)
	}
	backoff := 250 * time.Millisecond
	for ctx.Err() == nil {
		connectionConfig, configErr := pgx.ParseConfig(s.databaseURL)
		if configErr != nil {
			state(false, fmt.Errorf("parse PostgreSQL event fan-out configuration: %w", configErr))
			return
		}
		if connectionConfig.RuntimeParams == nil {
			connectionConfig.RuntimeParams = make(map[string]string)
		}
		connectionConfig.RuntimeParams["application_name"] = "taskboard-event-fanout"
		connection, err := pgx.ConnectConfig(ctx, connectionConfig)
		if err == nil {
			_, err = connection.Exec(ctx, "LISTEN "+eventNotificationChannel)
		}
		if err == nil {
			err = catchUpEvents(ctx, connection, &cursor, deliver)
		}
		if err == nil {
			state(true, nil)
			backoff = 250 * time.Millisecond
			for ctx.Err() == nil {
				if _, err = connection.WaitForNotification(ctx); err != nil {
					break
				}
				if err = catchUpEvents(ctx, connection, &cursor, deliver); err != nil {
					break
				}
			}
		}
		if connection != nil {
			_ = connection.Close(context.Background())
		}
		if ctx.Err() != nil {
			return
		}
		state(false, fmt.Errorf("PostgreSQL event fan-out disconnected: %w", err))
		if !waitForRetry(ctx, backoff) {
			return
		}
		if backoff < 10*time.Second {
			backoff *= 2
			if backoff > 10*time.Second {
				backoff = 10 * time.Second
			}
		}
	}
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Store) latestEventCursor(ctx context.Context) (EventCursor, error) {
	var cursor EventCursor
	var createdAt string
	err := s.db.QueryRowContext(ctx, `SELECT id,created_at FROM events ORDER BY created_at::timestamptz DESC,id DESC LIMIT 1`).Scan(&cursor.ID, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return cursor, nil
	}
	if err != nil {
		return cursor, err
	}
	cursor.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	return cursor, err
}

func catchUpEvents(ctx context.Context, connection *pgx.Conn, cursor *EventCursor, deliver func(model.Event)) error {
	for {
		rows, err := connection.Query(ctx, `SELECT id,task_id,run_id,kind,actor,message,payload,created_at
			FROM events
			WHERE created_at::timestamptz > $1 OR (created_at::timestamptz = $1 AND id > $2)
			ORDER BY created_at::timestamptz,id LIMIT $3`, cursor.CreatedAt, cursor.ID, eventCatchUpBatchSize)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			event, next, err := scanEvent(rows)
			if err != nil {
				rows.Close()
				return err
			}
			deliver(event)
			*cursor = next
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if count < eventCatchUpBatchSize {
			return nil
		}
	}
}

type eventScanner interface {
	Scan(dest ...any) error
}

func scanEvent(row eventScanner) (model.Event, EventCursor, error) {
	var event model.Event
	var payload, createdAt string
	if err := row.Scan(&event.ID, &event.TaskID, &event.RunID, &event.Kind, &event.Actor, &event.Message, &payload, &createdAt); err != nil {
		return event, EventCursor{}, err
	}
	if err := json.Unmarshal([]byte(payload), &event.Payload); err != nil {
		return event, EventCursor{}, fmt.Errorf("decode event %s payload: %w", event.ID, err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return event, EventCursor{}, fmt.Errorf("parse event %s timestamp: %w", event.ID, err)
	}
	event.CreatedAt = parsed
	return event, EventCursor{CreatedAt: parsed, ID: event.ID}, nil
}
