package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/ainsel-project/ainsel/services/hub/internal/invocations"
	"github.com/ainsel-project/ainsel/services/hub/internal/prometheus"
	"github.com/ainsel-project/ainsel/services/hub/internal/tasklogs"
)

// Agents publish token usage as agent_tokens_used_total{agent,repo,org,event_type,token_type,model}.
// The handlers in this file expose those metrics to the observability dashboard.
const (
	metricTokensUsed = "agent_tokens_used_total"

	// metricCostUsed is the USD counterpart of metricTokensUsed, raised by the
	// runtime from pi's usage.cost.total (priced via the model's cost block in
	// the generated models.json, spec.llm.cost). Presence is meaningful (issue
	// #75): an agent whose model carries no cost block raises nothing, while a
	// priced model that costs nothing raises 0 — so a series that exists with
	// value 0 is a measured free run, and no series means "not reported".
	metricCostUsed = "agent_cost_usd_total"

	// defaultSummaryRange is the range used when the caller does not supply one.
	// Kept at 24h for backward compatibility with the original "Tokens last 24h" tile.
	defaultSummaryRange = "24h"

	// summaryWindow is the duration form of defaultSummaryRange, used by the
	// tokens timeseries endpoint which still renders a fixed 24h sparkline.
	summaryWindow = 24 * time.Hour

	// timeseriesStep determines how many sparkline points the 24h tile renders.
	// 30 minutes → ~48 points across 24h. Matches the spec.
	timeseriesStep = 30 * time.Minute
)

// Values of the token_type label the agent runtime publishes. Pi reports
// prompt-cache traffic separately from billable input: usage.input excludes
// cacheRead/cacheWrite, and usage.totalTokens is the sum of all four. An agent
// loop with prompt caching enabled sends the large majority of its prompt as
// cache reads, so counting only input + output under-reports consumption by
// roughly an order of magnitude.
const (
	tokenTypeInput      = "input"
	tokenTypeOutput     = "output"
	tokenTypeCacheRead  = "cache_read"
	tokenTypeCacheWrite = "cache_write"
)

// tokenTotal sums every token component the runtime publishes. This is the one
// place the accounting rule lives — callers must not re-derive it, because
// omitting the cache components is exactly the bug this guards against.
func tokenTotal(input, output, cacheRead, cacheWrite float64) float64 {
	return input + output + cacheRead + cacheWrite
}

// tokenTotalsByType returns per-token_type totals for a window in a single
// grouped query. A component the runtime never published is absent from the
// map; a component it published with value 0 is present with value 0. Callers
// use that distinction (issue #75): presence of a cache_read/cache_write key
// means the runtime reports cache figures — a real 0% hit rate — while its
// absence means "not reported" and must not render as a measured zero.
func tokenTotalsByType(ctx context.Context, p *prometheus.Client, rangeKey string) (map[string]float64, error) {
	result, err := p.Query(ctx,
		fmt.Sprintf(`sum by (token_type) (increase(%s[%s]))`, metricTokensUsed, rangeKey))
	if err != nil {
		return nil, err
	}
	totals := make(map[string]float64, len(result.Data))
	for _, m := range result.Data {
		totals[m.Labels["token_type"]] += m.Value
	}
	return totals, nil
}

// TokensSummary powers the "Tokens last 24h" tile.
//
// Source names the backend the figures came from: prometheus, or postgres when
// the hub answered from its own conversation rows.
//
// CacheReported/CostReported say whether the window held any *reported*
// cache/cost figure (issue #75). When false, the corresponding numerics are
// sums over rows that never carried the figure — readers must render them as
// "not reported", never as a measured 0 / 0% / $0.00. A reported zero counts
// as reported, so a provider that genuinely reports cached_tokens: 0 keeps
// its real 0%.
type TokensSummary struct {
	Source              string  `json:"source"`
	InputTokens         float64 `json:"inputTokens"`
	OutputTokens        float64 `json:"outputTokens"`
	CacheReadTokens     float64 `json:"cacheReadTokens"`
	CacheWriteTokens    float64 `json:"cacheWriteTokens"`
	TotalTokens         float64 `json:"totalTokens"`
	PreviousTotalTokens float64 `json:"previousTotalTokens"`
	// CostUSD is the priced consumption over the window, summed from pi's
	// per-message usage.cost. It is only meaningful when CostReported is
	// true: an unpriced model reports nothing (issue #75), which is not the
	// same fact as a run that cost nothing.
	CostUSD       float64   `json:"costUsd"`
	CacheReported bool      `json:"cacheReported"`
	CostReported  bool      `json:"costReported"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// TokensTimeseries powers the sparkline embedded in the tile.
type TokensTimeseries struct {
	Source string            `json:"source"`
	Range  string            `json:"range"`
	Step   string            `json:"step"`
	Points []TimeseriesPoint `json:"points"`
}

// TokenSubjectRow is one row in the Agent activity table — a unique
// (agent, repo, eventType, model) tuple with its tokens.
//
// CacheReported/CostReported are per-row presence flags (issue #75): false
// means no conversation in that group ever carried the figure, so the summed
// cache/cost numerics are placeholders and readers must render them as "not
// reported" rather than 0 / $0.00.
type TokenSubjectRow struct {
	Agent            string  `json:"agent"`
	AgentName        string  `json:"agentName"`
	Repo             string  `json:"repo"`
	EventType        string  `json:"eventType"`
	Model            string  `json:"model"`
	InputTokens      float64 `json:"inputTokens"`
	OutputTokens     float64 `json:"outputTokens"`
	CacheReadTokens  float64 `json:"cacheReadTokens"`
	CacheWriteTokens float64 `json:"cacheWriteTokens"`
	TotalTokens      float64 `json:"totalTokens"`
	CostUSD          float64 `json:"costUsd"`
	CacheReported    bool    `json:"cacheReported"`
	CostReported     bool    `json:"costReported"`
}

// TokensBySubject is the response for the /metrics/tokens/by-subject endpoint.
//
// Source names the backend the figures came from: postgres when the hub
// answered from its own conversation rows, prometheus when it read the agent
// token counters. The two differ in coverage, not units: a scale-to-zero agent
// invisible to a scrape still has conversation rows, and rows written before
// migration 0024 carry no subject on the records path (they group under "").
type TokensBySubject struct {
	Range     string            `json:"range"`
	Rows      []TokenSubjectRow `json:"rows"`
	UpdatedAt time.Time         `json:"updatedAt"`
	Source    string            `json:"source"`
}

// tokensBackend chooses the source that answers a token-metric query, on the
// same rule metricsBackend applies to the event metrics: the hub's own records
// win by default, Prometheus answers when it is pinned or the records are not
// there.
//
// The records win for tokens for a sharper reason than they do for the event
// counters. The agent token series is scraped from agent pods that routinely
// finish before the first scrape — a scale-to-zero agent's consumption can be
// invisible to Prometheus forever, while its conversation rows are written the
// moment a turn ends. Since migration 0022 the conversations table carries the
// cache components too, so the records are now a complete ledger: input,
// output, cache_read and cache_write, the same four the Prometheus counter
// publishes as token_type series — and since migration 0024 they carry the
// subject labels (repo, event type) the by-subject table groups by, so every
// token panel this function routes to is served by the same rule.
func (s *Server) tokensBackend() (source, reason string) {
	if s.metricsSource == MetricsSourcePrometheus {
		if s.prom != nil {
			return metricsSourcePrometheus, ""
		}
		return "", promRequiredMessage
	}
	switch {
	case s.taskLogs.Ready():
		return metricsSourcePostgres, ""
	case s.prom != nil:
		return metricsSourcePrometheus, ""
	default:
		return "", metricsRequiredMessage
	}
}

// getTokensSummary aggregates token totals over a configurable window.
// Accepts an optional ?range= query parameter (1h|6h|24h|7d); defaults to 24h
// when omitted for backward compatibility.
func (s *Server) getTokensSummary(w http.ResponseWriter, r *http.Request) {
	rangeKey := r.URL.Query().Get("range")
	if rangeKey == "" {
		rangeKey = defaultSummaryRange
	}
	if _, ok := supportedRanges[rangeKey]; !ok {
		writeError(w, http.StatusBadRequest, "unsupported range; allowed values: 1h, 6h, 24h, 7d")
		return
	}

	source, reason := s.tokensBackend()
	if source == "" {
		writeError(w, http.StatusServiceUnavailable, reason)
		return
	}

	// Keyed by source, as the event summary does: a hub that gains (or loses)
	// a backend cannot serve the other backend's figures under this one's name
	// for the length of the TTL.
	cacheKey := "tokens-summary:" + source + ":" + rangeKey
	if cached, ok := s.observabilityCache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, cached)
		return
	}

	ctx := r.Context()

	// Records path: sum the four components straight from the conversation
	// rows. The window is [now-range, now) and the prior one the range before
	// it — the same shape the increase(...)/offset queries give on Prometheus.
	if source == metricsSourcePostgres {
		rng := supportedRanges[rangeKey]
		now := time.Now().UTC()
		cur, err := s.taskLogs.TokenUsageSince(ctx, now.Add(-rng.Duration), now)
		if err != nil {
			writeError(w, http.StatusBadGateway, "failed to query token records: "+err.Error())
			return
		}
		prior, err := s.taskLogs.TokenUsageSince(ctx, now.Add(-2*rng.Duration), now.Add(-rng.Duration))
		if err != nil {
			writeError(w, http.StatusBadGateway, "failed to query token records: "+err.Error())
			return
		}
		resp := TokensSummary{
			Source:              source,
			InputTokens:         float64(cur.InputTokens),
			OutputTokens:        float64(cur.OutputTokens),
			CacheReadTokens:     float64(cur.CacheReadTokens),
			CacheWriteTokens:    float64(cur.CacheWriteTokens),
			TotalTokens:         cur.Total(),
			PreviousTotalTokens: prior.Total(),
			CostUSD:             cur.CostUSD,
			CacheReported:       cur.CacheReported,
			CostReported:        cur.CostReported,
			UpdatedAt:           time.Now().UTC(),
		}
		s.observabilityCache.set(cacheKey, resp)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	totals, err := tokenTotalsByType(ctx, s.prom, rangeKey)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to query token metrics: "+err.Error())
		return
	}
	// The prior window is compared as a single total, so it needs no breakdown.
	prior, err := singleScalar(ctx, s.prom,
		fmt.Sprintf(`sum(increase(%s[%s] offset %s))`, metricTokensUsed, rangeKey, rangeKey))
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to query token metrics: "+err.Error())
		return
	}

	input := totals[tokenTypeInput]
	output := totals[tokenTypeOutput]
	cacheRead := totals[tokenTypeCacheRead]
	cacheWrite := totals[tokenTypeCacheWrite]
	// Presence of the series is the report (issue #75): a cache_read or
	// cache_write series that exists — even with value 0 — means the runtime
	// reports cache figures, so the zeros are measured; absence means the
	// upstream is silent and the tile must say "not reported".
	_, cacheReadSeries := totals[tokenTypeCacheRead]
	_, cacheWriteSeries := totals[tokenTypeCacheWrite]
	cacheReported := cacheReadSeries || cacheWriteSeries

	// Cost has no token_type breakdown: the runtime raises one USD total per
	// message. A hub whose agents run unpriced models simply has no series —
	// that is "not reported", not a measured $0 (issue #75), so the query
	// reports vector emptiness instead of coercing it to zero.
	cost, costReported, err := singleScalarReported(ctx, s.prom,
		fmt.Sprintf(`sum(increase(%s[%s]))`, metricCostUsed, rangeKey))
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to query cost metrics: "+err.Error())
		return
	}

	resp := TokensSummary{
		Source:              source,
		InputTokens:         input,
		OutputTokens:        output,
		CacheReadTokens:     cacheRead,
		CacheWriteTokens:    cacheWrite,
		TotalTokens:         tokenTotal(input, output, cacheRead, cacheWrite),
		PreviousTotalTokens: prior,
		CostUSD:             cost,
		CacheReported:       cacheReported,
		CostReported:        costReported,
		UpdatedAt:           time.Now().UTC(),
	}
	s.observabilityCache.set(cacheKey, resp)
	writeJSON(w, http.StatusOK, resp)
}

// getTokensTimeseries returns a 24h sparkline of total tokens consumed,
// stepped at 30-minute intervals.
func (s *Server) getTokensTimeseries(w http.ResponseWriter, r *http.Request) {
	rangeKey := r.URL.Query().Get("range")
	if rangeKey == "" {
		rangeKey = defaultSummaryRange
	}
	// Only 24h is supported initially — the tile is hardcoded to 24h on the UI side.
	// Accepting a parameter keeps the endpoint shape consistent with the rest of
	// /metrics/* and leaves room for 7d later without an API break.
	if rangeKey != defaultSummaryRange {
		writeError(w, http.StatusBadRequest, "unsupported range; only 24h is supported")
		return
	}

	source, reason := s.tokensBackend()
	if source == "" {
		writeError(w, http.StatusServiceUnavailable, reason)
		return
	}

	cacheKey := "tokens-timeseries-" + source + "-" + rangeKey
	if cached, ok := s.observabilityCache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, cached)
		return
	}

	end := time.Now().UTC()
	start := end.Add(-summaryWindow)

	if source == metricsSourcePostgres {
		buckets, err := s.taskLogs.TokenUsageBuckets(r.Context(), start, end, timeseriesStep)
		if err != nil {
			writeError(w, http.StatusBadGateway, "failed to query token records: "+err.Error())
			return
		}
		// Dense output, same contract as the event timeseries: the sparkline
		// places points by index, so a sparse response would stretch a few
		// populated buckets across the whole day. Buckets are aligned to the
		// step on the UTC epoch, matching the store's floor().
		byBucket := make(map[int64]float64, len(buckets))
		for _, b := range buckets {
			byBucket[b.Bucket.Unix()] = float64(b.TotalTokens)
		}
		gridStart := start.Truncate(timeseriesStep)
		points := make([]TimeseriesPoint, 0)
		for ts := gridStart; ts.Before(end); ts = ts.Add(timeseriesStep) {
			points = append(points, TimeseriesPoint{Timestamp: ts, Value: byBucket[ts.Unix()]})
		}
		resp := TokensTimeseries{
			Source: source,
			Range:  rangeKey,
			Step:   timeseriesStep.String(),
			Points: points,
		}
		s.observabilityCache.set(cacheKey, resp)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	query := fmt.Sprintf(`sum(increase(%s[%s]))`, metricTokensUsed, "30m")

	result, err := s.prom.QueryRange(r.Context(), query, start, end, timeseriesStep)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to query token timeseries: "+err.Error())
		return
	}

	points := make([]TimeseriesPoint, 0)
	if len(result.Series) > 0 {
		for _, sample := range result.Series[0].Samples {
			points = append(points, TimeseriesPoint{
				Timestamp: sample.Timestamp.UTC(),
				Value:     sample.Value,
			})
		}
	}

	resp := TokensTimeseries{
		Source: source,
		Range:  rangeKey,
		Step:   timeseriesStep.String(),
		Points: points,
	}
	s.observabilityCache.set(cacheKey, resp)
	writeJSON(w, http.StatusOK, resp)
}

// getTokensBySubject returns one row per (agent, repo, event_type, model) with
// input/output token totals and the priced cost. Honors the page-level range
// selector via ?range=. Records by default (task_conversations grouped by the
// subject columns migration 0024 landed); the PromQL path answers when the
// counters are pinned or the hub has no database — see tokensBackend.
func (s *Server) getTokensBySubject(w http.ResponseWriter, r *http.Request) {
	rangeKey := r.URL.Query().Get("range")
	if rangeKey == "" {
		rangeKey = defaultSummaryRange
	}
	if _, ok := supportedRanges[rangeKey]; !ok {
		writeError(w, http.StatusBadRequest, "unsupported range; allowed values: 1h, 6h, 24h, 7d")
		return
	}

	source, reason := s.tokensBackend()
	if source == "" {
		writeError(w, http.StatusServiceUnavailable, reason)
		return
	}

	// Keyed by source, as the summary and timeseries do: a hub that gains (or
	// loses) a backend within the TTL cannot serve the other backend's figures
	// under this one's name.
	cacheKey := "tokens-by-subject:" + source + ":" + rangeKey
	if cached, ok := s.observabilityCache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, cached)
		return
	}

	ctx := r.Context()

	// Records path: group the conversation rows by the subject the runner
	// reported with each turn. The window is [now-range, now) — the same one
	// the summary tile sums — so the table's total equals the tile's for the
	// same range. Rows with no agent name stay out, as on the PromQL path:
	// they would render as a blank row.
	if source == metricsSourcePostgres {
		rng := supportedRanges[rangeKey]
		now := time.Now().UTC()
		storeRows, err := s.taskLogs.TokenUsageBySubject(ctx, now.Add(-rng.Duration), now)
		if err != nil {
			writeError(w, http.StatusBadGateway, "failed to query token records: "+err.Error())
			return
		}
		out := make([]TokenSubjectRow, 0, len(storeRows))
		for _, row := range storeRows {
			if row.AgentName == "" {
				continue
			}
			out = append(out, TokenSubjectRow{
				Agent:            row.AgentName,
				AgentName:        row.AgentName,
				Repo:             row.Repo,
				EventType:        row.EventType,
				Model:            row.Model,
				InputTokens:      float64(row.InputTokens),
				OutputTokens:     float64(row.OutputTokens),
				CacheReadTokens:  float64(row.CacheReadTokens),
				CacheWriteTokens: float64(row.CacheWriteTokens),
				TotalTokens:      tokenTotal(float64(row.InputTokens), float64(row.OutputTokens), float64(row.CacheReadTokens), float64(row.CacheWriteTokens)),
				CostUSD:          row.CostUSD,
				CacheReported:    row.CacheReported,
				CostReported:     row.CostReported,
			})
		}
		sortTokenSubjectRows(out)

		resp := TokensBySubject{
			Range:     rangeKey,
			Rows:      out,
			UpdatedAt: time.Now().UTC(),
			Source:    source,
		}
		s.observabilityCache.set(cacheKey, resp)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	tokensQ := fmt.Sprintf(`sum by (agent, repo, event_type, model, token_type) (increase(%s[%s]))`, metricTokensUsed, rangeKey)
	tokensResult, err := s.prom.Query(ctx, tokensQ)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to query token metrics: "+err.Error())
		return
	}

	// Cost rides the same subject labels as the token counter, as its own
	// series (no token_type). Presence of the series is the report (issue
	// #75): agents on unpriced models raise no series and their rows read
	// costReported false — "not reported", not a measured $0.
	costQ := fmt.Sprintf(`sum by (agent, repo, event_type, model) (increase(%s[%s]))`, metricCostUsed, rangeKey)
	costResult, err := s.prom.Query(ctx, costQ)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to query cost metrics: "+err.Error())
		return
	}

	rows := map[subjectKey]*TokenSubjectRow{}
	for _, m := range tokensResult.Data {
		key := subjectKey{
			Agent:     m.Labels["agent"],
			Repo:      m.Labels["repo"],
			EventType: m.Labels["event_type"],
			Model:     m.Labels["model"],
		}
		// Skip rows where the agent label is missing — these are usually a
		// stale series or a malformed exporter and would render as a blank row.
		if key.Agent == "" {
			continue
		}
		row, ok := rows[key]
		if !ok {
			row = &TokenSubjectRow{Agent: key.Agent, Repo: key.Repo, EventType: key.EventType, Model: key.Model}
			rows[key] = row
		}
		switch m.Labels["token_type"] {
		case tokenTypeInput:
			row.InputTokens += m.Value
		case tokenTypeOutput:
			row.OutputTokens += m.Value
		case tokenTypeCacheRead:
			row.CacheReadTokens += m.Value
			row.CacheReported = true
		case tokenTypeCacheWrite:
			row.CacheWriteTokens += m.Value
			row.CacheReported = true
		}
	}
	for _, m := range costResult.Data {
		key := subjectKey{
			Agent:     m.Labels["agent"],
			Repo:      m.Labels["repo"],
			EventType: m.Labels["event_type"],
			Model:     m.Labels["model"],
		}
		if key.Agent == "" {
			continue
		}
		row, ok := rows[key]
		if !ok {
			row = &TokenSubjectRow{Agent: key.Agent, Repo: key.Repo, EventType: key.EventType, Model: key.Model}
			rows[key] = row
		}
		row.CostUSD += m.Value
		row.CostReported = true
	}

	nameMap := s.agentNameMap(ctx)
	out := make([]TokenSubjectRow, 0, len(rows))
	for _, row := range rows {
		row.TotalTokens = tokenTotal(row.InputTokens, row.OutputTokens, row.CacheReadTokens, row.CacheWriteTokens)
		if name, ok := nameMap[row.Agent]; ok && name != "" {
			row.AgentName = name
		} else {
			row.AgentName = row.Agent
		}
		out = append(out, *row)
	}
	// Stable order so polling on the frontend doesn't shuffle the table.
	sortTokenSubjectRows(out)

	resp := TokensBySubject{
		Range:     rangeKey,
		Rows:      out,
		UpdatedAt: time.Now().UTC(),
		Source:    source,
	}
	s.observabilityCache.set(cacheKey, resp)
	writeJSON(w, http.StatusOK, resp)
}

// sortTokenSubjectRows orders by-subject rows by (repo, event_type, agent) so
// polling doesn't shuffle the table. Both backends use it: the frontend
// re-sorts by totalTokens client-side, and a shared order keeps rows from
// jumping when the response's source switches backends.
func sortTokenSubjectRows(out []TokenSubjectRow) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		if out[i].EventType != out[j].EventType {
			return out[i].EventType < out[j].EventType
		}
		return out[i].Agent < out[j].Agent
	})
}

// subjectKey identifies a row in the by-subject response.
type subjectKey struct {
	Agent     string
	Repo      string
	EventType string
	Model     string
}

// TokenEventRow is one row in the tokens-by-event response — a unique event
// with its aggregated token totals.
//
// Backed by the task_conversations table: since migration 0022 it persists all
// four components pi reports, so TotalTokens here follows the same accounting
// rule as the summary tile (tokenTotal over every component) and the two views
// are comparable for conversations reported by cache-aware runtimes. Since
// migration 0025 the cache/cost columns are NULL for figures the runtime never
// reported, and CacheReported/CostReported carry that distinction per event
// (issue #75): false means the summed figure is a placeholder for "not
// reported", not a measured zero.
type TokenEventRow struct {
	Event            string  `json:"event"`
	InputTokens      float64 `json:"inputTokens"`
	OutputTokens     float64 `json:"outputTokens"`
	CacheReadTokens  float64 `json:"cacheReadTokens"`
	CacheWriteTokens float64 `json:"cacheWriteTokens"`
	TotalTokens      float64 `json:"totalTokens"`
	CostUSD          float64 `json:"costUsd"`
	CacheReported    bool    `json:"cacheReported"`
	CostReported     bool    `json:"costReported"`
}

// TokensByEvent is the response for the /metrics/tokens/by-event endpoint.
type TokensByEvent struct {
	Range     string          `json:"range"`
	Rows      []TokenEventRow `json:"rows"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// getTokensByEvent returns token totals grouped by event ID. It queries
// task_conversations grouped by invocation_id, then maps each invocation to
// its originating event via the in-memory invocations store.
func (s *Server) getTokensByEvent(w http.ResponseWriter, r *http.Request) {
	rangeKey := r.URL.Query().Get("range")
	if rangeKey == "" {
		rangeKey = defaultSummaryRange
	}
	if _, ok := supportedRanges[rangeKey]; !ok {
		writeError(w, http.StatusBadRequest, "unsupported range; allowed values: 1h, 6h, 24h, 7d")
		return
	}

	cacheKey := "tokens-by-event-" + rangeKey
	if cached, ok := s.observabilityCache.get(cacheKey); ok {
		writeJSON(w, http.StatusOK, cached)
		return
	}

	if !s.taskLogs.Ready() {
		writeError(w, http.StatusServiceUnavailable, logStoreRequiredMessage)
		return
	}

	rng := supportedRanges[rangeKey]
	since := time.Now().UTC().Add(-rng.Duration)

	invRows, err := s.taskLogs.TokensByInvocationSince(r.Context(), since)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to query token data: "+err.Error())
		return
	}

	out := aggregateTokensByEvent(invRows, s.invocations)

	resp := TokensByEvent{
		Range:     rangeKey,
		Rows:      out,
		UpdatedAt: time.Now().UTC(),
	}
	s.observabilityCache.set(cacheKey, resp)
	writeJSON(w, http.StatusOK, resp)
}

// invocationGetter is the subset of invocations.Store used by
// aggregateTokensByEvent, allowing tests to supply a fake.
type invocationGetter interface {
	Get(id string) (invocations.Invocation, bool)
}

// aggregateTokensByEvent maps invocation-level token rows to event-level
// totals via the invocations store, then returns a stable-sorted slice.
func aggregateTokensByEvent(invRows []tasklogs.InvocationTokenRow, invStore invocationGetter) []TokenEventRow {
	eventTokens := map[string]*TokenEventRow{}
	for _, row := range invRows {
		inv, ok := invStore.Get(row.InvocationID)
		if !ok || inv.EventID == "" {
			continue
		}
		er, ok := eventTokens[inv.EventID]
		if !ok {
			er = &TokenEventRow{Event: inv.EventID}
			eventTokens[inv.EventID] = er
		}
		er.InputTokens += float64(row.InputTokens)
		er.OutputTokens += float64(row.OutputTokens)
		er.CacheReadTokens += float64(row.CacheReadTokens)
		er.CacheWriteTokens += float64(row.CacheWriteTokens)
		er.CostUSD += row.CostUSD
		// An event is "reported" if any of its invocations reported (issue #75).
		er.CacheReported = er.CacheReported || row.CacheReported
		er.CostReported = er.CostReported || row.CostReported
	}

	out := make([]TokenEventRow, 0, len(eventTokens))
	for _, row := range eventTokens {
		row.TotalTokens = tokenTotal(row.InputTokens, row.OutputTokens, row.CacheReadTokens, row.CacheWriteTokens)
		out = append(out, *row)
	}
	// Stable order by event ID so polling doesn't shuffle the table.
	sort.Slice(out, func(i, j int) bool { return out[i].Event < out[j].Event })
	return out
}
