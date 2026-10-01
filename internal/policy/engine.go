// Package policy engine extension: Phase 2 fine-grained evaluation.
//
// The Resolver (resolver.go) answers "which policy governs this request?".
// The Engine in this file answers "is this request allowed under that policy,
// and what constraints flow downstream?". Splitting selection from evaluation
// keeps each testable in isolation and makes the decision object the single
// contract the router consumes.
package policy

import (
	"context"
	"strings"
	"time"

	"github.com/corerouter/corerouter/internal/domain"
)

// EvaluateInput bundles everything the policy engine needs.
type EvaluateInput struct {
	Policy   *domain.RoutingPolicy
	Request  *domain.RequestContext
	Task     domain.TaskClassification
	Endpoint *domain.Endpoint
}

// Engine evaluates fine-grained rules.
type Engine struct{}

// NewEngine constructs the policy decision engine (stateless).
func NewEngine() *Engine { return &Engine{} }

// Evaluate produces a PolicyDecision for one request.
//
// Evaluation order is fixed so deny reasons are deterministic:
//
//  1. policy presence / enabled
//  2. tenant / key / endpoint scope
//  3. batch vs interactive mode
//  4. request size / token limits
//  5. model and provider allow/deny lists
//  6. region constraints
//  7. data sensitivity
//  8. cost and latency ceilings
//
// The first failing check denies the request; warnings accumulate otherwise.
func (e *Engine) Evaluate(_ context.Context, in EvaluateInput) *domain.PolicyDecision {
	rc := in.Request
	now := time.Now().UTC()
	dec := &domain.PolicyDecision{
		Allowed:   true,
		DecidedAt: now,
		Task:      in.Task,
		UseCache:  true,
	}
	if rc != nil {
		dec.RequestID = rc.RequestID
		dec.EndpointID = rc.EndpointID
	}
	if in.Policy != nil {
		dec.PolicyID = in.Policy.ID
		dec.PolicyName = in.Policy.Name
		dec.PolicyVersion = in.Policy.Version
		dec.EffectiveLimits = in.Policy.Limits
		// Cache defaults from policy.
		if in.Policy.Limits.CacheEnabled != nil {
			dec.UseCache = *in.Policy.Limits.CacheEnabled
		}
		if in.Endpoint != nil && in.Endpoint.RoutingOverride != nil && in.Endpoint.RoutingOverride.UseCache != nil {
			dec.UseCache = *in.Endpoint.RoutingOverride.UseCache
		}
	}
	if rc != nil && rc.CacheBypass {
		dec.UseCache = false
		dec.MatchedRules = append(dec.MatchedRules, "cache_bypass_requested")
	}

	// No policy means the synthesized default allows everything.
	if in.Policy == nil {
		dec.MatchedRules = append(dec.MatchedRules, "synthesized_default_allow")
		dec.Shaping = defaultShaping(in.Task)
		return dec
	}
	p := in.Policy
	if !p.Enabled {
		return deny(dec, "policy_disabled", "the matched routing policy is disabled")
	}

	// Scope checks are informational here because matching already enforced
	// them; they are re-checked to produce explicit deny reasons when a pinned
	// policy is used outside its scope.
	if len(p.Match.TenantIDs) > 0 && rc != nil {
		if rc.Tenant == nil || !containsStr(p.Match.TenantIDs, rc.Tenant.ID) {
			return deny(dec, "tenant_mismatch", "this policy does not govern the request tenant")
		}
		dec.MatchedRules = append(dec.MatchedRules, "tenant_scoped")
	}
	if len(p.Match.APIKeyIDs) > 0 && rc != nil {
		if rc.APIKey == nil || !containsStr(p.Match.APIKeyIDs, rc.APIKey.ID) {
			return deny(dec, "key_mismatch", "this policy does not govern the calling API key")
		}
		dec.MatchedRules = append(dec.MatchedRules, "key_scoped")
	}
	if len(p.Match.EndpointIDs) > 0 && rc != nil {
		if rc.EndpointID == "" || !containsStr(p.Match.EndpointIDs, rc.EndpointID) {
			return deny(dec, "endpoint_mismatch", "this policy does not govern the calling endpoint")
		}
		dec.MatchedRules = append(dec.MatchedRules, "endpoint_scoped")
	}

	// Batch vs interactive.
	if p.Match.Batch != nil && rc != nil {
		if *p.Match.Batch != rc.Batch {
			mode := "interactive"
			if rc.Batch {
				mode = "batch"
			}
			return deny(dec, "mode_mismatch", "policy requires "+boolToMode(*p.Match.Batch)+" traffic, got "+mode)
		}
	}
	if p.Limits.BatchOnly && rc != nil && !rc.Batch {
		return deny(dec, "batch_only", "this policy serves batch traffic only")
	}
	if p.Limits.InteractiveOnly && rc != nil && rc.Batch {
		return deny(dec, "interactive_only", "this policy serves interactive traffic only")
	}

	// Request size / token limits.
	if rc != nil {
		if p.Limits.MaxPromptTokens > 0 && rc.PromptTokens > p.Limits.MaxPromptTokens {
			return deny(dec, "prompt_too_large", "prompt exceeds the policy token limit")
		}
		if p.Limits.MaxRequestBytes > 0 && rc.RequestBytes > p.Limits.MaxRequestBytes {
			return deny(dec, "request_too_large", "request body exceeds the policy size limit")
		}
		if p.Match.MaxPromptTokens > 0 && rc.PromptTokens > p.Match.MaxPromptTokens {
			return deny(dec, "prompt_too_large", "prompt exceeds the matched policy window")
		}
	}

	// Model allow/deny.
	if rc != nil && rc.RequestedModel != "" {
		if len(p.Limits.DeniedModels) > 0 && domain.MatchAnyModelPattern(p.Limits.DeniedModels, rc.RequestedModel) {
			dec.RejectedTargets = append(dec.RejectedTargets, domain.RejectedTarget{Model: rc.RequestedModel, Reason: "model denied by policy", Rule: "denied_models"})
			return deny(dec, "model_denied", "model "+quote(rc.RequestedModel)+" is denied by policy")
		}
		if len(p.Limits.AllowedModels) > 0 && !domain.MatchAnyModelPattern(p.Limits.AllowedModels, rc.RequestedModel) {
			return deny(dec, "model_not_allowed", "model "+quote(rc.RequestedModel)+" is not in the policy allow list")
		}
	}

	// Region constraints.
	if rc != nil && rc.Region != "" {
		if len(p.Limits.AllowedRegions) > 0 && !containsStrFold(p.Limits.AllowedRegions, rc.Region) {
			return deny(dec, "region_not_allowed", "region "+quote(rc.Region)+" is not permitted by policy")
		}
		if len(p.Limits.DeniedRegions) > 0 && containsStrFold(p.Limits.DeniedRegions, rc.Region) {
			return deny(dec, "region_denied", "region "+quote(rc.Region)+" is denied by policy")
		}
	}

	// Data sensitivity: sensitive payloads must not use cache unless allowed,
	// and denied labels block routing to third-party targets (enforced in the
	// router via RejectedTargets; here we record the constraint).
	if rc != nil && len(rc.DataSensitivity) > 0 {
		for _, label := range rc.DataSensitivity {
			for _, denied := range p.Limits.DeniedSensitive {
				if strings.EqualFold(label, denied) {
					dec.Warnings = append(dec.Warnings, "sensitive_label:"+label+" constrained by policy")
					// Force cache bypass for sensitive payloads by default.
					if p.Limits.BypassCacheForSensitive || p.Limits.RequireCacheBypassSensitive {
						dec.UseCache = false
						dec.MatchedRules = append(dec.MatchedRules, "cache_bypass_sensitive")
					}
				}
			}
		}
		// Any sensitive label bypasses the cache unless the policy opts in.
		if p.Limits.RequireCacheBypassSensitive {
			dec.UseCache = false
		} else if p.Limits.BypassCacheForSensitive && len(rc.DataSensitivity) > 0 {
			hasSensitive := false
			for _, l := range rc.DataSensitivity {
				if !strings.EqualFold(l, "public") && l != "" {
					hasSensitive = true
				}
			}
			if hasSensitive {
				dec.UseCache = false
				dec.MatchedRules = append(dec.MatchedRules, "cache_bypass_sensitive")
			}
		}
	}

	// Cost ceiling is enforced downstream, but an explicit zero-allowance deny
	// (hard budget cap of 0) is reported here for a clear message.
	if p.Limits.MaxCostPerRequestUSD < 0 {
		return deny(dec, "budget_exhausted", "policy cost ceiling is exhausted")
	}

	// Warnings: near-limit signals that do not block.
	if rc != nil {
		if p.Limits.MaxCostPerRequestUSD > 0 && rc.CostCeilingUSD > 0 && rc.CostCeilingUSD < p.Limits.MaxCostPerRequestUSD {
			dec.Warnings = append(dec.Warnings, "client cost ceiling tighter than policy")
		}
		if p.Limits.MaxLatencyMS > 0 && rc.LatencyTargetMS > p.Limits.MaxLatencyMS {
			dec.Warnings = append(dec.Warnings, "latency target exceeds policy maximum; will be clamped")
		}
	}

	dec.Shaping = defaultShaping(in.Task)
	dec.MatchedRules = append(dec.MatchedRules, "policy_evaluated")
	return dec
}

func deny(dec *domain.PolicyDecision, reason, msg string) *domain.PolicyDecision {
	dec.Allowed = false
	dec.DenyReason = reason
	dec.DenyMessage = msg
	dec.UseCache = false
	return dec
}

func defaultShaping(task domain.TaskClassification) domain.PromptShapePlan {
	plan := domain.PromptShapePlan{NormalizeSystem: true, ProviderAdapt: true}
	switch task.Task {
	case domain.TaskLongContext:
		plan.TrimContext = true
		plan.SummarizeHistory = true
		plan.MaxHistoryMessages = 20
	case domain.TaskToolUse:
		plan.ToolPrompting = true
	case domain.TaskStructuredOutput:
		plan.FormatStructured = true
	case domain.TaskCoding, domain.TaskReasoning:
		plan.Compress = false
		plan.TrimContext = true
	}
	return plan
}

func containsStr(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func containsStrFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

func boolToMode(b bool) string {
	if b {
		return "batch"
	}
	return "interactive"
}

func quote(s string) string { return "\"" + s + "\"" }
