# Administration

Taskboard keeps administrative authority inside the same trusted workspace.
Browser principals mapped to `owner` or `admin` may use the `/api/v1/admin/*`
API. Service principals and members/viewers cannot. Private-task visibility is
unchanged; administrative export and confirmed deletion are explicit privileged
workflows, not ordinary task access.

## Agent credentials and offboarding

`POST /api/v1/admin/credentials` accepts `name`, an `agent:`-prefixed
`principal_id`, and an optional future RFC3339 `expires_at`. The plaintext
`tb_agent_...` bearer is returned once; only its SHA-256 hash is stored.
`POST /api/v1/admin/credentials/{id}/rotate` atomically invalidates the old
secret and returns a replacement. `DELETE /api/v1/admin/credentials/{id}`
revokes it. `GET /api/v1/admin/credentials` returns metadata and last-use time,
never secret material.

`POST /api/v1/admin/principals/{principal}/offboard` requires a JSON body with
`confirm` exactly matching the principal and an optional `reason`. It rejects
self-offboarding, invalidates browser sessions, revokes every stored agent
credential for that principal, and denies future OIDC/Cloudflare assertions for
the principal. Removing the user from the identity provider remains the
authoritative organization-level offboarding step.
`DELETE /api/v1/admin/principals/{principal}/offboard` with the same exact
`confirm` field removes an accidental local deny entry; credentials revoked by
offboarding remain revoked and must be replaced.

## Export, deletion, retention, and audit

- `GET /api/v1/admin/export` exports all tasks, templates, task events, and the
  administrative audit history as JSON.
- `DELETE /api/v1/admin/tasks/{id}` requires `{ "confirm": "<task-id>" }`.
- `POST /api/v1/admin/retention` requires `{ "confirm": "apply retention" }`
  and deletes terminal tasks older than `TASKBOARD_RETENTION_DAYS`. A value of
  zero disables the workflow.
- `GET /api/v1/admin/audit?limit=...` exports newest-first append-only
  administrative records. Task retention does not remove this table.

Back up the database before bulk retention or deletion. These operations are
intentionally not exposed to MCP agents.

## Signed webhooks

When the webhook URL and secret are configured, Taskboard durably queues every
task event, including events created while delivery is offline. Requests carry
`X-Taskboard-Event-ID`, `X-Taskboard-Delivery-Attempt`, and
`X-Taskboard-Signature: sha256=<hex HMAC-SHA256 of the raw JSON body>`.
Receivers should verify the signature before parsing and deduplicate by event
ID. Non-2xx responses and network failures retry with exponential backoff up to
one hour. After `TASKBOARD_WEBHOOK_MAX_ATTEMPTS`, delivery moves to the dead
letter queue.

Use `GET /api/v1/admin/webhooks/dead-letters` to inspect dead letters and
`POST /api/v1/admin/webhooks/{id}/retry` to reset one after correcting the
receiver. Enabling delivery on an existing installation backfills its durable
event history. The worker follows Taskboard's supported single-replica model;
receivers must still be idempotent.
