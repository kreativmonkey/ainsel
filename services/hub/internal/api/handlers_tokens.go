// internal/api/handlers_tokens.go
package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/ainsel-project/ainsel/services/hub/internal/prometheus"
	"github.com/ainsel-project/ainsel/services/hub/internal/tasklogs"
)

// TokenEntry represents token consumption for one agent + repo + issue + model combination.
//
// The runtime publishes prompt-cache traffic as its own token_type series, and
// pi's usage.input excludes it, so the cache components are reported alongside
// input/output rather than folded into them.
//
// The rows come from one of two backends — the hub's own conversation rows or
// the Prometheus counters (see tokensBackend for the rule) — and both answer
// the same (agent, repo, issue, model, token_type) tuple, so a consumer never
// has to know which one served it. The response names the source anyway for
// introspectability.
//
// CacheReported/CostReported are per-row presence flags (issue #75): false
// means no row/series in the group carried a reported cache/cost figure, so
// the summed numerics are placeholders readers must render as "not reported",
// never as a measured 0 / $0.00. A reported zero counts as reported.
type TokenEntry struct {
	Agent            string  `json:"agent"`
	Repository       string  `json:"repository"`
	IssueNumber      string  `json:"issueNumber"`
	Model            string  `json:"model"`
	InputTokens      float64 `json:"inputTokens"`
	OutputTokens     float64 `json:"outputTokens"`
	CacheReadTokens  float64 `json:"cacheReadTokens"`
	CacheWriteTokens float64 `json:"cacheWriteTokens"`
	TotalTokens      float64 `json:"totalTokens"`
	// CostUSD is priced consumption (pi's usage.cost, priced via
	// spec.llm.cost) — from the conversation rows' cost_usd column on the
	// records path, from agent_cost_usd_total on the Prometheus path. Only
	// meaningful when CostReported is true: an unpriced model reports
	// nothing, which is not the same fact as a run that cost nothing.
	CostUSD       float64 `json:"costUsd"`
	CacheReported bool    `json:"cacheReported"`
	CostReported  bool    `json:"costReported"`
}

// TokenTotals holds aggregate token counts.
//
// CacheReported/CostReported are window-level presence flags (issue #75):
// true when any row in the window reported the figure, so a totals zero
// backed entirely by NULLs / absent series reads as "not reported".
type TokenTotals struct {
	InputTokens      float64 `json:"inputTokens"`
	OutputTokens     float64 `json:"outputTokens"`
	CacheReadTokens  float64 `json:"cacheReadTokens"`
	CacheWriteTokens float64 `json:"cacheWriteTokens"`
	TotalTokens      float64 `json:"totalTokens"`
	CostUSD          float64 `json:"costUsd"`
	CacheReported    bool    `json:"cacheReported"`
	CostReported     bool    `json:"costReported"`
}

func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listTokens(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// listTokens serves GET /api/v1/tokens — the endpoint the MCP get_token_usage
// tool reads. It answers from the hub's own conversation rows by default and
// from the Prometheus counters when the hub is pinned to them, on the shared
// rule tokensBackend documents: records by default, Prometheus when pinned,
// never a silent swap. Both backends answer the same subject tuple, so the
// response shape is identical; the `source` field names which one served.
func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	source, reason := s.tokensBackend()
	if source == "" {
		writeError(w, http.StatusServiceUnavailable, reason)
		return
	}

	q := r.URL.Query()

	// issueNumber is an integer: the records path binds it as one (the column
	// is INTEGER), and the PromQL path has always matched its decimal form as
	// a label value. Guard on presence, never on a parsed zero — issue_number
	// = 0 is a real value in the records (subject unknown, pre-0024 rows), not
	// a "no filter" marker.
	var issueNumber int
	hasIssue := false
	if v := q.Get("issueNumber"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "issueNumber must be an integer")
			return
		}
		issueNumber, hasIssue = n, true
	}

	// since/until bound the records window (RFC 3339). The PromQL path has no
	// window — it reads cumulative counters — but a malformed bound is a
	// client error on both paths rather than a silently ignored filter.
	var since, until time.Time
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be an RFC 3339 timestamp")
			return
		}
		since = t
	}
	if v := q.Get("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "until must be an RFC 3339 timestamp")
			return
		}
		until = t
	}

	if source == metricsSourcePostgres {
		s.listTokensFromRecords(w, r, q, since, until, issueNumber, hasIssue)
		return
	}
	s.listTokensFromPrometheus(w, r, q)
}

// listTokensFromRecords answers from task_conversations, grouped by
// (agent_name, repo, issue_number, model). Absent since/until mean
// "everything the store still retains" — the conversations table has its own
// retention loop (tasklogs.ConversationRetention) — so the records path and
// the cumulative-counter path agree in meaning, not just in shape.
func (s *Server) listTokensFromRecords(w http.ResponseWriter, r *http.Request, q url.Values,
	since, until time.Time, issueNumber int, hasIssue bool) {
	f := tasklogs.TokenUsageByIssueFilter{
		Since:     since,
		Until:     until,
		AgentName: q.Get("agent"),
		Repo:      q.Get("repository"),
	}
	if hasIssue {
		f.IssueNumber = &issueNumber
	}

	rows, err := s.taskLogs.TokenUsageByIssue(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to query token records: "+err.Error())
		return
	}

	tokens := make([]TokenEntry, 0, len(rows))
	var totals TokenTotals
	for _, row := range rows {
		entry := TokenEntry{
			Agent:            row.AgentName,
			Repository:       row.Repo,
			IssueNumber:      issueNumberLabel(row.IssueNumber),
			Model:            row.Model,
			InputTokens:      float64(row.InputTokens),
			OutputTokens:     float64(row.OutputTokens),
			CacheReadTokens:  float64(row.CacheReadTokens),
			CacheWriteTokens: float64(row.CacheWriteTokens),
			CostUSD:          row.CostUSD,
			CacheReported:    row.CacheReported,
			CostReported:     row.CostReported,
		}
		entry.TotalTokens = tokenTotal(entry.InputTokens, entry.OutputTokens, entry.CacheReadTokens, entry.CacheWriteTokens)
		tokens = append(tokens, entry)
		totals.InputTokens += entry.InputTokens
		totals.OutputTokens += entry.OutputTokens
		totals.CacheReadTokens += entry.CacheReadTokens
		totals.CacheWriteTokens += entry.CacheWriteTokens
		totals.CostUSD += entry.CostUSD
		totals.CacheReported = totals.CacheReported || entry.CacheReported
		totals.CostReported = totals.CostReported || entry.CostReported
	}
	totals.TotalTokens = tokenTotal(totals.InputTokens, totals.OutputTokens, totals.CacheReadTokens, totals.CacheWriteTokens)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"tokens": tokens,
		"total":  totals,
		"source": metricsSourcePostgres,
	})
}

// issueNumberLabel renders a record's issue number for the JSON API, where
// the value has always been the Prometheus label string: an unknown subject
// (issue_number 0, rows written before migration 0024) reads as "", the same
// as a series without the issue_id label. Consumers render "" as "unknown".
func issueNumberLabel(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func (s *Server) listTokensFromPrometheus(w http.ResponseWriter, r *http.Request, q url.Values) {
	// Build PromQL query with optional label filters.
	// The agent emits agent_tokens_used_total with labels
	// {agent, repo, org, event_type, issue_id, model, token_type}.
	// The JSON API still exposes "repository"/"issueNumber" so the frontend
	// doesn't have to change.
	filters := ""
	var parts []string
	if v := q.Get("agent"); v != "" {
		parts = append(parts, fmt.Sprintf(`agent="%s"`, prometheus.SanitizeLabel(v)))
	}
	if v := q.Get("repository"); v != "" {
		parts = append(parts, fmt.Sprintf(`repo="%s"`, prometheus.SanitizeLabel(v)))
	}
	if v := q.Get("issueNumber"); v != "" {
		parts = append(parts, fmt.Sprintf(`issue_id="%s"`, prometheus.SanitizeLabel(v)))
	}
	if len(parts) > 0 {
		filters = "{" + joinFilters(parts) + "}"
	}

	promql := fmt.Sprintf(
		`sum by (agent, repo, issue_id, model, token_type) (agent_tokens_used_total%s)`,
		filters,
	)

	result, err := s.prom.Query(r.Context(), promql)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to query metrics: "+err.Error())
		return
	}

	// Cost rides the same subject labels as the token counter without the
	// token_type dimension. Presence of the series is the report (issue #75):
	// agents on unpriced models raise no series and their rows read
	// costReported false — "not reported", not a measured $0.
	costPromql := fmt.Sprintf(
		`sum by (agent, repo, issue_id, model) (agent_cost_usd_total%s)`,
		filters,
	)
	costResult, err := s.prom.Query(r.Context(), costPromql)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to query cost metrics: "+err.Error())
		return
	}

	// Merge the per-token_type rows into TokenEntry objects.
	// Key: agent|repo|issue_id|model
	type entryKey struct {
		Agent       string
		Repository  string
		IssueNumber string
		Model       string
	}
	merged := map[entryKey]*TokenEntry{}

	for _, m := range result.Data {
		key := entryKey{
			Agent:       m.Labels["agent"],
			Repository:  m.Labels["repo"],
			IssueNumber: m.Labels["issue_id"],
			Model:       m.Labels["model"],
		}
		entry, ok := merged[key]
		if !ok {
			entry = &TokenEntry{
				Agent:       key.Agent,
				Repository:  key.Repository,
				IssueNumber: key.IssueNumber,
				Model:       key.Model,
			}
			merged[key] = entry
		}
		switch m.Labels["token_type"] {
		case tokenTypeInput:
			entry.InputTokens += m.Value
		case tokenTypeOutput:
			entry.OutputTokens += m.Value
		case tokenTypeCacheRead:
			entry.CacheReadTokens += m.Value
			entry.CacheReported = true
		case tokenTypeCacheWrite:
			entry.CacheWriteTokens += m.Value
			entry.CacheReported = true
		}
	}

	for _, m := range costResult.Data {
		key := entryKey{
			Agent:       m.Labels["agent"],
			Repository:  m.Labels["repo"],
			IssueNumber: m.Labels["issue_id"],
			Model:       m.Labels["model"],
		}
		entry, ok := merged[key]
		if !ok {
			entry = &TokenEntry{
				Agent:       key.Agent,
				Repository:  key.Repository,
				IssueNumber: key.IssueNumber,
				Model:       key.Model,
			}
			merged[key] = entry
		}
		entry.CostUSD += m.Value
		entry.CostReported = true
	}

	tokens := make([]TokenEntry, 0, len(merged))
	var totals TokenTotals
	for _, entry := range merged {
		entry.TotalTokens = tokenTotal(entry.InputTokens, entry.OutputTokens, entry.CacheReadTokens, entry.CacheWriteTokens)
		tokens = append(tokens, *entry)
		totals.InputTokens += entry.InputTokens
		totals.OutputTokens += entry.OutputTokens
		totals.CacheReadTokens += entry.CacheReadTokens
		totals.CacheWriteTokens += entry.CacheWriteTokens
		totals.CostUSD += entry.CostUSD
		totals.CacheReported = totals.CacheReported || entry.CacheReported
		totals.CostReported = totals.CostReported || entry.CostReported
	}
	totals.TotalTokens = tokenTotal(totals.InputTokens, totals.OutputTokens, totals.CacheReadTokens, totals.CacheWriteTokens)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"tokens": tokens,
		"total":  totals,
		"source": metricsSourcePrometheus,
	})
}

func joinFilters(parts []string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += ", "
		}
		result += p
	}
	return result
}
