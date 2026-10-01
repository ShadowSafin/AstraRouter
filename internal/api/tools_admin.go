package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/shadowsafin/corerouter/internal/domain"
	"github.com/shadowsafin/corerouter/internal/schema"
	"github.com/shadowsafin/corerouter/internal/storage"
	"github.com/shadowsafin/corerouter/internal/tools"
)

// Phase 4 administrative surface: the tool registry, tool policies, invocation
// history and agent-run traces.
//
// The handlers are deliberately thin. Validation lives in admin/schema, policy
// decisions live in tools, and persistence lives in storage, so this file is
// only transport: decode, delegate, audit, encode.

// maxToolParameterBytes bounds a stored tool schema. A schema is a small
// document by definition; anything larger is a paste accident.
const maxToolParameterBytes = 64 * 1024

// toolWrite is the body of tool create and update.
type toolWrite struct {
	Name             string            `json:"name"`
	Description      string            `json:"description,omitempty"`
	Kind             string            `json:"kind,omitempty"`
	Owner            string            `json:"owner,omitempty"`
	TenantID         string            `json:"tenant_id,omitempty"`
	Parameters       json.RawMessage   `json:"parameters,omitempty"`
	Strict           bool              `json:"strict,omitempty"`
	SafetyLevel      string            `json:"safety_level,omitempty"`
	Handler          string            `json:"handler,omitempty"`
	Version          string            `json:"version,omitempty"`
	Enabled          *bool             `json:"enabled,omitempty"`
	RequiresApproval bool              `json:"requires_approval,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
}

// toolPolicyWrite is the tool policy write body.
//
// It exists so 'enabled' can be told apart from an omitted field. Decoding
// straight into domain.ToolPolicy would make an absent 'enabled' mean false,
// so creating a policy with only a mode and bounds would store a policy that
// is silently inert: the operator reads "mode: automatic" and every request
// still comes back tool-disabled.
type toolPolicyWrite struct {
	Name             string                `json:"name"`
	TenantID         string                `json:"tenant_id,omitempty"`
	Enabled          *bool                 `json:"enabled,omitempty"`
	Mode             domain.ToolPolicyMode `json:"mode,omitempty"`
	AllowedTools     []string              `json:"allowed_tools,omitempty"`
	DeniedTools      []string              `json:"denied_tools,omitempty"`
	AllowedProviders []string              `json:"allowed_providers,omitempty"`
	DeniedProviders  []string              `json:"denied_providers,omitempty"`
	RequireApproval  bool                  `json:"require_approval,omitempty"`
	MaxSteps         int                   `json:"max_steps,omitempty"`
	MaxToolCalls     int                   `json:"max_tool_calls,omitempty"`
	MaxResultBytes   int                   `json:"max_result_bytes,omitempty"`
	MaxRunSeconds    int                   `json:"max_run_seconds,omitempty"`
	BlockSensitive   bool                  `json:"block_sensitive,omitempty"`
}

// policy converts the write body into a policy, defaulting 'enabled' to true.
//
// A policy is written in order to take effect, so an omitted flag means "on".
// The alternative silently stores a dead policy, which is the worst possible
// failure mode for a control-plane setting.
func (b toolPolicyWrite) policy() domain.ToolPolicy {
	enabled := true
	if b.Enabled != nil {
		enabled = *b.Enabled
	}
	return domain.ToolPolicy{
		Name:             b.Name,
		TenantID:         b.TenantID,
		Enabled:          enabled,
		Mode:             b.Mode,
		AllowedTools:     b.AllowedTools,
		DeniedTools:      b.DeniedTools,
		AllowedProviders: b.AllowedProviders,
		DeniedProviders:  b.DeniedProviders,
		RequireApproval:  b.RequireApproval,
		MaxSteps:         b.MaxSteps,
		MaxToolCalls:     b.MaxToolCalls,
		MaxResultBytes:   b.MaxResultBytes,
		MaxRunSeconds:    b.MaxRunSeconds,
		BlockSensitive:   b.BlockSensitive,
	}
}

// handleAdminListTools serves GET /admin/v1/tools.
func (s *Server) handleAdminListTools(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 10*time.Second)
	defer cancel()
	rc := requestContext(ctx)

	if s.repos == nil || s.repos.Tools == nil {
		writeError(w, domain.NewError(domain.ErrCodeInternal, "the tool registry is unavailable"),
			metaFromContext(rc, nil))
		return
	}

	tools, err := s.repos.Tools.List(ctx)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}

	// Built-in handler names travel with the list so the dashboard can offer the
	// valid choices instead of a free-text field that produces runtime errors.
	writeJSON(w, http.StatusOK, map[string]any{
		"tools":            tools,
		"builtin_handlers": tools2HandlerNames(),
	})
}

// tools2HandlerNames exposes the built-in handler list.
func tools2HandlerNames() []string { return tools.HandlerNames }

// handleAdminCreateTool serves POST /admin/v1/tools.
func (s *Server) handleAdminCreateTool(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 10*time.Second)
	defer cancel()
	rc := requestContext(ctx)

	if s.repos == nil || s.repos.Tools == nil {
		writeError(w, domain.NewError(domain.ErrCodeInternal, "the tool registry is unavailable"),
			metaFromContext(rc, nil))
		return
	}

	var body toolWrite
	if err := decodeJSONBody(r, maxToolParameterBytes, &body); err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	spec, err := s.toolSpecFromWrite(&body)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}

	saved, err := s.repos.Tools.Upsert(ctx, spec)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}

	s.audit(ctx, rc, domain.AuditCreate, domain.ResourceTool, saved.ID, nil, toolAuditFields(saved))
	writeJSON(w, http.StatusCreated, saved)
}

// handleAdminUpdateTool serves PUT /admin/v1/tools/{id}.
func (s *Server) handleAdminUpdateTool(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 10*time.Second)
	defer cancel()
	rc := requestContext(ctx)

	if s.repos == nil || s.repos.Tools == nil {
		writeError(w, domain.NewError(domain.ErrCodeInternal, "the tool registry is unavailable"),
			metaFromContext(rc, nil))
		return
	}

	id := chiURLParam(r, "id")
	existing, err := s.repos.Tools.GetByID(ctx, id)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	if existing == nil {
		writeError(w, domain.NewError(domain.ErrCodeNotFound, "tool not found"),
			metaFromContext(rc, nil))
		return
	}

	var body toolWrite
	if err := decodeJSONBody(r, maxToolParameterBytes, &body); err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	before := toolAuditFields(existing)

	// Name and owner scope are the natural key; changing either would silently
	// create a different tool and orphan the old one.
	if body.Name != "" && body.Name != existing.Name {
		writeError(w, domain.NewError(domain.ErrCodeInvalidRequest,
			"tool name is immutable; delete and recreate to rename"),
			metaFromContext(rc, nil))
		return
	}
	spec, err := s.toolSpecFromWrite(&body)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	spec.ID = existing.ID
	spec.Name = existing.Name
	spec.Owner = existing.Owner
	spec.TenantID = existing.TenantID
	if body.Enabled == nil {
		spec.Enabled = existing.Enabled
	}
	if !body.RequiresApproval && existing.RequiresApproval {
		spec.RequiresApproval = existing.RequiresApproval
	}

	saved, err := s.repos.Tools.Upsert(ctx, spec)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}

	s.audit(ctx, rc, domain.AuditUpdate, domain.ResourceTool, saved.ID, before, toolAuditFields(saved))
	writeJSON(w, http.StatusOK, saved)
}

// handleAdminSetToolEnabled serves POST /admin/v1/tools/{id}/enabled.
func (s *Server) handleAdminSetToolEnabled(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 10*time.Second)
	defer cancel()
	rc := requestContext(ctx)

	if s.repos == nil || s.repos.Tools == nil {
		writeError(w, domain.NewError(domain.ErrCodeInternal, "the tool registry is unavailable"),
			metaFromContext(rc, nil))
		return
	}

	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSONBody(r, 1<<16, &body); err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}

	id := chiURLParam(r, "id")
	existing, err := s.repos.Tools.GetByID(ctx, id)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	if existing == nil {
		writeError(w, domain.NewError(domain.ErrCodeNotFound, "tool not found"),
			metaFromContext(rc, nil))
		return
	}
	if err := s.repos.Tools.SetEnabled(ctx, id, body.Enabled); err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}

	action := domain.AuditEnable
	if !body.Enabled {
		action = domain.AuditDisable
	}
	s.audit(ctx, rc, action, domain.ResourceTool, id,
		map[string]any{"enabled": existing.Enabled}, map[string]any{"enabled": body.Enabled})

	writeJSON(w, http.StatusOK, map[string]any{"id": id, "enabled": body.Enabled})
}

// handleAdminDeleteTool serves DELETE /admin/v1/tools/{id}.
func (s *Server) handleAdminDeleteTool(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 10*time.Second)
	defer cancel()
	rc := requestContext(ctx)

	if s.repos == nil || s.repos.Tools == nil {
		writeError(w, domain.NewError(domain.ErrCodeInternal, "the tool registry is unavailable"),
			metaFromContext(rc, nil))
		return
	}

	id := chiURLParam(r, "id")
	existing, err := s.repos.Tools.GetByID(ctx, id)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	if existing == nil {
		writeError(w, domain.NewError(domain.ErrCodeNotFound, "tool not found"),
			metaFromContext(rc, nil))
		return
	}
	if err := s.repos.Tools.Delete(ctx, id); err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}

	// History keeps its own copy of the tool name, so removing a registry entry
	// never orphans an audit trail.
	s.audit(ctx, rc, domain.AuditDelete, domain.ResourceTool, id, toolAuditFields(existing), nil)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// toolSpecFromWrite validates a tool write into a domain spec.
//
// The schema is compiled here rather than at call time: a tool whose schema does
// not compile would fail on the first call that needed it, in the middle of an
// agent run, which is the worst possible time to discover a typo.
func (s *Server) toolSpecFromWrite(body *toolWrite) (*domain.ToolSpec, error) {
	if strings.TrimSpace(body.Name) == "" {
		return nil, domain.NewError(domain.ErrCodeInvalidRequest, "'name' is required")
	}
	if len(body.Name) > 64 {
		return nil, domain.NewError(domain.ErrCodeInvalidRequest,
			"'name' exceeds 64 characters")
	}
	if _, err := domain.ParseToolChoice(nil, []domain.Tool{{
		Type:     "function",
		Function: domain.FunctionDefinition{Name: body.Name},
	}}); err != nil {
		// Reuses the same name rule every provider enforces, so a tool that
		// registers here can actually be called.
		return nil, err
	}

	kind := domain.ToolKind(body.Kind)
	if kind == "" {
		kind = domain.ToolExternal
	}
	if !kind.Valid() {
		return nil, domain.Errorf(domain.ErrCodeInvalidRequest,
			"unknown tool kind %q; expected builtin or external", body.Kind)
	}

	owner := domain.ToolOwner(body.Owner)
	if owner == "" {
		owner = domain.OwnerPlatform
	}
	if !owner.Valid() {
		return nil, domain.Errorf(domain.ErrCodeInvalidRequest,
			"unknown tool owner %q; expected platform or tenant", body.Owner)
	}
	if owner == domain.OwnerTenant && strings.TrimSpace(body.TenantID) == "" {
		return nil, domain.NewError(domain.ErrCodeInvalidRequest,
			"a tenant tool requires 'tenant_id'")
	}

	safety := domain.SafetyLevel(body.SafetyLevel)
	if safety == "" {
		safety = domain.SafetySafe
	}
	if !safety.Valid() {
		return nil, domain.Errorf(domain.ErrCodeInvalidRequest,
			"unknown safety_level %q; expected safe, sensitive or dangerous", body.SafetyLevel)
	}

	parameters := body.Parameters
	if len(parameters) == 0 {
		parameters = json.RawMessage(`{"type": "object"}`)
	}
	if len(parameters) > maxToolParameterBytes {
		return nil, domain.NewError(domain.ErrCodeInvalidRequest,
			"tool parameters exceed the 64 KiB limit")
	}
	if _, err := schema.Compile(parameters); err != nil {
		return nil, domain.Errorf(domain.ErrCodeInvalidRequest,
			"tool parameters are not a usable JSON schema: %s", err.Error())
	}

	// Executability is derived, not trusted: a tool is runnable only when it is
	// a built-in with a known handler. Anything else is registered for
	// discovery and executed by the client.
	handler := body.Handler
	executable := false
	if kind == domain.ToolBuiltin {
		if handler == "" {
			// A built-in with no handler is a mistake, not a disabled tool.
			return nil, domain.NewError(domain.ErrCodeInvalidRequest,
				"a builtin tool requires 'handler'; available: "+
					strings.Join(tools.HandlerNames, ", "))
		}
		if !tools.HandlerKnown(handler) {
			return nil, domain.Errorf(domain.ErrCodeInvalidRequest,
				"unknown handler %q; available: %s", handler, strings.Join(tools.HandlerNames, ", "))
		}
		executable = true
	} else {
		handler = ""
	}

	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}

	return &domain.ToolSpec{
		ID:               domain.NewID(),
		Name:             body.Name,
		Description:      body.Description,
		Kind:             kind,
		Owner:            owner,
		TenantID:         body.TenantID,
		Parameters:       parameters,
		Strict:           body.Strict,
		SafetyLevel:      safety,
		Executable:       executable,
		Handler:          handler,
		Version:          body.Version,
		Enabled:          enabled,
		RequiresApproval: body.RequiresApproval,
		Labels:           body.Labels,
	}, nil
}

// toolAuditFields renders a tool for an audit event without its schema blob.
func toolAuditFields(spec *domain.ToolSpec) map[string]any {
	if spec == nil {
		return nil
	}
	return map[string]any{
		"name":        spec.Name,
		"kind":        string(spec.Kind),
		"owner":       string(spec.Owner),
		"enabled":     spec.Enabled,
		"executable":  spec.Executable,
		"safety":      string(spec.SafetyLevel),
		"approval":    spec.RequiresApproval,
		"description": spec.Description,
	}
}

// ---------------------------------------------------------------------------
// Tool policies
// ---------------------------------------------------------------------------

// handleAdminListToolPolicies serves GET /admin/v1/tool-policies.
func (s *Server) handleAdminListToolPolicies(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 10*time.Second)
	defer cancel()
	rc := requestContext(ctx)

	if s.repos == nil || s.repos.ToolPolicies == nil {
		writeError(w, domain.NewError(domain.ErrCodeInternal, "the tool policy store is unavailable"),
			metaFromContext(rc, nil))
		return
	}

	policies, err := s.repos.ToolPolicies.List(ctx)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": policies})
}

// handleAdminUpsertToolPolicy serves PUT /admin/v1/tool-policies.
func (s *Server) handleAdminUpsertToolPolicy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 10*time.Second)
	defer cancel()
	rc := requestContext(ctx)

	if s.repos == nil || s.repos.ToolPolicies == nil {
		writeError(w, domain.NewError(domain.ErrCodeInternal, "the tool policy store is unavailable"),
			metaFromContext(rc, nil))
		return
	}

	var body toolPolicyWrite
	if err := decodeJSONBody(r, 1<<20, &body); err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, domain.NewError(domain.ErrCodeInvalidRequest,
			"'name' is required"), metaFromContext(rc, nil))
		return
	}
	if body.Mode != "" && !body.Mode.Valid() {
		writeError(w, domain.Errorf(domain.ErrCodeInvalidRequest,
			"unknown mode %q; expected disabled, manual or automatic", body.Mode),
			metaFromContext(rc, nil))
		return
	}
	policy := body.policy()

	var before map[string]any
	existing, err := s.repos.ToolPolicies.List(ctx)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	for _, candidate := range existing {
		if candidate.Name == policy.Name && candidate.TenantID == policy.TenantID {
			before = map[string]any{
				"mode":           string(candidate.Mode),
				"enabled":        candidate.Enabled,
				"max_steps":      candidate.MaxSteps,
				"max_tool_calls": candidate.MaxToolCalls,
			}
			// An omitted 'enabled' on an update means "leave it alone". Reading it
			// as false would silently disable a live policy every time an
			// operator edited an unrelated bound.
			if body.Enabled == nil {
				policy.Enabled = candidate.Enabled
			}
		}
	}
	policy.Normalize()

	saved, err := s.repos.ToolPolicies.Upsert(ctx, &policy)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}

	action := domain.AuditUpdate
	if before == nil {
		action = domain.AuditCreate
	}
	s.audit(ctx, rc, action, domain.ResourceToolPolicy, saved.ID, before, map[string]any{
		"name":             saved.Name,
		"mode":             string(saved.Mode),
		"enabled":          saved.Enabled,
		"max_steps":        saved.MaxSteps,
		"max_tool_calls":   saved.MaxToolCalls,
		"require_approval": saved.RequireApproval,
	})

	writeJSON(w, http.StatusOK, saved)
}

// handleAdminDeleteToolPolicy serves DELETE /admin/v1/tool-policies/{id}.
func (s *Server) handleAdminDeleteToolPolicy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 10*time.Second)
	defer cancel()
	rc := requestContext(ctx)

	if s.repos == nil || s.repos.ToolPolicies == nil {
		writeError(w, domain.NewError(domain.ErrCodeInternal, "the tool policy store is unavailable"),
			metaFromContext(rc, nil))
		return
	}

	id := chiURLParam(r, "id")
	if err := s.repos.ToolPolicies.Delete(ctx, id); err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}

	s.audit(ctx, rc, domain.AuditDelete, domain.ResourceToolPolicy, id, nil, nil)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// ---------------------------------------------------------------------------
// Tool invocation history and agent runs
// ---------------------------------------------------------------------------

// handleAdminListToolInvocations serves GET /admin/v1/tool-invocations.
func (s *Server) handleAdminListToolInvocations(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 15*time.Second)
	defer cancel()
	rc := requestContext(ctx)

	if s.repos == nil || s.repos.Invocations == nil {
		writeError(w, domain.NewError(domain.ErrCodeInternal,
			"the tool invocation store is unavailable"), metaFromContext(rc, nil))
		return
	}

	query := r.URL.Query()
	filter := storage.ToolInvocationFilter{
		TenantID:  query.Get("tenant_id"),
		RequestID: query.Get("request_id"),
		ToolName:  query.Get("tool_name"),
		ToolID:    query.Get("tool_call_id"),
		Status:    query.Get("status"),
		Limit:     parseIntParam(r, "limit", 100),
		Offset:    parseIntParam(r, "offset", 0),
	}

	invocations, err := s.repos.Invocations.List(ctx, filter)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}

	// Aggregate counters are returned with the rows so the dashboard can render
	// a usage summary without a second query per row.
	summary := map[string]int{}
	total := 0
	latencyTotal := int64(0)
	for _, invocation := range invocations {
		summary[string(invocation.Status)]++
		total++
		latencyTotal += invocation.LatencyMS
	}
	averageLatency := int64(0)
	if total > 0 {
		averageLatency = latencyTotal / int64(total)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"invocations":        invocations,
		"summary":            summary,
		"total":              total,
		"average_latency_ms": averageLatency,
		"limit":              filter.Limit,
		"offset":             filter.Offset,
	})
}

// handleAdminGetToolInvocation serves GET /admin/v1/tool-invocations/{id}.
func (s *Server) handleAdminGetToolInvocation(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 10*time.Second)
	defer cancel()
	rc := requestContext(ctx)

	if s.repos == nil || s.repos.Invocations == nil {
		writeError(w, domain.NewError(domain.ErrCodeInternal,
			"the tool invocation store is unavailable"), metaFromContext(rc, nil))
		return
	}

	id := chiURLParam(r, "id")
	invocations, err := s.repos.Invocations.List(ctx, storage.ToolInvocationFilter{Limit: 500})
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	for _, invocation := range invocations {
		if invocation.ID != id {
			continue
		}
		execution, eerr := s.repos.Invocations.ExecutionFor(ctx, id)
		if eerr != nil {
			writeError(w, eerr, metaFromContext(rc, nil))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"invocation": invocation,
			"execution":  execution,
		})
		return
	}

	writeError(w, domain.Errorf(domain.ErrCodeNotFound, "tool invocation %q was not found", id),
		metaFromContext(rc, nil))
}

// handleAdminListAgentRuns serves GET /admin/v1/agent-runs.
func (s *Server) handleAdminListAgentRuns(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 15*time.Second)
	defer cancel()
	rc := requestContext(ctx)

	if s.repos == nil || s.repos.AgentRuns == nil {
		writeError(w, domain.NewError(domain.ErrCodeInternal,
			"the agent run store is unavailable"), metaFromContext(rc, nil))
		return
	}

	runs, err := s.repos.AgentRuns.ListRuns(ctx, r.URL.Query().Get("tenant_id"),
		parseIntParam(r, "limit", 50))
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	// "total" matches the tool-invocation list, so one dashboard query shape
	// serves both histories. It is the number of rows this page returned rather
	// than every matching row, because that is all the caller can act on here.
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "total": len(runs)})
}

// handleAdminGetAgentRun serves GET /admin/v1/agent-runs/{id} with its steps.
func (s *Server) handleAdminGetAgentRun(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 10*time.Second)
	defer cancel()
	rc := requestContext(ctx)

	if s.repos == nil || s.repos.AgentRuns == nil {
		writeError(w, domain.NewError(domain.ErrCodeInternal,
			"the agent run store is unavailable"), metaFromContext(rc, nil))
		return
	}

	run, steps, err := s.repos.AgentRuns.GetByID(ctx, chiURLParam(r, "id"))
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	if run == nil {
		writeError(w, domain.NewError(domain.ErrCodeNotFound, "agent run not found"),
			metaFromContext(rc, nil))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "steps": steps})
}
