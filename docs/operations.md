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

Schema migrations run during startup and are designed to be additive. Release
notes must call out any migration that changes rollback requirements.

## Credential rotation

Rotate `TASKBOARD_AUTH_TOKEN` when an agent credential may have been exposed or
an agent host leaves the trust boundary. Update every MCP gateway atomically,
restart Taskboard, and verify that the previous value is rejected. Rotate OIDC
and VAPID secrets according to their providers; changing the VAPID key pair
requires browsers to register new push subscriptions.
