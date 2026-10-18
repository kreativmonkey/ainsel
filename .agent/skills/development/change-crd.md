# Skill: Changing a CRD

**When to use:** when you add or change a field on `Agent`, `AgentImage`, or
`WebhookConnector` — or anything else under `shared/api/api/v1alpha1/`.

A CRD change is four artifacts that CI checks against each other: the Go types,
the generated operator base, the chart's copy of that base, and the CRD
reference doc. Change one and forget the rest and you get a cluster whose
schema silently drops your field — the API server prunes unknown fields instead
of erroring, so the field simply never appears.

---

## 1. Edit the Go types

The CRD types live in the shared module, not in the operators:

```
shared/api/api/v1alpha1/agent_types.go          # Agent (+ AgentStatus, scaling)
shared/api/api/v1alpha1/agentimage_types.go     # AgentImage
shared/api/api/v1alpha1/webhookconnector_types.go
```

Kubebuilder markers are the schema's source of truth — use them rather than
hand-writing YAML:

```go
// +kubebuilder:validation:Minimum=0
// +kubebuilder:default=1
// +optional
Replicas *int32 `json:"replicas,omitempty"`
```

Notes that matter here:

- **`+optional` + `omitempty`** on anything without a required value, or every
  existing CR in every cluster starts failing validation on upgrade.
- Defaults are applied by the API server on write — they do **not**
  retroactively fill existing objects, so code must still handle the zero value.
- A new `status` subresource field requires regenerating the shared deepcopy code and, if
  it is printed, a `+kubebuilder:printcolumn` marker.
- `Trigger` and `CronTrigger` are **DB-backed, not CRDs** (see
  [`docs/crd-reference.md`](../../../docs/crd-reference.md)). Changing them
  means hub migrations + API handlers, not CRDs. The stale
  `ainsel.dev_triggers.yaml` / `ainsel.dev_crontriggers.yaml` bases in
  `operators/agent/config/crd/bases/` are leftovers — do not treat them as
  shipped.

## 2. Regenerate the operator bases

```bash
cd operators/agent && make manifests                 # Agent, AgentImage
cd operators/event-gateway && make manifests         # WebhookConnector
cd operators/agent && bin/controller-gen object:headerFile=hack/boilerplate.go.txt paths=../../shared/api/...
```

`make manifests` runs controller-gen against `./...` **and** `../../shared/api/...`
(the types are in the sibling module). `make generate` only scans `./...`, so it
does **not** refresh `shared/api/api/v1alpha1/zz_generated.deepcopy.go`. Run
controller-gen explicitly against the shared module as shown above whenever a
type changes.

Expected: the base YAML under `config/crd/bases/ainsel.dev_*.yaml` gains your
field. If the base *loses* something you did not touch, you regenerated with a
different controller-gen version than `CONTROLLER_TOOLS_VERSION` pins — fix the
version, don't commit the churn.

`make manifests` also regenerates `config/rbac/role.yaml`: if your change adds
an RBAC marker (or the controller now touches a new resource), the role change
belongs in the same commit.

## 3. Copy into the chart

The chart ships CRDs as **templates** (`chart/templates/crds/`) so every
`helm upgrade` re-applies the schema:

```bash
cp operators/agent/config/crd/bases/ainsel.dev_agents.yaml chart/templates/crds/agent.yaml
```

Then re-add the annotation the generator does not know about:

```yaml
metadata:
  annotations:
    controller-gen.kubebuilder.io/version: v0.20.1
    helm.sh/resource-policy: keep        # without it, `helm uninstall` deletes the CRD and every CR
```

`python3 scripts/check-crd-sync.py` (from the repo root) checks exactly these
two things — that each chart copy matches its operator base's `spec`, and that
the `keep` annotation survived the copy. Run it before pushing; it is the same
check `ci-chart.yml` and `release.yml` run.

## 4. Document it

[`docs/crd-reference.md`](../../../docs/crd-reference.md) is the reference
operators read. Add the field with its type, default, and what it does. If the
field is user-facing in the console, the console needs it too
([`frontend/src/pages/`](../../../frontend/src/)) — and the hub's simplified
projection of the CRD (`docs/api-reference.md` → Agents) may need it as well.

## 5. Verify

```bash
python3 scripts/check-crd-sync.py            # chart copy == operator base, annotation intact
cd operators/agent && make setup-envtest && go test ./...    # envtest suites reconcile the new schema
cd operators/agent && make lint              # custom golangci-lint (logcheck etc.)
cd shared/api && go build ./... && go test ./...
helm lint chart/ && helm template chart/ > /dev/null
```

- [ ] `check-crd-sync.py` passes
- [ ] Every consumer of the type builds (`shared/api` is imported by the hub, both operators, and the services)
- [ ] `docs/crd-reference.md` describes the field, including its default
- [ ] Backward compatible: existing CRs validate, and a fresh `helm upgrade` of an old cluster applies

---

## Common pitfalls

| Pitfall | Symptom | Fix |
|---|---|---|
| Edited the chart YAML instead of the Go types | `check-crd-sync.py` fails, and the operator ignores the field | Change the type, regenerate, copy down (§1→§3) |
| Copied the base over the chart file and lost the annotation | `helm uninstall` would delete the CRD and all CRs | Re-add `helm.sh/resource-policy: keep` (the sync check catches it) |
| Regenerated only one operator | event-gateway's base still carries the old `WebhookConnector` schema | `make manifests` in **both** operator dirs |
| Changed types without regenerating shared deepcopy | `go build` fails on a missing `DeepCopyObject` field | Run controller-gen explicitly against `../../shared/api/...` from `operators/agent` |
| Required field added to an existing CRD | Existing CRs fail validation on upgrade | `+optional` with a default, or migrate first |
| New field not in `docs/crd-reference.md` | Users cannot discover it; the next agent re-derives it wrongly | §4 is part of the change |
| Treated `Trigger`/`CronTrigger` as CRDs | You edit manifests nothing reads | They are DB-backed — hub migrations instead |
