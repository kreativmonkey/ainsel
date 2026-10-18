# Upgrade Guide

This guide covers how to upgrade an existing Ainsel installation to a newer chart version.

## General upgrade procedure

Before upgrading, complete these pre-upgrade steps:

1. **Back up the Postgres database.** All persistent state (agents, personas, triggers, connectors, invocation history) lives in Postgres. Create a backup before proceeding.
2. **Read the changelog.** Review [`CHANGELOG.md`](https://github.com/ainsel-project/ainsel/blob/main/CHANGELOG.md) or the notes for the target version on the [releases page](https://github.com/ainsel-project/ainsel/releases). Breaking changes are listed first.
3. **Check for breaking changes.** See [Checking for breaking changes](#checking-for-breaking-changes) below.
4. **Fetch the released chart.** Charts are published to GHCR as OCI artifacts,
   one per release, tagged with the chart version:

   ```bash
   helm pull oci://ghcr.io/ainsel-project/charts/ainsel --version <X.Y.Z>
   ```

5. **Upgrade:**

   ```bash
   helm upgrade ainsel oci://ghcr.io/ainsel-project/charts/ainsel \
     --version <X.Y.Z> -n <namespace> -f values.yaml
   ```

   If you are deploying directly from the repository chart directory:

   ```bash
   helm upgrade ainsel ./chart -n <namespace> -f values.yaml
   ```

## Checking for breaking changes

`CHANGELOG.md` at the root of this repository is generated and maintained by [release-please](https://github.com/googleapis/release-please) from conventional commits, and the same content is published on each GitHub Release. Entries are grouped by type (*BREAKING*, *Features*, *Bug Fixes*, *Miscellaneous Chores*) and linked to their commits and PRs.

To check whether the new chart version introduces new or changed values, diff the default values between versions:

```bash
# Export the new chart's default values
helm show values ainsel/ainsel > new-values.yaml

# Compare against your current values.yaml
diff values.yaml new-values.yaml
```

This reveals keys that are new (you may need to add them) or keys that have been renamed or removed (you must update your `values.yaml` accordingly).

## CRD upgrades

Helm does **not** upgrade CRDs automatically on `helm upgrade`. This is intentional: Helm skips CRD updates during upgrades to prevent accidental data loss caused by destructive schema changes.

If a release introduces new or changed CRDs, apply them manually before or after the Helm upgrade:

```bash
kubectl apply -f chart/templates/crds/
```

Check the changelog for the release to find out whether CRDs have changed and what the recommended apply order is.

## Database migrations

The hub backend runs database migrations automatically at startup. When you upgrade the hub image, any pending migrations are applied when the new pod starts.

If a migration fails, the hub pod will crash-loop. Diagnose with:

```bash
kubectl logs -n <namespace> deployment/ainsel-hub
```

Look for migration error messages near the top of the log output.

**Always back up Postgres before upgrading.** If a migration produces unexpected results, the only safe recovery path is to restore from the backup taken before the upgrade.

## Migrating triggers from CRD storage

If your installation still stores triggers as `Trigger` / `CronTrigger`
custom resources, run the one-time `migrate-triggers` tool after upgrading so
those triggers land in the hub's Postgres database:

```bash
HUB_DB_URL=postgres://user:pass@host:5432/dbname?sslmode=disable \
KUBECONFIG=/path/to/kubeconfig \
go run ./services/hub/cmd/migrate-triggers
```

- `HUB_DB_URL` is required; `HUB_NAMESPACE` (default `ainsel`) selects the
  namespace the CRs are read from.
- The cluster is reached via `KUBECONFIG` or, when run in-cluster, via the
  ServiceAccount's credentials — which then need read access to `Trigger` and
  `CronTrigger` CRDs in that namespace.
- The tool is **idempotent**: triggers whose IDs already exist in the database
  are skipped, so a partial run can simply be re-executed.

The package doc comment in
[`services/hub/cmd/migrate-triggers/main.go`](https://github.com/ainsel-project/ainsel/blob/main/services/hub/cmd/migrate-triggers/main.go)
is the source of truth for usage.

## Rolling back

List available revisions:

```bash
helm history ainsel -n <namespace>
```

Roll back to a previous revision:

```bash
helm rollback ainsel <revision> -n <namespace>
```

> **Important:** Rolling back the Helm release restores the previous Kubernetes resources (Deployments, ConfigMaps, etc.) and the previous image versions. It does **not** reverse any database migrations that have already run. If the migration must be reversed, restore the Postgres database from the backup taken before the upgrade.

## Version-specific notes

### Unreleased — legacy metric aliases removed

`GET /api/v1/metrics/{summary,timeseries,agents}` are gone and answer `404`.
They existed only for the deployed frontend bundle predating the dashboard
rewrite; every shipped frontend since uses the canonical
`/api/v1/observability/metrics/...` paths. Custom dashboards calling the old
paths must switch to the canonical ones.
### Upgrading to v0.2

- **Job-per-task execution model.** Task execution moved to one-shot
  Kubernetes Jobs (one Job per task, single execution model). Agent runtimes
  must talk to the internal task routes
  (`/api/internal/agents/{name}/next-task`, `.../tasks/{id}`,
  `.../tasks/{id}/ack`, `.../tasks/{id}/nack`) — true for every runtime image
  built after that change.
- **Removed: legacy agent task route aliases.** The
  `/api/v1/agents/{name}/next-task`, `.../tasks/{id}/ack`, and
  `.../tasks/{id}/nack` aliases (leftovers of the resident-agent era) are
  removed and now return 404. If a per-task Job pod reports 404 on task
  traffic, it is running a stale runtime image — point the agent's
  `spec.imageRef` at a current `AgentImage` (a runtime image built after #359).

### Upgrading to v0.1

No breaking changes.

### Upgrading to v0.2

**The chart no longer bundles Qdrant.** Agent memory is now a bring-your-own
external service: you run your own [mem0](https://github.com/mem0ai/mem0)
instance (mem0 Cloud or self-hosted, with its own vector store) and point
agents at it via `MEM0_API_URL` — the chart does not deploy it (see
[`docs/adding-memory.md`](adding-memory.md)). On `helm upgrade`:

1. The bundled `qdrant` StatefulSet, its Service, and the `qdrant`
   NetworkPolicy are removed. Any `qdrant:` keys left in your values file
   become inert no-ops and can be deleted.
2. The StatefulSet's PVC is **not** release-owned and is left behind. If you
   believe you need the data in it, export it *before* upgrading; otherwise
   delete the orphaned PVC afterwards:

   ```bash
   kubectl delete pvc data-qdrant-0 -n <namespace>
   ```

3. Agent egress to `qdrant:6333` is no longer granted by the `agent-egress`
   policy. If you run a private Qdrant in-cluster, add your own additive
   egress and ingress policies (see
   [docs/network-policies.md](network-policies.md) for the out-of-band
   pattern).
