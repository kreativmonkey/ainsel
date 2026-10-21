# Contributing to AInsel

Thanks for your interest in contributing. This guide walks through setting up
the repo, the conventions we follow, and how to land a change.

If you only need a quick orientation, the top-level [`README.md`](README.md)
has the architecture diagram and folder map. AI agents working in this repo
should read [`AGENTS.md`](AGENTS.md) instead — it's the terse, pointer-heavy
version of this guide. For an executable setup checklist (toolchain pins,
bootstrap, baseline verification), see
[`.agent/skills/development/project-setup.md`](.agent/skills/development/project-setup.md).
The same directory holds the repo's other playbooks — dependency and license
updates, CRD changes, and the operational runbooks — indexed in
[`.agent/README.md`](.agent/README.md).

## Code of Conduct

Please read [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) before participating.
The short version: be kind, assume good faith, and keep disagreements focused
on code and design rather than people.

## Reporting issues

Open a GitHub issue in the
[ainsel-project/ainsel](https://github.com/ainsel-project/ainsel/issues)
repo. Useful issues include:

- What you expected to happen.
- What actually happened (logs, error messages, screenshots where relevant).
- A minimal reproduction if you have one.

For security-sensitive reports, do not open a public issue — contact the
maintainers directly.

## Development environment

You'll need:

- **Node.js 24+** — pinned in [`.nvmrc`](.nvmrc) and `package.json#engines`.
  The floor is set by the toolchain, not by preference: jsdom 30 needs
  `^22.22.2 || ^24.15.0 || >=26.0.0` (its `undici` dependency calls
  `worker_threads.markAsUncloneable`, which Node 20 does not have) and
  mermaid 12 needs `>=22.12.0`. `frontend/Dockerfile` builds on `node:26`.
- **pnpm 9.15+** — pinned in `package.json#packageManager`.
- **Go 1.26+** — pinned in each module's `go.mod`.
- **Helm 3** — for working with the chart.
- A Kubernetes cluster if you want to deploy locally (kind, k3s, minikube).

Clone and install:

```bash
git clone https://github.com/ainsel-project/ainsel.git
cd ainsel

pnpm install                  # frontend workspace deps
# The go.work root is not a module, so `go build ./...` at the root errors —
# build and test per module instead:
for m in $(go list -m -f '{{.Dir}}'); do (cd "$m" && go build ./...); done
for m in $(go list -m -f '{{.Dir}}'); do (cd "$m" && go test ./...); done
pnpm --filter frontend dev    # frontend dev server (Vite picks the port)
```

The two operators run envtest suites and need control-plane binaries first:
`make setup-envtest` in `operators/agent/` and `operators/event-gateway/`
before their `go test`.

If a build fails on a stale checkout, try `pnpm install --frozen-lockfile`
and `go mod download` from a fresh clone before assuming a real regression.

## Repository layout

This is a monorepo organized by *role*, not by language:

- `frontend/` — operations console (React + Vite + TypeScript).
- `services/` — long-running HTTP services.
  - `services/hub/` — control plane: event routing and REST API.
  - `services/webhook-receiver/` — Generic webhook receiver (pass-through event envelopes).
  - `services/mcp/` — MCP server registry.
  - `services/chat-mcp/` — chat MCP sidecar: exposes chat tools to the agent
    runtime (see [`docs/chat.md`](docs/chat.md)).
- `operators/` — Kubernetes operators.
  - `operators/agent/` — reconciles `Agent` and `Trigger` CRDs.
  - `operators/event-gateway/` — reconciles `WebhookConnector` CRDs.
- `shared/api/` — Go module imported by every Go service for the canonical
  event schema and filter engine.
- `pi/` — pi-native agent runtime.
- `chart/` — single Helm chart that deploys the whole platform.
- `docs/` — architecture, CRD reference, deployment guide, conventions.

Each top-level folder has its own `README.md` with package-specific details.
See [`docs/architecture.md`](docs/architecture.md) for the full data flow.

## Working with the Go workspace

Go modules are joined via [`go.work`](go.work). When you change a file in
`shared/api/`, downstream modules pick it up automatically at build time — no
`replace` directive needed inside the workspace.

Adding a new Go module:

```bash
mkdir services/new-thing
cd services/new-thing
go mod init github.com/ainsel-project/ainsel/services/new-thing
cd ../..
# Add the new path under `use ( … )` in go.work.
go work sync
```

Bumping dependencies: do it per-module (`cd services/hub && go get -u …`),
then run `go work sync` from the root.

## Working with the Helm chart

```bash
helm lint chart/                            # validate templates and values
helm template chart/ -f your-values.yaml    # render manifests locally
```

The chart references image tags via `values.yaml`. Image builds and tag
updates are managed by CI.
Avoid editing the workflows or Dockerfiles without coordinating — see
[Releases](#releases) for how versions and tags are produced.

## Working with hub database migrations

Migrations live in `services/hub/internal/db/migrations/` and are applied by the
hub at startup. They are **append-only**: once a migration has been published,
never renumber, edit or delete it. Give the next file the next number.

This is not a style preference. `schema_migrations` records only the *highest*
applied version, so a database that has already been taken to version N silently
accepts a renumbered history: `Up()` finds nothing above N and applies nothing,
and the migration that should have run never does. Ship the code and every query
touching the missing column returns a 500 — with a pod that started clean.

To make that class of mistake fatal instead of silent, `Migrate` also keeps a
per-version checksum history in `schema_history`: an applied version whose
recorded checksum differs from the file shipping now stops the hub from starting
(`migration history drift`). See
[docs/upgrade-guide.md](docs/upgrade-guide.md#migration-history-integrity) for
how to recover a database that is already in that state.

Adding a migration is therefore the only permitted change to that directory:

```bash
# next number, never a reused one
cp services/hub/internal/db/migrations/0027_example.up.sql \
   services/hub/internal/db/migrations/0028_add_my_column.up.sql
```

A new migration must be safe to run on a database that already holds data — the
dev cluster is upgraded in place, not recreated.

## CI runners

Everything in `.github/workflows/` runs on **GitHub Actions** with the standard
`ubuntu-latest` label, so any runner can pick the jobs up. Deploying to a
cluster is out of scope here: this repo builds and publishes images, and
rolling them out is left to whatever pipeline owns the environment.

| workflow | trigger | does |
| --- | --- | --- |
| `ci-<component>.yml` (10) | PR based on `main`, path-filtered | build, test, lint that component |
| `pr-title.yml` | PR opened, edited or updated | reject a PR title release-please could not parse |
| `dependabot-auto-merge.yml` | PR by `dependabot[bot]`, or dispatch with a PR number | wait for every other check on the PR, then squash-merge it; refuse on any red check |
| `ci-chart.yml` | same, for `chart/**` and `operators/*/config/crd/**` | helm lint, template with default/example/medium/large values, CRD sync check |
| `ci-workflows.yml` | same, for `dev-image-*.yml` and their test | assert the publish decision for every event/branch combination |
| `dev-image-<component>.yml` (8) | push to `main`, path-filtered | build and push images (see tags below) |
| `dev-image-<component>.yml` (8) | PR, or a dispatch without `publish`, path-filtered | **build only** - no login, no push, image discarded |
| `maintenance-untag.yml` | weekly schedule, or dispatch | report (and, on an explicit dispatch, delete) stale Docker Hub tags - see [`docs/maintenance.md`](docs/maintenance.md) |
| `betterleaks.yml` | push and PR on `main` | secret scanning |
| `deploy-docs-pages.yml` | push to `main` on docs paths | publish the docs site |
| `release.yml` | push to `main`, or dispatch | release-please maintains the release PR; when one merges, publish images + chart and verify |

Toolchains come from `actions/setup-go@v7` and `actions/setup-node@v7` after
`actions/checkout@v7`. Docker build jobs run without a `container:` so the
runner's own Docker daemon is available to `docker/build-push-action`; the
chart jobs install Helm with the official `get-helm-3` script.

`dev-image-*` tags are deliberately asymmetric, and the guard lives in each
workflow:

- push to `main` → `:dev` (mutable, what the dev cluster runs) and `:<short-sha>`
- any other branch → nothing: the triggers match `main` only, and the publish
  decision refuses to name `:dev` even if a run somehow starts from another ref

Only the trunk may name `:dev`. The dev cluster runs it, and a build that is
not the integration line used to overwrite it on every push, which took the
dev cluster down by replacing its images with code that lacked migrations
`main` had already applied - see PR #182.

Publishing is a single decision computed in each workflow's `Compute image
metadata` step, never re-derived from `github.event_name` in the build steps. Two
leaks closed by that:

- a PR run is a compile check on the Dockerfile and nothing more - the image is
  loaded into the runner and discarded, so a branch never produces a registry
  tag. There is no image to deploy from a PR: merge to `main` for that.
- `workflow_dispatch` is not restricted to a branch, so a dispatch from any
  feature branch used to publish a `:<short-sha>` tag no review had looked at. It
  now publishes only when the `publish` input is set.

`scripts/test-dev-image-workflows.py` executes each workflow's real step body over
every event/branch combination and fails if either stops being true; it is wired
into `ci-workflows.yml` because a YAML `run:` block is otherwise unchecked until a
build talks to the live registry.

The pi variants additionally move their floating `:1.24` / `:8.0` tags on
`main` only, and always build against the base produced by their own run rather
than whatever `:dev` happens to point at. They publish no per-build tag at all -
see [`pi/README.md`](pi/README.md).

Every tag these workflows write accumulates in the registry, and Docker Hub has no
server-side retention for a personal namespace. [`docs/maintenance.md`](docs/maintenance.md)
is the policy and the collection pass that keeps the account from growing without
bound.

## Commit conventions

We follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>: <description>

[optional body]
```

Types: `feat`, `fix`, `chore`, `docs`, `refactor`, `test`, `perf`, `revert`.

- Lowercase type, lowercase description, no trailing period.
- Imperative mood ("add login page", not "added login page").
- Body is optional, separated by a blank line; explain *why* the change is
  needed, not just *what* changed.

release-please reads these commits to compute the next version and to write
`CHANGELOG.md`, so the format is load-bearing rather than stylistic. Two rules
decide what a reader of the changelog actually sees:

- **A subject release-please cannot parse is dropped** - it appears nowhere in
  the changelog and cannot bump the version. `.github/workflows/pr-title.yml`
  rejects such a PR title, because squash-merge makes the title the subject.
- Of the commits that do parse, only `feat`, `fix`, `perf` and `revert` are
  listed by default. `chore` (where Dependabot's `chore(deps)` bumps land),
  `docs`, `style`, `refactor`, `test`, `build` and `ci` are hidden - unless the
  commit is breaking, in which case it is shown. That keeps dependency noise out
  of the changelog without hiding anything that changes behaviour.

Breaking changes need a marker: either append `!` to the type
(`feat(api)!: drop enabledMCPs`) or add a `BREAKING CHANGE: <what>` footer.
Without one the change still ships - it just won't appear under *BREAKING* in
the changelog, and the version won't bump for it.

The full convention lives in [`docs/conventions.md`](docs/conventions.md).

## Releases

Releases are driven by [release-please](https://github.com/googleapis/release-please)
from conventional commits — no hand-picked versions, no hand-written
changelogs. One version covers the whole product, because
`chart/values.yaml` pins a single image tag per component and the chart cannot
express per-component versions without a redesign.

Three files define it:

| file | role |
| --- | --- |
| `release-please-config.json` | `release-type: simple` at the repo root, plain `vX.Y.Z` tags (`include-component-in-tag: false`), `bump-minor-pre-major` so a breaking change on a `0.x` base bumps minor, and `extra-files` entries that keep `chart/Chart.yaml`'s `version` and `appVersion` in step with the release |
| `.release-please-manifest.json` | the last released version — `0.1.5` until the first release, after which release-please maintains it |
| `CHANGELOG.md` | generated and maintained by release-please |

### How a release happens

1. Merge PRs into `main` with a squash, as usual — see
   [Why PRs squash-merge into `main`](#why-prs-squash-merge-into-main).
2. On every push to `main`, `.github/workflows/release.yml` runs
   release-please. If there are release-worthy commits it opens — or refreshes —
   a release PR titled `chore(main): release <version>`, containing the
   `CHANGELOG.md` diff, the `chart/Chart.yaml` bump and the manifest update.
3. **That PR is the dry run.** The version, the changelog and the chart bump
   are all visible before anything is published, and nothing happens until the
   PR is merged.
4. Merging it cuts the release: release-please creates the `vX.Y.Z` tag and the
   GitHub Release, and the same workflow run publishes the artifacts.

| artifact | where | tags |
| --- | --- | --- |
| 8 images + 2 pi variants | Docker Hub, `dpinsel/ainsel-*` | `:vX.Y.Z`, `:X.Y.Z`, and `:latest` for stable releases |
| Helm chart | GHCR, `oci://ghcr.io/ainsel-project/charts/ainsel` | `X.Y.Z` |
| chart `.tgz` | attached to the GitHub Release | — |

The two pi variants build with `BASE_TAG=<release>` against the pi base built in
the same run, which is what makes a release reproducible rather than pinned to
whatever `:dev` happens to be. A final job pulls every tag back from Docker Hub
and GHCR and checks the Release is published with the chart attached, so a green
run means the artifacts exist.

If a run fails partway, re-run it: the chart job gates `chart/Chart.yaml`
against the release version, image pushes are idempotent per tag, and the
Release upload uses `--clobber`.

### One-time setup: let the workflow open the release PR

release-please needs to **create and approve pull requests**, and a repo can allow
`GITHUB_TOKEN` to write PRs while still forbidding *workflows* from opening them.
That second switch is a repo setting, and the job's own token cannot inspect it,
so CI cannot catch it early — the run just fails with:

```
release-please failed: GitHub Actions is not permitted to create or approve pull requests
```

Fix: **Settings → Actions → General → Workflow permissions → tick "Allow GitHub
Actions to create and approve pull requests" → Save**, then re-run the failed job.
Alternatively create a PAT with write permission on pull requests, store it as the
repository secret `RELEASE_PLEASE_TOKEN`, and `release.yml` will pass it to the
action; that also covers the approval step under branch protection, at the cost of
a long-lived credential to rotate. With the secret unset the workflow keeps using
`GITHUB_TOKEN`.

Until that setting is on the release pipeline produces **nothing at all**: no
`vX.Y.Z` tag, so the `chart` and `images` jobs skip, and no `:latest` image. As of
writing no GitHub release exists — `git ls-remote --tags origin` is empty and the
workflow has had exactly one run, which failed on this setting — so the `latest`,
`0.1.0` … `0.3.0` tags on Docker Hub predate release-please and are not releases of
anything.

### Why PRs squash-merge into `main`

release-please reads the conventional commits **on `main`** to derive the
version and `CHANGELOG.md`. A squash-merged PR is exactly what it wants: one
commit carrying that PR's conventional subject, and `pr-title.yml` already
rejects titles release-please could not parse. What stays forbidden is any
merge onto `main` that collapses several PRs into a single non-conventional
commit — a bulk `release:` promotion commit is the worked example: `release:`
is not a conventional type, so `main` would appear to contain no features and
no fixes at all. The changelog would omit everything the release actually
ships, and release-please would propose no bump.

### Prereleases and `latest`

A version with a suffix (`v0.3.0-rc.1`) is marked as a prerelease on GitHub and
skips the `:latest` tag, so `latest` always means the newest stable release.

### Known rough edge: chart image tags

`chart/values.yaml` still hardcodes each component's image tag (`0.1.0`, and
`0.2.1` for the frontend), which predates this pipeline. So a released chart
installs those older images unless the tags are overridden. Defaulting them to
`.Chart.AppVersion` is the fix, but it has to land together with a release —
before one exists, the documented quickstart would point at tags that are not
published.

### Releasing is not deploying

A dev install keeps running mutable `:dev` images built from `main`, and
rolling those out is whatever deploy pipeline runs in that environment. A
release does not touch it. Pointing an environment at a release means setting
the chart version (or the component `image.tag` values) explicitly.

## Branching

- `main` is the trunk: the default branch, the integration line and the
  release line. Pushes here build `:dev` and `:<short-sha>` images, and
  releases are cut from it by release-please tagging - see
  [Releases](#releases). **Never commit directly to `main`**: it is
  protected, and PR review is the only route in.
- Feature work happens on `type/short-description` branches
  (`feat/agent-scaling`, `fix/webhook-timeout`, `docs/architecture-update`)
  and is merged via PR into `main`.
- Base new branches on the latest `origin/main`:
  `git fetch origin && git checkout -b feat/my-thing origin/main`.
- Always `git pull --rebase` before pushing to catch remote changes.
- Delete branches after merging.

## Pull requests

- **One topic per PR.** Don't bundle unrelated changes — open separate PRs.
- Open against `main`. Squash merge is the norm - it is the only merge type
  the repository allows, so the PR title becomes the commit subject on the
  trunk (see [Why PRs squash-merge into `main`](#why-prs-squash-merge-into-main)).
- The PR description should explain *why*, not just *what*.
- Reference the issue you're addressing if there is one.
- Rebase your branch on `main` regularly:
  `git fetch origin && git rebase origin/main`.
- CI runs only for PRs based on `main`. A PR stacked on another
  feature branch gets no CI, so retarget it if you need the checks.

Before opening a PR, run everything in [Testing & quality](#testing--quality)
locally. CI will catch regressions, but your reviewers shouldn't have to.

## Testing & quality

Run these before pushing:

```bash
# go.work root is not a module — run per module (see .agent/skills/development/project-setup.md)
for m in $(go list -m -f '{{.Dir}}'); do (cd "$m" && go build ./...); done  # all Go modules compile
for m in $(go list -m -f '{{.Dir}}'); do (cd "$m" && go test ./...); done   # all Go tests pass (operators need `make setup-envtest` first)
golangci-lint run             # Go lint clean (run inside each component dir)
pnpm lint                     # frontend lint clean
pnpm test                     # frontend tests pass
pnpm build                    # frontend builds
helm lint chart/              # only if you touched the chart
```

Tests live next to source:

- Go: `*_test.go` files in the same package.
- TypeScript: `*.test.ts` or `*.test.tsx` next to source, or in a `__tests__/`
  directory.

Add or update tests for behavior you change. Especially: edge cases, error
paths, and anything that touches the canonical event schema.

## Code style

- **Go**: `gofmt`, `golangci-lint` with the config at repo root. Keep
  packages small and named for what they do. Tests live next to source.
- **TypeScript**: Prettier + ESLint with the configs at repo root. Prefer
  composition over inheritance; co-locate component, styles, and tests.
- **Markdown**: keep lines under ~100 chars where reasonable; use fenced
  code blocks with language tags; link targets relative to the file.
- **Editor**: an [`.editorconfig`](.editorconfig) at the repo root pins
  line endings, indent style, and trim-trailing-whitespace.

Full conventions in [`docs/conventions.md`](docs/conventions.md).

## Writing a connector

To add support for a new event source (Jira, Slack, Office365, etc.),
see [`docs/writing-a-connector.md`](docs/writing-a-connector.md) — a
full tutorial covering the CRD, service, operator, and chart integration.

## Where to ask for help

- [Issues](https://github.com/ainsel-project/ainsel/issues)
  for bugs and feature requests.
- For architectural questions, start by skimming
  [`docs/architecture.md`](docs/architecture.md).

There is no chat, Discord, Matrix, or discussion forum — GitHub issues
are the only support surface. Open an issue for anything not covered above.
