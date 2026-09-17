package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
	"github.com/oklog/ulid/v2"
)

var (
	ErrConflict   = errors.New("task version conflict")
	ErrForbidden  = errors.New("task access forbidden")
	ErrValidation = errors.New("validation failed")
)

type Principal struct {
	ID    string
	Agent bool
}

func HumanPrincipal(id string) Principal { return Principal{ID: strings.TrimSpace(id)} }
func AgentPrincipal(id string) Principal { return Principal{ID: strings.TrimSpace(id), Agent: true} }

func CanView(task model.Task, principal Principal) bool {
	if principal.Agent {
		return task.Visibility == model.VisibilityAgent || task.Visibility == model.VisibilityTeam && task.Owner == principal.ID
	}
	return task.Visibility != model.VisibilityPrivate || task.CreatedBy != "" && task.CreatedBy == principal.ID
}

func canMutate(task model.Task, principal Principal) bool {
	if !CanView(task, principal) {
		return false
	}
	if !principal.Agent {
		return true
	}
	return task.Owner != "" && task.Owner == principal.ID
}

type Service struct {
	store         *store.Store
	leaseDuration time.Duration
	mu            sync.RWMutex
	subscribers   map[chan model.Event]struct{}
}

func New(database *store.Store, leaseDuration time.Duration) *Service {
	return &Service{store: database, leaseDuration: leaseDuration, subscribers: make(map[chan model.Event]struct{})}
}

func (s *Service) Ready(ctx context.Context) error { return s.store.Ping(ctx) }

func (s *Service) Subscribe() (<-chan model.Event, func()) {
	channel := make(chan model.Event, 16)
	s.mu.Lock()
	s.subscribers[channel] = struct{}{}
	s.mu.Unlock()
	return channel, func() {
		s.mu.Lock()
		if _, ok := s.subscribers[channel]; ok {
			delete(s.subscribers, channel)
			close(channel)
		}
		s.mu.Unlock()
	}
}

func (s *Service) publish(event model.Event) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for subscriber := range s.subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
}

func (s *Service) Start(ctx context.Context, request model.StartRequest, actor string) (model.StartResult, error) {
	request.Title = strings.TrimSpace(request.Title)
	request.Type = normalizedTaskType(request.Type)
	request.Visibility = normalizedVisibility(request.Visibility, model.VisibilityTeam)
	request.Summary = strings.TrimSpace(request.Summary)
	request.Section = normalizedSection(request.Section)
	request.Project = strings.TrimSpace(request.Project)
	request.Repository = strings.TrimSpace(request.Repository)
	request.Priority = normalizedPriority(request.Priority)
	request.DueDate = strings.TrimSpace(request.DueDate)
	request.DeferUntil = strings.TrimSpace(request.DeferUntil)
	request.Recurrence = normalizedRecurrence(request.Recurrence)
	request.Agent = strings.TrimSpace(request.Agent)
	request.Client = strings.TrimSpace(request.Client)
	if request.Title == "" {
		return model.StartResult{}, fmt.Errorf("%w: title is required", ErrValidation)
	}
	if !model.IsTaskType(request.Type) {
		return model.StartResult{}, fmt.Errorf("%w: type must be personal or work", ErrValidation)
	}
	if !model.IsTaskVisibility(request.Visibility) {
		return model.StartResult{}, fmt.Errorf("%w: visibility must be private, team, or agent", ErrValidation)
	}
	if len(request.Title) > 200 || len(request.Summary) > 2000 || len(request.Section) > 80 || len(request.Project) > 120 || len(request.Repository) > 300 || len(request.Agent) > 100 || len(request.Client) > 100 {
		return model.StartResult{}, fmt.Errorf("%w: task text is too long", ErrValidation)
	}
	if err := validatePlanning(request.Priority, request.DueDate, request.DeferUntil, request.Recurrence); err != nil {
		return model.StartResult{}, err
	}
	if request.Agent == "" {
		request.Agent = actor
	}
	if request.Agent == "" {
		request.Agent = "unassigned"
	}
	if len(request.Checklist) == 0 {
		return model.StartResult{}, fmt.Errorf("%w: at least one checklist item is required", ErrValidation)
	}
	if len(request.Checklist) > 100 {
		return model.StartResult{}, fmt.Errorf("%w: checklist is limited to 100 items", ErrValidation)
	}
	for index := range request.Checklist {
		request.Checklist[index] = strings.TrimSpace(request.Checklist[index])
		if request.Checklist[index] == "" || len(request.Checklist[index]) > 300 {
			return model.StartResult{}, fmt.Errorf("%w: checklist items must contain 1-300 characters", ErrValidation)
		}
	}

	now := time.Now().UTC()
	taskID, runID := newID(now), newID(now)
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.StartResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	sortOrder, err := nextSortOrder(ctx, tx, request.Section)
	if err != nil {
		return model.StartResult{}, err
	}
	creator := defaultActor(actor, "user")
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks(id,title,summary,task_type,visibility,created_by,section,project,repository,priority,due_date,defer_until,recurrence,sort_order,status,owner,version,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, taskID, request.Title, request.Summary, request.Type, request.Visibility, creator, request.Section, request.Project, request.Repository, request.Priority, request.DueDate, request.DeferUntil, request.Recurrence, sortOrder, model.TaskActive, request.Agent, stamp(now), stamp(now))
	if err != nil {
		return model.StartResult{}, err
	}
	for index, label := range request.Checklist {
		status := model.ItemTodo
		if index == 0 {
			status = model.ItemActive
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO checklist_items(id,task_id,label,status,position,required,updated_at) VALUES(?,?,?,?,?,1,?)`, newID(now), taskID, label, status, index, stamp(now)); err != nil {
			return model.StartResult{}, err
		}
	}
	lease := now.Add(s.leaseDuration)
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_runs(id,task_id,agent,client,status,lease_expires_at,last_heartbeat_at,started_at) VALUES(?,?,?,?,?,?,?,?)`, runID, taskID, request.Agent, request.Client, model.TaskActive, stamp(lease), stamp(now), stamp(now))
	if err != nil {
		return model.StartResult{}, err
	}
	event := model.Event{ID: newID(now), TaskID: taskID, RunID: runID, Kind: "task.started", Actor: request.Agent, Message: request.Title, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, event); err != nil {
		return model.StartResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.StartResult{}, err
	}
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return model.StartResult{}, err
	}
	s.publish(event)
	return model.StartResult{Task: task, Run: task.Runs[0]}, nil
}

func (s *Service) StartFor(ctx context.Context, request model.StartRequest, principal Principal) (model.StartResult, error) {
	if principal.Agent {
		if request.Visibility == "" {
			request.Visibility = model.VisibilityAgent
		}
		if request.Visibility == model.VisibilityPrivate {
			return model.StartResult{}, ErrForbidden
		}
		request.Agent = principal.ID
	} else if request.Visibility == "" {
		request.Visibility = model.VisibilityPrivate
	}
	return s.Start(ctx, request, principal.ID)
}

// Create records work without starting it. Queued tasks deliberately have no
// run or lease; those are attached only when an agent claims the task.
func (s *Service) Create(ctx context.Context, request model.CreateRequest, actor string) (model.Task, error) {
	request.Title = strings.TrimSpace(request.Title)
	request.Type = normalizedTaskType(request.Type)
	request.Visibility = normalizedVisibility(request.Visibility, model.VisibilityTeam)
	request.Summary = strings.TrimSpace(request.Summary)
	request.Section = normalizedSection(request.Section)
	request.Project = strings.TrimSpace(request.Project)
	request.Repository = strings.TrimSpace(request.Repository)
	request.Priority = normalizedPriority(request.Priority)
	request.DueDate = strings.TrimSpace(request.DueDate)
	request.DeferUntil = strings.TrimSpace(request.DeferUntil)
	request.Recurrence = normalizedRecurrence(request.Recurrence)
	if request.Title == "" {
		return model.Task{}, fmt.Errorf("%w: title is required", ErrValidation)
	}
	if !model.IsTaskType(request.Type) {
		return model.Task{}, fmt.Errorf("%w: type must be personal or work", ErrValidation)
	}
	if !model.IsTaskVisibility(request.Visibility) {
		return model.Task{}, fmt.Errorf("%w: visibility must be private, team, or agent", ErrValidation)
	}
	if len(request.Title) > 200 || len(request.Summary) > 2000 || len(request.Section) > 80 || len(request.Project) > 120 || len(request.Repository) > 300 {
		return model.Task{}, fmt.Errorf("%w: task text is too long", ErrValidation)
	}
	if err := validatePlanning(request.Priority, request.DueDate, request.DeferUntil, request.Recurrence); err != nil {
		return model.Task{}, err
	}
	if len(request.Checklist) > 100 {
		return model.Task{}, fmt.Errorf("%w: checklist is limited to 100 items", ErrValidation)
	}
	for index := range request.Checklist {
		request.Checklist[index] = strings.TrimSpace(request.Checklist[index])
		if request.Checklist[index] == "" || len(request.Checklist[index]) > 300 {
			return model.Task{}, fmt.Errorf("%w: checklist items must contain 1-300 characters", ErrValidation)
		}
	}

	now := time.Now().UTC()
	taskID := newID(now)
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.Task{}, err
	}
	defer func() { _ = tx.Rollback() }()
	sortOrder, err := nextSortOrder(ctx, tx, request.Section)
	if err != nil {
		return model.Task{}, err
	}
	creator := defaultActor(actor, "user")
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(id,title,summary,task_type,visibility,created_by,section,project,repository,priority,due_date,defer_until,recurrence,sort_order,status,version,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, taskID, request.Title, request.Summary, request.Type, request.Visibility, creator, request.Section, request.Project, request.Repository, request.Priority, request.DueDate, request.DeferUntil, request.Recurrence, sortOrder, model.TaskQueued, stamp(now), stamp(now)); err != nil {
		return model.Task{}, err
	}
	for index, label := range request.Checklist {
		if _, err := tx.ExecContext(ctx, `INSERT INTO checklist_items(id,task_id,label,status,position,required,updated_at) VALUES(?,?,?,?,?,1,?)`, newID(now), taskID, label, model.ItemTodo, index, stamp(now)); err != nil {
			return model.Task{}, err
		}
	}
	event := model.Event{ID: newID(now), TaskID: taskID, Kind: "task.created", Actor: creator, Message: request.Title, Payload: map[string]any{"status": model.TaskQueued, "section": request.Section, "type": request.Type, "visibility": request.Visibility}, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, event); err != nil {
		return model.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Task{}, err
	}
	task, err := s.store.GetTask(ctx, taskID)
	if err == nil {
		s.publish(event)
	}
	return task, err
}

func (s *Service) CreateFor(ctx context.Context, request model.CreateRequest, principal Principal) (model.Task, error) {
	if principal.Agent {
		if request.Visibility == "" {
			request.Visibility = model.VisibilityAgent
		}
		if request.Visibility != model.VisibilityAgent {
			return model.Task{}, ErrForbidden
		}
	} else if request.Visibility == "" {
		request.Visibility = model.VisibilityPrivate
	}
	return s.Create(ctx, request, principal.ID)
}

func (s *Service) SaveTemplate(ctx context.Context, request model.TemplateRequest) (model.Template, error) {
	request.Name = strings.TrimSpace(request.Name)
	request.Title = strings.TrimSpace(request.Title)
	request.Type = normalizedTaskType(request.Type)
	request.Summary = strings.TrimSpace(request.Summary)
	request.Section = normalizedSection(request.Section)
	request.Project = strings.TrimSpace(request.Project)
	request.Repository = strings.TrimSpace(request.Repository)
	request.Priority = normalizedPriority(request.Priority)
	request.Recurrence = normalizedRecurrence(request.Recurrence)
	if !model.IsTaskType(request.Type) {
		return model.Template{}, fmt.Errorf("%w: type must be personal or work", ErrValidation)
	}
	if request.Name == "" || request.Title == "" || len(request.Name) > 80 || len(request.Title) > 200 || len(request.Summary) > 2000 || len(request.Section) > 80 || len(request.Project) > 120 || len(request.Repository) > 300 {
		return model.Template{}, fmt.Errorf("%w: template name and title are required and text must fit field limits", ErrValidation)
	}
	if err := validatePlanning(request.Priority, "", "", request.Recurrence); err != nil {
		return model.Template{}, err
	}
	if len(request.Checklist) > 100 {
		return model.Template{}, fmt.Errorf("%w: checklist is limited to 100 items", ErrValidation)
	}
	for index := range request.Checklist {
		request.Checklist[index] = strings.TrimSpace(request.Checklist[index])
		if request.Checklist[index] == "" || len(request.Checklist[index]) > 300 {
			return model.Template{}, fmt.Errorf("%w: checklist items must contain 1-300 characters", ErrValidation)
		}
	}
	encoded, err := json.Marshal(request.Checklist)
	if err != nil {
		return model.Template{}, err
	}
	now := time.Now().UTC()
	_, err = s.store.DB().ExecContext(ctx, `INSERT INTO task_templates(id,name,title,summary,task_type,section,project,repository,priority,recurrence,checklist_json,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET title=excluded.title,summary=excluded.summary,task_type=excluded.task_type,section=excluded.section,project=excluded.project,repository=excluded.repository,priority=excluded.priority,recurrence=excluded.recurrence,checklist_json=excluded.checklist_json,updated_at=excluded.updated_at`, newID(now), request.Name, request.Title, request.Summary, request.Type, request.Section, request.Project, request.Repository, request.Priority, request.Recurrence, string(encoded), stamp(now), stamp(now))
	if err != nil {
		return model.Template{}, err
	}
	return s.templateByName(ctx, request.Name)
}

func (s *Service) ListTemplates(ctx context.Context) ([]model.Template, error) {
	rows, err := s.store.DB().QueryContext(ctx, `SELECT id,name,title,summary,task_type,section,project,repository,priority,recurrence,checklist_json,created_at,updated_at FROM task_templates ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []model.Template{}
	for rows.Next() {
		item, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) DeleteTemplate(ctx context.Context, id string) error {
	result, err := s.store.DB().ExecContext(ctx, `DELETE FROM task_templates WHERE id=?`, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Service) templateByName(ctx context.Context, name string) (model.Template, error) {
	return scanTemplate(s.store.DB().QueryRowContext(ctx, `SELECT id,name,title,summary,task_type,section,project,repository,priority,recurrence,checklist_json,created_at,updated_at FROM task_templates WHERE name=?`, name))
}

type templateScanner interface{ Scan(...any) error }

func scanTemplate(scanner templateScanner) (model.Template, error) {
	var item model.Template
	var checklist, created, updated string
	if err := scanner.Scan(&item.ID, &item.Name, &item.Title, &item.Summary, &item.Type, &item.Section, &item.Project, &item.Repository, &item.Priority, &item.Recurrence, &checklist, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Template{}, store.ErrNotFound
		}
		return model.Template{}, err
	}
	if err := json.Unmarshal([]byte(checklist), &item.Checklist); err != nil {
		return model.Template{}, err
	}
	item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return item, nil
}

func (s *Service) Get(ctx context.Context, id string) (model.Task, error) {
	return s.store.GetTask(ctx, strings.TrimSpace(id))
}

func (s *Service) GetFor(ctx context.Context, id string, principal Principal) (model.Task, error) {
	task, err := s.Get(ctx, id)
	if err != nil {
		return model.Task{}, err
	}
	if !CanView(task, principal) {
		return model.Task{}, store.ErrNotFound
	}
	return task, nil
}

func (s *Service) List(ctx context.Context, statuses []model.TaskStatus, limit int) ([]model.Task, error) {
	for _, status := range statuses {
		if !model.IsTaskStatus(status) {
			return nil, fmt.Errorf("%w: unknown status %q", ErrValidation, status)
		}
	}
	return s.store.ListTasks(ctx, statuses, limit)
}

func (s *Service) ListFor(ctx context.Context, statuses []model.TaskStatus, limit int, principal Principal) ([]model.Task, error) {
	for _, status := range statuses {
		if !model.IsTaskStatus(status) {
			return nil, fmt.Errorf("%w: unknown status %q", ErrValidation, status)
		}
	}
	return s.store.ListVisibleTasks(ctx, statuses, limit, principal.ID, principal.Agent)
}

func (s *Service) ClaimFor(ctx context.Context, taskID string, request model.ClaimRequest, principal Principal) (model.StartResult, error) {
	if !principal.Agent {
		return model.StartResult{}, ErrForbidden
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.StartResult{}, err
	}
	if task.Status != model.TaskQueued && task.Status != model.TaskStale {
		return model.StartResult{}, fmt.Errorf("%w: only queued or stale work can be claimed", ErrValidation)
	}
	if task.Visibility == model.VisibilityTeam && task.Owner != principal.ID {
		return model.StartResult{}, ErrForbidden
	}
	request.Agent = principal.ID
	return s.Claim(ctx, taskID, request, principal.ID)
}

func (s *Service) Claim(ctx context.Context, taskID string, request model.ClaimRequest, actor string) (model.StartResult, error) {
	taskID = strings.TrimSpace(taskID)
	request.Agent = strings.TrimSpace(request.Agent)
	request.Client = strings.TrimSpace(request.Client)
	if taskID == "" || request.ExpectedVersion < 1 {
		return model.StartResult{}, fmt.Errorf("%w: task_id and expected_version are required", ErrValidation)
	}
	if request.Agent == "" {
		request.Agent = defaultActor(actor, "unassigned")
	}

	now := time.Now().UTC()
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.StartResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := store.LoadTask(ctx, tx, taskID)
	if err != nil {
		return model.StartResult{}, err
	}
	if current.Version != request.ExpectedVersion {
		return model.StartResult{}, ErrConflict
	}
	if current.Status == model.TaskDone || current.Status == model.TaskCancelled {
		return model.StartResult{}, fmt.Errorf("%w: terminal tasks cannot be claimed", ErrValidation)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE checklist_items SET status=?,updated_at=?
		WHERE id=(SELECT id FROM checklist_items WHERE task_id=? AND status=? ORDER BY position LIMIT 1)
		AND NOT EXISTS (SELECT 1 FROM checklist_items WHERE task_id=? AND status=?)`, model.ItemActive, stamp(now), taskID, model.ItemTodo, taskID, model.ItemActive); err != nil {
		return model.StartResult{}, err
	}
	runID := newID(now)
	lease := now.Add(s.leaseDuration)
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_runs(id,task_id,agent,client,status,lease_expires_at,last_heartbeat_at,started_at) VALUES(?,?,?,?,?,?,?,?)`, runID, taskID, request.Agent, request.Client, model.TaskActive, stamp(lease), stamp(now), stamp(now)); err != nil {
		return model.StartResult{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,owner=?,version=version+1,updated_at=?,completed_at=NULL WHERE id=? AND version=?`, model.TaskActive, request.Agent, stamp(now), taskID, request.ExpectedVersion)
	if err != nil {
		return model.StartResult{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return model.StartResult{}, ErrConflict
	}
	event := model.Event{ID: newID(now), TaskID: taskID, RunID: runID, Kind: "task.claimed", Actor: request.Agent, Message: "Task claimed", Payload: map[string]any{"status": model.TaskActive, "version": request.ExpectedVersion + 1}, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, event); err != nil {
		return model.StartResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.StartResult{}, err
	}
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return model.StartResult{}, err
	}
	s.publish(event)
	for _, run := range task.Runs {
		if run.ID == runID {
			return model.StartResult{Task: task, Run: run}, nil
		}
	}
	return model.StartResult{}, store.ErrNotFound
}

func (s *Service) Update(ctx context.Context, taskID string, request model.UpdateRequest, actor string) (model.Task, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || request.ExpectedVersion < 1 {
		return model.Task{}, fmt.Errorf("%w: task_id and expected_version are required", ErrValidation)
	}
	if request.Status != "" && !model.IsTaskStatus(request.Status) {
		return model.Task{}, fmt.Errorf("%w: unknown task status", ErrValidation)
	}
	if request.Type != nil {
		value := model.TaskType(strings.ToLower(strings.TrimSpace(string(*request.Type))))
		if !model.IsTaskType(value) {
			return model.Task{}, fmt.Errorf("%w: type must be personal or work", ErrValidation)
		}
		request.Type = &value
	}
	if request.Visibility != nil {
		value := model.TaskVisibility(strings.ToLower(strings.TrimSpace(string(*request.Visibility))))
		if !model.IsTaskVisibility(value) {
			return model.Task{}, fmt.Errorf("%w: visibility must be private, team, or agent", ErrValidation)
		}
		request.Visibility = &value
	}
	if request.Status == model.TaskBlocked && (request.Blocker == nil || strings.TrimSpace(*request.Blocker) == "") {
		return model.Task{}, fmt.Errorf("%w: blocked status requires blocker", ErrValidation)
	}
	if request.Status == model.TaskWaiting && (request.WaitingFor == nil || strings.TrimSpace(*request.WaitingFor) == "") {
		return model.Task{}, fmt.Errorf("%w: waiting status requires waiting_for", ErrValidation)
	}
	if len(request.SkipItemIDs) > 0 && strings.TrimSpace(request.SkipReason) == "" {
		return model.Task{}, fmt.Errorf("%w: skipped items require skip_reason", ErrValidation)
	}
	if len(request.AddItems) > 50 {
		return model.Task{}, fmt.Errorf("%w: add_items is limited to 50", ErrValidation)
	}
	if request.Checklist != nil {
		if len(*request.Checklist) > 100 {
			return model.Task{}, fmt.Errorf("%w: checklist is limited to 100 items", ErrValidation)
		}
		for index := range *request.Checklist {
			(*request.Checklist)[index] = strings.TrimSpace((*request.Checklist)[index])
			if (*request.Checklist)[index] == "" || len((*request.Checklist)[index]) > 300 {
				return model.Task{}, fmt.Errorf("%w: checklist items must contain 1-300 characters", ErrValidation)
			}
		}
	}
	if request.Owner != nil {
		value := strings.TrimSpace(*request.Owner)
		if len(value) > 100 {
			return model.Task{}, fmt.Errorf("%w: owner is limited to 100 characters", ErrValidation)
		}
		request.Owner = &value
	}
	if request.Section != nil {
		value := normalizedSection(*request.Section)
		if len(value) > 80 {
			return model.Task{}, fmt.Errorf("%w: section is limited to 80 characters", ErrValidation)
		}
		request.Section = &value
	}
	if request.Title != nil {
		value := strings.TrimSpace(*request.Title)
		if value == "" || len(value) > 200 {
			return model.Task{}, fmt.Errorf("%w: title must contain 1-200 characters", ErrValidation)
		}
		request.Title = &value
	}
	for name, target := range map[string]**string{
		"summary": &request.Summary, "project": &request.Project, "repository": &request.Repository,
		"due_date": &request.DueDate, "defer_until": &request.DeferUntil, "recurrence": &request.Recurrence,
	} {
		if *target == nil {
			continue
		}
		value := strings.TrimSpace(**target)
		if name == "recurrence" {
			value = normalizedRecurrence(value)
		}
		*target = &value
	}
	if request.Summary != nil && len(*request.Summary) > 2000 || request.Project != nil && len(*request.Project) > 120 || request.Repository != nil && len(*request.Repository) > 300 {
		return model.Task{}, fmt.Errorf("%w: task text is too long", ErrValidation)
	}
	if request.Priority != nil {
		value := normalizedPriority(*request.Priority)
		request.Priority = &value
	}
	priority, dueDate, deferUntil, recurrence := model.PriorityNormal, "", "", ""
	if request.Priority != nil {
		priority = *request.Priority
	}
	if request.DueDate != nil {
		dueDate = *request.DueDate
	}
	if request.DeferUntil != nil {
		deferUntil = *request.DeferUntil
	}
	if request.Recurrence != nil {
		recurrence = *request.Recurrence
	}
	if err := validatePlanning(priority, dueDate, deferUntil, recurrence); err != nil {
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
	if request.Visibility != nil && *request.Visibility != current.Visibility {
		var activeRuns int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_runs WHERE task_id=? AND status=? AND ended_at IS NULL`, taskID, model.TaskActive).Scan(&activeRuns); err != nil {
			return model.Task{}, err
		}
		if activeRuns > 0 {
			return model.Task{}, fmt.Errorf("%w: visibility cannot change while an agent run is active", ErrValidation)
		}
		if *request.Visibility == model.VisibilityPrivate && (current.CreatedBy == "" || current.CreatedBy != strings.TrimSpace(actor)) {
			return model.Task{}, ErrForbidden
		}
		if *request.Visibility == model.VisibilityAgent && current.Status != model.TaskQueued && current.Status != model.TaskStale && current.Status != model.TaskDone && current.Status != model.TaskCancelled {
			return model.Task{}, fmt.Errorf("%w: only queued, stale, or terminal work can move to agent pickup", ErrValidation)
		}
	}
	resultingVisibility := current.Visibility
	if request.Visibility != nil {
		resultingVisibility = *request.Visibility
	}
	if resultingVisibility == model.VisibilityAgent && request.Owner != nil && *request.Owner != "" {
		return model.Task{}, fmt.Errorf("%w: agent-pickup ownership is set by claim", ErrValidation)
	}
	if request.Checklist != nil {
		var runCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_runs WHERE task_id=?`, taskID).Scan(&runCount); err != nil {
			return model.Task{}, err
		}
		if current.Status != model.TaskQueued || runCount > 0 {
			return model.Task{}, fmt.Errorf("%w: checklist replacement is limited to queued tasks without runs", ErrValidation)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM checklist_items WHERE task_id=?`, taskID); err != nil {
			return model.Task{}, err
		}
		for position, label := range *request.Checklist {
			if _, err := tx.ExecContext(ctx, `INSERT INTO checklist_items(id,task_id,label,status,position,required,updated_at) VALUES(?,?,?,?,?,1,?)`, newID(now), taskID, label, model.ItemTodo, position, stamp(now)); err != nil {
				return model.Task{}, err
			}
		}
	}

	completedItemIDs := make([]string, 0, len(request.CompleteItemIDs))
	for _, id := range unique(request.CompleteItemIDs) {
		result, err := tx.ExecContext(ctx, `UPDATE checklist_items SET status=?,note='',updated_at=? WHERE id=? AND task_id=? AND status<>?`, model.ItemDone, stamp(now), id, taskID, model.ItemDone)
		if err != nil {
			return model.Task{}, err
		}
		if count, _ := result.RowsAffected(); count == 1 {
			completedItemIDs = append(completedItemIDs, id)
			continue
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM checklist_items WHERE id=? AND task_id=?`, id, taskID).Scan(&exists); err != nil {
			return model.Task{}, err
		}
		if exists == 0 {
			return model.Task{}, fmt.Errorf("%w: checklist item %q not found", ErrValidation, id)
		}
	}
	for _, id := range unique(request.SkipItemIDs) {
		result, err := tx.ExecContext(ctx, `UPDATE checklist_items SET status=?,note=?,updated_at=? WHERE id=? AND task_id=?`, model.ItemSkipped, strings.TrimSpace(request.SkipReason), stamp(now), id, taskID)
		if err != nil {
			return model.Task{}, err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return model.Task{}, fmt.Errorf("%w: checklist item %q not found", ErrValidation, id)
		}
	}
	if request.CurrentItemID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE checklist_items SET status=?,updated_at=? WHERE task_id=? AND status=?`, model.ItemTodo, stamp(now), taskID, model.ItemActive); err != nil {
			return model.Task{}, err
		}
		result, err := tx.ExecContext(ctx, `UPDATE checklist_items SET status=?,updated_at=? WHERE id=? AND task_id=? AND status NOT IN (?,?)`, model.ItemActive, stamp(now), request.CurrentItemID, taskID, model.ItemDone, model.ItemSkipped)
		if err != nil {
			return model.Task{}, err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return model.Task{}, fmt.Errorf("%w: current checklist item is missing or terminal", ErrValidation)
		}
	}
	var position int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),-1)+1 FROM checklist_items WHERE task_id=?`, taskID).Scan(&position); err != nil {
		return model.Task{}, err
	}
	for _, label := range request.AddItems {
		label = strings.TrimSpace(label)
		if label == "" || len(label) > 300 {
			return model.Task{}, fmt.Errorf("%w: added checklist items must contain 1-300 characters", ErrValidation)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO checklist_items(id,task_id,label,status,position,required,updated_at) VALUES(?,?,?,?,?,1,?)`, newID(now), taskID, label, model.ItemTodo, position, stamp(now)); err != nil {
			return model.Task{}, err
		}
		position++
	}
	status := current.Status
	if request.Status != "" {
		status = request.Status
	}
	if status == model.TaskDone {
		var remaining int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM checklist_items WHERE task_id=? AND required=1 AND status NOT IN (?,?)`, taskID, model.ItemDone, model.ItemSkipped).Scan(&remaining); err != nil {
			return model.Task{}, err
		}
		if remaining > 0 {
			return model.Task{}, fmt.Errorf("%w: %d required checklist items remain open", ErrValidation, remaining)
		}
	}
	if status == model.TaskActive && current.Status != model.TaskActive && request.CurrentItemID == "" {
		if _, err := tx.ExecContext(ctx, `UPDATE checklist_items SET status=?,updated_at=?
			WHERE id=(SELECT id FROM checklist_items WHERE task_id=? AND status=? ORDER BY position LIMIT 1)
			AND NOT EXISTS (SELECT 1 FROM checklist_items WHERE task_id=? AND status=?)`, model.ItemActive, stamp(now), taskID, model.ItemTodo, taskID, model.ItemActive); err != nil {
			return model.Task{}, err
		}
	}
	title, summary, taskType := current.Title, current.Summary, current.Type
	visibility := current.Visibility
	project, repository := current.Project, current.Repository
	priority = current.Priority
	dueDate, deferUntil, recurrence = current.DueDate, current.DeferUntil, current.Recurrence
	if request.Title != nil {
		title = *request.Title
	}
	if request.Summary != nil {
		summary = *request.Summary
	}
	if request.Type != nil {
		taskType = *request.Type
	}
	if request.Visibility != nil {
		visibility = *request.Visibility
	}
	if request.Project != nil {
		project = *request.Project
	}
	if request.Repository != nil {
		repository = *request.Repository
	}
	if request.Priority != nil {
		priority = *request.Priority
	}
	if request.DueDate != nil {
		dueDate = *request.DueDate
	}
	if request.DeferUntil != nil {
		deferUntil = *request.DeferUntil
	}
	if request.Recurrence != nil {
		recurrence = *request.Recurrence
	}
	currentNote, blocker, waitingFor := current.CurrentNote, current.Blocker, current.WaitingFor
	owner, section := current.Owner, current.Section
	if request.Owner != nil {
		owner = *request.Owner
	}
	if request.Visibility != nil && *request.Visibility != current.Visibility {
		if visibility == model.VisibilityAgent || visibility == model.VisibilityPrivate && owner != current.CreatedBy {
			owner = ""
		}
	}
	if request.Section != nil {
		section = *request.Section
	}
	sortOrder := current.SortOrder
	if section != current.Section {
		sortOrder, err = nextSortOrder(ctx, tx, section)
		if err != nil {
			return model.Task{}, err
		}
	}
	if request.CurrentNote != nil {
		currentNote = clipped(*request.CurrentNote, 1000)
	}
	if request.Blocker != nil {
		blocker = clipped(*request.Blocker, 1000)
	}
	if request.WaitingFor != nil {
		waitingFor = clipped(*request.WaitingFor, 1000)
	}
	if status != model.TaskBlocked {
		blocker = ""
	}
	if status != model.TaskWaiting {
		waitingFor = ""
	}
	var completed any
	if current.CompletedAt != nil {
		completed = stamp(*current.CompletedAt)
	}
	if (status == model.TaskDone || status == model.TaskCancelled) && current.Status != model.TaskDone && current.Status != model.TaskCancelled {
		completed = stamp(now)
	}
	if status != model.TaskDone && status != model.TaskCancelled {
		completed = nil
	}
	var reviewed any
	if current.ReviewedAt != nil {
		reviewed = stamp(*current.ReviewedAt)
	}
	if request.Reviewed {
		reviewed = stamp(now)
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET title=?,summary=?,task_type=?,visibility=?,status=?,owner=?,section=?,project=?,repository=?,priority=?,due_date=?,defer_until=?,recurrence=?,sort_order=?,reviewed_at=?,current_note=?,blocker=?,waiting_for=?,version=version+1,updated_at=?,completed_at=? WHERE id=? AND version=?`, title, summary, taskType, visibility, status, owner, section, project, repository, priority, dueDate, deferUntil, recurrence, sortOrder, reviewed, currentNote, blocker, waitingFor, stamp(now), completed, taskID, request.ExpectedVersion)
	if err != nil {
		return model.Task{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return model.Task{}, ErrConflict
	}
	var recurringEvent *model.Event
	if status == model.TaskDone && current.Status != model.TaskDone && recurrence != "" {
		nextID := newID(now)
		nextDue, err := nextRecurringDate(dueDate, recurrence, now)
		if err != nil {
			return model.Task{}, err
		}
		nextOrder, err := nextSortOrder(ctx, tx, section)
		if err != nil {
			return model.Task{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(id,title,summary,task_type,visibility,created_by,section,project,repository,priority,due_date,defer_until,recurrence,sort_order,status,version,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, nextID, title, summary, taskType, visibility, current.CreatedBy, section, project, repository, priority, nextDue, "", recurrence, nextOrder, model.TaskQueued, stamp(now), stamp(now)); err != nil {
			return model.Task{}, err
		}
		rows, err := tx.QueryContext(ctx, `SELECT label,required FROM checklist_items WHERE task_id=? ORDER BY position`, taskID)
		if err != nil {
			return model.Task{}, err
		}
		type recurringItem struct {
			label    string
			required int
		}
		var recurringItems []recurringItem
		for rows.Next() {
			var item recurringItem
			if err := rows.Scan(&item.label, &item.required); err != nil {
				return model.Task{}, errors.Join(err, rows.Close())
			}
			recurringItems = append(recurringItems, item)
		}
		if err := rows.Err(); err != nil {
			return model.Task{}, errors.Join(err, rows.Close())
		}
		if err := rows.Close(); err != nil {
			return model.Task{}, err
		}
		for position, item := range recurringItems {
			if _, err := tx.ExecContext(ctx, `INSERT INTO checklist_items(id,task_id,label,status,position,required,updated_at) VALUES(?,?,?,?,?,?,?)`, newID(now), nextID, item.label, model.ItemTodo, position, item.required, stamp(now)); err != nil {
				return model.Task{}, err
			}
		}
		event := model.Event{ID: newID(now), TaskID: nextID, Kind: "task.recurring_created", Actor: defaultActor(actor, current.Owner), Message: title, Payload: map[string]any{"status": model.TaskQueued, "source_task_id": taskID, "due_date": nextDue}, CreatedAt: now}
		if err := store.InsertEvent(ctx, tx, event); err != nil {
			return model.Task{}, err
		}
		recurringEvent = &event
	}
	if request.RunID != "" {
		runStatus := status
		var ended any
		if status == model.TaskDone || status == model.TaskCancelled {
			ended = stamp(now)
		}
		result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status=?,lease_expires_at=?,last_heartbeat_at=?,ended_at=? WHERE id=? AND task_id=?`, runStatus, stamp(now.Add(s.leaseDuration)), stamp(now), ended, request.RunID, taskID)
		if err != nil {
			return model.Task{}, err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return model.Task{}, fmt.Errorf("%w: run not found", ErrValidation)
		}
	}
	payload := map[string]any{"status": status, "version": request.ExpectedVersion + 1}
	if request.Owner != nil {
		payload["owner"] = owner
	}
	if request.Section != nil {
		payload["section"] = section
	}
	if request.Type != nil {
		payload["type"] = taskType
	}
	if request.Visibility != nil {
		payload["visibility"] = visibility
	}
	if len(completedItemIDs) > 0 {
		payload["completed_item_ids"] = completedItemIDs
	}
	event := model.Event{ID: newID(now), TaskID: taskID, RunID: request.RunID, Kind: "task.updated", Actor: defaultActor(actor, current.Owner), Message: currentNote, Payload: payload, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, event); err != nil {
		return model.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Task{}, err
	}
	task, err := s.store.GetTask(ctx, taskID)
	if err == nil {
		s.publish(event)
		if recurringEvent != nil {
			s.publish(*recurringEvent)
		}
	}
	return task, err
}

func (s *Service) UpdateFor(ctx context.Context, taskID string, request model.UpdateRequest, principal Principal) (model.Task, error) {
	current, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.Task{}, err
	}
	if !canMutate(current, principal) {
		return model.Task{}, ErrForbidden
	}
	if principal.Agent && request.Visibility != nil {
		return model.Task{}, ErrForbidden
	}
	if principal.Agent && request.Owner != nil && strings.TrimSpace(*request.Owner) != principal.ID {
		return model.Task{}, ErrForbidden
	}
	return s.Update(ctx, taskID, request, principal.ID)
}

func (s *Service) Heartbeat(ctx context.Context, taskID, runID, actor string) (model.AgentRun, error) {
	now := time.Now().UTC()
	result, err := s.store.DB().ExecContext(ctx, `UPDATE agent_runs SET lease_expires_at=?,last_heartbeat_at=? WHERE id=? AND task_id=? AND ended_at IS NULL AND status=?`, stamp(now.Add(s.leaseDuration)), stamp(now), strings.TrimSpace(runID), strings.TrimSpace(taskID), model.TaskActive)
	if err != nil {
		return model.AgentRun{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return model.AgentRun{}, store.ErrNotFound
	}
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return model.AgentRun{}, err
	}
	for _, run := range task.Runs {
		if run.ID == runID {
			return run, nil
		}
	}
	return model.AgentRun{}, store.ErrNotFound
}

func (s *Service) HeartbeatFor(ctx context.Context, taskID, runID string, principal Principal) (model.AgentRun, error) {
	if !principal.Agent {
		return model.AgentRun{}, ErrForbidden
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.AgentRun{}, err
	}
	for _, run := range task.Runs {
		if run.ID == strings.TrimSpace(runID) && run.Agent == principal.ID {
			return s.Heartbeat(ctx, taskID, runID, principal.ID)
		}
	}
	return model.AgentRun{}, store.ErrNotFound
}

func (s *Service) Move(ctx context.Context, taskID string, request model.MoveRequest, actor string) (model.Task, error) {
	taskID = strings.TrimSpace(taskID)
	request.Direction = strings.ToLower(strings.TrimSpace(request.Direction))
	if taskID == "" || request.ExpectedVersion < 1 || request.Direction != "up" && request.Direction != "down" {
		return model.Task{}, fmt.Errorf("%w: task_id, expected_version, and direction up or down are required", ErrValidation)
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
	if current.Status == model.TaskDone || current.Status == model.TaskCancelled {
		return model.Task{}, fmt.Errorf("%w: terminal tasks cannot be reordered", ErrValidation)
	}
	operator, ordering := "<", "DESC"
	if request.Direction == "down" {
		operator, ordering = ">", "ASC"
	}
	var neighborID string
	var neighborOrder int64
	err = tx.QueryRowContext(ctx, `SELECT id,sort_order FROM tasks WHERE section=? AND id<>? AND status NOT IN (?,?) AND sort_order `+operator+` ? ORDER BY sort_order `+ordering+`,updated_at `+ordering+` LIMIT 1`, current.Section, taskID, model.TaskDone, model.TaskCancelled, current.SortOrder).Scan(&neighborID, &neighborOrder)
	if errors.Is(err, sql.ErrNoRows) {
		return current, nil
	}
	if err != nil {
		return model.Task{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET sort_order=?,updated_at=? WHERE id=?`, current.SortOrder, stamp(now), neighborID); err != nil {
		return model.Task{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET sort_order=?,version=version+1,updated_at=? WHERE id=? AND version=?`, neighborOrder, stamp(now), taskID, request.ExpectedVersion)
	if err != nil {
		return model.Task{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return model.Task{}, ErrConflict
	}
	event := model.Event{ID: newID(now), TaskID: taskID, Kind: "task.moved", Actor: defaultActor(actor, current.Owner), Message: request.Direction, Payload: map[string]any{"version": request.ExpectedVersion + 1}, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, event); err != nil {
		return model.Task{}, err
	}
	neighborDirection := "down"
	if request.Direction == "down" {
		neighborDirection = "up"
	}
	neighborEvent := model.Event{ID: newID(now), TaskID: neighborID, Kind: "task.moved", Actor: defaultActor(actor, current.Owner), Message: neighborDirection, Payload: map[string]any{}, CreatedAt: now}
	if err := store.InsertEvent(ctx, tx, neighborEvent); err != nil {
		return model.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Task{}, err
	}
	task, err := s.store.GetTask(ctx, taskID)
	if err == nil {
		s.publish(event)
		s.publish(neighborEvent)
	}
	return task, err
}

func (s *Service) MoveFor(ctx context.Context, taskID string, request model.MoveRequest, principal Principal) (model.Task, error) {
	if principal.Agent {
		return model.Task{}, ErrForbidden
	}
	task, err := s.GetFor(ctx, taskID, principal)
	if err != nil {
		return model.Task{}, err
	}
	if !canMutate(task, principal) {
		return model.Task{}, ErrForbidden
	}
	return s.Move(ctx, taskID, request, principal.ID)
}

func (s *Service) SweepStale(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	result, err := s.store.DB().ExecContext(ctx, `UPDATE agent_runs SET status=? WHERE ended_at IS NULL AND status=? AND lease_expires_at < ?`, model.TaskStale, model.TaskActive, stamp(now))
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	_, err = s.store.DB().ExecContext(ctx, `UPDATE tasks SET status=?,version=version+1,updated_at=?
		WHERE status=?
		AND id IN (SELECT task_id FROM agent_runs WHERE status=?)
		AND id NOT IN (SELECT task_id FROM agent_runs WHERE ended_at IS NULL AND status=? AND lease_expires_at >= ?)`, model.TaskStale, stamp(now), model.TaskActive, model.TaskStale, model.TaskActive, stamp(now))
	return count, err
}

func newID(now time.Time) string   { return ulid.Make().String() }
func stamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func clipped(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}
func defaultActor(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}
func normalizedSection(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return model.DefaultSection
}
func normalizedPriority(value model.Priority) model.Priority {
	value = model.Priority(strings.ToLower(strings.TrimSpace(string(value))))
	if value == "" {
		return model.PriorityNormal
	}
	return value
}
func normalizedTaskType(value model.TaskType) model.TaskType {
	value = model.TaskType(strings.ToLower(strings.TrimSpace(string(value))))
	if value == "" {
		return model.TaskPersonal
	}
	return value
}
func normalizedVisibility(value, fallback model.TaskVisibility) model.TaskVisibility {
	value = model.TaskVisibility(strings.ToLower(strings.TrimSpace(string(value))))
	if value == "" {
		return fallback
	}
	return value
}
func normalizedRecurrence(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
func validatePlanning(priority model.Priority, dueDate, deferUntil, recurrence string) error {
	if !model.IsPriority(priority) {
		return fmt.Errorf("%w: priority must be low, normal, high, or urgent", ErrValidation)
	}
	for name, value := range map[string]string{"due_date": dueDate, "defer_until": deferUntil} {
		if value != "" {
			if _, err := time.Parse("2006-01-02", value); err != nil {
				return fmt.Errorf("%w: %s must use YYYY-MM-DD", ErrValidation, name)
			}
		}
	}
	if recurrence != "" && recurrence != "daily" && recurrence != "weekly" && recurrence != "monthly" {
		return fmt.Errorf("%w: recurrence must be daily, weekly, monthly, or empty", ErrValidation)
	}
	return nil
}
func nextSortOrder(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, section string) (int64, error) {
	var value int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order),0)+1024 FROM tasks WHERE section=?`, section).Scan(&value)
	return value, err
}
func nextRecurringDate(dueDate, recurrence string, now time.Time) (string, error) {
	base := now.UTC()
	if dueDate != "" {
		parsed, err := time.Parse("2006-01-02", dueDate)
		if err != nil {
			return "", err
		}
		base = parsed
	}
	switch recurrence {
	case "daily":
		base = base.AddDate(0, 0, 1)
	case "weekly":
		base = base.AddDate(0, 0, 7)
	case "monthly":
		base = base.AddDate(0, 1, 0)
	default:
		return "", fmt.Errorf("%w: unknown recurrence", ErrValidation)
	}
	return base.Format("2006-01-02"), nil
}
func unique(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
