# Workplace readiness

Taskboard's durable tasks, checklists, optimistic updates, agent leases, live
status, and append-only transition events form a useful control plane for teams
assigning work to agents. The present release intentionally targets one trusted
workspace. It must not be described or deployed as a tenant-isolated service.

## Current trust boundary

- Every permitted browser identity can see and change the shared board.
- OIDC identifies people and records their actions, but does not grant per-task
  or per-project permissions.
- All agents use one deployment-scoped MCP bearer credential. A task records the
  agent-provided name, not a cryptographically distinct agent principal.
- Sections and projects organize work; they are not authorization boundaries.

This model is appropriate for a small trusted team or a dedicated deployment per
team. Separate deployments are the safe isolation mechanism today.

## Changes for shared workplace deployments

### Work lanes

Work mode should expose three explicit lanes while keeping visibility and
assignment as separate server-side concepts:

- **Private work** is visible only to its creator and collaborators they invite.
- **Team work** is visible to members of the selected workspace or project and
  may be assigned to a person or an agent.
- **Agent queue work** is visible to eligible agents and is atomically claimed
  by one agent. Capability labels, concurrency limits, and lease expiry decide
  which agents may claim it and when abandoned work becomes available again.

These lanes must be enforced by every REST, SSE, and MCP query. A client-side
filter is a presentation choice, not an authorization boundary. Tasks should
store a visibility policy independently from an optional assignee so, for
example, a team-visible task can still be assigned to an agent without
disappearing from the team board.

1. **First-class principals and credentials.** Store individually revocable,
   hashed agent credentials with stable principal IDs, scopes, expiry, and
   last-used metadata. Bind every run and event to the authenticated principal
   instead of trusting a caller-supplied agent name.
2. **Workspace and project authorization.** Add workspace IDs to durable data,
   memberships, roles such as owner/admin/member/viewer, project-level grants,
   and server-side authorization on every REST, SSE, and MCP operation.
3. **Assignment policy.** Distinguish who may create, assign, claim, reassign,
   approve, and complete work. Make agent-queue claims atomic, use leases for
   recovery, and support capability labels and concurrency limits without
   allowing an agent to broaden its own access.
4. **Administrative lifecycle.** Add credential rotation and revocation,
   membership offboarding, OIDC group-to-role mapping, export/deletion,
   configurable retention, and an auditable administrative log.
5. **Operational controls.** Add quotas, rate limiting, metrics, audit export,
   webhook/integration delivery with retries, tested disaster recovery, and a
   documented availability model.

## Compatibility direction

The current schema can evolve without abandoning simple self-hosting: existing
installations can migrate into a generated default workspace, and the existing
deployment token can become an administrator-created agent credential. The
single-workspace interface should remain the default until an operator enables
additional workspaces and roles.
