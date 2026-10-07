package push

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
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

func TestDailyReviewRespectsVisibilityDeferralAndDeliveryReceipt(t *testing.T) {
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	tasks := service.New(database, time.Minute)
	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	err = database.SavePushSubscription(t.Context(), store.PushSubscription{Endpoint: "https://web.push.apple.com/review", OwnerID: "person", P256DH: base64.RawURLEncoding.EncodeToString(receiver.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16))})
	if err != nil {
		t.Fatal(err)
	}
	s := New(database, tasks, public, private, "mailto:test@example.com", slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.ConfigureReview([]string{"agent:producer"}, "Europe/Berlin")
	sent := 0
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		sent++
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	create := func(visibility string, deferUntil string) model.Task {
		t.Helper()
		item, err := tasks.Create(t.Context(), model.CreateRequest{Title: "Review incident", Visibility: model.TaskVisibility(visibility)}, "agent:producer")
		if err != nil {
			t.Fatal(err)
		}
		wait := "Decision needed"
		item, err = tasks.Update(t.Context(), item.ID, model.UpdateRequest{ExpectedVersion: item.Version, Status: model.TaskWaiting, WaitingFor: &wait, DeferUntil: &deferUntil}, "agent:producer")
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	create("private", "")
	create("team", "2026-10-09")
	now := time.Date(2026, 10, 7, 7, 0, 0, 0, time.UTC) // 09:00 Berlin
	if err := s.deliverReview(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatal("private or deferred work leaked into review")
	}
	item := create("team", "")
	if err := s.deliverReview(t.Context(), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatal("review sent before morning")
	}
	for range 2 {
		if err := s.deliverReview(t.Context(), now); err != nil {
			t.Fatal(err)
		}
	}
	if sent != 1 {
		t.Fatalf("sent %d reviews, want one", sent)
	}
	// A process restart shares the same receipt. Closing work removes it from
	// tomorrow's snapshot without needing to edit a notification queue.
	if _, err := tasks.Update(t.Context(), item.ID, model.UpdateRequest{ExpectedVersion: item.Version, Status: model.TaskDone}, "agent:producer"); err != nil {
		t.Fatal(err)
	}
	if err := s.deliverReview(t.Context(), now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if sent != 1 {
		t.Fatal("resolved incident was included")
	}
}

func TestUrgentAutomaticWorkIsNotBatched(t *testing.T) {
	s := &Service{}
	s.ConfigureReview([]string{"agent:producer"}, "UTC")
	for _, priority := range []string{"normal", "low", "high", "urgent"} {
		got := s.routine(model.Task{CreatedBy: "agent:producer", Priority: model.Priority(priority)})
		if got != (priority == "normal" || priority == "low") {
			t.Fatalf("priority %s routine=%v", priority, got)
		}
	}
	if s.routine(model.Task{CreatedBy: "person", Priority: "normal"}) {
		t.Fatal("human work was batched")
	}
}
