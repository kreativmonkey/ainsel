package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ainsel-project/ainsel/services/hub/internal/invocations"
	"github.com/ainsel-project/ainsel/services/hub/internal/prometheus"
	"github.com/ainsel-project/ainsel/services/hub/internal/tasklogs"
	agentv1alpha1 "github.com/ainsel-project/ainsel/shared/api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// newServerWithTokensProm registers the canonical tokens routes onto a server
// wired with the given fake Prometheus client and optional seeded K8s objects.
// The source is pinned to Prometheus: these tests cover the PromQL path, and
// without the pin a server that ever grew a records store would silently move
// them onto the other backend (the pattern the summary/timeseries source tests
// use). by-event ignores the pin — it is records-only by design.
func newServerWithTokensProm(t *testing.T, prom *prometheus.Client, objects ...runtime.Object) *Server {
	t.Helper()
	s := testServer(t, objects...)
	s.prom = prom
	s.SetMetricsSource(MetricsSourcePrometheus)
	s.mux.HandleFunc("/api/v1/observability/metrics/tokens/summary", s.handleObservability)
	s.mux.HandleFunc("/api/v1/observability/metrics/tokens/timeseries", s.handleObservability)
	s.mux.HandleFunc("/api/v1/observability/metrics/tokens/by-subject", s.handleObservability)
	s.mux.HandleFunc("/api/v1/observability/metrics/tokens/by-event", s.handleObservability)
	return s
}

// --- summary -----------------------------------------------------------------

func TestTokensSummary_ReturnsCurrentAndPriorWindowTotals(t *testing.T) {
	srv := fakePromServer(t, func(path string, params url.Values) interface{} {
		q := params.Get("query")
		switch {
		case strings.Contains(q, "offset 24h"):
			return vectorResponse([]vectorSample{{Labels: map[string]string{}, Value: "1200"}})
		case strings.Contains(q, "sum by (token_type)"):
			return vectorResponse([]vectorSample{
				{Labels: map[string]string{"token_type": "input"}, Value: "1500"},
				{Labels: map[string]string{"token_type": "output"}, Value: "300"},
				{Labels: map[string]string{"token_type": "cache_read"}, Value: "9000"},
				{Labels: map[string]string{"token_type": "cache_write"}, Value: "700"},
			})
		case strings.Contains(q, "agent_cost_usd_total"):
			return vectorResponse([]vectorSample{{Labels: map[string]string{}, Value: "1.25"}})
		}
		t.Fatalf("unexpected query: %s", q)
		return nil
	})
	defer srv.Close()

	s := newServerWithTokensProm(t, prometheus.NewClient(srv.URL, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/summary", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body TokensSummary
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.InputTokens != 1500 || body.OutputTokens != 300 {
		t.Errorf("input/output wrong: %+v", body)
	}
	if body.CacheReadTokens != 9000 || body.CacheWriteTokens != 700 {
		t.Errorf("cache components wrong: %+v", body)
	}
	// The total must include cache traffic. Pi's usage.input excludes it, so
	// input+output alone (1800) would under-report the 11500 actually used.
	if body.TotalTokens != 11500 {
		t.Errorf("total must include cache components, got %v", body.TotalTokens)
	}
	if body.PreviousTotalTokens != 1200 {
		t.Errorf("previous total wrong: %v", body.PreviousTotalTokens)
	}
	if body.CostUSD != 1.25 {
		t.Errorf("cost wrong: %v", body.CostUSD)
	}
	// Series exist for cache and cost, so the window is "reported" (issue #75).
	if !body.CacheReported || !body.CostReported {
		t.Errorf("presence flags = %v/%v, want true/true", body.CacheReported, body.CostReported)
	}
	if body.UpdatedAt.IsZero() {
		t.Error("expected updatedAt to be populated")
	}
}

func TestTokensSummary_HandlesEmptyResults(t *testing.T) {
	srv := fakePromServer(t, func(path string, params url.Values) interface{} {
		return vectorResponse(nil)
	})
	defer srv.Close()

	s := newServerWithTokensProm(t, prometheus.NewClient(srv.URL, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/summary", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body TokensSummary
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.InputTokens != 0 || body.OutputTokens != 0 || body.TotalTokens != 0 || body.PreviousTotalTokens != 0 {
		t.Errorf("expected zeros, got %+v", body)
	}
	if body.CacheReadTokens != 0 || body.CacheWriteTokens != 0 {
		t.Errorf("expected zero cache components, got %+v", body)
	}
	// No series at all means nothing was reported (issue #75): the zeros above
	// are placeholders, and the flags must say so rather than let the UI
	// render a measured-looking 0% / $0.00.
	if body.CacheReported {
		t.Errorf("cacheReported = true, want false (no cache series)")
	}
	if body.CostReported {
		t.Errorf("costReported = true, want false (no cost series: unpriced models)")
	}
}

func TestTokensSummary_ReportedZerosKeepPresence(t *testing.T) {
	// Issue #75: a series that exists with value 0 is a measurement, not an
	// absence. A provider reporting cached_tokens: 0 has a real 0% hit rate,
	// and a priced model on a free run really costs $0 — both must keep
	// cacheReported/costReported true so the UI renders the zeros as measured.
	srv := fakePromServer(t, func(path string, params url.Values) interface{} {
		q := params.Get("query")
		switch {
		case strings.Contains(q, "offset 24h"):
			return vectorResponse(nil)
		case strings.Contains(q, "sum by (token_type)"):
			return vectorResponse([]vectorSample{
				{Labels: map[string]string{"token_type": "input"}, Value: "100"},
				{Labels: map[string]string{"token_type": "output"}, Value: "20"},
				{Labels: map[string]string{"token_type": "cache_read"}, Value: "0"},
				{Labels: map[string]string{"token_type": "cache_write"}, Value: "0"},
			})
		case strings.Contains(q, "agent_cost_usd_total"):
			return vectorResponse([]vectorSample{{Labels: map[string]string{}, Value: "0"}})
		}
		t.Fatalf("unexpected query: %s", q)
		return nil
	})
	defer srv.Close()

	s := newServerWithTokensProm(t, prometheus.NewClient(srv.URL, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/summary", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body TokensSummary
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.CacheReported {
		t.Errorf("cacheReported = false, want true (0-valued cache series is a measured 0%%)")
	}
	if !body.CostReported {
		t.Errorf("costReported = false, want true (0-valued cost series is a measured $0)")
	}
	if body.TotalTokens != 120 {
		t.Errorf("totalTokens = %v, want 120", body.TotalTokens)
	}
}

func TestTokensSummary_Returns503WhenNoBackendConfigured(t *testing.T) {
	// Neither Prometheus nor the hub's own records. The records answer tokens
	// by default now, so the old "name Prometheus" message is no longer the
	// honest one: a database would fix this too.
	s := testServer(t)
	s.mux.HandleFunc("/api/v1/observability/metrics/tokens/summary", s.handleObservability)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/summary", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), metricsRequiredMessage) {
		t.Errorf("expected %q, got %q", metricsRequiredMessage, rec.Body.String())
	}
}

func TestTokensSummary_AcceptsRangeParam(t *testing.T) {
	var capturedQueries []string
	srv := fakePromServer(t, func(path string, params url.Values) interface{} {
		capturedQueries = append(capturedQueries, params.Get("query"))
		return vectorResponse([]vectorSample{{Labels: map[string]string{}, Value: "100"}})
	})
	defer srv.Close()

	s := newServerWithTokensProm(t, prometheus.NewClient(srv.URL, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/summary?range=1h", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// All queries should use [1h] range.
	for _, q := range capturedQueries {
		if !strings.Contains(q, "[1h]") {
			t.Errorf("expected query with [1h] range, got %q", q)
		}
	}
}

func TestTokensSummary_RejectsInvalidRange(t *testing.T) {
	srv := fakePromServer(t, func(string, url.Values) interface{} { return nil })
	defer srv.Close()
	s := newServerWithTokensProm(t, prometheus.NewClient(srv.URL, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/summary?range=42y", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTokensSummary_DefaultRangeIs24h(t *testing.T) {
	// Without ?range=, the handler should default to 24h and use [24h] in queries.
	var capturedQueries []string
	srv := fakePromServer(t, func(path string, params url.Values) interface{} {
		capturedQueries = append(capturedQueries, params.Get("query"))
		return vectorResponse([]vectorSample{{Labels: map[string]string{}, Value: "50"}})
	})
	defer srv.Close()

	s := newServerWithTokensProm(t, prometheus.NewClient(srv.URL, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/summary", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, q := range capturedQueries {
		if !strings.Contains(q, "[24h]") {
			t.Errorf("expected default query with [24h] range, got %q", q)
		}
	}
}

// --- timeseries --------------------------------------------------------------

func TestTokensTimeseries_ReturnsPoints(t *testing.T) {
	var capturedQuery string
	srv := fakePromServer(t, func(path string, params url.Values) interface{} {
		if path != "/api/v1/query_range" {
			t.Fatalf("expected query_range, got %s", path)
		}
		capturedQuery = params.Get("query")
		return matrixResponse([][2]float64{
			{1715100000.0, 100},
			{1715101800.0, 250},
			{1715103600.0, 75},
		})
	})
	defer srv.Close()

	s := newServerWithTokensProm(t, prometheus.NewClient(srv.URL, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/timeseries?range=24h", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(capturedQuery, "agent_tokens_used_total") {
		t.Errorf("expected agent_tokens_used_total in query, got %q", capturedQuery)
	}
	var body TokensTimeseries
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Range != "24h" {
		t.Errorf("expected range=24h, got %q", body.Range)
	}
	if len(body.Points) != 3 {
		t.Fatalf("expected 3 points, got %d", len(body.Points))
	}
	if body.Points[1].Value != 250 {
		t.Errorf("expected mid-point value 250, got %v", body.Points[1].Value)
	}
}

func TestTokensTimeseries_DefaultRangeIs24h(t *testing.T) {
	srv := fakePromServer(t, func(string, url.Values) interface{} { return matrixResponse(nil) })
	defer srv.Close()
	s := newServerWithTokensProm(t, prometheus.NewClient(srv.URL, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/timeseries", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body TokensTimeseries
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Range != "24h" {
		t.Errorf("expected default range=24h, got %q", body.Range)
	}
}

func TestTokensTimeseries_RejectsUnsupportedRange(t *testing.T) {
	srv := fakePromServer(t, func(string, url.Values) interface{} { return matrixResponse(nil) })
	defer srv.Close()
	s := newServerWithTokensProm(t, prometheus.NewClient(srv.URL, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/timeseries?range=7d", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for 7d range, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- by-subject --------------------------------------------------------------

func TestTokensBySubject_AggregatesRows(t *testing.T) {
	srv := fakePromServer(t, func(path string, params url.Values) interface{} {
		q := params.Get("query")
		switch {
		case strings.Contains(q, "agent_tokens_used_total"):
			return vectorResponse([]vectorSample{
				{Labels: map[string]string{"agent": "developer", "repo": "frontend", "event_type": "pull_request.opened", "model": "gpt-4", "token_type": "input"}, Value: "10000"},
				{Labels: map[string]string{"agent": "developer", "repo": "frontend", "event_type": "pull_request.opened", "model": "gpt-4", "token_type": "output"}, Value: "2000"},
				{Labels: map[string]string{"agent": "developer", "repo": "frontend", "event_type": "pull_request.opened", "model": "gpt-4", "token_type": "cache_read"}, Value: "60000"},
				{Labels: map[string]string{"agent": "architect", "repo": "backend", "event_type": "issue.assigned", "model": "claude-3", "token_type": "input"}, Value: "5000"},
				{Labels: map[string]string{"agent": "architect", "repo": "backend", "event_type": "issue.assigned", "model": "claude-3", "token_type": "output"}, Value: "1500"},
				{Labels: map[string]string{"agent": "architect", "repo": "backend", "event_type": "issue.assigned", "model": "claude-3", "token_type": "cache_write"}, Value: "2500"},
			})
		case strings.Contains(q, "agent_cost_usd_total"):
			return vectorResponse([]vectorSample{
				{Labels: map[string]string{"agent": "developer", "repo": "frontend", "event_type": "pull_request.opened", "model": "gpt-4"}, Value: "0.42"},
			})
		}
		t.Fatalf("unexpected query: %s", q)
		return nil
	})
	defer srv.Close()

	devAgent := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "developer", Namespace: "test-ns"},
		Spec:       agentv1alpha1.AgentSpec{DisplayName: "Developer Bot"},
	}
	archAgent := &agentv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "architect", Namespace: "test-ns"},
		Spec:       agentv1alpha1.AgentSpec{DisplayName: "Architect Bot"},
	}
	s := newServerWithTokensProm(t, prometheus.NewClient(srv.URL, nil), devAgent, archAgent)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/by-subject?range=24h", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body TokensBySubject
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Range != "24h" {
		t.Errorf("expected range=24h, got %q", body.Range)
	}
	if body.Source != metricsSourcePrometheus {
		t.Errorf("expected source=%q (pinned counters), got %q", metricsSourcePrometheus, body.Source)
	}
	if len(body.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d (%+v)", len(body.Rows), body.Rows)
	}
	// Sorted by repo asc: backend then frontend.
	if body.Rows[0].Repo != "backend" || body.Rows[1].Repo != "frontend" {
		t.Errorf("rows out of order: %+v", body.Rows)
	}
	architect := body.Rows[0]
	developer := body.Rows[1]
	if architect.Agent != "architect" || architect.EventType != "issue.assigned" || architect.Model != "claude-3" {
		t.Errorf("architect row mislabeled: %+v", architect)
	}
	if architect.AgentName != "Architect Bot" {
		t.Errorf("architect agentName wrong: got %q", architect.AgentName)
	}
	if architect.InputTokens != 5000 || architect.OutputTokens != 1500 {
		t.Errorf("architect input/output wrong: %+v", architect)
	}
	if architect.CacheWriteTokens != 2500 || architect.CacheReadTokens != 0 {
		t.Errorf("architect cache components wrong: %+v", architect)
	}
	if architect.TotalTokens != 9000 {
		t.Errorf("architect total must include cache_write, got %v", architect.TotalTokens)
	}
	if developer.Agent != "developer" || developer.EventType != "pull_request.opened" || developer.Model != "gpt-4" {
		t.Errorf("developer row mislabeled: %+v", developer)
	}
	if developer.AgentName != "Developer Bot" {
		t.Errorf("developer agentName wrong: got %q", developer.AgentName)
	}
	if developer.InputTokens != 10000 || developer.OutputTokens != 2000 {
		t.Errorf("developer input/output wrong: %+v", developer)
	}
	if developer.CacheReadTokens != 60000 || developer.CacheWriteTokens != 0 {
		t.Errorf("developer cache components wrong: %+v", developer)
	}
	if developer.TotalTokens != 72000 {
		t.Errorf("developer total must include cache_read, got %v", developer.TotalTokens)
	}
	// Cost rides its own series; the architect's model raised none, so its row
	// reads costReported false — "not reported", not a measured $0 (issue #75).
	if developer.CostUSD != 0.42 {
		t.Errorf("developer cost wrong: got %v", developer.CostUSD)
	}
	if architect.CostUSD != 0 {
		t.Errorf("architect cost should be 0 without a series, got %v", architect.CostUSD)
	}
	if !developer.CacheReported || !developer.CostReported {
		t.Errorf("developer presence = %v/%v, want true/true", developer.CacheReported, developer.CostReported)
	}
	if !architect.CacheReported {
		t.Errorf("architect cacheReported = false, want true (cache_write series exists)")
	}
	if architect.CostReported {
		t.Errorf("architect costReported = true, want false (no cost series for its group)")
	}
}

// TestTokenTotal_IncludesEveryComponent locks the accounting rule: pi reports
// usage.input excluding cache traffic, so a total that drops the cache
// components under-reports consumption by the whole cached volume.
func TestTokenTotal_IncludesEveryComponent(t *testing.T) {
	if got := tokenTotal(1, 2, 3, 4); got != 10 {
		t.Errorf("tokenTotal(1,2,3,4) = %v, want 10", got)
	}
	if got := tokenTotal(5, 5, 0, 0); got != 10 {
		t.Errorf("tokenTotal with no cache = %v, want 10", got)
	}
	// A pure cache hit has no billable input but is still real consumption.
	if got := tokenTotal(0, 0, 500, 0); got != 500 {
		t.Errorf("tokenTotal for cache-only message = %v, want 500", got)
	}
}

func TestTokensBySubject_EmptyResultIs200(t *testing.T) {
	srv := fakePromServer(t, func(string, url.Values) interface{} { return vectorResponse(nil) })
	defer srv.Close()
	s := newServerWithTokensProm(t, prometheus.NewClient(srv.URL, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/by-subject", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body TokensBySubject
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Rows) != 0 {
		t.Errorf("expected empty rows, got %+v", body.Rows)
	}
}

func TestTokensBySubject_SkipsRowsWithoutAgent(t *testing.T) {
	// A series without an agent label (stale exporter, misconfigured agent) must
	// not produce a row with a blank Agent field — it'd render as an empty row
	// on the dashboard.
	srv := fakePromServer(t, func(path string, params url.Values) interface{} {
		q := params.Get("query")
		if strings.Contains(q, "agent_tokens_used_total") {
			return vectorResponse([]vectorSample{
				{Labels: map[string]string{"agent": "", "repo": "x", "event_type": "y", "token_type": "input"}, Value: "1"},
				{Labels: map[string]string{"agent": "dev", "repo": "x", "event_type": "y", "token_type": "input"}, Value: "2"},
			})
		}
		return vectorResponse(nil)
	})
	defer srv.Close()
	s := newServerWithTokensProm(t, prometheus.NewClient(srv.URL, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/by-subject", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body TokensBySubject
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Rows) != 1 || body.Rows[0].Agent != "dev" {
		t.Errorf("expected one row for dev, got %+v", body.Rows)
	}
}

func TestTokensBySubject_FallsBackToIDWhenAgentMissing(t *testing.T) {
	srv := fakePromServer(t, func(path string, params url.Values) interface{} {
		q := params.Get("query")
		if strings.Contains(q, "agent_tokens_used_total") {
			return vectorResponse([]vectorSample{
				{Labels: map[string]string{"agent": "ghost", "repo": "x", "event_type": "y", "token_type": "input"}, Value: "5"},
			})
		}
		return vectorResponse(nil)
	})
	defer srv.Close()
	// No agents seeded — ghost is not in K8s.
	s := newServerWithTokensProm(t, prometheus.NewClient(srv.URL, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/by-subject", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body TokensBySubject
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Rows) != 1 {
		t.Fatalf("expected 1 row, got %+v", body.Rows)
	}
	if body.Rows[0].Agent != "ghost" {
		t.Errorf("expected agent=ghost, got %q", body.Rows[0].Agent)
	}
	if body.Rows[0].AgentName != "ghost" {
		t.Errorf("expected agentName to fallback to ID when missing, got %q", body.Rows[0].AgentName)
	}
}

func TestTokensBySubject_RejectsUnknownRange(t *testing.T) {
	srv := fakePromServer(t, func(string, url.Values) interface{} { return vectorResponse(nil) })
	defer srv.Close()
	s := newServerWithTokensProm(t, prometheus.NewClient(srv.URL, nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/by-subject?range=42y", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestTokensBySubject_PinnedPrometheusWithoutClientIs503(t *testing.T) {
	// The pin is not a preference to be quietly overridden: this hub has no
	// Prometheus, and the 503 must name it — the same rule the summary and the
	// per-agent endpoint follow, now that by-subject routes through tokensBackend.
	s := testServer(t)
	s.SetMetricsSource(MetricsSourcePrometheus)
	s.mux.HandleFunc("/api/v1/observability/metrics/tokens/by-subject", s.handleObservability)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/by-subject", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != promRequiredMessage {
		t.Errorf("error = %q, want %q", body.Error, promRequiredMessage)
	}
}

func TestTokensBySubject_NoBackendAtAllIs503(t *testing.T) {
	// Neither a database nor Prometheus: the 503 names both fixes, not just
	// Prometheus — a database would light this panel up on its own now.
	s := testServer(t)
	s.mux.HandleFunc("/api/v1/observability/metrics/tokens/by-subject", s.handleObservability)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/by-subject", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != metricsRequiredMessage {
		t.Errorf("error = %q, want %q", body.Error, metricsRequiredMessage)
	}
}

// --- caching across token endpoints -----------------------------------------

func TestTokensSummary_Caches(t *testing.T) {
	var calls atomic.Int32
	srv := fakePromServer(t, func(_ string, _ url.Values) interface{} {
		calls.Add(1)
		return vectorResponse([]vectorSample{{Labels: map[string]string{}, Value: "1"}})
	})
	defer srv.Close()

	s := newServerWithTokensProm(t, prometheus.NewClient(srv.URL, nil))

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/summary", nil)
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: expected 200, got %d", i, rec.Code)
		}
	}
	// 2 queries on the first call (all token types in one grouped query, plus
	// the prior window) and one for the cost series; none thereafter.
	if got := calls.Load(); got != 3 {
		t.Errorf("expected 2 prometheus calls (cached after first), got %d", got)
	}
}

// --- by-event ----------------------------------------------------------------

func TestTokensByEvent_Returns503WhenTaskLogsNotConfigured(t *testing.T) {
	s := newServerWithTokensProm(t, prometheus.NewClient("http://localhost:0", nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/by-event?range=24h", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTokensByEvent_RejectsUnknownRange(t *testing.T) {
	s := newServerWithTokensProm(t, prometheus.NewClient("http://localhost:0", nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/by-event?range=42y", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestTokensByEvent_DefaultRangeIs24h(t *testing.T) {
	// Without taskLogs configured, the handler returns 503 regardless of range.
	// This test verifies the route is reachable and the default range doesn't
	// cause a 400 (it would if the range validation ran before the taskLogs check).
	s := newServerWithTokensProm(t, prometheus.NewClient("http://localhost:0", nil))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/by-event", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	// 503 because taskLogs is nil, not 400 for range — confirms default range is accepted.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- aggregateTokensByEvent (unit tests) ------------------------------------

// fakeInvocationGetter is a test double for invocationGetter.
type fakeInvocationGetter struct {
	data map[string]invocations.Invocation
}

func (f *fakeInvocationGetter) Get(id string) (invocations.Invocation, bool) {
	inv, ok := f.data[id]
	return inv, ok
}

func TestAggregateTokensByEvent_AggregatesAndSorts(t *testing.T) {
	// Two invocations map to event "ev-a", one to "ev-b". One invocation has
	// no matching event (evicted). One invocation maps to an invocation with
	// empty EventID (should be skipped).
	store := &fakeInvocationGetter{
		data: map[string]invocations.Invocation{
			"inv-1": {ID: "inv-1", EventID: "ev-a"},
			"inv-2": {ID: "inv-2", EventID: "ev-a"},
			"inv-3": {ID: "inv-3", EventID: "ev-b"},
			"inv-4": {ID: "inv-4", EventID: ""}, // empty EventID → skip
		},
	}

	rows := []tasklogs.InvocationTokenRow{
		{InvocationID: "inv-1", InputTokens: 100, OutputTokens: 50, CacheReadTokens: 4000, CacheWriteTokens: 200, CostUSD: 0.1},
		{InvocationID: "inv-2", InputTokens: 200, OutputTokens: 80, CacheReadTokens: 1000, CostUSD: 0.025},
		{InvocationID: "inv-3", InputTokens: 500, OutputTokens: 150},
		{InvocationID: "inv-4", InputTokens: 999, OutputTokens: 999}, // skipped (empty EventID)
		{InvocationID: "inv-5", InputTokens: 10, OutputTokens: 20},   // skipped (not in store)
	}

	result := aggregateTokensByEvent(rows, store)

	// Should have 2 rows: ev-a and ev-b, sorted alphabetically.
	if len(result) != 2 {
		t.Fatalf("expected 2 rows, got %d: %+v", len(result), result)
	}
	if result[0].Event != "ev-a" || result[1].Event != "ev-b" {
		t.Errorf("rows not sorted by event ID: %+v", result)
	}

	// ev-a: inv-1 + inv-2. The cache components ride along in the per-event
	// total: an event whose cached prompt dominates must not read as its
	// uncached remainder (the under-count this closes, issue #281).
	evA := result[0]
	if evA.InputTokens != 300 || evA.OutputTokens != 130 {
		t.Errorf("ev-a uncached tokens wrong: %+v", evA)
	}
	if evA.CacheReadTokens != 5000 || evA.CacheWriteTokens != 200 {
		t.Errorf("ev-a cache tokens wrong: %+v", evA)
	}
	if evA.TotalTokens != 5630 { // 300 + 130 + 5000 + 200
		t.Errorf("ev-a total = %v, want 5630 (all four components)", evA.TotalTokens)
	}
	if evA.CostUSD != 0.125 {
		t.Errorf("ev-a cost = %v, want 0.125", evA.CostUSD)
	}

	// ev-b: inv-3 (500+150) = input 500, output 150, total 650
	evB := result[1]
	if evB.InputTokens != 500 || evB.OutputTokens != 150 || evB.TotalTokens != 650 {
		t.Errorf("ev-b tokens wrong: %+v", evB)
	}
}

func TestAggregateTokensByEvent_EmptyInputReturnsEmptySlice(t *testing.T) {
	store := &fakeInvocationGetter{data: map[string]invocations.Invocation{}}
	result := aggregateTokensByEvent(nil, store)
	if len(result) != 0 {
		t.Errorf("expected empty result, got %+v", result)
	}
}

func TestAggregateTokensByEvent_SkipsInvocationWithNoEventID(t *testing.T) {
	store := &fakeInvocationGetter{
		data: map[string]invocations.Invocation{
			"inv-1": {ID: "inv-1", EventID: ""},
		},
	}
	rows := []tasklogs.InvocationTokenRow{
		{InvocationID: "inv-1", InputTokens: 100, OutputTokens: 50},
	}
	result := aggregateTokensByEvent(rows, store)
	if len(result) != 0 {
		t.Errorf("expected empty result (no valid event IDs), got %+v", result)
	}
}
