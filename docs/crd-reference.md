# CRD Reference

> For the admin-facing model — how to use these CRDs to configure agents
> for your org — see the
> [administrator guide](administrator-guide.md). This document is the raw
> spec derived from `shared/api/api/v1alpha1/*.go`.
>
> **Note:** `Trigger` and `CronTrigger` were moved from Kubernetes CRDs to
> database tables.
> Their Go types still exist in `shared/api/api/v1alpha1/` as the canonical
> schema used by the hub REST API, and are documented below for schema
> completeness.

All CRDs use API group `ainsel.dev` version `v1alpha1`.

---

## Agent

Defines an AI agent with its LLM configuration, persona, runtime settings,
and scaling parameters.

The agent operator reconciles `Agent` resources into Kubernetes Deployments.
An `Agent` references an [`AgentImage`](#agentimage) via `spec.imageRef.name`
for its container image and tool catalog.

### Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `displayName` | string | Yes | Human-readable agent name |
| `description` | string | No | Agent description |
| `imageRef.name` | string | Yes | Name of the `AgentImage` CR in the same namespace that provides the container image and tool catalog |
| `runtime.engine` | string | No | Task runtime: `pi` (default), `codex`, or `claude`. Native engines require an image containing the official CLI and `/opt/ainsel/subscription/runner.mjs` |
| `runtime.subscription.createClaim` | bool | `false` | Create a retained 5Gi Filesystem `ReadWriteOncePod` PVC using the default StorageClass. Only reuse claims labeled with this Agent UID; no Agent owner reference |
| `runtime.subscription.claimName` | string | Subscription providers | Dedicated `ReadWriteOncePod` PVC. Pi `openai-codex` mounts `/home/agent/.pi/agent`; native engines mount `/home/agent`. Retained independently of the Agent |
| `runtime.subscription.login` | bool | No | Default false. Drain existing task Jobs, withhold new dispatch and create `login-<agent>-0` pod for direct end-user CLI sign-in. False removes the login pod before resuming tasks |
| `runtime.imagePullPolicy` | string | No | Kubernetes image pull policy (`Always`, `Never`, `IfNotPresent`) |
| `runtime.resources` | ResourceRequirements | No | CPU/memory requests and limits for the agent pod |
| `llm.model` | string | Yes | LLM model identifier (e.g. `unsloth/gpt-oss-120b-GGUF:F16`) |
| `llm.provider` | string | Pi: yes (no schema default) | LLM provider backend. Enum: `ollama-cloud`, `opencode`, `alibaba-cloud`, `custom`, `openai-codex`. `openai-codex` requires a Pi subscription; native Codex/Claude runtimes use their own CLI authentication. Unknown values are rejected by the API server; an unset provider on Pi fails reconciliation with `Ready=False` / reason `InvalidLLMProvider` |
| `llm.maxTurns` | int | No | Maximum tool-use loop turns |
| `llm.temperature` | float64 | No | LLM sampling temperature |
| `llm.vision` | bool | No | Whether pi sends image input. When true the operator advertises `"input": ["text", "image"]` to pi, so screenshots and image attachments are attached; while false pi sends none, even to a model that can see them. The endpoint is never probed: images sent to a model that cannot see them fail the request. Takes effect when the agent's pod next starts (the init container copies `models.json` in at start). Default false |
| `persona.id` | string | Yes | ULID of a persona managed by the hub. The operator mounts a ConfigMap named `persona-<id>` (rendered by the hub) at `/etc/agent/persona.md` |
| `enabledTools[]` | []string | No | List of tool names to enable for this agent (e.g. `forgejo`, `git`, `shell`) |
| `skills.items[]` | []string | No | Agent-scoped skill selection (skill ids). Present = explicit override (`items: []` = no skills at all); absent = inherit the referenced image's `enabledSkills` |
| `mcp.servers[]` | []AgentMCPServer | No | Agent-scoped MCP servers (`name`, `url`, `tokenFromEnv`), resolved from the hub's MCP registry when the agent is written. Present = explicit override (`servers: []` = connect to none); absent = inherit the image's `mcpServers`. The operator injects `MCP_SERVERS` / `MCP_SERVER_TOKENS` from this list |
| `env[]` | []AgentEnvVar | No | Per-agent environment variables (`name`, `value`, `secret`, `valueFrom`) layered on top of the referenced image's `env`: a matching name overrides the image's value and secret flag, a new name is added. Literal values go into the `<agent>-image-env` Secret. `valueFrom.secretKeyRef` passes an existing Secret reference directly to the agent container; it overrides an image default of the same name |
| `env[].valueFrom.secretKeyRef` | SecretKeySelector | No | Existing Secret in the Agent namespace (`name`, `key`, optional `optional`). Mutually exclusive with a nonempty `value`. The operator never reads or copies the referenced value. Missing required Secrets or keys prevent pod startup. New pods resolve the current value; restart existing pods after rotation. Available through the CRD; the hub masks references and rejects env edits for agents containing them |
| `scaling.replicas` | int32 | No | Concurrency cap: the maximum number of per-task Jobs the operator keeps in flight for this agent (default 1). Subscription providers cap positive values at 1. Each queued task gets its own Job up to this cap; `0` pauses the agent (nothing is dispatched) |
| `scaling.disabled` | bool | No | Set by the operator for a stopped agent: hold at zero even if the hub still counts queued work. Cleared on start |
| `pause.pausedUntil` | Time | No | Pause window end. **Present = paused**: while active (or while `pausedUntil` is omitted = indefinite) the hub's claim endpoint hands this agent no tasks, so routed tasks accumulate as a backlog that drains in id order when the pause ends — nothing is lost. Expiry is evaluated at read time, so an expiring pause needs no controller. Orthogonal to `scaling`, which governs how many tasks run at once, not whether they are delivered. Managed via `POST`/`DELETE /api/v1/agents/{name}/pause` or by editing the CR. Caveat: while an **indefinite** pause is active nothing prunes the backlog — `agent_tasks` and invocation rows keep growing until the pause is lifted |
| `memory.enabled` | bool | Yes | Enable shared memory |
| `memory.provider` | string | No | Memory provider |
| `ollamaCloud.apiKeySecretRef` | SecretKeySelector | No | Secret containing the Ollama Cloud API key (key: `api-key`). Used when `llm.provider` is `ollama-cloud` |
| `ollamaCloud.baseUrl` | string | No | Override for the Ollama Cloud API base URL (default `https://ollama.com/v1`). Written to the generated `models.json`; the endpoint is never probed |
| `openCode.apiKeySecretRef` | SecretKeySelector | No | Secret containing the OpenCode API key (key: `api-key`). Used when `llm.provider` is `opencode` |
| `openCode.baseUrl` | string | No | Override for the OpenCode API base URL (default `https://opencode.ai/zen/go/v1`) |
| `alibabaCloud.apiKeySecretRef` | SecretKeySelector | No | Secret containing the Alibaba Token Plan API key (key: `api-key`). Used when `llm.provider` is `alibaba-cloud` |
| `alibabaCloud.baseUrl` | string | No | Override for the Alibaba base URL — e.g. a different MaaS region than the built-in `https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1`, or a private compatible-mode endpoint |
| `customProvider.url` | string | No | Base URL of a custom OpenAI-compatible LLM API (e.g. `https://api.openai.com/v1`). Required when `llm.provider` is `custom` (the `customProvider` block is optional, but `url` is mandatory when `customProvider` is specified) |
| `customProvider.apiKeySecretRef` | SecretKeySelector | No | Secret containing the custom provider API key (key: `api-key`) |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | []Condition | `Ready`, `ConsumerReady`, `Degraded` |
| `replicas` | int32 | Containers running now (operator, from the Deployment) |
| `pendingTasks` | int32 | Tasks waiting for this agent — **written by the hub**, read by the operator |
| `activeTasks` | int32 | Tasks claimed and in flight — hub-written; scale-down waits for these to finish |
| `queueObservedAt` | Time | When the hub last *measured* this agent's queue — hub-written. If it goes stale the operator holds its container count instead of scaling down |
| `lastInvocation` | Time | Last queue activity (hub-written idle clock driving the quiet window). `nil` means the hub has never observed this agent, which is not the same as its queue being empty |
| `scaling` | AgentScalingStatus | The operator's decision: `desired` count, `mode` (`static` or `queue`), `reason` and `message` explaining it (`ScaledToZero`, `Draining`, `QueueSignalStale`, …) |
| `paused` | bool | Display mirror of the effective pause state of `spec.pause`, written by the operator each reconcile (requeued at expiry so it self-clears). `kubectl get agents` shows it. The authoritative gate is the hub evaluating `spec.pause` at read time, so this may lag briefly |
| `observedGeneration` | int64 | Last observed generation |

The hub and the operator both write this status, and neither clobbers the other's
fields: each patches only its own (`pendingTasks`, `activeTasks`, `queueObservedAt`,
`lastInvocation` for the hub; `replicas`, `scaling`, conditions for the operator). The
split is load-bearing — the operator scales on a drain signal it does not own, so it
must not be the one to clear it.

### Example

```yaml
apiVersion: ainsel.dev/v1alpha1
kind: Agent
metadata:
  name: code-reviewer
  namespace: ainsel
spec:
  displayName: "Code Reviewer"
  description: "Reviews pull requests for code quality and correctness"
  imageRef:
    name: ainsel-ai-agent-latest
  runtime:
    imagePullPolicy: IfNotPresent
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 500m
        memory: 512Mi
  llm:
    model: unsloth/gpt-oss-120b-GGUF:F16
    provider: ollama-cloud
    maxTurns: 25
    temperature: 0.3
  persona:
    id: 01HQTS7XKZ4P9XRN3Q8X9XKXKW
  enabledTools:
    - forgejo
    - git
    - shell
  skills:
    items:
      - git-review
  mcp:
    servers:
      - name: memory-server
        url: http://memory-server.ainsel.svc.cluster.local:8080/mcp
  env:
    # Layered on top of the referenced image's env: LOG_LEVEL overrides the
    # image's value, FEATURE_FLAGS is added.
    - name: LOG_LEVEL
      value: debug
    - name: FEATURE_FLAGS
      value: beta
  scaling:
    # At most two tasks run at once, each in its own Job; with an empty queue
    # the agent runs nothing at all — there is no standing container.
    replicas: 2
  memory:
    enabled: true
    provider: example
  ollamaCloud:
    apiKeySecretRef:
      name: ollama-cloud-apikey
      key: api-key
```

---

## AgentImage

Defines a container image for an agent along with its tool catalog,
environment variables, MCP server configuration, and sidecar containers.
Referenced by `Agent.spec.imageRef.name`.

### Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `displayName` | string | Yes | Human-readable image name |
| `description` | string | No | Image description |
| `imageURL` | string | Yes | Container image URL (e.g. `registry.example.com/ainsel/ainsel-ai-agent:latest`) |
| `tools[]` | []AgentImageTool | No | Tools exposed by this image |
| `tools[].name` | string | Yes | Tool name |
| `tools[].kind` | string | Yes | Tool kind: `container`, `shell`, or `mcp` |
| `tools[].description` | string | No | Tool description |
| `tools[].mcpSource` | string | No | MCP source identifier (for `mcp` kind tools) |
| `tools[].disabled` | bool | No | Whether the tool is disabled (false = enabled) |
| `tools[].isNew` | bool | No | True if added by last refresh; cleared on next PUT |
| `tools[].examples[]` | []AgentImageToolExample | No | Usage examples for the tool |
| `tools[].examples[].title` | string | Yes | Example title |
| `tools[].examples[].snippet` | string | Yes | Example code snippet |
| `env[]` | []AgentImageEnvVar | No | Environment variables injected into every agent pod using this image. Operator-set vars take precedence. When `secret` is true, the hub API masks the value |
| `env[].name` | string | Yes | Environment variable name |
| `env[].value` | string | Yes | Environment variable value |
| `env[].secret` | bool | No | If true, the value is treated as sensitive and masked by the hub API |
| `mcpServers[]` | []AgentImageMCPServer | No | External MCP servers whose tools are imported into the image tool catalog via refresh |
| `mcpServers[].name` | string | Yes | MCP server name |
| `mcpServers[].url` | string | Yes | MCP server URL |
| `mcpServers[].tokenFromEnv` | string | No | Name of an agent-pod env var whose value is forwarded as the bearer token. Empty means anonymous. Must match `^[A-Z_][A-Z0-9_]*$` (max 64 chars) |
| `sidecars[]` | []AgentImageSidecar | No | Sidecar containers injected alongside the agent runtime in the same pod |
| `sidecars[].name` | string | Yes | Container name and MCP server name used in `MCP_SERVERS` |
| `sidecars[].image` | string | Yes | Container image URL |
| `sidecars[].port` | int32 | No | Container listen port (default: 8080) |
| `sidecars[].mcpPath` | string | No | When non-empty, the operator appends an `MCP_SERVERS` entry pointing to this sidecar (e.g. `/mcp`) |
| `sidecars[].env[]` | []AgentImageEnvVar | No | Environment variables for the sidecar container |
| `enabledSkills[]` | []string | No | List of skill names enabled for this image |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `phase` | string | `Ready` |

### Example

```yaml
apiVersion: ainsel.dev/v1alpha1
kind: AgentImage
metadata:
  name: ainsel-ai-agent-latest
  namespace: ainsel
spec:
  displayName: "AInsel AI Agent (latest)"
  description: "Default agent image with forgejo, git, and shell tools"
  imageURL: registry.example.com/ainsel/ainsel-ai-agent:latest
  tools:
    - name: forgejo
      kind: container
      description: "Forgejo MCP server for issue/PR operations"
    - name: git
      kind: shell
      description: "Git operations via shell"
    - name: memory
      kind: mcp
      mcpSource: memory-server
      description: "Shared memory MCP server"
      examples:
        - title: Store a memory
          snippet: "memory_store(content=\"hello\", user_id=\"agent1\")"
  env:
    - name: LOG_LEVEL
      value: info
    - name: API_KEY
      value: secret-value
      secret: true
  mcpServers:
    - name: memory-server
      url: http://memory-mcp.ainsel.svc:8080/mcp
      tokenFromEnv: MEMORY_MCP_TOKEN
  sidecars:
    - name: sqlite-mcp
      image: registry.example.com/ainsel/sqlite-mcp:latest
      port: 9090
      mcpPath: /mcp
  enabledSkills:
    - code-review
    - pr-summary
```

---

## WebhookConnector

Configures a webhook receiver for an external source (e.g. Forgejo, GitHub),
including the webhook endpoint, HMAC verification, and container image.

> The previous `ForgejoConnector` CRD was replaced by this generic
> `WebhookConnector`. The old fields (`url`, `externalUrl`, `credentials`,
> `events`) no longer exist.

### Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `displayName` | string | No | User-facing name |
| `webhookEndpoint` | string | Yes | URL to paste into the source webhook settings. Set by the hub when creating the connector; read-only thereafter |
| `signatureHeader` | string | Yes | HTTP header the sender puts the HMAC-SHA256 signature in (e.g. `X-Hub-Signature-256` for GitHub, `X-Forgejo-Signature` for Forgejo) |
| `webhookSecret.secretRef.name` | string | Yes | Name of the K8s Secret holding the MAC key |
| `webhookSecret.secretRef.key` | string | Yes | Key in the Secret (key: `secret`) |
| `image.repository` | string | Yes | Container image repository |
| `image.tag` | string | Yes | Container image tag |
| `disabled` | bool | No | Scales the Deployment to zero when true |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | []Condition | `Ready`, `Disabled` |
| `observedGeneration` | int64 | Last observed generation |

### Example

```yaml
apiVersion: ainsel.dev/v1alpha1
kind: WebhookConnector
metadata:
  name: c-0c4b01e3 # the connector's generated id — set by the hub, routes events
  namespace: ainsel
spec:
  displayName: "connector-forgejo-ainsel"
  webhookEndpoint: "https://ainsel.example.com/webhooks/c-0c4b01e3"
  signatureHeader: "X-Forgejo-Signature"
  webhookSecret:
    secretRef:
      name: connector-c-0c4b01e3-webhook-hmac
      key: secret
  image:
    repository: dpinsel/ainsel-webhook-receiver
    tag: latest
```

---

## Trigger

> **DB-backed (not a Kubernetes CRD).** `Trigger` was moved from a CRD to a
> database table. The Go type in
> `shared/api/api/v1alpha1/trigger_types.go` remains the canonical schema used
> by the hub REST API. It is documented here for schema completeness.

Routes events from a connector to an agent, with optional filters.

### Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `displayName` | string | Yes | User-facing trigger name |
| `agentRef` | string | Yes | **Id** of the agent that should receive matching events (the `a-…` name of its `Agent` CR) — display names are not valid refs |
| `connectorRef` | string | Yes | **Id** of the connector that sources events (the `c-…` name of its `WebhookConnector` CR) — display names are not valid refs |
| `filters[]` | []Filter | No | Event filters to apply before delivering to the agent |

#### Filter

Each filter has three fields:

| Field | Type | Description |
|-------|------|-------------|
| `field` | string | Dot-separated path into the event payload (e.g. `repo`, `action`, `issue.labels.name`). A path that crosses an array collects the value from every element, so `issue.labels.name` is the list of label names |
| `op` | string | Comparison operator (see table below) |
| `value` | string | Comparison value (used by all operators except `in`/`not-in`) |
| `values` | []string | Comparison values for `in` and `not-in` operators |

**Supported operators:**

| Operator | Description |
|----------|-------------|
| `eq` | Exact string match |
| `neq` | Not equal |
| `contains` | Field contains the value as a substring |
| `not-contains` | Field does not contain the value |
| `prefix` | Field starts with the value |
| `suffix` | Field ends with the value |
| `in` | Field value is one of the entries in `values` |
| `not-in` | Field value is not in `values` |
| `regex` | Field matches the value as a regular expression |

Filters are combined with AND logic — all filters must match for the trigger to fire.

### Status

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | []Condition | `AgentRefValid`, `ConnectorRefValid` |
| `observedGeneration` | int64 | Last observed generation |

### Example

```yaml
# DB-backed schema (not a Kubernetes CRD). Shown as YASL for illustration.
displayName: "Code Review on PR Open"
agentRef: a-3f9a2b
connectorRef: c-1a2b3c
filters:
  - field: type
    op: eq
    value: pull_request
```

---

## CronTrigger

> **DB-backed (not a Kubernetes CRD).** `CronTrigger` was moved from a CRD to
> a database table. The Go type in
> `shared/api/api/v1alpha1/crontrigger_types.go` remains the canonical schema
> used by the hub REST API. It is documented here for schema completeness.

Schedules a recurring prompt delivered to an agent on a cron schedule.
Unlike a webhook-driven `Trigger`, a `CronTrigger` has no connector — the
hub emits a synthetic event on the schedule (connector `cron`) and enqueues
the task directly for the trigger's agent.

### Spec

| Field | Type | Required | Description |
|--------|------|----------|-------------|
| `displayName` | string | Yes | User-facing cron trigger name |
| `agentRef` | string | Yes | Name of the Agent that should receive the scheduled prompt |
| `schedule` | string | Yes | Standard 5-field cron expression (minute hour day-of-month month day-of-week, in the hub's local time). E.g. `0 9 * * 1-5` fires at 09:00 on weekdays |
| `prompt` | string | Yes | Text delivered to the agent as the user message on each fire. Sent verbatim — no event template rendering |
| `enabled` | bool | No | Whether the schedule is active. Defaults to `true` when unset. Allows pausing without deleting |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | []Condition | `AgentRefValid`, `ScheduleValid`, `Ready` |
| `lastRun` | Time | Last time the cron trigger fired and published an event |
| `nextRun` | Time | Next scheduled fire time computed by the hub |
| `observedGeneration` | int64 | Last observed generation |

### Example

```yaml
# DB-backed schema (not a Kubernetes CRD). Shown as YAML for illustration.
displayName: "Daily Code Review Summary"
agentRef: code-reviewer
schedule: "0 9 * * 1-5"
prompt: "Summarize all open pull requests and post a daily digest."
enabled: true
```
### Native subscription CLI login

Native runtimes use the official `codex` or `claude` CLI with direct end-user
sign-in. AInsel does not extract, translate, read, or distribute CLI credentials.
Use one dedicated PVC per agent/account. Its `ReadWriteOncePod` access mode
requires a CSI driver supporting RWOP (Kubernetes 1.29 or newer recommended);
`ReadWriteOnce` does not prevent concurrent writers on the same node and is
rejected. Set `runtime.subscription.createClaim: true` to let the operator create
a 5Gi Filesystem claim using the cluster default StorageClass, or provision
one separately. With `createClaim`, the hub generates `<agent-id>-home` if the
API request omits `claimName`; direct Kubernetes manifests must supply the name.
Managed claims carry the creating Agent UID in the
`ainsel.dev/subscription-agent-uid` label and cannot be adopted by a replacement
Agent, even when selected through existing-claim mode. Unlabeled manually
provisioned claims remain supported. Claims have no Agent owner reference and remain after Agent deletion.
Delete them manually only when the credentials are no longer needed. Use your
storage class's encryption and backup policies. A default StorageClass and CSI
driver supporting RWOP are required for automatic provisioning.

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: reviewer-cli-home
spec:
  accessModes: [ReadWriteOncePod]
  resources:
    requests:
      storage: 1Gi
```

Select an `AgentImage` built for the native runtime: Node.js, the unmodified
official CLI, and `runtimes/subscription/runner.mjs` installed at
`/opt/ainsel/subscription/runner.mjs`. The existing Pi image is not a supported
native image. Both the CLI and runner must work as UID/GID 1000 with a read-only
root filesystem. The PVC is writable at `/home/agent`; `HOME`, `CODEX_HOME`, and
`CLAUDE_CONFIG_DIR` point into that private home. CLI API key and API endpoint
overrides are withheld so subscription sign-in remains authoritative.

For an Agent named `reviewer`, enable login mode after configuring its image:

```sh
kubectl -n ainsel patch agent reviewer --type merge -p '{"spec":{"runtime":{"engine":"codex","subscription":{"claimName":"reviewer-cli-home","login":true}}}}'
kubectl -n ainsel wait --for=condition=Ready pod/login-reviewer-0 --timeout=300s
kubectl -n ainsel exec -it login-reviewer-0 -- codex login --device-auth
# For runtime.engine: claude, sign in directly through the official CLI:
# kubectl -n ainsel exec -it login-reviewer-0 -- claude auth login
kubectl -n ainsel patch agent reviewer --type merge -p '{"spec":{"runtime":{"subscription":{"login":false}}}}'
```

Login mode lets existing task Jobs finish before creating the pod; long tasks
can make the wait exceed its timeout. New tasks remain queued. Turning login
off deletes the login pod and waits for deletion before resuming dispatch. The
CSI RWOP constraint also prevents overlap while a pod is terminating. Native
workers run at most one task Job per agent, even if `scaling.replicas` is larger;
zero still pauses execution. Login containers receive only CLI home environment
variables and no hub credentials. They have no Kubernetes service account token.
Each native task receives a hub capability valid only for its own task and
conversation messages. It expires after 24 hours; a Job still running or
retrying after that needs to be replaced.
Restrict namespace `pods/exec` access to the account owner and administrators:
anyone who can exec into these containers can use that signed-in account.

Native Jobs omit sidecars and process namespace sharing to keep the private home
inaccessible through another container's `/proc`. Pi-specific skill installation,
container tools, chat sidecars, and automatic MCP configuration are unavailable
in this native runtime path. Configure supported CLI features directly through
the official CLI; do not place tokens in Agent YAML. Subscription eligibility,
limits, and permitted usage remain governed by the CLI provider and the user's
account plan.

For public GitHub pull-request events, the runner fetches the patch from
GitHub's fixed API host, limits it to 512 KiB, and includes it as untrusted
review context. Set `AINSEL_REVIEW_REPO` (for example,
`kreativmonkey/ainsel`) on the Agent to restrict which repository it will
review. Findings appear in the AInsel task transcript; this runtime does not
post GitHub review comments.

### Pi with a ChatGPT subscription

Use the Pi image with `spec.runtime.engine: pi`, `spec.llm.provider:
openai-codex` and `spec.runtime.subscription.claimName` referencing a dedicated
Filesystem `ReadWriteOncePod` PVC. The operator persists `/home/agent/.pi/agent`
(including Pi's `auth.json`) while retaining Pi initialization, skills and the
normal Pi task runner. The built-in Codex OAuth provider and model catalog are
used directly; AInsel does not generate an OpenAI Completions override.

Set `spec.runtime.subscription.login: true` to drain tasks and create the login
pod. For agent `reviewer`, connect with:

```sh
kubectl -n ainsel exec -it login-reviewer-0 -- pi
```

Inside Pi, run `/login`, select OpenAI Codex and choose device code login.
Complete the browser confirmation, exit Pi, then set
`spec.runtime.subscription.login: false`. The login pod must terminate before
new tasks start. Positive concurrency limits are capped at one to protect
refresh-token updates on the shared authentication directory. No separate
worker node or Codex CLI sandbox is needed for this provider flow.

The operator and hub never inspect the credential file. Keep the PVC private
to this agent. Login pods receive no hub credentials, provider API keys or MCP
sidecars. Pi login and task pods use the same runtime image and auth directory.

In the console, select Pi, enable **ChatGPT subscription**, choose the credential
PVC and enable login mode, then save. The console selects `openai-codex` as the
provider. Select an explicit model from Pi's built-in Codex catalog (the
operator passes both provider and model to Pi). Existing native engines remain
available independently.
