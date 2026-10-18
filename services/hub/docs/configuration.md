# Configuration

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `HUB_DB_URL` | _required_ | Postgres DSN. The hub refuses to start without it: it is the event queue, the trigger and cron trigger store, the log and conversation store, and the source the metric panels read by default. |
| `HUB_PORT` | `8080` | Port for the REST API server |
| `HUB_METRICS_PORT` | `9090` | Port for Prometheus metrics |
| `HUB_NAMESPACE` | `ainsel` | Kubernetes namespace for CRD operations |
| `HUB_PROMETHEUS_URL` | _(unset)_ | Prometheus HTTP base URL, used for the token endpoints and raw PromQL only. Those return `503` when it is unset; the event metrics endpoints do not need it. |
| `HUB_METRICS_SOURCE` | `postgres` | Which backend answers `/metrics/summary` and `/metrics/timeseries`: `postgres` reads the hub's own records, `prometheus` reads the scraped `hub_*_total` counters. Anything else falls back to `postgres` with a warning at startup. See [observability.md](../../../docs/observability.md). |

### Auth (OIDC)

The chart renders the base auth vars (`HUB_AUTH_MODE`, `OIDC_ISSUER`,
`OIDC_AUDIENCE`, `OIDC_SIGNING_ALGS`, `OIDC_ROLES_CLAIM`, …) from `auth.*` values — see
[deployment.md](../../../docs/deployment.md) and the chart README. The
endpoint overrides below have **no chart values**; set them as raw hub-backend
pod env (e.g. `hub.extraEnv`):

| Variable | Default | Description |
|----------|---------|-------------|
| `OIDC_JWKS_URL` | _(unset)_ | Pin the JWKS endpoint, overriding the issuer's OIDC discovery document. Leave unset for standards-compliant IdPs. |
| `OIDC_USERINFO_URL` | _(unset)_ | Pin the userinfo endpoint, overriding discovery. Hub backend only — the frontend login flow always uses discovery. |

`OIDC_SIGNING_ALGS` is a comma-separated allowlist (default `RS256`):
`RS256`, `RS384`, `RS512`, `PS256`, `PS384`, `PS512`, `ES256`, `ES384`,
`ES512`, and `EdDSA` are supported; `HS*`, `none`, and unknown values fail
startup. Tokens must be JWT access tokens; opaque-token introspection is
not supported. Discovery must report the exact configured issuer, and
userinfo must match the validated token's `sub` before claims are used.

`OIDC_ROLES_CLAIM` defaults to `urn:zitadel:iam:org:project:roles` for existing
Zitadel installations. Configure `groups` or a nested path such as
`realm_access.roles` for other providers; exact claim keys take precedence
over dotted paths. Roles are informational: hub database records determine
permissions. `ZITADEL_ISSUER` and `OIDC_PROJECT_ID` remain aliases; neutral
variables take precedence, and an unset audience falls back to the legacy
project ID before the client ID.

## Helm Values

When deployed via the ainsel-chart:

```yaml
hub:
  image:
    repository: localhost:30500/ainsel/ainsel-hub-backend
    tag: latest
  port: 8080
  metricsPort: 9090
  namespace: ainsel
  ingress:
    enabled: true
    host: ainsel.example.com
    path: /ainsel/api
    className: nginx
  resources:
    requests:
      cpu: 50m
      memory: 128Mi
    limits:
      cpu: 200m
      memory: 256Mi
```

## Kubernetes RBAC

The hub backend needs read access to:
- `Agent` CRDs (for API and trigger resolution)
- `WebhookConnector` CRDs (for API)

And write access for API mutations (create, update, delete). Triggers and
cron triggers are stored in Postgres, not as CRDs.

The one-time [`migrate-triggers`](../cmd/migrate-triggers/main.go) tool (see
[Upgrading from CRD-based trigger storage](../../../docs/upgrade-guide.md#migrating-triggers-from-crd-storage))
additionally needs read access to `Trigger` and `CronTrigger` CRDs in
`HUB_NAMESPACE` when run in-cluster.
