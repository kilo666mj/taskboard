# Design

## Domain boundary

Taskboard owns durable workflow state, transition validation, subscriptions,
and notification policy. Switchboard owns MCP composition and transport. A
task outlives any individual agent process; a run represents one agent's leased
participation in that task.

## Planning and execution lifecycle

- Capture creates a `queued` task. Only its title is required; it has no run or
  lease and defaults to the `General` section.
- “Start myself” changes the task to `active` and records the browser identity as
  owner without creating an agent run.
- “Assign to agent” records the intended owner while leaving the task queued.
  Assignment is planning metadata, not a claim that an agent process exists.
- An agent claim changes the task to `active`, creates the leased run, and makes
  the first open checklist item current when one exists.
- Sections are flat labels on tasks. Existing and unsectioned tasks migrate to
  `General`; section ordering is deterministic rather than separately stored.
- Project is an optional planning association. Repository remains a separate,
  optional development-work locator instead of being overloaded as a project.
- Priority, due date, and defer-until affect views and reminders but do not
  change execution status. Blocked and waiting retain their required reasons.
- Completing a daily, weekly, or monthly recurring task transactionally creates
  the next queued occurrence and resets its copied checklist.
- Named templates store reusable task metadata and checklist labels without
  creating a task or run.
- Visibility is independent of assignment. Private tasks are creator-only,
  team tasks are shared with people and explicitly assigned agents, and agent
  pickup tasks form a claimable queue. The immutable creator can publish a
  private task and later make it private again when no agent run is active.

## State model

- `tasks`: user-visible unit of work and aggregate status.
- `checklist_items`: ordered, required-by-default steps.
- `agent_runs`: agent/client identity, status, heartbeat, and lease.
- `events`: immutable, concise state transitions for audit and streaming.
- `task_messages`: immutable human↔agent conversation content, independently
  paginated so conversation does not cause task-version conflicts.
- `message_receipts`: explicit per-run observation and acknowledgement state;
  fetching or returning a message never marks it delivered.
- `push_subscriptions`: browser push endpoints and their public encryption keys.
- `task_templates`: named reusable task and checklist definitions.

Task versions are monotonically increasing. A mutating request carries the
version it observed. A mismatch returns a conflict and requires a fresh read.
Tasks also carry stable per-section sort values; moving a task swaps values
under optimistic concurrency, and changing section places it at the end.
Messages have their own immutable ULIDs and do not increment the task version.
Edits append a replacement linked through `supersedes_id` rather than mutating
history. Run-targeted messages never transfer to a replacement run; task-level
messages may be delivered to it until that run records its own receipt.

Structured escalations are decisions, not a fifth message kind. Each escalation
links an agent-authored question message to its choices and recommendation, and
later to one immutable human answer message. A non-blocking escalation leaves
the task and run active. A blocking escalation atomically changes the task to
`waiting`, records what is needed, and ends the current run so its lease can no
longer be renewed. Answering it changes the task to `queued`; it never revives
the ended run. A controller resumes execution by claiming a fresh replacement
run, while the escalation and both messages remain attached to the task.

Run controls are durable requests, not imperative process signals. Every
`pause`, `cancel`, `resume`, or `retry` targets one immutable run and records a
lifecycle of `requested` → `acknowledged` → `accepted` → `completed`, or a
terminal `rejected`/`expired` outcome. Creating or accepting a request does not
change task or run state. The controller applies the requested transition only
when it reports completion, making acknowledgement and outcome visible even
when the worker cannot comply.

Pause and cancel target a currently active run. Resume targets a waiting or
blocked task's ended run, and retry targets a stale or cancelled task's ended
run. A completed pause ends the run and waits the task; a completed cancel ends
the run and cancels the task. Completed resume and retry queue the task for a
fresh claim rather than reviving the target run. Controls never transfer to a
replacement run.

Only an authenticated human with task-write permission may originate a control
request. The target run fixes the responsible agent principal; a controller
with `task:control` may poll and update only controls addressed to that
principal and cannot retarget or invent them. The explicit human request is the
authorization for pause, cancel, resume, or retry, so completing it does not
also require the autonomous `task:sensitive` capability. Controllers may reject
a request with a concise user-visible reason. Optimistic task versions are
checked again at completion so a delayed control cannot overwrite newer task,
run, or terminal state.

External delivery references are immutable typed pointers, not synchronized
copies. Supported kinds are `linear`, `repository`, `worktree`, `branch`,
`commit`, `pull_request`, `ci_run`, `deployment`, `screenshot`, and `review`.
Each reference records a concise label, locator, optional HTTPS URL, creating
principal, optional source run, and creation time. The server derives human or
agent provenance from authentication; agents may attach references only from
their active owned run. URLs are links only when they parse as absolute HTTPS
URLs with a host and no embedded credentials. Local paths, branch names, SHAs,
and SSH remotes remain plain-text locators.

Taskboard remains authoritative for execution state, Linear for product
requirements, and GitHub or the referenced delivery system for code, review,
CI, and deployment state. Integrations append a reference or selected audit
event; they do not bidirectionally copy ticket bodies or status fields.
Delivery milestones are a read model derived from reference kinds and selected
Taskboard events, not additional mutable task statuses.

Run handoffs are append-only structured continuation snapshots scoped to an
immutable run. Agents may add checkpoints without changing the task version;
terminal and stale snapshots are generated from persisted facts when a final
checkpoint is absent. Claims expose prior handoffs so a replacement can resume
without treating free-form status notes as durable execution state.

Completion contracts are human-owned acceptance gates separate from checklist
execution. Agent evidence is append-only and remains submitted until a human
verifies or rejects it. Required gates block the done transition unless they
are satisfied or explicitly waived with a recorded reason.

The session bridge stores only controller-authenticated availability and
operator action requests keyed by immutable run ID. The controller retains the
private run-to-thread mapping and performs open/resume actions; Taskboard never
stores or follows an agent-supplied session URL.

Dependencies are a minimal cycle-safe `blocked_by` graph. Readiness is derived:
every prerequisite must be done. It is not a task status, and is checked both
when listing pickup work and transactionally during claim and completion.

Operational worker matching uses stable lowercase task-requirement tokens and
short-lived, controller-authenticated worker advertisements. Capacity and
operational capabilities filter pickup candidates; they remain strictly
separate from authorization capabilities, which continue to control what an
authenticated principal may do.

Product analytics are aggregate read models over existing task, run,
escalation, control, and reference records. Optional usage records contain only
numeric token/cost totals plus bounded provider/model labels. Task deletion
cascades usage records, and no metric path stores prompts, reasoning,
credentials, logs, or arbitrary tool output.

## Review and deferred scope

Daily review targets unplanned, overdue, stale, blocked, and waiting tasks that
have not been reviewed in the last day. Weekly review broadens that to queued
work and the next seven days, with a seven-day acknowledgement window.

Tags, nested projects, and custom statuses were evaluated for this milestone
and deliberately deferred. Current usage has not shown a need
that outweighs the added capture and filtering complexity; flat sections,
optional projects, and the existing execution states cover the observed work.

## Notification policy

Each completed checklist step creates a quiet notification. Transitions to
waiting or done also create quiet notifications, with the final step and done
transition combined into one notification. Blocked and stale transitions are
urgent. The Tauri process receives the live SSE event and invokes the platform
notification plugin. The PWA receives the same policy through encrypted Web
Push, including when its window is closed.
Each device stores separate preferences for agent progress, due reminders, and
optional summaries. Background progress delivery honors the server-side device
preference; reminder and summary sweeps run in the client and deduplicate by
task and local calendar date.

## Trust model

The service accepts one deployment bearer secret and derives a distinct
HTTP-only browser-session value from it. A production reverse proxy terminates
TLS. Switchboard holds the bearer secret as an environment-backed upstream
credential. Task text is untrusted display data and is always inserted using
DOM text nodes.

The initial shared credential proves the calling installation, not a particular
agent. A later Switchboard identity-forwarding extension can sign client and
run claims without changing the task model.

The current deployment is one trusted workspace. OIDC and Cloudflare Access
browser identities enforce creator-only private tasks across REST, SSE, and Web
Push. Private tasks are excluded from MCP entirely. Agent-pickup tasks and
explicit team assignments are enforced through MCP, but a shared deployment
bearer plus client name is not a cryptographically individual agent credential.
Workspace roles and tenant isolation remain future work documented in
`docs/workplace-readiness.md`.
