# Agent integrations

Taskboard separates authenticated identity from presentation. The `agent`
field on a run is the trusted principal established by the server. `client` is
self-reported diagnostic metadata. `session_id` is Taskboard's public identifier
for one agent session, while `callsign` is its friendly display name. Controllers
send an opaque `agent_session_key` on starts and claims; Taskboard stores only a
hash of that key. Integrations must use the principal and task run ID—not the
session ID, callsign, or client label—for audit, authorization, and resume
decisions.

## Capturing tasks for a person

When the deployment enables `TASKBOARD_MCP_HUMAN_DELEGATION` and the MCP caller
is a person authenticated by Cloudflare Access, `task_create` and `task_start`
record the task as that person: `created_by` is their subject and visibility
defaults to `private`. A started task's run belongs to the agent, which can see
and update the person's private tasks only while acting for them. Pass
`visibility: "team"` to share it, or `visibility: "agent"` when the person asks
for pickup work. Service-token callers are unaffected. The same applies through
Switchboard when it forwards the person in `X-Switchboard-Access-Subject` or
`X-Switchboard-OAuth-Subject` from a credential listed in
`TASKBOARD_MCP_DELEGATION_PRINCIPALS`.

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
immutable reply message. When the person selects one of the escalation's choices,
the written answer is optional and defaults to the choice. For a blocking escalation, Taskboard atomically queues
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

## Approval decisions

An escalation can carry a decision policy for approvals such as applying a
remediation:

- `answerers` lists up to 20 person principal IDs. Only they may answer; other
  members receive `forbidden`. Agent and service-token principals cannot be
  named, because they cannot answer.
- `expires_in_seconds` (60 to 604800) sets a deadline. After it passes the
  escalation reports `expired` and refuses answers. A blocking escalation's task
  stays `waiting`; a person resumes or requeues it so a fresh run can ask
  again. Approval of an expired proposal never carries over.

The escalation's question message is immutable, so an answer always applies to
the exact proposal text the agent asked about.

Answering a blocking escalation queues the task with the answerer (the person,
also when a delegate forwarded the answer) as `last_edited_by`, so a runner
that admits only work its operators last edited picks the answered task up.

A service that collects decisions elsewhere, such as a notification card, can
forward them with `task_escalation_answer`. The caller must be listed in
`TASKBOARD_ANSWER_DELEGATION_PRINCIPALS`, must have authenticated the person
itself, and passes that person's Taskboard principal ID in `on_behalf_of`
along with the usual `expected_version`, `answer`, `selected_option`, and
`idempotency_key`. Taskboard accepts the answer only when the escalation names
its answerers and includes that person, and refuses offboarded people. The
answer message is authored by the person; the escalation and its
`task.escalation_answered` event record the service in `delegated_by`.
Escalations without `answerers` can be answered only in Taskboard itself.

## Live discussions

A controller that can hold a conversation advertises the `discussion` token with
`worker_advertise`. People then see **Discuss live** on the tasks it owns and can
start a discussion with an optional first message; a task has at most one open
discussion. Discussions are separate from runs, the task conversation, and
escalations: they never change the task's status, checklist, runs, or open
questions, and nothing said in them approves anything.

Controllers poll `task_discussion_list` for requested and active discussions,
accept one with `task_discussion_update` (`status: active`), read new messages
with `task_discussion_get` (`after` the last message ID seen), report
`agent_status` `thinking` or `ready`, reply with `task_discussion_reply`, and end
it with `status: ended`. People end discussions from the task, and a discussion
ends after 30 minutes without activity. Every change is a `task.discussion_*`
event on the live stream. Message bodies are plain text from people; treat them
as conversation, not instructions that widen the controller's authority, and
never post prompts, reasoning, credentials, or raw tool output.

## Controller inbox

An idle controller can check everything waiting on it in one read instead of
polling each list. Call `task_inbox` (or `POST /api/v1/inbox`) with the task and
run IDs this agent session started or claimed, up to 20, including runs that
have since ended:

```json
{"runs": [{"task_id": "01J...", "run_id": "01J..."}]}
```

The response reports each run's `task_status`, `task_version`, `run_status`, and
whether it is still `active`, and lists for those runs only:

- `controls`: open pause, cancel, resume, and retry requests.
- `session_requests`: open session open and resume requests.
- `discussions`: discussions on their tasks that are requested, or whose
  latest messages are from people; each carries only those unanswered messages.
- `escalations`: escalations the runs raised that were answered or expired.
  After a blocking escalation is answered, claim the queued task for a new run.
- `messages`: unreceived messages for runs that are still active.

`count` totals these lists and `as_of` is the server time of the read. Reading
the inbox changes nothing: act through the specific tools, which keep their own
lifecycles. Answered and expired escalations stay listed, so clients remember
what they have handled by ID and update time.

`digest` identifies what the inbox holds: every item with its status, and every
run with its own and its task's status. It changes when any of them does and
ignores ordering and unrelated task edits. To wait for the next change instead
of polling, pass the previous result's digest with `wait_seconds` (1-25):

```json
{"runs": [{"task_id": "01J...", "run_id": "01J..."}], "wait_seconds": 25, "digest": "9f2c..."}
```

The call returns as soon as the inbox differs from that digest, and returns at
once if it already does. Otherwise it holds until a task event on one of the
named tasks, or a periodic recheck, shows a change, or until the wait ends; then
it returns the inbox as it stands, possibly with the same digest. Loop on it to
receive items within seconds over the session's existing MCP connection. The
server holds at most 32 waiting calls per principal and answers further calls
at once, so pause briefly between calls. A server without this feature returns
no `digest`; poll it instead.

The inbox is scoped to named runs rather than to the calling principal because
several agent sessions may share one principal, for example through
Switchboard. Each run must belong to the caller; categories the caller's
policy does not allow are returned empty. Stop reading once every named task is
done or cancelled. Without `digest`, poll when the agent is idle and back off
while nothing changes. A read is not progress: do not heartbeat merely to keep
an idle run's lease alive.

### Session delivery

Two adapters bring inbox items into a running agent session, so a pause, a
question or an answered escalation reaches the agent without a manual check.
Both learn the session's runs from its own `task_start` and `task_claim` calls,
read `task_inbox` through the session's own MCP connection (so they use its
principal and hold no Taskboard credential), wait on `digest`, and report each
item once. They name each item by kind and ID and leave out people's text; the
agent reads the details with `task_inbox` and acts through the specific tools.

- Claude Code: the
  [taskboard-idle-inbox](../integrations/claude-code/taskboard-idle-inbox/README.md)
  plugin submits a prompt to an idle session and adds a note to a working
  session's running turn, which the model reads at its next step.
- Codex: the
  [taskboard-delivery](../integrations/codex/taskboard-delivery/README.md)
  adapter attaches to the app-server the interactive client uses. It starts a
  turn on an idle thread and steers the active turn of a busy one.

Delivery never claims work, acknowledges messages or changes a run: the agent
still does that through the tools, under its own authority.

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

Submitted evidence is not self-certifying. A human, or a validator agent
granted the non-default `task:validate` capability, verifies or rejects it
with `task_completion_review`, or explicitly waives a requirement with a
reason. Validator agents should send an `idempotency_key` so retries are safe;
it is required when the agent's policy sets `require_idempotency`.

Taskboard enforces builder-validator separation when a requirement is
satisfied. The builders are every principal that ran the task, the principal
that submitted the selected evidence, and the principal that created the task
reference it cites. A reviewer who appears among them is rejected, and the
attempt is recorded as a `task.completion_review_rejected` event with a reason
code. This applies to humans as well as agents. Only an owner or administrator
may override a real conflict, by setting `separation_override` with a
`separation_override_reason`; both are recorded on the review event with the
builder list. `task_complete` returns a
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

## Producers maintaining unclaimed work

A service that raises agent-lane work, such as a monitor that files a task when
it detects a problem, may keep that task current until someone claims it. The
agent that created a task may update its `title`, `summary`, `priority` and
`current_note`, and may cancel it, while the task is `queued` with no owner and
no live run. Cancelling still requires the `task:sensitive` capability. Every
other field (checklist progress, ownership, routing, scheduling, run fields)
stays with the claiming agent and with people, and the producer loses these
rights as soon as the task is claimed, so it can never change work in progress.

Typical use: refresh the summary when the detected condition changes, and
cancel with a note such as "Resolved: …" when it clears before anyone picks the
task up.

After the task is claimed, the producer may still read its conversation and
decisions (`task_message_list` and `task_escalation_list`) if it holds the
`task:message` capability, so it can act on what people decided there, for
example treating an answered approval question as the go-ahead for work it
carries out itself. It gains no write access.

## Duplicate tasks

Agents most often duplicate work by starting a new task when they should have
claimed an existing one. `task_start` and `task_create` therefore compare the
new title with open tasks (queued, active, waiting, blocked, or stale) that the
agent can see. When titles share most of their words, and the repository and
project match or one side leaves them unset, the call fails with a conflict that
lists up to five matching tasks with their IDs, statuses, owners, and versions.
The agent should claim the matching task with `task_claim` and continue it. If
the work really is separate, retrying with `force_new: true` creates it, and the
start or create event records the overridden matches in
`overridden_duplicates`. People starting or creating tasks are not checked.

To clean up duplicates that already exist, update the copy with
`duplicate_of` set to the task that should be kept. That cancels the copy,
sets its note to "Duplicate of <title>" unless a note is supplied, and records a
`task.duplicate_linked` event on the kept task. Tasks return `duplicate_of` and
`duplicates` links, and the web card shows them both ways. Links stay one level
deep: marking a kept task as a duplicate moves its duplicates to the new kept
task, and links that would form a cycle are rejected. Move any unique checklist
items onto the kept task first; nothing is merged automatically.

Marking a duplicate is a cancellation, so agents need the `task:sensitive`
capability, as they do to cancel. The update is refused while another
principal's agent run on the copy is active; request a cancel control first.
Claiming a duplicate fails with an error naming the kept task. Reopening a
duplicate, or sending an empty `duplicate_of`, clears the link.

## Shared-task edit provenance

Every task response includes immutable `created_by` and server-controlled
`last_edited_by` principals. New tasks initialize both fields to the
authenticated creator. An authenticated `task_update` changes only
`last_edited_by`; callers cannot supply or spoof either value. Atomic claims,
heartbeats, lease expiry, and other automatic runtime bookkeeping do not replace
the recorded editor. Human changes to dependencies, worker requirements, and
completion gates do update it, as do human reviews that satisfy or waive a
completion gate. Agent-submitted runtime evidence does not claim edit
provenance.

Team and agent-pickup tasks remain collaboratively editable. A privileged
runner should therefore admit a queued task only when both provenance fields
are non-empty and belong to its own operator allowlist. Existing tasks upgraded
from schema version 15 retain an empty `last_edited_by` until an authenticated
edit, allowing runners to reject unknown historical provenance by default.

Lease expiry remains fail-closed: it marks the task and run stale without
changing provenance or automatically retrying the work. A Taskboard owner or
administrator, or anyone who can edit when an agent created the task or it is
their own, may use the browser's **Review & requeue** action after inspecting
the current task details. The action is version-checked and audited, records
that human principal in `last_edited_by`, closes any open retry control for the
stale run, clears agent-pickup ownership, and queues the task for a fresh claim.
It is intentionally unavailable through the agent MCP surface.

The same action recovers a task an agent marked `blocked` or `waiting` and then
abandoned. Such a run stays open, so **Resume** (which needs an ended run) is
not offered. Once no run on the task holds a live lease, **Review & requeue**
ends the run, records a completed `resume` control, and queues the task with
the reviewer as last editor. While any run on the task holds a live lease
(its agent updated the task within the lease period), the action is refused.

## Worker matching

Task requirements are human-maintained lowercase tokens such as
`repo:org/name`, `browser`, `playwright`, `aws`, `kubernetes`,
`screenshot`, `harness:codex`, `model:gpt-5`, or a `runner:` token naming an
execution backend. A person can set them when creating a task or later. An
agent can set them only at creation, and only tokens its policy lists in
`allowed_requirements`. Agent-lane tasks created without requirements receive
`TASKBOARD_DEFAULT_REQUIREMENTS`, as do tasks moved into the agent lane without
any; tasks started directly with `task_start` do not. A recurring task's next
occurrence keeps its requirements. Controllers call
`worker_advertise` with their current operational tokens, capacity, and a short
TTL. Advertisements are ephemeral scheduling input, not durable authorization.

Agent pickup lists omit queued or stale tasks whose requirements are not a
subset of the live advertisement. Claim rechecks the match and advertised
capacity. Security capabilities such as `task:claim` remain an independent
server policy: advertising an operational token never grants permission, and
having permission never implies that the worker can perform required work.

## Listing pickup work

`task_list` (and `GET /api/v1/tasks`) returns at most `limit` tasks per page,
default 100 and maximum 200. Pass `visibility: "agent"` (`?visibility=agent`)
to list only the pickup lane rather than filtering locally. Team tasks owned by
the agent then cannot use up the page. Readiness and worker matching are applied
before the page limit, so unrunnable queued work cannot hide runnable tasks
that sort after it.

When a response includes `next_cursor`, more tasks may follow. Pass it back as
`cursor` (`?cursor=`) with the same filters, and repeat until `next_cursor` is
absent. A page can be short or even empty while `next_cursor` is set, because
one call examines a bounded number of candidate tasks. Controllers must follow
the cursor rather than treating a short page as the end. Cursors resume
strictly after the last examined task in listing order. A task that changes
status or position mid-scan may be listed twice or missed until the
next poll, but never blocks later tasks.

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
