package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/service"
	"github.com/kilo666mj/taskboard/internal/store"
)

func TestWebhookDeliveryDoesNotFollowRedirects(t *testing.T) {
	redirectTargetCalled := false
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectTargetCalled = true
	}))
	defer target.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	deliveries := New(nil, redirect.URL, "0123456789abcdef0123456789abcdef", 3, slog.New(slog.NewTextHandler(io.Discard, nil)))
	deliveries.client.Transport = redirect.Client().Transport
	if err := deliveries.send(t.Context(), "event", 1, []byte(`{"kind":"task.created"}`)); err == nil {
		t.Fatal("redirecting webhook was accepted")
	}
	if redirectTargetCalled {
		t.Fatal("webhook client followed a redirect")
	}
}

func TestSignedWebhookDelivery(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	received := make(chan []byte, 1)
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		if r.Header.Get("X-Taskboard-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) || r.Header.Get("X-Taskboard-Event-ID") == "" {
			t.Error("missing or invalid webhook authentication headers")
		}
		received <- body
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header)}, nil
	})

	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "taskboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	tasks := service.New(database, time.Minute)
	if _, err := tasks.Create(t.Context(), model.CreateRequest{Title: "Webhook task"}, "owner"); err != nil {
		t.Fatal(err)
	}

	deliveries := New(database, "https://hooks.example.test/taskboard", secret, 3, slog.New(slog.NewTextHandler(io.Discard, nil)))
	deliveries.client.Transport = transport
	deliveries.process(t.Context())
	select {
	case body := <-received:
		if len(body) == 0 {
			t.Fatal("empty webhook body")
		}
	case <-time.After(time.Second):
		t.Fatal("webhook was not delivered")
	}
	if dead, err := database.ListDeadWebhookDeliveries(t.Context(), 10); err != nil || len(dead) != 0 {
		t.Fatalf("dead letters = %+v, %v", dead, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
