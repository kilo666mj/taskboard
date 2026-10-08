package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
)

func normalizeRequirements(values []string) ([]string, error) {
	if len(values) > 50 {
		return nil, fmt.Errorf("%w: requirements are limited to 50", ErrValidation)
	}
	seen := map[string]bool{}
	items := []string{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if !model.IsRequirementToken(value) {
			return nil, fmt.Errorf("%w: requirements must be lowercase namespace:value tokens", ErrValidation)
		}
		if !seen[value] {
			seen[value] = true
			items = append(items, value)
		}
	}
	sort.Strings(items)
	return items, nil
}
func workerMatches(requirements, capabilities []string) bool {
	have := map[string]bool{}
	for _, item := range capabilities {
		have[item] = true
	}
	for _, item := range requirements {
		if !have[item] {
			return false
		}
	}
	return true
}
func insertRequirements(ctx context.Context, tx *store.Tx, taskID string, items []string, actor string, now time.Time) error {
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_requirements(task_id,requirement,created_by,created_at) VALUES(?,?,?,?)`, taskID, item, actor, stamp(now)); err != nil {
			return err
		}
	}
	return nil
}

// applyDefaultRequirements gives an agent-lane task without requirements the
// instance defaults and reports the tokens it added.
func (s *Service) applyDefaultRequirements(ctx context.Context, tx *store.Tx, taskID, actor string, now time.Time) ([]string, error) {
	if len(s.defaultRequirements) == 0 {
		return nil, nil
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_requirements WHERE task_id=?`, taskID).Scan(&existing); err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, nil
	}
	return s.defaultRequirements, insertRequirements(ctx, tx, taskID, s.defaultRequirements, actor, now)
}

func (s *Service) setTaskRequirementsFor(ctx context.Context, taskID string, request model.SetTaskRequirementsRequest, principal Principal) (model.Task, error) {
	if principal.Agent || !principal.Can(PermissionTaskWrite) {
		return model.Task{}, ErrForbidden
	}
	items, err := normalizeRequirements(request.Requirements)
	if err != nil || request.ExpectedVersion < 1 {
		if err == nil {
			err = fmt.Errorf("%w: expected_version is required", ErrValidation)
		}
		return model.Task{}, err
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.Task{}, err
	}
	if !canMutate(task, principal) {
		return model.Task{}, ErrForbidden
	}
	now := time.Now().UTC()
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.Task{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := store.LoadTask(ctx, tx, taskID)
	if err != nil {
		return model.Task{}, err
	}
	if current.Version != request.ExpectedVersion {
		return model.Task{}, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM task_requirements WHERE task_id=?`, taskID); err != nil {
		return model.Task{}, err
	}
	if err = insertRequirements(ctx, tx, taskID, items, principal.ID, now); err != nil {
		return model.Task{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET last_edited_by=?,version=version+1,updated_at=? WHERE id=? AND version=?`, principal.ID, stamp(now), taskID, request.ExpectedVersion); err != nil {
		return model.Task{}, err
	}
	event := model.Event{ID: newID(now), TaskID: taskID, Kind: "task.requirements_updated", Actor: principal.ID, Message: "Operational requirements updated", Payload: map[string]any{"requirements": items, "version": request.ExpectedVersion + 1}, CreatedAt: now}
	if err = store.InsertEvent(ctx, tx, event); err != nil {
		return model.Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.Task{}, err
	}
	s.publish(event)
	return s.store.GetTask(ctx, taskID)
}
func (s *Service) AdvertiseWorkerFor(ctx context.Context, request model.AdvertiseWorkerRequest, principal Principal) (model.WorkerAdvertisement, error) {
	if !principal.Agent || !principal.HasCapability(CapabilityWorkerAdvertise) {
		return model.WorkerAdvertisement{}, ErrForbidden
	}
	items, err := normalizeRequirements(request.Capabilities)
	if err != nil {
		return model.WorkerAdvertisement{}, err
	}
	if request.Capacity < 0 || request.Capacity > 100 {
		return model.WorkerAdvertisement{}, fmt.Errorf("%w: capacity must be 0-100", ErrValidation)
	}
	ttl := time.Duration(request.TTLSeconds) * time.Second
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	if ttl < 30*time.Second || ttl > time.Hour {
		return model.WorkerAdvertisement{}, fmt.Errorf("%w: ttl_seconds must be 30-3600", ErrValidation)
	}
	now := time.Now().UTC()
	item := model.WorkerAdvertisement{Principal: principal.ID, Capabilities: items, Capacity: request.Capacity, UpdatedAt: now, ExpiresAt: now.Add(ttl)}
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return item, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = store.UpsertWorkerAdvertisement(ctx, tx, item); err != nil {
		return item, err
	}
	if err = tx.Commit(); err != nil {
		return item, err
	}
	return item, nil
}
