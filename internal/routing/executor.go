package routing

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"time"

	"github.com/corerouter/corerouter/internal/domain"
	"github.com/corerouter/corerouter/internal/providers"
)

// StreamStartedError reports a failure that occurred after response bytes had
// already been written to the client.
//
// Once a stream has started the HTTP status is already committed, so the gateway
// cannot fail over or change the status code: the only honest options are to
// terminate the stream with an error event or to leave the client with a
// truncated response. The API layer uses this type to choose the former.
type StreamStartedError struct {
	// Err is the underlying normalized failure.
	Err error
	// Attempts counts provider calls made before the failure.
	Attempts int
}

// Error implements the error interface.
func (e *StreamStartedError) Error() string {
	return fmt.Sprintf("stream failed after it had started: %v", e.Err)
}

// Unwrap exposes the underlying error.
func (e *StreamStartedError) Unwrap() error { return e.Err }

// Executor carries a route decision through its retry and fallback chain.
//
// It is the only component that calls provider adapters, which means retry
// semantics, failover semantics, health bookkeeping and attempt tracing are each
// defined exactly once, for every provider.
type Executor struct {
	// adapters resolves a provider id or name to a live adapter.
	adapters *providers.Registry
	// health records the outcome of every attempt.
	health *HealthTracker
	// observer receives decision, attempt and outcome records.
	observer Observer
	logger   *slog.Logger

	// now and sleep are injectable so tests can exercise backoff and deadline
	// behaviour without waiting.
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
	// jitter sources its randomness from the request id, keeping backoff
	// deterministic under replay while still de-synchronizing a fleet.
	jitter bool
}

// ExecutorOption customizes an executor.
type ExecutorOption func(*Executor)

// WithObserver attaches an observer.
func WithObserver(o Observer) ExecutorOption {
	return func(e *Executor) {
		if o != nil {
			e.observer = o
		}
	}
}

// WithExecutorLogger attaches a logger.
func WithExecutorLogger(l *slog.Logger) ExecutorOption {
	return func(e *Executor) {
		if l != nil {
			e.logger = l
		}
	}
}

// WithClock overrides the clock and sleep functions. It exists for tests.
func WithClock(now func() time.Time, sleep func(context.Context, time.Duration) error) ExecutorOption {
	return func(e *Executor) {
		if now != nil {
			e.now = now
		}
		if sleep != nil {
			e.sleep = sleep
		}
	}
}

// WithJitter controls whether retry backoff is jittered.
func WithJitter(enabled bool) ExecutorOption {
	return func(e *Executor) { e.jitter = enabled }
}

// NewExecutor constructs an executor.
func NewExecutor(adapters *providers.Registry, health *HealthTracker, opts ...ExecutorOption) *Executor {
	e := &Executor{
		adapters: adapters,
		health:   health,
		observer: NopObserver{},
		logger:   slog.Default(),
		now:      time.Now,
		sleep:    sleepContext,
		jitter:   true,
	}
	for _, opt := range opts {
		opt(e)
	}
	if e.health == nil {
		e.health = NewHealthTracker(HealthConfig{})
	}
	return e
}

// sleepContext sleeps unless the context ends first.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ExecuteResult is the outcome of a request that was served.
type ExecuteResult struct {
	// Response is the normalized completion, present for both streaming and
	// non-streaming requests.
	Response *providers.Response
	// Decision is the route decision that was executed.
	Decision *domain.RouteDecision
	// Trace is the complete attempt history.
	Trace *domain.RequestTrace
	// Attempts counts provider calls made.
	Attempts int
	// ProviderLatencyMS is the latency of the attempt that succeeded.
	ProviderLatencyMS int64
	// FirstTokenMS is the time to first streamed token, when streaming.
	FirstTokenMS int64
	// FallbackUsed is true when the first chain target did not serve the request.
	FallbackUsed bool
}

// Execute runs the request against the decision's chain.
//
// The stream handler is optional. When it is nil the request is performed as a
// single non-streaming call, which is what a client asking for a buffered
// response needs. When it is non-nil each delta is offered to it as it arrives.
func (e *Executor) Execute(
	ctx context.Context,
	rc *domain.RequestContext,
	req *providers.Request,
	handler providers.StreamHandler,
) (*ExecuteResult, error) {
	decision := rc.Resolution
	if decision == nil {
		return nil, domain.NewError(domain.ErrCodeInternal, "execution was called without a route decision")
	}

	started := e.now()
	trace := &domain.RequestTrace{
		ID:             domain.NewID(),
		RequestID:      rc.RequestID,
		TraceID:        rc.TraceID,
		TenantID:       rc.TenantID(),
		APIKeyID:       rc.APIKeyID(),
		Decision:       decision,
		StartedAt:      started,
		ClientStreamed: handler != nil,
		CreatedAt:      started,
	}

	if e.observer != nil {
		e.observer.RecordDecision(ctx, rc, decision)
	}

	attempts := decision.Chain.Attempts()
	if len(attempts) == 0 {
		err := domain.NewError(domain.ErrCodeUnavailable, "the routing decision produced an empty fallback chain")
		return e.finish(ctx, rc, trace, started, 0, false, nil, err)
	}

	var (
		lastErr       error
		totalAttempts int
		// firstTargetKey identifies the policy's primary target so a later
		// attempt can be reported as a fallback.
		firstTargetKey = attempts[0].ProviderID + "\x00" + attempts[0].Model
	)

	for targetIndex, target := range attempts {
		// Budget-aware fallback: never spend more on a fallback than the request's
		// own ceiling allows. Without this check a failover could quietly violate
		// the cost limit the request was accepted under.
		if targetIndex > 0 && decision.CostCeilingUSD > 0 {
			projected := target.EstimatedCost(rc.PromptTokens, rc.MaxOutputTokens).USD
			if projected > decision.CostCeilingUSD {
				trace.Spans = append(trace.Spans, domain.TraceSpan{
					Kind:    domain.SpanFallback,
					Name:    "skip:" + target.Ref().String(),
					StartMS: e.now().Sub(started).Milliseconds(),
					Attributes: map[string]string{
						"reason":        "projected cost exceeds ceiling",
						"projected_usd": formatFloat(projected),
						"ceiling_usd":   formatFloat(decision.CostCeilingUSD),
					},
				})
				continue
			}
		}

		adapter, ok := e.resolve(target)
		if !ok {
			lastErr = domain.NewError(domain.ErrCodeUnavailable,
				fmt.Sprintf("provider %s is not configured", target.Ref().String())).
				WithProvider(target.ProviderName, target.Model, totalAttempts+1)
			e.recordFailure(target, lastErr, 0)
			continue
		}

		for retryAttempt := 1; retryAttempt <= maxRetries(decision.Retry); retryAttempt++ {
			// Check the wall-clock budget before opening a connection the request
			// can no longer afford to wait for.
			remaining := rc.Remaining(e.now())
			if !rc.Deadline.IsZero() && remaining <= 0 {
				lastErr = domain.NewError(domain.ErrCodeTimeout,
					"the request exceeded its total time budget before a provider could answer").
					WithProvider(target.ProviderName, target.Model, totalAttempts+1)
				return e.finish(ctx, rc, trace, started, totalAttempts, totalAttempts > 0, nil, lastErr)
			}

			attemptCtx, cancel := e.attemptContext(ctx, rc, remaining)
			attemptNumber := totalAttempts + 1

			attemptStart := e.now()
			attempt := domain.TraceAttempt{
				Number:          attemptNumber,
				Target:          target,
				StartedOffsetMS: attemptStart.Sub(started).Milliseconds(),
			}

			// streamState tracks whether any chunk reached the client, which
			// decides whether failover is still possible.
			state := &streamState{}
			wrapped := wrapHandler(handler, state)

			callReq := *req
			callReq.Model = target.Model
			callReq.Ref = target.Ref()
			callReq.MaxTokens = effectiveMaxTokens(target, rc)

			var (
				resp *providers.Response
				err  error
			)
			if handler != nil {
				resp, err = adapter.ChatCompletionStream(attemptCtx, &callReq, wrapped)
			} else {
				resp, err = adapter.ChatCompletion(attemptCtx, &callReq)
			}
			cancel()

			duration := e.now().Sub(attemptStart)
			attempt.DurationMS = duration.Milliseconds()
			attempt.FirstTokenMS = state.firstTokenMS

			totalAttempts++
			if e.observer != nil {
				e.observer.RecordAttempt(ctx, rc, attempt)
			}

			if err == nil {
				attempt.Usage = resp.Usage
				trace.Attempts = append(trace.Attempts, attempt)
				e.recordSuccess(target, duration)

				return e.finish(ctx, rc, trace, started, totalAttempts, firstTargetKey != target.ProviderID+"\x00"+target.Model, resp, nil)
			}

			// A failure after the client has seen data cannot be retried: the
			// bytes are already delivered.
			if state.started {
				attempt.Error = err.Error()
				attempt.ErrorCode = domain.AsError(err).Code
				trace.Attempts = append(trace.Attempts, attempt)
				e.recordFailure(target, err, duration)
				startedErr := &StreamStartedError{Err: err, Attempts: totalAttempts}
				_, _ = e.finish(ctx, rc, trace, started, totalAttempts, true, nil, startedErr)
				return nil, startedErr
			}

			normalized := domain.AsError(err)
			attempt.Status = normalized.Status
			attempt.ErrorCode = normalized.Code
			attempt.Error = normalized.Message
			trace.Attempts = append(trace.Attempts, attempt)
			e.recordFailure(target, err, duration)
			lastErr = err

			// Retry within the same provider, if the policy allows it.
			if retryAttempt < maxRetries(decision.Retry) && decision.Retry.Allows(retryAttempt, err) {
				backoff := e.backoffFor(rc, decision.Retry, err, retryAttempt)
				if backoff > 0 {
					attempt.RetryTriggered = true
					attempt.BackoffMS = backoff.Milliseconds()
					// Reflect the revised attempt record in the trace.
					trace.Attempts[len(trace.Attempts)-1] = attempt

					if remaining > 0 && backoff > remaining {
						// Waiting would exceed the budget; give up now rather than
						// sleeping past the deadline.
						lastErr = domain.NewError(domain.ErrCodeTimeout,
							"the retry backoff would exceed the request time budget").
							WithProvider(target.ProviderName, target.Model, attemptNumber)
						return e.finish(ctx, rc, trace, started, totalAttempts, totalAttempts > 0, nil, lastErr)
					}
					if err := e.sleep(attemptCtx2(ctx, rc), backoff); err != nil {
						return e.finish(ctx, rc, trace, started, totalAttempts, totalAttempts > 0, nil,
							domain.NewError(domain.ErrCodeCanceled, "the client canceled the request while backing off").Wrap(err))
					}
				}
				continue
			}
			break
		}

		// Retries for this provider are exhausted. Decide whether to fail over.
		lastNormalized := domain.AsError(lastErr)
		if !decision.Fallback.Allows(lastErr) {
			return e.finish(ctx, rc, trace, started, totalAttempts, totalAttempts > 0, nil, lastErr)
		}
		if targetIndex == len(attempts)-1 {
			// Last target in the chain.
			break
		}

		if delay := decision.Fallback.BackoffBeforeFailover; delay > 0 {
			trace.Spans = append(trace.Spans, domain.TraceSpan{
				Kind:       domain.SpanFallback,
				Name:       "failover:" + target.Ref().String(),
				StartMS:    e.now().Sub(started).Milliseconds(),
				DurationMS: delay.Milliseconds(),
				Attributes: map[string]string{
					"error_code": string(lastNormalized.Code),
					"backoff_ms": fmt.Sprintf("%d", delay.Milliseconds()),
				},
			})
			if err := e.sleep(attemptCtx2(ctx, rc), delay); err != nil {
				return e.finish(ctx, rc, trace, started, totalAttempts, true, nil,
					domain.NewError(domain.ErrCodeCanceled, "the client canceled the request during failover").Wrap(err))
			}
		}

		if len(trace.Attempts) > 0 {
			trace.Attempts[len(trace.Attempts)-1].FallbackTriggered = true
		}
	}

	if lastErr == nil {
		lastErr = domain.NewError(domain.ErrCodeUnavailable, "no provider could serve the request")
	}
	finalErr := domain.NewError(
		domain.AsError(lastErr).Code,
		fmt.Sprintf("all %d provider attempt(s) failed; last error: %s", totalAttempts, domain.AsError(lastErr).Message),
	).WithProvider(domain.AsError(lastErr).Provider, domain.AsError(lastErr).Model, totalAttempts)
	finalErr.Cause = lastErr

	return e.finish(ctx, rc, trace, started, totalAttempts, true, nil, finalErr)
}

// attemptContext bounds an attempt by both the request deadline and the
// per-attempt budget.
func (e *Executor) attemptContext(ctx context.Context, rc *domain.RequestContext, remaining time.Duration) (context.Context, context.CancelFunc) {
	perAttempt := rc.Resolution.Timeout.PerAttempt
	switch {
	case perAttempt <= 0 && rc.Deadline.IsZero():
		return context.WithCancel(ctx)
	case perAttempt <= 0:
		return context.WithDeadline(ctx, rc.Deadline)
	case rc.Deadline.IsZero():
		return context.WithTimeout(ctx, perAttempt)
	}

	// Both bounds exist: take the earlier one.
	deadline := e.now().Add(perAttempt)
	if rc.Deadline.Before(deadline) {
		deadline = rc.Deadline
	}
	_ = remaining
	return context.WithDeadline(ctx, deadline)
}

// attemptCtx2 returns a context bounded by the request deadline for backoff
// sleeps, so a sleep never outlives the request.
func attemptCtx2(ctx context.Context, rc *domain.RequestContext) context.Context {
	if rc.Deadline.IsZero() {
		return ctx
	}
	// A cancellable child is not created here because the parent already carries
	// the deadline; the sleep helper selects on ctx.Done() as well.
	return ctx
}

// backoffFor computes the delay before the next attempt, honouring a provider's
// Retry-After hint when the policy asks for it.
func (e *Executor) backoffFor(rc *domain.RequestContext, policy domain.RetryPolicy, err error, attempt int) time.Duration {
	delay := policy.BackoffFor(attempt)

	if policy.HonorRetryAfter {
		if hint, ok := providers.RetryAfter(err); ok && hint > delay {
			delay = hint
			if policy.MaxBackoff > 0 && delay > policy.MaxBackoff {
				delay = policy.MaxBackoff
			}
		}
	}

	if e.jitter && delay > 0 {
		delay = jitter(delay, rc.RequestID.String(), attempt)
	}
	if delay < 0 {
		delay = 0
	}
	return delay
}

// jitter scales a delay by a deterministic factor in [0.8, 1.2).
//
// Randomizing across replicas prevents a synchronized retry storm after a
// provider blip, while deriving the factor from the request id keeps replay and
// tests reproducible.
func jitter(delay time.Duration, seed string, attempt int) time.Duration {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%s\x00%d", seed, attempt)
	fraction := float64(h.Sum64()%1000) / 1000.0 // [0,1)
	factor := 0.8 + 0.4*fraction                 // [0.8,1.2)
	scaled := time.Duration(float64(delay) * factor)
	if scaled < 0 {
		return delay
	}
	return scaled
}

// effectiveMaxTokens returns the completion allowance to send upstream.
//
// A ceiling only takes effect when the client asked for tokens. When it said
// nothing, zero is returned and the provider applies its own default, because
// injecting a router-chosen limit is what made long answers stop mid-sentence:
// the value looked like the client's request, so nothing reported the answer as
// truncated. A policy ceiling is an upper bound on what a caller may request, not
// a target length the gateway picks on the caller's behalf.
//
// Callers that need a bound for costing or context fitting read
// rc.MaxOutputTokens, which always carries the ceiling.
func effectiveMaxTokens(target domain.RouteTarget, rc *domain.RequestContext) int {
	if rc == nil || rc.RequestedOutputTokens <= 0 {
		return 0
	}
	// rc.MaxOutputTokens is the client's request after the policy ceiling was
	// applied, so it is the clamped value rather than the raw one.
	maxTokens := rc.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = rc.RequestedOutputTokens
	}
	if target.MaxOutputTokens > 0 && (maxTokens <= 0 || target.MaxOutputTokens < maxTokens) {
		maxTokens = target.MaxOutputTokens
	}
	return maxTokens
}

// maxRetries returns the number of attempts allowed against one provider.
func maxRetries(policy domain.RetryPolicy) int {
	if policy.MaxAttempts < 1 {
		return 1
	}
	return policy.MaxAttempts
}

// resolve finds the adapter for a target.
func (e *Executor) resolve(target domain.RouteTarget) (providers.Adapter, bool) {
	if e.adapters == nil {
		return nil, false
	}
	if target.ProviderID != "" {
		if a, ok := e.adapters.ByID(target.ProviderID); ok {
			return a, true
		}
	}
	if target.ProviderName != "" {
		return e.adapters.ByName(target.ProviderName)
	}
	return nil, false
}

// recordSuccess feeds a successful attempt into the health tracker.
func (e *Executor) recordSuccess(target domain.RouteTarget, latency time.Duration) {
	if e.health == nil {
		return
	}
	e.health.RecordSuccess(target.ProviderID, target.ProviderName, latency)
}

// recordFailure feeds a failed attempt into the health tracker.
func (e *Executor) recordFailure(target domain.RouteTarget, err error, latency time.Duration) {
	if e.health == nil {
		return
	}
	code := domain.AsError(err).Code
	e.health.RecordFailure(target.ProviderID, target.ProviderName, code, latency)
}

// streamState records streaming progress so the executor can tell a failure that
// can still fail over from one that cannot.
type streamState struct {
	started      bool
	firstTokenMS int64
}

// wrapHandler wraps the caller's handler to record progress. It returns nil when
// the caller did not ask for streaming, so the adapter takes its non-streaming
// path.
func wrapHandler(handler providers.StreamHandler, state *streamState) providers.StreamHandler {
	if handler == nil {
		return nil
	}
	start := time.Now()
	return func(chunk providers.Chunk) error {
		if !state.started {
			state.started = true
			state.firstTokenMS = time.Since(start).Milliseconds()
		}
		return handler(chunk)
	}
}

// finish records the outcome and renders the result.
func (e *Executor) finish(
	ctx context.Context,
	rc *domain.RequestContext,
	trace *domain.RequestTrace,
	started time.Time,
	attempts int,
	fallbackUsed bool,
	resp *providers.Response,
	err error,
) (*ExecuteResult, error) {
	now := e.now()
	trace.TotalMS = now.Sub(started).Milliseconds()
	if trace.TotalMS < 0 {
		// A monotonic clock regression should not produce negative telemetry.
		trace.TotalMS = 0
	}

	if err != nil {
		normalized := domain.AsError(err)
		trace.Outcome = outcomeForError(normalized.Code)
		trace.ErrorCode = normalized.Code
	} else {
		if fallbackUsed {
			trace.Outcome = domain.OutcomeFallback
		} else {
			trace.Outcome = domain.OutcomeSuccess
		}
	}

	if e.observer != nil {
		e.observer.RecordOutcome(ctx, rc, trace)
	}

	if err != nil {
		return &ExecuteResult{
			Decision:     rc.Resolution,
			Trace:        trace,
			Attempts:     attempts,
			FallbackUsed: fallbackUsed,
		}, err
	}

	var providerMillis int64
	if len(trace.Attempts) > 0 {
		providerMillis = trace.Attempts[len(trace.Attempts)-1].DurationMS
	}
	var firstToken int64
	for _, a := range trace.Attempts {
		if a.FirstTokenMS > 0 {
			firstToken = a.FirstTokenMS
			break
		}
	}

	return &ExecuteResult{
		Response:          resp,
		Decision:          rc.Resolution,
		Trace:             trace,
		Attempts:          attempts,
		ProviderLatencyMS: providerMillis,
		FirstTokenMS:      firstToken,
		FallbackUsed:      fallbackUsed,
	}, nil
}

// outcomeForError classifies a terminal error for aggregation.
func outcomeForError(code domain.ErrorCode) domain.UsageOutcome {
	switch code {
	case domain.ErrCodeCanceled:
		return domain.OutcomeCanceled
	case domain.ErrCodeInvalidRequest, domain.ErrCodeAuthentication,
		domain.ErrCodePermission, domain.ErrCodeQuotaExceeded,
		domain.ErrCodeContextLength, domain.ErrCodeNotFound,
		domain.ErrCodeContentFiltered:
		return domain.OutcomeRejected
	default:
		return domain.OutcomeError
	}
}

// IsStreamStarted reports whether err indicates a stream that had already begun.
func IsStreamStarted(err error) bool {
	var target *StreamStartedError
	return errors.As(err, &target)
}
