# Helm and EKS deployment

The optional chart in `charts/taskboard` deploys Taskboard with external
PostgreSQL. It does not create a database or application credentials. The
defaults run one replica, reference existing Kubernetes Secrets, and enforce a
non-root, read-only container with no service-account token or Linux
capabilities.

## Prerequisites

- Kubernetes 1.28 or newer and Helm 3 or newer.
- A PostgreSQL URL with TLS verification. Direct connections and session-mode
  pooling support Taskboard's dedicated `LISTEN/NOTIFY` connection; PgBouncer
  transaction pooling does not.
- An ingress controller or private load balancer that preserves `Host` and SSE
  streaming.
- Two Secrets named `taskboard-database` and `taskboard-secrets`, or matching
  overrides in a private values file.

The database Secret must contain `url`. The application Secret must contain an
`auth-token` with at least 32 random characters. It may also contain
`oidc-client-secret`, `vapid-public-key`, and `vapid-private-key`.

## External Secrets on EKS

Prefer External Secrets Operator with AWS Secrets Manager instead of committing
Secret manifests or plaintext Helm values. Given an existing `ClusterSecretStore`
named `aws-secrets-manager`, these resources materialize the names expected by
the chart:

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: taskboard-database
  namespace: taskboard
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: ClusterSecretStore
    name: aws-secrets-manager
  target:
    name: taskboard-database
  data:
    - secretKey: url
      remoteRef:
        key: production/taskboard
        property: database_url
---
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: taskboard-secrets
  namespace: taskboard
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: ClusterSecretStore
    name: aws-secrets-manager
  target:
    name: taskboard-secrets
  data:
    - secretKey: auth-token
      remoteRef:
        key: production/taskboard
        property: auth_token
    - secretKey: oidc-client-secret
      remoteRef:
        key: production/taskboard
        property: oidc_client_secret
    - secretKey: vapid-public-key
      remoteRef:
        key: production/taskboard
        property: vapid_public_key
    - secretKey: vapid-private-key
      remoteRef:
        key: production/taskboard
        property: vapid_private_key
```

The External Secrets controller needs AWS access; the Taskboard pod does not.
Keep `serviceAccount.automount=false` unless a separate integration genuinely
requires Kubernetes API credentials.

## Private values

Create an untracked `taskboard-values.yaml`:

```yaml
image:
  # Prefer the digest published for the selected release.
  digest: sha256:replace-with-release-image-digest

taskboard:
  allowedHosts: [taskboard.example.com]
  browserAuthMode: oidc
  oidc:
    issuer: https://id.example.com
    clientID: taskboard
    redirectURL: https://taskboard.example.com/api/v1/auth/oidc/callback
    allowedGroups: [taskboard-users]

ingress:
  enabled: true
  className: alb
  annotations:
    alb.ingress.kubernetes.io/scheme: internal
    alb.ingress.kubernetes.io/target-type: ip
    alb.ingress.kubernetes.io/backend-protocol: HTTP
  hosts:
    - host: taskboard.example.com
      paths:
        - path: /
          pathType: Prefix

metrics:
  enabled: true

networkPolicy:
  ingress:
    from:
      # Use the actual ALB subnet or VPC source ranges seen by the CNI.
      - ipBlock:
          cidr: 10.40.0.0/16
  databaseCIDRs: [10.40.0.0/16]
```

NetworkPolicy defaults are deliberately restrictive. Match the actual ingress
controller, monitoring namespace, cluster DNS labels, database CIDRs, and
database port before installing. HTTPS egress is allowed because OIDC,
Cloudflare Access JWKS, and Web Push use external HTTPS services. Narrow
`httpsCIDRs` if those destinations have stable network ranges.

Install and verify:

```sh
helm upgrade --install taskboard ./charts/taskboard \
  --namespace taskboard --create-namespace \
  --values taskboard-values.yaml \
  --atomic --wait

kubectl rollout status deployment/taskboard -n taskboard
kubectl port-forward service/taskboard 8095:8095 -n taskboard
curl --fail http://127.0.0.1:8095/readyz
```

The separate metrics Service is created only when `metrics.enabled=true` and is
allowed only from the configured monitoring selector. Never route its port
through the public ingress.

## Cloudflare Access

Cloudflare Access can protect the hostname while Taskboard verifies assertions
at the origin. Replace the OIDC block with:

```yaml
taskboard:
  browserAuthMode: cloudflare_access
  cloudflareAccess:
    teamDomain: https://your-team.cloudflareaccess.com
    audience: replace-with-access-application-aud
    allowedGroups: [taskboard-users]
```

Restrict the origin security group, ingress, or Cloudflare Tunnel so requests
cannot bypass Access. Keep the Taskboard MCP bearer secret configured even when
browser authentication uses Access; non-loopback startup fails closed without
it. Service-token assertions can identify automated MCP workloads individually.

## Upgrades

Back up PostgreSQL first and review the release's migration notes. Pin an image
digest or complete version tag, render the change with `helm template`, then use
`helm upgrade --atomic --wait`. The rolling strategy starts at most one new pod
at a time and PostgreSQL serializes migrations, but an old image may not be
compatible after a schema upgrade. Roll back the image and database together
when release notes require it.
