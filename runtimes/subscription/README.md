# Native subscription runtime

This image runs the official, unmodified Codex CLI and Claude Code binaries.
Exact versions and registry integrity hashes live in `package.json` and
`package-lock.json`. Both official npm packages install the matching native
Linux binary. Node executes the AInsel runner separately; Pi is not involved.
Claude automatic updates are disabled so the image pins remain effective.

Build from the repository root:

```sh
docker build -f runtimes/subscription/Dockerfile -t ainsel-subscription:local .
```

The build context must contain `runtimes/subscription/runner.mjs`.
The development workflow publishes `ghcr.io/<owner>/ainsel-subscription`
with the commit tag and `:dev` on main. Releases publish
`dpinsel/ainsel-subscription` with the version tags. Select this image in the
AgentImage used by the subscription agent; use an immutable tag or digest.

The default task command is:

```sh
node /opt/ainsel/subscription/runner.mjs
```

The image runs as UID/GID 1000, includes git and TLS certificates, and uses
`/workspace` as its working directory. Mount the login credential PVC at
`/home/agent` in both the login pod and task pod. Set the pod's `fsGroup` to
1000 so a fresh PVC is writable. Persist the complete home: provider state can
include files next to `.claude`, as well as the `.codex` and `.claude` folders.
`CODEX_HOME` and `CLAUDE_CONFIG_DIR` already point to those folders.

For a persistent login pod, override its container command with
`["sleep", "infinity"]`; then execute the provider's native login flow:

```sh
kubectl exec -it <login-pod> -- codex login --device-auth
kubectl exec -it <login-pod> -- claude auth login
```

Follow the native terminal instructions. Login state remains on the PVC for
subsequent task pods. Use a separate home PVC for each provider account and
serialize jobs sharing a home. Do not bake credentials into images or commit
them into this directory.

The image includes the repository license manifest and license texts under
`/opt/ainsel/`. Claude Code retains its upstream proprietary license; installing
it does not change AInsel's Apache-2.0 license or grant extra provider rights.

For public GitHub pull request events, the runner validates the webhook's
repository, PR number, and immutable base/head SHAs against GitHub's API and
includes the bounded patch in the prompt. Before starting either native CLI,
it populates the empty `/workspace` with the exact verified PR head and fetches
the base commit for local comparisons. A moved head, failed fetch, or nonempty
workspace fails the task before the CLI starts. Fetches use only the canonical
GitHub HTTPS origin, without inherited Git configuration or credentials;
redirects, hooks, and submodule downloads are disabled. Private repositories
are not supported by this anonymous checkout path.
