package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/kilo666mj/taskboard/internal/store"
)

type Service struct {
	database    *store.Store
	url         string
	secret      []byte
	maxAttempts int
	client      *http.Client
	logger      *slog.Logger
	wait        sync.WaitGroup
}

func New(database *store.Store, url, secret string, maxAttempts int, logger *slog.Logger) *Service {
	return &Service{database: database, url: url, secret: []byte(secret), maxAttempts: maxAttempts, client: &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, logger: logger}
}

func (s *Service) Enabled() bool { return s.url != "" && len(s.secret) > 0 }

func (s *Service) Run(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	s.wait.Add(1)
	go func() {
		defer s.wait.Done()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			s.process(ctx)
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (s *Service) Wait() { s.wait.Wait() }

func (s *Service) process(ctx context.Context) {
	if _, err := s.database.QueueWebhookEvents(ctx, 100); err != nil {
		s.logger.Warn("queue webhook events", "error", err)
		return
	}
	for range 100 {
		delivery, event, err := s.database.NextWebhookDelivery(ctx)
		if err != nil {
			if err != store.ErrNotFound {
				s.logger.Warn("load webhook delivery", "error", err)
			}
			return
		}
		payload, err := json.Marshal(event)
		if err == nil {
			err = s.send(ctx, delivery.EventID, delivery.Attempts+1, payload)
		}
		if err == nil {
			if err := s.database.CompleteWebhookDelivery(ctx, delivery.ID); err != nil {
				s.logger.Warn("complete webhook delivery", "error", err)
				return
			}
			continue
		}
		attempts := delivery.Attempts + 1
		delay := time.Minute * time.Duration(1<<min(attempts-1, 6))
		if updateErr := s.database.FailWebhookDelivery(ctx, delivery.ID, attempts, s.maxAttempts, time.Now().UTC().Add(delay), err.Error()); updateErr != nil {
			s.logger.Warn("record webhook failure", "error", updateErr)
		}
	}
}

func (s *Service) send(ctx context.Context, eventID string, attempt int, payload []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write(payload)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Taskboard-Event-ID", eventID)
	request.Header.Set("X-Taskboard-Delivery-Attempt", strconv.Itoa(attempt))
	request.Header.Set("X-Taskboard-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", response.StatusCode)
	}
	return nil
}
