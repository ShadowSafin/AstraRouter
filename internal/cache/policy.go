package cache

import (
	"strings"
	"time"

	"github.com/shadowsafin/corerouter/internal/domain"
)

// PolicyInput is everything the cache policy decision needs. It mirrors the
// request flow order: authenticate → tenant/endpoint policy → cache decision.
type PolicyInput struct {
	TenantID     string
	EndpointID   string
	ProviderHint string
	Model        string
	Request      KeyInput
	Stream       bool
	CacheBypass  bool
	Sensitive    bool
	Sensitivity  string
	HasTools     bool
	ToolsSafe    bool
	HasImages    bool
	N            int
	Temperature  *float64
	Seed         *int
	PolicyUseCache bool
	PolicyID       string
	EndpointCache  *bool
	RequestBytes int
}

// PolicyConfig are the global defaults the per-scope rows override.
type PolicyConfig struct {
	Enabled               bool
	ExactEnabled          bool
	SemanticEnabled       bool
	PrefixEnabled         bool
	TTL                   time.Duration
	SemanticThreshold     float64
	BypassTools           bool
	AllowNondeterministic bool
	BypassLiveData        bool
}

// Evaluate decides whether a request may use the cache and under which
// tiers. Safety first: any doubt bypasses with a named reason rather than
// risking an incorrect reuse.
func Evaluate(in PolicyInput, cfg PolicyConfig) domain.CacheDecision {
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	allow := func() domain.CacheDecision {
		return domain.CacheDecision{
			Cacheable:     true,
			TTL:           ttl,
			TTLSeconds:    int(ttl.Seconds()),
			AllowExact:    cfg.Enabled && cfg.ExactEnabled,
			AllowPrefix:   cfg.Enabled && cfg.PrefixEnabled,
			AllowSemantic: cfg.Enabled && cfg.SemanticEnabled,
			PolicyID:      in.PolicyID,
			Scope:         "global",
		}
	}
	bypass := func(reason string) domain.CacheDecision {
		return domain.CacheDecision{Cacheable: false, BypassReason: reason, TTL: ttl, PolicyID: in.PolicyID}
	}

	if !cfg.Enabled {
		return bypass(domain.CacheBypassDisabled)
	}
	if in.CacheBypass {
		return bypass(domain.CacheBypassRequested)
	}
	if in.Stream {
		return bypass(domain.CacheBypassStreaming)
	}
	if in.Sensitive {
		return bypass(domain.CacheBypassSensitive)
	}
	if !in.PolicyUseCache {
		return bypass(domain.CacheBypassPolicyDisabled)
	}
	if in.EndpointCache != nil && !*in.EndpointCache {
		return bypass(domain.CacheBypassEndpointDisabled)
	}
	if in.HasImages {
		return bypass(domain.CacheBypassMultimodal)
	}
	if in.N > 1 {
		return bypass(domain.CacheBypassMultiSample)
	}
	if in.HasTools && cfg.BypassTools {
		// Safe built-ins (now/echo) are deterministic and tiny; anything else
		// may depend on definitions, approvals or side effects.
		if !in.ToolsSafe {
			return bypass(domain.CacheBypassToolRequest)
		}
	}
	if cfg.BypassLiveData && looksLive(in.Request) {
		return bypass(domain.CacheBypassLiveData)
	}
	if !cfg.AllowNondeterministic && isNondeterministic(in.Temperature, in.Seed) {
		return bypass(domain.CacheBypassNondeterministic)
	}
	return allow()
}

// isNondeterministic reports whether generation settings allow reuse.
// A zero temperature (or an explicit seed) is deterministic; anything else
// needs an operator opt-in.
func isNondeterministic(temp *float64, seed *int) bool {
	if seed != nil {
		return false
	}
	if temp == nil {
		return false
	}
	return *temp > 0
}

// looksLive is a conservative heuristic for live-data prompts. It errs on
// the side of bypassing: a false positive costs one provider call, a false
// negative serves stale facts.
func looksLive(in KeyInput) bool {
	for _, m := range in.Messages {
		s := strings.ToLower(m.Text())
		if s == "" {
			continue
		}
		for _, kw := range []string{
			"live ", "real-time", "realtime", "right now", "as of today",
			"stock price", "weather now", "current price", "latest news",
			"what time is it", "today's date",
		} {
			if strings.Contains(s, kw) {
				return true
			}
		}
	}
	return false
}

// ToolsSafeForCache reports whether the attached tools are deterministic
// built-ins whose results are safe to reuse. Registry tools are resolved by
// the caller; wire-only tools default to unsafe.
func ToolsSafeForCache(tools []domain.Tool, registrySafe map[string]bool) bool {
	if len(tools) == 0 {
		return true
	}
	for _, t := range tools {
		name := t.Function.Name
		if safe, ok := registrySafe[name]; ok {
			if !safe {
				return false
			}
			continue
		}
		// Unknown wire tools: only the deterministic built-ins are safe.
		if name != "now" && name != "echo" {
			return false
		}
	}
	return true
}

// SensitivityOf folds the request sensitivity labels into one verdict.
func SensitivityOf(labels []string) (sensitive bool, joined string) {
	for _, l := range labels {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" || l == "public" {
			continue
		}
		return true, l
	}
	return false, ""
}
