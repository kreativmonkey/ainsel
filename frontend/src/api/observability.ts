import { useQuery } from '@tanstack/react-query'
import { request, ServiceUnavailableError } from './client'

// Which backend answered a metrics query. The hub serves its event panels from
// its own Postgres records by default and from Prometheus only when pinned (or
// when there is no database); the two differ in units, Prometheus reporting a
// rate and the hub's records a count per bucket. Panels surface this so a reader
// can tell which they're looking at.
export type MetricsSource = 'prometheus' | 'postgres'

// Shape mirrors what GET /api/v1/observability/metrics/summary returns:
// scalar floats per metric. Backend names: snake_case for the metric registry,
// but JSON tags use camelCase — and the errors counter is `routingErrors`.
export type ObservabilitySummary = {
  eventsConsumed: number
  triggersMatched: number
  eventsRouted: number
  routingErrors: number
  updatedAt: string
  source?: MetricsSource
}

export type TimeseriesPoint = {
  timestamp: string
  value: number
}

export type TimeseriesSeries = {
  name: string
  points: TimeseriesPoint[]
}

// Backend returns a single metric's points flat (no series wrapper); the
// metric registry uses snake_case names.
export type ObservabilityTimeseries = {
  metric: string
  range: string
  step?: string
  points: TimeseriesPoint[]
  source?: MetricsSource
}

export type Range = '1h' | '6h' | '24h' | '7d'

// The reason a panel has no data, in the hub's own words.
//
// The hub's 503 bodies name the specific dependency that is missing — the
// metrics backend, Prometheus, or the log store — because the panels need
// different things and a bare status code cannot say which. Showing that text
// instead of a fixed string keeps "prometheus not configured" from reading as
// "telemetry not configured" on a panel that never asked for Prometheus.
export function unavailableDetail(error: unknown): string | undefined {
  if (error instanceof ServiceUnavailableError) return error.message
  return undefined
}

// Human label for which backend answered a token panel. The two differ in
// coverage — a scale-to-zero agent has conversation rows even when it was
// never scraped, and rows written before migration 0024 carry no subject — so
// a reader needs to know which one produced the table (issue #411).
export function metricsSourceLabel(source?: MetricsSource): string | undefined {
  if (!source) return undefined
  return source === 'postgres' ? 'hub records' : 'prometheus'
}

// Human width of a bucket, from the `step` the hub echoes back (a Go duration:
// "30s", "10m0s", "1h0m0s"). The chart labels need it because the two metric
// backends measure different things per point: Prometheus returns a per-second
// rate, the hub's records return a count per bucket. Labelling the second as
// "events / hour" would be a unit lie, so the label is built from the step.
export function formatStep(step?: string): string | undefined {
  if (!step) return undefined
  const parts = /^(?:(\d+)h)?(?:(\d+)m)?(?:([\d.]+)s)?$/.exec(step)
  if (!parts) return undefined
  const [, h, m, s] = parts
  const out: string[] = []
  if (h && h !== '0') out.push(`${h}h`)
  if (m && m !== '0') out.push(`${m}m`)
  if (s && s !== '0') out.push(`${s}s`)
  return out.length ? out.join('') : undefined
}

// chartUnit names what one bar of a throughput chart holds. The two metric
// backends answer in different units: Prometheus returns a per-second rate
// (sum(rate(...[window]))), the hub's own records return a count per bucket.
// Only the count can be labelled from `step`, and a rate cannot be labelled
// "events / hour" without a 3600x lie, so every panel asks this one function
// rather than keeping its own ternary.
export function chartUnit(source?: MetricsSource, step?: string): string {
  if (source === 'postgres') return `events / ${formatStep(step) ?? 'bucket'}`
  return 'events / period'
}

export type MetricName =
  | 'events_consumed'
  | 'triggers_matched'
  | 'events_routed'
  | 'routing_errors'

export type TimeseriesParams = {
  range: Range
  metric: MetricName
}

export type TokensSummary = {
  totalTokens: number
  inputTokens: number
  outputTokens: number
  // Pi reports prompt-cache traffic separately (usage.input excludes it), so
  // totalTokens includes these; a hub older than the cache split omits them.
  cacheReadTokens?: number
  cacheWriteTokens?: number
  // Priced consumption (pi's usage.cost, priced via spec.llm.cost). Only
  // meaningful when costReported is true (issue #75).
  costUsd?: number
  // Presence flags (issue #75): false means the window never carried a
  // reported cache/cost figure, so the corresponding numerics are
  // placeholders — render them as "not reported", never as 0 / 0% / $0.00.
  // A reported zero keeps its flag true, so a genuine 0% still renders as 0%.
  // Missing on hubs older than the flags: treat as unknown and keep the
  // legacy rendering rather than claiming "not reported".
  cacheReported?: boolean
  costReported?: boolean
}

export type TokensSubjectRow = {
  agent: string
  agentName?: string
  repo?: string
  eventType?: string
  model?: string
  inputTokens: number
  outputTokens: number
  cacheReadTokens?: number
  cacheWriteTokens?: number
  totalTokens: number
  costUsd?: number
  // Per-row presence flags, same rule as TokensSummary's (issue #75).
  cacheReported?: boolean
  costReported?: boolean
}

export type TokensBySubject = {
  range: string
  rows: TokensSubjectRow[]
  // Which backend answered: the hub's conversation rows (default) or the
  // Prometheus counters (pinned, or no database). The two differ in
  // coverage — a scale-to-zero agent has rows even when it was never
  // scraped — so a reader needs to know which one produced the table.
  source?: MetricsSource
}

export type TokenEventRow = {
  event: string
  inputTokens: number
  outputTokens: number
  cacheReadTokens?: number
  cacheWriteTokens?: number
  totalTokens: number
  costUsd?: number
  // Per-row presence flags, same rule as TokensSummary's (issue #75).
  cacheReported?: boolean
  costReported?: boolean
}

export type TokensByEvent = {
  range: string
  rows: TokenEventRow[]
}

export type LogLevel = 'debug' | 'info' | 'warn' | 'error'

export type LogEntry = {
  timestamp: string
  level?: LogLevel
  message: string
  app?: string
  agent?: string
  agentName?: string
  labels?: Record<string, string>
}

export type LogsParams = {
  app?: string
  agent?: string
  range?: Range
  limit?: number
}

export function getObservabilitySummary(range?: Range) {
  const query = range ? { range } : undefined
  return request<ObservabilitySummary>('/observability/metrics/summary', { query })
}

export function useObservabilitySummary(range?: Range) {
  return useQuery({
    queryKey: ['observability', 'summary', range ?? 'default'],
    queryFn: () => getObservabilitySummary(range),
  })
}

export function getObservabilityTimeseries(params: TimeseriesParams) {
  return request<ObservabilityTimeseries>('/observability/metrics/timeseries', {
    query: params,
  })
}

export function useObservabilityTimeseries(params: TimeseriesParams) {
  return useQuery({
    queryKey: ['observability', 'timeseries', params],
    queryFn: () => getObservabilityTimeseries(params),
  })
}

export function getTokensSummary(range?: Range) {
  const query = range ? { range } : undefined
  return request<TokensSummary>('/observability/metrics/tokens/summary', { query })
}

export function useTokensSummary(range?: Range) {
  return useQuery({
    queryKey: ['observability', 'tokens', 'summary', range ?? 'default'],
    queryFn: () => getTokensSummary(range),
  })
}

// The hub's cumulative token ledger — GET /api/v1/tokens with no since/until.
// This is the same endpoint the MCP get_token_usage tool reads, and the only
// one that answers "how much has been consumed in total" on both backends:
// the records path sums every retained conversation row, the PromQL path
// reads the cumulative counters.
export type TokensTotals = {
  inputTokens: number
  outputTokens: number
  cacheReadTokens?: number
  cacheWriteTokens?: number
  totalTokens: number
  costUsd?: number
  // Window-level presence flags, same rule as TokensSummary's (issue #75).
  cacheReported?: boolean
  costReported?: boolean
}

// One ledger row — the hub's TokenEntry struct
// (services/hub/internal/api/handlers_tokens.go): token consumption for one
// agent + repo + issue + model combination, from either backend. The cache
// and cost fields are optional for the same reason as in TokensSummary: a
// hub older than the cache split omits them.
export type TokensLedgerEntry = {
  agent: string
  repository: string
  issueNumber: string
  model: string
  inputTokens: number
  outputTokens: number
  cacheReadTokens?: number
  cacheWriteTokens?: number
  totalTokens: number
  costUsd?: number
  // Per-row presence flags, same rule as TokensSummary's (issue #75).
  cacheReported?: boolean
  costReported?: boolean
}

type TokensLedgerEnvelope = {
  tokens: TokensLedgerEntry[]
  total: TokensTotals
  source?: MetricsSource
}

// getTokensLedger is the all-time source for the Tokens page's
// "Total Tokens (all time)" tile. /tokens/summary is range-bound — without a
// range it answers the server default of 24h — which is exactly the figure
// that tile used to show under its all-time label.
export function getTokensLedger() {
  return request<TokensLedgerEnvelope>('/tokens')
}

export function useTokensLedger() {
  return useQuery({
    queryKey: ['observability', 'tokens', 'ledger'],
    queryFn: getTokensLedger,
  })
}

export function getTokensBySubject(range: Range) {
  return request<TokensBySubject>('/observability/metrics/tokens/by-subject', {
    query: { range },
  })
}

export function useTokensBySubject(range: Range) {
  return useQuery({
    queryKey: ['observability', 'tokens', 'by-subject', range],
    queryFn: () => getTokensBySubject(range),
  })
}

export function getTokensByEvent(range: Range) {
  return request<TokensByEvent>('/observability/metrics/tokens/by-event', {
    query: { range },
  })
}

export function useTokensByEvent(range: Range) {
  return useQuery({
    queryKey: ['observability', 'tokens', 'by-event', range],
    queryFn: () => getTokensByEvent(range),
  })
}

// Backend wraps the list in `{ logs, total, query }`; unwrap so consumers can
// keep treating the result as an array. The raw element shape from the backend
// is `{ timestamp, message, agentName?, labels? }` — `level`, `app`, and
// `agent` live inside the Loki stream `labels` map, not as top-level fields.
// We normalize here so every consumer sees consistent top-level fields.
type LogsEnvelope = {
  logs: LogEntry[]
  total: number
  query: string
}

// normalizeLogEntry copies `labels.agent`, `labels.app`, and `labels.level`
// into top-level fields when the direct fields are absent. This lets UI
// components read `row.agent`, `row.app`, `row.level` without worrying about
// the labels fallback.
function normalizeLogEntry(entry: LogEntry): LogEntry {
  const labels = entry.labels
  if (!labels) return entry
  return {
    ...entry,
    agent: entry.agent ?? labels.agent,
    app: entry.app ?? labels.app,
    level: entry.level ?? (labels.level as LogLevel | undefined),
  }
}

export async function getObservabilityLogs(params: LogsParams = {}): Promise<LogEntry[]> {
  const env = await request<LogsEnvelope>('/observability/logs', { query: params })
  return (env.logs ?? []).map(normalizeLogEntry)
}

export function useObservabilityLogs(
  params: LogsParams,
  opts: { enabled?: boolean; refetchInterval?: number | false } = {},
) {
  return useQuery({
    queryKey: ['observability', 'logs', params],
    queryFn: () => getObservabilityLogs(params),
    refetchInterval: opts.refetchInterval ?? false,
    enabled: opts.enabled ?? true,
  })
}