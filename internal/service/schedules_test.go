package service

import (
	"errors"
	"testing"
	"time"

	"github.com/kilo666mj/taskboard/internal/model"
)

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time          { return c.now }
func (c *testClock) Set(value string)        { c.now = mustTime(value) }
func (c *testClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func mustTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func clockedService(t *testing.T, start string) (*Service, *testClock) {
	t.Helper()
	tasks := testService(t, time.Minute)
	clock := &testClock{now: mustTime(start)}
	tasks.store.SetClock(clock.Now)
	return tasks, clock
}

func pickupIDs(t *testing.T, tasks *Service, agent Principal) map[string]bool {
	t.Helper()
	page, err := tasks.listFor(t.Context(), model.ListTasksRequest{Visibility: model.VisibilityAgent, Statuses: []model.TaskStatus{model.TaskQueued}}, agent)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, task := range page.Tasks {
		ids[task.ID] = true
	}
	return ids
}

func TestDeferUntilTimestampHoldsAgentPickup(t *testing.T) {
	tasks, clock := clockedService(t, "2026-10-17T09:00:00Z")
	person, agent := HumanPrincipal("person"), AgentPrincipal("agent:worker")
	task, err := tasks.createFor(t.Context(), model.CreateRequest{Title: "Nightly report", Visibility: model.VisibilityAgent, DeferUntil: "2026-10-17T12:30:00+02:00"}, person)
	if err != nil {
		t.Fatal(err)
	}
	if task.DeferUntil != "2026-10-17T10:30:00Z" {
		t.Fatalf("defer_until = %q, want normalized UTC", task.DeferUntil)
	}
	if !task.Deferred || task.Ready {
		t.Fatalf("deferred=%v ready=%v, want deferred and not ready", task.Deferred, task.Ready)
	}
	if pickupIDs(t, tasks, agent)[task.ID] {
		t.Fatal("deferred task was offered for pickup")
	}
	if _, err := tasks.claimFor(t.Context(), task.ID, model.ClaimRequest{ExpectedVersion: task.Version}, agent); !errors.Is(err, ErrValidation) {
		t.Fatalf("claim before defer_until error = %v, want validation", err)
	}
	clock.Set("2026-10-17T10:30:00Z")
	if !pickupIDs(t, tasks, agent)[task.ID] {
		t.Fatal("task was not offered once defer_until passed")
	}
	if _, err := tasks.claimFor(t.Context(), task.ID, model.ClaimRequest{ExpectedVersion: task.Version}, agent); err != nil {
		t.Fatalf("claim after defer_until: %v", err)
	}
}

func TestDeferUntilDateEndsAtMidnightInReviewTimezone(t *testing.T) {
	tasks, clock := clockedService(t, "2026-10-18T03:00:00Z")
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	tasks.store.SetDeferLocation(location)
	task, err := tasks.createFor(t.Context(), model.CreateRequest{Title: "Date deferral", Visibility: model.VisibilityAgent, DeferUntil: "2026-10-18"}, HumanPrincipal("person"))
	if err != nil {
		t.Fatal(err)
	}
	// 03:00 UTC is still 23:00 on the 17th in New York.
	if !task.Deferred {
		t.Fatal("date deferral ended before local midnight")
	}
	clock.Set("2026-10-18T04:00:00Z")
	current, err := tasks.Get(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Deferred || !current.Ready {
		t.Fatalf("deferred=%v ready=%v after local midnight", current.Deferred, current.Ready)
	}
}

func TestDeferUntilRejectsUnknownFormats(t *testing.T) {
	tasks := testService(t, time.Minute)
	for _, value := range []string{"tomorrow", "2026-10-17 09:00", "17/10/2026"} {
		if _, err := tasks.createFor(t.Context(), model.CreateRequest{Title: "Bad defer " + value, DeferUntil: value}, HumanPrincipal("person")); !errors.Is(err, ErrValidation) {
			t.Errorf("defer_until %q error = %v, want validation", value, err)
		}
	}
}

func dailySchedule(name string) model.ScheduleRequest {
	return model.ScheduleRequest{
		Name: name, Cron: "0 7 * * *", Timezone: "UTC", Title: "Morning dependency audit",
		Summary: "Audit dependencies.", Project: "taskboard", Checklist: []string{"Scan", "Report"}, Requirements: []string{"runner:local"},
	}
}

func TestScheduleCreatesAgentTaskWithCreatorProvenance(t *testing.T) {
	tasks, clock := clockedService(t, "2026-10-17T06:00:00Z")
	person := HumanPrincipal("person")
	schedule, err := tasks.CreateScheduleFor(t.Context(), dailySchedule("audit"), person)
	if err != nil {
		t.Fatal(err)
	}
	if schedule.NextRunAt == nil || !schedule.NextRunAt.Equal(mustTime("2026-10-17T07:00:00Z")) {
		t.Fatalf("next_run_at = %v, want 07:00", schedule.NextRunAt)
	}
	clock.Set("2026-10-17T06:59:59Z")
	if fired, err := tasks.RunDueSchedules(t.Context()); err != nil || fired != 0 {
		t.Fatalf("early sweep fired=%d err=%v", fired, err)
	}
	clock.Set("2026-10-17T07:00:10Z")
	if fired, err := tasks.RunDueSchedules(t.Context()); err != nil || fired != 1 {
		t.Fatalf("due sweep fired=%d err=%v", fired, err)
	}
	if fired, err := tasks.RunDueSchedules(t.Context()); err != nil || fired != 0 {
		t.Fatalf("repeat sweep fired=%d err=%v", fired, err)
	}
	schedule, err = tasks.GetScheduleFor(t.Context(), schedule.ID, person)
	if err != nil {
		t.Fatal(err)
	}
	if schedule.LastOutcome != model.ScheduleOutcomeCreated || schedule.LastTaskID == "" {
		t.Fatalf("last outcome=%q task=%q", schedule.LastOutcome, schedule.LastTaskID)
	}
	if !schedule.NextRunAt.Equal(mustTime("2026-10-18T07:00:00Z")) {
		t.Fatalf("next_run_at = %v, want next day", schedule.NextRunAt)
	}
	task, err := tasks.Get(t.Context(), schedule.LastTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Visibility != model.VisibilityAgent || task.Status != model.TaskQueued || task.ScheduleID != schedule.ID {
		t.Fatalf("occurrence visibility=%s status=%s schedule=%q", task.Visibility, task.Status, task.ScheduleID)
	}
	if task.CreatedBy != "person" || task.LastEditedBy != "person" {
		t.Fatalf("occurrence provenance = %q/%q, want the schedule's person", task.CreatedBy, task.LastEditedBy)
	}
	if len(task.Items) != 2 || len(task.Requirements) != 1 || task.Requirements[0] != "runner:local" || task.Project != "taskboard" {
		t.Fatalf("occurrence items=%d requirements=%v project=%q", len(task.Items), task.Requirements, task.Project)
	}
}

func TestScheduleSkipsWhileOccurrenceOpenAndDoesNotBackfill(t *testing.T) {
	tasks, clock := clockedService(t, "2026-10-17T06:00:00Z")
	person := HumanPrincipal("person")
	schedule, err := tasks.CreateScheduleFor(t.Context(), dailySchedule("audit"), person)
	if err != nil {
		t.Fatal(err)
	}
	clock.Set("2026-10-17T07:00:00Z")
	if _, err := tasks.RunDueSchedules(t.Context()); err != nil {
		t.Fatal(err)
	}
	first, _ := tasks.GetScheduleFor(t.Context(), schedule.ID, person)

	clock.Set("2026-10-18T07:00:05Z")
	if fired, err := tasks.RunDueSchedules(t.Context()); err != nil || fired != 1 {
		t.Fatalf("second sweep fired=%d err=%v", fired, err)
	}
	second, _ := tasks.GetScheduleFor(t.Context(), schedule.ID, person)
	if second.LastOutcome != model.ScheduleOutcomeSkipped || second.LastTaskID != first.LastTaskID {
		t.Fatalf("outcome=%q task=%q, want skip pointing at open occurrence %q", second.LastOutcome, second.LastTaskID, first.LastTaskID)
	}

	if _, err := tasks.store.DB().ExecContext(t.Context(), `UPDATE tasks SET status=? WHERE id=?`, model.TaskDone, first.LastTaskID); err != nil {
		t.Fatal(err)
	}
	// Three days pass while Taskboard is down: one late run, then the next future time.
	clock.Set("2026-10-21T12:00:00Z")
	if fired, err := tasks.RunDueSchedules(t.Context()); err != nil || fired != 1 {
		t.Fatalf("catch-up sweep fired=%d err=%v", fired, err)
	}
	if fired, err := tasks.RunDueSchedules(t.Context()); err != nil || fired != 0 {
		t.Fatalf("backfill sweep fired=%d err=%v", fired, err)
	}
	third, _ := tasks.GetScheduleFor(t.Context(), schedule.ID, person)
	if third.LastOutcome != model.ScheduleOutcomeCreated || third.LastTaskID == first.LastTaskID {
		t.Fatalf("catch-up outcome=%q task=%q", third.LastOutcome, third.LastTaskID)
	}
	if !third.NextRunAt.Equal(mustTime("2026-10-22T07:00:00Z")) {
		t.Fatalf("next_run_at = %v, want next future run", third.NextRunAt)
	}
}

func TestScheduleOverlapAllowCreatesEveryRun(t *testing.T) {
	tasks, clock := clockedService(t, "2026-10-17T06:00:00Z")
	request := dailySchedule("audit")
	request.Overlap = model.ScheduleOverlapAllow
	schedule, err := tasks.CreateScheduleFor(t.Context(), request, HumanPrincipal("person"))
	if err != nil {
		t.Fatal(err)
	}
	clock.Set("2026-10-17T07:00:00Z")
	if _, err := tasks.RunDueSchedules(t.Context()); err != nil {
		t.Fatal(err)
	}
	clock.Set("2026-10-18T07:00:00Z")
	if _, err := tasks.RunDueSchedules(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := tasks.store.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM tasks WHERE schedule_id=?`, schedule.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("occurrences = %d, want 2", count)
	}
}

func TestScheduleRunNowKeepsNextRunAndUsesEditorProvenance(t *testing.T) {
	tasks, clock := clockedService(t, "2026-10-17T06:00:00Z")
	owner, editor := HumanPrincipal("owner"), HumanPrincipal("editor")
	schedule, err := tasks.CreateScheduleFor(t.Context(), dailySchedule("audit"), owner)
	if err != nil {
		t.Fatal(err)
	}
	request := dailySchedule("audit")
	request.Title = "Edited audit"
	request.ExpectedVersion = schedule.Version
	schedule, err = tasks.UpdateScheduleFor(t.Context(), schedule.ID, request, editor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.UpdateScheduleFor(t.Context(), schedule.ID, request, editor); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error = %v, want conflict", err)
	}
	clock.Advance(10 * time.Minute)
	result, err := tasks.RunScheduleNowFor(t.Context(), schedule.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if result.Task == nil || result.Task.Title != "Edited audit" {
		t.Fatalf("run-now task = %+v", result.Task)
	}
	if result.Task.CreatedBy != "owner" || result.Task.LastEditedBy != "editor" {
		t.Fatalf("provenance = %q/%q, want owner/editor", result.Task.CreatedBy, result.Task.LastEditedBy)
	}
	if !result.Schedule.NextRunAt.Equal(*schedule.NextRunAt) {
		t.Fatalf("run-now moved next_run_at from %v to %v", schedule.NextRunAt, result.Schedule.NextRunAt)
	}
}

func TestScheduleDisableStopsRuns(t *testing.T) {
	tasks, clock := clockedService(t, "2026-10-17T06:00:00Z")
	person := HumanPrincipal("person")
	schedule, err := tasks.CreateScheduleFor(t.Context(), dailySchedule("audit"), person)
	if err != nil {
		t.Fatal(err)
	}
	request := dailySchedule("audit")
	request.Enabled = new(false)
	request.ExpectedVersion = schedule.Version
	schedule, err = tasks.UpdateScheduleFor(t.Context(), schedule.ID, request, person)
	if err != nil {
		t.Fatal(err)
	}
	if schedule.Enabled || schedule.NextRunAt != nil {
		t.Fatalf("disabled schedule enabled=%v next=%v", schedule.Enabled, schedule.NextRunAt)
	}
	clock.Set("2026-10-17T07:00:00Z")
	if fired, err := tasks.RunDueSchedules(t.Context()); err != nil || fired != 0 {
		t.Fatalf("disabled sweep fired=%d err=%v", fired, err)
	}
}

func TestSchedulePermissionsAndValidation(t *testing.T) {
	tasks, _ := clockedService(t, "2026-10-17T06:00:00Z")
	person := HumanPrincipal("person")
	for name, principal := range map[string]Principal{
		"agent":  AgentPrincipal("agent:worker"),
		"viewer": HumanPrincipalWithRole("viewer", RoleViewer),
	} {
		if _, err := tasks.CreateScheduleFor(t.Context(), dailySchedule("audit-"+name), principal); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s create error = %v, want forbidden", name, err)
		}
	}
	if _, err := tasks.ListSchedulesFor(t.Context(), AgentPrincipal("agent:worker")); err != nil {
		t.Errorf("agent list: %v", err)
	}
	invalid := map[string]func(*model.ScheduleRequest){
		"every minute":  func(r *model.ScheduleRequest) { r.Cron = "* * * * *" },
		"tz prefix":     func(r *model.ScheduleRequest) { r.Cron = "CRON_TZ=UTC 0 7 * * *" },
		"every":         func(r *model.ScheduleRequest) { r.Cron = "@every 1h" },
		"garbage":       func(r *model.ScheduleRequest) { r.Cron = "tomorrow morning" },
		"bad timezone":  func(r *model.ScheduleRequest) { r.Timezone = "Mars/Olympus" },
		"bad overlap":   func(r *model.ScheduleRequest) { r.Overlap = "queue" },
		"missing title": func(r *model.ScheduleRequest) { r.Title = " " },
		"bad requirement": func(r *model.ScheduleRequest) {
			r.Requirements = []string{"has space"}
		},
	}
	for name, mutate := range invalid {
		request := dailySchedule("audit-" + name)
		mutate(&request)
		if _, err := tasks.CreateScheduleFor(t.Context(), request, person); !errors.Is(err, ErrValidation) {
			t.Errorf("%s error = %v, want validation", name, err)
		}
	}
	if _, err := tasks.CreateScheduleFor(t.Context(), dailySchedule("audit"), person); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.CreateScheduleFor(t.Context(), dailySchedule("audit"), person); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate name error = %v, want conflict", err)
	}
	hourly := dailySchedule("hourly")
	hourly.Cron = "@hourly"
	if _, err := tasks.CreateScheduleFor(t.Context(), hourly, person); err != nil {
		t.Errorf("@hourly: %v", err)
	}
}

func TestScheduleTimezoneControlsRunTime(t *testing.T) {
	tasks, _ := clockedService(t, "2026-10-17T06:00:00Z")
	request := dailySchedule("audit")
	request.Timezone = "Europe/Copenhagen"
	schedule, err := tasks.CreateScheduleFor(t.Context(), request, HumanPrincipal("person"))
	if err != nil {
		t.Fatal(err)
	}
	// 07:00 in Copenhagen on 17 October 2026 (CEST, UTC+2) is 05:00 UTC, already
	// past, so the first run is the next morning.
	if !schedule.NextRunAt.Equal(mustTime("2026-10-18T05:00:00Z")) {
		t.Fatalf("next_run_at = %v", schedule.NextRunAt)
	}
}

func TestScheduleSpacingIsCheckedTheSameWhateverTheTime(t *testing.T) {
	// 07:59 then 00:00 the next day is fine, but 00:50 then 00:59 is nine
	// minutes and 06:59 then 07:00 is one: rejected at any time of day.
	tight := "0,10,20,30,40,50,59 0,2,4,6,7 * * *"
	for _, at := range []string{"2026-10-17T23:00:00Z", "2026-10-18T00:30:00Z", "2026-10-18T06:59:30Z"} {
		tasks, _ := clockedService(t, at)
		request := dailySchedule("tight")
		request.Cron = tight
		if _, err := tasks.CreateScheduleFor(t.Context(), request, HumanPrincipal("person")); !errors.Is(err, ErrValidation) {
			t.Errorf("at %s: %q error = %v, want validation", at, tight, err)
		}
	}
	// Five minutes apart is the floor, including across DST in the schedule's zone.
	tasks, _ := clockedService(t, "2026-10-17T23:00:00Z")
	request := dailySchedule("every-five")
	request.Cron, request.Timezone = "*/5 * * * *", "Europe/Berlin"
	if _, err := tasks.CreateScheduleFor(t.Context(), request, HumanPrincipal("person")); err != nil {
		t.Fatalf("every five minutes: %v", err)
	}
}

func TestTimedRunStopsAndSaysSoWhenTheDefinitionNoLongerParses(t *testing.T) {
	tasks, clock := clockedService(t, "2026-10-17T06:00:00Z")
	person := HumanPrincipal("person")
	schedule, err := tasks.CreateScheduleFor(t.Context(), dailySchedule("audit"), person)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.store.DB().ExecContext(t.Context(), `UPDATE task_schedules SET timezone=? WHERE id=?`, "Mars/Olympus_Mons", schedule.ID); err != nil {
		t.Fatal(err)
	}
	clock.Set("2026-10-17T07:00:10Z")
	if fired, err := tasks.RunDueSchedules(t.Context()); err != nil || fired != 0 {
		t.Fatalf("sweep fired=%d err=%v, want nothing fired", fired, err)
	}
	schedule, err = tasks.GetScheduleFor(t.Context(), schedule.ID, person)
	if err != nil {
		t.Fatal(err)
	}
	if schedule.LastOutcome != model.ScheduleOutcomeStopped || schedule.NextRunAt != nil || schedule.LastTaskID != "" {
		t.Fatalf("stopped schedule = outcome %q next %v task %q", schedule.LastOutcome, schedule.NextRunAt, schedule.LastTaskID)
	}
}
