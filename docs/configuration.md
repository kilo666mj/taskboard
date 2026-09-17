# Configuration

Taskboard reads configuration from environment variables. Empty optional values
disable their feature.

| Variable | Default | Purpose |
| --- | --- | --- |
| `TASKBOARD_LISTEN_ADDRESS` | `127.0.0.1:8095` | HTTP listen address. The container image overrides this to `0.0.0.0:8095`. |
| `TASKBOARD_DATABASE_PATH` | `taskboard.db` | SQLite database path. |
| `TASKBOARD_AUTH_TOKEN` | none | Deployment-scoped MCP bearer credential; at least 32 characters. Required on non-loopback listeners. |
| `TASKBOARD_ALLOW_INSECURE` | `false` | Allow unauthenticated use only for explicit local development. Never enable on a network listener. |
| `TASKBOARD_ALLOWED_HOSTS` | none | Comma-separated hostnames accepted by the application. |
| `TASKBOARD_LEASE_SECONDS` | `120` | Agent lease duration, from 30 through 3600 seconds. |
| `TASKBOARD_MCP_DEFAULT_TYPE` | `work` | Default type for agent-created tasks: `personal` or `work`. |
| `TASKBOARD_BROWSER_AUTH_MODE` | `oidc` | Browser authentication mode: `oidc` or `cloudflare_access`. |
| `TASKBOARD_VAPID_PUBLIC_KEY` | none | Web Push VAPID public key. |
| `TASKBOARD_VAPID_PRIVATE_KEY` | none | Matching private key. Keep it secret and stable across upgrades. |
| `TASKBOARD_VAPID_CONTACT` | `mailto:admin@localhost` | Public VAPID contact URI. Use a real public address in production. |
| `TASKBOARD_OIDC_ISSUER` | none | OIDC issuer URL. |
| `TASKBOARD_OIDC_CLIENT_ID` | none | OIDC client ID. Public PKCE clients do not require a secret. |
| `TASKBOARD_OIDC_CLIENT_SECRET` | none | Optional confidential-client secret. |
| `TASKBOARD_OIDC_REDIRECT_URL` | none | Exact OIDC callback URL ending in `/api/v1/auth/oidc/callback`. |
| `TASKBOARD_OIDC_ALLOWED_SUBJECTS` | none | Optional comma-separated subject allowlist. |
| `TASKBOARD_OIDC_ALLOWED_EMAILS` | none | Optional comma-separated email allowlist. |
| `TASKBOARD_OIDC_ALLOWED_GROUPS` | none | Optional comma-separated group allowlist. |
| `TASKBOARD_CF_ACCESS_TEAM_DOMAIN` | none | Cloudflare Access HTTPS team origin. |
| `TASKBOARD_CF_ACCESS_AUD` | none | Exact Access application audience tag. |
| `TASKBOARD_CF_ACCESS_ALLOWED_SUBJECTS` | none | Optional comma-separated Access subject allowlist. |
| `TASKBOARD_CF_ACCESS_ALLOWED_EMAILS` | none | Optional comma-separated Access email allowlist. |
| `TASKBOARD_CF_ACCESS_ALLOWED_GROUPS` | none | Optional comma-separated Access group allowlist. |

OIDC requires issuer, client ID, and redirect URL together. At least one OIDC
allowlist is recommended for a workplace deployment unless the identity
provider application assignment already restricts access.

In `cloudflare_access` mode, Taskboard verifies the edge-injected
`Cf-Access-Jwt-Assertion` against the team JWKS, issuer, and application
audience. Assertions may authenticate browser REST/SSE requests and MCP agents.
Requests that supply both an Access assertion and the deployment bearer fail
closed as ambiguous. Restrict direct origin access because a valid assertion is
still a bearer credential until it expires.

For identity-provider sessions, subject allowlist entries are the Access `sub`
claim. Cloudflare service-token assertions have no `sub` or email; Taskboard
gives them the stable subject `service_token:<common_name>`, which can also be
placed in the subject allowlist.

Generate an agent credential with a password manager or a system random source,
for example:

```sh
openssl rand -hex 32
```

Generate Web Push keys with the matching release binary:

```sh
taskboard-keygen
```
