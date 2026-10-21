package tasklogs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultRetention is how long log entries are kept before pruning.
const DefaultRetention = 7 * 24 * time.Hour

// ConversationRetention is how long conversation messages are kept.
// Shorter than log retention since conversations are high-volume.
const ConversationRetention = 48 * time.Hour

// Store is the task_logs database layer.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wires a Store against the given pgxpool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Ready reports whether the store can answer anything. NewStore tolerates a
// nil pool so wiring never has to special-case a hub without a database, but a
// store over a nil pool answers nothing. Callers choosing between backends ask
// this instead of asking whether a pointer was set — the same contract
// telemetry.Store.Ready gives the event-metric panels.
func (s *Store) Ready() bool {
	return s != nil && s.pool != nil
}

// Insert persists a single log entry. The entry's ID and CreatedAt fields
// are populated from the database on success.
func (s *Store) Insert(ctx context.Context, e *Entry) error {
	fieldsJSON, err := json.Marshal(e.Fields)
	if err != nil {
		return fmt.Errorf("tasklogs.Insert: marshal fields: %w", err)
	}
	return s.pool.QueryRow(ctx, `
		INSERT INTO task_logs (invocation_id, correlation_id, agent_name, level, message, fields)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at
	`, e.InvocationID, e.CorrelationID, e.AgentName, e.Level, e.Message, fieldsJSON).
		Scan(&e.ID, &e.CreatedAt)
}

// List returns entries newest-first, applying the given filters.
func (s *Store) List(ctx context.Context, opts ListOptions) ([]Entry, error) {
	query := `SELECT id, invocation_id, correlation_id, agent_name, level, message, fields, created_at FROM task_logs WHERE 1=1`
	args := []any{}
	argN := 1

	if opts.AgentName != "" {
		query += fmt.Sprintf(" AND agent_name = $%d", argN)
		args = append(args, opts.AgentName)
		argN++
	}
	if len(opts.AgentNames) > 0 {
		query += fmt.Sprintf(" AND agent_name = ANY($%d)", argN)
		args = append(args, opts.AgentNames)
		argN++
	}
	if opts.Level != "" {
		query += fmt.Sprintf(" AND level = $%d", argN)
		args = append(args, opts.Level)
		argN++
	}
	if !opts.Since.IsZero() {
		query += fmt.Sprintf(" AND created_at >= $%d", argN)
		args = append(args, opts.Since)
		argN++
	}
	if !opts.Until.IsZero() {
		query += fmt.Sprintf(" AND created_at < $%d", argN)
		args = append(args, opts.Until)
		argN++
	}

	query += " ORDER BY created_at DESC"

	limit := opts.Limit
	if limit <= 0 {
		limit = 500
	}
	if limit > 1000 {
		limit = 1000
	}
	query += fmt.Sprintf(" LIMIT $%d", argN)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("tasklogs.List: %w", err)
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		var fieldsJSON []byte
		if err := rows.Scan(&e.ID, &e.InvocationID, &e.CorrelationID,
			&e.AgentName, &e.Level, &e.Message, &fieldsJSON, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("tasklogs.List scan: %w", err)
		}
		if len(fieldsJSON) > 0 {
			if err := json.Unmarshal(fieldsJSON, &e.Fields); err != nil {
				return nil, fmt.Errorf("tasklogs.List unmarshal fields: %w", err)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Prune deletes entries older than the given retention duration.
// Returns the number of deleted rows.
func (s *Store) Prune(ctx context.Context, retention time.Duration) (int64, error) {
	cutoff := time.Now().Add(-retention)
	tag, err := s.pool.Exec(ctx, `DELETE FROM task_logs WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("tasklogs.Prune: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CountByLevelSince returns the number of entries with the given level
// created at or after the given time.
func (s *Store) CountByLevelSince(ctx context.Context, level string, since time.Time) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM task_logs WHERE level = $1 AND created_at >= $2`,
		level, since,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("tasklogs.CountByLevelSince: %w", err)
	}
	return count, nil
}

// InsertConversation persists a single conversation message. Nil cache/cost
// fields are stored as NULL — the encoding that keeps "not reported" distinct
// from a measured zero (issue #75); a non-nil zero is stored as 0.
func (s *Store) InsertConversation(ctx context.Context, m *ConversationMessage) error {
	return s.pool.QueryRow(ctx, `
		INSERT INTO task_conversations
			(invocation_id, correlation_id, agent_name, role, content, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd, stop_reason, repo, org, event_type, issue_number)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING id, created_at
	`, m.InvocationID, m.CorrelationID, m.AgentName, m.Role, m.Content,
		m.Model, m.InputTokens, m.OutputTokens, m.CacheReadTokens, m.CacheWriteTokens, m.CostUSD, m.StopReason,
		m.Repo, m.Org, m.EventType, m.IssueNumber).Scan(&m.ID, &m.CreatedAt)
}

// ListConversations returns conversation messages matching opts, oldest-first
// so a caller can replay a conversation in order. Filter by agent, invocation
// or correlation ID when provided; AgentNames restricts to a set of agents and
// is how the API scopes results to the agents a caller may read.
func (s *Store) ListConversations(ctx context.Context, opts ConversationListOptions) ([]ConversationMessage, error) {
	query := `SELECT id, invocation_id, correlation_id, agent_name, role, content, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd, stop_reason, repo, org, event_type, issue_number, created_at
		FROM task_conversations WHERE 1=1`
	args := []any{}
	argN := 1

	if opts.AgentName != "" {
		query += fmt.Sprintf(" AND agent_name = $%d", argN)
		args = append(args, opts.AgentName)
		argN++
	}
	if len(opts.AgentNames) > 0 {
		query += fmt.Sprintf(" AND agent_name = ANY($%d)", argN)
		args = append(args, opts.AgentNames)
		argN++
	}
	if opts.InvocationID != "" {
		query += fmt.Sprintf(" AND invocation_id = $%d", argN)
		args = append(args, opts.InvocationID)
		argN++
	}
	if opts.CorrelationID != "" {
		query += fmt.Sprintf(" AND correlation_id = $%d", argN)
		args = append(args, opts.CorrelationID)
		argN++
	}

	query += " ORDER BY created_at ASC, id ASC"

	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	query += fmt.Sprintf(" LIMIT $%d", argN)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("tasklogs.ListConversations: %w", err)
	}
	defer rows.Close()

	var out []ConversationMessage
	for rows.Next() {
		var m ConversationMessage
		if err := rows.Scan(&m.ID, &m.InvocationID, &m.CorrelationID,
			&m.AgentName, &m.Role, &m.Content, &m.Model,
			&m.InputTokens, &m.OutputTokens, &m.CacheReadTokens, &m.CacheWriteTokens,
			&m.CostUSD, &m.StopReason, &m.Repo, &m.Org, &m.EventType, &m.IssueNumber,
			&m.CreatedAt); err != nil {
			return nil, fmt.Errorf("tasklogs.ListConversations scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PruneConversations deletes conversation messages older than the given
// retention duration. Returns the number of deleted rows.
func (s *Store) PruneConversations(ctx context.Context, retention time.Duration) (int64, error) {
	cutoff := time.Now().Add(-retention)
	tag, err := s.pool.Exec(ctx, `DELETE FROM task_conversations WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("tasklogs.PruneConversations: %w", err)
	}
	return tag.RowsAffected(), nil
}

// InvocationTokenRow holds token totals grouped by invocation ID.
//
// The cache components are summed alongside input/output because pi reports
// them separately and usage.input excludes them: an invocation total built
// from input+output alone is short by the whole cached volume, which is
// usually the large majority of an agent loop's prompt.
//
// CacheReported/CostReported follow TokenSubjectUsageRow's rule (issue #75):
// true when at least one row in the group carried a reported (non-NULL)
// figure, so a summed zero backed by NULLs reads as "not reported".
type InvocationTokenRow struct {
	InvocationID     string
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	CostUSD          float64
	CacheReported    bool
	CostReported     bool
}

// TokensByInvocationSince returns token totals grouped by invocation_id for
// conversation messages created at or after the given time. Only rows with a
// non-empty invocation_id are returned.
func (s *Store) TokensByInvocationSince(ctx context.Context, since time.Time) ([]InvocationTokenRow, error) {
	query := `SELECT invocation_id, COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(cache_write_tokens), 0),
			COALESCE(SUM(cost_usd), 0),
			COUNT(cache_read_tokens) + COUNT(cache_write_tokens) > 0, COUNT(cost_usd) > 0
		FROM task_conversations
		WHERE invocation_id != ''`
	args := []any{}
	argN := 1

	if !since.IsZero() {
		query += fmt.Sprintf(" AND created_at >= $%d", argN)
		args = append(args, since)
	}

	query += " GROUP BY invocation_id"

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("tasklogs.TokensByInvocationSince: %w", err)
	}
	defer rows.Close()

	var out []InvocationTokenRow
	for rows.Next() {
		var r InvocationTokenRow
		if err := rows.Scan(&r.InvocationID, &r.InputTokens, &r.OutputTokens,
			&r.CacheReadTokens, &r.CacheWriteTokens, &r.CostUSD,
			&r.CacheReported, &r.CostReported); err != nil {
			return nil, fmt.Errorf("tasklogs.TokensByInvocationSince scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TokenUsageRow holds the four token components pi reports, summed over a
// window. The components stay separate so callers can render the same split
// the Prometheus-backed views do; callers that need a single figure sum all
// four (see api.tokenTotal — the accounting rule lives there, not here).
//
// CacheReported/CostReported say whether the window held any row with a
// *reported* (non-NULL) cache/cost figure (issue #75). A window whose rows
// all left the column NULL — an un-caching provider, or pre-migration-0025
// rows whose ambiguous zeros migration 0025 backfilled to NULL — reads
// false, and readers must render the
// summed figures as "not reported", never as a measured 0. A reported zero
// counts as reported, so a provider that genuinely reports cached_tokens: 0
// keeps its real 0.
type TokenUsageRow struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CostUSD          float64
	CacheReported    bool
	CostReported     bool
}

// Total returns the sum of every component. Callers that display a single
// token count must use this rather than adding two fields by hand, which is
// exactly the under-count this split exists to prevent.
func (r TokenUsageRow) Total() float64 {
	return float64(r.InputTokens + r.OutputTokens + r.CacheReadTokens + r.CacheWriteTokens)
}

// TokenUsageSince sums every token component over [since, until). A zero
// since means "from the beginning of what the store still retains" — the
// conversations table has its own retention loop, so this is the records
// equivalent of Prometheus' lookback-window semantics.
func (s *Store) TokenUsageSince(ctx context.Context, since, until time.Time) (TokenUsageRow, error) {
	query := `SELECT COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(cache_write_tokens), 0),
			COALESCE(SUM(cost_usd), 0),
			COUNT(cache_read_tokens) + COUNT(cache_write_tokens) > 0, COUNT(cost_usd) > 0
		FROM task_conversations WHERE 1=1`
	args := []any{}
	argN := 1

	if !since.IsZero() {
		query += fmt.Sprintf(" AND created_at >= $%d", argN)
		args = append(args, since)
		argN++
	}
	if !until.IsZero() {
		query += fmt.Sprintf(" AND created_at < $%d", argN)
		args = append(args, until)
	}

	var row TokenUsageRow
	if err := s.pool.QueryRow(ctx, query, args...).Scan(
		&row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheWriteTokens, &row.CostUSD,
		&row.CacheReported, &row.CostReported,
	); err != nil {
		return TokenUsageRow{}, fmt.Errorf("tasklogs.TokenUsageSince: %w", err)
	}
	return row, nil
}

// AgentTokenRow holds the four token components pi reports, summed over a
// window for one agent. The accounting rule is the same as TokenUsageRow's:
// callers that need a single figure sum all four (see api.tokenTotal).
// CacheReported follows TokenUsageRow's rule (issue #75).
type AgentTokenRow struct {
	AgentName        string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CacheReported    bool
}

// AgentTokenUsageSince sums every token component per agent over
// [since, until), grouped by agent_name. A zero since means "from the
// beginning of what the store still retains" — the same semantics
// TokenUsageSince documents, so the per-agent rows stay consistent with the
// token summary tile when both are read over the same window.
func (s *Store) AgentTokenUsageSince(ctx context.Context, since, until time.Time) ([]AgentTokenRow, error) {
	query := `SELECT agent_name, COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(cache_write_tokens), 0),
			COUNT(cache_read_tokens) + COUNT(cache_write_tokens) > 0
		FROM task_conversations WHERE 1=1`
	args := []any{}
	argN := 1

	if !since.IsZero() {
		query += fmt.Sprintf(" AND created_at >= $%d", argN)
		args = append(args, since)
		argN++
	}
	if !until.IsZero() {
		query += fmt.Sprintf(" AND created_at < $%d", argN)
		args = append(args, until)
	}

	query += " GROUP BY agent_name ORDER BY agent_name"

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("tasklogs.AgentTokenUsageSince: %w", err)
	}
	defer rows.Close()

	var out []AgentTokenRow
	for rows.Next() {
		var r AgentTokenRow
		if err := rows.Scan(&r.AgentName, &r.InputTokens, &r.OutputTokens,
			&r.CacheReadTokens, &r.CacheWriteTokens, &r.CacheReported); err != nil {
			return nil, fmt.Errorf("tasklogs.AgentTokenUsageSince scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TokenUsageBySubject sums every token component and the cost per
// (agent_name, repo, event_type, model) group over [since, until), the same
// window semantics TokenUsageSince documents. It backs the by-subject token
// table (issue #411): the four components and the cost ride the same rows the
// summary tile sums, so the table's total equals the tile's for the same
// window by construction.
//
// Rows written before migration 0024 group under the empty subject — they are
// kept, not dropped, so the totals stay comparable with the summary. The scan
// is a created_at range scan over the window: idx_task_conversations_subject
// does not serve it (the query constrains none of its leading columns), which
// is acceptable while the retention loop keeps the table at 48 h of rows.
func (s *Store) TokenUsageBySubject(ctx context.Context, since, until time.Time) ([]TokenSubjectUsageRow, error) {
	query := `SELECT agent_name, repo, event_type, model,
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(cache_write_tokens), 0),
			COALESCE(SUM(cost_usd), 0),
			COUNT(cache_read_tokens) + COUNT(cache_write_tokens) > 0, COUNT(cost_usd) > 0
		FROM task_conversations WHERE 1=1`
	args := []any{}
	argN := 1

	if !since.IsZero() {
		query += fmt.Sprintf(" AND created_at >= $%d", argN)
		args = append(args, since)
		argN++
	}
	if !until.IsZero() {
		query += fmt.Sprintf(" AND created_at < $%d", argN)
		args = append(args, until)
	}

	// Deterministic order so the API's stable sort has a stable input; the
	// frontend re-sorts by total anyway.
	query += " GROUP BY agent_name, repo, event_type, model ORDER BY agent_name, repo, event_type, model"

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("tasklogs.TokenUsageBySubject: %w", err)
	}
	defer rows.Close()

	var out []TokenSubjectUsageRow
	for rows.Next() {
		var r TokenSubjectUsageRow
		if err := rows.Scan(&r.AgentName, &r.Repo, &r.EventType, &r.Model,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens,
			&r.CostUSD, &r.CacheReported, &r.CostReported); err != nil {
			return nil, fmt.Errorf("tasklogs.TokenUsageBySubject scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TokenUsageBucket is one fixed-width time bucket of total tokens consumed.
// Buckets the store has no rows for are absent — the caller zero-fills so a
// chart places points by index rather than stretching sparse ones.
type TokenUsageBucket struct {
	Bucket      time.Time
	TotalTokens int64
}

// TokenUsageBuckets sums all four token components into fixed-width buckets
// of step, over [start, end). Bucket boundaries are UTC epochs floored to
// step, matching how the Prometheus range path steps its increase() windows.
func (s *Store) TokenUsageBuckets(ctx context.Context, start, end time.Time, step time.Duration) ([]TokenUsageBucket, error) {
	if step <= 0 {
		return nil, fmt.Errorf("tasklogs.TokenUsageBuckets: step must be positive")
	}
	stepSecs := int64(step / time.Second)
	// The cache columns are NULLable since migration 0025 (issue #75), and SQL
	// arithmetic on a NULL yields NULL — without the COALESCEs a row with an
	// unreported cache figure would drop out of its bucket's total entirely.
	// NULL contributes 0 to the total, as everywhere else in this store.
	query := `
		SELECT to_timestamp(floor(extract(epoch from created_at) / $1) * $1) AS bucket,
			COALESCE(SUM(input_tokens + output_tokens + COALESCE(cache_read_tokens, 0) + COALESCE(cache_write_tokens, 0)), 0)
		FROM task_conversations
		WHERE created_at >= $2 AND created_at < $3
		GROUP BY bucket
		ORDER BY bucket`

	rows, err := s.pool.Query(ctx, query, stepSecs, start, end)
	if err != nil {
		return nil, fmt.Errorf("tasklogs.TokenUsageBuckets: %w", err)
	}
	defer rows.Close()

	var out []TokenUsageBucket
	for rows.Next() {
		var b TokenUsageBucket
		if err := rows.Scan(&b.Bucket, &b.TotalTokens); err != nil {
			return nil, fmt.Errorf("tasklogs.TokenUsageBuckets scan: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// TokenUsageByIssue sums every token component and the cost per
// (agent_name, repo, issue_number, model) group over the filter's window. It
// backs GET /api/v1/tokens (issue #412): the same four components and the
// same cost the token summary tile sums, grouped by the subject columns
// migration 0024 added, so the endpoint's total equals the tile's over the
// same window by construction.
//
// Rows written before migration 0024 group under repo="" / issue_number=0 —
// they are kept, not dropped, so the totals stay comparable with the
// summary. Filters apply server-side; a nil IssueNumber means "no issue
// filter" — never issue_number = 0, which would match every unlabelled row.
// The window semantics are TokenUsageSince's: a zero bound is unbounded, so
// the default reads everything the store still retains.
func (s *Store) TokenUsageByIssue(ctx context.Context, f TokenUsageByIssueFilter) ([]TokenIssueUsageRow, error) {
	query := `SELECT agent_name, repo, issue_number, model,
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(cache_write_tokens), 0),
			COALESCE(SUM(cost_usd), 0),
			COUNT(cache_read_tokens) + COUNT(cache_write_tokens) > 0, COUNT(cost_usd) > 0
		FROM task_conversations WHERE 1=1`
	args := []any{}
	argN := 1

	if !f.Since.IsZero() {
		query += fmt.Sprintf(" AND created_at >= $%d", argN)
		args = append(args, f.Since)
		argN++
	}
	if !f.Until.IsZero() {
		query += fmt.Sprintf(" AND created_at < $%d", argN)
		args = append(args, f.Until)
		argN++
	}
	if f.AgentName != "" {
		query += fmt.Sprintf(" AND agent_name = $%d", argN)
		args = append(args, f.AgentName)
		argN++
	}
	if f.Repo != "" {
		query += fmt.Sprintf(" AND repo = $%d", argN)
		args = append(args, f.Repo)
		argN++
	}
	if f.IssueNumber != nil {
		// Bound as an int: issue_number is INTEGER, and 0 is a real value
		// (subject unknown), reachable only when the caller asked for it.
		query += fmt.Sprintf(" AND issue_number = $%d", argN)
		args = append(args, *f.IssueNumber)
		// No further placeholders follow, so argN is not incremented again.
	}

	// Deterministic order: the JSON API has no sort param, and a map-iteration
	// order would shuffle rows between two identical calls.
	query += " GROUP BY agent_name, repo, issue_number, model ORDER BY agent_name, repo, issue_number, model"

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("tasklogs.TokenUsageByIssue: %w", err)
	}
	defer rows.Close()

	var out []TokenIssueUsageRow
	for rows.Next() {
		var r TokenIssueUsageRow
		if err := rows.Scan(&r.AgentName, &r.Repo, &r.IssueNumber, &r.Model,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens,
			&r.CostUSD, &r.CacheReported, &r.CostReported); err != nil {
			return nil, fmt.Errorf("tasklogs.TokenUsageByIssue scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
