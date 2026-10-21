# Chart

Helm chart that deploys the entire AInsel platform into a single namespace.

## What it deploys

- The two operators ([`operators/agent`](../operators/agent/),
  [`operators/event-gateway`](../operators/event-gateway/)).
- The hub backend ([`services/hub`](../services/hub/)) with its `Ingress`.
- The frontend console ([`frontend`](../frontend/)) with its `Ingress`.
- PostgreSQL event queue and Postgres (in-cluster, optional).
- The MCP server ([`services/mcp`](../services/mcp/), opt-in).
- All CRDs (`Agent`, `AgentImage`, `WebhookConnector`).

## Usage

For the full deployment guide, see [`docs/deployment.md`](../docs/deployment.md).

### Quickstart (local, zero auth)

For a first look on a local/dev cluster — nothing is exposed outside the
cluster, the hub runs without auth (auto-detected "local mode"), and you
access API and UI via port-forward:

```bash
helm install ainsel . -n ainsel --create-namespace -f values-quickstart.yaml
kubectl -n ainsel port-forward svc/hub-backend 8080:8080
curl http://localhost:8080/api/v1/stats
# UI (no-auth mode):
kubectl -n ainsel port-forward svc/hub-frontend 8081:80
```

Note: `-n` must match the chart's `namespace` value (both `ainsel` above).

### Authentication modes

`auth.mode` selects how the platform authenticates users:

| mode    | Hub                        | Frontend              | Use when |
|---------|----------------------------|-----------------------|----------|
| _(empty)_ | legacy behavior: OIDC if configured, else auto no-auth when nothing is exposed | derived | upgrading existing installs |
| `none`  | no auth middleware         | "no auth" indicator   | port-forward or behind an edge auth proxy |
| `local` | username/password accounts, admin bootstrapped from a Secret | login form | exposed installs without an IdP (`values-local.yaml`) |
| `oidc+local` | OIDC and local accounts chained: local JWTs verified by the hub, everything else by the IdP | login chooser (SSO button + form) | gradual SSO migration or a break-glass local admin next to SSO |
| `oidc`  | external OIDC provider     | IdP redirect flow     | organization SSO (`values-example.yaml`) |

In `local` and `oidc+local` mode the chart generates `ainsel-local-admin`
(bootstrap admin password) and `ainsel-local-jwt` (signing key) Secrets
unless you point `auth.local.adminPasswordSecret` / `auth.local.jwtSecretSecret`
at your own. Users are managed via **Admin → Users** in the UI or the
`/api/v1/users` API. `oidc+local` bootstraps the local admin too — delete it
via **Admin → Users** if you want SSO-only identities despite the mode.

Lint and render locally:

```bash
helm lint .
helm template . -f your-values.yaml
```

Install:

```bash
helm install ainsel . -n ainsel --create-namespace -f your-values.yaml
```

Upgrade:

```bash
helm upgrade ainsel . -n ainsel -f your-values.yaml
```

Single-namespace deployment with default values:

```bash
helm install ainsel . --namespace ainsel --create-namespace
```

The chart does not render the target `Namespace` object unless
`createNamespace: true` is set — pre-creating the namespace yourself and then
installing also works.

See `values-example.yaml` for a starting point for your own
environment-specific overlay.

## Resource Sizing

The default values in `values.yaml` are the **small** tier — sized for a
single-node dev cluster with one or two users. For production deployments,
use one of the overlay files:

```bash
# Medium tier — ~10 active agents (estimated, not yet load-tested)
helm install ainsel . -n ainsel -f chart/values-medium.yaml

# Large tier — ~100 active agents (estimated, not yet load-tested)
helm install ainsel . -n ainsel -f chart/values-large.yaml
```

| Component | Small (default) | Medium (~10 agents) | Large (~100 agents) |
|---|---|---|---|
| agentOperator | 50m/64Mi → 200m/256Mi | 100m/128Mi → 400m/512Mi | 300m/512Mi → 1/2Gi |
| connectorOperator | 50m/64Mi → 200m/256Mi | 100m/128Mi → 400m/512Mi | 300m/512Mi → 1/2Gi |
| hub | 50m/128Mi → 200m/256Mi | 200m/512Mi → 500m/1Gi | 500m/1Gi → 2/4Gi |
| ui | 10m/16Mi → 100m/64Mi | 50m/64Mi → 100m/128Mi | 100m/128Mi → 200m/256Mi |
| postgres | 50m/256Mi → 500m/512Mi | 200m/512Mi → 1/2Gi | 500m/1Gi → 2/4Gi |
| mcp | 50m/64Mi → 200m/128Mi | 100m/128Mi → 400m/256Mi | 300m/256Mi → 1/1Gi |

Values are shown as `requests → limits` (CPU/memory). Medium and large
tiers are **estimates** — measure under your actual workload and adjust.

StatefulSet storage sizes (`postgres.storage`)
are orthogonal to CPU/memory tiers. Scale storage independently based on
data volume and retention requirements.

### Fresh-install behavior and limitations

- **Namespace:** keep `createNamespace: false` (default) and use
  `--create-namespace` or a pre-created namespace. Rendering the namespace
  inside the chart fails ownership validation on Helm 3.x when the namespace
  already exists.
- **Auth:** `auth.mode` selects the authentication mode (see Authentication
  modes above). `oidc` requires a complete OIDC configuration; `local` shows a
  login form in the UI and bootstraps the admin credentials from chart-generated
  Secrets; `oidc+local` requires both halves (OIDC config **and** the local
  Secrets) and shows a login chooser offering SSO and the local form; `none`
  (and the auto-detected local mode of a quickstart install)
  runs the hub without auth middleware and the UI shows a no-auth indicator —
  only safe when nothing is exposed outside the cluster or an edge auth proxy
  sits in front of the ingress.
- **One event gateway operator per cluster:** released operator images
  (`0.1.0`) watch connector resources across all namespaces even though the
  chart sets `POD_NAMESPACE`. Running two AInsel releases in the same
  cluster makes their gateway operators fight over connector deployments.
  Namespace-scoped watching lands in a later operator release; until then
  keep a single release per cluster or set `connectorOperator.enabled: false`
  on additional releases.
- **TLS with cert-manager:** when `networkPolicy.enabled` is true, the
  default-deny policy still allows ingress-controller traffic to cert-manager
  HTTP-01 solver pods (`networkPolicy.allowAcmeSolver`, default true), so
  http01 issuance works out of the box.

### Prometheus Alert Rules (bonus)

To install CPU throttling and OOM alert rules, enable the `prometheusRules`
key in your values file:

```yaml
observability:
  prometheusRules:
    enabled: true
```

This requires the `monitoring.coreos.com/v1` PrometheusRule CRD (installed
by the Prometheus Operator). The rules are disabled by default to avoid
install failures on clusters without the Prometheus Operator.

## Notable values

The complete key reference lives in
[docs/configuration.md](docs/configuration.md) — one table per key, linked
from here. Only genuinely notable (security- or install-critical) keys are
listed:

| Key | Default | Purpose |
|---|---|----|
| [`auth.mode`](docs/configuration.md#auth) | `""` (legacy) | Authentication mode: `oidc` / `local` / `oidc+local` / `none` |
| [`auth.allowInsecureNoAuth`](docs/configuration.md#auth) | `false` | Run hub without auth middleware — local/air-gapped testing only |
| [`auth.internalValidateSecret` / `auth.skillsMcpToken`](docs/configuration.md#auth) | `""` (feature off) | Internal credentials; keep them deliberately separate |
| [`networkPolicy.enabled`](docs/configuration.md#networkpolicy) | `true` | Default-deny ingress; requires a policy-enforcing CNI |
| [`networkPolicy.allowAcmeSolver`](docs/configuration.md#networkpolicy) | `true` | Let ingress reach cert-manager HTTP-01 solver pods |
| [`createNamespace`](docs/configuration.md#global) | `false` | Render a `Namespace` object; prefer `--create-namespace` |
| [`connectorOperator.clusterWideRbac`](docs/configuration.md#connector-operator) | `true` | ClusterRole/CRB required by released operator images; one gateway operator per cluster while enabled |
| [`postgres.auth.existingSecret`](docs/configuration.md#postgres) | `""` (auto-generated) | Bring your own DB credentials (keys `username`, `password`, `database`, `dsn`) |
| [`hub.platformExternalUrl`](docs/configuration.md#hub) | `""` (derived) | Externally-reachable platform URL used in generated links |

Comments in `values.yaml` document every nested key; the tables in
[docs/configuration.md](docs/configuration.md) mirror them.

## CRDs

CRDs ship as chart templates (`templates/crds/`), so every `helm upgrade`
re-applies them and **schema changes reach existing clusters with the
release** — the chart never leaves a CRD frozen at install time. The CRD
manifests are generated by Kubebuilder in `operators/*/config/crd/bases/`
and copied here verbatim (plus a `helm.sh/resource-policy: keep`
annotation so uninstall never deletes CRDs). CI enforces the copy stays
in sync — `python3 scripts/check-crd-sync.py` fails the chart build if a
chart CRD drifts from its operator base.

## Reference

- [Deployment guide](../docs/deployment.md)
- [CRD reference](../docs/crd-reference.md)
- [Configuration reference](docs/configuration.md)