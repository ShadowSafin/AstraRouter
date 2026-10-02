package main

import (
	"context"

	"github.com/shadowsafin/synapass/internal/domain"
	"github.com/shadowsafin/synapass/internal/storage"
	"github.com/shadowsafin/synapass/internal/tools"
)

// agentRunSink adapts the storage repositories to the tool package's RunSink.
//
// The adaptation lives here rather than in storage so the tool package keeps no
// dependency on persistence: internal/tools defines the small interface it
// needs, and this file satisfies it. The alternative, making the repository
// implement RunRecord directly, would drag the tool package's vocabulary into
// the storage layer for no benefit.
type agentRunSink struct {
	runs *storage.AgentRunRepository
}

// UpsertRun persists a run header.
func (s *agentRunSink) UpsertRun(ctx context.Context, run tools.RunRecord) error {
	if s == nil || s.runs == nil {
		return nil
	}
	return s.runs.Upsert(ctx, &storage.AgentRun{
		ID:         run.ID,
		RequestID:  run.RequestID,
		TenantID:   run.TenantID,
		Status:     run.Status,
		Steps:      run.Steps,
		ToolCalls:  run.ToolCalls,
		Provider:   run.Provider,
		Model:      run.Model,
		LatencyMS:  run.LatencyMS,
		StopReason: run.StopReason,
	})
}

// InsertRunStep persists one step of a run.
func (s *agentRunSink) InsertRunStep(ctx context.Context, runID string, step tools.RunStep) error {
	if s == nil || s.runs == nil {
		return nil
	}
	return s.runs.InsertStep(ctx, runID, &storage.AgentStep{
		RunID:     runID,
		Step:      step.Step,
		Kind:      step.Kind,
		Provider:  step.Provider,
		Model:     step.Model,
		ToolCalls: step.ToolCalls,
		LatencyMS: step.LatencyMS,
		Tokens:    step.Tokens,
		Detail:    step.Detail,
	})
}

// toolInvocationSink adapts the storage repository to the tool package's
// InvocationSink.
//
// The repository methods take pointers and return the storage error type, while
// the tool package's interface is value-oriented, so this shim is where the two
// vocabularies meet. It also drops errors deliberately: a tool result has
// already been produced for the client by the time the row is written, and
// failing the request because an audit insert failed would leave the client and
// the audit trail disagreeing.
type toolInvocationSink struct {
	repo *storage.ToolInvocationRepository
}

// InsertInvocation persists one tool call.
func (s *toolInvocationSink) InsertInvocation(
	ctx context.Context, invocation *domain.ToolInvocation,
) error {
	if s == nil || s.repo == nil || invocation == nil {
		return nil
	}
	return s.repo.Insert(ctx, invocation)
}

// InsertExecution persists a tool result payload.
func (s *toolInvocationSink) InsertExecution(
	ctx context.Context, execution *domain.ToolExecution,
) error {
	if s == nil || s.repo == nil || execution == nil {
		return nil
	}
	return s.repo.InsertExecution(ctx, execution)
}
