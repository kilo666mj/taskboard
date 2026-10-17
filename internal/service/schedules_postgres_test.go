package service

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/store"
	"github.com/oklog/ulid/v2"
)

// TestPostgresScheduleFiresOnceAcrossReplicas runs two services against one
// PostgreSQL schema, as two replicas would, and sweeps a due schedule from
// both at once.
func TestPostgresScheduleFiresOnceAcrossReplicas(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TASKBOARD_TEST_POSTGRES_URL"))
	if databaseURL == "" {
		t.Skip("TASKBOARD_TEST_POSTGRES_URL is not set")
	}
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "taskboard_test_" + strings.ToLower(ulid.Make().String())
	if _, err := admin.ExecContext(t.Context(), `CREATE SCHEMA `+schema); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	var replicas [2]*Service
	var stores []*store.Store
	t.Cleanup(func() {
		for _, database := range stores {
			if err := database.Close(); err != nil {
				t.Errorf("close PostgreSQL store: %v", err)
			}
		}
		if _, err := admin.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("drop PostgreSQL test schema: %v", err)
		}
		if err := admin.Close(); err != nil {
			t.Errorf("close PostgreSQL admin connection: %v", err)
		}
	})
	clock := &testClock{now: mustTime("2026-10-17T06:00:00Z")}
	for index := range replicas {
		database, err := store.OpenURL(t.Context(), parsed.String())
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, database)
		database.SetClock(clock.Now)
		replicas[index] = New(database, time.Minute)
	}
	schedule, err := replicas[0].CreateScheduleFor(t.Context(), dailySchedule("audit"), HumanPrincipal("person"))
	if err != nil {
		t.Fatal(err)
	}
	clock.Set("2026-10-17T07:00:00Z")
	var fired [2]int
	var errs [2]error
	var wait sync.WaitGroup
	for index, replica := range replicas {
		wait.Add(1)
		go func() {
			defer wait.Done()
			fired[index], errs[index] = replica.RunDueSchedules(t.Context())
		}()
	}
	wait.Wait()
	for index, err := range errs {
		// A replica that loses the race may see a serialization error; the
		// schedule must still fire exactly once.
		if err != nil && !strings.Contains(err.Error(), "could not serialize") {
			t.Fatalf("replica %d sweep: %v", index, err)
		}
	}
	if fired[0]+fired[1] != 1 {
		t.Fatalf("fired = %v, want exactly one replica to fire", fired)
	}
	var count int
	if err := stores[0].DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM tasks WHERE schedule_id=?`, schedule.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("occurrences = %d, want 1", count)
	}
}
