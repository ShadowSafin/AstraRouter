package api

import (
	"context"
	"time"

	"github.com/corerouter/corerouter/internal/cache"
	"github.com/corerouter/corerouter/internal/domain"
)

// cacheLookup is the Phase 5 request-flow cache check.
//
// It evaluates the policy-aware decision first (tenant/endpoint policy,
// streaming, sensitivity, tools, live-data, determinism), then performs the
// full-key lookup (tenant, key, model, content, tools, settings, contract,
// policy, endpoint, sensitivity). Endpoint scopes are namespaced in the key
// rather than bypassed, so scoped traffic caches safely per scope.
func (s *Server) cacheLookup(
	ctx context.Context,
	rc *domain.RequestContext,
	body *domain.ChatCompletionRequest,
	policy *domain.RoutingPolicy,
	toolCtx *toolContext,
) (*domain.CacheLookupResult, *domain.CacheDecision) {
	if s.cache == nil || !s.cache.Enabled() || rc.Stream {
		var reason string
		if rc.Stream {
			reason = domain.CacheBypassStreaming
		}
		dec := &domain.CacheDecision{Cacheable: false, BypassReason: reason}
		rc.CacheDecision = dec
		if s.metrics != nil && reason != "" {
			s.metrics.ObserveCacheBypass(rc.TenantID(), reason)
		}
		return &domain.CacheLookupResult{Hit: false, BypassReason: reason}, dec
	}

	sensitive, sensitivity := cache.SensitivityOf(rc.DataSensitivity)
	policyUseCache := true
	policyID := ""
	policyVersion := 0
	if rc.PolicyDecision != nil {
		policyUseCache = rc.PolicyDecision.UseCache
		policyID = rc.PolicyDecision.PolicyID
		policyVersion = rc.PolicyDecision.PolicyVersion
	} else if policy != nil {
		policyID = policy.ID
		policyVersion = policy.Version
	}
	var endpointCache *bool
	if rc.EndpointOverride != nil {
		endpointCache = rc.EndpointOverride.UseCache
	}

	toolsSafe := cache.ToolsSafeForCache(body.Tools, safeRegistryNames(toolCtx))
	hasImages := hasImageParts(body)

	n := rc.N
	if body.N != nil {
		n = *body.N
	}

	dec := s.cache.Evaluate(domain.CacheEvalInput{
		TenantID: rc.TenantID(), APIKeyID: rc.APIKeyID(),
		EndpointID: rc.EndpointID, Model: body.Model, Stream: rc.Stream,
		CacheBypass: rc.CacheBypass, Sensitive: sensitive, Sensitivity: sensitivity,
		HasTools: len(body.Tools) > 0, ToolsSafe: toolsSafe, HasImages: hasImages,
		N: n, Temperature: body.Temperature, Seed: body.Seed,
		PolicyUseCache: policyUseCache, PolicyID: policyID,
		EndpointCache: endpointCache, PromptText: body.PromptText(),
	})
	// Overlay the most specific durable cache rule, if any. Global config
	// stays the default; a matching row tightens or relaxes it per scope.
	var matched *domain.CachePolicy
	if s.repos != nil && s.repos.CachePolicies != nil {
		if m, err := s.repos.CachePolicies.MatchForRequest(ctx, rc.TenantID(), rc.EndpointID, rc.APIKeyID(), "", body.Model); err == nil {
			matched = m
		}
	}
	if matched != nil {
		applyCachePolicy(&dec, matched)
	}
	rc.CacheDecision = &dec
	if !dec.Cacheable {
		if s.metrics != nil {
			s.metrics.ObserveCacheBypass(rc.TenantID(), dec.BypassReason)
		}
		return &domain.CacheLookupResult{Hit: false, BypassReason: dec.BypassReason}, &dec
	}

	start := time.Now()
	lookup := s.cache.LookupFull(ctx, rc.TenantID(), rc.APIKeyID(), body.Model,
		body, policyID, policyVersion, rc.EndpointID, sensitivity, body.User)
	// Enforce per-scope tier/threshold overrides post-lookup: the shared
	// lookup uses global tiers, so a rule that disables one turns the hit
	// into a miss rather than serving it.
	if lookup.Hit && matched != nil && !tierAllowed(lookup.Kind, matched, dec) {
		lookup = domain.CacheLookupResult{Hit: false, BypassReason: domain.CacheBypassPolicyDisabled}
	}
	if lookup.Hit && lookup.Kind == domain.CacheSemantic && matched != nil && matched.Threshold > 0 && lookup.Similarity < matched.Threshold {
		lookup = domain.CacheLookupResult{Hit: false, BypassReason: domain.CacheBypassPolicyDisabled}
	}
	rc.CacheLookupMS = lookup.LookupMS
	if s.metrics != nil {
		if lookup.Hit {
			s.metrics.ObserveCacheKind(rc.TenantID(), string(lookup.Kind), true)
			s.metrics.ObserveCacheLookup(rc.TenantID(), "hit", lookup.LookupMS/1000)
			s.metrics.ObserveCacheHitDetail(rc.TenantID(), string(lookup.Kind),
				float64(lookup.LatencySavedMS)/1000, lookup.Similarity)
		} else {
			s.metrics.ObserveCacheKind(rc.TenantID(), "", false)
			s.metrics.ObserveCacheLookup(rc.TenantID(), "miss", lookup.LookupMS/1000)
		}
	}
	_ = start
	lookup.Trace = &domain.CacheTrace{
		Hit: lookup.Hit, Kind: string(lookup.Kind), Key: lookup.Key,
		LookupMS: lookup.LookupMS, Similarity: lookup.Similarity,
		BypassReason: lookup.BypassReason, ReuseCount: lookup.ReuseCount,
		LatencySavedMS: lookup.LatencySavedMS,
	}
	return &lookup, &dec
}

// cacheStore stores a completed non-streaming response when the request's
// cache decision allows it. Serving metadata (provider, model, policy,
// endpoint, tools hash, usage, latency) travels in the envelope so future
// hits can validate that the world has not changed.
func (s *Server) cacheStore(
	ctx context.Context,
	rc *domain.RequestContext,
	body *domain.ChatCompletionRequest,
	payload []byte,
	provider, model, policyID string,
	policyVersion int,
	usage domain.TokenUsage,
	costUSD float64,
	providerLatencyMS int64,
) {
	if s.cache == nil || !s.cache.Enabled() {
		return
	}
	if rc.Stream || rc.CacheBypass {
		return
	}
	dec := rc.CacheDecision
	if dec == nil || !dec.Cacheable {
		return
	}
	sensitive, _ := cache.SensitivityOf(rc.DataSensitivity)
	if sensitive {
		return
	}
	meta := domain.CacheHitMeta{
		Model: model, Provider: provider, PolicyID: policyID,
		PolicyVersion: policyVersion, EndpointID: rc.EndpointID,
		Usage: usage, CostUSD: costUSD, ProviderLatencyMS: providerLatencyMS,
	}
	if dec != nil && dec.TTLSeconds > 0 {
		meta.ExpiresAt = domain.Now().Add(time.Duration(dec.TTLSeconds) * time.Second)
	}
	key := s.cache.StoreFull(ctx, rc.TenantID(), rc.APIKeyID(), body.Model, body, payload, meta)
	if key != "" && s.repos != nil && s.repos.CacheEntries != nil {
		bctx, cancel := bookkeepingContext()
		defer cancel()
		_ = s.repos.CacheEntries.UpsertMeta(bctx, &domain.CacheEntry{
			TenantID: rc.TenantID(), Kind: domain.CacheExact, CacheKey: key,
			Model: model, Provider: provider, PolicyID: policyID,
			EndpointID: rc.EndpointID, APIKeyID: rc.APIKeyID(),
			PromptHash: cache.KeyInputFromRequest(rc.TenantID(), rc.APIKeyID(),
				body.Model, body, policyID, policyVersion, rc.EndpointID, "", body.User).PromptHash(),
			PromptPreview: cache.KeyInputFromRequest(rc.TenantID(), rc.APIKeyID(),
				body.Model, body, policyID, policyVersion, rc.EndpointID, "", body.User).PromptPreview(120),
			HitCount: 0,
		})
	}
}

// safeRegistryNames maps registry tool names to their safety verdict.
func safeRegistryNames(toolCtx *toolContext) map[string]bool {
	if toolCtx == nil || toolCtx.Registry == nil {
		return nil
	}
	out := map[string]bool{}
	for _, t := range toolCtx.Registry.All() {
		// Only deterministic read-only built-ins are safe to reuse.
		out[t.Name] = t.SafetyLevel == domain.SafetySafe && t.Executable
	}
	return out
}

func hasImageParts(body *domain.ChatCompletionRequest) bool {
	if body == nil {
		return false
	}
	for _, m := range body.Messages {
		if m.Content.HasImages() {
			return true
		}
	}
	return false
}

// applyCachePolicy overlays a durable per-scope rule onto the global
// decision. A disabled rule bypasses; TTL and tier flags narrow or widen
// the global defaults without ever widening tenant isolation.
func applyCachePolicy(dec *domain.CacheDecision, matched *domain.CachePolicy) {
	if dec == nil || matched == nil {
		return
	}
	if !matched.Enabled {
		dec.Cacheable = false
		dec.BypassReason = domain.CacheBypassPolicyDisabled
		dec.Scope = cachePolicyScope(matched)
		return
	}
	if matched.TTLSeconds > 0 {
		dec.TTLSeconds = matched.TTLSeconds
	}
	if matched.Semantic != nil {
		dec.AllowSemantic = *matched.Semantic
	}
	if matched.Prefix != nil {
		dec.AllowPrefix = *matched.Prefix
	}
	if matched.BypassTools != nil && !*matched.BypassTools {
		// An explicit opt-in to tool reuse: only still-safe requests benefit,
		// the Evaluate verdict for unsafe tools stands.
	}
	if matched.AllowNonDet != nil && *matched.AllowNonDet {
		if dec.BypassReason == domain.CacheBypassNondeterministic {
			dec.Cacheable = true
			dec.BypassReason = ""
		}
	}
	dec.Scope = cachePolicyScope(matched)
}

// tierAllowed reports whether a hit kind survives the rule's tier flags.
func tierAllowed(kind domain.CacheKind, matched *domain.CachePolicy, dec domain.CacheDecision) bool {
	switch kind {
	case domain.CacheSemantic:
		if matched.Semantic != nil {
			return *matched.Semantic
		}
		return dec.AllowSemantic
	case domain.CachePrefix:
		if matched.Prefix != nil {
			return *matched.Prefix
		}
		return dec.AllowPrefix
	default:
		return dec.AllowExact
	}
}
