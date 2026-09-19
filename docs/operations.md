# Operations

## Health and logs

`GET /healthz` reports the process version. `GET /readyz` verifies that the
configured SQLite or PostgreSQL store is available. The service emits structured JSON logs to standard
output; collect them with the container runtime or system journal.

Alert on repeated process restarts, readiness failures, authentication failures,
and storage exhaustion. Task contents may appear in ordinary application use,
so treat the database, backups, and operator access as sensitive.

## Prometheus metrics

Set `TASKBOARD_METRICS_LISTEN_ADDRESS` to enable a separate listener that serves
only `GET /metrics`. It is disabled by default and is intentionally absent from
the public application listener. The metrics endpoint has no authentication.
Bind it to loopback for a local collector, or to a private pod interface exposed
only through the metrics Service and protected by a Kubernetes NetworkPolicy
that admits the monitoring namespace. Do not put the metrics port behind the
public ingress.

```dotenv
TASKBOARD_METRICS_LISTEN_ADDRESS=127.0.0.1:9090
```

```yaml
scrape_configs:
  - job_name: taskboard
    static_configs:
      - targets: ["taskboard.internal:9090"]
```

Taskboard exports HTTP request counts and latency, authentication decisions,
database readiness and connection-pool state, agent run and lease operations,
event fan-out, subscriber count, and Web Push outcomes. Labels use only fixed,
bounded vocabularies such as route patterns, status classes, mechanisms, and
outcomes. Task IDs, user IDs, agent IDs, push endpoints, database addresses, and
error text are never labels.

Useful initial alerts include:

```promql
# Any failed database readiness check over five minutes.
increase(taskboard_database_ping_total{outcome="error"}[5m]) > 0

# Event consumers are falling behind and losing live updates.
increase(taskboard_event_deliveries_total{outcome="dropped"}[5m]) > 0

# Agent leases have expired. Tune the threshold to expected work patterns.
increase(taskboard_agent_run_operations_total{operation="sweep",outcome="stale"}[15m]) > 0

# More than 5% server errors with at least modest traffic.
(
  sum(rate(taskboard_http_requests_total{status_class="5xx"}[5m]))
  /
  clamp_min(sum(rate(taskboard_http_requests_total[5m])), 0.1)
) > 0.05
```

Start dashboards with request rate, p50/p95/p99 request duration by route,
server-error ratio, authentication failures by mechanism, database pool usage,
agent run outcomes, stale leases, event drops and subscriber count, and push
success/error/expired rates. Authentication failures and push errors are often
environment-dependent, so establish a baseline before paging on them.

## Backups

For PostgreSQL deployments, use the managed service's automated backups and
point-in-time recovery, and regularly produce a logically portable `pg_dump`.
Test restoration into a separate database. Keep database credentials and backup
access outside the Taskboard image and Kubernetes manifests.

For SQLite deployments, back up the database before every upgrade and on a regular schedule. For
a native deployment, SQLite's online backup command avoids copying a database
and WAL at inconsistent points:

```sh
sqlite3 /var/lib/taskboard/taskboard.db \
  ".backup '/var/backups/taskboard/taskboard-$(date +%F).db'"
```

For a container deployment without a host SQLite client, stop the service and
archive the entire `taskboard-data` volume. Restore into an empty volume while
the service is stopped, then start Taskboard and check `/readyz`.

Periodically perform a restore drill. A backup that has never been restored is
not a verified recovery path.

The administrative full export materializes all tasks, templates, events, and
audit entries in memory before sending JSON. Run large exports during a quiet
period, monitor process memory, and prefer a database-native backup for routine
disaster recovery. Treat exported JSON as sensitive workspace data, encrypt it
at rest and in transit, and remove temporary copies after verification.

## Upgrades and rollback

Before upgrading from v0.4.2 or earlier, configure at least one OIDC or
Cloudflare Access subject, email, or group allowlist. Deployments that
deliberately rely only on the upstream application policy must instead set the
matching explicit trust flag. The unmatched human role now defaults to
`member`; configure `TASKBOARD_ADMIN_GROUPS` or `TASKBOARD_OWNER_GROUPS` before
the upgrade when administrative API access is required.

1. Read the release notes and verify the downloaded checksum or image digest.
2. Back up the database and preserve the current binary or image digest.
3. Install the new version and restart the service.
4. Check `/healthz`, `/readyz`, OIDC sign-in, MCP task listing, and the live event stream.
5. If validation fails, stop the service, restore the prior binary or image and,
   only if a release note requires it, restore the matching database backup.

Schema migrations run transactionally during startup. Taskboard records every
ordered migration in `schema_migrations` with an immutable name and SHA-256
checksum. Startup refuses to continue when it finds a future schema version, a
gap or changed checksum in migration history, or a schema that is missing a
required table, column, or index.

Version 1 is the baseline for the public SQLite and PostgreSQL schemas. On the
first upgrade to the versioned runner, a complete pre-versioned SQLite schema or
an existing version-1 schema is upgraded, validated, and sealed with baseline
metadata. A database containing only part of the legacy schema is rejected
instead of being guessed into a usable shape.

Version 2 installs PostgreSQL's transactional event-notification trigger.
SQLite records the version as a compatibility no-op. PostgreSQL startup also
verifies that the trigger exists and rejects a partially modified schema.

Versions 3 and 4 add administrative lifecycle records and friendly agent-run
callsigns. Taskboard v0.6.0 advances the schema from version 4 to version 14:
version 5 adds task messages, 6 structured escalations, 7 acknowledged run
controls, 8 typed delivery references, 9 run handoffs, 10 completion contracts,
11 trusted session bridges, 12 task dependencies, 13 worker matching, and 14
numeric usage records. A v0.6.0 binary upgrades any complete supported schema
from version 1 through 13 directly and transactionally.

Back up the database before upgrading to v0.6.0. The new tables are additive,
but a v0.5.x binary rejects schema version 14 as newer than it supports. Rolling
back from v0.6.0 therefore requires restoring the database backup taken before
the upgrade together with the prior binary or image.

Migration files are append-only after release: never edit, reorder, or reuse a
version. CI must exercise a fresh database and an upgrade from every supported
schema version for both backends. Release notes must state the new schema
version, the oldest directly supported version, backup requirements, and
whether restoring the previous binary also requires restoring its database
backup.

Before upgrading, take and verify a backup as described above. If startup
rejects the schema, retain the error and database untouched; do not manually
edit `schema_migrations`. Roll back by stopping Taskboard, restoring both the
previous binary or image and its matching database backup, then checking
`/readyz` before reopening traffic.

## PostgreSQL event fan-out

Each PostgreSQL-backed process maintains a dedicated session named
`taskboard-event-fanout`. The transaction that inserts an audit event also emits
a `taskboard_events` notification containing only its ULID. On startup and
after every listener reconnect, Taskboard reads events after its in-memory
cursor in batches from the durable `events` table before reporting the listener
healthy. Live SSE and Web Push delivery are therefore coherent across replicas
without placing task content in PostgreSQL notification payloads.

`/readyz` returns unavailable while the listener is reconnecting. Alert on
readiness failures and
`taskboard_event_deliveries_total{outcome="fanout_error"}`. A brief outage may
delay live delivery, but catch-up restores it after reconnection. Audit history
and task mutations remain committed independently of live delivery. Use a
direct PostgreSQL connection or a session-pooling proxy; transaction-pooling
proxies cannot preserve `LISTEN` state.

## Credential rotation

Rotate `TASKBOARD_AUTH_TOKEN` when an agent credential may have been exposed or
an agent host leaves the trust boundary. Update every MCP gateway atomically,
restart Taskboard, and verify that the previous value is rejected. Rotate OIDC
and VAPID secrets according to their providers; changing the VAPID key pair
requires browsers to register new push subscriptions.
