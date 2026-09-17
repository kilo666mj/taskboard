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

## TLS and proxy controls

- Redirect HTTP to HTTPS and enable HSTS at the proxy after HTTPS is stable.
- Limit request body sizes and apply rate limits to authentication and MCP paths.
- Preserve streaming for `/api/v1/events`; disable proxy buffering there.
- Restrict access through the OIDC application and Taskboard allowlists.
- Do not forward an instance running with `TASKBOARD_ALLOW_INSECURE=true`.

## Cloudflare Access

To use Cloudflare Access instead of the built-in OIDC browser flow, protect the
entire Taskboard hostname with an Access application and configure:

```dotenv
TASKBOARD_BROWSER_AUTH_MODE=cloudflare_access
TASKBOARD_CF_ACCESS_TEAM_DOMAIN=https://your-team.cloudflareaccess.com
TASKBOARD_CF_ACCESS_AUD=your-access-application-aud-tag
```

Taskboard validates the `Cf-Access-Jwt-Assertion` signature, issuer, expiry, and
audience at the origin; it does not trust an email-only proxy header. Optional
subject, email, and group allowlists provide an additional origin-side gate.

Cloudflare Access can also authenticate MCP workloads through an Access service
token or OAuth flow. After Cloudflare validates the client it injects the same
assertion, which Taskboard maps to a stable `cloudflare_access:<subject>` audit
actor. Do not also send `Authorization: Bearer` on that request. A deployment
may keep the Taskboard bearer for a separate private or explicitly bypassed MCP
route, but the public Access route should use one credential source per request.

For service tokens, the audit actor is
`cloudflare_access:service_token:<common_name>` because Cloudflare intentionally
leaves the assertion's `sub` and `email` claims empty.

The desktop shell follows the normal Cloudflare Access flow in its webview in
this mode. The external-browser, single-use handoff remains specific to OIDC.
Cloudflare logout uses the same-origin `/cdn-cgi/access/logout` endpoint.
