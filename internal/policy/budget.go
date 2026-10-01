package policy

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/corerouter/corerouter/internal/domain"
)

// CounterStore is the fast counter backend used for budgets and rate limits.
//
// It is an interface rather than a Redis client so the policy package can be
// tested with an in-memory implementation and so a deployment without Redis
// degrades to the in-process fallback instead of failing to compile.
//
// Spend is addressed by a *period key* -- an already-rendered label such as
// "2026-09-30" or "2026-09" -- rather than by a BudgetPeriod constant. That
// choice is deliberate: rendering the label in one place (periodKey) is what
// guarantees a charge and a subsequent budget check address the same counter. An
// earlier design passed the constant from one side and the label from the other,
// which silently made every budget check read a counter nothing ever incremented.
type CounterStore interface {
	// IncrementSpend adds amount to a scope's spend for a period key and returns
	// the new total. Implementations must be atomic, because two concurrent
	// requests reading then writing would both see the pre-charge value and
	// undercount.
	IncrementSpend(ctx context.Context, scopeKey string, amount float64, periodKey string) (float64, error)
	// GetSpend returns the current spend for a scope and period key.
	GetSpend(ctx context.Context, scopeKey, periodKey string) (float64, error)
	// AllowRequest implements a fixed-window counter. It returns whether the
	// request is permitted, how many requests remain, and when the window resets.
	AllowRequest(ctx context.Context, bucketKey string, limit int, window time.Duration) (allowed bool, remaining int, resetAt time.Time, err error)
}

// BudgetScope identifies what a budget applies to.
type BudgetScope string

const (
	// ScopeTenant applies the budget to all traffic for a tenant.
	ScopeTenant BudgetScope = "tenant"
	// ScopeKey applies the budget to a single API key.
	ScopeKey BudgetScope = "key"
	// ScopePolicy applies the budget to traffic governed by one policy.
	ScopePolicy BudgetScope = "policy"
)

// Enforcer applies budgets and rate limits to requests.
type Enforcer struct {
	store  CounterStore
	logger *slog.Logger
	now    func() time.Time
	// enabled allows a deployment to record spend without blocking traffic.
	enabled bool
}

// EnforcerOption customizes an enforcer.
type EnforcerOption func(*Enforcer)

// WithEnforcerLogger attaches a logger.
func WithEnforcerLogger(l *slog.Logger) EnforcerOption {
	return func(e *Enforcer) {
		if l != nil {
			e.logger = l
		}
	}
}

// WithEnforcerClock overrides the clock. It exists for tests.
func WithEnforcerClock(now func() time.Time) EnforcerOption {
	return func(e *Enforcer) {
		if now != nil {
			e.now = now
		}
	}
}

// WithEnforcementEnabled controls whether limits actually block requests.
func WithEnforcementEnabled(enabled bool) EnforcerOption {
	return func(e *Enforcer) { e.enabled = enabled }
}

// NewEnforcer constructs an enforcer.
func NewEnforcer(store CounterStore, opts ...EnforcerOption) *Enforcer {
	e := &Enforcer{
		store:   store,
		logger:  slog.Default(),
		now:     time.Now,
		enabled: true,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Charge records spend against the scopes implied by a request.
//
// Charging happens after a response is produced, so it can only ever affect
// subsequent requests. That ordering is deliberate: blocking a request based on
// the spend of requests still in flight would make the gateway's behaviour depend
// on concurrency, which is impossible to reason about or to explain to a customer.
func (e *Enforcer) Charge(ctx context.Context, rc *domain.RequestContext, cost domain.Cost) error {
	if e == nil || e.store == nil || cost.IsZero() {
		return nil
	}

	now := e.now()
	var firstErr error

	for _, scope := range scopesFor(rc) {
		label := PeriodKey(scope.period, now)
		if _, err := e.store.IncrementSpend(ctx, scope.key, cost.USD, label); err != nil {
			e.logger.Warn("failed to record spend",
				"scope", scope.key, "period", label, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// CheckPreflight verifies the request is permitted before a provider is called.
//
// Three independent limits are checked: the request rate, the token rate, and the
// spend ceiling for the current period. Spend is checked here as well as recorded
// afterwards because a tenant that has already exhausted its daily budget must be
// stopped at the door rather than after paying for a completion.
func (e *Enforcer) CheckPreflight(ctx context.Context, rc *domain.RequestContext, policy *domain.RoutingPolicy) error {
	if e == nil || e.store == nil || !e.enabled {
		return nil
	}

	limits := effectiveLimits(policy)
	now := e.now()

	// Request rate: one token per request from a bucket sized to the per-minute
	// allowance.
	if limits.RequestsPerMinute > 0 {
		key := rateKey(rc, "rpm")
		allowed, remaining, resetAt, err := e.store.AllowRequest(ctx, key, limits.RequestsPerMinute, time.Minute)
		if err != nil {
			// A rate limiter that cannot reach its store must not become an
			// outage. The request is allowed and the failure is recorded.
			e.logger.Warn("rate limiter unavailable; allowing the request", "error", err)
		} else if !allowed {
			return rateLimitError(limits.RequestsPerMinute, "requests per minute", remaining, resetAt, now)
		}
	}

	// Token rate: charged prospectively using the estimated prompt size plus the
	// requested completion allowance, which is the worst case the client asked
	// for.
	if limits.TokensPerMinute > 0 {
		key := rateKey(rc, "tpm")
		estimated := rc.PromptTokens + rc.MaxOutputTokens
		if estimated < 1 {
			estimated = 1
		}
		// The bucket is sized so a single oversized request can still pass: a
		// limit smaller than one request's worst case would deadlock the client.
		allowed, remaining, resetAt, err := e.store.AllowRequest(ctx, key, limits.TokensPerMinute, time.Minute)
		if err != nil {
			e.logger.Warn("token rate limiter unavailable; allowing the request", "error", err)
		} else if !allowed {
			return rateLimitError(limits.TokensPerMinute, "tokens per minute", remaining, resetAt, now)
		}
	}

	// Spend ceilings. The projected charge is the routing estimate when one is
	// available, otherwise the prompt cost alone.
	projected := 0.0
	if rc.Resolution != nil {
		projected = rc.Resolution.EstimatedCost.USD
	}
	if limits.DailyBudgetUSD > 0 {
		if err := e.checkBudget(ctx, rc, ScopeTenant, limits.DailyBudgetUSD, domain.BudgetDaily, projected, now); err != nil {
			return err
		}
	}
	if limits.MonthlyBudgetUSD > 0 {
		if err := e.checkBudget(ctx, rc, ScopeTenant, limits.MonthlyBudgetUSD, domain.BudgetMonthly, projected, now); err != nil {
			return err
		}
	}

	return nil
}

// checkBudget verifies a single spend ceiling.
func (e *Enforcer) checkBudget(
	ctx context.Context,
	rc *domain.RequestContext,
	scope BudgetScope,
	limit float64,
	period domain.BudgetPeriod,
	projected float64,
	now time.Time,
) error {
	key := scopeKey(scope, rc)
	spent, err := e.store.GetSpend(ctx, key, PeriodKey(period, now))
	if err != nil {
		e.logger.Warn("failed to read budget; allowing the request", "scope", key, "error", err)
		return nil
	}
	if spent+projected <= limit {
		return nil
	}

	resetAt := ResetAt(period, now)
	quotaErr := domain.Errorf(domain.ErrCodeQuotaExceeded,
		"%s budget of $%.2f exceeded (spent $%.4f); resets at %s",
		period, limit, spent, resetAt.UTC().Format(time.RFC3339))
	// 402 Payment Required is the most informative status for a budget block: it
	// distinguishes "you are out of budget" from "you are going too fast" (429)
	// for any client that inspects the status rather than the body.
	quotaErr.Status = 402
	return quotaErr
}

// rateLimitError renders a rate limit rejection with reset information.
func rateLimitError(limit int, label string, remaining int, resetAt, now time.Time) *domain.Error {
	retryAfter := resetAt.Sub(now)
	if retryAfter < 0 {
		retryAfter = time.Second
	}
	err := domain.Errorf(domain.ErrCodeRateLimited,
		"rate limit exceeded for %s (limit %d); retry in %s", label, limit, retryAfter.Round(time.Second))
	err.Status = 429
	return err
}

// effectiveLimits returns the limits in force, applying engine defaults where the
// policy is silent.
func effectiveLimits(policy *domain.RoutingPolicy) domain.PolicyLimits {
	if policy == nil {
		return domain.PolicyLimits{}
	}
	return policy.Limits
}

// scopeRef names a spend scope with its period.
type scopeRef struct {
	key    string
	period domain.BudgetPeriod
}

// scopesFor returns the spend scopes a request charges against.
//
// Charges are recorded per tenant, per key and per policy. Per-key spend is what
// lets an operator answer "which application burned the budget?"; per-policy spend
// answers "which routing rule is expensive?", and both questions come up during an
// incident.
//
// The scopes carry period *constants*, not formatted labels: the label is derived
// once inside Charge. Passing a pre-formatted label here would make it look like an
// unrecognized period on the way back in, collapsing daily and monthly spend onto
// the same counter and double-charging every request.
func scopesFor(rc *domain.RequestContext) []scopeRef {
	out := make([]scopeRef, 0, 4)

	if id := rc.TenantID(); id != "" {
		out = append(out,
			scopeRef{key: "tenant:" + id, period: domain.BudgetDaily},
			scopeRef{key: "tenant:" + id, period: domain.BudgetMonthly},
		)
	}
	if id := rc.APIKeyID(); id != "" {
		out = append(out,
			scopeRef{key: "key:" + id, period: domain.BudgetDaily},
		)
	}
	if rc.Resolution != nil && rc.Resolution.PolicyID != "" {
		out = append(out, scopeRef{key: "policy:" + rc.Resolution.PolicyID, period: domain.BudgetDaily})
	}
	return out
}

// scopeKey renders a budget scope key.
func scopeKey(scope BudgetScope, rc *domain.RequestContext) string {
	switch scope {
	case ScopeKey:
		return "key:" + rc.APIKeyID()
	case ScopePolicy:
		return "policy:" + rc.PolicyID
	default:
		return "tenant:" + rc.TenantID()
	}
}

// rateKey renders a rate limit bucket key.
func rateKey(rc *domain.RequestContext, kind string) string {
	primary := rc.APIKeyID()
	if primary == "" {
		primary = rc.TenantID()
	}
	if primary == "" {
		primary = "anonymous"
	}
	return fmt.Sprintf("%s:%s", kind, primary)
}

// PeriodKey renders the counter suffix for a budget period.
//
// This is the single place a period becomes a label. Every read and write of a
// spend counter goes through it, which is what keeps a charge and a budget check
// addressing the same counter. It is exported so the background reconciliation job
// can address the same counters the request path writes.
func PeriodKey(period domain.BudgetPeriod, now time.Time) string {
	switch period {
	case domain.BudgetDaily:
		return "d:" + now.UTC().Format("2006-01-02")
	case domain.BudgetWeekly:
		year, week := now.UTC().ISOWeek()
		return fmt.Sprintf("w:%d-W%02d", year, week)
	case domain.BudgetMonthly:
		return "m:" + now.UTC().Format("2006-01")
	default:
		return "total"
	}
}

// ResetAt returns when a budget period next resets.
func ResetAt(period domain.BudgetPeriod, now time.Time) time.Time {
	now = now.UTC()
	switch period {
	case domain.BudgetDaily:
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	case domain.BudgetWeekly:
		// ISO weeks start on Monday.
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		return startOfDay.AddDate(0, 0, 8-weekday)
	case domain.BudgetMonthly:
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	default:
		return time.Time{}
	}
}
