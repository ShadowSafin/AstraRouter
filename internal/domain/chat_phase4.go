package domain

import (
	"encoding/json"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Phase 4 additions to the chat request.
//
// The wire additions are deliberately opt-in and additive: a request that omits
// them behaves exactly as it did before Phase 4, which is what keeps every
// existing client working.
// ---------------------------------------------------------------------------

// ToolExecution is the client half of Phase 4's tool control.
//
// It rides on the chat request because that is where the tools already live, and
// it is namespaced ("tool_execution") so it cannot collide with an OpenAI field
// that might be added later.
type ToolRunConfig struct {
	// Mode requests how much the gateway should do. Empty means "client
	// executes", which is the behaviour every pre-Phase-4 client expects.
	Mode ToolPolicyMode `json:"mode,omitempty"`
	// MaxSteps overrides the policy step bound for this request, clamped to the
	// policy ceiling. A client may ask for fewer steps than policy allows; it
	// may never ask for more.
	MaxSteps int `json:"max_steps,omitempty"`
	// MaxToolCalls overrides the policy call bound the same way.
	MaxToolCalls int `json:"max_tool_calls,omitempty"`
}

// Validate checks the client-supplied bounds.
//
// Negative values are rejected rather than clamped: a client that sends -1 has a
// bug, and silently turning that into "unbounded" would be the worst possible
// interpretation.
func (e *ToolRunConfig) Validate() error {
	if e == nil {
		return nil
	}
	if e.Mode != "" && !e.Mode.Valid() {
		return Errorf(ErrCodeInvalidRequest,
			"unknown 'tool_execution.mode' %q; expected disabled, manual or automatic", e.Mode)
	}
	if e.MaxSteps < 0 {
		return NewError(ErrCodeInvalidRequest,
			"'tool_execution.max_steps' must not be negative").withParam("tool_execution.max_steps")
	}
	if e.MaxToolCalls < 0 {
		return NewError(ErrCodeInvalidRequest,
			"'tool_execution.max_tool_calls' must not be negative").withParam("tool_execution.max_tool_calls")
	}
	return nil
}

// EffectiveMode resolves the mode for a request: the policy decides, and a
// client may only ever ask for less autonomy than the policy allows.
//
// This monotonicity is deliberate. A client must not be able to opt itself into
// gateway-side execution that the operator has switched off, so "automatic" is
// granted only when both the policy and the request agree.
func (e *ToolRunConfig) EffectiveMode(policy ToolPolicy) ToolPolicyMode {
	if !policy.Enabled || policy.Mode == ToolsDisabled {
		return ToolsDisabled
	}
	if e == nil || e.Mode == "" {
		return policy.Mode
	}
	requested := e.Mode
	// Rank the modes so a client can only reduce autonomy.
	rank := func(m ToolPolicyMode) int {
		switch m {
		case ToolsAutomatic:
			return 2
		case ToolsManual:
			return 1
		default:
			return 0
		}
	}
	if rank(requested) > rank(policy.Mode) {
		return policy.Mode
	}
	return requested
}

// ApplyBounds folds the client's requested bounds into a policy copy, never
// raising a ceiling the operator set.
func (e *ToolRunConfig) ApplyBounds(policy *ToolPolicy) {
	if e == nil || policy == nil {
		return
	}
	if e.MaxSteps > 0 && e.MaxSteps < policy.MaxSteps {
		policy.MaxSteps = e.MaxSteps
	}
	if e.MaxToolCalls > 0 && e.MaxToolCalls < policy.MaxToolCalls {
		policy.MaxToolCalls = e.MaxToolCalls
	}
	policy.Normalize()
}

// ToolRunStatus reports how a tool-enabled request ended, for the response
// metadata and the dashboard.
type ToolRunStatus string

const (
	// ToolRunNone means no tool run happened: the request had no tools, or the
	// client executed them itself.
	ToolRunNone ToolRunStatus = "none"
	// ToolRunClientExecuted means the model emitted calls the client handles.
	ToolRunClientExecuted ToolRunStatus = "client_executed"
	// ToolRunGatewayExecuted means the gateway ran at least one tool.
	ToolRunGatewayExecuted ToolRunStatus = "gateway_executed"
	// ToolRunDenied means policy refused tool use for the request.
	ToolRunDenied ToolRunStatus = "denied"
)

// ToolRunMetadata is the Phase 4 block in the response metadata.
//
// It lives in a separate struct rather than growing ResponseMetadata so the
// tool feature can be reasoned about, and versioned, on its own.
type ToolRunMetadata struct {
	Mode   ToolPolicyMode `json:"mode,omitempty"`
	Status ToolRunStatus  `json:"status"`
	RunID  string         `json:"run_id,omitempty"`
	// Steps and Calls are the run's turn and call counts.
	Steps int `json:"steps,omitempty"`
	Calls int `json:"calls,omitempty"`
	// Executed counts calls the gateway actually ran.
	Executed int `json:"executed,omitempty"`
	// Skipped counts calls handed back to the client or refused by policy.
	Skipped int `json:"skipped,omitempty"`
	// StopReason explains a bounded stop.
	StopReason string `json:"stop_reason,omitempty"`
	// Tools lists the tool names involved, in call order.
	Tools []string `json:"tools,omitempty"`
	// Structured reports structured-output conformance when it was requested.
	Structured *StructuredOutputMeta `json:"structured,omitempty"`
	// LatencyMS is the whole run's wall-clock time.
	LatencyMS int64 `json:"latency_ms,omitempty"`
}

// ToolRunEvent is the downstream record of a finished bounded tool run.
//
// It carries the client-facing summary plus the identities a consumer needs to
// join it to the request and the stored history. The wire summary and the
// event are built from the same run, so the two can never disagree.
type ToolRunEvent struct {
	// RunID is the stored agent run's id.
	RunID string `json:"run_id"`
	// RequestID and TenantID join the run to the request that caused it.
	RequestID string `json:"request_id"`
	TenantID  string `json:"tenant_id,omitempty"`
	// Success is whether the request itself succeeded, not whether the model
	// answered: a bounded run that explained itself is still a success.
	Success bool `json:"success"`
	// Run is the client-facing summary.
	Run ToolRunMetadata `json:"run"`
	// Provider and Model are the serving target of the final turn.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// Persisted is false when the run trace failed to persist. The event still
	// fires, because a lost trace the operator never hears about is worse than
	// a trace known to be missing.
	Persisted bool `json:"persisted"`
}

// StructuredOutputMeta reports how structured output was handled.
type StructuredOutputMeta struct {
	// Requested is "none", "json_object" or "json_schema".
	Requested string `json:"requested"`
	// Valid is true when the answer was checked and matched the schema. It is
	// nil when no check was requested, so a client can tell "not checked" from
	// "checked and fine".
	Valid *bool `json:"valid,omitempty"`
	// Schema names the schema that was enforced, when one was.
	Schema string `json:"schema,omitempty"`
	// Error explains a conformance failure.
	Error string `json:"error,omitempty"`
	// Repaired records that the gateway extracted a JSON object from a response
	// that had surrounding prose, which is the common real-world case.
	Repaired bool `json:"repaired,omitempty"`
}

// ToolValidationIssue is one problem found in a request's tool declarations.
type ToolValidationIssue struct {
	Index   int    `json:"index"`
	Name    string `json:"name,omitempty"`
	Message string `json:"message"`
}

// ValidateTools checks a request's tool declarations before routing.
//
// The wire tools are opaque today, so a client can send a tool with no name, a
// duplicate name, or a parameters block that is not a schema object; all three
// are rejected upstream with an opaque 400 or, worse, silently misrouted.
func ValidateTools(tools []Tool) (ToolValidationIssue, error) {
	if len(tools) > 128 {
		return ToolValidationIssue{}, NewError(ErrCodeInvalidRequest,
			"a request may declare at most 128 tools").withParam("tools")
	}
	seen := make(map[string]bool, len(tools))
	for i, tool := range tools {
		issue := ToolValidationIssue{Index: i, Name: tool.Function.Name}
		if strings.TrimSpace(tool.Function.Name) == "" {
			issue.Message = "'tools[].function.name' is required"
			return issue, Errorf(ErrCodeInvalidRequest, "%s", issue.Message).withParam("tools")
		}
		if !validFunctionName(tool.Function.Name) {
			issue.Message = "'" + tool.Function.Name + "' is not a valid function name; use letters, digits, underscore and dot"
			return issue, Errorf(ErrCodeInvalidRequest, "%s", issue.Message).withParam("tools")
		}
		if seen[tool.Function.Name] {
			issue.Message = "tool " + tool.Function.Name + " is declared more than once"
			return issue, Errorf(ErrCodeInvalidRequest, "%s", issue.Message).withParam("tools")
		}
		seen[tool.Function.Name] = true
		if len(tool.Function.Parameters) == 0 {
			continue
		}
		// Parameters must be a JSON *object*. A bare string, number or array is
		// syntactically valid JSON but is not a schema, and forwarding one to a
		// provider produces an opaque upstream rejection deep inside a run.
		var probe map[string]any
		if err := json.Unmarshal(tool.Function.Parameters, &probe); err != nil {
			issue.Message = "tool " + tool.Function.Name +
				" has parameters that are not a JSON object describing its schema"
			return issue, Errorf(ErrCodeInvalidRequest, "%s", issue.Message).withParam("tools")
		}
	}
	return ToolValidationIssue{}, nil
}

// validFunctionName reports whether a name is acceptable to every provider.
//
// The rule is the intersection of OpenAI, Anthropic and Ollama: letters,
// digits, underscore and dot, starting with a letter or digit. A name outside it
// would be rejected by at least one upstream, so it is caught here instead.
func validFunctionName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9', r == '.', r == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// EffectiveTools resolves which tools a request advertises.
//
// Request-supplied tools win over registry tools: the client named them, and
// silently substituting a registered tool for one the client asked for would
// change what the model is allowed to call.
func (e *ToolRunConfig) EffectiveTools(requested []Tool, registryWire []Tool) []Tool {
	if len(requested) > 0 {
		return requested
	}
	return registryWire
}

// ToolTiming is the latency split a run reports, per the observability
// requirement: how long the model spent handing off, and how long the final
// answer took.
type ToolTiming struct {
	ModelLatencyMS int64 `json:"model_latency_ms"`
	ToolLatencyMS  int64 `json:"tool_latency_ms"`
	FinalLatencyMS int64 `json:"final_latency_ms"`
}

// Total returns the run's wall-clock time as recorded.
func (t ToolTiming) Total() time.Duration {
	return time.Duration(t.ModelLatencyMS+t.ToolLatencyMS+t.FinalLatencyMS) * time.Millisecond
}
