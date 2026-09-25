# Configuration

Taskboard reads configuration from environment variables. Empty optional values
disable their feature.

| Variable | Default | Purpose |
| --- | --- | --- |
| `TASKBOARD_LISTEN_ADDRESS` | `127.0.0.1:8095` | HTTP listen address. The container image overrides this to `0.0.0.0:8095`. |
| `TASKBOARD_METRICS_LISTEN_ADDRESS` | none | Optional, separate Prometheus listener (for example `127.0.0.1:9090`). It serves only `GET /metrics` and has no application authentication. |
| `TASKBOARD_DATABASE_PATH` | `taskboard.db` | SQLite database path. |
| `TASKBOARD_DATABASE_URL` | none | PostgreSQL connection URL. When set, PostgreSQL is used and `TASKBOARD_DATABASE_PATH` is ignored. |
| `TASKBOARD_AUTH_TOKEN` | none | Deployment-scoped MCP bearer credential; at least 32 characters. Required on non-loopback listeners. Calls using it are attributed to `agent:shared`. |
| `TASKBOARD_ALLOW_INSECURE` | `false` | Allow unauthenticated use only for explicit local development. Without a token, startup refuses non-loopback listeners. |
| `TASKBOARD_ALLOWED_HOSTS` | none | Comma-separated hostnames accepted by the application. It is required for non-loopback listeners. Loopback listeners safely default to `localhost`, `127.0.0.1`, and `::1`. Health and readiness probes are exempt. |
| `TASKBOARD_LEASE_SECONDS` | `120` | Agent lease duration, from 30 through 3600 seconds. |
| `TASKBOARD_TASK_TYPE` | none | Fix every new task on this instance to `personal` or `work` and hide the type selector. Type changes to existing tasks are ignored. Leave empty to choose a type per task. |
| `TASKBOARD_MCP_DEFAULT_TYPE` | `work` | Deprecated; use `TASKBOARD_TASK_TYPE`. Default type for MCP-created tasks when `TASKBOARD_TASK_TYPE` is empty. |
| `TASKBOARD_MCP_HUMAN_DELEGATION` | `false` | Let a person verified by Cloudflare Access on `/mcp` have `task_create` record tasks as themselves. See [MCP human delegation](#mcp-human-delegation). |
| `TASKBOARD_MCP_DELEGATION_PRINCIPALS` | none | Comma-separated dedicated agent credential principals, such as `agent:switchboard`, allowed to forward a Cloudflare Access person in `X-Switchboard-Access-Subject`. Requires delegation and Cloudflare Access browser mode; `agent:shared` is refused. |
| `TASKBOARD_BROWSER_AUTH_MODE` | `oidc` | Browser authentication mode: `oidc` or `cloudflare_access`. |
| `TASKBOARD_VAPID_PUBLIC_KEY` | none | Web Push VAPID public key. |
| `TASKBOARD_VAPID_PRIVATE_KEY` | none | Matching private key. Keep it secret and stable across upgrades. |
| `TASKBOARD_VAPID_CONTACT` | `mailto:admin@localhost` | Public VAPID contact URI. Use a real public address in production. |
| `TASKBOARD_OIDC_ISSUER` | none | OIDC issuer URL. |
| `TASKBOARD_OIDC_CLIENT_ID` | none | OIDC client ID. Public PKCE clients do not require a secret. |
| `TASKBOARD_OIDC_CLIENT_SECRET` | none | Optional confidential-client secret. |
| `TASKBOARD_OIDC_REDIRECT_URL` | none | Exact OIDC callback URL ending in `/api/v1/auth/oidc/callback`. |
| `TASKBOARD_OIDC_ALLOWED_SUBJECTS` | none | Comma-separated subject allowlist. At least one OIDC allowlist is required unless provider-policy trust is explicitly enabled. |
| `TASKBOARD_OIDC_ALLOWED_EMAILS` | none | Comma-separated email allowlist. |
| `TASKBOARD_OIDC_ALLOWED_GROUPS` | none | Comma-separated group allowlist. |
| `TASKBOARD_OIDC_TRUST_PROVIDER_POLICY` | `false` | Explicitly rely on the OIDC provider's application-assignment policy when all application allowlists are empty. |
| `TASKBOARD_CF_ACCESS_TEAM_DOMAIN` | none | Cloudflare Access HTTPS team origin. |
| `TASKBOARD_CF_ACCESS_AUD` | none | Exact Access application audience tag. |
| `TASKBOARD_CF_ACCESS_ALLOWED_SUBJECTS` | none | Comma-separated Access subject allowlist. At least one Access allowlist is required unless edge-policy trust is explicitly enabled. |
| `TASKBOARD_CF_ACCESS_ALLOWED_EMAILS` | none | Comma-separated Access email allowlist. |
| `TASKBOARD_CF_ACCESS_ALLOWED_GROUPS` | none | Comma-separated Access group allowlist. |
| `TASKBOARD_CF_ACCESS_TRUST_POLICY` | `false` | Explicitly rely on the Cloudflare Access application policy when all application allowlists are empty. |
| `TASKBOARD_DEFAULT_ROLE` | `member` | Role assigned when no configured role group matches: `owner`, `admin`, `member`, or `viewer`. Grant privileged roles with explicit group mappings. |
| `TASKBOARD_OWNER_GROUPS` | none | Comma-separated OIDC or Cloudflare Access groups mapped to `owner`. |
| `TASKBOARD_ADMIN_GROUPS` | none | Comma-separated OIDC or Cloudflare Access groups mapped to `admin`. |
| `TASKBOARD_MEMBER_GROUPS` | none | Comma-separated OIDC or Cloudflare Access groups mapped to `member`. |
| `TASKBOARD_VIEWER_GROUPS` | none | Comma-separated OIDC or Cloudflare Access groups mapped to `viewer`. |
| `TASKBOARD_AGENT_CAPABILITIES` | safe task/template capabilities | Comma-separated default capabilities for service principals. `task:sensitive` is deliberately excluded. |
| `TASKBOARD_AGENT_MAX_CONCURRENT_RUNS` | `8` | Maximum live leased runs per service principal (1-100). |
| `TASKBOARD_AGENT_MAX_PICKUPS_PER_MINUTE` | `30` | Maximum task starts and claims per service principal per minute (1-1000). |
| `TASKBOARD_AGENT_MAX_RUN_SECONDS` | `28800` | Maximum run age that may be extended by heartbeat (60-604800 seconds). |
| `TASKBOARD_AGENT_REQUIRE_IDEMPOTENCY` | `false` | Require `idempotency_key` on mutating task operations for service principals. |
| `TASKBOARD_AGENT_POLICIES_JSON` | none | JSON object containing per-principal capability and limit overrides. |
| `TASKBOARD_RETENTION_DAYS` | `0` | Age in days for explicit administrative pruning of completed/cancelled tasks. `0` disables retention deletion. |
| `TASKBOARD_WEBHOOK_URL` | none | Trusted HTTPS endpoint for durable signed event delivery. It receives task titles, notes, actors, status, and visibility. Requires `TASKBOARD_WEBHOOK_SECRET`. |
| `TASKBOARD_WEBHOOK_SECRET` | none | HMAC-SHA256 webhook signing secret of at least 32 characters. |
| `TASKBOARD_WEBHOOK_MAX_ATTEMPTS` | `8` | Delivery attempts before a webhook enters the administrative dead-letter queue (1-100). |

OIDC requires issuer, client ID, and redirect URL together. Taskboard fails
startup when all OIDC allowlists are empty unless
`TASKBOARD_OIDC_TRUST_PROVIDER_POLICY=true` explicitly records that the
provider's application-assignment policy is the authorization boundary. The
equivalent Cloudflare Access acknowledgement is
`TASKBOARD_CF_ACCESS_TRUST_POLICY=true`.

Role mappings are evaluated in descending authority order: owner, admin,
member, then viewer. If an identity belongs to several mapped groups, the
highest role wins. Set `TASKBOARD_DEFAULT_ROLE=viewer` when every identity that
may change data should be explicitly placed in a member, admin, or owner group.
The same mappings apply to group claims from OIDC and Cloudflare Access.
Invalid or missing in-process role values fail safely to `viewer`.

| Action | Owner | Admin | Member | Viewer |
| --- | --- | --- | --- | --- |
| Read visible tasks, templates, and live events | yes | yes | yes | yes |
| Manage own Web Push subscription | yes | yes | yes | yes |
| Create, edit, assign, move, and complete visible tasks | yes | yes | yes | no |
| Create, replace, and delete task templates | yes | yes | no | no |

Private tasks remain visible only to their immutable creator, including for
owners and admins. Roles apply to the single trusted workspace and do not add
tenant or project isolation. MCP service principals keep the existing agent
authorization model; automated-agent capabilities are configured separately.

## Automated-agent safety policies

Service principals receive named capabilities instead of inheriting browser
roles. The supported labels are `task:read`, `task:create`, `task:claim`,
`task:update`, `task:message`, `task:escalate`, `task:control`, `task:reference`, `task:handoff`, `task:evidence`, `task:session`, `task:usage`, `task:complete`, `task:sensitive`, `worker:advertise`, `template:read`,
and `template:manage`. `task:message` permits an owned active run to append
conversation and record explicit receipts; it does not permit task mutation.
Cancelling a task or skipping checklist items requires the
separate `task:sensitive` capability. It is excluded from the default policy,
so granting it is the operator approval gate for those irreversible actions.

Per-principal policies use the canonical authenticated actor ID. For example,
this policy gives a Cloudflare service token read/claim access, one concurrent
run, a two-hour maximum run, and mandatory idempotency keys:

```dotenv
TASKBOARD_AGENT_POLICIES_JSON={"cloudflare_access:service_token:build":{"capabilities":["task:read","task:claim","task:update","task:complete"],"max_concurrent_runs":1,"max_pickups_per_minute":10,"max_run_seconds":7200,"require_idempotency":true}}
```

Mutating MCP task tools accept an `idempotency_key` of 8-128 letters, digits,
periods, underscores, colons, or hyphens. Repeating the same operation and
request returns the original task/run identity (with its current task state);
reusing a key with different input is a conflict. Heartbeats are naturally
idempotent. In-process serialization closes concurrent duplicate races for the
supported single-replica deployment model.

Taskboard applies bounded in-process limits to desktop session exchanges,
failed authentication attempts, and authenticated mutations. A rejected burst
receives `429 Too Many Requests` with `Retry-After`. These safeguards protect a
single process; keep ingress-level limits as an additional deployment control,
especially when several replicas share a public endpoint.

Administrative credential, offboarding, retention, export/deletion, audit, and
webhook procedures are documented in [Administration](administration.md).

`TASKBOARD_DATABASE_URL` accepts `postgres://` and `postgresql://` URLs. A
multi-host URL can use `target_session_attrs=read-write` to select the writable
node after database failover:

```dotenv
TASKBOARD_DATABASE_URL=postgresql://taskboard:password@db01:5432,db02:5432/taskboard?sslmode=verify-full&sslrootcert=system&target_session_attrs=read-write
```

Keep this value in a secret store. Taskboard applies serialized, additive
schema migrations during startup, including when several pods start together.

In `cloudflare_access` mode, Taskboard verifies the edge-injected
`Cf-Access-Jwt-Assertion` against the team JWKS, issuer, and application
audience. Assertions may authenticate browser REST/SSE requests and MCP agents.
Requests that supply both an Access assertion and the deployment bearer fail
closed as ambiguous. Restrict direct origin access because a valid assertion is
still a bearer credential until it expires.

Browser mutations require an exact same-origin `Origin` header, reject
`Sec-Fetch-Site: cross-site`, and accept JSON request bodies only with an
`application/json` media type. Live event streams are limited to four per
identity and 128 per server process.

For identity-provider sessions, subject allowlist entries are the Access `sub`
claim. Cloudflare service-token assertions have no `sub` or email; Taskboard
gives them the stable subject `service_token:<common_name>`, which can also be
placed in the subject allowlist.

Actor IDs are assigned only from trusted authentication state. Browser users
keep the stable subject issued by OIDC or Cloudflare Access. The shared MCP
bearer uses `agent:shared`, unauthenticated loopback development uses
`agent:local`, and verified Cloudflare Access MCP workloads use their
`cloudflare_access:<subject>` identity (including
`cloudflare_access:service_token:<common_name>`). MCP `clientInfo` is untrusted
display metadata and is recorded separately on an agent run.

### MCP human delegation

By default every `/mcp` caller is an agent, so a person who connects their own
MCP client through Cloudflare Access still creates agent-lane tasks. With
`TASKBOARD_MCP_HUMAN_DELEGATION=true`, an MCP request carrying a Cloudflare
Access identity for a person (never a service token) is still an agent
principal, but `task_create` records the task as that person:

- `created_by` is the person's subject and visibility defaults to `private`,
  as in the browser; `team` may be requested explicitly.
- The person's role must allow task writes and the agent policy must grant
  `task:create`.
- The `task.created` event carries `delegated_via: "mcp"`.
- Passing `visibility: "agent"` keeps the previous agent-lane behavior.

Delegation covers only `task_create`. `task_start`, claims, updates and every
human-owned control (acceptance criteria, dependencies, reviews) keep agent
authority, so an agent cannot see or change a private task after creating it
for the person.

#### Through Switchboard

When people reach Taskboard through Switchboard, Taskboard sees Switchboard's
bearer instead of the person's Access assertion. To delegate in that topology:

1. Mint a dedicated agent credential for Switchboard, for example principal
   `agent:switchboard` (see [Administration](administration.md)), and configure
   Switchboard's Taskboard capability to use it instead of
   `TASKBOARD_AUTH_TOKEN`.
2. Enable `forward_cloudflare_access_subject` on that capability. Switchboard
   then sends the verified person as `cloudflare_access:<sub>` in
   `X-Switchboard-Access-Subject`, and never for service tokens.
3. Set `TASKBOARD_MCP_HUMAN_DELEGATION=true` and
   `TASKBOARD_MCP_DELEGATION_PRINCIPALS=agent:switchboard`.

Taskboard reads the header only from a listed principal and ignores it from
every other caller, including the shared bearer. It rejects the request when
the forwarded value is not a person's `cloudflare_access:` subject or when that
person has been offboarded. The header carries no groups, so the delegated
person gets `TASKBOARD_DEFAULT_ROLE`. Cloudflare documents the Access `sub` as
unique to an email address per account, so tasks recorded through Switchboard
belong to the identity that signs in to the browser when both applications are
in the same Cloudflare account. A person who is removed and re-added in Access
receives a new `sub`.

Generate an agent credential with a password manager or a system random source,
for example:

```sh
openssl rand -hex 32
```

Generate Web Push keys with the matching release binary:

```sh
taskboard-keygen
```
