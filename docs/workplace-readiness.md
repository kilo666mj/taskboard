# Workplace readiness

Taskboard's durable tasks, checklists, optimistic updates, agent leases, live
status, and append-only transition events form a useful control plane for teams
assigning work to agents. The present release intentionally targets one trusted
workspace. It must not be described or deployed as a tenant-isolated service.

## Current trust boundary

- Every permitted browser identity can see and change team and agent-pickup
  tasks. Private tasks are restricted to their immutable creator.
- OIDC or Cloudflare Access identifies people and enforces private-task access,
  but does not yet grant workspace roles or project permissions.
- Agents using the deployment-scoped MCP bearer share the canonical
  `agent:shared` principal. Their self-reported MCP client name is diagnostic
  run metadata and cannot become an audit actor. Cloudflare Access workloads
  use their verified Access subject as a distinct service principal.
- Sections and projects organize work; they are not authorization boundaries.

This model is appropriate for a small trusted team or a dedicated deployment per
team. Separate deployments are the safe isolation mechanism today.

## Changes for shared workplace deployments

### Implemented work lanes

Work mode exposes three explicit lanes while keeping visibility and assignment
as separate server-side concepts:

- **Private work** is visible only to its creator. Collaborator invitations are
  not yet implemented.
- **Team work** is visible to authenticated people and to the explicitly
  assigned agent.
- **Agent pickup work** is visible to agents and is atomically claimed by one
  agent. Lease expiry makes abandoned work claimable again.

The lanes are enforced by REST, SSE, Web Push, and MCP. A client-side lane view
is only a presentation choice. Existing tasks migrate to team visibility; new
browser tasks default to private and MCP tasks default to agent pickup. The
creator remains immutable so a published task can be made private again, while
an active agent run blocks that transition.

1. **First-class principals and credentials.** Store individually revocable,
   hashed agent credentials with stable principal IDs, scopes, expiry, and
   last-used metadata. The current shared bearer remains compatible as
   `agent:shared`; unattended services should move to distinct verified
   Cloudflare Access identities now, then to first-class credentials when they
   are available. Human-driven agents acting through a person's authenticated
   session need not invent a second identity.
2. **Workspace and project authorization.** Add workspace IDs to durable data,
   memberships, roles such as owner/admin/member/viewer, project-level grants,
   and server-side authorization on every REST, SSE, and MCP operation.
3. **Expanded assignment policy.** Add roles for assigning, reassigning,
   approving, and completing work, plus capability labels and concurrency
   limits without allowing an agent to broaden its own access.
4. **Administrative lifecycle.** Add credential rotation and revocation,
   membership offboarding, OIDC group-to-role mapping, export/deletion,
   configurable retention, and an auditable administrative log.
5. **Operational controls.** Add quotas, rate limiting, metrics, audit export,
   webhook/integration delivery with retries, tested disaster recovery, and a
   documented availability model.

## Compatibility direction

The current schema can evolve without abandoning simple self-hosting: existing
installations can migrate into a generated default workspace, and the existing
deployment token's `agent:shared` actor can become an administrator-created
agent credential. Client display names remain metadata rather than authority.
Runs started before this attribution change retain their legacy display-derived
owner until they finish or become stale; reclaiming stale work binds it to
`agent:shared`. The single-workspace interface should remain the default until
an operator enables additional workspaces and roles.
