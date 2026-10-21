# Observability

This document describes how to observe a running ainsel platform: where logs come from, which metrics are exported, and which parts of the console need an external metrics backend.

## Logs

All components write **structured JSON logs** using Go's `log/slog` package. Every log line is a JSON object written to stdout, which Kubernetes collects and forwards to your log aggregation backend.

### Log levels

| Level | When used |
|-------|-----------|
| `info` | Normal operation — startup, shutdown, successful events |
| `warn` | Recoverable issues — retries, degraded state |
| `error` | Failures that require attention |

### Querying logs via the hub API

Agent task logs are stored in the hub's own database, so the console's log panels
need no external log backend:

```
GET /api/v1/observability/logs?app=<agent>&range=<1h|6h|24h>&limit=<n>
```

Despite the `app` parameter name, the value is an **agent name** — the filter is
`task_logs.agent_name`, and the console's LogQL-shaped `app=` label is a
compatibility holdover from when this endpoint proxied Loki.

The entries are the structured log lines agents publish while a task runs. Each
one carries its agent, level, message, and the invocation and correlation ids it
belongs to, which is what links a log line to an event (see
[Event detail & conversation transcript](#event-detail--conversation-transcript)).

The hub's own process log — startup, routing decisions, `activity_event` lines —
goes to stdout as JSON and is yours to collect with whatever the cluster already
runs (`kubectl logs deploy/hub-backend`, or a log pipeline). It is not served by
this endpoint.

## Event detail & conversation transcript

Activity events (see [`GET /api/v1/events`](api-reference.md)) are the entry point for tracing what happened for a given event. From the **Activity** page or **Observability → Events**, every event row shows an always-visible `open →` link (and a **View full event** link when the row is expanded). Either opens the event detail view at `/observability/events/<id>`.

![The Activity stream listing 16,921 events, with filters for free-text search,
status, outcome, connector and agent above a table of when, connector, trigger,
agent and status — each row resolving to either MATCH, where the event routed
to an agent, or SKIP, where no trigger matched](images/activity-stream.webp)

*Every event the platform has seen, and the routing decision made for it.
`SKIP` rows are as useful as `MATCH` ones: they are how you find a trigger
filter that is quietly too narrow.*

The event detail view lists each invocation matched to that event with its agent, trigger, status, duration, and total token usage. Below that it renders the full agent conversation transcript for the invocation: the user prompt, assistant thinking and text, tool calls, and tool results. These messages are served by [`GET /api/v1/observability/conversations`](api-reference.md).

![The event detail view for a matched pull_request.opened event: event id,
connector and MATCH status across the top, the agents whose triggers matched,
the channel journey from connector to agent delivery, the invocation table
with agent, trigger, status, duration and token total, and beneath it the
recorded conversation opening with the event envelope rendered verbatim as
the agent's prompt](images/event-detail.webp)

*One event, end to end: routing decision, channel journey, invocation
outcome, and the exact prompt the agent received. (Source-identifying
fields redacted.)*

Transcripts are populated by the agent runtime, which reports its messages back to the hub when a task completes. If an invocation has no reported messages, the event detail view says so explicitly rather than rendering an empty transcript.

### Queued, running, and unknown

Every view — the Activity table, the channel views, the routing table, and the
event detail journey — renders the same derived run state for a run, computed by
the hub from the task row, the first evidence of work (a claim or the first
conversation row), and the invocation record. **Queued** means a task exists and
no worker has picked it up yet; **running** means something is actively working
on it (in per-task Job mode the queue row stays `pending` while the pod works,
which is why the raw task status alone is not enough); **unknown** means nothing
backs the row — a pruned record or a task that was never enqueued — and is never
rendered as queued or running. A task still queued past the hub's start deadline
(`TASK_START_TIMEOUT_SECONDS`) is flagged *stuck*. The Activity page's Outcome
filter accepts all five states (`queued`, `running`, `success`, `failure`,
`timeout`) and filters over the whole history, not just the loaded page.

## Metrics

Metrics are exposed by the hub on a dedicated metrics port (default `9090`) at
`/metrics` — that export is for Prometheus, Grafana and the alert rules, and it is
unaffected by which backend the console reads. The hub's own API serves the console
at:

```
GET /api/v1/observability/metrics/summary
```

### Hub metrics

The following counters are exported by the hub. No other ainsel components export custom metrics today (see [Operator metrics](#operator-metrics) for controller-runtime defaults, and the note below for future work).

| Metric | Type | Description |
|--------|------|-------------|
| `hub_events_consumed_total` | counter | Events fetched by the router from the `events` table |
| `hub_triggers_matched_total` | counter | Events that matched at least one trigger rule |
| `hub_events_routed_total` | counter | Events successfully dispatched to an agent |
| `hub_routing_errors_total` | counter | Events that failed to route to an agent |

> **Note:** The webhook-receiver, MCP service, and agent pods do not export custom metrics today. Adding per-component metrics is follow-up work.

### Which backend answers

The console labels the metric-backed panels **telemetry**. Those panels read
`/api/v1/observability/metrics/*`, which the hub answers from one of two backends:

| Panel | Needs |
|-------|-------|
| KPI cards (events consumed, triggers matched, events routed, errors) and the throughput charts | The hub's own database, by default. Prometheus answers them only if you pin it, or if the hub has no database |
| Token tiles (summary and timeseries) | The hub's own database, by default — conversation rows carry every token component pi reports, cache included. Prometheus answers them only if you pin it, or if the hub has no database |
| Per-agent metrics (`/api/v1/observability/metrics/agents`: per-agent tokens, invocations, failures, mean duration) | The hub's own database, by default — tokens from conversation rows, invocations/failures/durations from the invocation records the hub writes for every dispatch. Prometheus answers only if you pin it, or if the hub has no database |
| Token table by subject (per repo and event type) | The hub's own database, by default — since #411 the rows are grouped by the subject columns migration 0024 added to the conversation rows. Prometheus answers only if you pin it, or if the hub has no database |
| Raw PromQL (`/api/v1/observability/metrics/query`, MCP `query_metrics`) | Prometheus |

So the throughput charts, event KPIs, the per-agent table and the token table by
subject all work on a default install with no Prometheus at all, and they work the
same way on an install that has one. When *nothing* can answer — no database, and
either no Prometheus or a Prometheus pinned but not configured — a panel says
**No metrics source configured** and shows the hub's reason underneath. Only raw
PromQL still names Prometheus specifically: nothing else in the console asks it
anything.

The event panels read the hub's records rather than its counters because the hub
wrote those records. `hub_events_consumed_total` and a row in `events` are two views
of one routing decision, and the row is the better witness of the two:

- **A counter restarts at zero with its process.** `sum(hub_events_consumed_total)`
  reads the newest sample, so the KPI cards drop to the new pod's uptime on every
  hub roll. `events` and `agent_tasks` are not pruned at all, so they keep counting.
- **A scrape is a sample, not a record.** Harmless for the hub, which is a long-lived
  Service a ServiceMonitor watches. It is not harmless for agents: the runtime
  publishes its token and invocation counters from inside the pod, and a run shorter
  than the 30 s scrape interval is a run that was never sampled. That is the shape of
  most ainsel agents — one-shot task pods that finish (or scale to zero) between
  scrapes — which is why the per-agent table reads the hub's `task_conversations`
  and `invocations` rows rather than `agent_tokens_used_total` and
  `agent_invocations_total`.

Pin the counters with `observability.metricsSource: prometheus` (`HUB_METRICS_SOURCE`)
if you want them anyway. The one case that argues for it: `task_logs` is pruned after
7 days, so a `7d` error count read from the records covers only the errors still
retained, while a counter that predates the pruning remembers all of them.

`observability.prometheus.url` is what lights up raw PromQL, and nothing else,
unless you also pin the source. The token tiles, the token table by subject and
the per-agent table answer from the hub's own rows whether or not Prometheus
exists. Note the retention cliff on the records path: `task_conversations` and
`invocations` are pruned after 48 hours, so a range wider than that (or no range)
reports what is still retained, not lifetime — pinning Prometheus is the way to
read counters that predate the pruning. The hub reads both settings once at
startup, so configure them and let `helm upgrade` roll the pods.

The two backends report in different units, and every metrics response carries a
`source` field (`"prometheus"` or `"postgres"`) so a reader can tell which it is:
a Prometheus point is a per-second rate of a counter, a Postgres point is a count
of rows inside that bucket. The console derives its axis label from `source` and
`step` accordingly. Postgres also sees only rows that are still retained, so a
window older than the retention reports zero rather than the history a counter
would still remember.

Cache and cost figures carry a second distinction, marked by `cacheReported` /
`costReported` flags on the token responses: *not reported* is not the same fact
as *measured zero*. A provider without prompt caching reports no cache figure at
all, and a model without a `spec.llm.cost` block reports no cost — the runtime
then omits the fields (the hub stores NULL, and raises no `agent_cost_usd_total`
series), and the console renders those tiles and cells as "not reported" instead
of a real-looking `0%` hit rate or `$0.00`. A reported zero — a provider that
genuinely reports `cached_tokens: 0`, a priced model on a free run — keeps its
flag true and renders as the measured zero it is. Rows written before migration
0025 stored ambiguous zeros; the migration backfills them to NULL, so those rows
read as not reported too.

To have Prometheus scrape the hub's own `/metrics` endpoint, enable the ServiceMonitor or PodMonitor resources in `values.yaml`. This is independent of `metricsSource`: the hub exports these counters whether or not it also reads them back.

```yaml
observability:
  prometheus:
    url: "http://prometheus.monitoring.svc.cluster.local:9090"
  serviceMonitor:
    enabled: true
    labels:
      release: prometheus   # match your Prometheus Operator selector
    interval: "30s"
  podMonitor:
    enabled: false
```

## Required vs optional backends

The hub's **PostgreSQL database is required** — it is the event queue and the
backend the console's event metrics read by default, and the hub refuses to start
without `HUB_DB_URL`. Prometheus is **optional**: the platform and its console work
without it, and only the panels listed above are affected.

| Backend | Effect when absent |
|---------|--------------------|
| **Prometheus** | Raw PromQL (`/api/v1/observability/metrics/query`, MCP `query_metrics`) returns `503` naming Prometheus as the missing backend. Every console panel — token tiles, the token table by subject (since #411), event KPIs, throughput charts and the per-agent metrics table — keeps working, served from the hub's own records. Everything else is unaffected. |
| **No Prometheus *and* no database** | Every metrics panel returns `503` with **No metrics source configured**. This is not a supported configuration: the hub will not start without a database. |

The platform health endpoint reports the status of every pod in the hub's namespace:

```
GET /api/v1/platform/health
```

It returns an array of pod summaries (`name`, `phase`, `ready`, `restarts`, per-container
state) — useful for spotting a crash-looping component, but it does **not** probe
Prometheus or the log backend, so it cannot tell you whether telemetry is configured.
For that, check the env var and the hub's startup log as described in
[Troubleshooting](troubleshooting).

## Operator metrics

Both the agent operator and the connector operator are built on [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime), which automatically exports the following metrics on port `8080` of each operator pod (path: `/metrics`):

- Reconcile duration histogram (`controller_runtime_reconcile_time_seconds`)
- Work queue depth gauge (`workqueue_depth`)
- Reconcile error counter (`controller_runtime_reconcile_errors_total`)

To scrape these, create a `ServiceMonitor` (or `PodMonitor`) pointing at the operator's metrics port. The Helm chart does not currently ship ServiceMonitor resources for the operators — this is planned as future work.

## Tracing

Distributed tracing is **not implemented**. Trace context propagation across hub, operators, and agent invocations is future work.
