package push

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/service"
	"github.com/kilo666mj/taskboard/internal/store"
	"github.com/oklog/ulid/v2"
)

func TestPostgresFanoutFeedsRemoteEventStreamAndPush(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("TASKBOARD_TEST_POSTGRES_URL"))
	if baseURL == "" {
		t.Skip("TASKBOARD_TEST_POSTGRES_URL is not set")
	}
	databaseURL, cleanup := postgresFanoutFixture(t, baseURL)
	defer cleanup()
	firstStore, err := store.OpenURL(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = firstStore.Close() }()
	secondStore, err := store.OpenURL(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = secondStore.Close() }()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	firstTasks := service.New(firstStore, time.Minute)
	secondTasks := service.New(secondStore, time.Minute)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	firstTasks.RunEventFanout(ctx, logger)
	secondTasks.RunEventFanout(ctx, logger)
	waitForFanoutReady(t, ctx, firstTasks, secondTasks)

	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	receiverKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := secondStore.SavePushSubscription(ctx, store.PushSubscription{
		Endpoint: "https://web.push.apple.com/fanout-test",
		P256DH:   base64.RawURLEncoding.EncodeToString(receiverKey.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
		OwnerID:  "owner",
	}); err != nil {
		t.Fatal(err)
	}
	remotePush := New(secondStore, secondTasks, publicKey, privateKey, "mailto:test@example.com", logger)
	pushDelivered := make(chan struct{}, 1)
	remotePush.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		select {
		case pushDelivered <- struct{}{}:
		default:
		}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	remotePush.Run(ctx)

	localEvents, cancelLocal := firstTasks.Subscribe()
	defer cancelLocal()
	remoteEvents, cancelRemote := secondTasks.Subscribe()
	defer cancelRemote()
	started, err := firstTasks.Start(ctx, model.StartRequest{Title: "Cross-replica delivery", Checklist: []string{"notify both replicas"}}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskEvent(t, ctx, localEvents, started.Task.ID, "task.started")
	waitForTaskEvent(t, ctx, remoteEvents, started.Task.ID, "task.started")
	controller, err := sql.Open("pgx", baseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.ExecContext(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='taskboard-event-fanout' AND pid<>pg_backend_pid()`); err != nil {
		_ = controller.Close()
		t.Fatal(err)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	waitForFanoutUnready(t, ctx, secondTasks)
	updated, err := firstTasks.Update(ctx, started.Task.ID, model.UpdateRequest{
		ExpectedVersion: started.Task.Version,
		RunID:           started.Run.ID,
		Status:          model.TaskDone,
		CompleteItemIDs: []string{started.Task.Items[0].ID},
	}, "owner")
	if err != nil {
		t.Fatal(err)
	}

	local := waitForTaskEvent(t, ctx, localEvents, updated.ID, "task.updated")
	remote := waitForTaskEvent(t, ctx, remoteEvents, updated.ID, "task.updated")
	if local.ID != remote.ID {
		t.Fatalf("replicas received different event IDs: %s and %s", local.ID, remote.ID)
	}
	select {
	case <-pushDelivered:
	case <-ctx.Done():
		t.Fatal("remote Web Push worker did not receive the fanned-out event")
	}
	select {
	case event := <-localEvents:
		if event.ID == local.ID {
			t.Fatalf("local event %s was delivered twice", event.ID)
		}
	case <-time.After(250 * time.Millisecond):
	}

	cancel()
	remotePush.Wait()
	firstTasks.WaitEventFanout()
	secondTasks.WaitEventFanout()
}

func waitForFanoutUnready(t *testing.T, ctx context.Context, tasks *service.Service) {
	t.Helper()
	for {
		if err := tasks.Ready(ctx); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("PostgreSQL event fan-out did not report the disconnected listener")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func waitForFanoutReady(t *testing.T, ctx context.Context, services ...*service.Service) {
	t.Helper()
	for {
		ready := true
		for _, tasks := range services {
			if err := tasks.Ready(ctx); err != nil {
				ready = false
				break
			}
		}
		if ready {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("PostgreSQL event fan-out did not become ready")
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func waitForTaskEvent(t *testing.T, ctx context.Context, events <-chan model.Event, taskID, kind string) model.Event {
	t.Helper()
	for {
		select {
		case event := <-events:
			if event.TaskID == taskID && event.Kind == kind {
				return event
			}
		case <-ctx.Done():
			t.Fatalf("did not receive %s event for task %s", kind, taskID)
		}
	}
}

func postgresFanoutFixture(t *testing.T, baseURL string) (string, func()) {
	t.Helper()
	admin, err := sql.Open("pgx", baseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "fanout_test_" + strings.ToLower(ulid.Make().String())
	if _, err := admin.ExecContext(t.Context(), `CREATE SCHEMA `+schema); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String(), func() {
		if _, err := admin.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("drop PostgreSQL fan-out schema: %v", err)
		}
		if err := admin.Close(); err != nil {
			t.Errorf("close PostgreSQL admin connection: %v", err)
		}
	}
}
