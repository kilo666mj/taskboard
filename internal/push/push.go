package push

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	pwakit "github.com/kilo666mj/pwa-kit"
	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/observability"
	"github.com/kilo666mj/taskboard/internal/service"
	"github.com/kilo666mj/taskboard/internal/store"
)

type Service struct {
	database   *store.Store
	tasks      *service.Service
	publicKey  string
	privateKey string
	contact    string
	logger     *slog.Logger
	client     *http.Client
	wait       sync.WaitGroup
	metrics    *observability.Metrics
}

type notification struct {
	title  string
	body   string
	tag    string
	urgent bool
}

func New(database *store.Store, tasks *service.Service, publicKey, privateKey, contact string, logger *slog.Logger, metrics ...*observability.Metrics) *Service {
	service := &Service{database: database, tasks: tasks, publicKey: publicKey, privateKey: privateKey, contact: contact, logger: logger, client: pwakit.NewPublicHTTPClient(15 * time.Second)}
	if len(metrics) > 0 {
		service.metrics = metrics[0]
	}
	return service
}

func (s *Service) Enabled() bool     { return s.publicKey != "" && s.privateKey != "" }
func (s *Service) PublicKey() string { return s.publicKey }
func (s *Service) Save(ctx context.Context, subscription store.PushSubscription) error {
	return s.database.SavePushSubscription(ctx, subscription)
}
func (s *Service) Delete(ctx context.Context, endpoint, ownerID string) error {
	return s.database.DeletePushSubscription(ctx, endpoint, ownerID)
}
func (s *Service) UpdatePreferences(ctx context.Context, endpoint, ownerID string, progress, reminders, summaries bool) error {
	return s.database.UpdatePushPreferences(ctx, endpoint, ownerID, progress, reminders, summaries)
}

func (s *Service) Run(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	events, cancel := s.tasks.Subscribe()
	s.wait.Add(1)
	go func() {
		defer s.wait.Done()
		defer cancel()
		for {
			select {
			case event, ok := <-events:
				if !ok {
					return
				}
				s.deliver(ctx, event)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (s *Service) Wait() { s.wait.Wait() }

func (s *Service) deliver(ctx context.Context, event model.Event) {
	if event.Kind != "task.updated" {
		return
	}
	status := eventStatus(event)
	completedItemIDs := eventCompletedItemIDs(event)
	if len(completedItemIDs) == 0 && !notifiableStatus(status) {
		return
	}
	task, err := s.tasks.Get(ctx, event.TaskID)
	if err != nil {
		s.logger.Error("load task for push", "error", err)
		return
	}
	notifications := notificationsFor(task, status, completedItemIDs)
	subscriptions, err := s.database.ListPushSubscriptions(ctx)
	if err != nil {
		s.logger.Error("list push subscriptions", "error", err)
		return
	}
	for _, message := range notifications {
		payload, _ := json.Marshal(map[string]any{"title": message.title, "body": message.body, "tag": message.tag, "url": "/?task=" + task.ID, "urgent": message.urgent})
		for _, subscription := range subscriptions {
			if !subscription.NotifyProgress || !service.CanView(task, service.HumanPrincipal(subscription.OwnerID)) {
				continue
			}
			result, err := pwakit.Send(ctx, pwakit.Config{PublicKey: s.publicKey, PrivateKey: s.privateKey, Contact: s.contact}, pwakit.Subscription{Endpoint: subscription.Endpoint, Keys: pwakit.Keys{P256dh: subscription.P256DH, Auth: subscription.Auth}}, payload, pwakit.Options{TTL: 300, Urgency: "normal", HTTPClient: s.client})
			if result.Expired() {
				if s.metrics != nil {
					s.metrics.ObservePush("expired")
				}
				_ = s.database.DeletePushSubscription(ctx, subscription.Endpoint, subscription.OwnerID)
			} else if err != nil {
				if s.metrics != nil {
					s.metrics.ObservePush("error")
				}
				s.logger.Warn("push delivery failed", "error", err)
			} else if s.metrics != nil {
				s.metrics.ObservePush("success")
			}
		}
	}
}

func eventStatus(event model.Event) model.TaskStatus {
	if status, ok := event.Payload["status"].(model.TaskStatus); ok {
		return status
	}
	if status, ok := event.Payload["status"].(string); ok {
		return model.TaskStatus(status)
	}
	return ""
}

func eventCompletedItemIDs(event model.Event) []string {
	switch values := event.Payload["completed_item_ids"].(type) {
	case []string:
		return values
	case []any:
		ids := make([]string, 0, len(values))
		for _, value := range values {
			if id, ok := value.(string); ok {
				ids = append(ids, id)
			}
		}
		return ids
	default:
		return nil
	}
}

func notifiableStatus(status model.TaskStatus) bool {
	return status == model.TaskBlocked || status == model.TaskWaiting || status == model.TaskStale || status == model.TaskDone
}

func notificationsFor(task model.Task, status model.TaskStatus, completedItemIDs []string) []notification {
	items := make(map[string]string, len(task.Items))
	for _, item := range task.Items {
		items[item.ID] = item.Label
	}
	notifications := make([]notification, 0, len(completedItemIDs)+1)
	for _, id := range completedItemIDs {
		if label, ok := items[id]; ok {
			title := task.Title + " · Step complete"
			if status == model.TaskDone {
				title = task.Title + " · done"
			}
			notifications = append(notifications, notification{
				title: title,
				body:  label,
				tag:   "task-" + task.ID + "-step-" + id,
			})
		}
	}
	if !notifiableStatus(status) || status == model.TaskDone && len(notifications) > 0 {
		return notifications
	}
	body := task.CurrentNote
	if task.Blocker != "" {
		body = task.Blocker
	}
	if task.WaitingFor != "" {
		body = task.WaitingFor
	}
	if body == "" {
		body = "Task status changed to " + string(task.Status)
	}
	return append(notifications, notification{
		title:  task.Title + " · " + string(task.Status),
		body:   body,
		tag:    "task-" + task.ID,
		urgent: task.Status == model.TaskBlocked || task.Status == model.TaskStale,
	})
}
