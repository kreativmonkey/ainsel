// Package tasklogs provides storage and retrieval of structured log entries
// published by agents during task execution. Entries are persisted in
// Postgres so the hub's observability endpoints work without an external
// log backend required.
package tasklogs

import "time"

// Level values for a log entry.
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Entry is a single structured log line published by an agent.
type Entry struct {
	ID            int64          `json:"id"`
	InvocationID  string         `json:"invocationId,omitempty"`
	CorrelationID string         `json:"correlationId,omitempty"`
	AgentName     string         `json:"agentName"`
	Level         string         `json:"level"`
	Message       string         `json:"message"`
	Fields        map[string]any `json:"fields,omitempty"`
	CreatedAt     time.Time      `json:"createdAt"`
}

// ListOptions filters and paginates List results.
type ListOptions struct {
	AgentName string
	// AgentNames restricts results to any of these agents. Used to scope the
	// log view to the agents a caller may read; an empty slice applies no
	// restriction, so callers that mean "no agents at all" must not query.
	AgentNames []string
	Level      string
	Since      time.Time
	Until      time.Time
	Limit      int
}

// TokenSubjectUsageRow is one (agent_name, repo, event_type, model) group of
// the four token components pi reports plus the priced cost, summed over a
// window. The accounting rule is TokenUsageRow's: callers that need a single
// figure sum all four components (see api.tokenTotal).
//
// Rows written before migration 0024 carry no subject — Repo and EventType
// are "" there. They are still consumption, so readers must render their
// subject as unknown rather than drop them, or the table stops adding up
// against the token summary over the same window.
//
// CacheReported and CostReported say whether any row in the group carried a
// *reported* figure (issue #75): a group whose cache columns were all NULL —
// an un-caching provider, or a pre-migration row — reads CacheReported false,
// and readers must render the summed cache figures as "not reported", never
// as a measured 0. A reported zero counts towards the flag, so a provider
// that genuinely reports cached_tokens: 0 stays a real 0%.
type TokenSubjectUsageRow struct {
	AgentName        string
	Repo             string
	EventType        string
	Model            string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CostUSD          float64
	// CacheReported is true when at least one row in the group had a non-NULL
	// cache column; CostReported likewise for cost_usd (issue #75). The two
	// cache columns are deliberately one presence group rather than two
	// flags: the flag answers "did the runtime report cache figures?", and
	// readers render both cache cells from it. A provider that reports only
	// one component marks the pair reported, and the other column then reads
	// as a measured zero — accepted because the alternative (per-column
	// flags) would double every presence claim in the API for a case no
	// known provider produces.
	CacheReported bool
	CostReported  bool
}

// ConversationListOptions filters conversation message queries.
type ConversationListOptions struct {
	// AgentName restricts results to a single agent.
	AgentName string
	// AgentNames restricts results to any of these agents, for scoping to the
	// agents a caller may read. Empty applies no restriction.
	AgentNames    []string
	InvocationID  string
	CorrelationID string
	// Limit caps the number of messages returned. <= 0 means the default.
	Limit int
}

// ConversationMessage is a single message in an agent conversation,
// captured from the pi RPC event stream (assistant responses).
//
// The four token fields mirror the components pi reports (usage.input
// excludes prompt-cache traffic; totalTokens is the sum of all four), so a
// window summed from these rows is comparable with the Prometheus
// agent_tokens_used_total figures.
//
// The cache and cost fields are pointers because absence and a measured zero
// are different facts (issue #75): nil means the runtime never reported the
// figure — an un-caching provider, an unpriced model, a user/toolResult row
// with no usage at all — and is stored as NULL, while a non-nil zero is a
// run that genuinely had no cache hits or cost nothing. Readers must render
// NULL-backed sums as "not reported", never as 0. Migration 0025 backfills
// the ambiguous zeros written by older runtimes to NULL, so pre-migration
// rows read as not reported too.
type ConversationMessage struct {
	ID               int64  `json:"id"`
	InvocationID     string `json:"invocationId,omitempty"`
	CorrelationID    string `json:"correlationId,omitempty"`
	AgentName        string `json:"agentName"`
	Role             string `json:"role"`
	Content          string `json:"content"`
	Model            string `json:"model,omitempty"`
	InputTokens      int    `json:"inputTokens,omitempty"`
	OutputTokens     int    `json:"outputTokens,omitempty"`
	CacheReadTokens  *int   `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens *int   `json:"cacheWriteTokens,omitempty"`
	// CostUSD is pi's usage.cost.total for this message, priced from the
	// model's cost block in the generated models.json (spec.llm.cost). nil
	// means not reported — the model is unpriced or the runtime predates cost
	// reporting — while a non-nil zero is a measured free run (issue #75).
	CostUSD    *float64 `json:"costUsd,omitempty"`
	StopReason string   `json:"stopReason,omitempty"`
	// Subject labels the token tables group by (issue #410): the normalised
	// event context the runner held when the turn ran, reported on the same
	// payload as the token components so the counter and the row cannot
	// disagree. Rows written before migration 0024 keep the zero values —
	// readers must render those as "unknown", never drop the row.
	Repo        string    `json:"repo,omitempty"`
	Org         string    `json:"org,omitempty"`
	EventType   string    `json:"eventType,omitempty"`
	IssueNumber int       `json:"issueNumber,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// TokenUsageByIssueFilter selects rows for Store.TokenUsageByIssue. Zero
// values mean "no restriction" — except IssueNumber, which is a pointer
// because issue_number = 0 is a real value (rows whose subject was unknown
// when written, pre-migration-0024) and must never double as the no-filter
// marker.
type TokenUsageByIssueFilter struct {
	// Since/Until bound the window as [since, until); a zero bound is
	// unbounded, which reads "everything the store still retains" — the
	// conversations table has its own retention loop (ConversationRetention),
	// the same semantics TokenUsageSince documents.
	Since time.Time
	Until time.Time
	// AgentName and Repo are exact matches; "" applies no filter.
	AgentName string
	Repo      string
	// IssueNumber is an exact match; nil applies no filter.
	IssueNumber *int
}

// TokenIssueUsageRow is one (agent_name, repo, issue_number, model) group of
// the four token components pi reports plus the priced cost, summed over a
// window. The accounting rule is TokenUsageRow's: callers that need a single
// figure sum all four components (see api.tokenTotal).
//
// Rows written before migration 0024 carry no subject — Repo is "" and
// IssueNumber is 0 there. They are still consumption, so readers must render
// their subject as unknown rather than drop them, or the rows stop adding up
// against the token summary over the same window.
//
// CacheReported and CostReported follow TokenSubjectUsageRow's rule: true
// when at least one row in the group carried a reported (non-NULL) figure,
// so a summed zero backed by NULLs reads as "not reported", never as a
// measured 0 (issue #75).
type TokenIssueUsageRow struct {
	AgentName        string
	Repo             string
	IssueNumber      int
	Model            string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CostUSD          float64
	CacheReported    bool
	CostReported     bool
}
