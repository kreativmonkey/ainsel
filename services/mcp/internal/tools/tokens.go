package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// UsageTools wraps the hub's token usage (/api/v1/tokens) and dashboard summary
// (/api/v1/stats) endpoints. Both are served from the hub's own records by
// default — the conversation rows the hub writes as agents report usage — and
// from Prometheus counters only when the hub is pinned to them; neither is a
// stub at the time of writing.
//
// Both pass the hub's JSON through verbatim, so they carry costUsd once
// models are priced (spec.llm.cost, issue #281) and the cacheReported /
// costReported presence flags alongside it: an agent on unpriced models
// reports no cost at all, which the flags mark as "not reported" rather than
// a measured zero (issue #75). The token response also carries a source field
// naming the backend that answered.
//
// /api/v1/tokens supports agent, repository, issueNumber filters, and the
// records path honours since/until (RFC 3339); the Prometheus path reads
// cumulative counters and ignores them.
type UsageTools struct {
	HubURL     string
	HTTPClient *http.Client
}

func NewUsageTools(hubURL string) *UsageTools {
	return &UsageTools{
		HubURL:     hubURL,
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (t *UsageTools) GetTokenUsageTool() mcp.Tool {
	return mcp.NewTool("get_token_usage",
		mcp.WithDescription("Hub-aggregated token usage: counts per agent, repository, issue, and model, split into input, output, cacheRead and cacheWrite with a total, plus costUsd (USD, priced from each model's spec.llm.cost; when costReported is false the model is unpriced and costUsd is not a measured zero). Results capped at 50 rows."),
		mcp.WithString("agent", mcp.Description("Filter by agent name")),
		mcp.WithString("repository", mcp.Description("Filter by repository")),
		mcp.WithString("issueNumber", mcp.Description("Filter by issue number")),
		mcp.WithString("since", mcp.Description("Earliest timestamp (RFC 3339); honoured when the hub answers from its own records (the default), ignored on a Prometheus-pinned hub")),
		mcp.WithString("until", mcp.Description("Latest timestamp (RFC 3339); honoured when the hub answers from its own records (the default), ignored on a Prometheus-pinned hub")),
	)
}

func (t *UsageTools) GetStatsTool() mcp.Tool {
	return mcp.NewTool("get_stats",
		mcp.WithDescription("Dashboard summary: counts of agents, triggers, connectors (with healthy subset), error count in the last hour, and aggregate token totals."),
	)
}

func (t *UsageTools) GetTokenUsage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, _ := req.Params.Arguments.(map[string]any)
	q := url.Values{}
	for _, k := range []string{"agent", "repository", "issueNumber", "since", "until"} {
		if v, ok := args[k].(string); ok && v != "" {
			q.Set(k, v)
		}
	}
	path := "/api/v1/tokens"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	body, err := hubGet(ctx, t.HTTPClient, t.HubURL, path)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to get token usage: %v", err)), nil
	}
	// The hub's /api/v1/tokens endpoint does not support a limit param, so
	// cap locally at 50 rows to keep the response compact for MCP clients.
	body = capBareArray(body, 50, "more token usage rows available — narrow filters (agent, repository, since/until) to refine")
	return mcp.NewToolResultText(string(body)), nil
}

func (t *UsageTools) GetStats(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	body, err := hubGet(ctx, t.HTTPClient, t.HubURL, "/api/v1/stats")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to get stats: %v", err)), nil
	}
	return mcp.NewToolResultText(string(body)), nil
}
