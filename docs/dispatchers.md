# Dispatchers and execution backends

**Status: proposed.** This note defines how Taskboard work reaches different
execution mechanisms, such as a host-local scheduler or Kubernetes Jobs on
autoscaled nodes. It records decisions and the gaps that must close before the
contract is complete. Sections marked *gap* describe behavior that does not
exist yet.

## Principle

Taskboard is the queue. It never pushes work into an external queue and never
knows how a task is executed. Each execution mechanism runs a **dispatcher**
that pulls the work routed to it, launches it in that mechanism's own way, and
reports through the existing run contract in
[Agent integrations](agent-integrations.md).

```text
                 ┌────────────── Taskboard ──────────────┐
people, agents → │ tasks · requirements · runs · leases   │
notification     │ escalations · controls · handoffs      │
clients          └───────▲───────────────────▲────────────┘
                         │ pull, claim,      │
                         │ heartbeat         │
               host-local dispatcher   Kubernetes Job dispatcher
                         │                   │
                   local process        Job → pod on an
                                        autoscaled node
```

Adding a mechanism means writing a dispatcher. It does not change Taskboard's
schema, its notification clients, or other dispatchers.

## Roles

- **Dispatcher**: a long-running process that advertises, lists pickup work,
  claims it, launches a worker, maps controls to its mechanism, and watches the
  worker's liveness. Each dispatcher has its own agent principal.
- **Worker**: the process that performs the task (for example a coding agent in
  a pod). It reports checklist progress, messages, references, handoffs,
  evidence, and escalations against the run the dispatcher claimed.

A dispatcher must have its own principal because Taskboard keeps one
advertisement row per principal (`worker_advertisements.principal`) and counts
advertised capacity against that principal's active runs. Two dispatchers
sharing a principal overwrite each other's advertisement.

## Routing

Each mechanism serves one runner token in the `runner:` namespace, for example
`runner:local` or `runner:k8s-job`. A task that must run on a given mechanism
carries that token as an operational requirement, alongside any others such as
`repo:org/name` or `harness:codex`. A dispatcher advertises its runner token
plus the capabilities that its workers actually provide.

Matching is the existing subset rule: a task is offered to a dispatcher only
when every requirement appears in that dispatcher's live advertisement. Two
consequences matter here:

- A task with **no** requirements matches every dispatcher. In an instance with
  more than one dispatcher, untagged work goes to whichever polls first.
- Requirements are human-maintained. Agents, including integrations that create
  tasks on a person's behalf, cannot set them.

*Gap: default routing.* Add an instance-level `TASKBOARD_DEFAULT_REQUIREMENTS`
applied when an agent-pickup task is created without requirements, so an
instance with one execution backend needs no per-task tagging. Per-project
defaults can follow if one instance serves several backends. Creation-time
defaults are recorded as normal requirements, remain human-editable, and keep
`last_edited_by` provenance unchanged.

*Gap: integration-requested routing.* Integrations such as a chat bridge may
need to create pickup work for a specific runner. Allow an agent policy to
name the requirement tokens that principal may set at creation, rather than
opening requirement edits to all agents.

## Dispatch lifecycle

1. **Advertise.** Call `worker_advertise` with the runner token, capabilities,
   capacity, and a TTL, and renew it before expiry. Capacity is the
   mechanism's concurrency budget. For autoscaled clusters this is the job
   budget, not the number of nodes currently present.
2. **List.** Call `task_list` with `visibility: "agent"` and follow
   `next_cursor` until it is absent. Readiness, dependencies, and matching are
   already applied before the page limit.
3. **Admit.** Apply the dispatcher's own policy before claiming, such as the
   provenance allowlist in
   [shared-task edit provenance](agent-integrations.md#shared-task-edit-provenance).
4. **Claim.** The dispatcher claims the task with its stable
   `agent_session_key`. The claim is the exclusive dispatch decision: Taskboard
   serializes it with the task version, so concurrent dispatcher replicas and
   repeated polls cannot launch the same task twice.
5. **Launch.** Start the worker with the task ID, run ID, and the handoffs
   returned by the claim. Make the launch idempotent on the run ID (for
   example a Kubernetes Job named from it) so a dispatcher restart re-attaches
   instead of launching again.
6. **Bridge startup.** While the worker is starting, the dispatcher heartbeats
   the run, because node provisioning can exceed the default two-minute lease.
   It reports a concise note such as "Waiting for capacity" so the board shows
   why an active run has no checklist progress yet.
7. **Run.** Once the worker is running it heartbeats and reports progress
   itself. The dispatcher stops heartbeating that run but keeps watching the
   worker's liveness.
8. **Finish.** The worker completes, blocks, waits, or escalates through the
   normal tools. If the worker exits without a terminal transition, the
   dispatcher writes a handoff with what it observed (exit status, not logs)
   and lets the lease expire. Expiry marks the task stale; Taskboard never
   retries automatically.

Claiming in the dispatcher rather than in the worker keeps dispatch exclusive
and makes pending capacity visible as an active run. The cost is that time
waiting for a node counts toward the run's maximum duration, which is measured
from the claim. Size `max_run_seconds` in the dispatcher principal's policy to
include worst-case provisioning time.

### Worker identity

Run mutations require `run.agent` to equal the calling principal, so today the
worker must authenticate as the dispatcher's principal. On a cluster that means
distributing the dispatcher's credential to worker pods, for example through a
projected secret or workload identity.

*Gap: run-scoped worker credentials.* Let a dispatcher mint a short-lived token
bound to one task run, accepted for that run's heartbeat, progress, message,
reference, handoff, evidence, usage, escalation, and completion calls only.
It expires when the run ends. This removes long-lived dispatcher credentials
from worker environments and fits the individually authorized agents described
in [workplace readiness](workplace-readiness.md).

## Controls

Dispatchers own control handling because they are the only component that can
act on the mechanism. Follow the acknowledged lifecycle in
[Acknowledged run controls](agent-integrations.md#acknowledged-run-controls).

| Control | Before the worker runs | While the worker runs |
| --- | --- | --- |
| cancel | Delete the pending worker, then complete the control | Signal the worker (for example SIGTERM), wait for its safe point, then complete |
| pause | Delete the pending worker, then complete | Ask the worker to reach a safe point and write a handoff, then complete |
| resume, retry | Not applicable | Not applicable: Taskboard queues the task and the next poll claims a fresh run |

Controls target an immutable run. A dispatcher must poll `task_control_list`
as well as reading heartbeat responses, because resume and retry target ended
runs.

## Escalations and approvals

A blocking escalation ends the run, so the worker exits. When a person answers,
Taskboard queues the task and the dispatcher claims a fresh run on its next
poll. The new worker reads the escalation answer and prior handoffs from the
claim. Workers on ephemeral infrastructure must therefore carry state through
handoffs and delivery references (branch, commit, pull request), not local
disk.

This lets notification clients present questions and approvals without knowing
which mechanism runs the task. A notification card's action answers the
Taskboard escalation; the owning dispatcher resumes the work.

Approval-style decisions need more than an ordinary answer:

- *Gap: delegated answers.* Escalation answers require a human principal with
  task write permission. A notification service that forwards a person's
  button press needs a delegated path, comparable to the existing MCP human
  delegation, that records the verified person rather than the service.
- *Gap: restricted answerers.* An escalation should optionally name the people
  or group allowed to answer it, so approving a remediation can be narrower
  than general task membership.
- *Gap: decision expiry.* An escalation should optionally expire, after which
  answers are rejected and the worker's proposal needs a new decision.

The immutable question message already binds an answer to the exact proposal
text, so a separate proposal fingerprint is not needed.

## Shared dispatcher library

The advertise, list, claim, heartbeat, control, and handoff loop is identical
across mechanisms. It belongs in one versioned Go module with a small backend
interface:

```go
type Backend interface {
	// Launch starts a worker for the run. It must be idempotent on runID.
	Launch(ctx context.Context, run Run) error
	// State reports whether the worker is pending, running, or exited.
	State(ctx context.Context, runID string) (WorkerState, error)
	// Stop signals the worker and returns once it has stopped or the
	// context ends.
	Stop(ctx context.Context, runID string, reason StopReason) error
}
```

Reference backends: a host-local process runner and a Kubernetes Job runner.
Mechanism-specific policy, such as node selectors, resource requests, sandbox
settings, and budgets, stays in each backend's configuration.

## Notification sinks

Taskboard's own Web Push and desktop notifications remain the default. A
deployment that runs a separate notification service should be able to send
escalations, stale runs, and completions there instead, without Taskboard
depending on it. *Gap:* the existing signed webhook (`TASKBOARD_WEBHOOK_URL`)
is the integration point to evaluate first; a dedicated sink would be added
only if the webhook's event set or payload is insufficient.

## Migration order

1. Close the default-routing gap and document runner tokens.
2. Extract the dispatcher library and build the host-local backend.
3. Close the delegated-answer, restricted-answerer, and expiry gaps, then move
   approval decisions from notification cards into escalations.
4. Build the Kubernetes Job backend and run-scoped worker credentials.
5. Retire per-application command queues that duplicate runs and leases.
