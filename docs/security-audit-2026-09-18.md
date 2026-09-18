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
vulnerabilities:

- Taskboard is a shared workspace, not a tenant-isolation boundary. Use a
  dedicated deployment when mutually untrusted groups require isolation.
- The backwards-compatible default human role is `admin`; workplace deployments
  should configure group-to-role mappings and a less-privileged default.
- TLS and HSTS are expected at the ingress or reverse proxy.
- The administrative full export is intentionally unbounded and can consume
  memory proportional to workspace size.
- The Helm network policy permits general HTTPS egress for OIDC, push, and
  webhook integrations. Restrict it further when endpoint requirements are
  known.
- Larger installations still need deployment-specific quotas, rate limiting,
  and regularly exercised disaster recovery.
