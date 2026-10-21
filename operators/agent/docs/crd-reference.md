# CRD Reference

## Agent

**API Group:** `ainsel.dev`
**Version:** `v1alpha1`
**Kind:** `Agent`

### Spec Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `displayName` | string | Yes | Human-readable agent name |
| `description` | string | No | Agent description |
| `forgejo.username` | string | Yes | Forgejo username for the agent |
| `forgejo.email` | string | Yes | Forgejo email for the agent |
| `runtime.image` | string | Yes | Container image for ainsel-ai-agent |
| `runtime.imagePullPolicy` | string | No | Image pull policy (default: IfNotPresent) |
| `runtime.resources` | ResourceRequirements | No | CPU/memory requests and limits |
| `llm.model` | string | Yes | LLM model name. No default: the runtime refuses to start without a model (issue #96) |
| `llm.provider` | string | Pi: yes (no schema default) | LLM provider backend. Enum: `ollama-cloud`, `opencode`, `alibaba-cloud`, `custom`, `openai-codex`. `openai-codex` requires a Pi subscription; native Codex/Claude runtimes use their own CLI authentication. Unknown values are rejected by the API server; an unset provider on Pi fails reconciliation with `Ready=False` / `InvalidLLMProvider` |
| `llm.maxTurns` | int | No | Maximum tool-use loop turns |
| `llm.temperature` | float64 | No | LLM temperature |
| `llm.vision` | bool | No | Whether pi sends image input (pi attaches screenshots/images only when true; the endpoint is never probed). Default false |
| `persona.inline` | string | No | Inline persona text |
| `persona.configMapRef.name` | string | No | ConfigMap containing persona |
| `persona.configMapRef.key` | string | No | Key in ConfigMap |
| `skills` | []AgentSkill | No | Additional skill ConfigMaps |
| `scaling.minReplicas` | int32 | No | Floor for queue-driven scaling, and the opt-in: unset means pin exactly `replicas`. `0` lets the agent go dormant. Must not exceed `replicas`. |
| `scaling.replicas` | int32 | No | Pod count in static mode, ceiling in queue mode. Default `1`. |
| `memory.enabled` | bool | No | Enable shared memory |
| `memory.provider` | string | No | Memory provider |
| `ollamaCloud.apiKeySecretRef` | SecretKeySelector | No | Secret containing the Ollama Cloud API key (key: `api-key`). Used when `llm.provider` is `ollama-cloud` |
| `ollamaCloud.baseUrl` | string | No | Override for the Ollama Cloud API base URL (default `https://ollama.com/v1`). Written to the generated models.json; the endpoint is never probed |
| `openCode.apiKeySecretRef` | SecretKeySelector | No | Secret containing the OpenCode API key (key: `api-key`). Used when `llm.provider` is `opencode` |
| `openCode.baseUrl` | string | No | Override for the OpenCode API base URL (default `https://opencode.ai/zen/go/v1`) |
| `alibabaCloud.apiKeySecretRef` | SecretKeySelector | No | Secret containing the Alibaba Token Plan API key (key: `api-key`). Used when `llm.provider` is `alibaba-cloud` |
| `alibabaCloud.baseUrl` | string | No | Override for the Alibaba base URL — e.g. a different MaaS region than the built-in `ap-southeast-1` endpoint |
| `customProvider.url` | string | No | Base URL of a custom OpenAI-compatible LLM API. Required when `llm.provider` is `custom` |
| `customProvider.apiKeySecretRef` | SecretKeySelector | No | Secret containing the custom provider API key (key: `api-key`) |

### Status Fields

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | []Condition | Standard Kubernetes conditions |
| `replicas` | int32 | Current ready replica count |
| `lastInvocation` | Time | Last time work was handed to the agent; the idle clock scale-down measures against |
| `pendingTasks` | int32 | Tasks queued for this agent. Written by the hub, not the operator. |
| `activeTasks` | int32 | Tasks claimed but not finished. Written by the hub. |
| `queueObservedAt` | Time | When the hub measured those counts. Past `--queue-signal-ttl` the operator holds its pod count instead of scaling down. |
| `scaling` | AgentScalingStatus | What the operator is doing with the pods: `mode` (`static`/`queue`), `desired`, `reason` and a one-line `message` |
| `observedGeneration` | int64 | Last observed spec generation |

### Example

```yaml
apiVersion: ainsel.dev/v1alpha1
kind: Agent
metadata:
  name: code-reviewer
  namespace: ainsel
spec:
  displayName: "Code Reviewer"
  description: "Reviews pull requests for code quality"
  forgejo:
    username: code-reviewer
    email: code-reviewer@ainsel.dev
  runtime:
    image: registry.example.com/ainsel/ainsel-ai-agent:latest
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 500m
        memory: 512Mi
  llm:
    model: glm-5.1:cloud
    provider: ollama-cloud
    maxTurns: 10
    temperature: 0.3
  persona:
    configMapRef:
      name: code-reviewer-persona
      key: CLAUDE.md
  scaling:
    replicas: 3
    minReplicas: 0
  memory:
    enabled: true
    provider: example
```

---

## Trigger

**API Group:** `ainsel.dev`
**Version:** `v1alpha1`
**Kind:** `Trigger`

### Spec Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `agentRef` | string | Yes | Name of the Agent to invoke |
| `connectorRef` | string | Yes | Name of the WebhookConnector source |
| `eventType` | string | Yes | Event type pattern (supports `*` wildcard) |
| `ignoreBotEvents` | bool | No | Skip events from bot actors (default: `true`) |
| `filters` | []Filter | No | Additional data payload filters (AND logic) |

### Filter Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `field` | string | Yes | Dotted path into event data |
| `op` | string | Yes | Operator: `eq`, `neq`, `prefix`, `suffix`, `contains`, `in`, `regex` |
| `value` | string | No | Single comparison value |
| `values` | []string | No | List of values (for `in` operator) |

### Example

```yaml
apiVersion: ainsel.dev/v1alpha1
kind: Trigger
metadata:
  name: review-prs-on-main
  namespace: ainsel
spec:
  agentRef: code-reviewer
  connectorRef: forgejo
  eventType: "pull_request.opened"
  ignoreBotEvents: true
  filters:
    - field: "pull_request.base"
      op: eq
      value: "main"
```

### Wildcard Event Types

```yaml
# Match all issue events
eventType: "issue.*"

# Match all events
eventType: "*"

# Match specific event
eventType: "push"
```
