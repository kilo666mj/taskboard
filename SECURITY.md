# Security policy

## Reporting a vulnerability

Please use GitHub's private vulnerability reporting feature. Do not open a
public issue containing exploit details, credentials, task contents, identity
data, push endpoints, or production topology.

Include the affected version or commit, reproduction steps, impact, and any
suggested mitigation. You should receive an acknowledgement within seven days.

## Supported versions

The latest tagged release and the current `main` branch receive security fixes.
Operators should keep Taskboard and its reverse proxy current and follow the
backup and upgrade procedure in [`docs/operations.md`](docs/operations.md).

## Deployment boundary

Taskboard is currently a single trusted workspace, not a multi-tenant service.
All permitted browser users share the board, and agent access uses one
deployment-scoped bearer credential. Run it behind TLS, restrict OIDC access,
keep credentials and inventories outside the repository, and do not expose an
insecure-mode listener beyond loopback. In Cloudflare Access mode, restrict
direct origin reachability and require Taskboard's cryptographic assertion
validation; never substitute unverified identity headers.
