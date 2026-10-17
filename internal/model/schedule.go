package model

import (
	"errors"
	"time"
)

// DeferDateLayout is the date-only form of defer_until.
const DeferDateLayout = "2006-01-02"

// DeferTimeLayout is the normalized timestamp form of defer_until.
const DeferTimeLayout = "2006-01-02T15:04:05Z"

// NormalizeDeferUntil accepts a date (YYYY-MM-DD) or an RFC 3339 timestamp
// and returns the stored form: the date unchanged, or the timestamp in UTC
// with second precision.
func NormalizeDeferUntil(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if _, err := time.Parse(DeferDateLayout, value); err == nil {
		return value, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", errors.New("defer_until must use YYYY-MM-DD or an RFC 3339 timestamp")
	}
	return parsed.UTC().Truncate(time.Second).Format(DeferTimeLayout), nil
}

// DeferStart returns the instant a deferral ends. A date-only value ends at
// midnight in location. It reports false for an empty or unparseable value.
func DeferStart(value string, location *time.Location) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	if location == nil {
		location = time.UTC
	}
	if parsed, err := time.ParseInLocation(DeferDateLayout, value, location); err == nil {
		return parsed, true
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}

// DeferActive reports whether a task deferred to value is still held at now.
func DeferActive(value string, location *time.Location, now time.Time) bool {
	start, ok := DeferStart(value, location)
	return ok && now.Before(start)
}

// ScheduleOverlap decides what a schedule does when its previous occurrence
// is still open at the next run time.
type ScheduleOverlap string

const (
	// ScheduleOverlapSkip records a skipped run and waits for the next time.
	ScheduleOverlapSkip ScheduleOverlap = "skip"
	// ScheduleOverlapAllow creates another occurrence regardless.
	ScheduleOverlapAllow ScheduleOverlap = "allow"
)

// Schedule creates an agent-pickup task each time its cron expression fires.
// Occurrences carry the schedule's creator and last editor as their edit
// provenance, so runners admit them exactly as if that person had queued
// the task.
type Schedule struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Enabled      bool            `json:"enabled"`
	Cron         string          `json:"cron"`
	Timezone     string          `json:"timezone"`
	Overlap      ScheduleOverlap `json:"overlap"`
	Title        string          `json:"title"`
	Type         TaskType        `json:"type"`
	Summary      string          `json:"summary,omitempty"`
	Section      string          `json:"section"`
	Project      string          `json:"project,omitempty"`
	Repository   string          `json:"repository,omitempty"`
	Priority     Priority        `json:"priority"`
	Checklist    []string        `json:"checklist"`
	Requirements []string        `json:"requirements,omitempty"`
	CreatedBy    string          `json:"created_by"`
	LastEditedBy string          `json:"last_edited_by"`
	NextRunAt    *time.Time      `json:"next_run_at,omitempty"`
	LastRunAt    *time.Time      `json:"last_run_at,omitempty"`
	LastOutcome  string          `json:"last_outcome,omitempty"`
	LastTaskID   string          `json:"last_task_id,omitempty"`
	Version      int64           `json:"version"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// Schedule run outcomes recorded in Schedule.LastOutcome.
const (
	ScheduleOutcomeCreated = "created"
	ScheduleOutcomeSkipped = "skipped_open_occurrence"
)

// ScheduleRequest creates or replaces a schedule. ExpectedVersion is required
// when updating.
type ScheduleRequest struct {
	Name            string          `json:"name"`
	Enabled         *bool           `json:"enabled,omitempty"`
	Cron            string          `json:"cron" jsonschema:"Five-field cron expression (minute hour day-of-month month day-of-week) or a descriptor such as @daily"`
	Timezone        string          `json:"timezone,omitempty" jsonschema:"IANA timezone for the cron expression; defaults to the instance review timezone"`
	Overlap         ScheduleOverlap `json:"overlap,omitempty" jsonschema:"skip (default) or allow when the previous occurrence is still open"`
	Title           string          `json:"title"`
	Type            TaskType        `json:"type,omitempty"`
	Summary         string          `json:"summary,omitempty"`
	Section         string          `json:"section,omitempty"`
	Project         string          `json:"project,omitempty"`
	Repository      string          `json:"repository,omitempty"`
	Priority        Priority        `json:"priority,omitempty"`
	Checklist       []string        `json:"checklist,omitempty"`
	Requirements    []string        `json:"requirements,omitempty"`
	ExpectedVersion int64           `json:"expected_version,omitempty"`
}

// ScheduleRunResult reports one schedule firing.
type ScheduleRunResult struct {
	Schedule Schedule `json:"schedule"`
	Task     *Task    `json:"task,omitempty"`
}
