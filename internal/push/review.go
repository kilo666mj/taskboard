package push

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/service"
	pwakit "go.michaelspost.com/pwa-kit"
)

// ConfigureReview batches normal/low-priority updates from explicit producers.
// Existing subscription opt-outs and high/urgent notifications still apply.
func (s *Service) ConfigureReview(producers []string, timezone string) {
	s.routineProducers = make(map[string]bool, len(producers))
	for _, producer := range producers {
		s.routineProducers[producer] = true
	}
	s.reviewLocation, _ = time.LoadLocation(timezone) // validated by config.Load
	if s.reviewLocation == nil {
		s.reviewLocation = time.UTC
	}
}

func (s *Service) routine(task model.Task) bool {
	return s.routineProducers[task.CreatedBy] && task.Priority != "urgent" && task.Priority != "high"
}

// Snapshot current work rather than queueing stale alert text. Run after 09:00
// in the configured timezone, once per subscription/day, including after restart.
func (s *Service) deliverReview(ctx context.Context, now time.Time) error {
	if len(s.routineProducers) == 0 || now.In(s.reviewLocation).Hour() < 9 {
		return nil
	}
	day := now.In(s.reviewLocation).Format("2006-01-02")
	rows, err := s.database.DB().QueryContext(ctx, `SELECT id FROM tasks WHERE status IN ('waiting','blocked','stale') ORDER BY CASE priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END,created_at`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	var tasks []model.Task
	for _, id := range ids {
		task, err := s.tasks.Get(ctx, id)
		if err != nil {
			return err
		}
		if (task.Status == model.TaskWaiting || task.Status == model.TaskBlocked || task.Status == model.TaskStale) && s.routineProducers[task.CreatedBy] && (task.DeferUntil == "" || task.DeferUntil <= day) {
			tasks = append(tasks, task)
		}
	}
	subscriptions, err := s.database.ListPushSubscriptions(ctx)
	if err != nil {
		return err
	}
	for _, subscription := range subscriptions {
		if !subscription.NotifyProgress {
			continue
		}
		revoked, err := s.database.PrincipalRevoked(ctx, subscription.OwnerID)
		if err != nil {
			return err
		}
		if revoked {
			continue
		}
		var titles []string
		for _, task := range tasks {
			if service.CanView(task, service.HumanPrincipalWithRole(subscription.OwnerID, service.RoleViewer)) {
				titles = append(titles, task.Title)
			}
		}
		if len(titles) == 0 {
			continue
		}
		stamp := now.UTC().Format(time.RFC3339Nano)
		claimed, err := s.database.DB().ExecContext(ctx, `INSERT INTO push_review_deliveries(endpoint,day,lease_until) VALUES(?,?,?) ON CONFLICT(endpoint,day) DO UPDATE SET lease_until=excluded.lease_until WHERE push_review_deliveries.sent_at IS NULL AND push_review_deliveries.lease_until<?`, subscription.Endpoint, day, now.Add(5*time.Minute).UTC().Format(time.RFC3339Nano), stamp)
		if err != nil {
			return err
		}
		count, err := claimed.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			continue
		}
		body := strings.Join(titles[:min(3, len(titles))], "\n")
		if len(titles) > 3 {
			body += fmt.Sprintf("\n…and %d more", len(titles)-3)
		}
		payload, _ := json.Marshal(map[string]any{"title": fmt.Sprintf("Daily review · %d automatic tasks", len(titles)), "body": body, "tag": "taskboard-daily-review-" + day, "url": "/?view=automatic", "urgent": false})
		result, err := pwakit.Send(ctx, pwakit.Config{PublicKey: s.publicKey, PrivateKey: s.privateKey, Contact: s.contact}, pwakit.Subscription{Endpoint: subscription.Endpoint, Keys: pwakit.Keys{P256dh: subscription.P256DH, Auth: subscription.Auth}}, payload, pwakit.Options{TTL: 3600, Urgency: "normal", HTTPClient: s.client})
		if result.Expired() {
			if err := s.database.DeletePushSubscription(ctx, subscription.Endpoint, subscription.OwnerID); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			s.logger.Warn("daily review push failed", "error", err)
			continue
		}
		if _, err := s.database.DB().ExecContext(ctx, `UPDATE push_review_deliveries SET sent_at=? WHERE endpoint=? AND day=?`, stamp, subscription.Endpoint, day); err != nil {
			return err
		}
	}
	_, err = s.database.DB().ExecContext(ctx, `DELETE FROM push_review_deliveries WHERE day<?`, now.AddDate(0, 0, -30).Format("2006-01-02"))
	return err
}
