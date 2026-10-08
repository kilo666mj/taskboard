package service

import (
	"context"
	"errors"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
)

// The exported task methods below return what their unexported bodies return,
// with links to tasks the caller may not see reduced to their IDs: a blocker
// keeps whether it is satisfied, a kept task its ID, and tasks merged in as
// duplicates are left out.

// GetFor returns a task the principal may see.
func (s *Service) GetFor(ctx context.Context, id string, principal Principal) (model.Task, error) {
	task, err := s.getFor(ctx, id, principal)
	return s.hideLinks(ctx, principal, task, err)
}

// ListFor returns a page of the tasks the principal may see.
func (s *Service) ListFor(ctx context.Context, request model.ListTasksRequest, principal Principal) (model.TaskPage, error) {
	page, err := s.listFor(ctx, request, principal)
	if err != nil {
		return page, err
	}
	for index := range page.Tasks {
		if page.Tasks[index], err = s.hideLinks(ctx, principal, page.Tasks[index], nil); err != nil {
			return model.TaskPage{}, err
		}
	}
	return page, nil
}

func (s *Service) CreateFor(ctx context.Context, request model.CreateRequest, principal Principal) (model.Task, error) {
	task, err := s.createFor(ctx, request, principal)
	return s.hideLinks(ctx, principal, task, err)
}

func (s *Service) StartFor(ctx context.Context, request model.StartRequest, principal Principal) (model.StartResult, error) {
	result, err := s.startFor(ctx, request, principal)
	result.Task, err = s.hideLinks(ctx, principal, result.Task, err)
	return result, err
}

func (s *Service) ClaimFor(ctx context.Context, taskID string, request model.ClaimRequest, principal Principal) (model.StartResult, error) {
	result, err := s.claimFor(ctx, taskID, request, principal)
	result.Task, err = s.hideLinks(ctx, principal, result.Task, err)
	return result, err
}

func (s *Service) UpdateFor(ctx context.Context, taskID string, request model.UpdateRequest, principal Principal) (model.Task, error) {
	task, err := s.updateFor(ctx, taskID, request, principal)
	return s.hideLinks(ctx, principal, task, err)
}

func (s *Service) RenameRunFor(ctx context.Context, taskID, runID string, request model.RenameRunRequest, principal Principal) (model.Task, error) {
	task, err := s.renameRunFor(ctx, taskID, runID, request, principal)
	return s.hideLinks(ctx, principal, task, err)
}

func (s *Service) MoveFor(ctx context.Context, taskID string, request model.MoveRequest, principal Principal) (model.Task, error) {
	task, err := s.moveFor(ctx, taskID, request, principal)
	return s.hideLinks(ctx, principal, task, err)
}

func (s *Service) AddTaskDependencyFor(ctx context.Context, taskID string, request model.AddTaskDependencyRequest, principal Principal) (model.Task, error) {
	task, err := s.addTaskDependencyFor(ctx, taskID, request, principal)
	return s.hideLinks(ctx, principal, task, err)
}

func (s *Service) RemoveTaskDependencyFor(ctx context.Context, taskID, blockedBy string, expectedVersion int64, principal Principal) (model.Task, error) {
	task, err := s.removeTaskDependencyFor(ctx, taskID, blockedBy, expectedVersion, principal)
	return s.hideLinks(ctx, principal, task, err)
}

func (s *Service) SetTaskRequirementsFor(ctx context.Context, taskID string, request model.SetTaskRequirementsRequest, principal Principal) (model.Task, error) {
	task, err := s.setTaskRequirementsFor(ctx, taskID, request, principal)
	return s.hideLinks(ctx, principal, task, err)
}

func (s *Service) ReviewAndRequeueFor(ctx context.Context, taskID string, request model.ReviewRequeueRequest, principal Principal) (ReviewRequeueResult, error) {
	result, err := s.reviewAndRequeueFor(ctx, taskID, request, principal)
	result.Task, err = s.hideLinks(ctx, principal, result.Task, err)
	return result, err
}

// ListTaskDependenciesFor returns a task's blockers, reduced to their IDs
// where the principal may not see them.
func (s *Service) ListTaskDependenciesFor(ctx context.Context, taskID string, principal Principal) ([]model.TaskDependency, error) {
	dependencies, err := s.listTaskDependenciesFor(ctx, taskID, principal)
	if err != nil {
		return nil, err
	}
	task, err := s.hideLinks(ctx, principal, model.Task{Dependencies: dependencies}, nil)
	return task.Dependencies, err
}

// hideLinks reduces the task's links to tasks the principal may not see. It
// passes an error from the call it wraps through untouched.
func (s *Service) hideLinks(ctx context.Context, principal Principal, task model.Task, err error) (model.Task, error) {
	if err != nil || len(task.Dependencies) == 0 && task.DuplicateOf == nil && len(task.Duplicates) == 0 {
		return task, err
	}
	visible := map[string]bool{}
	canSee := func(id string) (bool, error) {
		if seen, ok := visible[id]; ok {
			return seen, nil
		}
		linked, err := s.store.TaskAccess(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			visible[id] = false
			return false, nil
		}
		if err != nil {
			return false, err
		}
		visible[id] = CanView(linked, principal)
		return visible[id], nil
	}
	if len(task.Dependencies) > 0 {
		dependencies := make([]model.TaskDependency, len(task.Dependencies))
		for index, dependency := range task.Dependencies {
			ok, err := canSee(dependency.BlockedByTaskID)
			if err != nil {
				return model.Task{}, err
			}
			if !ok {
				dependency.BlockedByTitle, dependency.BlockedByStatus, dependency.Hidden = "", "", true
			}
			dependencies[index] = dependency
		}
		task.Dependencies = dependencies
	}
	if task.DuplicateOf != nil {
		ok, err := canSee(task.DuplicateOf.TaskID)
		if err != nil {
			return model.Task{}, err
		}
		if !ok {
			task.DuplicateOf = &model.TaskLink{TaskID: task.DuplicateOf.TaskID, Hidden: true}
		}
	}
	if len(task.Duplicates) > 0 {
		duplicates := []model.TaskLink{}
		for _, link := range task.Duplicates {
			ok, err := canSee(link.TaskID)
			if err != nil {
				return model.Task{}, err
			}
			if ok {
				duplicates = append(duplicates, link)
			}
		}
		task.Duplicates = duplicates
		if len(duplicates) == 0 {
			task.Duplicates = nil
		}
	}
	return task, nil
}
