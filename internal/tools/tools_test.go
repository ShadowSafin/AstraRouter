package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/corerouter/corerouter/internal/domain"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func nowSpec() domain.ToolSpec {
	return domain.ToolSpec{
		ID:          "t-now",
		Name:        "now",
		Description: "current time",
		Kind:        domain.ToolBuiltin,
		Owner:       domain.OwnerPlatform,
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		SafetyLevel: domain.SafetySafe,
		Executable:  true,
		Handler:     "now",
		Enabled:     true,
	}
}

func echoSpec() domain.ToolSpec {
	return domain.ToolSpec{
		ID:          "t-echo",
		Name:        "echo",
		Kind:        domain.ToolBuiltin,
		Owner:       domain.OwnerPlatform,
		Parameters:  json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`),
		SafetyLevel: domain.SafetySafe,
		Executable:  true,
		Handler:     "echo",
		Enabled:     true,
	}
}

func externalSpec() domain.ToolSpec {
	return domain.ToolSpec{
		ID:          "t-ext",
		Name:        "web_search",
		Kind:        domain.ToolExternal,
		Owner:       domain.OwnerPlatform,
		Parameters:  json.RawMessage(`{"type":"object"}`),
		SafetyLevel: domain.SafetySensitive,
		Enabled:     true,
	}
}

func automaticPolicy() domain.ToolPolicy {
	p := domain.DefaultToolPolicy()
	p.Mode = domain.ToolsAutomatic
	p.MaxSteps = 4
	p.MaxToolCalls = 6
	return p
}

func callNamed(name string) domain.ToolCall {
	return domain.ToolCall{
		ID:       "call_" + name,
		Function: domain.FunctionCall{Name: name, Arguments: `{}`},
	}
}

// scriptedCompleter replays a fixed sequence of completions.
type scriptedCompleter struct {
	turns   []*Completion
	calls   int
	seen    [][]domain.ChatMessage
	failure error
}

func (s *scriptedCompleter) Complete(_ context.Context, messages []domain.ChatMessage) (*Completion, error) {
	s.seen = append(s.seen, append([]domain.ChatMessage(nil), messages...))
	if s.failure != nil {
		return nil, s.failure
	}
	if s.calls >= len(s.turns) {
		return &Completion{Content: "done", FinishReason: domain.FinishStop}, nil
	}
	turn := s.turns[s.calls]
	s.calls++
	return turn, nil
}

// slowCompleter always requests a tool, after a delay.
type slowCompleter struct{ delay time.Duration }

func (s *slowCompleter) Complete(ctx context.Context, _ []domain.ChatMessage) (*Completion, error) {
	select {
	case <-time.After(s.delay):
		return &Completion{
			ToolCalls:    []domain.ToolCall{callNamed("now")},
			FinishReason: domain.FinishToolCalls,
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// recordingSink captures invocations for assertions.
type recordingSink struct {
	invocations []domain.ToolInvocation
	executions  []domain.ToolExecution
}

func (r *recordingSink) InsertInvocation(_ context.Context, invocation *domain.ToolInvocation) error {
	r.invocations = append(r.invocations, *invocation)
	return nil
}

func (r *recordingSink) InsertExecution(_ context.Context, execution *domain.ToolExecution) error {
	r.executions = append(r.executions, *execution)
	return nil
}

type recordingRunSink struct {
	runs  []RunRecord
	steps []RunStep
}

func (r *recordingRunSink) UpsertRun(_ context.Context, run RunRecord) error {
	r.runs = append(r.runs, run)
	return nil
}

func (r *recordingRunSink) InsertRunStep(_ context.Context, _ string, step RunStep) error {
	r.steps = append(r.steps, step)
	return nil
}

func userTurn(text string) []domain.ChatMessage {
	return []domain.ChatMessage{{Role: domain.RoleUser, Content: domain.NewTextContent(text)}}
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

func TestBuildRegistryHidesOtherTenantsTools(t *testing.T) {
	platform := nowSpec()
	tenantTool := echoSpec()
	tenantTool.Owner = domain.OwnerTenant
	tenantTool.TenantID = "tenant-a"

	reg := BuildRegistry([]domain.ToolSpec{platform, tenantTool}, "tenant-b")
	if _, ok := reg.Lookup("now"); !ok {
		t.Error("a platform tool must be visible to every tenant")
	}
	if _, ok := reg.Lookup("echo"); ok {
		t.Error("another tenant's tool must not be visible")
	}

	owner := BuildRegistry([]domain.ToolSpec{platform, tenantTool}, "tenant-a")
	if _, ok := owner.Lookup("echo"); !ok {
		t.Error("a tenant must see its own tools")
	}
}

func TestAdvertiseSkipsDisabledTools(t *testing.T) {
	disabled := echoSpec()
	disabled.Enabled = false
	reg := BuildRegistry([]domain.ToolSpec{nowSpec(), disabled}, "")
	advertised := reg.Advertise()
	if len(advertised) != 1 || advertised[0].Function.Name != "now" {
		t.Fatalf("advertised = %+v, want only the enabled tool", advertised)
	}
	if reg.Len() != 2 {
		t.Errorf("the registry should still hold both entries, got %d", reg.Len())
	}
}

func TestWireRendersStrictFlag(t *testing.T) {
	spec := echoSpec()
	spec.Strict = true
	wire := spec.Wire()
	if wire.Function.Strict == nil || !*wire.Function.Strict {
		t.Error("a strict tool must advertise strict")
	}
}

// ---------------------------------------------------------------------------
// Pattern matching
// ---------------------------------------------------------------------------

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"now", "now", true},
		{"NOW", "now", true},
		{"web_*", "web_search", true},
		{"*_search", "web_search", true},
		{"*search*", "web_search_v2", true},
		{"*", "anything", true},
		{"web_*", "db_search", false},
		{"", "now", false},
		{"gpt-*", "gpt-4o-mini", true},
		{"gpt-*", "claude", false},
		{"*-mini", "gpt-4o-mini", true},
	}
	for _, c := range cases {
		if got := MatchPattern(c.pattern, c.name); got != c.want {
			t.Errorf("MatchPattern(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Policy evaluation
// ---------------------------------------------------------------------------

func TestEvaluateToolCallManualModeNeverExecutes(t *testing.T) {
	decision := EvaluateToolCall(domain.DefaultToolPolicy(),
		BuildRegistry([]domain.ToolSpec{nowSpec()}, ""), callNamed("now"), "bynara", false)
	if decision.Execute {
		t.Error("manual mode must never let the gateway execute a tool")
	}
	if decision.Reason == "" {
		t.Error("a refusal must explain itself")
	}
}

func TestEvaluateToolCallAutomaticExecutesBuiltin(t *testing.T) {
	decision := EvaluateToolCall(automaticPolicy(),
		BuildRegistry([]domain.ToolSpec{nowSpec()}, ""), callNamed("now"), "bynara", false)
	if !decision.Execute {
		t.Fatalf("an approved builtin must execute: %s", decision.Reason)
	}
}

func TestEvaluateToolCallRefusesExternalTool(t *testing.T) {
	// The core safety property: a registered external tool is advertised but
	// never executed by the gateway.
	decision := EvaluateToolCall(automaticPolicy(),
		BuildRegistry([]domain.ToolSpec{externalSpec()}, ""), callNamed("web_search"), "bynara", false)
	if decision.Execute {
		t.Fatal("an external tool must never be executed by the gateway")
	}
	if !strings.Contains(decision.Reason, "discovery only") {
		t.Errorf("reason = %q", decision.Reason)
	}
}

func TestEvaluateToolCallDenyListWins(t *testing.T) {
	policy := automaticPolicy()
	policy.DeniedTools = []string{"now"}
	decision := EvaluateToolCall(policy,
		BuildRegistry([]domain.ToolSpec{nowSpec()}, ""), callNamed("now"), "bynara", false)
	if decision.Execute || !strings.Contains(decision.Reason, "denied") {
		t.Errorf("a deny-listed tool must be refused: %+v", decision)
	}
}

func TestEvaluateToolCallApprovalRequirement(t *testing.T) {
	spec := nowSpec()
	spec.RequiresApproval = true
	decision := EvaluateToolCall(automaticPolicy(),
		BuildRegistry([]domain.ToolSpec{spec}, ""), callNamed("now"), "bynara", false)
	if decision.Execute || !strings.Contains(decision.Reason, "approval") {
		t.Errorf("a tool requiring approval must be refused: %+v", decision)
	}
}

func TestEvaluateToolCallProviderDeny(t *testing.T) {
	policy := automaticPolicy()
	policy.DeniedProviders = []string{"bynara*"}
	decision := EvaluateToolCall(policy,
		BuildRegistry([]domain.ToolSpec{nowSpec()}, ""), callNamed("now"), "bynara1", false)
	if decision.Execute || !strings.Contains(decision.Reason, "may not use tools") {
		t.Errorf("a denied provider must be refused: %+v", decision)
	}
}

func TestEvaluateToolCallSensitiveBlocked(t *testing.T) {
	policy := automaticPolicy()
	policy.BlockSensitive = true
	decision := EvaluateToolCall(policy,
		BuildRegistry([]domain.ToolSpec{nowSpec()}, ""), callNamed("now"), "bynara", true)
	if decision.Execute || !strings.Contains(decision.Reason, "sensitive") {
		t.Errorf("a sensitive request must be refused when policy says so: %+v", decision)
	}
}

func TestEvaluateToolCallUnregisteredGoesToClient(t *testing.T) {
	decision := EvaluateToolCall(automaticPolicy(),
		BuildRegistry([]domain.ToolSpec{nowSpec()}, ""), callNamed("client_side"), "bynara", false)
	if decision.Execute {
		t.Fatal("an unregistered tool must not be executed")
	}
	if !strings.Contains(decision.Reason, "client must execute") {
		t.Errorf("reason = %q", decision.Reason)
	}
}

func TestEffectivePolicyFallsBackToDefaults(t *testing.T) {
	policy := EffectivePolicy(context.Background(), nil, "t-1")
	if policy.Mode != domain.ToolsManual {
		t.Errorf("a missing policy source must yield the manual default, got %q", policy.Mode)
	}
	if policy.MaxSteps <= 0 || policy.MaxToolCalls <= 0 {
		t.Errorf("defaults must be bounded, got %+v", policy)
	}
}

// ---------------------------------------------------------------------------
// Argument validation
// ---------------------------------------------------------------------------

func TestValidateArgumentsRejectsSchemaViolation(t *testing.T) {
	err := ValidateArguments(echoSpec(), domain.ToolCall{
		Function: domain.FunctionCall{Name: "echo", Arguments: `{"value": 42}`},
	})
	if err == nil {
		t.Fatal("arguments violating the declared schema must be refused")
	}
	if !strings.Contains(err.Error(), "echo") {
		t.Errorf("the error should name the tool: %v", err)
	}
}

func TestDecodeArgumentsAcceptsEmptyAndObject(t *testing.T) {
	if _, err := DecodeArguments(domain.ToolCall{
		Function: domain.FunctionCall{Name: "x"},
	}); err != nil {
		t.Errorf("empty arguments must be accepted: %v", err)
	}
	if _, err := DecodeArguments(domain.ToolCall{
		Function: domain.FunctionCall{Name: "x", Arguments: `{"a":1}`},
	}); err != nil {
		t.Errorf("a JSON object must be accepted: %v", err)
	}
	if _, err := DecodeArguments(domain.ToolCall{
		Function: domain.FunctionCall{Name: "x", Arguments: `not json`},
	}); err == nil {
		t.Error("non-JSON arguments must be refused")
	}
}

// ---------------------------------------------------------------------------
// Executor
// ---------------------------------------------------------------------------

func TestExecutorRunsBuiltinAndRecords(t *testing.T) {
	sink := &recordingSink{}
	executor := NewExecutor(sink, automaticPolicy()).WithHandlers(map[string]Handler{
		"echo": func(_ context.Context, args map[string]any) (string, error) {
			return "echoed:" + args["value"].(string), nil
		},
	})

	out := executor.Execute(context.Background(), echoSpec(), domain.ToolCall{
		ID:       "call_1",
		Function: domain.FunctionCall{Name: "echo", Arguments: `{"value":"hi"}`},
	}, domain.ToolInvocation{RequestID: "req-1"})

	if out.Invocation.Status != domain.InvocationExecuted {
		t.Fatalf("status = %q (%s)", out.Invocation.Status, out.Invocation.DenyReason)
	}
	if out.Execution == nil || out.Execution.Content != "echoed:hi" {
		t.Fatalf("execution = %+v", out.Execution)
	}
	if len(sink.invocations) != 1 || len(sink.executions) != 1 {
		t.Errorf("sink recorded %d invocations and %d executions",
			len(sink.invocations), len(sink.executions))
	}
	if sink.invocations[0].ToolCallID != "call_1" {
		t.Errorf("the call id must be recorded: %+v", sink.invocations[0])
	}
}

func TestExecutorRecordsInvalidArgumentsWithoutRunning(t *testing.T) {
	ran := false
	executor := NewExecutor(&recordingSink{}, automaticPolicy()).WithHandlers(map[string]Handler{
		"echo": func(context.Context, map[string]any) (string, error) {
			ran = true
			return "", nil
		},
	})

	out := executor.Execute(context.Background(), echoSpec(), domain.ToolCall{
		Function: domain.FunctionCall{Name: "echo", Arguments: `{"nope": true}`},
	}, domain.ToolInvocation{})

	if ran {
		t.Error("a handler must not run with arguments that fail its schema")
	}
	if out.Invocation.Status != domain.InvocationInvalid {
		t.Errorf("status = %q", out.Invocation.Status)
	}
}

func TestExecutorTruncatesOversizedResult(t *testing.T) {
	policy := automaticPolicy()
	policy.MaxResultBytes = 16
	executor := NewExecutor(&recordingSink{}, policy).WithHandlers(map[string]Handler{
		"echo": func(context.Context, map[string]any) (string, error) {
			return strings.Repeat("x", 500), nil
		},
	})

	out := executor.Execute(context.Background(), echoSpec(), domain.ToolCall{
		Function: domain.FunctionCall{Name: "echo", Arguments: `{"value":"v"}`},
	}, domain.ToolInvocation{})
	if out.Execution == nil {
		t.Fatal("expected an execution")
	}
	if len(out.Execution.Content) > 80 {
		t.Errorf("a runaway result must be truncated, got %d bytes", len(out.Execution.Content))
	}
	if !strings.Contains(out.Execution.Content, "truncated") {
		t.Error("the truncation must be visible to the model")
	}
}

func TestExecutorRecordsHandlerFailure(t *testing.T) {
	executor := NewExecutor(&recordingSink{}, automaticPolicy()).WithHandlers(map[string]Handler{
		"echo": func(context.Context, map[string]any) (string, error) {
			return "", errors.New("the tool exploded")
		},
	})
	out := executor.Execute(context.Background(), echoSpec(), domain.ToolCall{
		Function: domain.FunctionCall{Name: "echo", Arguments: `{"value":"v"}`},
	}, domain.ToolInvocation{})
	if out.Invocation.Status != domain.InvocationFailed {
		t.Fatalf("status = %q", out.Invocation.Status)
	}
	if out.Execution == nil || out.Execution.Success {
		t.Fatalf("execution = %+v", out.Execution)
	}
}

// ---------------------------------------------------------------------------
// Loop
// ---------------------------------------------------------------------------

func TestLoopSingleTurnAnswersImmediately(t *testing.T) {
	completer := &scriptedCompleter{turns: []*Completion{
		{Content: "hello", FinishReason: domain.FinishStop},
	}}
	runs := &recordingRunSink{}
	result, err := Loop(context.Background(), completer,
		NewExecutor(&recordingSink{}, automaticPolicy()), runs, LoopOptions{
			Mode:            domain.ToolsAutomatic,
			Policy:          automaticPolicy(),
			Registry:        BuildRegistry([]domain.ToolSpec{echoSpec()}, ""),
			InitialMessages: userTurn("hi"),
			RunID:           "run-1",
		})
	if err != nil {
		t.Fatalf("Loop: %v", err)
	}
	if result.Steps != 1 || result.Status != domain.RunCompleted {
		t.Errorf("steps = %d, status = %q", result.Steps, result.Status)
	}
	if result.Final == nil || result.Final.Content != "hello" {
		t.Errorf("final = %+v", result.Final)
	}
	if len(runs.runs) == 0 {
		t.Error("the run must be recorded")
	}
}

func TestLoopExecutesToolThenFinishes(t *testing.T) {
	toolCall := callNamed("echo")
	toolCall.Function.Arguments = `{"value":"ping"}`
	completer := &scriptedCompleter{turns: []*Completion{
		{ToolCalls: []domain.ToolCall{toolCall}, FinishReason: domain.FinishToolCalls},
		{Content: "the tool said ping", FinishReason: domain.FinishStop},
	}}
	sink := &recordingSink{}
	executor := NewExecutor(sink, automaticPolicy()).WithHandlers(map[string]Handler{
		"echo": func(context.Context, map[string]any) (string, error) { return "ping", nil },
	})

	result, err := Loop(context.Background(), completer, executor, &recordingRunSink{}, LoopOptions{
		Mode:            domain.ToolsAutomatic,
		Policy:          automaticPolicy(),
		Registry:        BuildRegistry([]domain.ToolSpec{echoSpec()}, ""),
		InitialMessages: userTurn("ping please"),
		RunID:           "run-2",
	})
	if err != nil {
		t.Fatalf("Loop: %v", err)
	}
	if result.Steps != 2 || result.ToolCalls != 1 {
		t.Fatalf("steps = %d, tool calls = %d", result.Steps, result.ToolCalls)
	}
	if result.Status != domain.RunCompleted {
		t.Errorf("status = %q", result.Status)
	}
	if !result.Ran {
		t.Error("a run that executed a tool must report it")
	}

	// The second turn must see the assistant tool call and the tool result.
	finalMessages := completer.seen[1]
	if len(finalMessages) < 3 {
		t.Fatalf("conversation has %d messages: %+v", len(finalMessages), finalMessages)
	}
	assistant := finalMessages[len(finalMessages)-2]
	if assistant.Role != domain.RoleAssistant || len(assistant.ToolCalls) != 1 {
		t.Errorf("expected an assistant tool-call turn, got %+v", assistant)
	}
	resultMessage := finalMessages[len(finalMessages)-1]
	if resultMessage.Role != domain.RoleTool || resultMessage.Text() != "ping" {
		t.Errorf("expected the tool result to be injected, got %+v", resultMessage)
	}
	if resultMessage.ToolCallID != toolCall.ID {
		t.Errorf("the tool result must carry the call id, got %q", resultMessage.ToolCallID)
	}
	if len(sink.executions) != 1 {
		t.Errorf("expected one recorded execution, got %d", len(sink.executions))
	}
}

func TestLoopStopsAtStepLimit(t *testing.T) {
	// A model that always asks for another tool is the loop worst case, and it
	// must terminate.
	alwaysTools := make([]*Completion, 0, 8)
	for i := 0; i < 8; i++ {
		alwaysTools = append(alwaysTools, &Completion{
			ToolCalls:    []domain.ToolCall{callNamed("echo")},
			FinishReason: domain.FinishToolCalls,
		})
	}
	policy := automaticPolicy()
	policy.MaxSteps = 3
	completer := &scriptedCompleter{turns: alwaysTools}
	executor := NewExecutor(&recordingSink{}, policy).WithHandlers(map[string]Handler{
		"echo": func(context.Context, map[string]any) (string, error) { return "ok", nil },
	})

	result, err := Loop(context.Background(), completer, executor, nil, LoopOptions{
		Mode:            domain.ToolsAutomatic,
		Policy:          policy,
		Registry:        BuildRegistry([]domain.ToolSpec{echoSpec()}, ""),
		InitialMessages: userTurn("loop"),
		RunID:           "run-3",
	})
	if err != nil {
		t.Fatalf("Loop: %v", err)
	}
	if result.Status != domain.RunStepLimit {
		t.Fatalf("status = %q, want step_limit", result.Status)
	}
	if result.Steps > policy.MaxSteps {
		t.Errorf("steps = %d exceeded the limit of %d", result.Steps, policy.MaxSteps)
	}
}

func TestLoopStopsAtCallLimit(t *testing.T) {
	policy := automaticPolicy()
	policy.MaxSteps = 5
	policy.MaxToolCalls = 2
	completer := &scriptedCompleter{turns: []*Completion{
		{
			ToolCalls:    []domain.ToolCall{callNamed("echo"), callNamed("echo")},
			FinishReason: domain.FinishToolCalls,
		},
		{Content: "done", FinishReason: domain.FinishStop},
	}}
	executor := NewExecutor(&recordingSink{}, policy).WithHandlers(map[string]Handler{
		"echo": func(context.Context, map[string]any) (string, error) { return "ok", nil },
	})

	result, err := Loop(context.Background(), completer, executor, nil, LoopOptions{
		Mode:            domain.ToolsAutomatic,
		Policy:          policy,
		Registry:        BuildRegistry([]domain.ToolSpec{echoSpec()}, ""),
		InitialMessages: userTurn("many"),
		RunID:           "run-4",
	})
	if err != nil {
		t.Fatalf("Loop: %v", err)
	}
	if result.ToolCalls > policy.MaxToolCalls {
		t.Errorf("tool calls = %d exceeded the limit of %d", result.ToolCalls, policy.MaxToolCalls)
	}
	if result.Status != domain.RunCallLimit && result.Status != domain.RunCompleted {
		t.Errorf("status = %q", result.Status)
	}
}

func TestLoopSkipsUnapprovedToolAndTellsTheModel(t *testing.T) {
	// Manual mode: the client executes tools, so the loop must hand the call
	// back with an explanation rather than failing the request.
	policy := domain.DefaultToolPolicy()
	completer := &scriptedCompleter{turns: []*Completion{
		{ToolCalls: []domain.ToolCall{callNamed("echo")}, FinishReason: domain.FinishToolCalls},
		{Content: "understood", FinishReason: domain.FinishStop},
	}}
	result, err := Loop(context.Background(), completer, NewExecutor(nil, policy), nil, LoopOptions{
		Mode:            domain.ToolsManual,
		Policy:          policy,
		Registry:        BuildRegistry([]domain.ToolSpec{echoSpec()}, ""),
		InitialMessages: userTurn("echo"),
		RunID:           "run-5",
	})
	if err != nil {
		t.Fatalf("Loop: %v", err)
	}
	if result.Ran {
		t.Error("manual mode must not execute tools in the gateway")
	}
	second := completer.seen[1]
	last := second[len(second)-1]
	if last.Role != domain.RoleTool || !strings.Contains(last.Text(), "not executed by the gateway") {
		t.Errorf("the model must be told the call was not executed, got %+v", last)
	}
	if result.Status != domain.RunCompleted {
		t.Errorf("status = %q, want completed", result.Status)
	}
}

func TestLoopStopsOnTimeLimit(t *testing.T) {
	policy := automaticPolicy()
	policy.MaxSteps = 50
	policy.MaxRunSeconds = 1
	slow := &slowCompleter{delay: 400 * time.Millisecond}
	result, err := Loop(context.Background(), slow, NewExecutor(nil, policy), nil, LoopOptions{
		Mode:            domain.ToolsAutomatic,
		Policy:          policy,
		Registry:        BuildRegistry([]domain.ToolSpec{nowSpec()}, ""),
		InitialMessages: userTurn("wait"),
		RunID:           "run-6",
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Loop: %v", err)
	}
	if result.Status != domain.RunTimeLimit && result.Status != domain.RunFailed {
		t.Errorf("status = %q, want a time-bound stop", result.Status)
	}
}

func TestLoopHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	completer := &scriptedCompleter{turns: []*Completion{{Content: "x"}}}
	result, err := Loop(ctx, completer, NewExecutor(nil, automaticPolicy()), nil, LoopOptions{
		Mode:     domain.ToolsAutomatic,
		Policy:   automaticPolicy(),
		Registry: BuildRegistry(nil, ""),
		RunID:    "run-7",
	})
	if err == nil {
		t.Error("a cancelled context must surface an error")
	}
	if result == nil {
		t.Fatal("the loop must always return a result for observability")
	}
}
