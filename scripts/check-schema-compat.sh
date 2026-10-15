#!/usr/bin/env bash
# Migration compatibility check: run the previous release's tests against this
# tree's schema, so an image rollback never needs a database restore.
#
# PREVIOUS_RELEASE overrides the release to test (default: the newest v* tag).
# TASKBOARD_TEST_POSTGRES_URL, when set, adds the PostgreSQL tests.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"
previous="${PREVIOUS_RELEASE:-$(git tag --list 'v*' --sort=-v:refname | head -n 1)}"
if [[ -z "$previous" ]]; then
  echo "No v* release tag found; fetch tags (actions/checkout fetch-depth: 0)." >&2
  exit 1
fi

schema_constant() {
  sed -n "s/^const $1 = \([0-9][0-9]*\)$/\1/p"
}
current_latest="$(schema_constant latestSchemaVersion <internal/store/migrations.go)"
current_floor="$(schema_constant compatibleFromSchemaVersion <internal/store/migrations.go)"
previous_latest="$(git show "$previous:internal/store/migrations.go" | schema_constant latestSchemaVersion)"
if [[ -z "$current_latest" || -z "$current_floor" || -z "$previous_latest" ]]; then
  echo "Could not read schema versions from internal/store/migrations.go." >&2
  exit 1
fi
echo "Previous release $previous: schema $previous_latest. This tree: schema $current_latest, readable from $current_floor."
if (( current_latest < previous_latest )); then
  echo "This tree's schema $current_latest is older than release $previous's $previous_latest." >&2
  exit 1
fi
if (( current_latest > previous_latest && current_floor != previous_latest )); then
  echo "Migrations were added since $previous: set compatibleFromSchemaVersion to $previous_latest in internal/store/migrations.go." >&2
  exit 1
fi

work="$(mktemp -d)"
cleanup() {
  git worktree remove --force "$work/previous" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
git worktree add --quiet --detach "$work/previous" "$previous"

go build -o "$work/schema-migrate" ./internal/tools/schema-migrate
if ! grep -q 'TASKBOARD_TEST_SCHEMA_MIGRATOR' "$work/previous/internal/store/store.go"; then
  if (( current_latest > previous_latest )); then
    # Releases before rollback-safe migrations refuse any newer schema, so a rollback to one
    # of them still needs a database restore once a migration is added.
    # A skip is not a pass: say so loudly in the log, the annotations and the
    # job summary, so a green job is not read as a verified rollback.
    message="Rollback compatibility NOT checked: release $previous predates rollback-safe migrations and cannot run schema $current_latest. Rolling back to $previous needs a database restore."
    echo "::warning title=migration-compat skipped::$message"
    if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
      printf '### :warning: migration-compat skipped\n\n%s\n' "$message" >>"$GITHUB_STEP_SUMMARY"
    fi
    exit 0
  fi
  # Same schema: the previous release's own migrations build it.
  unset TASKBOARD_TEST_SCHEMA_MIGRATOR
else
  export TASKBOARD_TEST_SCHEMA_MIGRATOR="$work/schema-migrate"
fi
cd "$work/previous"
echo "Running $previous tests against schema $current_latest."
go test -count=1 ./...
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  printf 'Release %s tests passed against schema %s.\n' "$previous" "$current_latest" >>"$GITHUB_STEP_SUMMARY"
fi
