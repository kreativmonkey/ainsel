# Pi Runtime

Pi-native AInsel agent runtime. The container image runs upstream
[`pi`](https://pi.dev) (`@earendil-works/pi-coding-agent`) in RPC mode
with AInsel-specific pi extensions loaded.

There is **no wrapper process**. Pi is the only thing running in the
pod; the AInsel logic lives entirely in pi extensions.

## Role in the platform

See [`../docs/architecture.md`](../docs/architecture.md).

Each `Agent` CR managed by [`../operators/agent/`](../operators/agent/) runs
its work as one-shot Kubernetes Jobs; each Job pod runs this image. The
runtime fetches its task from the hub, feeds it to pi as a user message,
lets pi run an agent turn (with tool calls), then acks (or nacks) the task
and exits — the Job owns retries.

## Layout

```
pi/
├── agent-shim.js              # implements `agent --list-tools`
├── agent                      # /usr/local/bin/agent — shell dispatch to agent-shim
├── entrypoint.sh              # container ENTRYPOINT: cred bootstrap + exec pi --mode rpc
├── pi-extensions/
│   ├── ainsel-runner/         # one-shot task fetch + sendUserMessage event loop + ack/nack
│   ├── ainsel-mcp/            # registers tools from remote MCP servers as mcp__<server>__<tool>
│   ├── pi-auto-compact/       # preflight context compaction before each prompt (upstream pi-auto-compact)
└── Dockerfile                 # base image
└── Dockerfile.go              # Go toolchain variant
└── Dockerfile.maui            # .NET MAUI toolchain variant
```

## How an event flows

```
                     ┌──────────────────────────────────────────┐
                     │ pi (--mode rpc, one task per pod)        │
                     │                                          │
Job starts with ─────┼─► ainsel-runner extension:               │
AINSEL_TASK_ID       │     • GET task from the hub              │
                     │     • parse envelope                     │
                     │     • set AINSEL_EVENT_* env vars        │
                     │     • pi.sendUserMessage(<event>…)       │
                     │       │                                  │
                     │       ▼                                  │
                     │     pi runs an agent turn                │
                     │       • LLM via provider config          │
                     │       • tool calls                       │
                     │       ▼                                  │
                     │     ctx.waitForIdle()                    │
                     │       │                                  │
                     │       ▼                                  │
                     │     • POST ack (or nack) to the hub      │
                     │     • exit — the Job owns retries        │
                     └──────────────────────────────────────────┘
```

## Local development

```bash
# Syntax check.
node --check agent-shim.js
sh -n entrypoint.sh
sh -n agent
```

See [Image Variants](#image-variants) below for build commands.

## Image Variants

The CI builds variants as **separate image repositories** so agents can
pick exactly the toolchain they need without pulling unrelated SDKs.
Each variant extends the base `ainsel-pi` image, built in the same run.

| Image | Dockerfile | What's added |
| --- | ---------- | ------------ |
| `ghcr.io/<repository-owner>/ainsel-pi:dev` / `:<sha>` | `Dockerfile` | Base image (Node.js + pi + system tools) |
| `ghcr.io/<repository-owner>/ainsel-pi-go:1.24` | `Dockerfile.go` | Go 1.24 + golangci-lint |
| `ghcr.io/<repository-owner>/ainsel-pi-maui:8.0` | `Dockerfile.maui` | .NET 8 SDK + Android SDK + MAUI Android workload |

Variants publish **only** their floating tag. They used to publish a
`1.24-<sha>` / `8.0-<sha>` tag per build too, but nothing ever pulled a variant by
that tag, each one is ~3 GB, and together they were 88 GB of the registry - see
[`CONTRIBUTING.md`](../CONTRIBUTING.md). Pin a variant to a base by digest
instead of by build tag. A variant always builds on the base produced by its own
run (`BASE_TAG=<sha>`), never on whatever `:dev` happens to point at.

### Adding a new variant

1. Create `pi/Dockerfile.<variant>` that does `FROM ainsel/ainsel-pi:<base>`.
2. Install whatever the agent needs (SDKs, compilers, linters).
3. Keep `USER agent` at the end so the runtime stays non-root.
4. Add a corresponding build-push step in the pi CI workflows,
   pushing to a new repository
   (`ghcr.io/<repository-owner>/ainsel-pi-<variant>:<version>`). Publish the floating tag only,
   on main - see the variant note above.
5. Update the table above.

### Building locally

```bash
# Base image.
docker build -t ainsel/ainsel-pi:dev .

# Go variant (must have the base image already pushed to the registry).
docker build --build-arg BASE_TAG=dev -t ainsel/ainsel-pi-go:1.24-dev -f pi/Dockerfile.go pi/

# .NET MAUI variant (must have the base image already pushed to the registry).
docker build --build-arg BASE_TAG=dev -t ainsel/ainsel-pi-maui:8.0-dev -f pi/Dockerfile.maui pi/
```

### Environment variables

Variables consumed by the `ainsel-runner` extension (see
`pi-extensions/ainsel-runner/README.md` for details):

| Var                | Required | Purpose |
| ------------------ | -------- | ------- |
| `HUB_URL`          | yes      | Hub API the runner long-polls for tasks and reports results to |
| `HUB_INTERNAL_VALIDATE_SECRET` | yes | `X-Internal-Token` for the hub's internal task endpoints |
| `AGENT_NAME`       | yes      | Logical agent name, used in the task URLs and propagated into event context for every tool call |
| `AINSEL_LLM_MODEL` | yes    | Model the runner asks for; the operator writes it from `spec.llm.model`. The legacy name `OLLAMA_CLOUD_MODEL` is accepted as a fallback for one release (issue #96); there is no default model |
| `OLLAMA_API_KEY`   | yes      | Read by the pi-ollama-cloud provider (operator mounts from `<agent>-ollama-key` Secret) |
| `AGENT_TOOLS`      | no       | Comma-separated allowlist consumed by the ainsel-tools extension. Unset = all tools available. |
| `AGENT_PERSONA_PATH` | no     | Default `/etc/agent/persona.md`. The entrypoint passes this (and `AGENT.md` in the same directory if present) to pi via `--append-system-prompt`. |

Variables consumed by `entrypoint.sh` (git-credentials bootstrap):

| Var | Source |
| --- | --- |
| `FORGEJO_URL`, `FORGEJO_TOKEN` | single-connector mode env vars |
| `/etc/forgejo-urls/<connector>`, `/etc/forge-credentials/<connector>/token` | multi-connector mode files (operator-mounted) |

## Testing

Syntax-only for now (no test runner in this package):

```bash
node --check agent-shim.js
sh -n entrypoint.sh
sh -n agent
```

The pi-extensions packages under `pi-extensions/` have their own
TypeScript builds and tests.

## List-tools contract

The image keeps the `agent --list-tools` entrypoint that the hub's
`AgentImage` tool-sync Job depends on:

- `command: ["agent", "--list-tools"]` (set by the hub's Job) → kubelet
  exec's `/usr/local/bin/agent`, which exec's
  `node /usr/local/share/ainsel-pi/agent-shim.js --list-tools`.
- `agent-shim.js` lists `/usr/local/bin/tools/`, runs each binary with
  `--schema`, and emits the documented `listToolsManifest` JSON.

The default container start (no command override) goes through
`entrypoint.sh` → `pi --mode rpc` (with extensions) — the event loop.

## Why pi-driven, not a Node wrapper

Pi already provides everything a runner-style Node wrapper would have to
reimplement: long-lived process lifecycle (`--mode rpc`), programmatic
message injection (`pi.sendUserMessage()`), turn-completion await
(`ctx.waitForIdle()`), structured event stream, abort / signal
propagation, and a stable extension ABI. Keeping the runtime inside pi
means its lifecycle primitives stay first-class.

## Reference

- [Architecture overview](../docs/architecture.md)
