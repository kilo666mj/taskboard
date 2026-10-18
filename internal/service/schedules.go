package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
	"github.com/kilo666mj/taskboard/internal/store"
	"github.com/robfig/cron/v3"
)

// minScheduleInterval is the shortest gap allowed between two runs of one
// schedule, so a mistyped expression cannot flood the pickup lane.
const minScheduleInterval = 5 * time.Minute

// maxDueSchedulesPerSweep bounds how many schedules one sweep fires.
const maxDueSchedulesPerSweep = 50

var scheduleParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

const scheduleColumns = `id,name,enabled,cron,timezone,overlap,title,summary,task_type,section,project,repository,priority,checklist_json,requirements_json,created_by,last_edited_by,next_run_at,last_run_at,last_outcome,last_task_id,version,created_at,updated_at`

// parseSchedule validates a cron expression in a timezone and returns its
// schedule. Timezones belong in the timezone field, not in the expression.
func parseSchedule(expression, timezone string, now time.Time) (cron.Schedule, *time.Location, error) {
	upper := strings.ToUpper(expression)
	if strings.HasPrefix(upper, "TZ=") || strings.HasPrefix(upper, "CRON_TZ=") {
		return nil, nil, fmt.Errorf("%w: set the timezone field instead of a TZ prefix", ErrValidation)
	}
	if strings.HasPrefix(strings.ToLower(expression), "@every") {
		return nil, nil, fmt.Errorf("%w: @every is not supported; use a cron expression", ErrValidation)
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: unknown timezone %q", ErrValidation, timezone)
	}
	schedule, err := scheduleParser.Parse(expression)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: cron: %v", ErrValidation, err)
	}
	if schedule.Next(now.In(location)).IsZero() {
		return nil, nil, fmt.Errorf("%w: cron expression never fires", ErrValidation)
	}
	return schedule, location, nil
}

// spacingWindow is how far validateScheduleSpacing looks. It starts from a
// fixed date, so the answer never depends on when a schedule is saved, and a
// year in the schedule's own timezone covers every daily, weekly and monthly
// pattern and both DST changes.
const spacingWindow = 366 * 24 * time.Hour

// validateScheduleSpacing rejects an expression with any two consecutive runs
// closer than minScheduleInterval.
func validateScheduleSpacing(schedule cron.Schedule, location *time.Location) error {
	start := time.Date(2025, time.January, 1, 0, 0, 0, 0, location)
	end := start.Add(spacingWindow)
	previous := schedule.Next(start)
	// Runs at least minScheduleInterval apart fit in the window this many times.
	for range int(spacingWindow/minScheduleInterval) + 1 {
		next := schedule.Next(previous)
		if next.IsZero() || next.After(end) {
			return nil
		}
		if next.Sub(previous) < minScheduleInterval {
			return fmt.Errorf("%w: runs must be at least %d minutes apart (%s then %s)", ErrValidation, int(minScheduleInterval.Minutes()), previous.Format("Jan 2 15:04"), next.Format("15:04"))
		}
		previous = next
	}
	return nil
}

// nextRun returns the first run strictly after now, in UTC.
func nextRun(schedule cron.Schedule, location *time.Location, now time.Time) time.Time {
	return schedule.Next(now.In(location)).UTC()
}

func (s *Service) normalizeScheduleRequest(request *model.ScheduleRequest, now time.Time) (time.Time, error) {
	request.Name = strings.TrimSpace(request.Name)
	request.Cron = strings.Join(strings.Fields(request.Cron), " ")
	request.Timezone = strings.TrimSpace(request.Timezone)
	if request.Timezone == "" {
		request.Timezone = s.store.DeferLocation().String()
	}
	if request.Overlap == "" {
		request.Overlap = model.ScheduleOverlapSkip
	}
	request.Title = strings.TrimSpace(request.Title)
	request.Type = s.taskType(request.Type)
	request.Summary = strings.TrimSpace(request.Summary)
	request.Section = normalizedSection(request.Section)
	request.Project = strings.TrimSpace(request.Project)
	request.Repository = strings.TrimSpace(request.Repository)
	request.Priority = normalizedPriority(request.Priority)
	if request.Name == "" || len(request.Name) > 80 {
		return time.Time{}, fmt.Errorf("%w: name must contain 1-80 characters", ErrValidation)
	}
	if request.Title == "" || len(request.Title) > 200 || len(request.Summary) > 2000 || len(request.Section) > 80 || len(request.Project) > 120 || len(request.Repository) > 300 {
		return time.Time{}, fmt.Errorf("%w: title is required and text must fit field limits", ErrValidation)
	}
	if !model.IsTaskType(request.Type) {
		return time.Time{}, fmt.Errorf("%w: type must be personal or work", ErrValidation)
	}
	if !model.IsPriority(request.Priority) {
		return time.Time{}, fmt.Errorf("%w: priority must be low, normal, high, or urgent", ErrValidation)
	}
	if request.Overlap != model.ScheduleOverlapSkip && request.Overlap != model.ScheduleOverlapAllow {
		return time.Time{}, fmt.Errorf("%w: overlap must be skip or allow", ErrValidation)
	}
	if len(request.Checklist) > 100 {
		return time.Time{}, fmt.Errorf("%w: checklist is limited to 100 items", ErrValidation)
	}
	for index := range request.Checklist {
		request.Checklist[index] = strings.TrimSpace(request.Checklist[index])
		if request.Checklist[index] == "" || len(request.Checklist[index]) > 300 {
			return time.Time{}, fmt.Errorf("%w: checklist items must contain 1-300 characters", ErrValidation)
		}
	}
	requirements, err := normalizeRequirements(request.Requirements)
	if err != nil {
		return time.Time{}, err
	}
	request.Requirements = requirements
	schedule, location, err := parseSchedule(request.Cron, request.Timezone, now)
	if err != nil {
		return time.Time{}, err
	}
	// Only when saving: a timed run never re-checks spacing, so an accepted
	// schedule cannot stop later.
	if err := validateScheduleSpacing(schedule, location); err != nil {
		return time.Time{}, err
	}
	return nextRun(schedule, location, now), nil
}

// canManageSchedules reports whether a principal may create, change, run or
// delete schedules. Occurrences carry the editor's provenance, so only people
// may manage them; agents are read-only.
func canManageSchedules(principal Principal) bool {
	return !principal.Agent && principal.Can(PermissionTaskWrite)
}

// ListSchedulesFor lists every schedule.
func (s *Service) ListSchedulesFor(ctx context.Context, principal Principal) ([]model.Schedule, error) {
	if !principal.Can(PermissionTaskRead) {
		return nil, ErrForbidden
	}
	rows, err := s.store.DB().QueryContext(ctx, `SELECT `+scheduleColumns+` FROM task_schedules ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []model.Schedule{}
	for rows.Next() {
		item, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// GetScheduleFor returns one schedule.
func (s *Service) GetScheduleFor(ctx context.Context, id string, principal Principal) (model.Schedule, error) {
	if !principal.Can(PermissionTaskRead) {
		return model.Schedule{}, ErrForbidden
	}
	return s.getSchedule(ctx, s.store.DB(), strings.TrimSpace(id))
}

// CreateScheduleFor creates a schedule. Its first run is the next time the
// cron expression fires after now.
func (s *Service) CreateScheduleFor(ctx context.Context, request model.ScheduleRequest, principal Principal) (model.Schedule, error) {
	if !canManageSchedules(principal) {
		return model.Schedule{}, ErrForbidden
	}
	now := s.store.Now()
	next, err := s.normalizeScheduleRequest(&request, now)
	if err != nil {
		return model.Schedule{}, err
	}
	enabled := request.Enabled == nil || *request.Enabled
	checklist, requirements, err := encodeScheduleLists(request)
	if err != nil {
		return model.Schedule{}, err
	}
	var nextValue any
	if enabled {
		nextValue = runStamp(next)
	}
	id := newID(now)
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.Schedule{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureScheduleNameFree(ctx, tx, request.Name, ""); err != nil {
		return model.Schedule{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_schedules(id,name,enabled,cron,timezone,overlap,title,summary,task_type,section,project,repository,priority,checklist_json,requirements_json,created_by,last_edited_by,next_run_at,version,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, id, request.Name, enabled, request.Cron, request.Timezone, request.Overlap, request.Title, request.Summary, request.Type, request.Section, request.Project, request.Repository, request.Priority, checklist, requirements, principal.ID, principal.ID, nextValue, stamp(now), stamp(now)); err != nil {
		return model.Schedule{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Schedule{}, err
	}
	return s.getSchedule(ctx, s.store.DB(), id)
}

// UpdateScheduleFor replaces a schedule's definition. The next run is
// recomputed from now, and the editor becomes the provenance of later
// occurrences.
func (s *Service) UpdateScheduleFor(ctx context.Context, id string, request model.ScheduleRequest, principal Principal) (model.Schedule, error) {
	if !canManageSchedules(principal) {
		return model.Schedule{}, ErrForbidden
	}
	if request.ExpectedVersion < 1 {
		return model.Schedule{}, fmt.Errorf("%w: expected_version is required", ErrValidation)
	}
	id = strings.TrimSpace(id)
	now := s.store.Now()
	next, err := s.normalizeScheduleRequest(&request, now)
	if err != nil {
		return model.Schedule{}, err
	}
	checklist, requirements, err := encodeScheduleLists(request)
	if err != nil {
		return model.Schedule{}, err
	}
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.Schedule{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.getSchedule(ctx, tx, id)
	if err != nil {
		return model.Schedule{}, err
	}
	if current.Version != request.ExpectedVersion {
		return model.Schedule{}, fmt.Errorf("%w: schedule version is %d", ErrConflict, current.Version)
	}
	if err := ensureScheduleNameFree(ctx, tx, request.Name, id); err != nil {
		return model.Schedule{}, err
	}
	enabled := current.Enabled
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	var nextValue any
	if enabled {
		nextValue = runStamp(next)
	}
	result, err := tx.ExecContext(ctx, `UPDATE task_schedules SET name=?,enabled=?,cron=?,timezone=?,overlap=?,title=?,summary=?,task_type=?,section=?,project=?,repository=?,priority=?,checklist_json=?,requirements_json=?,last_edited_by=?,next_run_at=?,version=version+1,updated_at=? WHERE id=? AND version=?`,
		request.Name, enabled, request.Cron, request.Timezone, request.Overlap, request.Title, request.Summary, request.Type, request.Section, request.Project, request.Repository, request.Priority, checklist, requirements, principal.ID, nextValue, stamp(now), id, request.ExpectedVersion)
	if err != nil {
		return model.Schedule{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return model.Schedule{}, fmt.Errorf("%w: schedule changed concurrently", ErrConflict)
	}
	if err := tx.Commit(); err != nil {
		return model.Schedule{}, err
	}
	return s.getSchedule(ctx, s.store.DB(), id)
}

// DeleteScheduleFor removes a schedule. Tasks it already created are kept.
func (s *Service) DeleteScheduleFor(ctx context.Context, id string, principal Principal) error {
	if !canManageSchedules(principal) {
		return ErrForbidden
	}
	result, err := s.store.DB().ExecContext(ctx, `DELETE FROM task_schedules WHERE id=?`, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return store.ErrNotFound
	}
	return nil
}

// RunScheduleNowFor fires a schedule immediately without moving its next
// run. The overlap policy still applies.
func (s *Service) RunScheduleNowFor(ctx context.Context, id string, principal Principal) (model.ScheduleRunResult, error) {
	if !canManageSchedules(principal) {
		return model.ScheduleRunResult{}, ErrForbidden
	}
	result, fired, err := s.fireSchedule(ctx, strings.TrimSpace(id), nil, principal.ID)
	if err == nil && !fired {
		err = fmt.Errorf("%w: schedule changed concurrently", ErrConflict)
	}
	return result, err
}

// RunDueSchedules fires every enabled schedule whose next run has passed. A
// schedule that missed several runs while Taskboard was down fires once and
// then resumes at its next future time; missed runs are not backfilled.
// Concurrent replicas are safe: a run is claimed by advancing next_run_at.
func (s *Service) RunDueSchedules(ctx context.Context) (int, error) {
	now := s.store.Now()
	rows, err := s.store.DB().QueryContext(ctx, `SELECT id,next_run_at FROM task_schedules WHERE enabled=? AND next_run_at IS NOT NULL AND next_run_at<=? ORDER BY next_run_at,id LIMIT ?`, true, runStamp(now), maxDueSchedulesPerSweep)
	if err != nil {
		return 0, err
	}
	type due struct{ id, nextRunAt string }
	var items []due
	for rows.Next() {
		var item due
		if err := rows.Scan(&item.id, &item.nextRunAt); err != nil {
			return 0, errors.Join(err, rows.Close())
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return 0, errors.Join(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	fired := 0
	var errs []error
	for _, item := range items {
		nextRunAt := item.nextRunAt
		_, ok, err := s.fireSchedule(ctx, item.id, &nextRunAt, "")
		if err != nil {
			errs = append(errs, fmt.Errorf("schedule %s: %w", item.id, err))
			continue
		}
		if ok {
			fired++
		}
	}
	return fired, errors.Join(errs...)
}

// fireSchedule runs one schedule. When expectedNext is set, the run is a
// timed one: it only proceeds if next_run_at still equals expectedNext, and it
// advances next_run_at. It reports false when another caller fired first.
func (s *Service) fireSchedule(ctx context.Context, id string, expectedNext *string, runBy string) (model.ScheduleRunResult, bool, error) {
	now := s.store.Now()
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return model.ScheduleRunResult{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	// Serialize every firing of this schedule, timed or manual: the row lock
	// (PostgreSQL) or write lock (SQLite) is held from here to commit, so the
	// open-occurrence check below sees an occurrence another run committed. A
	// timed run also claims its slot here, before doing any work.
	lock, lockArgs := `UPDATE task_schedules SET version=version WHERE id=?`, []any{id}
	if expectedNext != nil {
		lock += ` AND next_run_at=?`
		lockArgs = append(lockArgs, *expectedNext)
	}
	locked, err := tx.ExecContext(ctx, lock, lockArgs...)
	if err != nil {
		return model.ScheduleRunResult{}, false, err
	}
	if count, _ := locked.RowsAffected(); count != 1 && expectedNext != nil {
		return model.ScheduleRunResult{}, false, nil // another caller fired this run
	}
	schedule, err := s.getSchedule(ctx, tx, id)
	if err != nil {
		return model.ScheduleRunResult{}, false, err
	}
	scheduledFor := now
	var nextValue any
	if expectedNext != nil {
		if !schedule.Enabled {
			return model.ScheduleRunResult{}, false, nil
		}
		if parsed, err := time.Parse(time.RFC3339Nano, *expectedNext); err == nil {
			scheduledFor = parsed
		}
		parsed, location, err := parseSchedule(schedule.Cron, schedule.Timezone, now)
		if err != nil {
			// A definition that no longer parses (a removed timezone, say)
			// stops instead of firing on every sweep, and records why.
			if _, err := tx.ExecContext(ctx, `UPDATE task_schedules SET last_run_at=?,last_outcome=?,last_task_id=?,next_run_at=NULL WHERE id=?`, stamp(now), model.ScheduleOutcomeStopped, "", schedule.ID); err != nil {
				return model.ScheduleRunResult{}, false, err
			}
			return model.ScheduleRunResult{}, false, tx.Commit()
		}
		nextValue = runStamp(nextRun(parsed, location, now))
	}

	outcome, taskID := model.ScheduleOutcomeCreated, ""
	var openTaskID string
	if schedule.Overlap == model.ScheduleOverlapSkip {
		// Filtering on open statuses first uses idx_tasks_status_updated.
		err := tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE status IN (?,?,?,?,?) AND schedule_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, model.TaskQueued, model.TaskActive, model.TaskWaiting, model.TaskBlocked, model.TaskStale, schedule.ID).Scan(&openTaskID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return model.ScheduleRunResult{}, false, err
		}
	}
	var event model.Event
	if openTaskID != "" {
		outcome, taskID = model.ScheduleOutcomeSkipped, openTaskID
		event = model.Event{ID: newID(now), TaskID: openTaskID, Kind: "schedule.skipped", Actor: scheduleActor(schedule, runBy), Message: "Skipped a scheduled run because this occurrence is still open", Payload: map[string]any{"schedule_id": schedule.ID, "schedule_name": schedule.Name, "scheduled_for": stamp(scheduledFor)}, CreatedAt: now}
		if err := store.InsertEvent(ctx, tx, event); err != nil {
			return model.ScheduleRunResult{}, false, err
		}
	} else {
		requirements, source := schedule.Requirements, "schedule"
		if len(requirements) == 0 && len(s.defaultRequirements) > 0 {
			requirements, source = s.defaultRequirements, "default"
		}
		request := model.CreateRequest{Title: schedule.Title, Type: s.taskType(schedule.Type), Visibility: model.VisibilityAgent, Summary: schedule.Summary, Section: schedule.Section, Project: schedule.Project, Repository: schedule.Repository, Priority: schedule.Priority, Checklist: schedule.Checklist}
		payload := map[string]any{"schedule_id": schedule.ID, "schedule_name": schedule.Name, "scheduled_for": stamp(scheduledFor)}
		if runBy != "" {
			payload["run_by"] = runBy
		}
		taskID, event, err = insertQueuedTask(ctx, tx, queuedTask{request: request, createdBy: schedule.CreatedBy, editedBy: schedule.LastEditedBy, scheduleID: schedule.ID, requirements: requirements, requirementsSource: source, payload: payload}, now)
		if err != nil {
			return model.ScheduleRunResult{}, false, err
		}
	}

	query := `UPDATE task_schedules SET last_run_at=?,last_outcome=?,last_task_id=? WHERE id=?`
	args := []any{stamp(now), outcome, taskID, schedule.ID}
	if expectedNext != nil {
		query = `UPDATE task_schedules SET last_run_at=?,last_outcome=?,last_task_id=?,next_run_at=? WHERE id=? AND next_run_at=?`
		args = []any{stamp(now), outcome, taskID, nextValue, schedule.ID, *expectedNext}
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return model.ScheduleRunResult{}, false, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return model.ScheduleRunResult{}, false, nil
	}
	if err := tx.Commit(); err != nil {
		return model.ScheduleRunResult{}, false, err
	}
	s.publish(event)
	output := model.ScheduleRunResult{}
	if output.Schedule, err = s.getSchedule(ctx, s.store.DB(), schedule.ID); err != nil {
		return model.ScheduleRunResult{}, true, err
	}
	if outcome == model.ScheduleOutcomeCreated {
		task, err := s.store.GetTask(ctx, taskID)
		if err != nil {
			return output, true, err
		}
		output.Task = &task
	}
	return output, true, nil
}

// runStamp formats run times with fixed width so they compare as strings.
func runStamp(value time.Time) string {
	return value.UTC().Truncate(time.Second).Format(model.DeferTimeLayout)
}

func scheduleActor(schedule model.Schedule, runBy string) string {
	if runBy != "" {
		return runBy
	}
	return schedule.LastEditedBy
}

func ensureScheduleNameFree(ctx context.Context, tx *store.Tx, name, exceptID string) error {
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT id FROM task_schedules WHERE name=?`, name).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) || err == nil && existing == exceptID {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: a schedule named %q already exists", ErrConflict, name)
}

func encodeScheduleLists(request model.ScheduleRequest) (string, string, error) {
	checklist := request.Checklist
	if checklist == nil {
		checklist = []string{}
	}
	requirements := request.Requirements
	if requirements == nil {
		requirements = []string{}
	}
	encodedChecklist, err := json.Marshal(checklist)
	if err != nil {
		return "", "", err
	}
	encodedRequirements, err := json.Marshal(requirements)
	return string(encodedChecklist), string(encodedRequirements), err
}

func (s *Service) getSchedule(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (model.Schedule, error) {
	item, err := scanSchedule(q.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM task_schedules WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Schedule{}, store.ErrNotFound
	}
	return item, err
}

func scanSchedule(scanner interface{ Scan(...any) error }) (model.Schedule, error) {
	var item model.Schedule
	var checklist, requirements, created, updated string
	var nextRunAt, lastRunAt sql.NullString
	if err := scanner.Scan(&item.ID, &item.Name, &item.Enabled, &item.Cron, &item.Timezone, &item.Overlap, &item.Title, &item.Summary, &item.Type, &item.Section, &item.Project, &item.Repository, &item.Priority, &checklist, &requirements, &item.CreatedBy, &item.LastEditedBy, &nextRunAt, &lastRunAt, &item.LastOutcome, &item.LastTaskID, &item.Version, &created, &updated); err != nil {
		return model.Schedule{}, err
	}
	if err := json.Unmarshal([]byte(checklist), &item.Checklist); err != nil {
		return model.Schedule{}, err
	}
	if err := json.Unmarshal([]byte(requirements), &item.Requirements); err != nil {
		return model.Schedule{}, err
	}
	item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if nextRunAt.Valid {
		value, _ := time.Parse(time.RFC3339Nano, nextRunAt.String)
		item.NextRunAt = &value
	}
	if lastRunAt.Valid {
		value, _ := time.Parse(time.RFC3339Nano, lastRunAt.String)
		item.LastRunAt = &value
	}
	return item, nil
}
