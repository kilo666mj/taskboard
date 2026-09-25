# Deployment

Taskboard is designed to run behind a TLS-terminating reverse proxy. The proxy
should preserve the original `Host` header, apply appropriate request-rate
limits, and forward requests to the loopback-published service port.

## Container image

Tagged releases publish multi-architecture images for Linux AMD64 and ARM64:

```sh
docker pull ghcr.io/kilo666mj/taskboard:latest
cp .env.example .env
docker compose up -d
curl --fail http://127.0.0.1:8095/readyz
```

Edit `.env` before starting. Replace every placeholder, set the public hostname
and OIDC callback, and generate the bearer and VAPID keys. The Compose service
publishes only to host loopback; the reverse proxy is the public entry point.

For reproducible production deployments, use a complete version tag such as
`ghcr.io/kilo666mj/taskboard:1.0.0` instead of `latest`.

For a local PostgreSQL evaluation, set a URL-safe `POSTGRES_PASSWORD` and apply
the Compose override:

```sh
export POSTGRES_PASSWORD='replace-with-a-random-url-safe-value'
docker compose -f compose.yaml -f compose.postgres.yaml up -d
```

The bundled PostgreSQL service is for evaluation. Use a managed database for
production.

## Kubernetes and EKS

An optional hardened Helm chart is available in `charts/taskboard`; see the
[Helm and EKS guide](helm.md) for External Secrets, ingress, NetworkPolicy,
metrics, Cloudflare Access, and upgrade examples.

Use an external PostgreSQL service such as RDS or Aurora PostgreSQL. Taskboard
pods do not need a persistent volume when `TASKBOARD_DATABASE_URL` is set.
Store the complete connection URL in a Kubernetes Secret and expose it to the
container with `secretKeyRef`:

```yaml
env:
  - name: TASKBOARD_DATABASE_URL
    valueFrom:
      secretKeyRef:
        name: taskboard-database
        key: url
```

Use TLS verification in the connection URL, keep `/readyz` as the readiness
probe, and use `/healthz` as the liveness probe. PostgreSQL makes task state,
sessions, templates, subscriptions, and audit events independent of pod
lifetime.

For Prometheus scraping, set `TASKBOARD_METRICS_LISTEN_ADDRESS=0.0.0.0:9090`,
add a named metrics port to the pod, and permit that port only from the
monitoring namespace with a NetworkPolicy. The metrics listener is unauthenticated
and must not be added to the public Service or ingress.

Run migrations as part of a controlled rollout with only one new-version pod
starting against the database at a time. PostgreSQL serializes migration work
with an advisory transaction lock, but a pre-upgrade backup and a documented
rollback remain required. Do not run an older Taskboard binary after a release
whose notes require a database restore for rollback.

PostgreSQL deployments distribute task events through a dedicated
`LISTEN taskboard_events` connection per replica. Notifications contain only a
26-character durable event ID; replicas then read ordered event data from the
database. A reconnecting replica catches up from its last delivered event, and
local notification echoes are deduplicated. This keeps SSE clients and Web Push
workers coherent across replicas.

Readiness fails while a configured PostgreSQL replica cannot establish or
recover its event listener. PostgreSQL proxies must support session-level
`LISTEN/NOTIFY`; use a direct connection or PgBouncer session pooling rather
than transaction pooling for Taskboard's database URL. SQLite remains a
single-replica deployment.

## Release binary

Each GitHub release contains Linux and macOS AMD64/ARM64 bundles with the server,
configuration initializer, Web Push key generator, README, license, and a
`SHA256SUMS` file. Verify a download before installing it:

```sh
sha256sum --check SHA256SUMS
sudo install -m 0755 taskboard-*/bin/taskboard /usr/local/bin/taskboard
```

Create a dedicated service account and state directory, place environment
configuration in a root-owned file, and use the hardened example unit in
`ansible/templates/taskboard.service.j2` as the systemd baseline.

## Ansible example

The tracked inventory uses documentation-only addresses. Copy it and the
private-variable template before use:

```sh
cp ansible/inventory.ini ansible/inventory.local.ini
cp ansible/private.yml.example ansible/private.yml
ansible-playbook -i ansible/inventory.local.ini ansible/deploy.yml
```

Both local files are ignored. Never put production hostnames, addresses, or
credentials back into tracked examples.

An instance that serves only one kind of work should set `taskboard_task_type`
to `personal` or `work` in `private.yml`. Every new task then gets that type
and the browser hides the type selector. The older `taskboard_mcp_default_type`
applies only while `taskboard_task_type` is empty.

## TLS and proxy controls

- Redirect HTTP to HTTPS and enable HSTS at the proxy after HTTPS is stable.
- Limit request body sizes and apply rate limits to authentication and MCP paths.
- Preserve streaming for `/api/v1/events`; disable proxy buffering there.
- Restrict access through the OIDC application and Taskboard allowlists.
- Do not forward an instance running with `TASKBOARD_ALLOW_INSECURE=true`.

Taskboard deliberately does not emit `Strict-Transport-Security`: its normal
upstream connection is loopback or private-cluster HTTP and it cannot determine
whether the public request arrived over correctly configured HTTPS. The public
ingress or reverse proxy owns HSTS for the externally visible hostname.

The MCP handler disables mcpkit's localhost-only transport check because
Taskboard is designed to run behind a trusted reverse proxy. Taskboard still
enforces its own authentication, Host allowlist, browser-origin checks, body
validation, and request limits. Do not expose a loopback Taskboard listener
through an untrusted generic proxy, and do not treat the mcpkit setting as a
replacement for origin network controls.

## Cloudflare Access

To use Cloudflare Access instead of the built-in OIDC browser flow, protect the
entire Taskboard hostname with an Access application and configure:

```dotenv
TASKBOARD_BROWSER_AUTH_MODE=cloudflare_access
TASKBOARD_CF_ACCESS_TEAM_DOMAIN=https://your-team.cloudflareaccess.com
TASKBOARD_CF_ACCESS_AUD=your-access-application-aud-tag
TASKBOARD_CF_ACCESS_ALLOWED_EMAILS=operator@example.com
```

Taskboard validates the `Cf-Access-Jwt-Assertion` signature, issuer, expiry, and
audience at the origin; it does not trust an email-only proxy header. Optional
subject, email, and group allowlists provide an origin-side gate. If the Access
application policy is intentionally the only identity allowlist, set
`TASKBOARD_CF_ACCESS_TRUST_POLICY=true` to acknowledge that design explicitly.

Cloudflare Access can also authenticate MCP workloads through an Access service
token or OAuth flow. After Cloudflare validates the client it injects the same
assertion, which Taskboard maps to a stable `cloudflare_access:<subject>` audit
actor. Do not also send `Authorization: Bearer` on that request. A deployment
may keep the Taskboard bearer for a separate private or explicitly bypassed MCP
route, but the public Access route should use one credential source per request.

When people sign in to MCP through the Access OAuth flow, their assertions carry
a person's subject rather than a service token. Set
`TASKBOARD_MCP_HUMAN_DELEGATION=true` (Ansible
`taskboard_mcp_human_delegation`) if tasks they capture through `task_create`
should belong to them instead of the agent pickup lane. Service tokens are
never delegated. See
[MCP human delegation](configuration.md#mcp-human-delegation).

For service tokens, the audit actor is
`cloudflare_access:service_token:<common_name>` because Cloudflare intentionally
leaves the assertion's `sub` and `email` claims empty.
Service-token identities are accepted as agents only on `/mcp`; they are
rejected from browser and human REST endpoints even when their name appears in
an application allowlist.

The desktop shell follows the normal Cloudflare Access flow in its webview in
this mode. The external-browser, single-use handoff remains specific to OIDC.
Cloudflare logout uses the same-origin `/cdn-cgi/access/logout` endpoint.
