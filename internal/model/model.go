package model

import "time"

type TaskStatus string

const DefaultSection = "General"

type TaskType string

const (
	TaskPersonal TaskType = "personal"
	TaskWork     TaskType = "work"
)

type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

const (
	TaskQueued    TaskStatus = "queued"
	TaskActive    TaskStatus = "active"
	TaskWaiting   TaskStatus = "waiting"
	TaskBlocked   TaskStatus = "blocked"
	TaskStale     TaskStatus = "stale"
	TaskDone      TaskStatus = "done"
	TaskCancelled TaskStatus = "cancelled"
)

type ItemStatus string

const (
	ItemTodo    ItemStatus = "todo"
	ItemActive  ItemStatus = "active"
	ItemDone    ItemStatus = "done"
	ItemSkipped ItemStatus = "skipped"
)

type Task struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Type        TaskType        `json:"type"`
	Summary     string          `json:"summary,omitempty"`
	Section     string          `json:"section"`
	Project     string          `json:"project,omitempty"`
	Repository  string          `json:"repository,omitempty"`
	Priority    Priority        `json:"priority"`
	DueDate     string          `json:"due_date,omitempty"`
	DeferUntil  string          `json:"defer_until,omitempty"`
	Recurrence  string          `json:"recurrence,omitempty"`
	SortOrder   int64           `json:"sort_order"`
	ReviewedAt  *time.Time      `json:"reviewed_at,omitempty"`
	Status      TaskStatus      `json:"status"`
	Owner       string          `json:"owner,omitempty"`
	CurrentNote string          `json:"current_note,omitempty"`
	Blocker     string          `json:"blocker,omitempty"`
	WaitingFor  string          `json:"waiting_for,omitempty"`
	Version     int64           `json:"version"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	Items       []ChecklistItem `json:"items"`
	Runs        []AgentRun      `json:"runs,omitempty"`
}

type ChecklistItem struct {
	ID        string     `json:"id"`
	TaskID    string     `json:"task_id"`
	Label     string     `json:"label"`
	Status    ItemStatus `json:"status"`
	Position  int        `json:"position"`
	Required  bool       `json:"required"`
	Note      string     `json:"note,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type AgentRun struct {
	ID            string     `json:"id"`
	TaskID        string     `json:"task_id"`
	Agent         string     `json:"agent"`
	Client        string     `json:"client,omitempty"`
	Status        TaskStatus `json:"status"`
	LeaseExpires  time.Time  `json:"lease_expires_at"`
	LastHeartbeat time.Time  `json:"last_heartbeat_at"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
}

type Event struct {
	ID        string         `json:"id"`
	TaskID    string         `json:"task_id"`
	RunID     string         `json:"run_id,omitempty"`
	Kind      string         `json:"kind"`
	Actor     string         `json:"actor"`
	Message   string         `json:"message,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type StartRequest struct {
	Title      string   `json:"title"`
	Type       TaskType `json:"type,omitempty"`
	Summary    string   `json:"summary,omitempty"`
	Section    string   `json:"section,omitempty"`
	Project    string   `json:"project,omitempty"`
	Repository string   `json:"repository,omitempty"`
	Priority   Priority `json:"priority,omitempty"`
	DueDate    string   `json:"due_date,omitempty"`
	DeferUntil string   `json:"defer_until,omitempty"`
	Recurrence string   `json:"recurrence,omitempty"`
	Checklist  []string `json:"checklist"`
	Agent      string   `json:"agent,omitempty"`
	Client     string   `json:"client,omitempty"`
}

type CreateRequest struct {
	Title      string   `json:"title"`
	Type       TaskType `json:"type,omitempty"`
	Summary    string   `json:"summary,omitempty"`
	Section    string   `json:"section,omitempty"`
	Project    string   `json:"project,omitempty"`
	Repository string   `json:"repository,omitempty"`
	Priority   Priority `json:"priority,omitempty"`
	DueDate    string   `json:"due_date,omitempty"`
	DeferUntil string   `json:"defer_until,omitempty"`
	Recurrence string   `json:"recurrence,omitempty"`
	Checklist  []string `json:"checklist,omitempty"`
}

type UpdateRequest struct {
	ExpectedVersion int64      `json:"expected_version"`
	RunID           string     `json:"run_id,omitempty"`
	Status          TaskStatus `json:"status,omitempty"`
	Title           *string    `json:"title,omitempty"`
	Type            *TaskType  `json:"type,omitempty"`
	Summary         *string    `json:"summary,omitempty"`
	Owner           *string    `json:"owner,omitempty"`
	Section         *string    `json:"section,omitempty"`
	Project         *string    `json:"project,omitempty"`
	Repository      *string    `json:"repository,omitempty"`
	Priority        *Priority  `json:"priority,omitempty"`
	DueDate         *string    `json:"due_date,omitempty"`
	DeferUntil      *string    `json:"defer_until,omitempty"`
	Recurrence      *string    `json:"recurrence,omitempty"`
	Reviewed        bool       `json:"reviewed,omitempty"`
	Checklist       *[]string  `json:"checklist,omitempty"`
	CurrentNote     *string    `json:"current_note,omitempty"`
	Blocker         *string    `json:"blocker,omitempty"`
	WaitingFor      *string    `json:"waiting_for,omitempty"`
	CurrentItemID   string     `json:"current_item_id,omitempty"`
	CompleteItemIDs []string   `json:"complete_item_ids,omitempty"`
	SkipItemIDs     []string   `json:"skip_item_ids,omitempty"`
	SkipReason      string     `json:"skip_reason,omitempty"`
	AddItems        []string   `json:"add_items,omitempty"`
}

type StartResult struct {
	Task Task     `json:"task"`
	Run  AgentRun `json:"run"`
}

type ClaimRequest struct {
	ExpectedVersion int64  `json:"expected_version"`
	Agent           string `json:"agent,omitempty"`
	Client          string `json:"client,omitempty"`
}

type MoveRequest struct {
	ExpectedVersion int64  `json:"expected_version"`
	Direction       string `json:"direction"`
}

type Template struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Title      string    `json:"title"`
	Type       TaskType  `json:"type"`
	Summary    string    `json:"summary,omitempty"`
	Section    string    `json:"section"`
	Project    string    `json:"project,omitempty"`
	Repository string    `json:"repository,omitempty"`
	Priority   Priority  `json:"priority"`
	Recurrence string    `json:"recurrence,omitempty"`
	Checklist  []string  `json:"checklist"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type TemplateRequest struct {
	Name       string   `json:"name"`
	Title      string   `json:"title"`
	Type       TaskType `json:"type,omitempty"`
	Summary    string   `json:"summary,omitempty"`
	Section    string   `json:"section,omitempty"`
	Project    string   `json:"project,omitempty"`
	Repository string   `json:"repository,omitempty"`
	Priority   Priority `json:"priority,omitempty"`
	Recurrence string   `json:"recurrence,omitempty"`
	Checklist  []string `json:"checklist,omitempty"`
}

func IsTaskStatus(value TaskStatus) bool {
	switch value {
	case TaskQueued, TaskActive, TaskWaiting, TaskBlocked, TaskStale, TaskDone, TaskCancelled:
		return true
	default:
		return false
	}
}

func IsPriority(value Priority) bool {
	switch value {
	case PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent:
		return true
	default:
		return false
	}
}

func IsTaskType(value TaskType) bool {
	switch value {
	case TaskPersonal, TaskWork:
		return true
	default:
		return false
	}
}
