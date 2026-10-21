package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	pgcontainer "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/ainsel-project/ainsel/services/hub/internal/db"
	"github.com/ainsel-project/ainsel/services/hub/internal/invocations"
	"github.com/ainsel-project/ainsel/services/hub/internal/prometheus"
	"github.com/ainsel-project/ainsel/services/hub/internal/tasklogs"
	"github.com/ainsel-project/ainsel/services/hub/internal/telemetry"
)

// iptr/fptr build ConversationMessage pointer fields: nil cache/cost means
// "not reported" (issue #75), so tests must be able to tell nil from &0.
func iptr(v int) *int         { return &v }
func fptr(v float64) *float64 { return &v }

// obsDBServer boots a Postgres testcontainer, migrates it, and returns a server
// wired with the hub's own telemetry and task-log stores but **no Prometheus** —
// the shape a standalone install actually runs in. The pool is returned so a
// test can seed raw rows. Skips when Docker is unavailable, like the other
// integration suites here.
func obsDBServer(t *testing.T, paths ...string) (*Server, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	c, err := pgcontainer.Run(ctx, "postgres:17-alpine",
		pgcontainer.WithDatabase("ainsel_test"),
		pgcontainer.WithUsername("test"),
		pgcontainer.WithPassword("test"),
		pgcontainer.BasicWaitStrategies(),
	)
	if err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = c.Terminate(ctx)
		t.Fatalf("connection string: %v", err)
	}
	if err := db.Migrate(ctx, dsn); err != nil {
		_ = c.Terminate(ctx)
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		_ = c.Terminate(ctx)
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_ = c.Terminate(context.Background())
	})

	s := testServer(t)
	s.SetTelemetryStore(telemetry.NewStore(pool))
	s.SetTaskLogStore(tasklogs.NewStore(pool))
	for _, p := range paths {
		s.mux.HandleFunc(p, s.handleObservability)
	}
	return s, pool
}

func obsSeedEvent(t *testing.T, pool *pgxpool.Pool, id string, at time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO events (id, connector, headers, data, raw, received_at)
		 VALUES ($1, 'test-connector', '{}', '{}', '', $2)`, id, at); err != nil {
		t.Fatalf("seed event %s: %v", id, err)
	}
}

func obsSeedTask(t *testing.T, pool *pgxpool.Pool, eventID, agent string, at time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO agent_tasks (event_id, agent_name, trigger_name, headers, payload, created_at)
		 VALUES ($1, $2, 'test-trigger', '{}', '{}', $3)`, eventID, agent, at); err != nil {
		t.Fatalf("seed task: %v", err)
	}
}

func obsSeedErrorLog(t *testing.T, pool *pgxpool.Pool, at time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO task_logs (agent_name, level, message, created_at)
		 VALUES ('agent-a', 'error', 'boom', $1)`, at); err != nil {
		t.Fatalf("seed error log: %v", err)
	}
}

// --- summary from the hub's own records ---

func TestObservability_SummaryReadsPostgresWhenPromMissing(t *testing.T) {
	s, pool := obsDBServer(t, "/api/v1/observability/metrics/summary")

	// The range-less call reports everything the hub still holds.
	obsSeedEvent(t, pool, "pg-sum-1", time.Now().UTC())
	obsSeedTask(t, pool, "pg-sum-1", "agent-a", time.Now().UTC())
	obsSeedErrorLog(t, pool, time.Now().UTC())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/summary", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from the hub's own records, got %d: %s", rec.Code, rec.Body.String())
	}
	var body MetricsSummary
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.EventsConsumed != 1 || body.TriggersMatched != 1 || body.EventsRouted != 1 {
		t.Errorf("unexpected summary from postgres: %+v", body)
	}
	if body.RoutingErrors != 1 {
		t.Errorf("expected 1 error from task_logs, got %v", body.RoutingErrors)
	}
	if body.Source != metricsSourcePostgres {
		t.Errorf("expected source %q, got %q", metricsSourcePostgres, body.Source)
	}
}

func TestObservability_SummaryPrefersRecordsOverPrometheus(t *testing.T) {
	// Both backends configured — the shape of an install that set
	// observability.prometheus.url. The hub's own rows must answer: the counters
	// are a copy of the same facts, and the copy restarts at zero with the pod
	// while these rows do not. The fake returns a value no seeded row can produce,
	// so a 999 anywhere in the body means Prometheus was consulted.
	s, pool := obsDBServer(t, "/api/v1/observability/metrics/summary")
	prom, hits := countingProm(t, "999")
	s.prom = prom

	obsSeedEvent(t, pool, "pg-pref-1", time.Now().UTC())
	obsSeedTask(t, pool, "pg-pref-1", "agent-a", time.Now().UTC())
	obsSeedErrorLog(t, pool, time.Now().UTC())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/summary", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from the hub's own records, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("Prometheus was queried %d times; the records must answer a hub that has both", n)
	}
	var body MetricsSummary
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Source != metricsSourcePostgres {
		t.Errorf("source = %q, want %q", body.Source, metricsSourcePostgres)
	}
	if body.EventsConsumed == 999 || body.TriggersMatched == 999 || body.EventsRouted == 999 {
		t.Errorf("counter figures reached the response body: %+v", body)
	}
	if body.EventsConsumed != 1 || body.TriggersMatched != 1 || body.EventsRouted != 1 || body.RoutingErrors != 1 {
		t.Errorf("unexpected figures from the records: %+v", body)
	}
}

func TestObservability_SummaryWithRangeCountsWindow(t *testing.T) {
	s, pool := obsDBServer(t, "/api/v1/observability/metrics/summary")

	now := time.Now().UTC()
	// One event inside the 1h window, one two hours old.
	obsSeedEvent(t, pool, "pg-win-in", now.Add(-10*time.Minute))
	obsSeedEvent(t, pool, "pg-win-out", now.Add(-2*time.Hour))
	obsSeedTask(t, pool, "pg-win-in", "agent-a", now.Add(-9*time.Minute))
	obsSeedErrorLog(t, pool, now.Add(-5*time.Minute))
	obsSeedErrorLog(t, pool, now.Add(-30*time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/summary?range=1h", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body MetricsSummary
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.EventsConsumed != 1 {
		t.Errorf("eventsConsumed = %v, want 1 (the 2h-old event must be outside the window)", body.EventsConsumed)
	}
	if body.TriggersMatched != 1 || body.EventsRouted != 1 {
		t.Errorf("unexpected task counts: %+v", body)
	}
	if body.RoutingErrors != 1 {
		t.Errorf("routingErrors = %v, want 1 (the 30h-old error is outside 1h)", body.RoutingErrors)
	}
}

func TestObservability_SummaryReturns503WithNoBackendAtAll(t *testing.T) {
	// Neither Prometheus nor the hub's own records. The console still needs a
	// 503 to label, which is the contract its unavailable state depends on.
	// testServer wires no stores, so that is exactly this state.
	s := testServer(t)
	s.mux.HandleFunc("/api/v1/observability/metrics/summary", s.handleObservability)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/summary", nil)
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
		t.Errorf("expected %q, got %q", metricsRequiredMessage, body.Error)
	}
}

func TestObservability_StoreWithNoDatabaseIsNotAMetricsBackend(t *testing.T) {
	// NewStore accepts a nil pool, so a wired-but-database-less store is a
	// non-nil pointer that can answer nothing. It must not be chosen as the
	// backend: the caller gets the same 503 as a hub with no backend at all,
	// rather than a 502 from a query that could never run. No container needed —
	// the missing pool is the point.
	s := testServer(t)
	s.SetTelemetryStore(telemetry.NewStore(nil))
	s.mux.HandleFunc("/api/v1/observability/metrics/summary", s.handleObservability)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/summary", nil)
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
		t.Errorf("expected %q, got %q", metricsRequiredMessage, body.Error)
	}
}

// --- timeseries from the hub's own records ---

func TestObservability_TimeseriesFromPostgresIsZeroFilled(t *testing.T) {
	s, pool := obsDBServer(t, "/api/v1/observability/metrics/timeseries")

	now := time.Now().UTC().Truncate(time.Minute)
	// Three events at one instant. The 1h range steps at 30s, so the response
	// must be 120 points whose sum equals what was seeded.
	for _, id := range []string{"pg-ts-a", "pg-ts-b", "pg-ts-c"} {
		obsSeedEvent(t, pool, id, now)
	}

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/observability/metrics/timeseries?metric=events_consumed&range=1h", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body MetricsTimeseries
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Source != metricsSourcePostgres {
		t.Errorf("expected source %q, got %q", metricsSourcePostgres, body.Source)
	}
	// Dense output is what the chart requires: bars are placed by index, so a
	// sparse response would draw three bars spread across the whole hour and
	// misreport when they happened.
	if len(body.Points) != 120 {
		t.Fatalf("expected 120 dense points for a 1h/30s window, got %d", len(body.Points))
	}
	var sum float64
	nonZero := []int{}
	for i, p := range body.Points {
		sum += p.Value
		if p.Value > 0 {
			nonZero = append(nonZero, i)
		}
	}
	if sum != 3 {
		t.Errorf("points sum = %v, want 3", sum)
	}
	// Exactly one bucket carries the seeded events, and it is the one that
	// actually contains them. Which bucket that is gets derived from the grid the
	// response returned, not assumed to be the last one: the handler takes its own
	// `end` some time after `now` is captured, so the events land in whichever of
	// the final buckets the seeded instant really falls in.
	if len(nonZero) != 1 {
		t.Fatalf("expected exactly one populated bucket, got %v", nonZero)
	}
	populated := body.Points[nonZero[0]]
	if populated.Timestamp.After(now) || now.Sub(populated.Timestamp) >= 30*time.Second {
		t.Errorf("populated bucket %d starts at %s, want the 30s bucket containing %s",
			nonZero[0], populated.Timestamp, now)
	}
}

func TestObservability_TimeseriesErrorsFromPostgres(t *testing.T) {
	s, pool := obsDBServer(t, "/api/v1/observability/metrics/timeseries")

	now := time.Now().UTC().Truncate(time.Minute)
	obsSeedErrorLog(t, pool, now.Add(-time.Minute))
	obsSeedErrorLog(t, pool, now.Add(-2*time.Minute))

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/observability/metrics/timeseries?metric=routing_errors&range=1h", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body MetricsTimeseries
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var sum float64
	for _, p := range body.Points {
		sum += p.Value
	}
	if sum != 2 {
		t.Errorf("error points sum = %v, want 2", sum)
	}
}

func TestObservability_TimeseriesRejectsMetricWithNoRecords(t *testing.T) {
	// The metric registry is shared between backends: a name the hub's records
	// cannot answer must not quietly return an empty chart.
	s, _ := obsDBServer(t, "/api/v1/observability/metrics/timeseries")

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/observability/metrics/timeseries?metric=%s&range=1h", "hub_something_new"), nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown metric, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- panels that never needed Prometheus must stop asking for it ---

func TestObservability_TokensByEventServesWithoutProm(t *testing.T) {
	// This panel reads task_conversations through the hub's log store. It used to
	// sit behind the Prometheus gate, so it 503'd on every standalone install
	// for a dependency it does not have.
	s, _ := obsDBServer(t, "/api/v1/observability/metrics/tokens/by-event")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/by-event?range=24h", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from the hub's own records, got %d: %s", rec.Code, rec.Body.String())
	}
	var body TokensByEvent
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Range != "24h" {
		t.Errorf("unexpected envelope: %+v", body)
	}
}

// --- tokens from the hub's own records (issue #281) --------------------------

// seedConversation persists one conversation row through the hub's store, the
// way the ingest handler does — so the cache columns travel the same path a
// runtime's payload travels in production.
func seedConversation(t *testing.T, store *tasklogs.Store, m *tasklogs.ConversationMessage) {
	t.Helper()
	if err := store.InsertConversation(context.Background(), m); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
}

func TestObservability_TokenSummaryServedFromRecords(t *testing.T) {
	// Both backends configured — the shape of an install that set
	// observability.prometheus.url. The conversation rows answer: they are
	// written the moment a turn ends, while the Prometheus series depend on an
	// agent pod surviving long enough to be scraped. The fake's value is one no
	// seeded row can produce, so 999 anywhere means Prometheus was consulted.
	s, _ := obsDBServer(t, "/api/v1/observability/metrics/tokens/summary")
	prom, hits := countingProm(t, "999")
	s.prom = prom

	// total = (100+1) + (20+2) + 8000 + 500 = 8623. An input/output-only sum
	// — what the records gave before migration 0022 — would report 123.
	seedConversation(t, s.taskLogs, &tasklogs.ConversationMessage{
		InvocationID: "inv-1", AgentName: "olli", Role: "assistant", Content: "[]",
		InputTokens: 100, OutputTokens: 20, CacheReadTokens: iptr(8000), CacheWriteTokens: iptr(500),
		CostUSD: fptr(0.0345),
	})
	seedConversation(t, s.taskLogs, &tasklogs.ConversationMessage{
		InvocationID: "inv-2", AgentName: "olli", Role: "assistant", Content: "[]",
		InputTokens: 1, OutputTokens: 2,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/summary?range=24h", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from the hub's own records, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("Prometheus was queried %d times; the records must answer a hub that has both", n)
	}
	var body TokensSummary
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Source != metricsSourcePostgres {
		t.Errorf("source = %q, want %q", body.Source, metricsSourcePostgres)
	}
	if body.TotalTokens != 8623 {
		t.Errorf("totalTokens = %v, want 8623 (all four components, cache included)", body.TotalTokens)
	}
	if body.CacheReadTokens != 8000 || body.CacheWriteTokens != 500 {
		t.Errorf("cache components wrong: %+v", body)
	}
	if body.CostUSD != 0.0345 {
		t.Errorf("costUsd = %v, want 0.0345 (summed from the conversation rows)", body.CostUSD)
	}
	// The first seeded row reported cache and cost, so the window reads
	// "reported" even though the second row reported neither (issue #75).
	if !body.CacheReported || !body.CostReported {
		t.Errorf("presence flags = %v/%v, want true/true", body.CacheReported, body.CostReported)
	}
	if body.PreviousTotalTokens != 0 {
		t.Errorf("previousTotalTokens = %v, want 0 (nothing older than the window was seeded)", body.PreviousTotalTokens)
	}
}

func TestObservability_TokenSummaryPresenceFlagsWhenSilent(t *testing.T) {
	// Issue #75: a window whose rows never carried a cache or cost figure —
	// user/toolResult rows plus an assistant row from a provider that reports
	// neither — must read cacheReported/costReported false. The summed zeros
	// are placeholders there; rendering them as a measured 0% / $0.00 is the
	// bug this pins.
	s, _ := obsDBServer(t, "/api/v1/observability/metrics/tokens/summary")
	seedConversation(t, s.taskLogs, &tasklogs.ConversationMessage{
		InvocationID: "inv-silent", AgentName: "olli", Role: "user", Content: "[]",
		InputTokens: 10,
	})
	seedConversation(t, s.taskLogs, &tasklogs.ConversationMessage{
		InvocationID: "inv-silent", AgentName: "olli", Role: "toolResult", Content: "[]",
		OutputTokens: 4,
	})
	seedConversation(t, s.taskLogs, &tasklogs.ConversationMessage{
		InvocationID: "inv-silent", AgentName: "olli", Role: "assistant", Content: "[]",
		InputTokens: 100, OutputTokens: 20,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/summary?range=24h", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body TokensSummary
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.TotalTokens != 134 {
		t.Errorf("totalTokens = %v, want 134 (input/output still count)", body.TotalTokens)
	}
	if body.CacheReported {
		t.Errorf("cacheReported = true, want false: no row in the window reported a cache figure")
	}
	if body.CostReported {
		t.Errorf("costReported = true, want false: no row in the window reported a cost")
	}
}

func TestObservability_TokenSummaryUsesPrometheusWhenPinned(t *testing.T) {
	// The pin is an escape hatch, not a suggestion: with records available and
	// metricsSource=prometheus, the counter path must still answer — including
	// for installs deliberately comparing the two backends.
	s, _ := obsDBServer(t, "/api/v1/observability/metrics/tokens/summary")
	prom, hits := countingProm(t, "7")
	s.prom = prom
	s.SetMetricsSource(MetricsSourcePrometheus)

	seedConversation(t, s.taskLogs, &tasklogs.ConversationMessage{
		InvocationID: "inv-1", AgentName: "olli", Role: "assistant", Content: "[]",
		InputTokens: 100, OutputTokens: 20,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/summary?range=24h", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from the pinned backend, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := hits.Load(); n == 0 {
		t.Error("pinned to prometheus but no query reached it")
	}
	var body TokensSummary
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Source != metricsSourcePrometheus {
		t.Errorf("source = %q, want %q", body.Source, metricsSourcePrometheus)
	}
}

func TestObservability_TokenTimeseriesServedFromRecordsIsZeroFilled(t *testing.T) {
	s, _ := obsDBServer(t, "/api/v1/observability/metrics/tokens/timeseries")

	seedConversation(t, s.taskLogs, &tasklogs.ConversationMessage{
		InvocationID: "inv-1", AgentName: "olli", Role: "assistant", Content: "[]",
		InputTokens: 100, OutputTokens: 20, CacheReadTokens: iptr(8000), CacheWriteTokens: iptr(500),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/timeseries?range=24h", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from the hub's own records, got %d: %s", rec.Code, rec.Body.String())
	}
	var body TokensTimeseries
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Source != metricsSourcePostgres {
		t.Errorf("source = %q, want %q", body.Source, metricsSourcePostgres)
	}
	// Dense like the event timeseries: the sparkline places points by index,
	// so two populated buckets must not become a two-point response.
	if len(body.Points) < 48 || len(body.Points) > 49 {
		t.Fatalf("expected 48 or 49 dense points for a 24h/30m window, got %d", len(body.Points))
	}
	var sum float64
	populated := 0
	for _, p := range body.Points {
		sum += p.Value
		if p.Value > 0 {
			populated++
		}
	}
	if sum != 8620 {
		t.Errorf("points sum = %v, want 8620 (100+20+8000+500, the cached volume included)", sum)
	}
	if populated != 1 {
		t.Errorf("expected exactly one populated bucket, got %d", populated)
	}
}

func TestObservability_StatsTokensServedFromRecords(t *testing.T) {
	// The dashboard's top-line tile follows the same rule as the panels: with a
	// database present the conversation ledger answers, and a Prometheus that
	// is also configured is not consulted.
	s, _ := obsDBServer(t)
	prom, hits := countingProm(t, "999")
	s.prom = prom

	seedConversation(t, s.taskLogs, &tasklogs.ConversationMessage{
		InvocationID: "inv-1", AgentName: "olli", Role: "assistant", Content: "[]",
		InputTokens: 100, OutputTokens: 20, CacheReadTokens: iptr(8000), CacheWriteTokens: iptr(500),
	})

	stats := s.GetStats(context.Background())
	if n := hits.Load(); n != 0 {
		t.Errorf("Prometheus was queried %d times; the records must answer stats too", n)
	}
	if stats.Tokens.TotalTokens != 8620 {
		t.Errorf("stats tokens total = %v, want 8620 (all four components)", stats.Tokens.TotalTokens)
	}
	if stats.Tokens.CacheReadTokens != 8000 {
		t.Errorf("stats cacheReadTokens = %v, want 8000", stats.Tokens.CacheReadTokens)
	}
	// The presence flags ride the same records path: the seed row reports
	// cache but no cost, so cache must read reported and cost must not
	// (issue #75).
	if !stats.Tokens.CacheReported {
		t.Errorf("stats cacheReported = false, want true (seeded row reports cache)")
	}
	if stats.Tokens.CostReported {
		t.Errorf("stats costReported = true, want false (seeded row carries no cost)")
	}
}

func TestObservability_TokenPanelsWithNoBackendAtAll(t *testing.T) {
	// Neither Prometheus nor a database: the token panels get the same 503 the
	// event panels give, naming both fixes — since #411 a database lights up
	// by-subject too, so no token panel names Prometheus alone any more.
	s := testServer(t)
	s.mux.HandleFunc("/api/v1/observability/metrics/tokens/summary", s.handleObservability)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/summary", nil)
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
		t.Errorf("expected %q, got %q", metricsRequiredMessage, body.Error)
	}
}

// --- by-subject from the hub's own records (issue #411) ---

func TestObservability_TokensBySubjectServedFromRecordsWithoutProm(t *testing.T) {
	// The headline acceptance: a hub with a database and no Prometheus answers
	// the by-subject table from its own conversation rows — the shape the two
	// token tables render on a default install.
	s, _ := obsDBServer(t,
		"/api/v1/observability/metrics/tokens/by-subject",
		"/api/v1/observability/metrics/tokens/summary",
	)

	// Same subject, two turns: must collapse into one row. Cost rides along —
	// without it the records-backed table would read 0 while the summary tile
	// on the same page reports a non-zero cost.
	seedConversation(t, s.taskLogs, &tasklogs.ConversationMessage{
		InvocationID: "inv-1", AgentName: "olli", Role: "assistant", Content: "[]",
		Model: "kimi", Repo: "org/repo", EventType: "pull_request.opened",
		InputTokens: 100, OutputTokens: 20, CacheReadTokens: iptr(8000), CacheWriteTokens: iptr(500),
		CostUSD: fptr(0.0345),
	})
	seedConversation(t, s.taskLogs, &tasklogs.ConversationMessage{
		InvocationID: "inv-1", AgentName: "olli", Role: "assistant", Content: "[]",
		Model: "kimi", Repo: "org/repo", EventType: "pull_request.opened",
		InputTokens: 1, OutputTokens: 2,
	})
	seedConversation(t, s.taskLogs, &tasklogs.ConversationMessage{
		InvocationID: "inv-2", AgentName: "olli", Role: "assistant", Content: "[]",
		Model: "gpt", Repo: "other/repo", EventType: "issue.assigned",
		InputTokens: 500, OutputTokens: 100,
	})
	// Pre-migration row: no subject. It is still consumption — it must render
	// (as "") rather than be dropped, or the table stops adding up against the
	// summary tile over the same window.
	seedConversation(t, s.taskLogs, &tasklogs.ConversationMessage{
		InvocationID: "inv-3", AgentName: "olli", Role: "assistant", Content: "[]",
		Model: "kimi", InputTokens: 40, OutputTokens: 8,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/by-subject?range=24h", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from the hub's own records, got %d: %s", rec.Code, rec.Body.String())
	}
	var body TokensBySubject
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Source != metricsSourcePostgres {
		t.Errorf("source = %q, want %q", body.Source, metricsSourcePostgres)
	}
	if len(body.Rows) != 3 {
		t.Fatalf("expected 3 groups, got %d: %+v", len(body.Rows), body.Rows)
	}
	// Sorted by (repo, event_type, agent): the empty subject first, not dropped.
	if body.Rows[0].Repo != "" || body.Rows[0].TotalTokens != 48 {
		t.Errorf("pre-migration row wrong (must group under \"\"): %+v", body.Rows[0])
	}
	if body.Rows[2].Repo != "other/repo" || body.Rows[2].EventType != "issue.assigned" || body.Rows[2].Model != "gpt" {
		t.Errorf("second group mislabeled: %+v", body.Rows[2])
	}
	if body.Rows[1].Repo != "org/repo" || body.Rows[1].EventType != "pull_request.opened" || body.Rows[1].Model != "kimi" {
		t.Errorf("main group mislabeled: %+v", body.Rows[1])
	}
	main := body.Rows[1]
	if main.InputTokens != 101 || main.OutputTokens != 22 || main.CacheReadTokens != 8000 || main.CacheWriteTokens != 500 {
		t.Errorf("main group components = %+v, want 101/22/8000/500", main)
	}
	if main.TotalTokens != 8623 {
		t.Errorf("main total = %v, want 8623 (all four components)", main.TotalTokens)
	}
	if main.CostUSD != 0.0345 {
		t.Errorf("main cost = %v, want 0.0345", main.CostUSD)
	}
	// Records rows already carry the agent name; no K8s lookup is needed (and
	// none of these agents exist in the fake client).
	if main.Agent != "olli" || main.AgentName != "olli" {
		t.Errorf("agent identity = %q/%q, want olli/olli", main.Agent, main.AgentName)
	}

	// The acceptance invariant: same fixture, same window, same table — the
	// table's total must equal the summary tile's total.
	var tableTotal float64
	for _, r := range body.Rows {
		tableTotal += r.TotalTokens
	}
	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/summary?range=24h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("summary: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var summary TokensSummary
	if err := json.NewDecoder(rec.Body).Decode(&summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if tableTotal != summary.TotalTokens {
		t.Errorf("table total = %v, summary tile total = %v; they must be equal for the same range", tableTotal, summary.TotalTokens)
	}
}

func TestObservability_TokensBySubjectPinnedPrometheusWithoutClientIs503(t *testing.T) {
	// A pin is never silently answered from the other backend — even though
	// this hub has perfectly good records to answer from.
	s, _ := obsDBServer(t, "/api/v1/observability/metrics/tokens/by-subject")
	s.SetMetricsSource(MetricsSourcePrometheus)
	seedConversation(t, s.taskLogs, &tasklogs.ConversationMessage{
		InvocationID: "inv-1", AgentName: "olli", Role: "assistant", Content: "[]",
		InputTokens: 100, OutputTokens: 20,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/observability/metrics/tokens/by-subject?range=24h", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 naming Prometheus, got %d: %s", rec.Code, rec.Body.String())
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

func TestObservability_TokensBySubjectCacheIsSourceScoped(t *testing.T) {
	// The source-scoped cache key is what makes a gate flip safe within the
	// TTL: the records-cached response must not be served under the pinned
	// prometheus source (or vice versa).
	const path = "/api/v1/observability/metrics/tokens/by-subject"
	s, _ := obsDBServer(t, path)
	seedConversation(t, s.taskLogs, &tasklogs.ConversationMessage{
		InvocationID: "inv-1", AgentName: "records-agent", Role: "assistant", Content: "[]",
		Repo: "org/repo", EventType: "issue.opened", Model: "kimi",
		InputTokens: 42, OutputTokens: 8,
	})

	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?range=24h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("records read: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var records TokensBySubject
	if err := json.NewDecoder(rec.Body).Decode(&records); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if records.Source != metricsSourcePostgres || len(records.Rows) != 1 {
		t.Fatalf("unexpected records response: %+v", records)
	}

	promSrv := fakePromServer(t, func(path string, params url.Values) interface{} {
		q := params.Get("query")
		if strings.Contains(q, "agent_tokens_used_total") {
			return vectorResponse([]vectorSample{
				{Labels: map[string]string{"agent": "prom-only", "repo": "p", "event_type": "p", "model": "p", "token_type": "input"}, Value: "7"},
			})
		}
		return vectorResponse(nil)
	})
	defer promSrv.Close()
	s.SetMetricsSource(MetricsSourcePrometheus)
	s.prom = prometheus.NewClient(promSrv.URL, nil)

	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?range=24h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("pinned read: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var pinned TokensBySubject
	if err := json.NewDecoder(rec.Body).Decode(&pinned); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pinned.Source != metricsSourcePrometheus {
		t.Errorf("source = %q after pinning; the records-cached entry leaked across sources", pinned.Source)
	}
	if len(pinned.Rows) != 1 || pinned.Rows[0].Agent != "prom-only" {
		t.Errorf("expected the Prometheus figures, got %+v", pinned.Rows)
	}
}

// --- per-agent metrics from the hub's own records (issue #413) ---

func obsSeedConversation(t *testing.T, pool *pgxpool.Pool, agent string, at time.Time,
	in, out, cacheRead, cacheWrite int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO task_conversations
		 (agent_name, role, content, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, created_at)
		 VALUES ($1, 'assistant', 'done', $2, $3, $4, $5, $6)`,
		agent, in, out, cacheRead, cacheWrite, at); err != nil {
		t.Fatalf("seed conversation for %s: %v", agent, err)
	}
}

func obsSeedInvocation(t *testing.T, pool *pgxpool.Pool, id, agent, status string,
	start time.Time, durationMs *int64) {
	t.Helper()
	var endTime any
	if durationMs != nil {
		endTime = start.Add(time.Duration(*durationMs) * time.Millisecond)
	}
	errMsg := ""
	if status == "failure" || status == "timeout" {
		errMsg = "boom"
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO invocations (id, agent_name, trigger_name, status, error, start_time, end_time, duration_ms)
		 VALUES ($1, $2, 'test-trigger', $3, $4, $5, $6, $7)`,
		id, agent, status, errMsg, start, endTime, durationMs); err != nil {
		t.Fatalf("seed invocation %s: %v", id, err)
	}
}

func obsAgentsServer(t *testing.T, paths ...string) (*Server, *pgxpool.Pool) {
	t.Helper()
	s, pool := obsDBServer(t, paths...)
	// The records path reads invocations through the same pool; obsDBServer
	// leaves the memory store from testServer in place, so wire the real one.
	s.invocations = invocations.NewPgStore(pool)
	return s, pool
}

func TestObservability_AgentsServedFromRecordsWithoutProm(t *testing.T) {
	// The headline acceptance: a hub with no Prometheus at all answers the
	// per-agent table from its own rows. This is the shape of the agents that
	// matter — one-shot task pods that finish between scrapes and would never
	// contribute a counter sample.
	const path = "/api/v1/observability/metrics/agents"
	s, pool := obsAgentsServer(t, path)
	now := time.Now().UTC()
	threeSec := int64(3000)
	fourSec := int64(4000)

	obsSeedConversation(t, pool, "agent-a", now.Add(-10*time.Minute), 100, 10, 500, 50)
	obsSeedConversation(t, pool, "agent-a", now.Add(-9*time.Minute), 20, 5, 0, 0)
	obsSeedInvocation(t, pool, "inv-a1", "agent-a", "success", now.Add(-10*time.Minute), &threeSec)
	obsSeedInvocation(t, pool, "inv-a2", "agent-a", "failure", now.Add(-9*time.Minute), &fourSec)
	obsSeedInvocation(t, pool, "inv-a3", "agent-a", "running", now.Add(-time.Minute), nil)
	// agent-b: dispatched but never produced a conversation (failed dispatch) —
	// it must still appear, via the invocations rollup alone.
	obsSeedInvocation(t, pool, "inv-b1", "agent-b", "timeout", now.Add(-5*time.Minute), nil)

	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with no Prometheus configured, got %d: %s", rec.Code, rec.Body.String())
	}
	var body AgentsMetricsResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Source != metricsSourcePostgres {
		t.Errorf("source = %q, want %q", body.Source, metricsSourcePostgres)
	}
	if len(body.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %+v", body.Agents)
	}
	a, b := body.Agents[0], body.Agents[1]
	if a.Agent != "agent-a" || b.Agent != "agent-b" {
		t.Fatalf("expected alphabetical order, got %q, %q", a.Agent, b.Agent)
	}
	if a.InputTokens != 120 || a.OutputTokens != 15 || a.CacheReadTokens != 500 || a.CacheWriteTokens != 50 {
		t.Errorf("agent-a components = %+v, want 120/15/500/50", a)
	}
	// The total must include the cache components — the classic under-count.
	if a.TotalTokens != 685 {
		t.Errorf("agent-a totalTokens = %v, want 685 (all four components)", a.TotalTokens)
	}
	if a.Invocations != 3 || a.Failures != 1 {
		t.Errorf("agent-a invocations/failures = %v/%v, want 3/1 (all statuses counted; failure+timeout are failures)",
			a.Invocations, a.Failures)
	}
	if a.AvgDurationMs != 3500 {
		t.Errorf("agent-a avgDurationMs = %v, want 3500 (mean over the two completed rows only)", a.AvgDurationMs)
	}
	if b.Invocations != 1 || b.Failures != 1 || b.TotalTokens != 0 {
		t.Errorf("agent-b = %+v, want invocations=1 failures=1 totalTokens=0", b)
	}
}

func TestObservability_AgentsRecordsMatchTokenSummaryTile(t *testing.T) {
	// Cross-view consistency: the per-agent rows and the token summary tile
	// read the same table over the same window, so their totals must agree.
	const agentsPath = "/api/v1/observability/metrics/agents"
	const summaryPath = "/api/v1/observability/metrics/tokens/summary"
	s, pool := obsAgentsServer(t, agentsPath, summaryPath)
	now := time.Now().UTC()

	obsSeedConversation(t, pool, "agent-a", now.Add(-30*time.Minute), 100, 10, 500, 50)
	obsSeedConversation(t, pool, "agent-b", now.Add(-20*time.Minute), 7, 3, 1, 2)

	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, agentsPath+"?range=24h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("agents: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var agents AgentsMetricsResponse
	if err := json.NewDecoder(rec.Body).Decode(&agents); err != nil {
		t.Fatalf("decode agents: %v", err)
	}
	var sum float64
	for _, a := range agents.Agents {
		sum += a.TotalTokens
	}

	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, summaryPath+"?range=24h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("summary: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var tile TokensSummary
	if err := json.NewDecoder(rec.Body).Decode(&tile); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if sum != tile.TotalTokens {
		t.Errorf("per-agent totals sum to %v, token summary tile reports %v — they must agree", sum, tile.TotalTokens)
	}
}

func TestObservability_AgentsRangeWindowsQueries(t *testing.T) {
	const path = "/api/v1/observability/metrics/agents"
	s, pool := obsAgentsServer(t, path)
	now := time.Now().UTC()

	obsSeedConversation(t, pool, "stale", now.Add(-2*time.Hour), 1000, 0, 0, 0)
	obsSeedConversation(t, pool, "fresh", now.Add(-10*time.Minute), 10, 0, 0, 0)
	obsSeedInvocation(t, pool, "inv-stale", "stale", "success", now.Add(-2*time.Hour), nil)
	obsSeedInvocation(t, pool, "inv-fresh", "fresh", "success", now.Add(-10*time.Minute), nil)

	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?range=1h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("range=1h: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var windowed AgentsMetricsResponse
	if err := json.NewDecoder(rec.Body).Decode(&windowed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(windowed.Agents) != 1 || windowed.Agents[0].Agent != "fresh" {
		t.Fatalf("range=1h must exclude the 2h-old rows, got %+v", windowed.Agents)
	}

	// No range = everything retained — the legacy all-time semantics. The
	// range=1h response above sits under a different cache key, so this read
	// is not served from it.
	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var allTime AgentsMetricsResponse
	if err := json.NewDecoder(rec.Body).Decode(&allTime); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(allTime.Agents) != 2 {
		t.Fatalf("range-less read must include both agents, got %+v", allTime.Agents)
	}
}

func TestObservability_AgentsCacheIsSourceScoped(t *testing.T) {
	// A hub that flips its pin within the cache TTL must not serve the other
	// backend's figures under the new source — the reason the key is
	// source-scoped (the old constant "agents" key could).
	const path = "/api/v1/observability/metrics/agents"
	s, pool := obsAgentsServer(t, path)
	obsSeedConversation(t, pool, "agent-a", time.Now().UTC().Add(-time.Minute), 42, 0, 0, 0)

	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("records read: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var records AgentsMetricsResponse
	if err := json.NewDecoder(rec.Body).Decode(&records); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if records.Source != metricsSourcePostgres || len(records.Agents) != 1 {
		t.Fatalf("unexpected records response: %+v", records)
	}

	promSrv := fakePromServer(t, func(path string, params url.Values) interface{} {
		q := params.Get("query")
		if strings.Contains(q, "agent_tokens_used_total") {
			return vectorResponse([]vectorSample{
				{Labels: map[string]string{"agent": "prom-only", "token_type": "input"}, Value: "1"},
			})
		}
		return vectorResponse(nil)
	})
	defer promSrv.Close()
	s.SetMetricsSource(MetricsSourcePrometheus)
	s.prom = prometheus.NewClient(promSrv.URL, nil)

	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("pinned read: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var pinned AgentsMetricsResponse
	if err := json.NewDecoder(rec.Body).Decode(&pinned); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pinned.Source != metricsSourcePrometheus {
		t.Errorf("source = %q after pinning; the records-cached entry leaked across sources", pinned.Source)
	}
	if len(pinned.Agents) != 1 || pinned.Agents[0].Agent != "prom-only" {
		t.Errorf("expected the Prometheus figures, got %+v", pinned.Agents)
	}
}
