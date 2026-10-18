# Deployment Guide

## Prerequisites

- Kubernetes cluster (tested on k3s)
- Helm 3
- Container registry accessible from the cluster (Zot, Docker Hub, etc.)
- Forgejo instance
- kubectl with cluster access

**PostgreSQL:** The chart bundles a PostgreSQL instance by default (`postgres.enabled: true` in `values.yaml`) — it backs the event queue, triggers, chat sessions, and all other hub state. The hub connects using the `HUB_DB_URL` secret (`ainsel-hub-db`). You do **not** need to deploy PostgreSQL separately; to use an external server, set `postgres.enabled: false` and provide your own secret with a `username`, `database`, and `dsn` keys (`postgres.auth.existingSecret`), and configure `postgres.host` / `postgres.port` for the readiness check.

## Step 1: Building Images

Images for every Ainsel component are built and pushed by CI on every merge
to `main` and on every release tag. See the CI build workflows for the build
configuration.

For local development against an out-of-cluster registry, build a single
component using the Makefile in its folder. Operator folders ship
`docker-build` / `docker-push` targets, for example:

```bash
make -C operators/agent docker-build docker-push IMG=<registry>/ainsel/agent-operator:dev
```

Other components keep their build commands per folder; check the relevant
`Makefile` or `Dockerfile` for the exact target.

## Generating Required Secrets

Before running `helm install`, generate the secrets that the chart expects.

### Webhook HMAC secret

The connector uses an HMAC secret to verify that webhook payloads originate from Forgejo. Generate a random value and store it as a Kubernetes secret:

```bash
kubectl create secret generic connector-webhook-secret \
  --from-literal=secret=$(openssl rand -hex 32) \
  -n <namespace>
```

Reference this secret name in your connector configuration or `values.yaml` as appropriate.

### Postgres secret (external database only)

By default the chart provisions its own Postgres instance and auto-generates a password. If you want to use an external database, set `postgres.enabled: false` and supply an existing secret:

```bash
kubectl create secret generic ainsel-hub-db \
  --from-literal=username=<db-user> \
  --from-literal=password=<db-password> \
  --from-literal=database=<db-name> \
  --from-literal=dsn=postgres://<db-user>:<db-password>@<host>:5432/<db-name> \
  -n <namespace>
```

Then configure the external host used by the readiness check and the existing secret:

```yaml
postgres:
  enabled: false
  host: postgres-rw.database.svc.cluster.local
  port: 5432
  auth:
    existingSecret: ainsel-hub-db
```

The secret's `dsn` must point at the same server and database.

### Image pull secret (private registry)

If your images are hosted in a private registry that requires authentication, create a pull secret and add it to the default service account (or reference it in your `values.yaml`):

```bash
kubectl create secret docker-registry regcred \
  --docker-server=<registry-host> \
  --docker-username=<username> \
  --docker-password=<password> \
  -n <namespace>
```

---

## Step 2: Create Secrets

```bash
kubectl create namespace ainsel

# Forgejo admin API token (for webhook registration and agent accounts)
kubectl create secret generic forgejo-admin-token -n ainsel \
  --from-literal=token=<your-forgejo-admin-api-token>

# Webhook HMAC secret (shared between Forgejo and the connector)
kubectl create secret generic forgejo-webhook-hmac -n ainsel \
  --from-literal=secret=<your-webhook-hmac-secret>

# LLM API key (for agent runtime pods — Ollama Cloud via pi).
# The operator looks for a secret named <agent-name>-ollama-key with key "api-key".
kubectl create secret generic code-reviewer-ollama-key -n ainsel \
  --from-literal=api-key=<your-ollama-cloud-api-key>
```

## Step 3: Configure values.yaml

The Helm chart lives at [`chart/`](../chart/) in this monorepo. Create a `values.yaml` with your settings:

```yaml
namespace: ainsel

agentOperator:
  image:
    repository: dpinsel/ainsel-k8s-ai-agent-operator
    tag: latest

connectorOperator:
  image:
    repository: dpinsel/ainsel-k8s-event-source-gateway-operator
    tag: latest

hub:
  image:
    repository: dpinsel/ainsel-hub-backend
    tag: latest
  ingress:
    enabled: true
    host: your-domain.com
    path: /ainsel/api

ui:
  enabled: true
  image:
    repository: dpinsel/ainsel-hub-frontend
    tag: latest
  ingress:
    enabled: true
    host: your-domain.com
    path: /ainsel
```

> **The database:** the hub stores everything in PostgreSQL — events, task
> deliveries, triggers, cron triggers, channels. `postgres.enabled` (default
> `true`) brings up the bundled single-pod StatefulSet; point the hub at your
> own server through its database secret instead (`host`, `port`, `user`,
> `password`, `dbname`, or a single `dsn`). The platform has no NATS
> dependency — the event queue lives in that same Postgres instance.

> **Note:** Connectors, agents, triggers, and personas are created at runtime via the hub UI or REST API (`/api/v1/connectors`, `/api/v1/agents`, `/api/v1/triggers`, `/api/v1/personas`). The chart does not bootstrap them.

## Authentication

`auth.mode` in `values.yaml` selects how the platform authenticates users:

| mode    | Hub                        | Frontend              | Use when |
|---------|----------------------------|-----------------------|----------|
| _(empty)_ | OIDC if `oidcIssuer` is set, otherwise no-auth (only auto-enabled when nothing is exposed via ingress) | derived | upgrading existing installs |
| `oidc`  | external OIDC provider     | IdP redirect flow     | organization SSO |
| `local` | hub-managed username/password accounts | login form | exposed installs without an IdP |
| `none`  | no auth middleware         | "no auth" indicator   | port-forward or behind an edge auth proxy (Authelia/Authentik/oauth2-proxy) |

Without any configuration the hub backend **refuses to start** unless it
gets either OIDC settings or an explicit no-auth opt-in — there is no
silent open-API default.

### OIDC (organization SSO)

```yaml
auth:
  mode: oidc
  oidcIssuer: "https://your-oidc-provider.example.com"
  oidcClientId: "ainsel"
  # Defaults to the client ID; set when the provider issues another aud claim.
  oidcAudience: "ainsel"
  oidcSigningAlgs: "RS256"
  oidcScopes: "openid profile email offline_access"
  # Optional, informational role/group claim; does not grant hub permissions.
  oidcRolesClaim: "groups"
```

OIDC providers must expose discovery (`/.well-known/openid-configuration`)
and issue signed JWT **access tokens** with the configured audience and
algorithm. Opaque access tokens and token introspection are not supported.
The frontend reads the issuer, client ID and scopes from the chart-rendered
`runtime-config.js` and uses them to drive the login flow.

Where supported, configure a separate API audience and set `oidcAudience`
to it. AInsel validates signature, issuer, audience, expiry and subject;
it cannot universally distinguish an ID token from an access token with
the same audience. OIDC has no provider-independent access-token type
claim. Clients must send the access token, not the ID token.

Register a **public** authorization-code client with PKCE (S256); the SPA
cannot keep a client secret. Register `https://<host><basePath>/auth/callback`
as the redirect URI and `https://<host><basePath>/` for logout. Preserve the
issuer exactly as discovery reports it, including its trailing slash: a
discovery issuer mismatch fails startup. Userinfo responses must have the
same `sub` as the validated JWT before their identity claims are used.

`auth.oidcSigningAlgs` is a comma-separated allowlist, defaulting to `RS256`.
Supported algorithms are `RS256`, `RS384`, `RS512`, `PS256`, `PS384`,
`PS512`, `ES256`, `ES384`, `ES512`, and `EdDSA`. Select only the algorithms
your provider uses. Empty lists or entries, `HS*`, `none`, and unknown
algorithms fail startup.

For **Authentik**, use the per-provider issuer shown in discovery, typically
`https://<authentik-host>/application/o/<application-slug>/`, and a public
client. Select an asymmetric signing key, leave token encryption disabled,
and include the `offline_access` scope mapping when refresh tokens are
wanted. Set `oidcRolesClaim: groups` if your scope mapping emits a groups
array in the access token. See the [Authentik OAuth2 provider guide](https://docs.goauthentik.io/add-secure-apps/providers/oauth2/).

`auth.oidcProjectId` is an optional legacy Zitadel compatibility setting. It
supplies the audience and project scope only when explicitly set; other
providers need no project ID. Configure **Zitadel** to issue JWT access
tokens; its project ID remains the audience when both legacy project and
client IDs are configured. The default scopes then include
`urn:zitadel:iam:org:project:id:<project-id>:aud`. An explicit `oidcAudience`
or `oidcScopes` overrides the corresponding default.
`OIDC_ISSUER` / `OIDC_AUDIENCE` are the common
service environment variables. Existing `ZITADEL_ISSUER` / `OIDC_PROJECT_ID`
remain aliases, with the neutral variables taking precedence.

JWT roles default to the legacy `urn:zitadel:iam:org:project:roles` claim.
Use `auth.oidcRolesClaim` for another claim, such as `groups` or the nested
path `realm_access.roles`. An exact claim key takes precedence over dotted
path traversal, so namespaced keys containing dots remain usable. Role
arrays contain strings; role objects use their keys as role names. Roles
are informational.
Administration and resource permissions remain explicit records in the hub
DB; an IdP group alone does not grant admin access.

#### Pinning the JWKS / userinfo endpoints

The hub backend resolves the JWKS and userinfo endpoints from the issuer's
discovery document at startup. Two optional, hub-backend-only escape hatches
pin them explicitly:

```yaml
hub:
  extraEnv:
    - name: OIDC_JWKS_URL
      value: "https://your-oidc-provider.example.com/custom/keys"
    - name: OIDC_USERINFO_URL
      value: "https://your-oidc-provider.example.com/custom/userinfo"
```

- Each variable overrides the corresponding endpoint from the discovery
  document. Leave both unset for standards-compliant providers — that is the
  recommended setup.
- The chart has no dedicated values for these; pass them via `hub.extraEnv`
  (raw pod env) as above.
- They affect the hub backend's token validation and user enrichment only.
  The frontend login flow always uses the issuer's standard discovery and
  never reads these variables.
- They pin endpoints; they do not bypass discovery. If the discovery document
  itself is unreachable at startup, the hub fails startup even when both are
  set.

### Local users (no IdP required)

```yaml
auth:
  mode: local
  local:
    adminUser: admin   # optional, defaults to admin
```

On first start the hub bootstraps an admin account. The chart generates the
password and the JWT signing key into Secrets (stable across upgrades):

```bash
kubectl -n ainsel get secret ainsel-local-admin \
  -o jsonpath='{.data.password}' | base64 -d
```

To bring your own credentials point `auth.local.adminPasswordSecret`
(key `password`) and `auth.local.jwtSecretSecret` (key `secret`) at
existing Secrets instead. Further users are managed via the UI
(**Admin → Users**) or the `/api/v1/users` API; every user can change their
own password under Profile. See `chart/values-local.yaml` for a full
example.

### No auth (local or proxied installs)

For port-forward access use `chart/values-quickstart.yaml`, which disables
all ingresses; the chart then detects "local mode" and enables no-auth
automatically. To serve an unauthenticated instance through an ingress
behind an edge auth proxy set:

```yaml
auth:
  mode: none
```

Never use `mode: none` on a reachable host without such a proxy in front of
the ingress.

---

## Step 4: Install

```bash
helm install ainsel ./chart -n ainsel -f values.yaml
```

The chart does not create the namespace itself by default (`createNamespace:
false`), so either pre-create it as in Step 2 or install with
`--create-namespace` instead.

## Step 5: Create a WebhookConnector and Configure the Forgejo Webhook

Create a connector via the hub UI (Settings → Connectors → New) or the REST API. Once saved, the connector operator provisions an Ingress whose path is shown in the connector detail view.

Point Forgejo at that URL:

1. Go to Forgejo Site Administration > Webhooks (or per-org webhooks)
2. Add webhook:
   - **URL**: The webhook endpoint shown in the hub UI connector detail
   - **Secret**: The HMAC secret you configured on the connector
   - **Content Type**: `application/json`
   - **Events**: Select issues, pull requests, push, etc.

## Step 6: Verify

```bash
# Check all pods are running
kubectl get pods -n ainsel

# Check CRDs are registered (no resources expected on a fresh install — create them via UI)
kubectl get agentimages,webhookconnectors -n ainsel

# Test the API (requires a bearer token)
curl -H "Authorization: Bearer $TOKEN" https://your-domain.com/ainsel/api/v1/agents

# The /health endpoint is in-cluster only (used by the pod probes); the ingress
# publishes /api/v1/* and nothing else.
kubectl run curl-test -n ainsel --rm -it --image=curlimages/curl --restart=Never -- \
  curl -s http://hub-backend:8080/health
```

## ArgoCD Deployment

For GitOps, create an ArgoCD Application that points at this repo's `chart/` directory:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ainsel
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://your-forgejo/ainsel-project/ainsel.git
    targetRevision: main
    path: chart
    helm:
      valueFiles:
        - values.yaml
  destination:
    server: https://kubernetes.default.svc
    namespace: ainsel
  syncPolicy:
    automated:
      selfHeal: true
      prune: true
```

## Upgrading

```bash
helm upgrade ainsel ./chart -n ainsel -f values.yaml
```

Note: Helm does not update CRDs on upgrade. If CRDs changed, apply them manually:

```bash
kubectl apply -f chart/templates/crds/
```

## Next steps

Once the platform is running, see the
[administrator guide](administrator-guide.md) to configure your first
agent, trigger, and connector. The guide includes a cookbook with
worked examples (code review, issue triage, comment Q&A,
issue → PR implementation).

For troubleshooting common deployment issues, see [docs/troubleshooting.md](troubleshooting.md).

### F5 NGINX connector routing

For F5 NGINX Ingress Controller, set `connectorOperator.ingressMode` to
`nginx-inc-minion` and deploy an operator image containing this setting. The
chart passes `CONNECTOR_INGRESS_MODE`; empty or unset keeps the existing
`ingress-nginx` behavior. Unsupported values stop operator startup.

Minion mode creates connector Ingresses with F5's mergeable Minion annotation
and `nginx.org/rewrites: "serviceName=<connector-service> rewrite=/publish"`,
preserving the exact webhook path and backend. This uses F5's `proxy_pass` URI
rewrite, avoiding invalid `rewrite` directives generated for exact paths by
`nginx.org/rewrite-target` on F5 NGINX Ingress Controller 5.5.4. TLS and HTTPS
redirects belong to an externally managed Master; the operator omits them from
Minions. Existing Ingresses update on operator startup.

Before enabling it, configure one F5 Master per webhook hostname with the same
`nginx` Ingress class, the TLS certificate, and no paths. Move existing UI/API
paths into a separate Minion; adding a Master annotation to an Ingress that
still has paths is invalid. All Minions for a host must have distinct paths.
The operator does not create the Master. Plan this conversion together with
the GitOps rollout to preserve UI/API access.

See the [F5 Mergeable Ingress example](https://github.com/nginx/kubernetes-ingress/tree/main/examples/ingress-resources/mergeable-ingress-types)
and [F5 rewrite annotations](https://docs.nginx.com/nginx-ingress-controller/configuration/ingress-resources/advanced-configuration-with-annotations/).
