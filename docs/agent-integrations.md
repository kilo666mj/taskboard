# Agent integrations

Taskboard separates authenticated identity from presentation. The `agent`
field on a run is the trusted principal established by the server. `client` is
self-reported diagnostic metadata. `callsign` is friendly display metadata
assigned by Taskboard and may be renamed by a signed-in operator. Integrations
must use the principal and run ID—not the callsign or client label—for audit,
authorization, and resume decisions.

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

## Friendly run identity

Taskboard assigns each run a short callsign such as `Maple` or `Orbit`. Active
runs cannot share a callsign, including names that differ only by case. The
associated `tone` is a stable palette slot derived from the immutable run ID;
interfaces must also show text or initials so color is never the only cue.

Operators can rename a run through the task interface. Renaming changes only
display metadata, increments the task version, and writes an audit event. The
authenticated principal, MCP client metadata, and run ID remain unchanged and
available in agent details.

Opening the corresponding Codex session is deliberately a controller concern.
A future Switchboard/Codex bridge may associate the immutable run ID with a
trusted thread reference and focus or resume it when requested. Taskboard must
not accept or navigate to an arbitrary URL supplied by an agent.
