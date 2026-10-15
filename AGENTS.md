# Taskboard agent instructions

Taskboard is the durable checklist and presence service for agent work. Keep
workflow state, transition validation, notification policy, and audit history
inside this application. Switchboard is only its MCP composition layer.

Never store prompts, chain-of-thought, credentials, or arbitrary tool output.
Status notes should be concise, user-visible summaries.

After Go changes, run:

```sh
gofmt -w .
go test ./...
go vet ./...
```

After web changes, run the JavaScript syntax check and exercise the affected
API/UI path. Preserve keyboard access, reduced-motion behavior, and responsive
layouts.

## Database migrations

An image rollback must work without restoring the database: release N-1 runs
against release N's schema, and a binary refuses a schema two or more releases
ahead. Migrations after `lastOneWaySchemaVersion` in
`internal/store/migrations.go` therefore follow these rules:

- Stay additive for one release. Add tables, nullable columns, or `NOT NULL`
  columns with a `DEFAULT`. Do not rename, drop, retype, or tighten anything the
  previous release still reads or writes. To rename, add the new column,
  backfill and write both, then drop the old one a release later.
- Expect the previous release to keep serving writes on the new schema after a
  rollback. It does not know the new columns, so they can go stale (for
  example, a column derived from another one that the old binary rewrites).
  Code reading a new column must tolerate stale or default values. Never let a
  security or trust decision depend on it alone.
- Give every migration `SQLiteDown` and `PostgresDown` statements that reverse
  it exactly.
- List each dropped column (`table.column`), table, index, or trigger in
  `Retires`. Only retire objects that neither this release nor the previous one
  uses.
- In the first migration added after a release, set
  `compatibleFromSchemaVersion` to that release's `latestSchemaVersion`. The
  database records it, and older binaries use it to decide whether they can
  run.

`go test ./internal/store` checks the rules and the down statements.
`scripts/check-schema-compat.sh` (the `migration-compat` CI job) runs the
previous release's tests against this tree's schema. If the previous release
predates this mechanism (v0.23.0 and earlier), the job cannot run them once a
migration has been added. It then passes with a "migration-compat skipped"
warning annotation and job summary. Treat that as "rollback not verified": a
rollback to that release still needs a database restore.
