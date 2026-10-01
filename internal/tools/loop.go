package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/shadowsafin/corerouter/internal/domain"
)

// Completer performs one model round-trip.
//
// It is the seam between the loop and the routing/provider machinery: the HTTP
// layer supplies a closure that resolves, executes and returns a completion, so
// this package stays free of routing, storage and transport concerns and the
// loop can be tested against a scripted model.
type Completer interface {
	Complete(ctx context.Context, messages []domain.ChatMessage) (*Completion, error)
}

// Completion is one model round-trip result.
type Completion struct {
	Content   string
	ToolCalls []domain.ToolCall
	// FinishReason is the model own verdict, used to decide whether the loop
	// should continue.
	FinishReason domain.FinishReason
	Provider     string
	Model        string
	// Usage across every turn of the run, accumulated by the caller.
	Usage domain.TokenUsage
	// Raw carries the provider response so the loop final answer can be
	// rendered with the same code path as a single-turn request.
	Raw any
}

// WantsTools reports whether the model asked for at least one tool.
func (c *Completion) WantsTools() bool { return c != nil && len(c.ToolCalls) > 0 }

// LoopOptions configures one bounded tool run.
type LoopOptions struct {
	// Mode is the request execution mode. Only ToolsAutomatic runs the loop.
	Mode domain.ToolPolicyMode
	// TenantID, RequestID and Provider attribute every recorded step.
	TenantID  string
	RequestID domain.RequestID
	Provider  string
	// Sensitive marks the request as carrying sensitive data, which policy may
	// refuse outright.
	Sensitive bool
	// InitialMessages is the conversation so far. It is never mutated: each step
	// copies, so a caller can retry the run.
	InitialMessages []domain.ChatMessage
	// Registry and Policy decide what may run.
	Registry *Registry
	Policy   domain.ToolPolicy
	// RunID is the pre-allocated durable run identifier, so every step row and
	// invocation can reference it without a round trip.
	RunID string
}

// LoopResult is the outcome of a bounded run.
type LoopResult struct {
	// Messages is the final conversation, including injected tool results.
	Messages []domain.ChatMessage
	// Final is the last completion, which is what the response is built from.
	Final *Completion
	// Steps is how many model round-trips happened.
	Steps int
	// ToolCalls is how many tool calls were made in total.
	ToolCalls int
	// Status is why the loop stopped.
	Status domain.AgentRunStatus
	// StopReason is a human-readable explanation for the dashboard.
	StopReason string
	// LatencyMS is the whole run wall-clock time.
	LatencyMS int64
	// Invocations is every tool call made, in order.
	Invocations []domain.ToolInvocation
	// Provider and Model are the serving target of the final turn.
	Provider string
	Model    string
	// Ran reports whether the gateway executed any tool at all.
	Ran bool
	// PersistError is the first failure from writing the run trace to the sink.
	// A run is not failed by this: the client still gets its answer. It is
	// surfaced so the caller can log it, because a lost trace is otherwise
	// indistinguishable from a run that never happened.
	PersistError error
}

// Loop drives the bounded multi-step tool flow.
//
// The loop is deliberately simple and deliberately bounded: run the model,
// execute whatever it asked for, append the results, repeat, until the model
// answers without asking for tools, or until one of the policy bounds is hit.
// Every bound is checked before the work that would exceed it, so the loop
// always stops at a step boundary with a coherent conversation rather than
// mid-tool.
func Loop(
	ctx context.Context,
	completer Completer,
	executor *Executor,
	sink RunSink,
	opts LoopOptions,
) (*LoopResult, error) {
	policy := opts.Policy
	policy.Normalize()

	started := time.Now()
	messages := append([]domain.ChatMessage(nil), opts.InitialMessages...)
	result := &LoopResult{
		Messages: messages,
		Status:   domain.RunCompleted,
		Provider: opts.Provider,
	}

	// Every run carries a wall-clock deadline so a slow model cannot stretch a
	// turn into a run that outlives the client patience.
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(policy.MaxRunSeconds)*time.Second)
	defer cancel()

	// notePersistence keeps the first sink failure. The run continues either
	// way, so this cannot be returned as an error, but swallowing it silently
	// would leave an operator looking at a completed run with no trace and no
	// explanation.
	notePersistence := func(err error) {
		if err != nil && result.PersistError == nil {
			result.PersistError = err
		}
	}

	recordStep := func(step RunStep) {
		if sink == nil {
			return
		}
		notePersistence(sink.InsertRunStep(runCtx, opts.RunID, step))
	}
	// writeRun persists the run header without touching result, so it can be
	// used to establish the row before the first step is recorded.
	writeRun := func(status domain.AgentRunStatus) {
		if sink == nil {
			return
		}
		notePersistence(sink.UpsertRun(runCtx, RunRecord{
			ID:         opts.RunID,
			RequestID:  opts.RequestID.String(),
			TenantID:   opts.TenantID,
			Status:     status,
			Steps:      result.Steps,
			ToolCalls:  result.ToolCalls,
			Provider:   result.Provider,
			Model:      result.Model,
			LatencyMS:  result.LatencyMS,
			StopReason: result.StopReason,
		}))
	}
	recordRun := func(status domain.AgentRunStatus, reason string) {
		result.Status = status
		result.StopReason = reason
		result.LatencyMS = time.Since(started).Milliseconds()
		writeRun(status)
	}

	// The run row is written before anything can reference it. agent_steps has a
	// foreign key to agent_runs, so recording a step first would fail on every
	// insert and silently leave a completed run with no step trace at all.
	writeRun(domain.RunRunning)

	// The loop always runs at least one turn: the client asked a question and
	// deserves an answer even if no tool is involved.
	for {
		if result.Steps >= policy.MaxSteps {
			recordStep(RunStep{
				Step: result.Steps + 1, Kind: "model", Provider: opts.Provider,
				Detail: map[string]any{"skipped": "step limit"},
			})
			recordRun(domain.RunStepLimit, fmt.Sprintf(
				"stopped at the %d-step limit", policy.MaxSteps))
			return result, nil
		}
		// The caller's cancellation is an abort the caller must be told about,
		// whereas the loop's own deadline is a bounded outcome that still leaves
		// a usable answer. Conflating them would report a disconnect as a
		// successful run.
		if err := ctx.Err(); err != nil {
			recordRun(domain.RunFailed, "the request was cancelled")
			return result, err
		}
		if err := runCtx.Err(); err != nil {
			recordRun(domain.RunTimeLimit, "the run exceeded its time limit")
			return result, nil
		}

		turnStarted := time.Now()
		completion, err := completer.Complete(runCtx, messages)
		if err != nil {
			recordRun(domain.RunFailed, err.Error())
			return result, err
		}
		result.Steps++
		result.Final = completion
		if completion.Provider != "" {
			result.Provider = completion.Provider
		}
		if completion.Model != "" {
			result.Model = completion.Model
		}
		turnLatency := time.Since(turnStarted).Milliseconds()
		recordStep(RunStep{
			Step: result.Steps, Kind: "model",
			Provider: completion.Provider, Model: completion.Model,
			ToolCalls: len(completion.ToolCalls), LatencyMS: turnLatency,
			Tokens: completion.Usage.TotalTokens,
			Detail: map[string]any{
				"finish_reason": string(completion.FinishReason),
				"has_content":   completion.Content != "",
			},
		})

		// A final answer ends the run. Anything else, no tool calls, a refusal,
		// or a provider that cannot call tools, is also final: there is nothing
		// more the loop can usefully do.
		if !completion.WantsTools() {
			recordRun(domain.RunCompleted, "")
			return result, nil
		}

		// The model own tool calls become an assistant turn before the results,
		// because that is the shape every provider expects.
		messages = append(messages, domain.ChatMessage{
			Role:      domain.RoleAssistant,
			Content:   domain.NewTextContent(completion.Content),
			ToolCalls: completion.ToolCalls,
		})

		executed := 0
		for _, call := range completion.ToolCalls {
			if result.ToolCalls >= policy.MaxToolCalls {
				recordRun(domain.RunCallLimit, fmt.Sprintf(
					"stopped at the %d-call limit", policy.MaxToolCalls))
				return result, nil
			}
			result.ToolCalls++

			spec, registered := opts.Registry.Lookup(call.Function.Name)
			decision := EvaluateToolCall(policy, opts.Registry, call, completion.Provider, opts.Sensitive)

			base := domain.ToolInvocation{
				RequestID: opts.RequestID,
				TenantID:  opts.TenantID,
				RunID:     opts.RunID,
				Step:      result.Steps,
				Provider:  completion.Provider,
				Model:     completion.Model,
			}

			if !decision.Execute || !registered {
				// Not ours to run: tell the model so it can proceed with what it
				// has, rather than stalling the conversation.
				base.ID = domain.NewID()
				base.Status = domain.InvocationSkipped
				base.DenyReason = decision.Reason
				base.ToolName = call.Function.Name
				base.ToolCallID = call.ID
				base.Arguments = call.Function.Arguments
				base.CreatedAt = time.Now().UTC()
				result.Invocations = append(result.Invocations, base)
				if executor != nil && executor.sink != nil {
					saved := base
					_ = executor.sink.InsertInvocation(runCtx, &saved)
				}
				messages = append(messages, ToolMessage(call, call.Function.Name,
					"not executed by the gateway: "+decision.Reason))
				continue
			}

			if executor == nil {
				messages = append(messages, ToolMessage(call, call.Function.Name,
					"no tool executor is configured"))
				continue
			}

			outcome := executor.Execute(runCtx, spec, call, base)
			result.Invocations = append(result.Invocations, outcome.Invocation)
			if outcome.Invocation.Status == domain.InvocationExecuted {
				result.Ran = true
			}
			executed++

			var content string
			switch {
			case outcome.Execution == nil:
				// The executor refused the call before producing a payload; its
				// recorded reason is the explanation the model needs.
				reason := outcome.Invocation.DenyReason
				if reason == "" {
					reason = "the tool was not executed"
				}
				content = ErrorMessageFor(fmt.Errorf("%s", reason))
			case outcome.Execution.Success:
				content = outcome.Execution.Content
			default:
				content = ErrorMessageFor(fmt.Errorf("%s", outcome.Execution.ErrorMessage))
			}
			messages = append(messages, ToolMessage(call, spec.Name, content))
		}

		recordStep(RunStep{
			Step: result.Steps, Kind: "tool",
			Provider: completion.Provider, Model: completion.Model,
			ToolCalls: executed,
			Detail: map[string]any{
				"requested": len(completion.ToolCalls),
				"executed":  executed,
			},
		})

		if executed == 0 && result.ToolCalls >= policy.MaxToolCalls {
			recordRun(domain.RunCallLimit, fmt.Sprintf(
				"stopped at the %d-call limit", policy.MaxToolCalls))
			return result, nil
		}
	}
}
