package push

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/service"
	"github.com/kilo666mj/taskboard/internal/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeliveryVAPIDSubject(t *testing.T) {
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "taskboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})
	tasks := service.New(database, time.Minute)
	started, err := tasks.Start(t.Context(), model.StartRequest{Title: "Push test", Checklist: []string{"Verify delivery"}}, "test")
	if err != nil {
		t.Fatal(err)
	}
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	receiverKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	err = database.SavePushSubscription(t.Context(), store.PushSubscription{
		Endpoint: "https://web.push.apple.com/test",
		P256DH:   base64.RawURLEncoding.EncodeToString(receiverKey.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ contact, subject string }{
		{"mailto:admin@example.com", "mailto:admin@example.com"},
		{"admin@example.com", "mailto:admin@example.com"},
		{"https://example.com/contact", "https://example.com/contact"},
	} {
		t.Run(test.contact, func(t *testing.T) {
			notifications := New(database, tasks, publicKey, privateKey, test.contact, slog.New(slog.NewTextHandler(io.Discard, nil)))
			called := false
			notifications.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				called = true
				header := r.Header.Get("Authorization")
				token := strings.Split(strings.TrimPrefix(header, "vapid t="), ",")[0]
				parts := strings.Split(token, ".")
				if len(parts) != 3 {
					t.Fatal("missing VAPID JWT")
				}
				payload, err := base64.RawURLEncoding.DecodeString(parts[1])
				if err != nil {
					t.Fatal(err)
				}
				var claims struct{ Sub, Aud string }
				if err := json.Unmarshal(payload, &claims); err != nil {
					t.Fatal(err)
				}
				if claims.Sub != test.subject || claims.Aud != "https://web.push.apple.com" {
					t.Fatalf("VAPID claims = %+v, want subject %q and Apple audience", claims, test.subject)
				}
				return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			notifications.deliver(t.Context(), model.Event{Kind: "task.updated", TaskID: started.Task.ID, Payload: map[string]any{
				"completed_item_ids": []string{started.Task.Items[0].ID},
			}})
			if !called {
				t.Fatal("push delivery did not send a request")
			}
		})
	}
}

func TestNotificationsForCompletedItems(t *testing.T) {
	task := model.Task{
		ID:     "task-1",
		Title:  "Ship taskboard",
		Status: model.TaskActive,
		Items: []model.ChecklistItem{
			{ID: "step-1", Label: "Build notifications"},
			{ID: "step-2", Label: "Verify notifications"},
		},
	}

	notifications := notificationsFor(task, model.TaskActive, []string{"step-1", "step-2"})
	if len(notifications) != 2 {
		t.Fatalf("notification count = %d, want 2", len(notifications))
	}
	if notifications[0].body != "Build notifications" || notifications[1].body != "Verify notifications" {
		t.Fatalf("notification bodies = %q, %q", notifications[0].body, notifications[1].body)
	}
	if notifications[0].urgent || notifications[1].urgent {
		t.Fatal("completed-step notifications must be quiet")
	}
}

func TestFinalStepCombinesDoneNotification(t *testing.T) {
	task := model.Task{
		ID:     "task-1",
		Title:  "Ship taskboard",
		Status: model.TaskDone,
		Items:  []model.ChecklistItem{{ID: "step-1", Label: "Verify notifications"}},
	}

	notifications := notificationsFor(task, model.TaskDone, []string{"step-1"})
	if len(notifications) != 1 {
		t.Fatalf("notification count = %d, want 1", len(notifications))
	}
	if notifications[0].body != "Verify notifications" {
		t.Fatalf("notification body = %q", notifications[0].body)
	}
}

func TestEventCompletedItemIDsAcceptsJSONShape(t *testing.T) {
	event := model.Event{Payload: map[string]any{"completed_item_ids": []any{"step-1", 2, "step-2"}}}
	ids := eventCompletedItemIDs(event)
	if len(ids) != 2 || ids[0] != "step-1" || ids[1] != "step-2" {
		t.Fatalf("completed item IDs = %#v", ids)
	}
}
