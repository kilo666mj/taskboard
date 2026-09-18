# Operations

## Health and logs

`GET /healthz` reports the process version. `GET /readyz` verifies that the
configured SQLite or PostgreSQL store is available. The service emits structured JSON logs to standard
output; collect them with the container runtime or system journal.

Alert on repeated process restarts, readiness failures, authentication failures,
and storage exhaustion. Task contents may appear in ordinary application use,
so treat the database, backups, and operator access as sensitive.

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

## Upgrades and rollback

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

## Credential rotation

Rotate `TASKBOARD_AUTH_TOKEN` when an agent credential may have been exposed or
an agent host leaves the trust boundary. Update every MCP gateway atomically,
restart Taskboard, and verify that the previous value is rejected. Rotate OIDC
and VAPID secrets according to their providers; changing the VAPID key pair
requires browsers to register new push subscriptions.
