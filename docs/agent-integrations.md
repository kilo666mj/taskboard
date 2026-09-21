# Agent integrations

Taskboard separates authenticated identity from presentation. The `agent`
field on a run is the trusted principal established by the server. `client` is
self-reported diagnostic metadata. `session_id` is Taskboard's public identifier
for one agent session, while `callsign` is its friendly display name. Controllers
send an opaque `agent_session_key` on starts and claims; Taskboard stores only a
hash of that key. Integrations must use the principal and task run ID—not the
session ID, callsign, or client label—for audit, authorization, and resume
decisions.

## Incremental checklist progress

An agent should start or claim work before performing it, then keep the returned
task and run IDs. After each bounded checklist step succeeds, immediately call
`task_update` with:

- that step in `complete_item_ids`;
- the latest task `expected_version`;
- the next step in `current_item_id`, when one exists;
- the run ID and a concise user-visible note when it adds useful context.

Do not accumulate several completed steps and send them only when the task is
finished. A failed step remains active; record a blocker or waiting state rather
than marking it complete. `task_complete` remains the final transition and is
rejected while a required checklist item is open.

`task_heartbeat` renews the run lease and returns a `progress` object with the
last checklist transition, its age, counts, current item, and a staleness
threshold. A stale hint asks the agent to report work it actually completed. It
is advisory: heartbeat never changes item state and the server never guesses
that a step is done.

## Task conversation and acknowledgement

Conversation content is append-only and independent of the task's optimistic
version. Messages use four kinds: `note`, `instruction`, `question`, and
`answer`. Workflow transitions and checklist progress remain events rather than
duplicate status messages.

A message may target the task or one immutable run ID. Task-level messages are
eligible for delivery to a replacement run; run-targeted messages never move to
a replacement. Agent-authored messages identify their source run separately
from the delivery target. Replies reference the message they answer, while an
edit creates a new message whose `supersedes_id` points to the prior version.

Heartbeat responses include messages pending for that run. Delivery is
repeat-until-receipt: ordinary notes stop appearing after the run explicitly
records them as observed, while messages requiring acknowledgement continue to
appear until explicitly acknowledged. Fetching a message does not itself prove
delivery or change receipt state. Integrations should persist received IDs,
apply instructions only after validating the current task and run context, and
send receipts idempotently.

Messages never change checklist items, acceptance criteria, task status, or run
state by themselves. In particular, words such as “pause” or “cancel” in prose
are not control requests. Message bodies are user-visible plain text; do not
store prompts, reasoning, credentials, secrets, or arbitrary tool output.

## Structured escalation and resume

Use `task_escalate` when a question needs an explicit decision record. Supply
the active `run_id`, current `expected_version`, question, optional choices and
recommendation, and whether the question is blocking. The escalation creates an
ordinary immutable question message plus structured decision metadata.

A non-blocking escalation leaves the task and run active, so the controller
continues heartbeat and work. A blocking escalation is a successful terminal
operation for the current run: Taskboard atomically changes the task to
`waiting`, changes the run to `waiting`, expires its lease, and sets
`ended_at`. The controller must stop heartbeat and exit or suspend execution
after the call succeeds. Retrying the same idempotency key returns the original
escalation; it does not create another question.

A signed-in member answers through the task's decision form or
`POST /api/v1/tasks/{task}/escalations/{escalation}/answer`. The answer is an
immutable reply message. For a blocking escalation, Taskboard atomically queues
the task and clears `waiting_for`; it deliberately does not reactivate the old
run. A controller watching events or polling the task may then claim it with the
new task version. That claim creates a new immutable run ID. A controller may
map the new run to an existing suspended agent session, but authorization,
heartbeats, receipts, and subsequent updates must all use the new run ID.
Run-targeted messages for the ended run remain historical and are not delivered
to the replacement; task-level context and the recorded answer remain visible.

There is no implicit resume on an answer. This prevents a human decision from
reviving a process that has exited, lost its lease, or been replaced. If the
answer arrives after some other authorized transition moved the blocking task
away from `waiting`, resolution returns a conflict and requires review rather
than overwriting newer state.

## Acknowledged run controls

Pause, cancel, resume, and retry are requests to the controller, not immediate
kills or task edits. Active-run heartbeats return `pending_controls` addressed
to that exact run. Controllers must also poll `task_control_list`, because
resume and retry target an ended run that can no longer heartbeat.

Process every request through the explicit lifecycle:

1. Change `requested` to `acknowledged` as soon as the controller has durably
   received it.
2. Change `acknowledged` to `accepted` when the controller can comply, or to
   `rejected` with a concise reason when it cannot.
3. After reaching a safe point and actually applying the operation, change
   `accepted` to `completed` with the latest task `expected_version`.

Neither acknowledgement nor acceptance changes the task or run. Completion is
the atomic workflow boundary: pause waits the task and ends the active run;
cancel cancels both; resume queues a waiting or blocked task; retry queues a
stale or cancelled task. Resume and retry never revive the target run—the
controller must claim a fresh run before execution. Controls are tied to their
immutable target run and are excluded from replacement-run heartbeats. A
completion that races with a stale sweep, replacement claim, or terminal task
returns a conflict rather than overwriting newer state.

Open controls expire after 24 hours and remain visible with an `expired`
outcome. Use stable idempotency keys for every lifecycle transition. Request
reasons and outcome notes are user-visible; do not put prompts, reasoning,
credentials, or raw tool output in them.

## Delivery references and milestones

Use `task_reference_add` as delivery artifacts appear. References are immutable
and typed as `linear`, `repository`, `worktree`, `branch`, `commit`,
`pull_request`, `ci_run`, `deployment`, `screenshot`, or `review`. Agent-created
references require the active source `run_id`. A locator may hold an issue key,
path, branch, SHA, or provider identifier. A URL is optional, but when present
it must be an absolute HTTPS URL with a host and no embedded credentials.

`task_delivery_get` returns the reference set and an ordered milestone read
model. Milestones are derived from the first reference of each relevant kind
plus selected Taskboard lifecycle events such as claim/start and completion.
They do not create custom statuses and must not be written back as task state.

The integration boundary is intentionally one-way and selective:

- Linear remains authoritative for product requirements. Store the issue key
  and link; do not copy the full ticket or mirror Taskboard execution status
  back into Linear unless a separate, explicit policy chooses individual
  events.
- GitHub and the referenced CI/deployment providers remain authoritative for
  commits, pull requests, reviews, checks, and releases. Store durable links or
  identifiers and derive milestones; do not bidirectionally synchronize their
  state machines into Taskboard.
- Taskboard remains authoritative for claims, leases, checklists, escalations,
  controls, and completion. External webhook consumers may append references
  after verifying provenance, but must not overwrite this workflow state.

Labels and locators are user-visible. Never attach signed URLs, credentials,
secrets, prompts, raw logs, or tool output. Worktree paths and SSH remotes are
plain-text locators rather than clickable links.

## Run handoffs

Controllers should call `task_handoff_add` after meaningful checkpoints and
before an orderly stop. A handoff is an append-only run-scoped snapshot with the
last completed step, workspace and branch, commits and pull requests, concise
validation and review findings, blocker, and recommended next action. Send only
facts useful to a replacement worker; never include prompts, reasoning,
credentials, raw logs, or arbitrary tool output.

When a run ends without a final handoff, Taskboard generates one from facts it
already owns. Lease expiry creates a `stale` snapshot from the persisted
checklist, blocker/waiting fields, and typed delivery references; it does not
invent progress. `task_claim` and `task_get` include prior handoffs, and
`task_handoff_list` remains available for explicit refresh. A replacement run
should inspect these records before changing the checklist or workspace.

## Completion contracts

Acceptance criteria are human-owned and independent from the agent's mutable
execution checklist. Operators may require a pull request, green CI,
validation, resolved review findings, deployment, explicit approval, or a
custom gate. Agents can read the contract with `task_completion_get` and submit
concise evidence with `task_completion_evidence_submit`; evidence tied to a
provider must reference an existing typed task reference of the expected kind.

Submitted evidence is not self-certifying. A human verifies or rejects it, or
explicitly waives a requirement with a reason. `task_complete` returns a
validation error naming every required gate that is neither satisfied nor
waived. Controllers must treat that rejection as durable workflow state and
must not emit a final success response until Taskboard accepts completion.

## Dependencies and readiness

`blocked_by` edges model ordered and cross-repository work without adding task
statuses. `task_dependency_list` returns each prerequisite's current status and
the derived `ready` value. Only `done` satisfies a dependency; cancellation
does not. Queued or stale work with unmet dependencies is excluded from agent
pickup results, and claim and completion recheck readiness transactionally.

Dependency edits are human-owned, task-versioned, and cycle-safe. Deleting a
task removes its incident edges through referential integrity. Controllers
should not reinterpret an unmet dependency as the execution `blocked` status.

## Worker matching

Task requirements are human-maintained lowercase tokens such as
`repo:org/name`, `browser`, `playwright`, `aws`, `kubernetes`,
`screenshot`, `harness:codex`, or `model:gpt-5`. Controllers call
`worker_advertise` with their current operational tokens, capacity, and a short
TTL. Advertisements are ephemeral scheduling input, not durable authorization.

Agent pickup lists omit queued or stale tasks whose requirements are not a
subset of the live advertisement. Claim rechecks the match and advertised
capacity. Security capabilities such as `task:claim` remain an independent
server policy: advertising an operational token never grants permission, and
having permission never implies that the worker can perform required work.

## Usage accounting and analytics

Controllers may call `task_usage_record` during an active owned run with only
numeric input/output token totals and an optional estimated cost in micros.
Provider and model labels are short metadata fields. Never send prompts,
reasoning, credentials, raw logs, tool output, or per-request payloads.

The operator analytics view derives queue age, execution duration, stale-run
rate, escalation rate, completed retry count, review-reference cycles, and
completion rate from existing durable state over a selected time window. It
adds numeric usage totals when present. These are operational indicators, not
billing-grade measurements: queue age covers currently queued work, execution
duration covers ended runs, review cycles use typed review references, and
completion rate is done divided by done plus cancelled. Deleted tasks cascade
their usage and disappear from subsequent aggregates; ordinary retention uses
the same deletion boundary.

## Friendly agent-session identity

Taskboard assigns each agent session a short callsign such as `Maple` or
`Orbit`. A controller must reuse one opaque `agent_session_key` across all task
starts and claims performed by that session. Taskboard hashes the key before
storage and returns a server-issued `session_id`; neither value is an authority
or a controller thread reference. Separate sessions cannot share a callsign,
including names that differ only by case. The associated `tone` is a stable
palette slot derived from the server-issued session ID; interfaces must also
show text or initials so color is never the only cue. Clients that omit the key
retain the legacy behavior of receiving a new identity for every task run.

Operators can rename an agent session through any of its task runs. The rename
applies to every run in that session, changes only display metadata, increments
the selected task's version, and writes an audit event. The authenticated
principal, MCP client metadata, session ID, and run IDs remain unchanged and
available in agent details.

Opening the corresponding agent session remains a controller concern. The
controller keeps the private immutable-run-to-thread mapping and advertises
only a label, expiry, and whether open or resume is available through
`task_session_register`. Taskboard stores no session URL, thread identifier, or
secret.

An operator's Open or Resume action creates an authenticated request bound to
the advertised run and controller principal. Controllers poll
`task_session_request_list`, acknowledge the request, perform the action using
their private mapping, then complete or reject it with a concise outcome. An
expired or unavailable advertisement disables the UI action. Requests and
transitions are audited using run IDs and action metadata only; Taskboard never
navigates to arbitrary agent-supplied URLs.
