package storage

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shadowsafin/synapass/internal/domain"
)

// Phase2 repositories for new entities. They follow the same patterns as the
// Phase 1 stores: UUID ids minted by the caller, UTC timestamps, JSONB for
// nested structures.

// ---------------------------------------------------------------------------
// Replay jobs
// ---------------------------------------------------------------------------

// ReplayRepository stores replay jobs and evaluation runs/results.
type ReplayRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewReplayRepository constructs the store.
func NewReplayRepository(pool *pgxpool.Pool, logger *slog.Logger) *ReplayRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReplayRepository{pool: pool, logger: logger}
}

// CreateJob inserts a replay job.
func (r *ReplayRepository) CreateJob(ctx context.Context, job *domain.ReplayJob) error {
	if job.ID == "" {
		job.ID = domain.NewID()
	}
	// The array columns are NOT NULL: a nil slice would insert NULL and
	// violate the constraint, so normalize to empty arrays up front. This is
	// the common case — the dashboard only sends request_ids.
	if job.RequestIDs == nil {
		job.RequestIDs = []string{}
	}
	if job.Providers == nil {
		job.Providers = []string{}
	}
	if job.Models == nil {
		job.Models = []string{}
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO replay_jobs (id, tenant_id, name, status, request_ids, dataset,
			providers, models, max_requests, created_by, progress, total, error)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		job.ID, nullableUUID(job.TenantID), job.Name, job.Status, job.RequestIDs,
		job.Dataset, job.Providers, job.Models, job.MaxRequests, job.CreatedBy,
		job.Progress, job.Total, job.Error)
	if err != nil {
		return wrapDBError("create replay job", err)
	}
	return nil
}

// UpdateJob updates status/progress.
func (r *ReplayRepository) UpdateJob(ctx context.Context, job *domain.ReplayJob) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE replay_jobs SET status=$2, progress=$3, total=$4, error=$5,
			finished_at=$6, updated_at=now() WHERE id=$1`,
		job.ID, job.Status, job.Progress, job.Total, job.Error, job.FinishedAt)
	if err != nil {
		return wrapDBError("update replay job", err)
	}
	return nil
}

// GetJob returns a job by id.
func (r *ReplayRepository) GetJob(ctx context.Context, id string) (*domain.ReplayJob, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, COALESCE(tenant_id::text,''), name, status, request_ids, dataset,
			providers, models, max_requests, created_by, progress, total, error,
			created_at, updated_at, finished_at
		FROM replay_jobs WHERE id=$1`, id)
	var j domain.ReplayJob
	if err := row.Scan(&j.ID, &j.TenantID, &j.Name, &j.Status, &j.RequestIDs,
		&j.Dataset, &j.Providers, &j.Models, &j.MaxRequests, &j.CreatedBy,
		&j.Progress, &j.Total, &j.Error, &j.CreatedAt, &j.UpdatedAt, &j.FinishedAt); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, wrapDBError("load replay job", err)
	}
	return &j, nil
}

// ListJobs returns recent jobs for a tenant (or all when tenant empty).
func (r *ReplayRepository) ListJobs(ctx context.Context, tenantID string, limit int) ([]domain.ReplayJob, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows interface {
		Close()
	}
	_ = rows
	var out []domain.ReplayJob
	if tenantID == "" {
		rs, err := r.pool.Query(ctx, `
			SELECT id, COALESCE(tenant_id::text,''), name, status, request_ids, dataset,
				providers, models, max_requests, created_by, progress, total, error,
				created_at, updated_at, finished_at
			FROM replay_jobs ORDER BY created_at DESC LIMIT $1`, limit)
		if err != nil {
			return nil, wrapDBError("list replay jobs", err)
		}
		defer rs.Close()
		for rs.Next() {
			var j domain.ReplayJob
			if err := rs.Scan(&j.ID, &j.TenantID, &j.Name, &j.Status, &j.RequestIDs,
				&j.Dataset, &j.Providers, &j.Models, &j.MaxRequests, &j.CreatedBy,
				&j.Progress, &j.Total, &j.Error, &j.CreatedAt, &j.UpdatedAt, &j.FinishedAt); err != nil {
				return nil, wrapDBError("scan replay job", err)
			}
			out = append(out, j)
		}
		return out, nil
	}
	rs, err := r.pool.Query(ctx, `
		SELECT id, COALESCE(tenant_id::text,''), name, status, request_ids, dataset,
			providers, models, max_requests, created_by, progress, total, error,
			created_at, updated_at, finished_at
		FROM replay_jobs WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT $2`, nullableUUID(tenantID), limit)
	if err != nil {
		return nil, wrapDBError("list replay jobs", err)
	}
	defer rs.Close()
	for rs.Next() {
		var j domain.ReplayJob
		if err := rs.Scan(&j.ID, &j.TenantID, &j.Name, &j.Status, &j.RequestIDs,
			&j.Dataset, &j.Providers, &j.Models, &j.MaxRequests, &j.CreatedBy,
			&j.Progress, &j.Total, &j.Error, &j.CreatedAt, &j.UpdatedAt, &j.FinishedAt); err != nil {
			return nil, wrapDBError("scan replay job", err)
		}
		out = append(out, j)
	}
	return out, nil
}

// CreateRun inserts an evaluation run.
func (r *ReplayRepository) CreateRun(ctx context.Context, run *domain.EvaluationRun) error {
	if run.ID == "" {
		run.ID = domain.NewID()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO evaluation_runs (id, tenant_id, replay_job_id, dataset, status, created_by, error)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		run.ID, nullableUUID(run.TenantID), nullableUUID(run.ReplayJobID),
		run.Dataset, run.Status, run.CreatedBy, run.Error)
	if err != nil {
		return wrapDBError("create evaluation run", err)
	}
	return nil
}

// UpdateRun updates a run.
func (r *ReplayRepository) UpdateRun(ctx context.Context, run *domain.EvaluationRun) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE evaluation_runs SET status=$2, error=$3, finished_at=$4 WHERE id=$1`,
		run.ID, run.Status, run.Error, run.FinishedAt)
	if err != nil {
		return wrapDBError("update evaluation run", err)
	}
	return nil
}

// GetRun returns a run.
func (r *ReplayRepository) GetRun(ctx context.Context, id string) (*domain.EvaluationRun, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, COALESCE(tenant_id::text,''), COALESCE(replay_job_id::text,''),
			dataset, status, created_by, error, created_at, finished_at
		FROM evaluation_runs WHERE id=$1`, id)
	var run domain.EvaluationRun
	if err := row.Scan(&run.ID, &run.TenantID, &run.ReplayJobID, &run.Dataset,
		&run.Status, &run.CreatedBy, &run.Error, &run.CreatedAt, &run.FinishedAt); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, wrapDBError("load evaluation run", err)
	}
	return &run, nil
}

// ListRuns returns recent runs.
func (r *ReplayRepository) ListRuns(ctx context.Context, tenantID string, limit int) ([]domain.EvaluationRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT id, COALESCE(tenant_id::text,''), COALESCE(replay_job_id::text,''),
		dataset, status, created_by, error, created_at, finished_at
		FROM evaluation_runs ORDER BY created_at DESC LIMIT $1`
	var args []any
	args = append(args, limit)
	if tenantID != "" {
		q = `SELECT id, COALESCE(tenant_id::text,''), COALESCE(replay_job_id::text,''),
			dataset, status, created_by, error, created_at, finished_at
			FROM evaluation_runs WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT $2`
		args = []any{nullableUUID(tenantID), limit}
	}
	rs, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, wrapDBError("list evaluation runs", err)
	}
	defer rs.Close()
	var out []domain.EvaluationRun
	for rs.Next() {
		var run domain.EvaluationRun
		if err := rs.Scan(&run.ID, &run.TenantID, &run.ReplayJobID, &run.Dataset,
			&run.Status, &run.CreatedBy, &run.Error, &run.CreatedAt, &run.FinishedAt); err != nil {
			return nil, wrapDBError("scan evaluation run", err)
		}
		out = append(out, run)
	}
	return out, nil
}

// AddResult inserts an evaluation result.
func (r *ReplayRepository) AddResult(ctx context.Context, res *domain.EvaluationResult) error {
	if res.ID == "" {
		res.ID = domain.NewID()
	}
	metrics, _ := json.Marshal(res.Metrics)
	if metrics == nil {
		metrics = []byte(`{}`)
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO evaluation_results (id, evaluation_id, request_id, provider, model,
			score, latency_ms, cost_usd, output_preview, is_regression, metrics)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		res.ID, res.EvaluationID, res.RequestID, res.Provider, res.Model,
		res.Score, res.LatencyMS, res.CostUSD, res.OutputPreview, res.IsRegression, metrics)
	if err != nil {
		return wrapDBError("insert evaluation result", err)
	}
	return nil
}

// ListResults returns results for an evaluation.
func (r *ReplayRepository) ListResults(ctx context.Context, evaluationID string, limit int) ([]domain.EvaluationResult, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rs, err := r.pool.Query(ctx, `
		SELECT id, evaluation_id::text, request_id, provider, model, score,
			latency_ms, cost_usd, output_preview, is_regression, metrics, created_at
		FROM evaluation_results WHERE evaluation_id=$1 ORDER BY score DESC LIMIT $2`,
		evaluationID, limit)
	if err != nil {
		return nil, wrapDBError("list evaluation results", err)
	}
	defer rs.Close()
	var out []domain.EvaluationResult
	for rs.Next() {
		var res domain.EvaluationResult
		var metrics []byte
		if err := rs.Scan(&res.ID, &res.EvaluationID, &res.RequestID, &res.Provider,
			&res.Model, &res.Score, &res.LatencyMS, &res.CostUSD, &res.OutputPreview,
			&res.IsRegression, &metrics, &res.CreatedAt); err != nil {
			return nil, wrapDBError("scan evaluation result", err)
		}
		if len(metrics) > 0 {
			_ = json.Unmarshal(metrics, &res.Metrics)
		}
		out = append(out, res)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Feedback
// ---------------------------------------------------------------------------

// FeedbackRepository stores feedback events.
type FeedbackRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewFeedbackRepository constructs the store.
func NewFeedbackRepository(pool *pgxpool.Pool, logger *slog.Logger) *FeedbackRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &FeedbackRepository{pool: pool, logger: logger}
}

// Insert adds a feedback event.
func (r *FeedbackRepository) Insert(ctx context.Context, e *domain.FeedbackEvent) error {
	if e.ID == "" {
		e.ID = domain.NewID()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = domain.Now()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO feedback_events (id, request_id, tenant_id, score, comment)
		VALUES ($1,$2,$3,$4,$5)`,
		e.ID, e.RequestID.String(), nullableUUID(e.TenantID), e.Score, e.Comment)
	if err != nil {
		return wrapDBError("insert feedback", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Overrides
// ---------------------------------------------------------------------------

// OverrideRepository stores audit overrides.
type OverrideRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewOverrideRepository constructs the store.
func NewOverrideRepository(pool *pgxpool.Pool, logger *slog.Logger) *OverrideRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &OverrideRepository{pool: pool, logger: logger}
}

// Create inserts an override.
func (r *OverrideRepository) Create(ctx context.Context, o *domain.AuditOverride) error {
	if o.ID == "" {
		o.ID = domain.NewID()
	}
	if o.CreatedAt.IsZero() {
		o.CreatedAt = domain.Now()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO audit_overrides (id, tenant_id, kind, target, enabled, reason, actor, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		o.ID, nullableUUID(o.TenantID), o.Kind, o.Target, o.Enabled, o.Reason, o.Actor, o.ExpiresAt)
	if err != nil {
		return wrapDBError("create override", err)
	}
	return nil
}

// List returns recent overrides.
func (r *OverrideRepository) List(ctx context.Context, limit int) ([]domain.AuditOverride, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rs, err := r.pool.Query(ctx, `
		SELECT id, COALESCE(tenant_id::text,''), kind, target, enabled, reason, actor, expires_at, created_at
		FROM audit_overrides ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, wrapDBError("list overrides", err)
	}
	defer rs.Close()
	var out []domain.AuditOverride
	for rs.Next() {
		var o domain.AuditOverride
		if err := rs.Scan(&o.ID, &o.TenantID, &o.Kind, &o.Target, &o.Enabled, &o.Reason, &o.Actor, &o.ExpiresAt, &o.CreatedAt); err != nil {
			return nil, wrapDBError("scan override", err)
		}
		out = append(out, o)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Endpoints
// ---------------------------------------------------------------------------

// EndpointRepository stores endpoint scopes.
type EndpointRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewEndpointRepository constructs the store.
func NewEndpointRepository(pool *pgxpool.Pool, logger *slog.Logger) *EndpointRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &EndpointRepository{pool: pool, logger: logger}
}

// Upsert creates or updates an endpoint by slug.
//
// Like routing policies, endpoints use partial unique indexes for tenant vs
// global scopes, so the upsert is read-then-write by scope rather than a single
// ON CONFLICT target.
func (r *EndpointRepository) Upsert(ctx context.Context, e *domain.Endpoint) (*domain.Endpoint, error) {
	if e.ID == "" {
		e.ID = domain.NewID()
	}
	overrideJSON, _ := json.Marshal(e.RoutingOverride)
	if overrideJSON == nil {
		overrideJSON = []byte(`{}`)
	}
	var existingID string
	if e.TenantID == "" {
		_ = r.pool.QueryRow(ctx,
			`SELECT id::text FROM endpoints WHERE tenant_id IS NULL AND slug = $1`,
			e.Slug).Scan(&existingID)
	} else {
		_ = r.pool.QueryRow(ctx,
			`SELECT id::text FROM endpoints WHERE tenant_id = $1 AND slug = $2`,
			e.TenantID, e.Slug).Scan(&existingID)
	}
	if existingID != "" {
		_, err := r.pool.Exec(ctx, `
			UPDATE endpoints SET name=$2, description=$3, routing_override=$4,
				enabled=$5, updated_at=now() WHERE id=$1`,
			existingID, e.Name, e.Description, overrideJSON, e.Enabled)
		if err != nil {
			return nil, wrapDBError("update endpoint", err)
		}
		row := r.pool.QueryRow(ctx, `
			SELECT id, COALESCE(tenant_id::text,''), slug, name, description, routing_override, enabled, created_at, updated_at
			FROM endpoints WHERE id=$1`, existingID)
		var out domain.Endpoint
		var overrideRaw []byte
		if err := row.Scan(&out.ID, &out.TenantID, &out.Slug, &out.Name, &out.Description, &overrideRaw, &out.Enabled, &out.CreatedAt, &out.UpdatedAt); err != nil {
			return nil, wrapDBError("load endpoint", err)
		}
		if len(overrideRaw) > 0 && string(overrideRaw) != "{}" && string(overrideRaw) != "null" {
			var ov domain.EndpointRoutingOverride
			if err := json.Unmarshal(overrideRaw, &ov); err == nil {
				out.RoutingOverride = &ov
			}
		}
		return &out, nil
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO endpoints (id, tenant_id, slug, name, description, routing_override, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING id, COALESCE(tenant_id::text,''), slug, name, description, routing_override, enabled, created_at, updated_at`,
		e.ID, nullableUUID(e.TenantID), e.Slug, e.Name, e.Description, overrideJSON, e.Enabled)
	var out domain.Endpoint
	var overrideRaw []byte
	if err := row.Scan(&out.ID, &out.TenantID, &out.Slug, &out.Name, &out.Description, &overrideRaw, &out.Enabled, &out.CreatedAt, &out.UpdatedAt); err != nil {
		return nil, wrapDBError("upsert endpoint", err)
	}
	if len(overrideRaw) > 0 && string(overrideRaw) != "{}" && string(overrideRaw) != "null" {
		var ov domain.EndpointRoutingOverride
		if err := json.Unmarshal(overrideRaw, &ov); err == nil {
			out.RoutingOverride = &ov
		}
	}
	return &out, nil
}

// List returns endpoints.
func (r *EndpointRepository) List(ctx context.Context) ([]domain.Endpoint, error) {
	rs, err := r.pool.Query(ctx, `
		SELECT id, COALESCE(tenant_id::text,''), slug, name, description, routing_override, enabled, created_at, updated_at
		FROM endpoints ORDER BY slug`)
	if err != nil {
		return nil, wrapDBError("list endpoints", err)
	}
	defer rs.Close()
	var out []domain.Endpoint
	for rs.Next() {
		var e domain.Endpoint
		var overrideRaw []byte
		if err := rs.Scan(&e.ID, &e.TenantID, &e.Slug, &e.Name, &e.Description, &overrideRaw, &e.Enabled, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, wrapDBError("scan endpoint", err)
		}
		if len(overrideRaw) > 0 && string(overrideRaw) != "{}" && string(overrideRaw) != "null" {
			var ov domain.EndpointRoutingOverride
			if err := json.Unmarshal(overrideRaw, &ov); err == nil {
				e.RoutingOverride = &ov
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// GetByID returns an endpoint by identifier.
func (r *EndpointRepository) GetByID(ctx context.Context, id string) (*domain.Endpoint, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, COALESCE(tenant_id::text,''), slug, name, description, routing_override, enabled, created_at, updated_at
		FROM endpoints WHERE id = $1`, id)
	var out domain.Endpoint
	var overrideRaw []byte
	if err := row.Scan(&out.ID, &out.TenantID, &out.Slug, &out.Name, &out.Description,
		&overrideRaw, &out.Enabled, &out.CreatedAt, &out.UpdatedAt); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, wrapDBError("load endpoint", err)
	}
	if len(overrideRaw) > 0 && string(overrideRaw) != "{}" && string(overrideRaw) != "null" {
		var ov domain.EndpointRoutingOverride
		if err := json.Unmarshal(overrideRaw, &ov); err == nil {
			out.RoutingOverride = &ov
		}
	}
	return &out, nil
}

// Delete removes an endpoint by identifier.
func (r *EndpointRepository) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM endpoints WHERE id = $1`, id)
	if err != nil {
		return wrapDBError("delete endpoint", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrCodeNotFound, "endpoint not found")
	}
	return nil
}

// GetBySlug resolves an endpoint scope by slug, preferring the tenant's own
// scope over a global one. The request path uses this on every scoped call,
// so it is one indexed lookup and no more.
func (r *EndpointRepository) GetBySlug(ctx context.Context, tenantID, slug string) (*domain.Endpoint, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, COALESCE(tenant_id::text,''), slug, name, description, routing_override, enabled, created_at, updated_at
		FROM endpoints
		WHERE slug = $1 AND (tenant_id = $2 OR tenant_id IS NULL)
		ORDER BY tenant_id NULLS LAST
		LIMIT 1`, slug, nullableUUID(tenantID))
	var out domain.Endpoint
	var overrideRaw []byte
	if err := row.Scan(&out.ID, &out.TenantID, &out.Slug, &out.Name, &out.Description,
		&overrideRaw, &out.Enabled, &out.CreatedAt, &out.UpdatedAt); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, wrapDBError("load endpoint", err)
	}
	if len(overrideRaw) > 0 && string(overrideRaw) != "{}" && string(overrideRaw) != "null" {
		var ov domain.EndpointRoutingOverride
		if err := json.Unmarshal(overrideRaw, &ov); err == nil {
			out.RoutingOverride = &ov
		}
	}
	return &out, nil
}
