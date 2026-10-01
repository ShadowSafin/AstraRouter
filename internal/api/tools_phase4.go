package api

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/corerouter/corerouter/internal/domain"
	"github.com/corerouter/corerouter/internal/providers"
	"github.com/corerouter/corerouter/internal/schema"
	"github.com/corerouter/corerouter/internal/tools"
)

// This file is the Phase 4 glue between the HTTP layer and the tool pipeline.
// It resolves the registry and policy for a request, validates structured
// output, and supplies the Completer the bounded loop calls back into. All
// decision logic lives in internal/tools; nothing here decides whether a tool
// may run.

// ToolServices is the tool plane a server needs.
//
// Every field is optional: with all of them nil the gateway behaves exactly as
// it did before Phase 4, which is what keeps tool calling from becoming a hard
// dependency of ordinary chat.
type ToolServices struct {
	Registry    tools.RegistrySource
	Policies    tools.PolicySource
	Invocations tools.InvocationSink
	Runs        tools.RunSink
}

// Enabled reports whether any tool plane is configured.
func (t ToolServices) Enabled() bool {
	return t.Registry != nil || t.Invocations != nil || t.Runs != nil
}

// toolServices returns the configured tool plane.
func (s *Server) toolServices() ToolServices {
	if s.tools == nil {
		return ToolServices{}
	}
	return *s.tools
}

// toolContext is the per-request tool state resolved before routing.
type toolContext struct {
	// Policy is the effective policy, with the client's requested bounds folded in.
	Policy domain.ToolPolicy
	// Mode is the resolved execution mode: a client may only reduce autonomy.
	Mode domain.ToolPolicyMode
	// Registry holds the tools visible to this tenant.
	Registry *tools.Registry
	// Choice is the parsed tool_choice, validated against the request's tools.
	Choice domain.ToolChoice
	// Structured is the parsed response_format.
	Structured domain.StructuredOutput
	// Sensitive marks the request as carrying sensitive data.
	Sensitive bool
}

// automatic reports whether the gateway may execute tools for this request.
func (t *toolContext) automatic() bool {
	return t != nil && t.Mode == domain.ToolsAutomatic
}

// gatewayExecution reports whether gateway-side tool execution is enabled.
//
// When false the gateway never runs tools itself: an automatic request is
// clamped to manual during context resolution, so agentic clients always get
// the model's tool calls back untouched and run their own tools. Nil-safe:
// a server without configuration never executes.
func (s *Server) gatewayExecution() bool {
	return s != nil && s.config != nil && s.config.Tools.GatewayExecution
}

// hasTools reports whether any tool is available to advertise.
func (t *toolContext) hasTools() bool {
	return t != nil && t.Registry != nil && t.Registry.Len() > 0
}

// resolveToolContext prepares the tool plane for one request.
//
// Every failure mode here is a refusal with a specific message, because "tool
// calling silently did nothing" costs an engineer an afternoon: a malformed
// schema, an unknown tool_choice and a policy that blocks tools all have to be
// distinguishable from each other.
func (s *Server) resolveToolContext(
	ctx context.Context,
	body *domain.ChatCompletionRequest,
	rc *domain.RequestContext,
) (*toolContext, error) {
	services := s.toolServices()
	if !services.Enabled() {
		return nil, nil
	}

	policy := tools.EffectivePolicy(ctx, services.Policies, rc.TenantID())

	// The registry is snapshotted once per request so a tool disabled halfway
	// through a run cannot produce a run that is not reproducible.
	var all []domain.ToolSpec
	if services.Registry != nil {
		listed, err := services.Registry.List(ctx)
		if err != nil {
			return nil, domain.NewError(domain.ErrCodeInternal,
				"failed to read the tool registry").Wrap(err)
		}
		all = listed
	}
	registry := tools.BuildRegistry(all, rc.TenantID())

	choice, err := domain.ParseToolChoice(body.ToolChoice, body.Tools)
	if err != nil {
		return nil, err
	}

	structured, err := domain.ParseStructuredOutput(body.ResponseFormat)
	if err != nil {
		return nil, err
	}
	// The contract is also needed by the plain completion path, which has no
	// toolContext, so it is recorded on the request context.
	rc.StructuredOutput = structured

	mode := body.ToolExecution.EffectiveMode(policy)
	if mode == domain.ToolsAutomatic && !s.gatewayExecution() {
		// Gateway execution is disabled: the model still emits tool calls,
		// but the client runs them. Clamping (rather than rejecting) keeps
		// the gateway drop-in compatible for agentic apps that request
		// automatic mode while streaming.
		mode = domain.ToolsManual
	}
	body.ToolExecution.ApplyBounds(&policy)

	tc := &toolContext{
		Policy:     policy,
		Mode:       mode,
		Registry:   registry,
		Choice:     choice,
		Structured: structured,
		Sensitive:  requestIsSensitive(rc),
	}

	// A disabled policy is not an error on its own: most SDKs send tool
	// definitions unconditionally, and failing the whole request would break
	// them. It only becomes fatal when the client demanded a tool call.
	if mode == domain.ToolsDisabled && choice.Mode == domain.ToolChoiceRequired {
		return nil, domain.NewError(domain.ErrCodePermission,
			"tool calling is disabled by policy for this tenant")
	}
	return tc, nil
}

// requestIsSensitive reports whether the request carries data policy treats as
// sensitive.
func requestIsSensitive(rc *domain.RequestContext) bool {
	for _, label := range rc.DataSensitivity {
		if label != "" && label != "public" {
			return true
		}
	}
	return false
}

// toolCompleter adapts the loop's Completer onto the gateway's routing and
// execution machinery.
//
// Each turn re-uses the decision made for the request rather than re-resolving:
// a multi-step run is one logical request, and re-resolving per turn would let a
// tool result push the conversation onto a different provider mid-run, which is
// how a run ends up split across two models with different capabilities.
type toolCompleter struct {
	server   *Server
	rc       *domain.RequestContext
	decision *domain.RouteDecision
	body     *domain.ChatCompletionRequest
	// usage accumulates across turns so billing reflects the whole run.
	usage domain.TokenUsage
	// cost accumulates across turns.
	cost domain.Cost
}

// Complete performs one model turn.
func (c *toolCompleter) Complete(
	ctx context.Context,
	messages []domain.ChatMessage,
) (*tools.Completion, error) {
	if c.server.executor == nil {
		return nil, domain.NewError(domain.ErrCodeInternal, "no provider executor is configured")
	}

	// Each turn gets its own body so the provider sees the tool results in the
	// messages rather than through mutated shared state.
	body := *c.body
	body.Messages = messages
	body.Stream = nil
	body.StreamOptions = nil

	req := &providers.Request{
		Model:        c.decision.Chosen.Model,
		Params:       &body,
		Ref:          c.decision.Chosen.Ref(),
		MaxTokens:    c.rc.MaxOutputTokens,
		PromptTokens: c.rc.PromptTokens,
		Stream:       false,
	}

	result, err := c.server.executor.Execute(ctx, c.rc, req, nil)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Response == nil {
		return nil, domain.NewError(domain.ErrCodeUpstream,
			"the provider returned an empty response")
	}

	usage := result.Response.Usage.Normalize()
	if usage.TotalTokens == 0 {
		usage = domain.TokenUsage{
			PromptTokens: c.rc.PromptTokens,
			Estimated:    true,
		}.Normalize()
	}
	c.usage = addUsage(c.usage, usage)
	c.cost.USD += costForUsage(c.decision.Chosen, usage).USD

	completion := &tools.Completion{
		Provider: c.decision.Chosen.ProviderName,
		Model:    c.decision.Chosen.Model,
		Usage:    usage,
		Raw:      result.Response,
	}
	if len(result.Response.Choices) > 0 {
		choice := result.Response.Choices[0]
		if choice.Message != nil {
			completion.Content = choice.Message.Text()
			completion.ToolCalls = choice.Message.ToolCalls
		}
		if choice.FinishReason != nil {
			completion.FinishReason = *choice.FinishReason
		}
	}
	return completion, nil
}

// addUsage sums two usage records.
func addUsage(a, b domain.TokenUsage) domain.TokenUsage {
	return domain.TokenUsage{
		PromptTokens:       a.PromptTokens + b.PromptTokens,
		CompletionTokens:   a.CompletionTokens + b.CompletionTokens,
		TotalTokens:        a.TotalTokens + b.TotalTokens,
		CachedPromptTokens: a.CachedPromptTokens + b.CachedPromptTokens,
		Estimated:          a.Estimated || b.Estimated,
	}
}

// validateStructuredOutput checks a final answer against a requested schema.
//
// A strict json_schema is a contract and is enforced. A non-strict one, or
// json_object, is a hint: the answer is checked and reported but the request is
// not failed, because returning imperfect-but-readable text beats returning an
// error to a client that asked a question.
func validateStructuredOutput(
	structured domain.StructuredOutput,
	content string,
	enforce bool,
) *domain.StructuredOutputMeta {
	meta := &domain.StructuredOutputMeta{Requested: structured.Mode, Schema: structured.Name}
	switch structured.Mode {
	case domain.FormatJSONObject:
		// json_object promises an *object*. Valid JSON is not enough: a bare
		// array or scalar would be handed to a caller whose own unmarshal then
		// fails, with the error pointing at their code rather than the response.
		object, repaired := extractJSONObject(content)
		if object == nil {
			meta.Valid = boolPointer(false)
			meta.Error = "the response was not a JSON object"
			return meta
		}
		meta.Valid = boolPointer(true)
		meta.Repaired = repaired
		return meta
	case domain.FormatJSONSchema:
	default:
		return nil
	}

	if len(structured.Schema) == 0 {
		return meta
	}
	compiled, err := schema.FromMap(structured.Schema)
	if err != nil {
		meta.Valid = boolPointer(false)
		meta.Error = "the requested schema could not be compiled: " + err.Error()
		return meta
	}

	payload, repaired := extractJSONObject(content)
	if payload == nil {
		meta.Valid = boolPointer(false)
		meta.Error = "the response did not contain a JSON object"
		return meta
	}
	meta.Repaired = repaired

	if errs := compiled.ValidateJSON(payload); !errs.Empty() {
		meta.Valid = boolPointer(false)
		meta.Error = errs.Error()
		return meta
	}
	meta.Valid = boolPointer(true)
	_ = enforce
	return meta
}

// extractJSONObject finds a JSON object in a response body.
//
// Models routinely wrap JSON in prose or a fenced code block even when asked for
// structured output, so the object is extracted when it is unambiguous. A
// response with no object at all is left for the validator to reject.
func extractJSONObject(content string) ([]byte, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil, false
	}
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		return []byte(trimmed), false
	}
	start := strings.IndexByte(trimmed, '{')
	end := strings.LastIndexByte(trimmed, '}')
	if start < 0 || end <= start {
		return nil, false
	}
	candidate := trimmed[start : end+1]
	if !validJSON(candidate) {
		return nil, false
	}
	return []byte(candidate), true
}

// validJSON reports whether raw is a decodable JSON document.
func validJSON(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	return json.Valid([]byte(trimmed))
}

// toolRunMetadata assembles the client-facing tool block.
func toolRunMetadata(run *tools.LoopResult, runID string, mode domain.ToolPolicyMode) *domain.ToolRunMetadata {
	meta := &domain.ToolRunMetadata{Mode: mode, Status: domain.ToolRunNone}
	if run == nil {
		return meta
	}
	meta.RunID = runID
	meta.Steps = run.Steps
	meta.Calls = run.ToolCalls
	meta.LatencyMS = run.LatencyMS
	meta.StopReason = run.StopReason
	for _, invocation := range run.Invocations {
		meta.Tools = append(meta.Tools, invocation.ToolName)
		if invocation.Status == domain.InvocationExecuted {
			meta.Executed++
		} else {
			meta.Skipped++
		}
	}
	switch {
	case run.Ran:
		meta.Status = domain.ToolRunGatewayExecuted
	case run.ToolCalls > 0:
		meta.Status = domain.ToolRunClientExecuted
	}
	return meta
}

// boolPointer is a small helper for the optional validity flags.
func boolPointer(v bool) *bool { return &v }

// toolRunTimeout is the wall-clock ceiling a run inherits from policy.
func toolRunTimeout(policy domain.ToolPolicy) time.Duration {
	seconds := policy.MaxRunSeconds
	if seconds <= 0 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}
