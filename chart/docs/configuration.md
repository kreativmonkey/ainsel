# Configuration Reference

## Full values.yaml Reference

> Defaults in this reference mirror `chart/values.yaml`; when in doubt,
> `values.yaml` wins. Every nested key also carries a comment in
> `values.yaml` — this page is the complete key reference, other docs link
> here instead of duplicating tables.

### Global

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `namespace` | string | `ainsel` | Kubernetes namespace for all resources |
| `createNamespace` | bool | `false` | Render the `Namespace` object inside the chart. Leave `false` (recommended) and use `helm install --create-namespace` or a pre-created namespace: rendering it inside the chart makes fresh installs fail ownership validation on Helm 3.x when the namespace already exists |

### NetworkPolicy

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `networkPolicy.enabled` | bool | `true` | Apply a default-deny-ingress policy plus per-component allow rules. Requires a network stack that enforces NetworkPolicy (Calico, Cilium, k3s' built-in controller; plain flannel on upstream Kubernetes does **not** enforce) |
| `networkPolicy.ingressNamespace` | string | `ingress-nginx` | Namespace running the ingress controller that serves the hub UI. Required so browser API traffic (ingress → hub-backend) is allowed under the default-deny policy |
| `networkPolicy.allowAcmeSolver` | bool | `true` | Allow ingress-controller traffic to reach cert-manager HTTP-01 solver pods. Without it, ACME http01 issuance fails under default-deny (`wrong status code 502`). Disable only if you issue certificates via DNS-01 or provide pre-existing secrets |
| `networkPolicy.connectorWebhookSources` | list | `[]` | Additional sources allowed to deliver webhooks to connector receiver pods, as a list of NetworkPolicy `from` peer objects. Use this when a webhook producer runs inside the cluster and delivers to connectors directly instead of through the ingress controller |

### Resource Sizing Tiers

The default resource values in `values.yaml` are the **small** tier (single-node
dev cluster). Overlay files are provided for larger deployments:

| Overlay | Target | Usage |
|---|---|---|
| `values.yaml` (defaults) | 1–2 users, dev | `helm install ainsel . -n ainsel` |
| `values-medium.yaml` | ~10 active agents | `helm install ainsel . -n ainsel -f chart/values-medium.yaml` |
| `values-large.yaml` | ~100 active agents | `helm install ainsel . -n ainsel -f chart/values-large.yaml` |

Per-component resource requests/limits by tier (CPU/memory):

| Component | Small (default) | Medium (~10 agents) | Large (~100 agents) |
|---|---|---|---|
| agentOperator | 50m/64Mi → 200m/256Mi | 100m/128Mi → 400m/512Mi | 300m/512Mi → 1/2Gi |
| connectorOperator | 50m/64Mi → 200m/256Mi | 100m/128Mi → 400m/512Mi | 300m/512Mi → 1/2Gi |
| hub | 50m/128Mi → 200m/256Mi | 200m/512Mi → 500m/1Gi | 500m/1Gi → 2/4Gi |
| ui | 10m/16Mi → 100m/64Mi | 50m/64Mi → 100m/128Mi | 100m/128Mi → 200m/256Mi |
| postgres | 50m/256Mi → 500m/512Mi | 200m/512Mi → 1/2Gi | 500m/1Gi → 2/4Gi |
| mcp | 50m/64Mi → 200m/128Mi | 100m/128Mi → 400m/256Mi | 300m/256Mi → 1/1Gi |

Medium and large values are **estimates** — not yet load-tested. Measure under
your actual workload and adjust accordingly. StatefulSet storage sizes
(`postgres.storage`) are orthogonal to
CPU/memory tiers; scale them independently.

### Common scheduling & security overrides

Every component block (`agentOperator`, `connectorOperator`, `hub`, `ui`,
`postgres`, `mcp`) accepts the same tail of standard Kubernetes overrides,
written `<component>.<key>`:

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `<component>.resources` | object | per component (see tiers) | CPU/memory requests and limits |
| `<component>.nodeSelector` | object | `{}` | Node selector |
| `<component>.tolerations` | list | `[]` | Tolerations |
| `<component>.affinity` | object | `{}` | Affinity rules |
| `<component>.podAnnotations` | object | `{}` | Extra pod annotations |
| `<component>.podLabels` | object | `{}` | Extra pod labels |
| `<component>.podSecurityContext` | object | see `values.yaml` | Pod-level securityContext (`runAsNonRoot`, seccomp `RuntimeDefault`; empty for `ui`/`postgres`) |
| `<component>.securityContext` | object | see `values.yaml` | Container-level securityContext (runAsUser 65532, no privilege escalation, drop all capabilities; empty for `ui`/`postgres`) |
| `<component>.imagePullSecrets` | list | `[]` | Pull secrets for the component's images |
| `<component>.priorityClassName` | string | `""` | PriorityClass for the pods |
| `<component>.extraEnv` | list | `[]` | Extra container env entries (`{name, value}` or `{name, valueFrom}`) |
| `<component>.extraVolumes` | list | `[]` | Extra pod volumes |
| `<component>.extraVolumeMounts` | list | `[]` | Extra mounts on the main container |
| `<component>.serviceAccount.annotations` | object | `{}` | Annotations on the rendered ServiceAccount. **Only exists on `agentOperator`, `connectorOperator`, `hub`, and `mcp`** — `ui` and `postgres` do not render a ServiceAccount |

### Agent Operator

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `agentOperator.enabled` | bool | `true` | Set to `false` to skip deploying the agent operator entirely |
| `agentOperator.replicas` | int | `1` | Operator replicas |
| `agentOperator.image.repository` | string | `dpinsel/ainsel-k8s-ai-agent-operator` | Image repository |
| `agentOperator.image.tag` | string | `"0.1.0"` | Image tag |
| `agentOperator.image.pullPolicy` | string | `IfNotPresent` | Image pull policy |
| `agentOperator.forgejoUrl` | string | `""` | Forgejo base URL passed to the operator as `FORGEJO_URL`; empty disables the Forgejo integration in the operator |
| `agentOperator.forgejoTokenSecret` | object | `{}` | `{name, key}` of an existing Secret holding the Forgejo token, passed to the operator as `FORGEJO_TOKEN_SECRET_NAME` / `FORGEJO_TOKEN_SECRET_KEY`. Empty disables it |
| `agentOperator.agentGracePeriod` | string | `0` (flag omitted) | Grace period for the terminating signal sent to agent pods, passed as `--agent-grace-period`. Parsed as a Go duration, so it needs a unit: `"30m"`, not `"1800"` |
| `agentOperator.resources` | object | 50m/64Mi → 200m/256Mi | CPU/memory |

### Connector Operator

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `connectorOperator.enabled` | bool | `true` | Set to `false` to skip deploying the event gateway operator entirely |
| `connectorOperator.replicas` | int | `1` | Operator replicas |
| `connectorOperator.clusterWideRbac` | bool | `true` | Install a ClusterRole + ClusterRoleBinding. Required by the released operator images, which watch Deployments, Services, Ingresses etc. across all namespaces. With cluster-wide watching, only **one** event gateway operator may run per cluster; set to `false` once you run an operator image that honors `POD_NAMESPACE` scoping |
| `connectorOperator.hubURL` | string | `""` | Passed as `HUB_URL` to the operator, which injects it into webhook-receiver connector pods. Defaults to the chart's hub service name when empty |
| `connectorOperator.image.repository` | string | `dpinsel/ainsel-k8s-event-source-gateway-operator` | Image repository |
| `connectorOperator.image.tag` | string | `"0.1.0"` | Image tag |
| `connectorOperator.image.pullPolicy` | string | `IfNotPresent` | Image pull policy for the operator container |
| `connectorOperator.connectorImagePullPolicy` | string | `IfNotPresent` | Pull policy applied to the webhook-receiver connector pods the operator creates (**not** the operator container above). Use `Always` for mutable tags (e.g. `:dev`) so newly built images are picked up on rollout |
| `connectorOperator.resources` | object | 50m/64Mi → 200m/256Mi | CPU/memory |

### Hub

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `hub.replicas` | int | `1` | Hub backend replicas |
| `hub.image.repository` | string | `dpinsel/ainsel-hub-backend` | Image repository |
| `hub.image.tag` | string | `"0.1.0"` | Image tag |
| `hub.image.pullPolicy` | string | `IfNotPresent` | Image pull policy |
| `hub.port` | int | `8080` | API server port |
| `hub.metricsPort` | int | `9090` | Metrics port |
| `hub.namespace` | string | `""` | Namespace the hub backend operates on (agents, backfill, ownership). Defaults to the top-level `namespace` when empty. Only set this if the hub must operate on a different namespace than it is deployed to |
| `hub.ingress.enabled` | bool | `true` | Enable ingress |
| `hub.ingress.host` | string | `ainsel.example.com` | Ingress hostname |
| `hub.ingress.path` | string | `/ainsel/api` | Ingress path prefix; only `<path>/v1/*` is published |
| `hub.ingress.className` | string | `nginx` | Ingress class |
| `hub.ingress.annotations` | object | `nginx.ingress.kubernetes.io/rewrite-target: /api/$2` | Ingress annotations |
| `hub.platformExternalUrl` | string | `""` | Externally-reachable platform URL. When empty it is derived from the hub ingress host and `ui.basePath` (`https://<hub.ingress.host><basePath>`) |
| `hub.connectorImages.webhook.repository` | string | `dpinsel/ainsel-webhook-receiver` | Webhook-receiver image repository. The hub copies repository/tag/digest into a new connector's `spec.image.*`; the event-gateway operator composes the container image from them |
| `hub.connectorImages.webhook.tag` | string | `0.1.0` | The tag the receiver follows: rebuild the image and this tag moves, and the next rollout of the connector picks it up |
| `hub.connectorImages.webhook.digest` | string | `""` | Set to pin the receiver to one build — then the tag records where that build came from and nothing moves it |
| `hub.resources` | object | 50m/128Mi → 200m/256Mi | CPU/memory |

> **Security note:** the hub ingress intentionally publishes only
> `<hub.ingress.path>/v1/*`. The hub also serves `/api/internal/*` (agent task
> ack/nack, event ingest, task logs, user-token validation), which authenticate
> with the cluster-wide `X-Internal-Token` shared secret and must stay
> unreachable from outside the cluster. `/health` is likewise in-cluster only —
> the pod liveness/readiness probes call it directly. See
> `chart/templates/hub-backend/ingress.yaml` before widening the path regex.

#### Hub Environment Variables

These environment variables are read by the hub backend at startup. Pass them
via `hub.extraEnv` in your `values.yaml`.

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `TASK_CLAIM_TIMEOUT_SECONDS` | int | `1800` | Duration (in seconds) after which a `claimed` task is considered stale. A background reaper runs every 5m and resets stale claims to `pending` (or `failed` if max attempts reached) with a 30s retry delay. |
| `HUB_WS_ALLOWED_ORIGINS` | string | _empty_ | Comma-separated list of extra Origins allowed to open `/api/v1/ws`. Same-origin requests are always allowed, so this is only needed when the UI is served from a different host or port than the hub API (local development). Entries may be full origins (`https://app.example.com`) or bare hosts (`localhost:5173`). |
| `OIDC_JWKS_URL` / `OIDC_USERINFO_URL` | string | _empty_ | Optional escape hatches pinning the JWKS / userinfo endpoints instead of the issuer's discovery document. The chart has no dedicated values for these; see [docs/deployment.md](../../docs/deployment.md). |

### UI

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `ui.enabled` | bool | `true` | Enable/disable UI deployment |
| `ui.basePath` | string | `/ainsel` | Base path the frontend is served under (must match the ingress path prefix). Empty string serves from the domain root |
| `ui.image.repository` | string | `dpinsel/ainsel-hub-frontend` | Image repository |
| `ui.image.tag` | string | `"0.2.1"` | Image tag |
| `ui.image.pullPolicy` | string | `IfNotPresent` | Image pull policy |
| `ui.ingress.enabled` | bool | `true` | Enable ingress |
| `ui.ingress.host` | string | `ainsel.example.com` | Ingress hostname |
| `ui.ingress.path` | string | `/ainsel(/|$)(.*)` | Ingress path (regex form used with the rewrite-target annotation) |
| `ui.ingress.className` | string | `nginx` | Ingress class |
| `ui.ingress.annotations` | object | `nginx.ingress.kubernetes.io/rewrite-target: /$2` | Ingress annotations |
| `ui.resources` | object | 10m/16Mi → 100m/64Mi | CPU/memory |

### Postgres

In-cluster PostgreSQL (event queue + relational store). Can be disabled in
favor of an externally managed database by pointing the hub's `HUB_DB_URL`
(via `hub.extraEnv`) elsewhere and setting `postgres.enabled: false`.

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `postgres.enabled` | bool | `true` | Deploy the in-cluster Postgres StatefulSet |
| `postgres.image.repository` | string | `postgres` | Image repository |
| `postgres.image.tag` | string | `"17-alpine"` | Image tag |
| `postgres.image.pullPolicy` | string | `IfNotPresent` | Image pull policy |
| `postgres.storage` | string | `5Gi` | PVC size for the StatefulSet |
| `postgres.database` | string | `ainselhub` | Database name (used when the chart manages credentials) |
| `postgres.username` | string | `ainselhub` | Role name (used when the chart manages credentials) |
| `postgres.auth.existingSecret` | string | `""` | Name of an existing Secret to use instead of chart-generated credentials. When empty, the chart generates a random password and creates a Secret named `ainsel-hub-db`. The Secret must have keys `username`, `password`, `database`, and `dsn` (the full DSN consumed by the hub backend as `HUB_DB_URL`) |
| `postgres.resources` | object | 50m/256Mi → 500m/512Mi | CPU/memory |

### MCP Server

The AInsel MCP server ([`services/mcp`](../../services/mcp/)), opt-in.

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `mcp.enabled` | bool | `false` | Enable the AInsel MCP server |
| `mcp.image.repository` | string | `dpinsel/ainsel-mcp` | Image repository |
| `mcp.image.tag` | string | `0.1.0` | Image tag |
| `mcp.image.pullPolicy` | string | `IfNotPresent` | Image pull policy |
| `mcp.replicas` | int | `1` | MCP server replicas |
| `mcp.port` | int | `8080` | MCP server port |
| `mcp.resourceUrl` | string | `""` | Intended externally-reachable MCP URL (surfaces in the OAuth protected-resource metadata and `WWW-Authenticate` header). **Not currently wired up:** no chart template consumes this key. The MCP service reads the `MCP_RESOURCE_URL` environment variable, so pass the URL via `mcp.extraEnv` until the chart wires this key |
| `mcp.ingress.enabled` | bool | `true` | Enable ingress (also renders the OAuth metadata well-known ingresses) |
| `mcp.ingress.host` | string | `ainsel.example.com` | Ingress hostname |
| `mcp.ingress.path` | string | `/ainsel/mcp` | Ingress path |
| `mcp.ingress.className` | string | `nginx` | Ingress class |
| `mcp.ingress.annotations` | object | `{}` | Ingress annotations |
| `mcp.resources` | object | 50m/64Mi → 200m/128Mi | CPU/memory |

### Chat MCP Sidecar

Injected into every agent pod by the agent operator when **both** `repository`
and `tag` are configured (passed to the operator as `CHAT_MCP_IMAGE`). When
either is unset, no sidecar is added.

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `chatMCP.image.repository` | string | `dpinsel/ainsel-chat-mcp` | Sidecar image repository |
| `chatMCP.image.tag` | string | `"0.1.0"` | Sidecar image tag |

### MCP discovery

`mcp.externalUrl` (empty by default) enables RFC 9728 discovery for the public
MCP endpoint. Expose `/.well-known/oauth-protected-resource<resource-path>`
through your ingress. Metadata points at the configured OIDC provider;
client registration is handled there.

### Auth

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `auth.mode` | string | `""` | Authentication mode: `oidc` (external OIDC provider — requires `oidcIssuer`/`oidcClientId`), `local` (hub-managed username/password accounts, admin bootstrapped from a chart Secret — see `auth.local`), `none` (no authentication; only safe port-forwarded or behind an edge auth proxy such as Authelia/Authentik/oauth2-proxy). Empty keeps the legacy behavior: OIDC when `oidcIssuer` is configured, otherwise the chart's local-mode detection auto-enables no-auth when nothing is exposed via ingress |
| `auth.oidcIssuer` | string | `""` | OIDC issuer URL |
| `auth.oidcClientId` | string | `""` | OIDC client id |
| `auth.oidcAudience` | string | `""` | Expected JWT audience; defaults to legacy project ID, then client ID |
| `auth.oidcSigningAlgs` | string | `"RS256"` | Comma-separated JWT signing algorithm allowlist: RS256/384/512, PS256/384/512, ES256/384/512, EdDSA. Symmetric algorithms and `none` fail startup |
| `auth.oidcScopes` | string | `""` | Space-separated scopes; standard default `openid profile email offline_access`, or explicit legacy project scope |
| `auth.oidcRolesClaim` | string | `"urn:zitadel:iam:org:project:roles"` | JWT role/group claim: exact key first, then dotted nested path (e.g. `realm_access.roles`). Set `groups` for a top-level groups array. Informational; hub DB manages permissions |
| `auth.oidcProjectId` | string | `""` | Optional legacy Zitadel project audience/scope compatibility |
| `auth.allowInsecureNoAuth` | bool | `false` | Run the hub backend **without** auth middleware (`HUB_ALLOW_INSECURE_NO_AUTH=true`). Only effective when `oidcIssuer` is empty. For local/air-gapped testing ONLY: every `/api/v1/*` endpoint becomes open to any caller. Never enable on a reachable host without an external auth proxy in front of the ingress. You normally do not need to set this by hand: when no ingress is enabled anywhere (hub/ui/mcp) and no OIDC issuer is set, the chart detects "local mode" and enables it automatically (see `values-quickstart.yaml`) |
| `auth.internalValidateSecret` | string | `""` | Shared secret between hub-backend and the MCP service protecting the `/api/internal/user-tokens/validate` endpoint. When unset, user-token auth is disabled and OIDC remains the only MCP auth |
| `auth.skillsMcpToken` | string | `""` | Bearer token for the read-only skill catalogue MCP (`/api/internal/skills/mcp`, in-cluster). Agents get it as a normal secret env entry on their AgentImage and reference it from `spec.mcpServers[].tokenFromEnv`. Deliberately separate from `internalValidateSecret`: that one is the cluster-wide internal credential and must not become a bearer token for a second endpoint. Unset leaves the catalogue endpoint answering 503 — opt-in per instance |
| `auth.local.adminUser` | string | `admin` | Username of the bootstrapped admin account (created on first start, `mode: local`) |
| `auth.local.adminPasswordSecret` | string | `""` | Existing Secret holding the admin password under key `password`. When empty, the chart generates one into Secret `ainsel-local-admin` (read it with `kubectl -n <ns> get secret ainsel-local-admin -o jsonpath='{.data.password}' \| base64 -d`) |
| `auth.local.jwtSecretSecret` | string | `""` | Existing Secret holding the JWT signing key under key `secret`. When empty, the chart generates one into Secret `ainsel-local-jwt` |

### Forgejo Integration

Enables the bug-report button in the frontend. When unset, the report
button's issue-creation call fails with "Forgejo is not configured" — leave
empty to disable issue creation.

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `forgejo.apiBase` | string | `""` | Forgejo API base URL |
| `forgejo.repo` | string | `""` | Target repository for bug-report issues |

### Observability

| Value | Type | Default | Description |
|-------|------|---------|-------------|
| `observability.prometheus.url` | string | `""` | External Prometheus URL for the hub's token endpoints and raw PromQL. Leave empty to keep the hub off Prometheus entirely: event KPIs and throughput charts are still served from the hub's own database; only the token panels and raw PromQL go dark (they have no fallback) |
| `observability.metricsSource` | string | `"postgres"` | Backend answering the event KPI and timeseries panels: `postgres` (the hub's own records — written by the same routing decision that increments the counters, survive hub pod restarts) or `prometheus` (scraped `hub_*_total` counters, which still cover activity older than the hub's database retention). Changes only what the hub reads back — it still exports `/metrics` either way |
| `observability.serviceMonitor.enabled` | bool | `false` | Create ServiceMonitor resources |
| `observability.serviceMonitor.labels` | object | `{}` | Extra labels on the ServiceMonitors |
| `observability.serviceMonitor.interval` | string | `30s` | Scrape interval |
| `observability.podMonitor.enabled` | bool | `false` | Create PodMonitor resources |
| `observability.podMonitor.labels` | object | `{}` | Extra labels on the PodMonitors |
| `observability.podMonitor.interval` | string | `30s` | Scrape interval |
| `observability.prometheusRules.enabled` | bool | `false` | Install CPU throttling / OOM PrometheusRule (requires the Prometheus Operator / `monitoring.coreos.com/v1` PrometheusRule CRD) |
