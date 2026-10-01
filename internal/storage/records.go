package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shadowsafin/corerouter/internal/domain"
)

// QueryFilter selects and paginates a set of traffic records.
//
// All fields are optional and combine with AND. It is a struct rather than a long
// parameter list because every dashboard view needs a different subset, and a
// struct keeps the call sites readable and the SQL builder written once.
type QueryFilter struct {
	TenantID  string
	APIKeyID  string
	Provider  string
	Model     string
	PolicyID  string
	Outcome   domain.UsageOutcome
	ErrorCode domain.ErrorCode
	// StatusMin filters to responses at or above a status, used for error views.
	StatusMin int
	// Search matches request id or end user, for support workflows.
	Search string
	From   time.Time
	To     time.Time
	Limit  int
	Offset int
}

// normalize applies defaults and bounds.
func (f *QueryFilter) normalize() {
	now := time.Now().UTC()
	if f.To.IsZero() {
		f.To = now
	}
	if f.From.IsZero() {
		f.From = f.To.Add(-24 * time.Hour)
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	// An unbounded limit is the classic way a dashboard query takes down a
	// database. The cap is generous but absolute.
	if f.Limit > 1000 {
		f.Limit = 1000
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
}

// where builds the WHERE clause and its arguments.
//
// Building the clause programmatically keeps the filter definition in one place
// and makes every query that uses it consistent. Values are always passed as
// parameters, never interpolated, so a filter value can never be interpreted as
// SQL.
func (f QueryFilter) where(alias string) (string, []any) {
	conditions := []string{fmt.Sprintf("%s.created_at >= $1", alias), fmt.Sprintf("%s.created_at < $2", alias)}
	args := []any{f.From, f.To}

	add := func(column string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf("%s.%s = $%d", alias, column, len(args)))
	}
	if f.TenantID != "" {
		add("tenant_id", f.TenantID)
	}
	if f.APIKeyID != "" {
		add("api_key_id", f.APIKeyID)
	}
	if f.Provider != "" {
		add("provider", f.Provider)
	}
	if f.Model != "" {
		add("model", f.Model)
	}
	if f.Outcome != "" {
		add("outcome", string(f.Outcome))
	}
	if f.ErrorCode != "" {
		add("error_code", string(f.ErrorCode))
	}
	if f.PolicyID != "" {
		add("policy_id", f.PolicyID)
	}
	if f.StatusMin > 0 {
		args = append(args, f.StatusMin)
		conditions = append(conditions, fmt.Sprintf("%s.status >= $%d", alias, len(args)))
	}
	if f.Search != "" {
		args = append(args, "%"+f.Search+"%")
		idx := len(args)
		conditions = append(conditions,
			fmt.Sprintf("(%s.request_id ILIKE $%d)", alias, idx))
	}

	return strings.Join(conditions, " AND "), args
}

// ---------------------------------------------------------------------------
// Usage
// ---------------------------------------------------------------------------

// UsageRepository stores billing records.
type UsageRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// Insert writes one usage record.
//
// The insert is idempotent on the primary key, so a retried write (for example
// after a network blip) cannot double-bill a request.
func (r *UsageRepository) Insert(ctx context.Context, rec *domain.UsageRecord) error {
	if rec.ID == "" {
		rec.ID = domain.NewID()
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = domain.Now()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO usage_records (
			id, request_id, trace_id, tenant_id, api_key_id, provider, model, requested_model,
			policy_id, request_type, prompt_tokens, completion_tokens, total_tokens,
			cached_prompt_tokens, estimated, cost_usd, latency_ms, provider_latency_ms, attempts,
			fallback_used, cache_hit, outcome, error_code, streaming, status, client_ip,
			user_agent, end_user, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,
		          $20,$21,$22,$23,$24,$25,$26,$27,$28,$29)
		ON CONFLICT (id) DO NOTHING`,
		rec.ID, rec.RequestID.String(), rec.TraceID, nullableUUID(rec.TenantID),
		nullableUUID(rec.APIKeyID), rec.Provider, rec.Model, rec.RequestedModel,
		nullableUUID(rec.PolicyID), string(rec.RequestType), rec.Usage.PromptTokens,
		rec.Usage.CompletionTokens, rec.Usage.TotalTokens, rec.Usage.CachedPromptTokens,
		rec.Usage.Estimated, rec.Cost.USD, rec.LatencyMS, rec.ProviderLatencyMS,
		rec.Attempts, rec.FallbackUsed, rec.CacheHit, string(rec.Outcome),
		string(rec.ErrorCode), rec.Streaming, rec.Status, rec.ClientIP, rec.UserAgent,
		rec.EndUser, rec.CreatedAt)
	if err != nil {
		return wrapDBError("insert usage record", err)
	}
	return nil
}

const usageColumns = `id, request_id, trace_id, COALESCE(tenant_id::text,''), COALESCE(api_key_id::text,''),
	provider, model, requested_model, COALESCE(policy_id::text,''), request_type,
	prompt_tokens, completion_tokens, total_tokens, cached_prompt_tokens, estimated,
	cost_usd, latency_ms, provider_latency_ms, attempts, fallback_used, cache_hit,
	outcome, error_code, streaming, status, client_ip, user_agent, end_user, created_at`

// List returns usage records matching a filter.
func (r *UsageRepository) List(ctx context.Context, filter QueryFilter) ([]domain.UsageRecord, error) {
	filter.normalize()
	where, args := filter.where("usage_records")

	args = append(args, filter.Limit, filter.Offset)
	query := fmt.Sprintf(`
		SELECT %s
		  FROM usage_records
		 WHERE %s
		 ORDER BY created_at DESC
		 LIMIT $%d OFFSET $%d`, usageColumns, where, len(args)-1, len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, wrapDBError("list usage records", err)
	}
	defer rows.Close()

	var out []domain.UsageRecord
	for rows.Next() {
		rec, err := scanUsage(rows)
		if err != nil {
			return nil, wrapDBError("scan usage record", err)
		}
		out = append(out, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("list usage records", err)
	}
	return out, nil
}

func scanUsage(row pgx.Row) (*domain.UsageRecord, error) {
	var (
		rec     domain.UsageRecord
		reqID   string
		outcome string
		errCode string
		reqType string
	)
	if err := row.Scan(&rec.ID, &reqID, &rec.TraceID, &rec.TenantID, &rec.APIKeyID,
		&rec.Provider, &rec.Model, &rec.RequestedModel, &rec.PolicyID, &reqType,
		&rec.Usage.PromptTokens, &rec.Usage.CompletionTokens, &rec.Usage.TotalTokens,
		&rec.Usage.CachedPromptTokens, &rec.Usage.Estimated, &rec.Cost.USD, &rec.LatencyMS,
		&rec.ProviderLatencyMS, &rec.Attempts, &rec.FallbackUsed, &rec.CacheHit,
		&outcome, &errCode, &rec.Streaming, &rec.Status, &rec.ClientIP, &rec.UserAgent,
		&rec.EndUser, &rec.CreatedAt); err != nil {
		return nil, err
	}
	rec.RequestID = domain.RequestID(reqID)
	rec.Outcome = domain.UsageOutcome(outcome)
	rec.ErrorCode = domain.ErrorCode(errCode)
	rec.RequestType = domain.RequestType(reqType)
	return &rec, nil
}

// Summary aggregates usage over a filter's range, ignoring pagination.
func (r *UsageRepository) Summary(ctx context.Context, filter QueryFilter) (*domain.UsageSummary, error) {
	filter.normalize()
	where, args := filter.where("usage_records")

	// Percentiles come from percentile_cont over the raw rows. An index-only
	// approximation would be faster at scale, but exact percentiles keep the
	// numbers reproducible when an operator compares two windows.
	query := fmt.Sprintf(`
		SELECT
			count(*)                                                          AS requests,
			count(*) FILTER (WHERE outcome IN ('success','fallback'))         AS successes,
			count(*) FILTER (WHERE outcome = 'error')                         AS errors,
			count(*) FILTER (WHERE outcome = 'rejected')                      AS rejections,
			count(*) FILTER (WHERE outcome = 'canceled')                      AS canceled,
			count(*) FILTER (WHERE fallback_used)                             AS fallbacks,
			COALESCE(sum(prompt_tokens), 0)                                   AS prompt_tokens,
			COALESCE(sum(completion_tokens), 0)                               AS completion_tokens,
			COALESCE(sum(total_tokens), 0)                                    AS total_tokens,
			COALESCE(sum(cost_usd), 0)                                        AS total_cost,
			COALESCE(avg(latency_ms), 0)                                      AS avg_latency,
			COALESCE(percentile_cont(0.50) WITHIN GROUP (ORDER BY latency_ms), 0) AS p50,
			COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms), 0) AS p95,
			COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY latency_ms), 0) AS p99,
			count(DISTINCT tenant_id)                                         AS unique_tenants,
			count(DISTINCT api_key_id)                                        AS unique_keys
		  FROM usage_records
		 WHERE %s`, where)

	var s domain.UsageSummary
	// Percentiles are scanned as float64: percentile_cont interpolates, so over
	// enough rows the value is fractional (e.g. 1019.95) and pgx cannot scan
	// NUMERIC with a fractional part into *int64. Rounding keeps the
	// millisecond contract of the domain type.
	var p50, p95, p99 float64
	err := r.pool.QueryRow(ctx, query, args...).Scan(
		&s.Requests, &s.Successes, &s.Errors, &s.Rejections, &s.Canceled, &s.Fallbacks,
		&s.PromptTokens, &s.CompletionTokens, &s.TotalTokens, &s.TotalCostUSD,
		&s.AvgLatencyMS, &p50, &p95, &p99,
		&s.UniqueTenants, &s.UniqueKeys)
	if err != nil {
		return nil, wrapDBError("summarize usage", err)
	}
	s.LatencyP50MS = int64(math.Round(p50))
	s.LatencyP95MS = int64(math.Round(p95))
	s.LatencyP99MS = int64(math.Round(p99))

	if s.Requests > 0 {
		s.ErrorRate = float64(s.Errors+s.Rejections) / float64(s.Requests)
		s.FallbackRate = float64(s.Fallbacks) / float64(s.Requests)
		s.AvgCostUSD = s.TotalCostUSD / float64(s.Requests)
	}
	return &s, nil
}

// Series returns usage bucketed over time.
func (r *UsageRepository) Series(ctx context.Context, filter QueryFilter, interval string) ([]domain.TimeBucket, error) {
	filter.normalize()

	width, ok := domain.ParseInterval(interval)
	if !ok {
		width = time.Hour
		interval = "1h"
	}
	where, args := filter.where("usage_records")

	// generate_series ensures every bucket appears even when it has no traffic.
	// Without it a chart would silently skip quiet periods, making an outage look
	// like a narrower range instead of a gap.
	//
	// The series start is aligned to the epoch grid, exactly like the grouping
	// expression below (floor to a multiple of the width since the epoch).
	// Aligning to `from` instead would shift every series bucket off the grid
	// the grouped rows land on, so the join would match nothing and every
	// bucket would read zero while the summary stayed correct.
	args = append(args, width.Milliseconds(), filter.From, filter.To)
	widthIdx := len(args) - 2
	fromIdx := len(args) - 1
	toIdx := len(args)

	query := fmt.Sprintf(`
		WITH buckets AS (
			SELECT generate_series(
				to_timestamp(floor(extract(epoch FROM $%d::timestamptz) * 1000 / $%d) * $%d / 1000),
				$%d::timestamptz,
				($%d::text || ' milliseconds')::interval) AS bucket_start
		),
		grouped AS (
			SELECT
				to_timestamp(floor(extract(epoch FROM created_at) * 1000 / (%d)) * (%d) / 1000) AS bucket_start,
				count(*) AS requests,
				count(*) FILTER (WHERE outcome IN ('success','fallback')) AS successes,
				count(*) FILTER (WHERE outcome = 'error') AS errors,
				count(*) FILTER (WHERE outcome = 'rejected') AS rejections,
				count(*) FILTER (WHERE fallback_used) AS fallbacks,
				count(*) FILTER (WHERE cache_hit) AS cache_hits,
				COALESCE(sum(prompt_tokens), 0) AS prompt_tokens,
				COALESCE(sum(completion_tokens), 0) AS completion_tokens,
				COALESCE(sum(total_tokens), 0) AS total_tokens,
				COALESCE(sum(cost_usd), 0) AS cost_usd,
				COALESCE(percentile_cont(0.50) WITHIN GROUP (ORDER BY latency_ms), 0) AS p50,
				COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms), 0) AS p95,
				COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY latency_ms), 0) AS p99
			  FROM usage_records
			 WHERE %s
			 GROUP BY 1
		)
		SELECT b.bucket_start,
		       COALESCE(g.requests, 0), COALESCE(g.successes, 0), COALESCE(g.errors, 0),
		       COALESCE(g.rejections, 0), COALESCE(g.fallbacks, 0), COALESCE(g.cache_hits, 0),
		       COALESCE(g.prompt_tokens, 0), COALESCE(g.completion_tokens, 0),
		       COALESCE(g.total_tokens, 0), COALESCE(g.cost_usd, 0),
		       COALESCE(g.p50, 0), COALESCE(g.p95, 0), COALESCE(g.p99, 0)
		  FROM buckets b
		  LEFT JOIN grouped g ON g.bucket_start = b.bucket_start
		 ORDER BY b.bucket_start`,
		fromIdx, widthIdx, widthIdx, toIdx, widthIdx,
		width.Milliseconds(), width.Milliseconds(),
		where)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, wrapDBError("build usage series", err)
	}
	defer rows.Close()

	var out []domain.TimeBucket
	for rows.Next() {
		var b domain.TimeBucket
		var p50, p95, p99 float64
		if err := rows.Scan(&b.Start, &b.Requests, &b.Successes, &b.Errors, &b.Rejections,
			&b.Fallbacks, &b.CacheHits, &b.PromptTokens, &b.CompletionTokens, &b.TotalTokens,
			&b.CostUSD, &p50, &p95, &p99); err != nil {
			return nil, wrapDBError("scan usage bucket", err)
		}
		// See Summary: bucket percentiles interpolate fractionally.
		b.LatencyP50MS = int64(math.Round(p50))
		b.LatencyP95MS = int64(math.Round(p95))
		b.LatencyP99MS = int64(math.Round(p99))
		b.End = b.Start.Add(width)
		b.Interval = interval
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("build usage series", err)
	}
	return out, nil
}

// ByProvider aggregates usage per provider.
func (r *UsageRepository) ByProvider(ctx context.Context, filter QueryFilter, limit int) ([]domain.ProviderBreakdownRow, error) {
	filter.normalize()
	where, args := filter.where("usage_records")
	if limit <= 0 {
		limit = 20
	}
	args = append(args, limit)

	query := fmt.Sprintf(`
		SELECT provider,
		       count(*) AS requests,
		       count(*) FILTER (WHERE outcome IN ('error','rejected')) AS errors,
		       count(*) FILTER (WHERE fallback_used) AS fallbacks,
		       COALESCE(sum(total_tokens), 0) AS tokens,
		       COALESCE(sum(cost_usd), 0) AS cost,
		       COALESCE(avg(latency_ms), 0) AS avg_latency,
		       COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms), 0) AS p95
		  FROM usage_records
		 WHERE %s
		 GROUP BY provider
		 ORDER BY requests DESC
		 LIMIT $%d`, where, len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, wrapDBError("aggregate usage by provider", err)
	}
	defer rows.Close()

	var out []domain.ProviderBreakdownRow
	for rows.Next() {
		var row domain.ProviderBreakdownRow
		var p95 float64
		if err := rows.Scan(&row.Provider, &row.Requests, &row.Errors, &row.Fallbacks,
			&row.TotalTokens, &row.CostUSD, &row.AvgLatencyMS, &p95); err != nil {
			return nil, wrapDBError("scan provider aggregate", err)
		}
		// See Summary: the p95 interpolates fractionally.
		row.LatencyP95MS = int64(math.Round(p95))
		if row.Requests > 0 {
			row.ErrorRate = float64(row.Errors) / float64(row.Requests)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("aggregate usage by provider", err)
	}
	return out, nil
}

// ByModel aggregates usage per model.
func (r *UsageRepository) ByModel(ctx context.Context, filter QueryFilter, limit int) ([]domain.ModelBreakdownRow, error) {
	filter.normalize()
	where, args := filter.where("usage_records")
	if limit <= 0 {
		limit = 20
	}
	args = append(args, limit)

	query := fmt.Sprintf(`
		SELECT model, count(*) AS requests,
		       count(*) FILTER (WHERE outcome IN ('error','rejected')) AS errors,
		       COALESCE(sum(total_tokens), 0) AS tokens,
		       COALESCE(sum(cost_usd), 0) AS cost,
		       COALESCE(avg(latency_ms), 0) AS avg_latency
		  FROM usage_records
		 WHERE %s
		 GROUP BY model
		 ORDER BY requests DESC
		 LIMIT $%d`, where, len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, wrapDBError("aggregate usage by model", err)
	}
	defer rows.Close()

	var out []domain.ModelBreakdownRow
	for rows.Next() {
		var row domain.ModelBreakdownRow
		if err := rows.Scan(&row.Model, &row.Requests, &row.Errors, &row.TotalTokens,
			&row.CostUSD, &row.AvgLatencyMS); err != nil {
			return nil, wrapDBError("scan model aggregate", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("aggregate usage by model", err)
	}
	return out, nil
}

// CurrentSpend returns the spend for a tenant-keyed scope in a period label.
func (r *UsageRepository) CurrentSpend(ctx context.Context, tenantID, periodLabel string) (float64, error) {
	var total float64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(cost_usd), 0)
		  FROM usage_records
		 WHERE tenant_id = $1 AND to_char(created_at, 'YYYY-MM') = $2`,
		tenantID, periodLabel).Scan(&total)
	if err != nil {
		return 0, wrapDBError("read current spend", err)
	}
	return total, nil
}

// Prune deletes usage records older than a cutoff, honouring retention settings.
func (r *UsageRepository) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM usage_records WHERE created_at < $1`, olderThan)
	if err != nil {
		return 0, wrapDBError("prune usage records", err)
	}
	return tag.RowsAffected(), nil
}

// ---------------------------------------------------------------------------
// Request logs
// ---------------------------------------------------------------------------

// RequestLogRepository stores per-request debug detail.
type RequestLogRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// Insert writes a request log entry.
func (r *RequestLogRepository) Insert(ctx context.Context, entry *domain.RequestLog) error {
	if entry.ID == "" {
		entry.ID = domain.NewID()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = domain.Now()
	}

	var decision []byte
	if entry.Decision != nil {
		encoded, err := json.Marshal(entry.Decision)
		if err != nil {
			// The decision is diagnostic; losing it must not lose the request log.
			r.logger.Warn("failed to encode route decision for the request log", "error", err)
		} else {
			decision = encoded
		}
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO request_logs (
			id, request_id, trace_id, tenant_id, api_key_id, requested_model, routed_model,
			provider, policy_id, strategy, status, outcome, error_code, error_message,
			latency_ms, attempts, fallback_used, prompt_tokens, completion_tokens, cost_usd,
			decision, client_ip, user_agent, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,
		          $21,$22,$23,$24)
		ON CONFLICT (request_id) DO NOTHING`,
		entry.ID, entry.RequestID, entry.TraceID, nullableUUID(entry.TenantID),
		nullableUUID(entry.APIKeyID), entry.RequestedModel, entry.RoutedModel, entry.Provider,
		nullableUUID(entry.PolicyID), entry.Strategy, entry.Status, string(entry.Outcome),
		string(entry.ErrorCode), entry.ErrorMessage, entry.LatencyMS, entry.Attempts,
		entry.FallbackUsed, entry.PromptTokens, entry.CompletionTokens, entry.CostUSD,
		nullableJSON(decision), entry.ClientIP, entry.UserAgent, entry.CreatedAt)
	if err != nil {
		return wrapDBError("insert request log", err)
	}
	return nil
}

const requestLogColumns = `id, request_id, trace_id, COALESCE(tenant_id::text,''),
	COALESCE(api_key_id::text,''), requested_model, routed_model, provider,
	COALESCE(policy_id::text,''), strategy, status, outcome, error_code, error_message,
	latency_ms, attempts, fallback_used, prompt_tokens, completion_tokens, cost_usd,
	decision, client_ip, user_agent, created_at`

// List returns request logs matching a filter.
func (r *RequestLogRepository) List(ctx context.Context, filter QueryFilter) ([]domain.RequestLog, error) {
	filter.normalize()
	where, args := filter.where("request_logs")
	args = append(args, filter.Limit, filter.Offset)

	query := fmt.Sprintf(`
		SELECT %s
		  FROM request_logs
		 WHERE %s
		 ORDER BY created_at DESC
		 LIMIT $%d OFFSET $%d`, requestLogColumns, where, len(args)-1, len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, wrapDBError("list request logs", err)
	}
	defer rows.Close()

	var out []domain.RequestLog
	for rows.Next() {
		entry, err := scanRequestLog(rows)
		if err != nil {
			return nil, wrapDBError("scan request log", err)
		}
		out = append(out, *entry)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("list request logs", err)
	}
	return out, nil
}

// GetByRequestID returns a single request log entry.
func (r *RequestLogRepository) GetByRequestID(ctx context.Context, requestID string) (*domain.RequestLog, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+requestLogColumns+` FROM request_logs WHERE request_id = $1`, requestID)
	entry, err := scanRequestLog(row)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, wrapDBError("load request log", err)
	}
	return entry, nil
}

func scanRequestLog(row pgx.Row) (*domain.RequestLog, error) {
	var (
		entry    domain.RequestLog
		outcome  string
		errCode  string
		decision []byte
	)
	if err := row.Scan(&entry.ID, &entry.RequestID, &entry.TraceID, &entry.TenantID,
		&entry.APIKeyID, &entry.RequestedModel, &entry.RoutedModel, &entry.Provider,
		&entry.PolicyID, &entry.Strategy, &entry.Status, &outcome, &errCode,
		&entry.ErrorMessage, &entry.LatencyMS, &entry.Attempts, &entry.FallbackUsed,
		&entry.PromptTokens, &entry.CompletionTokens, &entry.CostUSD, &decision,
		&entry.ClientIP, &entry.UserAgent, &entry.CreatedAt); err != nil {
		return nil, err
	}
	entry.Outcome = domain.UsageOutcome(outcome)
	entry.ErrorCode = domain.ErrorCode(errCode)
	if len(decision) > 0 {
		var d domain.RouteDecision
		if err := json.Unmarshal(decision, &d); err == nil {
			entry.Decision = &d
		}
	}
	return &entry, nil
}

// Prune deletes request logs older than a cutoff.
func (r *RequestLogRepository) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM request_logs WHERE created_at < $1`, olderThan)
	if err != nil {
		return 0, wrapDBError("prune request logs", err)
	}
	return tag.RowsAffected(), nil
}

// ---------------------------------------------------------------------------
// Audit events
// ---------------------------------------------------------------------------

// AuditRepository stores append-only audit events.
//
// There is deliberately no update or delete method: an audit trail that can be
// edited is not an audit trail.
type AuditRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// Insert writes an audit event.
func (r *AuditRepository) Insert(ctx context.Context, event *domain.AuditEvent) error {
	if event.ID == "" {
		event.ID = domain.NewID()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = domain.Now()
	}

	before, _ := json.Marshal(event.Before)
	after, _ := json.Marshal(event.After)
	metadata, _ := json.Marshal(orEmptyMap(event.Metadata))

	_, err := r.pool.Exec(ctx, `
		INSERT INTO audit_events (id, action, resource, resource_id, tenant_id, actor_key_id,
			actor_label, actor_ip, before, after, metadata, request_id, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		event.ID, string(event.Action), string(event.Resource), event.ResourceID,
		nullableUUID(event.TenantID), nullableUUID(event.ActorKeyID), event.ActorLabel,
		event.ActorIP, nullableJSON(before), nullableJSON(after), metadata,
		string(event.RequestID), event.CreatedAt)
	if err != nil {
		return wrapDBError("insert audit event", err)
	}
	return nil
}

// List returns audit events, newest first.
func (r *AuditRepository) List(ctx context.Context, tenantID string, resource domain.AuditResource, limit, offset int) ([]domain.AuditEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	conditions := []string{"TRUE"}
	args := []any{}
	if tenantID != "" {
		args = append(args, tenantID)
		conditions = append(conditions, fmt.Sprintf("tenant_id = $%d", len(args)))
	}
	if resource != "" {
		args = append(args, string(resource))
		conditions = append(conditions, fmt.Sprintf("resource = $%d", len(args)))
	}
	args = append(args, limit, offset)

	query := fmt.Sprintf(`
		SELECT id, action, resource, resource_id, COALESCE(tenant_id::text,''),
		       COALESCE(actor_key_id::text,''), actor_label, actor_ip, before, after,
		       metadata, request_id, created_at
		  FROM audit_events
		 WHERE %s
		 ORDER BY created_at DESC
		 LIMIT $%d OFFSET $%d`,
		strings.Join(conditions, " AND "), len(args)-1, len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, wrapDBError("list audit events", err)
	}
	defer rows.Close()

	var out []domain.AuditEvent
	for rows.Next() {
		var (
			event    domain.AuditEvent
			action   string
			res      string
			before   []byte
			after    []byte
			metadata []byte
		)
		if err := rows.Scan(&event.ID, &action, &res, &event.ResourceID, &event.TenantID,
			&event.ActorKeyID, &event.ActorLabel, &event.ActorIP, &before, &after,
			&metadata, &event.RequestID, &event.CreatedAt); err != nil {
			return nil, wrapDBError("scan audit event", err)
		}
		event.Action = domain.AuditAction(action)
		event.Resource = domain.AuditResource(res)
		if len(before) > 0 {
			_ = json.Unmarshal(before, &event.Before)
		}
		if len(after) > 0 {
			_ = json.Unmarshal(after, &event.After)
		}
		if len(metadata) > 0 {
			_ = json.Unmarshal(metadata, &event.Metadata)
		}
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("list audit events", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Budgets
// ---------------------------------------------------------------------------

// BudgetRepository stores budget definitions.
type BudgetRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// Upsert creates or updates a budget.
func (r *BudgetRepository) Upsert(ctx context.Context, b *domain.Budget) (*domain.Budget, error) {
	if b.ID == "" {
		b.ID = domain.NewID()
	}
	thresholds := b.AlertThresholdsUSD
	if thresholds == nil {
		thresholds = []float64{}
	}

	row := r.pool.QueryRow(ctx, `
		INSERT INTO budgets (id, tenant_id, scope, scope_id, period, limit_usd, spent_usd,
			enforced, alert_thresholds_usd, reset_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (tenant_id, scope, scope_id, period) DO UPDATE
			SET limit_usd = EXCLUDED.limit_usd,
			    enforced = EXCLUDED.enforced,
			    alert_thresholds_usd = EXCLUDED.alert_thresholds_usd,
			    reset_at = EXCLUDED.reset_at,
			    updated_at = now()
		RETURNING id, tenant_id, scope, scope_id, period, limit_usd, spent_usd, enforced,
		          alert_thresholds_usd, reset_at, created_at, updated_at`,
		b.ID, b.TenantID, b.Scope, b.ScopeID, string(b.Period), b.LimitUSD, b.SpentUSD,
		b.Enforced, thresholds, b.ResetAt)

	var out domain.Budget
	var period string
	if err := row.Scan(&out.ID, &out.TenantID, &out.Scope, &out.ScopeID, &period,
		&out.LimitUSD, &out.SpentUSD, &out.Enforced, &out.AlertThresholdsUSD,
		&out.ResetAt, &out.CreatedAt, &out.UpdatedAt); err != nil {
		return nil, wrapDBError("upsert budget", err)
	}
	out.Period = domain.BudgetPeriod(period)
	return &out, nil
}

// ListByTenant returns a tenant's budgets.
func (r *BudgetRepository) ListByTenant(ctx context.Context, tenantID string) ([]domain.Budget, error) {
	query := `
		SELECT id, tenant_id, scope, scope_id, period, limit_usd, spent_usd, enforced,
		       alert_thresholds_usd, reset_at, created_at, updated_at
		  FROM budgets`
	args := []any{}
	if tenantID != "" {
		query += ` WHERE tenant_id = $1`
		args = append(args, tenantID)
	}
	query += ` ORDER BY scope, period`

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, wrapDBError("list budgets", err)
	}
	defer rows.Close()

	var out []domain.Budget
	for rows.Next() {
		var (
			b      domain.Budget
			period string
		)
		if err := rows.Scan(&b.ID, &b.TenantID, &b.Scope, &b.ScopeID, &period,
			&b.LimitUSD, &b.SpentUSD, &b.Enforced, &b.AlertThresholdsUSD, &b.ResetAt,
			&b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, wrapDBError("scan budget", err)
		}
		b.Period = domain.BudgetPeriod(period)
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("list budgets", err)
	}
	return out, nil
}

// ReconcileSpend writes the authoritative spend figure back to Postgres.
//
// Redis holds the live counter; this persists it so a Redis flush does not lose
// the month's spend. It runs on an interval, not per request.
func (r *BudgetRepository) ReconcileSpend(ctx context.Context, tenantID, scope, scopeID string, period domain.BudgetPeriod, spent float64) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE budgets
		   SET spent_usd = $5, updated_at = now()
		 WHERE tenant_id = $1 AND scope = $2 AND scope_id = $3 AND period = $4`,
		tenantID, scope, scopeID, string(period), spent)
	if err != nil {
		return wrapDBError("reconcile budget spend", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Provider status snapshots
// ---------------------------------------------------------------------------

// ProviderSnapshotRepository stores periodic provider health rollups.
type ProviderSnapshotRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// Insert writes a snapshot.
func (r *ProviderSnapshotRepository) Insert(ctx context.Context, snap *domain.ProviderStatusSnapshot) error {
	if snap.ID == "" {
		snap.ID = domain.NewID()
	}
	if snap.CapturedAt.IsZero() {
		snap.CapturedAt = domain.Now()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO provider_status_snapshots (
			id, provider_id, state, window_ms, request_count, success_count, error_count,
			success_rate, error_rate, latency_p50_ms, latency_p95_ms, latency_p99_ms,
			total_tokens, total_cost_usd, message, captured_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		snap.ID, snap.ProviderID, string(snap.State), snap.WindowMS, snap.RequestCount,
		snap.SuccessCount, snap.ErrorCount, snap.SuccessRate, snap.ErrorRate,
		snap.LatencyP50MS, snap.LatencyP95MS, snap.LatencyP99MS, snap.TotalTokens,
		snap.TotalCostUSD, snap.Message, snap.CapturedAt)
	if err != nil {
		return wrapDBError("insert provider snapshot", err)
	}
	return nil
}

// ListRecent returns recent snapshots, optionally for one provider.
func (r *ProviderSnapshotRepository) ListRecent(ctx context.Context, providerID string, limit int) ([]domain.ProviderStatusSnapshot, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `
		SELECT s.id, s.provider_id, COALESCE(p.name, ''), s.state, s.window_ms,
		       s.request_count, s.success_count, s.error_count, s.success_rate, s.error_rate,
		       s.latency_p50_ms, s.latency_p95_ms, s.latency_p99_ms, s.total_tokens,
		       s.total_cost_usd, s.message, s.captured_at
		  FROM provider_status_snapshots s
		  LEFT JOIN providers p ON p.id = s.provider_id`
	args := []any{}
	if providerID != "" {
		query += ` WHERE s.provider_id = $1`
		args = append(args, providerID)
	}
	args = append(args, limit)
	query += fmt.Sprintf(` ORDER BY s.captured_at DESC LIMIT $%d`, len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, wrapDBError("list provider snapshots", err)
	}
	defer rows.Close()

	var out []domain.ProviderStatusSnapshot
	for rows.Next() {
		var (
			snap  domain.ProviderStatusSnapshot
			state string
		)
		if err := rows.Scan(&snap.ID, &snap.ProviderID, &snap.ProviderName, &state,
			&snap.WindowMS, &snap.RequestCount, &snap.SuccessCount, &snap.ErrorCount,
			&snap.SuccessRate, &snap.ErrorRate, &snap.LatencyP50MS, &snap.LatencyP95MS,
			&snap.LatencyP99MS, &snap.TotalTokens, &snap.TotalCostUSD, &snap.Message,
			&snap.CapturedAt); err != nil {
			return nil, wrapDBError("scan provider snapshot", err)
		}
		snap.State = domain.HealthState(state)
		out = append(out, snap)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("list provider snapshots", err)
	}
	return out, nil
}

// nullableJSON renders an empty JSON value as SQL NULL.
func nullableJSON(raw []byte) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}
