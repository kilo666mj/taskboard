package service

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
	"strings"
	"time"
)

func (s *Service) RecordUsageFor(ctx context.Context, taskID string, request model.RecordUsageRequest, principal Principal) (model.UsageRecord, error) {
	if !principal.Agent || !principal.HasCapability(CapabilityTaskUsage) {
		return model.UsageRecord{}, ErrForbidden
	}
	taskID, request.RunID, request.Provider, request.Model = strings.TrimSpace(taskID), strings.TrimSpace(request.RunID), strings.TrimSpace(request.Provider), strings.TrimSpace(request.Model)
	if request.RunID == "" || request.InputTokens < 0 || request.OutputTokens < 0 || request.EstimatedCostMicros < 0 || request.InputTokens == 0 && request.OutputTokens == 0 && request.EstimatedCostMicros == 0 {
		return model.UsageRecord{}, fmt.Errorf("%w: run_id and non-negative non-zero usage are required", ErrValidation)
	}
	if len(request.Provider) > 80 || len(request.Model) > 120 {
		return model.UsageRecord{}, fmt.Errorf("%w: provider/model labels are too long", ErrValidation)
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.UsageRecord{}, err
	}
	if _, ok := activeOwnedRun(task, request.RunID, principal.ID); !ok {
		return model.UsageRecord{}, fmt.Errorf("%w: active owned run is required", ErrValidation)
	}
	if request.IdempotencyKey != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
	}
	hash, err := requestHash(struct {
		TaskID string
		model.RecordUsageRequest
	}{taskID, request})
	if err != nil {
		return model.UsageRecord{}, err
	}
	request.IdempotencyHash = hash
	event, replay, err := s.replayIdempotency(ctx, principal, "task_usage_record", request.IdempotencyKey, hash)
	if err != nil {
		return model.UsageRecord{}, err
	}
	if replay {
		return s.store.GetUsageRecord(ctx, fmt.Sprint(event.Payload["usage_id"]))
	}
	now := time.Now().UTC()
	item := model.UsageRecord{ID: newID(now), TaskID: taskID, RunID: request.RunID, Provider: request.Provider, Model: request.Model, InputTokens: request.InputTokens, OutputTokens: request.OutputTokens, EstimatedCostMicros: request.EstimatedCostMicros, RecordedBy: principal.ID, CreatedAt: now}
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return item, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = store.InsertUsageRecord(ctx, tx, item); err != nil {
		return item, err
	}
	payload := map[string]any{"usage_id": item.ID, "input_tokens": item.InputTokens, "output_tokens": item.OutputTokens, "estimated_cost_micros": item.EstimatedCostMicros}
	mergePayload(payload, idempotencyPayload("task_usage_record", request.IdempotencyKey, request.IdempotencyHash))
	created := model.Event{ID: newID(now), TaskID: taskID, RunID: request.RunID, Kind: "task.usage_recorded", Actor: principal.ID, Message: "Aggregate usage recorded", Payload: payload, CreatedAt: now}
	if err = store.InsertEvent(ctx, tx, created); err != nil {
		return item, err
	}
	if err = tx.Commit(); err == nil {
		s.publish(created)
	}
	return item, err
}

func (s *Service) AnalyticsFor(ctx context.Context, days int, principal Principal) (model.AnalyticsSummary, error) {
	if principal.Agent || principal.Role != RoleOwner && principal.Role != RoleAdmin {
		return model.AnalyticsSummary{}, ErrForbidden
	}
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		return model.AnalyticsSummary{}, fmt.Errorf("%w: analytics window is limited to 365 days", ErrValidation)
	}
	end := time.Now().UTC()
	start := end.Add(-time.Duration(days) * 24 * time.Hour)
	summary := model.AnalyticsSummary{WindowDays: days, WindowStart: start, WindowEnd: end}
	rows, err := s.store.DB().QueryContext(ctx, `SELECT status,created_at,completed_at FROM tasks WHERE created_at>=?`, stamp(start))
	if err != nil {
		return summary, err
	}
	var queueSeconds float64
	var queueCount int64
	for rows.Next() {
		var status model.TaskStatus
		var created string
		var completed sql.NullString
		if err := rows.Scan(&status, &created, &completed); err != nil {
			_ = rows.Close()
			return summary, err
		}
		createdAt, _ := time.Parse(time.RFC3339Nano, created)
		summary.TasksCreated++
		switch status {
		case model.TaskDone:
			summary.TasksDone++
		case model.TaskCancelled:
			summary.TasksCancelled++
		case model.TaskQueued:
			queueSeconds += end.Sub(createdAt).Seconds()
			queueCount++
		}
	}
	_ = rows.Close()
	if queueCount > 0 {
		summary.MeanQueueAgeSeconds = queueSeconds / float64(queueCount)
	}
	terminal := summary.TasksDone + summary.TasksCancelled
	if terminal > 0 {
		summary.CompletionRate = float64(summary.TasksDone) / float64(terminal)
	}
	runRows, err := s.store.DB().QueryContext(ctx, `SELECT status,started_at,ended_at FROM agent_runs WHERE started_at>=?`, stamp(start))
	if err != nil {
		return summary, err
	}
	var execution float64
	var endedCount int64
	for runRows.Next() {
		var status model.TaskStatus
		var started string
		var endedAt sql.NullString
		if err := runRows.Scan(&status, &started, &endedAt); err != nil {
			_ = runRows.Close()
			return summary, err
		}
		summary.Runs++
		if status == model.TaskStale {
			summary.StaleRuns++
		}
		if endedAt.Valid {
			begin, _ := time.Parse(time.RFC3339Nano, started)
			finish, _ := time.Parse(time.RFC3339Nano, endedAt.String)
			execution += finish.Sub(begin).Seconds()
			endedCount++
		}
	}
	_ = runRows.Close()
	if summary.Runs > 0 {
		summary.StaleRunRate = float64(summary.StaleRuns) / float64(summary.Runs)
	}
	if endedCount > 0 {
		summary.MeanExecutionSeconds = execution / float64(endedCount)
	}
	queries := []struct {
		query  string
		target *int64
	}{{`SELECT COUNT(*) FROM task_escalations WHERE created_at>=?`, &summary.Escalations}, {`SELECT COUNT(*) FROM run_control_requests WHERE kind='retry' AND status='completed' AND created_at>=?`, &summary.Retries}, {`SELECT COUNT(*) FROM task_references WHERE kind='review' AND created_at>=?`, &summary.ReviewReferences}}
	for _, query := range queries {
		if err := s.store.DB().QueryRowContext(ctx, query.query, stamp(start)).Scan(query.target); err != nil {
			return summary, err
		}
	}
	if summary.TasksCreated > 0 {
		summary.EscalationRate = float64(summary.Escalations) / float64(summary.TasksCreated)
	}
	if summary.TasksDone > 0 {
		summary.MeanReviewCycles = float64(summary.ReviewReferences) / float64(summary.TasksDone)
	}
	if err := s.store.DB().QueryRowContext(ctx, `SELECT COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(estimated_cost_micros),0) FROM usage_records WHERE created_at>=?`, stamp(start)).Scan(&summary.InputTokens, &summary.OutputTokens, &summary.EstimatedCostMicros); err != nil {
		return summary, err
	}
	return summary, nil
}
