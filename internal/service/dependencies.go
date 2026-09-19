package service

import (
	"context"
	"fmt"
	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
	"strings"
	"time"
)

func (s *Service) ListTaskDependenciesFor(ctx context.Context, taskID string, principal Principal) ([]model.TaskDependency, error) {
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return nil, err
	}
	return s.store.ListTaskDependencies(ctx, task.ID)
}
func (s *Service) AddTaskDependencyFor(ctx context.Context, taskID string, request model.AddTaskDependencyRequest, principal Principal) (model.Task, error) {
	if principal.Agent || !principal.Can(PermissionTaskWrite) {
		return model.Task{}, ErrForbidden
	}
	taskID, request.BlockedByTaskID = strings.TrimSpace(taskID), strings.TrimSpace(request.BlockedByTaskID)
	if taskID == "" || request.BlockedByTaskID == "" || taskID == request.BlockedByTaskID || request.ExpectedVersion < 1 {
		return model.Task{}, fmt.Errorf("%w: distinct task IDs and expected_version are required", ErrValidation)
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.Task{}, err
	}
	if !canMutate(task, principal) {
		return model.Task{}, ErrForbidden
	}
	if _, err = s.GetFor(ctx, request.BlockedByTaskID, principal); err != nil {
		return model.Task{}, err
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
	var cycle int
	err = tx.QueryRowContext(ctx, `WITH RECURSIVE chain(id) AS (SELECT blocked_by_task_id FROM task_dependencies WHERE task_id=? UNION SELECT dependency.blocked_by_task_id FROM task_dependencies dependency JOIN chain ON dependency.task_id=chain.id) SELECT COUNT(*) FROM chain WHERE id=?`, request.BlockedByTaskID, taskID).Scan(&cycle)
	if err != nil {
		return model.Task{}, err
	}
	if cycle > 0 {
		return model.Task{}, fmt.Errorf("%w: dependency would create a cycle", ErrValidation)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO task_dependencies(task_id,blocked_by_task_id,created_by,created_at) VALUES(?,?,?,?) ON CONFLICT(task_id,blocked_by_task_id) DO NOTHING`, taskID, request.BlockedByTaskID, principal.ID, stamp(now))
	if err != nil {
		return model.Task{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return model.Task{}, fmt.Errorf("%w: dependency already exists", ErrValidation)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET version=version+1,updated_at=? WHERE id=? AND version=?`, stamp(now), taskID, request.ExpectedVersion); err != nil {
		return model.Task{}, err
	}
	event := model.Event{ID: newID(now), TaskID: taskID, Kind: "task.dependency_added", Actor: principal.ID, Message: "Task dependency added", Payload: map[string]any{"blocked_by_task_id": request.BlockedByTaskID, "version": request.ExpectedVersion + 1}, CreatedAt: now}
	if err = store.InsertEvent(ctx, tx, event); err != nil {
		return model.Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.Task{}, err
	}
	s.publish(event)
	return s.store.GetTask(ctx, taskID)
}
func (s *Service) RemoveTaskDependencyFor(ctx context.Context, taskID, blockedBy string, expectedVersion int64, principal Principal) (model.Task, error) {
	if principal.Agent || !principal.Can(PermissionTaskWrite) {
		return model.Task{}, ErrForbidden
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
	if current.Version != expectedVersion {
		return model.Task{}, ErrConflict
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM task_dependencies WHERE task_id=? AND blocked_by_task_id=?`, taskID, blockedBy)
	if err != nil {
		return model.Task{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return model.Task{}, store.ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET version=version+1,updated_at=? WHERE id=? AND version=?`, stamp(now), taskID, expectedVersion); err != nil {
		return model.Task{}, err
	}
	event := model.Event{ID: newID(now), TaskID: taskID, Kind: "task.dependency_removed", Actor: principal.ID, Message: "Task dependency removed", Payload: map[string]any{"blocked_by_task_id": blockedBy, "version": expectedVersion + 1}, CreatedAt: now}
	if err = store.InsertEvent(ctx, tx, event); err != nil {
		return model.Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.Task{}, err
	}
	s.publish(event)
	return s.store.GetTask(ctx, taskID)
}
