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

## State model

- `tasks`: user-visible unit of work and aggregate status.
- `checklist_items`: ordered, required-by-default steps.
- `agent_runs`: agent/client identity, status, heartbeat, and lease.
- `events`: immutable, concise state transitions for audit and streaming.
- `push_subscriptions`: browser push endpoints and their public encryption keys.
- `task_templates`: named reusable task and checklist definitions.

Task versions are monotonically increasing. A mutating request carries the
version it observed. A mismatch returns a conflict and requires a fresh read.
Tasks also carry stable per-section sort values; moving a task swaps values
under optimistic concurrency, and changing section places it at the end.

## Review and deferred scope

Daily review targets unplanned, overdue, stale, blocked, and waiting tasks that
have not been reviewed in the last day. Weekly review broadens that to queued
work and the next seven days, with a seven-day acknowledgement window.

Tags, dependencies, nested projects, and custom statuses were evaluated for
this milestone and deliberately deferred. Current usage has not shown a need
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

The current deployment is one trusted workspace. OIDC identities improve
attribution but do not create tenant, project, or task authorization boundaries,
and the deployment bearer identifies an installation rather than an individual
agent. The staged design for workplace identity, roles, and workspace isolation
is documented in `docs/workplace-readiness.md`.
