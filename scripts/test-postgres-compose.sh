#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
scratch=$(mktemp -d)
project="taskboard-postgres-smoke-${RANDOM}-$$"
export POSTGRES_PASSWORD=taskboard-test

compose() {
  docker compose --project-directory "$scratch" --project-name "$project" \
    -f "$scratch/compose.yaml" -f "$scratch/compose.postgres.yaml" "$@"
}

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then
    compose logs postgres >&2 || true
  fi
  if ! compose down --volumes --remove-orphans; then
    status=1
  fi
  rm -rf "$scratch" || status=1
  exit "$status"
}
trap cleanup EXIT

cp "$repo_root/compose.yaml" "$repo_root/compose.postgres.yaml" "$scratch/"
cp "$repo_root/.env.example" "$scratch/.env"

query() {
  compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U taskboard -d taskboard "$@"
}

# Only the database starts, in a fresh project with no published database port.
compose up -d --no-deps --wait --wait-timeout 120 postgres
test "$(query -Atc "SELECT current_setting('server_version_num')::int / 10000")" = 18
query -c 'CREATE TABLE compose_persistence_probe (value integer PRIMARY KEY); INSERT INTO compose_persistence_probe VALUES (42);'

# Renew anonymous volumes so an incorrectly mounted named volume cannot pass
# merely because Compose retained the image's anonymous data volume.
compose up -d --no-deps --force-recreate --renew-anon-volumes --wait --wait-timeout 120 postgres
test "$(query -Atc 'SELECT value FROM compose_persistence_probe')" = 42
echo 'PostgreSQL 18 Compose data survives container recreation.'
