#!/bin/sh
set -eu

chart="charts/taskboard"
helm lint "$chart"

rendered="$(helm template taskboard "$chart" \
  --namespace taskboard \
  --set metrics.enabled=true \
  --set ingress.enabled=true)"

require_rendered() {
  if ! printf '%s\n' "$rendered" | grep -F -- "$1" >/dev/null; then
    echo "rendered chart is missing: $1" >&2
    exit 1
  fi
}

require_rendered "kind: NetworkPolicy"
require_rendered "kind: PodDisruptionBudget"
require_rendered "automountServiceAccountToken: false"
require_rendered "readOnlyRootFilesystem: true"
require_rendered "runAsNonRoot: true"
require_rendered "secretKeyRef:"
require_rendered "name: TASKBOARD_METRICS_LISTEN_ADDRESS"
require_rendered "name: taskboard-metrics"
require_rendered "path: /readyz"

cloudflare="$(helm template taskboard "$chart" \
  --set taskboard.browserAuthMode=cloudflare_access \
  --set taskboard.cloudflareAccess.teamDomain=https://example.cloudflareaccess.com \
  --set taskboard.cloudflareAccess.audience=test-audience)"
if ! printf '%s\n' "$cloudflare" | grep -F "TASKBOARD_CF_ACCESS_AUD" >/dev/null; then
  echo "Cloudflare Access values were not rendered" >&2
  exit 1
fi
if printf '%s\n' "$cloudflare" | grep -F "TASKBOARD_OIDC_ISSUER" >/dev/null; then
  echo "OIDC configuration leaked into Cloudflare Access mode" >&2
  exit 1
fi

if helm template taskboard "$chart" --set taskboard.browserAuthMode=invalid >/dev/null 2>&1; then
  echo "invalid browser auth mode rendered successfully" >&2
  exit 1
fi
