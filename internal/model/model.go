package model

import "time"

type TaskStatus string

const DefaultSection = "General"

type TaskType string

type TaskVisibility string

const (
	TaskPersonal TaskType = "personal"
	TaskWork     TaskType = "work"
)

const (
	VisibilityPrivate TaskVisibility = "private"
	VisibilityTeam    TaskVisibility = "team"
	VisibilityAgent   TaskVisibility = "agent"
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
	ID           string           `json:"id"`
	Title        string           `json:"title"`
	Type         TaskType         `json:"type"`
	Visibility   TaskVisibility   `json:"visibility"`
	CreatedBy    string           `json:"created_by"`
	LastEditedBy string           `json:"last_edited_by"`
	Summary      string           `json:"summary,omitempty"`
	Section      string           `json:"section"`
	Project      string           `json:"project,omitempty"`
	Repository   string           `json:"repository,omitempty"`
	Priority     Priority         `json:"priority"`
	DueDate      string           `json:"due_date,omitempty"`
	DeferUntil   string           `json:"defer_until,omitempty"`
	Recurrence   string           `json:"recurrence,omitempty"`
	SortOrder    int64            `json:"sort_order"`
	ReviewedAt   *time.Time       `json:"reviewed_at,omitempty"`
	Status       TaskStatus       `json:"status"`
	Owner        string           `json:"owner,omitempty"`
	CurrentNote  string           `json:"current_note,omitempty"`
	Blocker      string           `json:"blocker,omitempty"`
	WaitingFor   string           `json:"waiting_for,omitempty"`
	Version      int64            `json:"version"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
	CompletedAt  *time.Time       `json:"completed_at,omitempty"`
	Items        []ChecklistItem  `json:"items"`
	Runs         []AgentRun       `json:"runs,omitempty"`
	Dependencies []TaskDependency `json:"dependencies,omitempty"`
	Ready        bool             `json:"ready"`
	Requirements []string         `json:"requirements,omitempty"`
}

type TaskDependency struct {
	TaskID          string     `json:"task_id"`
	BlockedByTaskID string     `json:"blocked_by_task_id"`
	BlockedByTitle  string     `json:"blocked_by_title"`
	BlockedByStatus TaskStatus `json:"blocked_by_status"`
	Satisfied       bool       `json:"satisfied"`
	CreatedBy       string     `json:"created_by"`
	CreatedAt       time.Time  `json:"created_at"`
}
type AddTaskDependencyRequest struct {
	BlockedByTaskID string `json:"blocked_by_task_id"`
	ExpectedVersion int64  `json:"expected_version"`
}

type SetTaskRequirementsRequest struct {
	Requirements    []string `json:"requirements"`
	ExpectedVersion int64    `json:"expected_version"`
}
type WorkerAdvertisement struct {
	Principal    string    `json:"principal"`
	Capabilities []string  `json:"capabilities"`
	Capacity     int       `json:"capacity"`
	UpdatedAt    time.Time `json:"updated_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}
type AdvertiseWorkerRequest struct {
	Capabilities []string `json:"capabilities"`
	Capacity     int      `json:"capacity"`
	TTLSeconds   int      `json:"ttl_seconds,omitempty"`
}

type UsageRecord struct {
	ID                  string    `json:"id"`
	TaskID              string    `json:"task_id"`
	RunID               string    `json:"run_id"`
	Provider            string    `json:"provider,omitempty"`
	Model               string    `json:"model,omitempty"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	EstimatedCostMicros int64     `json:"estimated_cost_micros"`
	RecordedBy          string    `json:"recorded_by"`
	CreatedAt           time.Time `json:"created_at"`
}
type RecordUsageRequest struct {
	RunID               string `json:"run_id"`
	Provider            string `json:"provider,omitempty"`
	Model               string `json:"model,omitempty"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	EstimatedCostMicros int64  `json:"estimated_cost_micros"`
	IdempotencyKey      string `json:"idempotency_key,omitempty"`
	IdempotencyHash     string `json:"-"`
}
type AnalyticsSummary struct {
	WindowDays           int       `json:"window_days"`
	WindowStart          time.Time `json:"window_start"`
	WindowEnd            time.Time `json:"window_end"`
	TasksCreated         int64     `json:"tasks_created"`
	TasksDone            int64     `json:"tasks_done"`
	TasksCancelled       int64     `json:"tasks_cancelled"`
	CompletionRate       float64   `json:"completion_rate"`
	MeanQueueAgeSeconds  float64   `json:"mean_queue_age_seconds"`
	MeanExecutionSeconds float64   `json:"mean_execution_seconds"`
	Runs                 int64     `json:"runs"`
	StaleRuns            int64     `json:"stale_runs"`
	StaleRunRate         float64   `json:"stale_run_rate"`
	Escalations          int64     `json:"escalations"`
	EscalationRate       float64   `json:"escalation_rate"`
	Retries              int64     `json:"retries"`
	ReviewReferences     int64     `json:"review_references"`
	MeanReviewCycles     float64   `json:"mean_review_cycles"`
	InputTokens          int64     `json:"input_tokens"`
	OutputTokens         int64     `json:"output_tokens"`
	EstimatedCostMicros  int64     `json:"estimated_cost_micros"`
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
	SessionID     string     `json:"session_id,omitempty"`
	Agent         string     `json:"agent"`
	Client        string     `json:"client,omitempty"`
	Callsign      string     `json:"callsign"`
	Tone          int        `json:"tone"`
	Status        TaskStatus `json:"status"`
	LeaseExpires  time.Time  `json:"lease_expires_at"`
	LastHeartbeat time.Time  `json:"last_heartbeat_at"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
}

type RunProgress struct {
	LastChangedAt     time.Time `json:"last_changed_at"`
	AgeSeconds        int64     `json:"age_seconds"`
	StaleAfterSeconds int64     `json:"stale_after_seconds"`
	Stale             bool      `json:"stale"`
	CurrentItemID     string    `json:"current_item_id,omitempty"`
	CompletedItems    int       `json:"completed_items"`
	TotalItems        int       `json:"total_items"`
	Hint              string    `json:"hint,omitempty"`
}

type HeartbeatResult struct {
	Run             AgentRun            `json:"run"`
	Progress        RunProgress         `json:"progress"`
	PendingMessages []TaskMessage       `json:"pending_messages,omitempty"`
	PendingControls []RunControlRequest `json:"pending_controls,omitempty"`
}

type MessageKind string

const (
	MessageNote        MessageKind = "note"
	MessageInstruction MessageKind = "instruction"
	MessageQuestion    MessageKind = "question"
	MessageAnswer      MessageKind = "answer"
)

// TaskMessage is immutable conversation content. AuthorRunID identifies an
// agent run that produced a message; TargetRunID restricts delivery to one
// immutable run. Empty TargetRunID means task-level context that replacement
// runs may also receive.
type TaskMessage struct {
	ID           string           `json:"id"`
	TaskID       string           `json:"task_id"`
	Author       string           `json:"author"`
	AuthorRunID  string           `json:"author_run_id,omitempty"`
	TargetRunID  string           `json:"target_run_id,omitempty"`
	Kind         MessageKind      `json:"kind"`
	Body         string           `json:"body"`
	ReplyToID    string           `json:"reply_to_id,omitempty"`
	SupersedesID string           `json:"supersedes_id,omitempty"`
	RequiresAck  bool             `json:"requires_ack"`
	CreatedAt    time.Time        `json:"created_at"`
	Receipts     []MessageReceipt `json:"receipts,omitempty"`
}

type MessageReceipt struct {
	MessageID      string     `json:"message_id"`
	RunID          string     `json:"run_id"`
	Observer       string     `json:"observer"`
	ObservedAt     time.Time  `json:"observed_at"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
}

type AddMessageRequest struct {
	AuthorRunID     string      `json:"author_run_id,omitempty"`
	TargetRunID     string      `json:"target_run_id,omitempty"`
	Kind            MessageKind `json:"kind"`
	Body            string      `json:"body"`
	ReplyToID       string      `json:"reply_to_id,omitempty"`
	SupersedesID    string      `json:"supersedes_id,omitempty"`
	RequiresAck     bool        `json:"requires_ack,omitempty"`
	IdempotencyKey  string      `json:"idempotency_key,omitempty"`
	IdempotencyHash string      `json:"-"`
}

type MessageReceiptRequest struct {
	ObservedMessageIDs     []string `json:"observed_message_ids,omitempty"`
	AcknowledgedMessageIDs []string `json:"acknowledged_message_ids,omitempty"`
	IdempotencyKey         string   `json:"idempotency_key,omitempty"`
	IdempotencyHash        string   `json:"-"`
}

type EscalationStatus string

const (
	EscalationOpen     EscalationStatus = "open"
	EscalationAnswered EscalationStatus = "answered"
)

// TaskEscalation adds decision semantics to an immutable question/answer pair.
// Blocking escalations end their source run; the run is never revived.
type TaskEscalation struct {
	ID                string           `json:"id"`
	TaskID            string           `json:"task_id"`
	RunID             string           `json:"run_id"`
	QuestionMessageID string           `json:"question_message_id"`
	AnswerMessageID   string           `json:"answer_message_id,omitempty"`
	Blocking          bool             `json:"blocking"`
	Options           []string         `json:"options"`
	Recommendation    string           `json:"recommendation,omitempty"`
	SelectedOption    string           `json:"selected_option,omitempty"`
	Status            EscalationStatus `json:"status"`
	ResolvedBy        string           `json:"resolved_by,omitempty"`
	CreatedAt         time.Time        `json:"created_at"`
	ResolvedAt        *time.Time       `json:"resolved_at,omitempty"`
}

type CreateEscalationRequest struct {
	RunID           string   `json:"run_id"`
	ExpectedVersion int64    `json:"expected_version"`
	Question        string   `json:"question"`
	Options         []string `json:"options,omitempty"`
	Recommendation  string   `json:"recommendation,omitempty"`
	Blocking        bool     `json:"blocking"`
	IdempotencyKey  string   `json:"idempotency_key,omitempty"`
	IdempotencyHash string   `json:"-"`
}

type ResolveEscalationRequest struct {
	ExpectedVersion int64  `json:"expected_version"`
	Answer          string `json:"answer"`
	SelectedOption  string `json:"selected_option,omitempty"`
	IdempotencyKey  string `json:"idempotency_key,omitempty"`
	IdempotencyHash string `json:"-"`
}

type RunControlKind string
type RunControlStatus string

const (
	RunControlPause  RunControlKind = "pause"
	RunControlCancel RunControlKind = "cancel"
	RunControlResume RunControlKind = "resume"
	RunControlRetry  RunControlKind = "retry"
)

const (
	RunControlRequested    RunControlStatus = "requested"
	RunControlAcknowledged RunControlStatus = "acknowledged"
	RunControlAccepted     RunControlStatus = "accepted"
	RunControlRejected     RunControlStatus = "rejected"
	RunControlCompleted    RunControlStatus = "completed"
	RunControlExpired      RunControlStatus = "expired"
)

type RunControlRequest struct {
	ID             string           `json:"id"`
	TaskID         string           `json:"task_id"`
	TargetRunID    string           `json:"target_run_id"`
	TargetAgent    string           `json:"target_agent"`
	Kind           RunControlKind   `json:"kind"`
	Status         RunControlStatus `json:"status"`
	RequestedBy    string           `json:"requested_by"`
	Reason         string           `json:"reason,omitempty"`
	OutcomeNote    string           `json:"outcome_note,omitempty"`
	TaskVersion    int64            `json:"task_version"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
	ExpiresAt      time.Time        `json:"expires_at"`
	AcknowledgedAt *time.Time       `json:"acknowledged_at,omitempty"`
	DecidedAt      *time.Time       `json:"decided_at,omitempty"`
	CompletedAt    *time.Time       `json:"completed_at,omitempty"`
}

type CreateRunControlRequest struct {
	TargetRunID     string         `json:"target_run_id"`
	Kind            RunControlKind `json:"kind"`
	Reason          string         `json:"reason,omitempty"`
	ExpectedVersion int64          `json:"expected_version"`
	IdempotencyKey  string         `json:"idempotency_key,omitempty"`
	IdempotencyHash string         `json:"-"`
}

type UpdateRunControlRequest struct {
	Status          RunControlStatus `json:"status"`
	OutcomeNote     string           `json:"outcome_note,omitempty"`
	ExpectedVersion int64            `json:"expected_version"`
	IdempotencyKey  string           `json:"idempotency_key,omitempty"`
	IdempotencyHash string           `json:"-"`
}

type ReviewRequeueRequest struct {
	TargetRunID     string `json:"target_run_id"`
	ExpectedVersion int64  `json:"expected_version"`
	ReviewNote      string `json:"review_note"`
}

type ReferenceKind string
type ReferenceProvenance string

const (
	ReferenceLinear      ReferenceKind = "linear"
	ReferenceRepository  ReferenceKind = "repository"
	ReferenceWorktree    ReferenceKind = "worktree"
	ReferenceBranch      ReferenceKind = "branch"
	ReferenceCommit      ReferenceKind = "commit"
	ReferencePullRequest ReferenceKind = "pull_request"
	ReferenceCIRun       ReferenceKind = "ci_run"
	ReferenceDeployment  ReferenceKind = "deployment"
	ReferenceScreenshot  ReferenceKind = "screenshot"
	ReferenceReview      ReferenceKind = "review"
)

const (
	ReferenceByHuman ReferenceProvenance = "human"
	ReferenceByAgent ReferenceProvenance = "agent"
)

type TaskReference struct {
	ID         string              `json:"id"`
	TaskID     string              `json:"task_id"`
	RunID      string              `json:"run_id,omitempty"`
	Kind       ReferenceKind       `json:"kind"`
	Label      string              `json:"label"`
	Locator    string              `json:"locator,omitempty"`
	URL        string              `json:"url,omitempty"`
	CreatedBy  string              `json:"created_by"`
	Provenance ReferenceProvenance `json:"provenance"`
	CreatedAt  time.Time           `json:"created_at"`
}

type AddTaskReferenceRequest struct {
	RunID           string        `json:"run_id,omitempty"`
	Kind            ReferenceKind `json:"kind"`
	Label           string        `json:"label"`
	Locator         string        `json:"locator,omitempty"`
	URL             string        `json:"url,omitempty"`
	IdempotencyKey  string        `json:"idempotency_key,omitempty"`
	IdempotencyHash string        `json:"-"`
}

type DeliveryMilestone struct {
	Kind        string    `json:"kind"`
	Label       string    `json:"label"`
	ReachedAt   time.Time `json:"reached_at"`
	ReferenceID string    `json:"reference_id,omitempty"`
	EventID     string    `json:"event_id,omitempty"`
}

type HandoffKind string

const (
	HandoffCheckpoint HandoffKind = "checkpoint"
	HandoffFinal      HandoffKind = "final"
	HandoffStale      HandoffKind = "stale"
)

// RunHandoff is an append-only, authenticated continuation snapshot. It keeps
// concise structured facts only; raw prompts, reasoning, and tool output do not
// belong in a handoff.
type RunHandoff struct {
	ID                string      `json:"id"`
	TaskID            string      `json:"task_id"`
	RunID             string      `json:"run_id"`
	Kind              HandoffKind `json:"kind"`
	LastCompletedStep string      `json:"last_completed_step,omitempty"`
	Worktree          string      `json:"worktree,omitempty"`
	Branch            string      `json:"branch,omitempty"`
	Commits           []string    `json:"commits"`
	PullRequests      []string    `json:"pull_requests"`
	Validation        []string    `json:"validation"`
	ReviewFindings    []string    `json:"review_findings"`
	Blocker           string      `json:"blocker,omitempty"`
	NextAction        string      `json:"next_action,omitempty"`
	CreatedBy         string      `json:"created_by"`
	CreatedAt         time.Time   `json:"created_at"`
}

type AddRunHandoffRequest struct {
	RunID             string      `json:"run_id"`
	Kind              HandoffKind `json:"kind,omitempty"`
	LastCompletedStep string      `json:"last_completed_step,omitempty"`
	Worktree          string      `json:"worktree,omitempty"`
	Branch            string      `json:"branch,omitempty"`
	Commits           []string    `json:"commits,omitempty"`
	PullRequests      []string    `json:"pull_requests,omitempty"`
	Validation        []string    `json:"validation,omitempty"`
	ReviewFindings    []string    `json:"review_findings,omitempty"`
	Blocker           string      `json:"blocker,omitempty"`
	NextAction        string      `json:"next_action,omitempty"`
	IdempotencyKey    string      `json:"idempotency_key,omitempty"`
	IdempotencyHash   string      `json:"-"`
}

type CompletionRequirementKind string
type CompletionRequirementStatus string
type CompletionEvidenceStatus string

const (
	RequirementPullRequest    CompletionRequirementKind = "pull_request"
	RequirementGreenCI        CompletionRequirementKind = "green_ci"
	RequirementValidation     CompletionRequirementKind = "validation"
	RequirementResolvedReview CompletionRequirementKind = "resolved_review"
	RequirementDeployment     CompletionRequirementKind = "deployment"
	RequirementHumanApproval  CompletionRequirementKind = "human_approval"
	RequirementCustom         CompletionRequirementKind = "custom"
)
const (
	RequirementPending   CompletionRequirementStatus = "pending"
	RequirementSatisfied CompletionRequirementStatus = "satisfied"
	RequirementWaived    CompletionRequirementStatus = "waived"
)
const (
	EvidenceSubmitted CompletionEvidenceStatus = "submitted"
	EvidenceVerified  CompletionEvidenceStatus = "verified"
	EvidenceRejected  CompletionEvidenceStatus = "rejected"
)

type CompletionRequirement struct {
	ID           string                      `json:"id"`
	TaskID       string                      `json:"task_id"`
	Kind         CompletionRequirementKind   `json:"kind"`
	Label        string                      `json:"label"`
	Required     bool                        `json:"required"`
	Status       CompletionRequirementStatus `json:"status"`
	CreatedBy    string                      `json:"created_by"`
	VerifiedBy   string                      `json:"verified_by,omitempty"`
	WaiverReason string                      `json:"waiver_reason,omitempty"`
	CreatedAt    time.Time                   `json:"created_at"`
	UpdatedAt    time.Time                   `json:"updated_at"`
	VerifiedAt   *time.Time                  `json:"verified_at,omitempty"`
	Evidence     []CompletionEvidence        `json:"evidence"`
}

type CompletionEvidence struct {
	ID            string                   `json:"id"`
	RequirementID string                   `json:"requirement_id"`
	TaskID        string                   `json:"task_id"`
	RunID         string                   `json:"run_id,omitempty"`
	ReferenceID   string                   `json:"reference_id,omitempty"`
	Note          string                   `json:"note,omitempty"`
	Status        CompletionEvidenceStatus `json:"status"`
	SubmittedBy   string                   `json:"submitted_by"`
	ReviewedBy    string                   `json:"reviewed_by,omitempty"`
	ReviewNote    string                   `json:"review_note,omitempty"`
	SubmittedAt   time.Time                `json:"submitted_at"`
	ReviewedAt    *time.Time               `json:"reviewed_at,omitempty"`
}

type CreateCompletionRequirementRequest struct {
	Kind            CompletionRequirementKind `json:"kind"`
	Label           string                    `json:"label"`
	Required        bool                      `json:"required"`
	ExpectedVersion int64                     `json:"expected_version"`
}

type SubmitCompletionEvidenceRequest struct {
	RunID           string `json:"run_id,omitempty"`
	ReferenceID     string `json:"reference_id,omitempty"`
	Note            string `json:"note,omitempty"`
	IdempotencyKey  string `json:"idempotency_key,omitempty"`
	IdempotencyHash string `json:"-"`
}

type ReviewCompletionRequest struct {
	Status          CompletionRequirementStatus `json:"status"`
	EvidenceID      string                      `json:"evidence_id,omitempty"`
	Note            string                      `json:"note,omitempty"`
	ExpectedVersion int64                       `json:"expected_version"`
}

type SessionBridgeState string
type SessionAction string
type SessionRequestStatus string

const (
	SessionBridgeAvailable     SessionBridgeState   = "available"
	SessionBridgeUnavailable   SessionBridgeState   = "unavailable"
	SessionBridgeExpired       SessionBridgeState   = "expired"
	SessionActionOpen          SessionAction        = "open"
	SessionActionResume        SessionAction        = "resume"
	SessionRequestRequested    SessionRequestStatus = "requested"
	SessionRequestAcknowledged SessionRequestStatus = "acknowledged"
	SessionRequestCompleted    SessionRequestStatus = "completed"
	SessionRequestRejected     SessionRequestStatus = "rejected"
)

type SessionBridge struct {
	RunID      string             `json:"run_id"`
	TaskID     string             `json:"task_id"`
	Controller string             `json:"controller"`
	State      SessionBridgeState `json:"state"`
	Label      string             `json:"label,omitempty"`
	CanOpen    bool               `json:"can_open"`
	CanResume  bool               `json:"can_resume"`
	UpdatedAt  time.Time          `json:"updated_at"`
	ExpiresAt  time.Time          `json:"expires_at"`
}
type RegisterSessionBridgeRequest struct {
	RunID           string             `json:"run_id"`
	State           SessionBridgeState `json:"state"`
	Label           string             `json:"label,omitempty"`
	CanOpen         bool               `json:"can_open"`
	CanResume       bool               `json:"can_resume"`
	ExpiresAt       time.Time          `json:"expires_at"`
	IdempotencyKey  string             `json:"idempotency_key,omitempty"`
	IdempotencyHash string             `json:"-"`
}
type SessionBridgeRequest struct {
	ID          string               `json:"id"`
	TaskID      string               `json:"task_id"`
	RunID       string               `json:"run_id"`
	Controller  string               `json:"controller"`
	Action      SessionAction        `json:"action"`
	Status      SessionRequestStatus `json:"status"`
	RequestedBy string               `json:"requested_by"`
	OutcomeNote string               `json:"outcome_note,omitempty"`
	CreatedAt   time.Time            `json:"created_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
}
type CreateSessionBridgeRequest struct {
	RunID  string        `json:"run_id"`
	Action SessionAction `json:"action"`
}
type UpdateSessionBridgeRequest struct {
	Status          SessionRequestStatus `json:"status"`
	OutcomeNote     string               `json:"outcome_note,omitempty"`
	IdempotencyKey  string               `json:"idempotency_key,omitempty"`
	IdempotencyHash string               `json:"-"`
}

type RenameRunRequest struct {
	ExpectedVersion int64  `json:"expected_version"`
	Callsign        string `json:"callsign"`
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
	Title           string         `json:"title"`
	Type            TaskType       `json:"type,omitempty"`
	Visibility      TaskVisibility `json:"visibility,omitempty"`
	Summary         string         `json:"summary,omitempty"`
	Section         string         `json:"section,omitempty"`
	Project         string         `json:"project,omitempty"`
	Repository      string         `json:"repository,omitempty"`
	Priority        Priority       `json:"priority,omitempty"`
	DueDate         string         `json:"due_date,omitempty"`
	DeferUntil      string         `json:"defer_until,omitempty"`
	Recurrence      string         `json:"recurrence,omitempty"`
	Checklist       []string       `json:"checklist"`
	Agent           string         `json:"agent,omitempty"`
	Client          string         `json:"client,omitempty"`
	AgentSessionKey string         `json:"agent_session_key,omitempty" jsonschema:"Opaque stable identifier for this agent session; reuse it across task starts and claims in the same session"`
	IdempotencyKey  string         `json:"idempotency_key,omitempty"`
	IdempotencyHash string         `json:"-"`
}

type CreateRequest struct {
	Title           string         `json:"title"`
	Type            TaskType       `json:"type,omitempty"`
	Visibility      TaskVisibility `json:"visibility,omitempty"`
	Summary         string         `json:"summary,omitempty"`
	Section         string         `json:"section,omitempty"`
	Project         string         `json:"project,omitempty"`
	Repository      string         `json:"repository,omitempty"`
	Priority        Priority       `json:"priority,omitempty"`
	DueDate         string         `json:"due_date,omitempty"`
	DeferUntil      string         `json:"defer_until,omitempty"`
	Recurrence      string         `json:"recurrence,omitempty"`
	Checklist       []string       `json:"checklist,omitempty"`
	IdempotencyKey  string         `json:"idempotency_key,omitempty"`
	IdempotencyHash string         `json:"-"`
	// DelegatedVia is set by the service, never by clients, when an agent
	// creates the task on behalf of the person operating it.
	DelegatedVia string `json:"-"`
}

type UpdateRequest struct {
	ExpectedVersion int64           `json:"expected_version"`
	RunID           string          `json:"run_id,omitempty"`
	Status          TaskStatus      `json:"status,omitempty"`
	Title           *string         `json:"title,omitempty"`
	Type            *TaskType       `json:"type,omitempty"`
	Visibility      *TaskVisibility `json:"visibility,omitempty"`
	Summary         *string         `json:"summary,omitempty"`
	Owner           *string         `json:"owner,omitempty"`
	Section         *string         `json:"section,omitempty"`
	Project         *string         `json:"project,omitempty"`
	Repository      *string         `json:"repository,omitempty"`
	Priority        *Priority       `json:"priority,omitempty"`
	DueDate         *string         `json:"due_date,omitempty"`
	DeferUntil      *string         `json:"defer_until,omitempty"`
	Recurrence      *string         `json:"recurrence,omitempty"`
	Reviewed        bool            `json:"reviewed,omitempty"`
	Checklist       *[]string       `json:"checklist,omitempty"`
	CurrentNote     *string         `json:"current_note,omitempty"`
	Blocker         *string         `json:"blocker,omitempty"`
	WaitingFor      *string         `json:"waiting_for,omitempty"`
	CurrentItemID   string          `json:"current_item_id,omitempty"`
	CompleteItemIDs []string        `json:"complete_item_ids,omitempty"`
	SkipItemIDs     []string        `json:"skip_item_ids,omitempty"`
	SkipReason      string          `json:"skip_reason,omitempty"`
	AddItems        []string        `json:"add_items,omitempty"`
	IdempotencyKey  string          `json:"idempotency_key,omitempty"`
	IdempotencyHash string          `json:"-"`
}

type StartResult struct {
	Task     Task         `json:"task"`
	Run      AgentRun     `json:"run"`
	Handoffs []RunHandoff `json:"handoffs,omitempty"`
}

type ClaimRequest struct {
	ExpectedVersion int64  `json:"expected_version"`
	Agent           string `json:"agent,omitempty"`
	Client          string `json:"client,omitempty"`
	AgentSessionKey string `json:"agent_session_key,omitempty" jsonschema:"Opaque stable identifier for this agent session; reuse it across task starts and claims in the same session"`
	IdempotencyKey  string `json:"idempotency_key,omitempty"`
	IdempotencyHash string `json:"-"`
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

// ListTasksRequest selects one page of tasks. Cursor is the NextCursor of the
// previous page; callers keep the same filters while following it.
type ListTasksRequest struct {
	Statuses   []TaskStatus
	Visibility TaskVisibility
	Limit      int
	Cursor     string
}

// TaskPage is one page of a task listing. An empty NextCursor means the listing
// is exhausted; a page may hold fewer than Limit tasks while NextCursor is set.
type TaskPage struct {
	Tasks      []Task
	NextCursor string
}

func IsTaskVisibility(value TaskVisibility) bool {
	switch value {
	case VisibilityPrivate, VisibilityTeam, VisibilityAgent:
		return true
	default:
		return false
	}
}

func IsMessageKind(value MessageKind) bool {
	switch value {
	case MessageNote, MessageInstruction, MessageQuestion, MessageAnswer:
		return true
	default:
		return false
	}
}
