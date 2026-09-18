# Taskboard Helm chart

This optional chart runs Taskboard on Kubernetes with external PostgreSQL. Its
defaults reference existing Secrets, use a non-root read-only container, omit
the service-account token, and apply probes, resources, a disruption budget,
and NetworkPolicy.

```sh
helm upgrade --install taskboard ./charts/taskboard \
  --namespace taskboard --create-namespace \
  --values taskboard-values.yaml --atomic --wait
```

The complete values, External Secrets, EKS ingress, Cloudflare Access, metrics,
and upgrade examples are in
<https://github.com/kilo666mj/taskboard/blob/main/docs/helm.md>.

The chart creates no credentials or database. Before installing, create the
Secrets selected by `database.existingSecret` and `secrets.existingSecret` and
customize NetworkPolicy selectors and CIDRs for the cluster.

Map OIDC or Cloudflare Access groups with `taskboard.roles.*Groups`. Set
`taskboard.roles.default` to `viewer` to require an explicit mapped group for
write access; the default `admin` value preserves existing installations.
Configure service-principal capabilities and limits under
`taskboard.agentPolicy`; keep `task:sensitive` out of the default capability
set unless an operator has explicitly approved agent cancellation and skipping.
Webhook signing uses `taskboard.webhook.url` and the key selected by
`secrets.webhookSecretKey`; retention remains disabled until
`taskboard.retentionDays` is greater than zero.
