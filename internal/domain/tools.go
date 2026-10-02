package domain

import (
	"encoding/json"
	"time"
)

// ---------------------------------------------------------------------------
// Phase 4: tools, tool execution and bounded agent runs.
//
// The request/response wire types for tool calling already exist in chat.go
// (Tool, ToolCall, FunctionCall). This file adds the control-plane model: what
// a tool *is* in AstraRouter's registry, what happened when one ran, and the
// rules that decide whether it may run at all.
// ---------------------------------------------------------------------------

// ToolKind is how a registered tool is implemented.
type ToolKind string

const (
	// ToolBuiltin is implemented inside the gateway: deterministic, no outbound
	// I/O, safe to execute automatically.
	ToolBuiltin ToolKind = "builtin"
	// ToolExternal is registered for discovery and schema declaration only. The
	// gateway never executes it; the client must run it and return the result.
	// Executing operator-supplied network endpoints is an SSRF/RCE surface that
	// a gateway should not acquire by accident.
	ToolExternal ToolKind = "external"
)

// Valid reports whether the kind is known.
func (k ToolKind) Valid() bool { return k == ToolBuiltin || k == ToolExternal }

// SafetyLevel is the operator's declared risk assessment for a tool.
type SafetyLevel string

const (
	// SafetySafe is read-only and deterministic.
	SafetySafe SafetyLevel = "safe"
	// SafetySensitive reaches outside the request (data, side effects).
	SafetySensitive SafetyLevel = "sensitive"
	// SafetyDangerous can mutate state or incur cost; never auto-executed.
	SafetyDangerous SafetyLevel = "dangerous"
)

// Valid reports whether the safety level is known.
func (s SafetyLevel) Valid() bool {
	switch s {
	case SafetySafe, SafetySensitive, SafetyDangerous:
		return true
	default:
		return false
	}
}

// ToolOwner scopes a tool's availability.
type ToolOwner string

const (
	// OwnerPlatform is available to every tenant.
	OwnerPlatform ToolOwner = "platform"
	// OwnerTenant is available only to one tenant.
	OwnerTenant ToolOwner = "tenant"
)

// Valid reports whether the owner scope is known.
func (o ToolOwner) Valid() bool { return o == OwnerPlatform || o == OwnerTenant }

// ToolSpec is a registered tool definition.
//
// Parameters is a JSON Schema describing the tool's arguments. It is stored as
// an opaque document on the way in (the client's schema is authoritative) and
// is validated against the JSON Schema subset before it is persisted, so a
// malformed schema fails at registration rather than at call time.
//
// The name is ToolSpec rather than Tool because Tool is already the OpenAI wire
// type on the request; this is the control-plane record of one.
type ToolSpec struct {
	ID string `json:"id"`
	// Name is the tool's function name, unique per owner scope.
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Kind        ToolKind  `json:"kind"`
	Owner       ToolOwner `json:"owner"`
	// TenantID scopes an OwnerTenant tool. Empty for platform tools.
	TenantID string `json:"tenant_id,omitempty"`
	// Parameters is the JSON Schema for the arguments object.
	Parameters json.RawMessage `json:"parameters,omitempty"`
	// Strict requires the provider to enforce the schema exactly.
	Strict      bool        `json:"strict,omitempty"`
	SafetyLevel SafetyLevel `json:"safety_level"`
	// Executable reports whether the gateway can run this tool itself.
	Executable bool `json:"executable"`
	// Handler names the built-in implementation for ToolBuiltin tools.
	Handler string `json:"handler,omitempty"`
	Version string `json:"version,omitempty"`
	// Enabled gates the registry entry without deleting it, so history and
	// policies referencing it stay resolvable.
	Enabled bool `json:"enabled"`
	// RequiresApproval forces the tool call back to the client even when the
	// request asked for automatic execution.
	RequiresApproval bool              `json:"requires_approval"`
	Labels           map[string]string `json:"labels,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

// Usable reports whether the registry entry may serve a request.
func (t *ToolSpec) Usable() bool { return t != nil && t.Enabled }

// Wire renders the registry entry as an OpenAI-style tool definition, which is
// the form every adapter translates for its own provider.
//
// This is what makes the registry useful to clients that know nothing about
// AstraRouter: a registered tool becomes an ordinary entry in the request's
// `tools` array, so the same tool works whichever provider ends up serving it.
func (t *ToolSpec) Wire() Tool {
	if t == nil {
		return Tool{}
	}
	strict := t.Strict
	wire := Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		},
	}
	if strict {
		wire.Function.Strict = &strict
	}
	return wire
}

// InvocationStatus is the lifecycle of one model-requested tool call.
type InvocationStatus string

const (
	// InvocationPending is a call the model asked for that has not run.
	InvocationPending InvocationStatus = "pending"
	// InvocationExecuted ran inside the gateway.
	InvocationExecuted InvocationStatus = "executed"
	// InvocationDenied was refused by policy.
	InvocationDenied InvocationStatus = "denied"
	// InvocationFailed ran and returned an error.
	InvocationFailed InvocationStatus = "failed"
	// InvocationSkipped was left to the client.
	InvocationSkipped InvocationStatus = "skipped"
	// InvocationInvalid had malformed arguments or no registered tool.
	InvocationInvalid InvocationStatus = "invalid"
)

// ToolInvocation is one tool call the model requested.
//
// It is written whether the call was executed, denied or handed back to the
// client, because "the model wanted to call X" is the fact an operator needs
// when a run misbehaves, and it is not recoverable from the final answer.
type ToolInvocation struct {
	ID        string    `json:"id"`
	RequestID RequestID `json:"request_id"`
	TenantID  string    `json:"tenant_id,omitempty"`
	// RunID ties the invocation to its agent run, when part of one.
	RunID string `json:"run_id,omitempty"`
	// Step is the agent step (1-based) that produced the call. Zero for a
	// single-turn request with no loop.
	Step       int    `json:"step"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name"`
	// Arguments is the raw argument string the model produced.
	Arguments string `json:"arguments,omitempty"`
	// ParsedArguments is the decoded argument object, when it parsed.
	ParsedArguments map[string]any   `json:"parsed_arguments,omitempty"`
	Status          InvocationStatus `json:"status"`
	// DenyReason explains a refused or skipped call.
	DenyReason string `json:"deny_reason,omitempty"`
	// LatencyMS is the gateway execution time.
	LatencyMS int64 `json:"latency_ms"`
	// ResultBytes is the size of the returned payload, for budget accounting.
	ResultBytes int       `json:"result_bytes,omitempty"`
	ErrorCode   string    `json:"error_code,omitempty"`
	Provider    string    `json:"provider,omitempty"`
	Model       string    `json:"model,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// ToolExecution is the result of running a tool.
//
// Content is stored separately from the invocation so the invocation row stays
// small and cheap to list; the payload is fetched only when a trace is opened.
type ToolExecution struct {
	ID           string `json:"id"`
	InvocationID string `json:"invocation_id"`
	ToolName     string `json:"tool_name"`
	// Content is the tool's returned text, injected back into the conversation.
	Content      string    `json:"content"`
	Success      bool      `json:"success"`
	ErrorMessage string    `json:"error_message,omitempty"`
	LatencyMS    int64     `json:"latency_ms"`
	CreatedAt    time.Time `json:"created_at"`
}

// ToolPolicyMode decides who may call tools at all.
type ToolPolicyMode string

const (
	// ToolsDisabled refuses every tool call.
	ToolsDisabled ToolPolicyMode = "disabled"
	// ToolsManual lets the model emit tool calls that the client executes. This
	// is the default and the behaviour every existing client already relies on.
	ToolsManual ToolPolicyMode = "manual"
	// ToolsAutomatic lets the gateway execute registered, approved tools itself.
	ToolsAutomatic ToolPolicyMode = "automatic"
)

// Valid reports whether the mode is known.
func (m ToolPolicyMode) Valid() bool {
	switch m {
	case ToolsDisabled, ToolsManual, ToolsAutomatic:
		return true
	default:
		return false
	}
}

// ToolPolicy is the control-plane rule set for tool use.
//
// A nil policy means the defaults: tools are enabled, the client executes
// them, nothing runs in the gateway. That default is what keeps every
// pre-Phase-4 client working unchanged.
type ToolPolicy struct {
	ID string `json:"id"`
	// TenantID scopes the policy; empty is the platform default.
	TenantID string         `json:"tenant_id,omitempty"`
	Name     string         `json:"name"`
	Enabled  bool           `json:"enabled"`
	Mode     ToolPolicyMode `json:"mode"`
	// AllowedTools and DeniedTools match tool names or glob patterns. Deny wins.
	AllowedTools []string `json:"allowed_tools,omitempty"`
	DeniedTools  []string `json:"denied_tools,omitempty"`
	// AllowedProviders and DeniedProviders constrain which upstream may emit a
	// tool call. Deny wins.
	AllowedProviders []string `json:"allowed_providers,omitempty"`
	DeniedProviders  []string `json:"denied_providers,omitempty"`
	// RequireApproval routes every call back to the client, whatever the request
	// asked for.
	RequireApproval bool `json:"require_approval"`
	// MaxSteps bounds a multi-step run.
	MaxSteps int `json:"max_steps"`
	// MaxToolCalls bounds total calls across a run.
	MaxToolCalls int `json:"max_tool_calls"`
	// MaxResultBytes bounds one tool result payload.
	MaxResultBytes int `json:"max_result_bytes"`
	// MaxRunSeconds bounds wall-clock time for a whole run.
	MaxRunSeconds int `json:"max_run_seconds"`
	// BlockSensitive refuses tool use on requests marked sensitive.
	BlockSensitive bool      `json:"block_sensitive"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// DefaultToolPolicy is the effective policy when nothing is stored.
func DefaultToolPolicy() ToolPolicy {
	return ToolPolicy{
		Name:           "default",
		Enabled:        true,
		Mode:           ToolsManual,
		MaxSteps:       3,
		MaxToolCalls:   8,
		MaxResultBytes: 32 * 1024,
		MaxRunSeconds:  60,
		BlockSensitive: false,
	}
}

// Normalize fills every unset bound so the pipeline never divides by zero or
// loops without a limit.
func (p *ToolPolicy) Normalize() {
	if p.Mode == "" {
		p.Mode = ToolsManual
	}
	if p.MaxSteps <= 0 {
		p.MaxSteps = 3
	}
	if p.MaxSteps > 10 {
		p.MaxSteps = 10
	}
	if p.MaxToolCalls <= 0 {
		p.MaxToolCalls = 8
	}
	if p.MaxToolCalls > 64 {
		p.MaxToolCalls = 64
	}
	if p.MaxResultBytes <= 0 {
		p.MaxResultBytes = 32 * 1024
	}
	if p.MaxResultBytes > 1024*1024 {
		p.MaxResultBytes = 1024 * 1024
	}
	if p.MaxRunSeconds <= 0 {
		p.MaxRunSeconds = 60
	}
	if p.MaxRunSeconds > 600 {
		p.MaxRunSeconds = 600
	}
}

// AgentRunStatus is the lifecycle of a bounded multi-step tool run.
type AgentRunStatus string

const (
	// RunRunning is a run that has started but not yet reached a terminal state.
	// It exists so the run row can be written before the first step: agent_steps
	// has a foreign key to agent_runs, so a step cannot be persisted until the
	// run it belongs to exists.
	RunRunning AgentRunStatus = "running"
	// RunCompleted finished with a final answer.
	RunCompleted AgentRunStatus = "completed"
	// RunStepLimit stopped at the step bound.
	RunStepLimit AgentRunStatus = "step_limit"
	// RunCallLimit stopped at the tool-call bound.
	RunCallLimit AgentRunStatus = "call_limit"
	// RunTimeLimit stopped at the wall-clock bound.
	RunTimeLimit AgentRunStatus = "time_limit"
	// RunFailed ended on an error.
	RunFailed AgentRunStatus = "failed"
	// RunDenied was refused before any step.
	RunDenied AgentRunStatus = "denied"
)

// ToolChoice is the parsed form of a request's tool_choice field.
//
// The raw field stays an opaque json.RawMessage on the wire (adapters
// translate it per provider), but the pipeline needs one interpretation across
// providers, and an unparseable or dangling tool_choice is currently forwarded
// verbatim to produce an opaque upstream 400.
type ToolChoice struct {
	// Mode is one of "auto", "none", "required" or a specific function name.
	Mode string `json:"mode"`
	// Name is set when Mode names a specific function.
	Name string `json:"name,omitempty"`
}

// Tool choice modes.
const (
	ToolChoiceAuto     = "auto"
	ToolChoiceNone     = "none"
	ToolChoiceRequired = "required"
	ToolChoiceNamed    = "function"
)

// ParseToolChoice interprets a wire tool_choice. An absent value means auto.
//
// A named choice that no supplied tool matches is reported as invalid rather
// than forwarded: the client asked for a tool that is not on the table, and
// saying so plainly beats an upstream error about a malformed field.
func ParseToolChoice(raw []byte, tools []Tool) (ToolChoice, error) {
	choice := ToolChoice{Mode: ToolChoiceAuto}
	if len(raw) == 0 || string(raw) == "null" {
		return choice, nil
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		switch asString {
		case ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired:
			choice.Mode = asString
			return choice, nil
		default:
			// A bare string is also OpenAI's legacy "name this function" form.
			return validateNamedChoice(choice, asString, tools)
		}
	}

	var asObject struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &asObject); err != nil {
		return choice, Errorf(ErrCodeInvalidRequest,
			"'tool_choice' must be \"auto\", \"none\", \"required\" or {\"type\":\"function\",\"function\":{\"name\":…}}").Wrap(err)
	}
	switch asObject.Type {
	case ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired:
		choice.Mode = asObject.Type
		return choice, nil
	case ToolChoiceNamed:
		name := asObject.Function.Name
		if name == "" {
			name = asObject.Name
		}
		return validateNamedChoice(choice, name, tools)
	default:
		return choice, Errorf(ErrCodeInvalidRequest,
			"unknown 'tool_choice' type %q; expected auto, none, required or function", asObject.Type)
	}
}

// validateNamedChoice resolves a named tool choice against the request's tools.
func validateNamedChoice(choice ToolChoice, name string, tools []Tool) (ToolChoice, error) {
	if name == "" {
		return choice, NewError(ErrCodeInvalidRequest,
			"'tool_choice' names a function but does not say which one")
	}
	for _, t := range tools {
		if t.Function.Name == name {
			choice.Mode = ToolChoiceNamed
			choice.Name = name
			return choice, nil
		}
	}
	return choice, Errorf(ErrCodeInvalidRequest,
		"'tool_choice' names %q, which is not one of the tools in this request", name)
}

// StructuredOutput is the parsed form of response_format.
//
// The wire field is opaque and forwarded per provider, so this exists only for
// the gateway's own validation decision: whether a conformance guarantee was
// asked for, and against which schema.
type StructuredOutput struct {
	// Mode is "none", "json_object" or "json_schema".
	Mode string `json:"mode"`
	// Strict requests that the gateway verify the answer against the schema.
	Strict bool `json:"strict,omitempty"`
	// Schema is the decoded schema object for json_schema mode.
	Schema map[string]any `json:"schema,omitempty"`
	// Name is the schema's declared name, used in error messages.
	Name string `json:"name,omitempty"`
}

// Structured output modes.
const (
	FormatNone       = "none"
	FormatJSONObject = "json_object"
	FormatJSONSchema = "json_schema"
)

// ParseStructuredOutput interprets response_format and extracts the schema.
//
// Both the OpenAI wire shape ({name, schema, strict}) and a bare schema object
// are accepted, because clients in the wild send both and rejecting the bare
// form would fail requests that are unambiguous.
func ParseStructuredOutput(format *ResponseFormat) (StructuredOutput, error) {
	out := StructuredOutput{Mode: FormatNone}
	if format == nil || format.Type == "" {
		return out, nil
	}

	switch format.Type {
	case FormatJSONObject:
		out.Mode = FormatJSONObject
		return out, nil
	case FormatJSONSchema:
		out.Mode = FormatJSONSchema
	default:
		return out, Errorf(ErrCodeInvalidRequest,
			"unknown 'response_format.type' %q; expected json_object or json_schema", format.Type)
	}

	raw := format.JSONSchema
	if len(raw) == 0 {
		return out, NewError(ErrCodeInvalidRequest,
			"'response_format.type' is json_schema but no schema was supplied")
	}

	var wrapper struct {
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema"`
		Strict bool            `json:"strict"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && len(wrapper.Schema) > 0 {
		out.Name = wrapper.Name
		out.Strict = wrapper.Strict
		if err := json.Unmarshal(wrapper.Schema, &out.Schema); err != nil {
			return out, NewError(ErrCodeInvalidRequest, "'response_format.json_schema.schema' is not an object").Wrap(err)
		}
		return out, nil
	}

	// Bare schema object.
	if err := json.Unmarshal(raw, &out.Schema); err != nil {
		return out, NewError(ErrCodeInvalidRequest,
			"'response_format.json_schema' must be an object with a 'schema' field").Wrap(err)
	}
	return out, nil
}

// Phase 4 audit enumerations. The string values must match the
// audit_events CHECK constraints (see 0005_phase4.sql).
const (
	// AuditExecute records a tool execution inside the gateway.
	AuditExecute AuditAction = "execute"
	// ResourceTool is the audit subject for a registry tool.
	ResourceTool AuditResource = "tool"
	// ResourceToolPolicy is the audit subject for tool policy changes.
	ResourceToolPolicy AuditResource = "tool_policy"
	// ResourceAgentRun is the audit subject for a bounded multi-step run.
	ResourceAgentRun AuditResource = "agent_run"
)
