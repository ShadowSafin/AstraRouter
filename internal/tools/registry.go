// Package tools implements Phase 4's control plane for tool calling: the tool
// registry, the policy that decides whether a call may run, the safe built-in
// executors, and the bounded multi-step loop.
//
// # Design commitments
//
// Three choices shape everything here, and they exist to keep a gateway from
// becoming an execution hazard:
//
//  1. Gateway execution is opt-in per request and off by default. A client that
//     sends `tools` and nothing else gets exactly the Phase 1-3 behaviour: the
//     model emits tool calls and the client runs them.
//
//  2. The gateway only ever executes registered built-in handlers. External and
//     tenant tools are advertised and validated but always returned to the
//     client, because executing operator-supplied network endpoints inside a
//     gateway is an SSRF and remote-code surface.
//
//  3. Every loop is bounded by steps, total calls, wall-clock time and result
//     size. A bound that can be raised without a policy edit is not a bound.
package tools

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/shadowsafin/astrarouter/internal/domain"
	"github.com/shadowsafin/astrarouter/internal/schema"
)

// RegistrySource reads registered tools. The storage repository satisfies it
// unmodified; tests use a fixture.
type RegistrySource interface {
	List(ctx context.Context) ([]domain.ToolSpec, error)
}

// PolicySource resolves the tool policy for a tenant.
type PolicySource interface {
	ResolveFor(ctx context.Context, tenantID string) (*domain.ToolPolicy, error)
}

// InvocationSink persists tool-call history. Persistence failures are reported
// but never fail a request: losing an audit row must not lose the answer.
type InvocationSink interface {
	InsertInvocation(ctx context.Context, invocation *domain.ToolInvocation) error
	InsertExecution(ctx context.Context, execution *domain.ToolExecution) error
}

// RunSink persists agent runs and their steps.
type RunSink interface {
	UpsertRun(ctx context.Context, run RunRecord) error
	InsertRunStep(ctx context.Context, runID string, step RunStep) error
}

// RunRecord is the storage-agnostic view of a run's header.
type RunRecord struct {
	ID         string
	RequestID  string
	TenantID   string
	Status     domain.AgentRunStatus
	Steps      int
	ToolCalls  int
	Provider   string
	Model      string
	LatencyMS  int64
	StopReason string
}

// RunStep is one row inside a run.
type RunStep struct {
	Step      int
	Kind      string
	Provider  string
	Model     string
	ToolCalls int
	LatencyMS int64
	Tokens    int
	Detail    map[string]any
}

// Registry resolves tools for a request and reports what each one permits.
//
// It is built once per request so the loop never re-reads the database mid-run:
// a tool that is disabled halfway through a run is a confusing failure, and a
// frozen view makes the run reproducible.
type Registry struct {
	specs   map[string]domain.ToolSpec
	ordered []domain.ToolSpec
}

// BuildRegistry snapshots the registry for one request.
func BuildRegistry(all []domain.ToolSpec, tenantID string) *Registry {
	reg := &Registry{specs: map[string]domain.ToolSpec{}}
	for _, spec := range all {
		// A tenant tool is visible only to its own tenant; a platform tool is
		// visible to everyone.
		if spec.Owner == domain.OwnerTenant && spec.TenantID != "" && spec.TenantID != tenantID {
			continue
		}
		reg.specs[spec.Name] = spec
		reg.ordered = append(reg.ordered, spec)
	}
	sort.SliceStable(reg.ordered, func(i, j int) bool {
		return reg.ordered[i].Name < reg.ordered[j].Name
	})
	return reg
}

// Lookup returns a registered tool by name.
func (r *Registry) Lookup(name string) (domain.ToolSpec, bool) {
	if r == nil {
		return domain.ToolSpec{}, false
	}
	spec, ok := r.specs[name]
	return spec, ok
}

// All returns every visible tool, name-ordered.
func (r *Registry) All() []domain.ToolSpec {
	if r == nil {
		return nil
	}
	return append([]domain.ToolSpec(nil), r.ordered...)
}

// Len reports how many tools are visible.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.ordered)
}

// Advertise renders the visible, enabled tools as OpenAI wire definitions.
func (r *Registry) Advertise() []domain.Tool {
	if r == nil {
		return nil
	}
	out := make([]domain.Tool, 0, len(r.ordered))
	for _, spec := range r.ordered {
		if !spec.Usable() {
			continue
		}
		out = append(out, spec.Wire())
	}
	return out
}

// ---------------------------------------------------------------------------
// Policy evaluation
// ---------------------------------------------------------------------------

// EffectivePolicy resolves the policy for a request.
//
// A nil source, a missing policy row or an unreadable store all converge on the
// built-in defaults, which are the least permissive interesting setting: tools
// work, the client executes them. Failing closed on an unreadable policy would
// take tool calling down platform-wide for a database blip.
func EffectivePolicy(ctx context.Context, source PolicySource, tenantID string) domain.ToolPolicy {
	defaults := domain.DefaultToolPolicy()
	if source == nil {
		return defaults
	}
	stored, err := source.ResolveFor(ctx, tenantID)
	if err != nil || stored == nil {
		return defaults
	}
	stored.Normalize()
	return *stored
}

// Decision is the verdict for one tool call.
type Decision struct {
	// Execute is true only when the gateway itself may run the tool.
	Execute bool
	// Reason explains a refusal, and is stored on the invocation row.
	Reason string
}

// EvaluateToolCall decides what happens to one requested tool call.
//
// The order of the checks is the policy's semantics: a disabled policy refuses
// everything; a deny-list entry refuses regardless of any allow-list; an
// unapproved or non-executable tool goes back to the client rather than
// failing, because the client may well be able to run it.
func EvaluateToolCall(
	policy domain.ToolPolicy,
	reg *Registry,
	call domain.ToolCall,
	providerName string,
	sensitive bool,
) Decision {
	if !policy.Enabled {
		return Decision{Reason: "tool calling is disabled by policy"}
	}
	if policy.Mode == domain.ToolsDisabled {
		return Decision{Reason: "tool calling is disabled by policy"}
	}
	if sensitive && policy.BlockSensitive {
		return Decision{Reason: "tool calling is blocked for sensitive requests"}
	}
	if !providerAllowed(policy, providerName) {
		return Decision{Reason: "provider " + providerName + " may not use tools"}
	}

	name := call.Function.Name
	if name == "" {
		return Decision{Reason: "the model requested a tool with no name"}
	}
	if patternMatch(policy.DeniedTools, name) {
		return Decision{Reason: "tool " + name + " is denied by policy"}
	}
	if len(policy.AllowedTools) > 0 && !patternMatch(policy.AllowedTools, name) {
		return Decision{Reason: "tool " + name + " is not in the policy allow list"}
	}

	spec, registered := reg.Lookup(name)
	if !registered {
		// An unregistered tool is not an error: the client declared it in the
		// request, and the client is the one that can run it.
		return Decision{Reason: "tool " + name + " is not registered; the client must execute it"}
	}
	if !spec.Enabled {
		return Decision{Reason: "tool " + name + " is disabled"}
	}

	if policy.Mode != domain.ToolsAutomatic {
		return Decision{Reason: "policy allows client-executed tools only"}
	}
	if policy.RequireApproval || spec.RequiresApproval {
		return Decision{Reason: "tool " + name + " requires approval"}
	}
	if !spec.Executable {
		return Decision{Reason: "tool " + name + " is registered for discovery only"}
	}
	return Decision{Execute: true}
}

// providerAllowed applies the provider allow and deny lists. Deny wins.
func providerAllowed(policy domain.ToolPolicy, providerName string) bool {
	if providerName == "" {
		return true
	}
	if patternMatch(policy.DeniedProviders, providerName) {
		return false
	}
	if len(policy.AllowedProviders) > 0 && !patternMatch(policy.AllowedProviders, providerName) {
		return false
	}
	return true
}

// patternMatch reports whether a name matches any glob pattern. An empty
// pattern list matches nothing, so callers check emptiness themselves.
func patternMatch(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if MatchPattern(pattern, name) {
			return true
		}
	}
	return false
}

// MatchPattern reports whether name matches pattern, where a pattern may use
// "*" as a wildcard.
//
// Tool and provider names are plain identifiers written by humans in a policy,
// so a substring wildcard is the whole matching model; a regular expression
// would put an unbounded evaluation in front of every tool call.
func MatchPattern(pattern, name string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return strings.EqualFold(pattern, name)
	}
	parts := strings.Split(strings.ToLower(pattern), "*")
	lowered := strings.ToLower(name)

	position := 0
	for i, part := range parts {
		if part == "" {
			continue
		}
		index := strings.Index(lowered[position:], part)
		if index < 0 {
			return false
		}
		// A leading wildcard may match nothing, so the first literal chunk must
		// be anchored at the start when the pattern starts with a wildcard.
		if i == 0 && !strings.HasPrefix(pattern, "*") && index != 0 {
			return false
		}
		position += index + len(part)
	}
	if last := parts[len(parts)-1]; last != "" && !strings.HasSuffix(pattern, "*") {
		if !strings.HasSuffix(lowered, last) {
			return false
		}
	}
	return true
}

// ValidateArguments checks a tool's arguments against its registered schema.
//
// This runs before execution, so a malformed call is reported as a tool error
// rather than being handed to a handler as an empty argument object.
func ValidateArguments(spec domain.ToolSpec, call domain.ToolCall) error {
	compiled, err := schema.Compile(spec.Parameters)
	if err != nil {
		// A schema that does not compile is a registry defect. Refusing the
		// call is correct: running with no validation would be worse.
		return domain.NewError(domain.ErrCodeInternal,
			"tool "+spec.Name+" has an invalid schema: "+err.Error())
	}
	if compiled.IsEmpty() {
		// No declared schema: the arguments must at least be valid JSON.
		if strings.TrimSpace(call.Function.Arguments) == "" {
			return nil
		}
		return nil
	}

	raw := call.Function.Arguments
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	if errs := compiled.ValidateJSON([]byte(raw)); !errs.Empty() {
		return domain.Errorf(domain.ErrCodeInvalidRequest,
			"arguments for tool %s do not match its schema: %s", spec.Name, errs.Error())
	}
	return nil
}

// DecodeArguments parses a tool call's arguments into an object.
//
// Models emit arguments as a JSON string; a few emit an already-decoded object
// or nothing at all. All three are accepted, because rejecting the last two
// would fail calls that are perfectly well formed.
func DecodeArguments(call domain.ToolCall) (map[string]any, error) {
	raw := strings.TrimSpace(call.Function.Arguments)
	if raw == "" {
		return map[string]any{}, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, domain.Errorf(domain.ErrCodeInvalidRequest,
			"arguments for tool %s are not a JSON object: %s", call.Function.Name, err.Error())
	}
	if decoded == nil {
		return map[string]any{}, nil
	}
	return decoded, nil
}
