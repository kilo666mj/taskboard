# Security audit — 2026-09-18

## Scope

This review covered the Go API and MCP server, embedded web client, Tauri
desktop wrapper, SQLite and PostgreSQL persistence, authentication and
authorization paths, deployment manifests, release container, and direct Go
dependencies.

## Result

No known exploitable dependency vulnerability or committed production secret
was found. The review found three application hardening gaps, all fixed in the
same change:

- Administrative mutations now write their immutable audit event in the same
  database transaction. A failed audit write rolls back the mutation.
- API responses now send `Cache-Control: no-store`, including one-time agent
  credentials and administrative exports.
- Signed webhook delivery rejects redirects, preventing a configured endpoint
  from forwarding event data and authentication headers to another target.

The desktop OIDC handoff requires explicit browser confirmation, push endpoints
are restricted by pwa-kit, push subscriptions are owner-scoped, and task
visibility and role checks are enforced across HTTP, MCP, event streams, and
notifications.

## Verification

- Full Go test suite and `go vet` with Go 1.27.1
- SQLite tests plus PostgreSQL integration coverage
- `govulncheck`: no called vulnerabilities
- Git history secret scan: only intentional test constants
- GitHub dependency alerts: none open at review time
- CodeQL and dependency-review checks on the preceding release were green

## Residual deployment considerations

These are operational tradeoffs rather than currently exploitable application
vulnerabilities. The follow-up hardening change reduced several of them:

- Taskboard is a shared workspace, not a tenant-isolation boundary. Use a
  dedicated deployment when mutually untrusted groups require isolation.
- The default human role is now `member`; privileged access requires an
  explicit role mapping or an intentional configuration override.
- Empty application identity allowlists require an explicit provider-policy
  trust acknowledgement, and non-loopback listeners require a Host allowlist.
- TLS and HSTS remain the responsibility of the ingress or reverse proxy.
- The administrative full export is intentionally unbounded and can consume
  memory proportional to workspace size; database-native backups are preferred
  for routine disaster recovery.
- The Helm network policy permits HTTPS egress for OIDC, push, and webhook
  integrations. Restrict it further when endpoint requirements are known.
- In-process authentication and mutation limits bound bursts to one process;
  larger installations still need deployment-specific ingress quotas and
  regularly exercised disaster recovery.
- `golang.org/x/crypto` is pinned to v0.56.0. `govulncheck` reports only
  GO-2026-5932 at module level; Taskboard does not import the affected,
  unmaintained `openpgp` package and the advisory has no upstream fixed version.
