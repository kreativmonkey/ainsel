package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/ainsel-project/ainsel/services/hub/internal/tasklogs"
	agentv1alpha1 "github.com/ainsel-project/ainsel/shared/api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Stats is the top-level stats response.
type Stats struct {
	Agents       ResourceStats `json:"agents"`
	Connectors   ResourceStats `json:"connectors"`
	Triggers     ResourceStats `json:"triggers"`
	CronTriggers ResourceStats `json:"cronTriggers"`
	Errors       ErrorStats    `json:"errors"`
	Tokens       TokenTotals   `json:"tokens"`
}

// ResourceStats holds total and healthy counts for a resource type.
type ResourceStats struct {
	Total   int `json:"total"`
	Healthy int `json:"healthy"`
}

// ErrorStats holds error counts.
type ErrorStats struct {
	LastHour int `json:"lastHour"`
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getStats(r.Context(), w)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) getStats(ctx context.Context, w http.ResponseWriter) {
	stats := s.GetStats(ctx)
	writeJSON(w, http.StatusOK, stats)
}

// GetStats returns the current stats. Exported for use by the WebSocket broadcaster.
func (s *Server) GetStats(ctx context.Context) Stats {
	var stats Stats

	// Agents
	var agentList agentv1alpha1.AgentList
	if err := s.client.List(ctx, &agentList, client.InNamespace(s.ns)); err == nil {
		stats.Agents.Total = len(agentList.Items)
		for _, a := range agentList.Items {
			if isReady(a.Status.Conditions) {
				stats.Agents.Healthy++
			}
		}
	}

	// Connectors (WebhookConnector)
	var connectorList agentv1alpha1.WebhookConnectorList
	if err := s.client.List(ctx, &connectorList, client.InNamespace(s.ns)); err == nil {
		stats.Connectors.Total = len(connectorList.Items)
		for _, c := range connectorList.Items {
			if isReady(c.Status.Conditions) {
				stats.Connectors.Healthy++
			}
		}
	}

	// Triggers (from database)
	if s.triggerStore != nil {
		allTriggers, err := s.triggerStore.ListTriggers(ctx, "", "")
		if err == nil {
			stats.Triggers.Total = len(allTriggers)
			for _, t := range allTriggers {
				if t.AgentValid && t.ConnectorValid {
					stats.Triggers.Healthy++
				}
			}
		}
	}

	// CronTriggers (from database)
	if s.triggerStore != nil {
		allCronTriggers, err := s.triggerStore.ListCronTriggers(ctx, "")
		if err == nil {
			stats.CronTriggers.Total = len(allCronTriggers)
			for _, ct := range allCronTriggers {
				if ct.AgentValid && ct.ScheduleValid {
					stats.CronTriggers.Healthy++
				}
			}
		}
	}

	// Errors in the last hour — query task_logs
	if s.taskLogs != nil {
		count, err := s.taskLogs.CountByLevelSince(ctx, tasklogs.LevelError, time.Now().Add(-time.Hour))
		if err == nil {
			stats.Errors.LastHour = count
		} else {
			slog.Warn("task log error count failed", "error", err)
		}
	}

	// Token totals — the hub's own conversation rows by default, Prometheus
	// when pinned or when there is no database. The runtime publishes
	// prompt-cache traffic as its own token_type series and pi's usage.input
	// excludes it, so all four components are read and summed on either path.
	// Note the windows differ by backend by construction: the records hold
	// everything the retention loop has not pruned (ConversationRetention),
	// the counters live and die with the process that raised them.
	switch source, _ := s.tokensBackend(); source {
	case metricsSourcePostgres:
		row, err := s.taskLogs.TokenUsageSince(ctx, time.Time{}, time.Time{})
		if err != nil {
			slog.Warn("token records query failed", "error", err)
			break
		}
		stats.Tokens.InputTokens = float64(row.InputTokens)
		stats.Tokens.OutputTokens = float64(row.OutputTokens)
		stats.Tokens.CacheReadTokens = float64(row.CacheReadTokens)
		stats.Tokens.CacheWriteTokens = float64(row.CacheWriteTokens)
		stats.Tokens.CostUSD = row.CostUSD
		stats.Tokens.TotalTokens = row.Total()
		stats.Tokens.CacheReported = row.CacheReported
		stats.Tokens.CostReported = row.CostReported
	case metricsSourcePrometheus:
		result, err := s.prom.Query(ctx, `sum by (token_type) (agent_tokens_used_total)`)
		if err == nil {
			for _, m := range result.Data {
				switch m.Labels["token_type"] {
				case tokenTypeInput:
					stats.Tokens.InputTokens += m.Value
				case tokenTypeOutput:
					stats.Tokens.OutputTokens += m.Value
				case tokenTypeCacheRead:
					stats.Tokens.CacheReadTokens += m.Value
					stats.Tokens.CacheReported = true
				case tokenTypeCacheWrite:
					stats.Tokens.CacheWriteTokens += m.Value
					stats.Tokens.CacheReported = true
				}
			}
			stats.Tokens.TotalTokens = tokenTotal(
				stats.Tokens.InputTokens,
				stats.Tokens.OutputTokens,
				stats.Tokens.CacheReadTokens,
				stats.Tokens.CacheWriteTokens,
			)
			// Presence of the series is the report (issue #75): no
			// agent_cost_usd_total series means the models are unpriced —
			// "not reported" — while a 0-valued series is a measured free run.
			cost, costReported, err := singleScalarReported(ctx, s.prom, `sum(agent_cost_usd_total)`)
			if err == nil {
				stats.Tokens.CostUSD = cost
				stats.Tokens.CostReported = costReported
			}
		} else {
			slog.Warn("prometheus token query failed", "error", err)
		}
	}

	return stats
}

// isReady returns true if the "Ready" condition is "True".
func isReady(conditions []metav1.Condition) bool {
	for _, c := range conditions {
		if c.Type == "Ready" && c.Status == "True" {
			return true
		}
	}
	return false
}
