package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shadowsafin/corerouter/internal/domain"
)

// Handler executes one registered tool.
//
// A handler receives the decoded argument object and returns the text to feed
// back into the conversation. It must honour the context deadline: the loop
// runs under a wall-clock bound, and a handler that ignores cancellation turns
// that bound into a suggestion.
type Handler func(ctx context.Context, arguments map[string]any) (string, error)

// HandlerNames lists the built-in handler names the registry accepts.
//
// The set is deliberately tiny and closed. Every handler here is deterministic,
// touches no network and no external system, which is what makes automatic
// execution defensible at all: the gateway can run them without becoming a
// confused deputy for anything an operator pointed it at.
var HandlerNames = []string{"now", "echo"}

// HandlerKnown reports whether a handler name is registered.
func HandlerKnown(name string) bool {
	for _, candidate := range HandlerNames {
		if candidate == name {
			return true
		}
	}
	return false
}

// BuiltinHandlers returns the built-in implementations by name.
func BuiltinHandlers() map[string]Handler {
	return map[string]Handler{
		// now returns the gateway clock. Useful for grounding time-sensitive
		// prompts, and it cannot leak anything.
		"now": func(ctx context.Context, arguments map[string]any) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			layout, _ := arguments["format"].(string)
			if layout == "" {
				layout = time.RFC3339
			}
			return time.Now().UTC().Format(layout), nil
		},

		// echo returns its input, which makes multi-step flows testable end to
		// end without touching anything real.
		"echo": func(ctx context.Context, arguments map[string]any) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if raw, ok := arguments["value"]; ok {
				encoded, err := json.Marshal(raw)
				if err != nil {
					return "", err
				}
				return string(encoded), nil
			}
			return "", nil
		},
	}
}

// Executor runs a validated tool call and records it.
//
// The executor owns argument validation, the safety ceiling on the result, and
// the invocation record, so the loop never has to remember those rules.
type Executor struct {
	handlers map[string]Handler
	sink     InvocationSink
	policy   domain.ToolPolicy
	// now is injectable so tests can assert timing without sleeping.
	now func() time.Time
}

// NewExecutor builds an executor over the built-in handlers.
func NewExecutor(sink InvocationSink, policy domain.ToolPolicy) *Executor {
	return &Executor{
		handlers: BuiltinHandlers(),
		sink:     sink,
		policy:   policy,
		now:      time.Now,
	}
}

// WithHandlers replaces the handler set. It exists so tests can exercise the
// loop without depending on the clock behaviour.
func (e *Executor) WithHandlers(handlers map[string]Handler) *Executor {
	e.handlers = handlers
	return e
}

// Result is the outcome of one executed tool call.
type Result struct {
	Invocation domain.ToolInvocation
	Execution  *domain.ToolExecution
}

// Execute runs one tool call that policy has already approved.
//
// The returned invocation is always populated, including on failure, because
// "the model called X and it broke" is exactly the fact an operator needs and
// it is not recoverable from the model final sentence.
func (e *Executor) Execute(
	ctx context.Context,
	spec domain.ToolSpec,
	call domain.ToolCall,
	invocationBase domain.ToolInvocation,
) Result {
	invocation := invocationBase
	invocation.ID = domain.NewID()
	invocation.ToolName = spec.Name
	invocation.ToolCallID = call.ID
	invocation.Arguments = call.Function.Arguments
	invocation.CreatedAt = e.now().UTC()

	arguments, err := DecodeArguments(call)
	if err != nil {
		invocation.Status = domain.InvocationInvalid
		invocation.DenyReason = err.Error()
		e.record(ctx, invocation, nil)
		return Result{Invocation: invocation}
	}
	invocation.ParsedArguments = arguments

	if err := ValidateArguments(spec, call); err != nil {
		invocation.Status = domain.InvocationInvalid
		invocation.DenyReason = err.Error()
		e.record(ctx, invocation, nil)
		return Result{Invocation: invocation}
	}

	handler, ok := e.handlers[spec.Handler]
	if !ok {
		invocation.Status = domain.InvocationSkipped
		invocation.DenyReason = fmt.Sprintf("no built-in handler named %q", spec.Handler)
		e.record(ctx, invocation, nil)
		return Result{Invocation: invocation}
	}

	started := e.now()
	content, err := handler(ctx, arguments)
	elapsed := e.now().Sub(started).Milliseconds()
	invocation.LatencyMS = elapsed

	execution := &domain.ToolExecution{
		ID:           domain.NewID(),
		InvocationID: invocation.ID,
		ToolName:     spec.Name,
		Content:      content,
		Success:      err == nil,
		LatencyMS:    elapsed,
		CreatedAt:    e.now().UTC(),
	}
	if err != nil {
		execution.ErrorMessage = err.Error()
		invocation.Status = domain.InvocationFailed
		invocation.ErrorCode = string(domain.ErrCodeInternal)
		invocation.DenyReason = err.Error()
		e.record(ctx, invocation, execution)
		return Result{Invocation: invocation, Execution: execution}
	}

	// A tool that ignores its result budget would otherwise be able to blow past
	// the run token ceiling with a single call.
	if e.policy.MaxResultBytes > 0 && len(content) > e.policy.MaxResultBytes {
		execution.Content = content[:e.policy.MaxResultBytes] +
			"\n[truncated by the gateway result limit]"
		invocation.ResultBytes = len(execution.Content)
	}

	invocation.Status = domain.InvocationExecuted
	invocation.ResultBytes = len(execution.Content)
	e.record(ctx, invocation, execution)

	return Result{Invocation: invocation, Execution: execution}
}

// record persists the invocation and its execution.
//
// Persistence failures are swallowed deliberately: the caller already has its
// answer, and failing the request because an audit insert failed would leave
// the client view and the audit trail disagreeing, which is worse than a
// missing row.
func (e *Executor) record(ctx context.Context, invocation domain.ToolInvocation, execution *domain.ToolExecution) {
	if e.sink == nil {
		return
	}
	saved := invocation
	if err := e.sink.InsertInvocation(ctx, &saved); err != nil {
		return
	}
	if execution != nil {
		execution.InvocationID = saved.ID
		_ = e.sink.InsertExecution(ctx, execution)
	}
}

// ToolMessage builds the conversation message that carries a tool result back
// to the model.
//
// The role, the call id and the tool name are all required by the OpenAI shape,
// and the model needs them to associate the result with its own request; a
// result injected without the id is silently dropped by several providers.
func ToolMessage(call domain.ToolCall, toolName, content string) domain.ChatMessage {
	return domain.ChatMessage{
		Role:       domain.RoleTool,
		Name:       toolName,
		ToolCallID: call.ID,
		Content:    domain.NewTextContent(content),
	}
}

// ErrorMessageFor renders a tool failure as the text the model should see.
//
// The model is told the tool failed and why, rather than being handed an empty
// result: a tool error the model cannot see produces a confidently wrong answer,
// which is the worst outcome of the whole feature.
func ErrorMessageFor(err error) string {
	if err == nil {
		return "the tool failed"
	}
	return "the tool failed: " + err.Error()
}

// SummarizeToolCall renders a compact description of a call for audit metadata
// and the dashboard, without dumping a large argument payload into a log line.
func SummarizeToolCall(call domain.ToolCall, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 200
	}
	arguments := strings.TrimSpace(call.Function.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	if len(arguments) > maxLen {
		arguments = arguments[:maxLen] + "..."
	}
	return call.Function.Name + "(" + arguments + ")"
}
