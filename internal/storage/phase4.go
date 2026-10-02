package storage

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shadowsafin/astrarouter/internal/domain"
)

// ---------------------------------------------------------------------------
// Phase 4 repositories: tools, tool policies, invocations and agent runs.
// ---------------------------------------------------------------------------

// ToolRepository stores the tool registry.
type ToolRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewToolRepository constructs the store.
func NewToolRepository(pool *pgxpool.Pool, logger *slog.Logger) *ToolRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &ToolRepository{pool: pool, logger: logger}
}

const toolColumns = `id, name, description, kind, owner, COALESCE(tenant_id::text, ''),
	parameters, strict, safety_level, executable, handler, version, enabled,
	requires_approval, labels, created_at, updated_at`

// Upsert creates or updates a tool by (owner scope, name).
func (r *ToolRepository) Upsert(ctx context.Context, spec *domain.ToolSpec) (*domain.ToolSpec, error) {
	if spec.ID == "" {
		spec.ID = domain.NewID()
	}
	parameters := spec.Parameters
	if len(parameters) == 0 {
		parameters = []byte(`{}`)
	}
	var parametersObj any
	if err := json.Unmarshal(parameters, &parametersObj); err != nil {
		return nil, domain.NewError(domain.ErrCodeInvalidRequest,
			"tool parameters must be a JSON object").Wrap(err)
	}
	labels, err := json.Marshal(orEmptyMap(spec.Labels))
	if err != nil {
		return nil, wrapDBError("encode tool labels", err)
	}

	// Platform tools have a NULL tenant_id, and a plain unique index treats
	// NULLs as distinct, so the conflict target is chosen by scope rather than
	// fixed. Exactly the read-then-write pattern the policy and endpoint
	// repositories use for the same reason.
	var existingID string
	if spec.TenantID == "" {
		_ = r.pool.QueryRow(ctx,
			`SELECT id::text FROM tools WHERE name = $1 AND tenant_id IS NULL`, spec.Name).Scan(&existingID)
	} else {
		_ = r.pool.QueryRow(ctx,
			`SELECT id::text FROM tools WHERE name = $1 AND tenant_id = $2`, spec.Name, spec.TenantID).Scan(&existingID)
	}

	kind := string(spec.Kind)
	if kind == "" {
		kind = string(domain.ToolExternal)
	}
	safety := string(spec.SafetyLevel)
	if safety == "" {
		safety = string(domain.SafetySafe)
	}

	if existingID != "" {
		_, err = r.pool.Exec(ctx, `
			UPDATE tools
			   SET description = $2, kind = $3, parameters = $4, strict = $5,
			       safety_level = $6, executable = $7, handler = $8, version = $9,
			       enabled = $10, requires_approval = $11, labels = $12,
			       updated_at = now()
			 WHERE id = $1`,
			existingID, spec.Description, kind, parametersObj, spec.Strict, safety,
			spec.Executable, spec.Handler, spec.Version, spec.Enabled,
			spec.RequiresApproval, labels)
		if err != nil {
			return nil, wrapDBError("update tool "+spec.Name, err)
		}
		return r.GetByID(ctx, existingID)
	}

	row := r.pool.QueryRow(ctx, `
		INSERT INTO tools (id, name, description, kind, owner, tenant_id, parameters, strict,
			safety_level, executable, handler, version, enabled, requires_approval, labels)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		RETURNING `+toolColumns,
		spec.ID, spec.Name, spec.Description, kind, string(spec.Owner), nullableUUID(spec.TenantID),
		parametersObj, spec.Strict, safety, spec.Executable, spec.Handler, spec.Version,
		spec.Enabled, spec.RequiresApproval, labels)

	out, err := scanTool(row)
	if err != nil {
		return nil, wrapDBError("create tool "+spec.Name, err)
	}
	return out, nil
}

// List returns every registered tool.
func (r *ToolRepository) List(ctx context.Context) ([]domain.ToolSpec, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+toolColumns+` FROM tools ORDER BY name`)
	if err != nil {
		return nil, wrapDBError("list tools", err)
	}
	defer rows.Close()

	var out []domain.ToolSpec
	for rows.Next() {
		spec, err := scanTool(rows)
		if err != nil {
			return nil, wrapDBError("scan tool", err)
		}
		out = append(out, *spec)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("list tools", err)
	}
	return out, nil
}

// GetByID returns one tool.
func (r *ToolRepository) GetByID(ctx context.Context, id string) (*domain.ToolSpec, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+toolColumns+` FROM tools WHERE id = $1`, id)
	spec, err := scanTool(row)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, wrapDBError("load tool", err)
	}
	return spec, nil
}

// SetEnabled toggles a tool without deleting it.
func (r *ToolRepository) SetEnabled(ctx context.Context, id string, enabled bool) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE tools SET enabled = $2, updated_at = now() WHERE id = $1`, id, enabled)
	if err != nil {
		return wrapDBError("set tool enabled", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrCodeNotFound, "tool not found")
	}
	return nil
}

// Delete removes a tool. Invocation history keeps its own copy of the name, so
// removing a registry entry never orphans the audit trail.
func (r *ToolRepository) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM tools WHERE id = $1`, id)
	if err != nil {
		return wrapDBError("delete tool", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrCodeNotFound, "tool not found")
	}
	return nil
}

func scanTool(row pgx.Row) (*domain.ToolSpec, error) {
	var (
		spec       domain.ToolSpec
		kind       string
		owner      string
		safety     string
		parameters []byte
		labels     []byte
	)
	if err := row.Scan(&spec.ID, &spec.Name, &spec.Description, &kind, &owner, &spec.TenantID,
		&parameters, &spec.Strict, &safety, &spec.Executable, &spec.Handler,
		&spec.Version, &spec.Enabled, &spec.RequiresApproval, &labels,
		&spec.CreatedAt, &spec.UpdatedAt); err != nil {
		return nil, err
	}
	spec.Kind = domain.ToolKind(kind)
	spec.Owner = domain.ToolOwner(owner)
	spec.SafetyLevel = domain.SafetyLevel(safety)
	if len(parameters) > 0 {
		spec.Parameters = parameters
	}
	if len(labels) > 0 {
		_ = json.Unmarshal(labels, &spec.Labels)
	}
	return &spec, nil
}

// ---------------------------------------------------------------------------
// Tool policies
// ---------------------------------------------------------------------------

// ToolPolicyRepository stores tool-use policies.
type ToolPolicyRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewToolPolicyRepository constructs the store.
func NewToolPolicyRepository(pool *pgxpool.Pool, logger *slog.Logger) *ToolPolicyRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &ToolPolicyRepository{pool: pool, logger: logger}
}

const toolPolicyColumns = `id, COALESCE(tenant_id::text, ''), name, enabled, mode,
	allowed_tools, denied_tools, allowed_providers, denied_providers, require_approval,
	max_steps, max_tool_calls, max_result_bytes, max_run_seconds, block_sensitive,
	created_at, updated_at`

// Upsert creates or updates a policy by (scope, name).
func (r *ToolPolicyRepository) Upsert(ctx context.Context, policy *domain.ToolPolicy) (*domain.ToolPolicy, error) {
	if policy.ID == "" {
		policy.ID = domain.NewID()
	}
	policy.Normalize()

	var existingID string
	if policy.TenantID == "" {
		_ = r.pool.QueryRow(ctx,
			`SELECT id::text FROM tool_policies WHERE name = $1 AND tenant_id IS NULL`, policy.Name).Scan(&existingID)
	} else {
		_ = r.pool.QueryRow(ctx,
			`SELECT id::text FROM tool_policies WHERE name = $1 AND tenant_id = $2`, policy.Name, policy.TenantID).Scan(&existingID)
	}

	arrays := [][]string{
		policy.AllowedTools, policy.DeniedTools, policy.AllowedProviders, policy.DeniedProviders,
	}
	for i := range arrays {
		if arrays[i] == nil {
			arrays[i] = []string{}
		}
	}

	if existingID != "" {
		_, err := r.pool.Exec(ctx, `
			UPDATE tool_policies
			   SET enabled = $2, mode = $3, allowed_tools = $4, denied_tools = $5,
			       allowed_providers = $6, denied_providers = $7, require_approval = $8,
			       max_steps = $9, max_tool_calls = $10, max_result_bytes = $11,
			       max_run_seconds = $12, block_sensitive = $13, updated_at = now()
			 WHERE id = $1`,
			existingID, policy.Enabled, string(policy.Mode), arrays[0], arrays[1], arrays[2],
			arrays[3], policy.RequireApproval, policy.MaxSteps, policy.MaxToolCalls,
			policy.MaxResultBytes, policy.MaxRunSeconds, policy.BlockSensitive)
		if err != nil {
			return nil, wrapDBError("update tool policy "+policy.Name, err)
		}
		return r.GetByID(ctx, existingID)
	}

	row := r.pool.QueryRow(ctx, `
		INSERT INTO tool_policies (id, tenant_id, name, enabled, mode, allowed_tools,
			denied_tools, allowed_providers, denied_providers, require_approval, max_steps,
			max_tool_calls, max_result_bytes, max_run_seconds, block_sensitive)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		RETURNING `+toolPolicyColumns,
		policy.ID, nullableUUID(policy.TenantID), policy.Name, policy.Enabled,
		string(policy.Mode), arrays[0], arrays[1], arrays[2], arrays[3],
		policy.RequireApproval, policy.MaxSteps, policy.MaxToolCalls,
		policy.MaxResultBytes, policy.MaxRunSeconds, policy.BlockSensitive)

	out, err := scanToolPolicy(row)
	if err != nil {
		return nil, wrapDBError("create tool policy "+policy.Name, err)
	}
	return out, nil
}

// List returns every policy.
func (r *ToolPolicyRepository) List(ctx context.Context) ([]domain.ToolPolicy, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+toolPolicyColumns+` FROM tool_policies ORDER BY name`)
	if err != nil {
		return nil, wrapDBError("list tool policies", err)
	}
	defer rows.Close()

	var out []domain.ToolPolicy
	for rows.Next() {
		policy, err := scanToolPolicy(rows)
		if err != nil {
			return nil, wrapDBError("scan tool policy", err)
		}
		out = append(out, *policy)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("list tool policies", err)
	}
	return out, nil
}

// GetByID returns one policy.
func (r *ToolPolicyRepository) GetByID(ctx context.Context, id string) (*domain.ToolPolicy, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+toolPolicyColumns+` FROM tool_policies WHERE id = $1`, id)
	policy, err := scanToolPolicy(row)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, wrapDBError("load tool policy", err)
	}
	return policy, nil
}

// ResolveFor returns the policy that applies to a tenant: the tenant's own
// policy when one exists, otherwise the platform default, otherwise the
// built-in defaults. A nil result means "use the defaults".
func (r *ToolPolicyRepository) ResolveFor(ctx context.Context, tenantID string) (*domain.ToolPolicy, error) {
	if tenantID != "" {
		row := r.pool.QueryRow(ctx,
			`SELECT `+toolPolicyColumns+` FROM tool_policies WHERE tenant_id = $1 ORDER BY name LIMIT 1`,
			tenantID)
		policy, err := scanToolPolicy(row)
		if err == nil {
			return policy, nil
		}
		if !isNotFound(err) {
			return nil, wrapDBError("resolve tenant tool policy", err)
		}
	}
	row := r.pool.QueryRow(ctx,
		`SELECT `+toolPolicyColumns+` FROM tool_policies WHERE tenant_id IS NULL ORDER BY name LIMIT 1`)
	policy, err := scanToolPolicy(row)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, wrapDBError("resolve platform tool policy", err)
	}
	return policy, nil
}

// Delete removes a policy.
func (r *ToolPolicyRepository) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM tool_policies WHERE id = $1`, id)
	if err != nil {
		return wrapDBError("delete tool policy", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrCodeNotFound, "tool policy not found")
	}
	return nil
}

func scanToolPolicy(row pgx.Row) (*domain.ToolPolicy, error) {
	var (
		policy domain.ToolPolicy
		mode   string
	)
	if err := row.Scan(&policy.ID, &policy.TenantID, &policy.Name, &policy.Enabled, &mode,
		&policy.AllowedTools, &policy.DeniedTools, &policy.AllowedProviders,
		&policy.DeniedProviders, &policy.RequireApproval, &policy.MaxSteps,
		&policy.MaxToolCalls, &policy.MaxResultBytes, &policy.MaxRunSeconds,
		&policy.BlockSensitive, &policy.CreatedAt, &policy.UpdatedAt); err != nil {
		return nil, err
	}
	policy.Mode = domain.ToolPolicyMode(mode)
	return &policy, nil
}

// ---------------------------------------------------------------------------
// Tool invocations and executions
// ---------------------------------------------------------------------------

// ToolInvocationRepository stores tool-call history.
type ToolInvocationRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewToolInvocationRepository constructs the store.
func NewToolInvocationRepository(pool *pgxpool.Pool, logger *slog.Logger) *ToolInvocationRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &ToolInvocationRepository{pool: pool, logger: logger}
}

// Insert records one tool call.
func (r *ToolInvocationRepository) Insert(ctx context.Context, invocation *domain.ToolInvocation) error {
	if invocation.ID == "" {
		invocation.ID = domain.NewID()
	}
	if invocation.CreatedAt.IsZero() {
		invocation.CreatedAt = domain.Now()
	}
	parsed := invocation.ParsedArguments
	if parsed == nil {
		parsed = map[string]any{}
	}
	parsedJSON, err := json.Marshal(parsed)
	if err != nil {
		return wrapDBError("encode tool arguments", err)
	}
	status := string(invocation.Status)
	if status == "" {
		status = string(domain.InvocationPending)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO tool_invocations (id, request_id, tenant_id, run_id, step, tool_call_id,
			tool_name, arguments, parsed_arguments, status, deny_reason, latency_ms,
			result_bytes, error_code, provider, model)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		invocation.ID, invocation.RequestID.String(), nullableUUID(invocation.TenantID),
		nullableUUID(invocation.RunID), maxInt(invocation.Step, 1), invocation.ToolCallID,
		invocation.ToolName, invocation.Arguments, parsedJSON, status,
		invocation.DenyReason, invocation.LatencyMS, invocation.ResultBytes,
		invocation.ErrorCode, invocation.Provider, invocation.Model)
	if err != nil {
		return wrapDBError("insert tool invocation", err)
	}
	return nil
}

// ToolInvocationFilter narrows a history listing.
type ToolInvocationFilter struct {
	TenantID  string
	RequestID string
	ToolName  string
	ToolID    string
	Status    string
	Limit     int
	Offset    int
}

// List returns recent invocations, newest first.
func (r *ToolInvocationRepository) List(ctx context.Context, filter ToolInvocationFilter) ([]domain.ToolInvocation, error) {
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, request_id, COALESCE(tenant_id::text, ''), COALESCE(run_id::text, ''),
		       step, tool_call_id, tool_name, arguments, parsed_arguments, status, deny_reason,
		       latency_ms, result_bytes, error_code, provider, model, created_at
		  FROM tool_invocations
		 WHERE ($1 = '' OR tenant_id::text = $1)
		   AND ($2 = '' OR request_id = $2)
		   AND ($3 = '' OR tool_name = $3)
		   AND ($4 = '' OR tool_call_id = $4)
		   AND ($5 = '' OR status = $5)
		 ORDER BY created_at DESC
		 LIMIT $6 OFFSET $7`,
		filter.TenantID, filter.RequestID, filter.ToolName, filter.ToolID, filter.Status,
		filter.Limit, filter.Offset)
	if err != nil {
		return nil, wrapDBError("list tool invocations", err)
	}
	defer rows.Close()

	var out []domain.ToolInvocation
	for rows.Next() {
		var (
			invocation domain.ToolInvocation
			status     string
			parsed     []byte
		)
		if err := rows.Scan(&invocation.ID, &invocation.RequestID, &invocation.TenantID,
			&invocation.RunID, &invocation.Step, &invocation.ToolCallID, &invocation.ToolName,
			&invocation.Arguments, &parsed, &status, &invocation.DenyReason,
			&invocation.LatencyMS, &invocation.ResultBytes, &invocation.ErrorCode,
			&invocation.Provider, &invocation.Model, &invocation.CreatedAt); err != nil {
			return nil, wrapDBError("scan tool invocation", err)
		}
		invocation.Status = domain.InvocationStatus(status)
		if len(parsed) > 0 {
			_ = json.Unmarshal(parsed, &invocation.ParsedArguments)
		}
		out = append(out, invocation)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("list tool invocations", err)
	}
	return out, nil
}

// InsertExecution records a tool's result payload.
func (r *ToolInvocationRepository) InsertExecution(ctx context.Context, execution *domain.ToolExecution) error {
	if execution.ID == "" {
		execution.ID = domain.NewID()
	}
	if execution.CreatedAt.IsZero() {
		execution.CreatedAt = domain.Now()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO tool_executions (id, invocation_id, tool_name, content, success,
			error_message, latency_ms)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		execution.ID, execution.InvocationID, execution.ToolName, execution.Content,
		execution.Success, execution.ErrorMessage, execution.LatencyMS)
	if err != nil {
		return wrapDBError("insert tool execution", err)
	}
	return nil
}

// ExecutionFor returns the stored payload for an invocation.
func (r *ToolInvocationRepository) ExecutionFor(ctx context.Context, invocationID string) (*domain.ToolExecution, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id::text, invocation_id::text, tool_name, content, success, error_message,
		       latency_ms, created_at
		  FROM tool_executions
		 WHERE invocation_id = $1
		 ORDER BY created_at DESC
		 LIMIT 1`, invocationID)
	var execution domain.ToolExecution
	if err := row.Scan(&execution.ID, &execution.InvocationID, &execution.ToolName,
		&execution.Content, &execution.Success, &execution.ErrorMessage,
		&execution.LatencyMS, &execution.CreatedAt); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, wrapDBError("load tool execution", err)
	}
	return &execution, nil
}

// ---------------------------------------------------------------------------
// Agent runs
// ---------------------------------------------------------------------------

// AgentRunRepository stores bounded multi-step runs.
type AgentRunRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewAgentRunRepository constructs the store.
func NewAgentRunRepository(pool *pgxpool.Pool, logger *slog.Logger) *AgentRunRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &AgentRunRepository{pool: pool, logger: logger}
}

// AgentRun is the durable view of one bounded tool run.
type AgentRun struct {
	ID         string                `json:"id"`
	RequestID  string                `json:"request_id,omitempty"`
	TenantID   string                `json:"tenant_id,omitempty"`
	Status     domain.AgentRunStatus `json:"status"`
	Steps      int                   `json:"steps"`
	ToolCalls  int                   `json:"tool_calls"`
	Provider   string                `json:"provider,omitempty"`
	Model      string                `json:"model,omitempty"`
	LatencyMS  int64                 `json:"total_latency_ms"`
	StopReason string                `json:"stop_reason,omitempty"`
	CreatedAt  time.Time             `json:"created_at"`
}

// AgentStep is one model round-trip or tool execution inside a run.
type AgentStep struct {
	ID        string         `json:"id"`
	RunID     string         `json:"run_id"`
	Step      int            `json:"step"`
	Kind      string         `json:"kind"`
	Provider  string         `json:"provider,omitempty"`
	Model     string         `json:"model,omitempty"`
	ToolCalls int            `json:"tool_calls"`
	LatencyMS int64          `json:"latency_ms"`
	Tokens    int            `json:"tokens"`
	Detail    map[string]any `json:"detail,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// Upsert creates or updates a run's header.
func (r *AgentRunRepository) Upsert(ctx context.Context, run *AgentRun) error {
	if run.ID == "" {
		run.ID = domain.NewID()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO agent_runs (id, request_id, tenant_id, status, steps, tool_calls,
			provider, model, total_latency_ms, stop_reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO UPDATE
			SET status = EXCLUDED.status, steps = EXCLUDED.steps,
			    tool_calls = EXCLUDED.tool_calls, provider = EXCLUDED.provider,
			    model = EXCLUDED.model, total_latency_ms = EXCLUDED.total_latency_ms,
			    stop_reason = EXCLUDED.stop_reason, updated_at = now()`,
		run.ID, run.RequestID, nullableUUID(run.TenantID), string(run.Status), run.Steps,
		run.ToolCalls, run.Provider, run.Model, run.LatencyMS, run.StopReason)
	if err != nil {
		return wrapDBError("upsert agent run", err)
	}
	return nil
}

// InsertStep records one model round-trip or tool execution inside a run.
func (r *AgentRunRepository) InsertStep(ctx context.Context, runID string, step *AgentStep) error {
	if step.ID == "" {
		step.ID = domain.NewID()
	}
	detail, err := json.Marshal(orEmptyMapAny(step.Detail))
	if err != nil {
		return wrapDBError("encode agent step detail", err)
	}
	kind := step.Kind
	if kind == "" {
		kind = "model"
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO agent_steps (id, run_id, step, kind, provider, model, tool_calls,
			latency_ms, tokens, detail)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		step.ID, runID, step.Step, kind, step.Provider, step.Model, step.ToolCalls,
		step.LatencyMS, step.Tokens, detail)
	if err != nil {
		return wrapDBError("insert agent step", err)
	}
	return nil
}

// GetByID returns a run with its steps.
func (r *AgentRunRepository) GetByID(ctx context.Context, id string) (*AgentRun, []AgentStep, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id::text, request_id, COALESCE(tenant_id::text, ''), status, steps, tool_calls,
		       provider, model, total_latency_ms, stop_reason, created_at
		  FROM agent_runs WHERE id = $1`, id)
	var run AgentRun
	var status string
	if err := row.Scan(&run.ID, &run.RequestID, &run.TenantID, &status, &run.Steps,
		&run.ToolCalls, &run.Provider, &run.Model, &run.LatencyMS, &run.StopReason,
		&run.CreatedAt); err != nil {
		if isNotFound(err) {
			return nil, nil, nil
		}
		return nil, nil, wrapDBError("load agent run", err)
	}
	run.Status = domain.AgentRunStatus(status)

	stepRows, err := r.pool.Query(ctx, `
		SELECT id::text, run_id::text, step, kind, provider, model, tool_calls, latency_ms,
		       tokens, detail
		  FROM agent_steps WHERE run_id = $1 ORDER BY step, created_at`, id)
	if err != nil {
		return nil, nil, wrapDBError("list agent steps", err)
	}
	defer stepRows.Close()

	var steps []AgentStep
	for stepRows.Next() {
		var (
			step   AgentStep
			kind   string
			detail []byte
		)
		if err := stepRows.Scan(&step.ID, &step.RunID, &step.Step, &kind, &step.Provider,
			&step.Model, &step.ToolCalls, &step.LatencyMS, &step.Tokens, &detail); err != nil {
			return nil, nil, wrapDBError("scan agent step", err)
		}
		step.Kind = kind
		if len(detail) > 0 {
			_ = json.Unmarshal(detail, &step.Detail)
		}
		steps = append(steps, step)
	}
	if err := stepRows.Err(); err != nil {
		return nil, nil, wrapDBError("list agent steps", err)
	}
	return &run, steps, nil
}

// ListRuns returns recent runs, newest first.
func (r *AgentRunRepository) ListRuns(ctx context.Context, tenantID string, limit int) ([]AgentRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, request_id, COALESCE(tenant_id::text, ''), status, steps, tool_calls,
		       provider, model, total_latency_ms, stop_reason, created_at
		  FROM agent_runs
		 WHERE ($1 = '' OR tenant_id::text = $1)
		 ORDER BY created_at DESC
		 LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, wrapDBError("list agent runs", err)
	}
	defer rows.Close()

	var out []AgentRun
	for rows.Next() {
		var (
			run    AgentRun
			status string
		)
		if err := rows.Scan(&run.ID, &run.RequestID, &run.TenantID, &status, &run.Steps,
			&run.ToolCalls, &run.Provider, &run.Model, &run.LatencyMS, &run.StopReason,
			&run.CreatedAt); err != nil {
			return nil, wrapDBError("scan agent run", err)
		}
		run.Status = domain.AgentRunStatus(status)
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("list agent runs", err)
	}
	return out, nil
}

// maxInt returns at least one.
func maxInt(value, floor int) int {
	if value < floor {
		return floor
	}
	return value
}
