package providers

import (
	"fmt"

	"github.com/shadowsafin/synapass/internal/domain"
)

// NewAdapter constructs the adapter for a provider's kind.
//
// The switch is exhaustive over domain.ProviderKind, and there is deliberately no
// default case that silently falls back to a generic implementation: a new kind
// must be wired here on purpose, which is what keeps "add a provider" from
// quietly doing the wrong thing.
func NewAdapter(p domain.Provider, opts Options) (Adapter, error) {
	switch p.Kind {
	case domain.ProviderOpenAI, domain.ProviderVLLM, domain.ProviderOpenAICompatible:
		// All three speak the OpenAI wire format. They share an implementation
		// and differ only in defaults and which optional fields they receive.
		return newOpenAIAdapter(p, opts)
	case domain.ProviderAnthropic:
		return newAnthropicAdapter(p, opts)
	case domain.ProviderOllama:
		return newOllamaAdapter(p, opts)
	case "":
		return nil, fmt.Errorf("provider %q has no kind configured", p.Name)
	default:
		return nil, fmt.Errorf("provider %q has unsupported kind %q", p.Name, p.Kind)
	}
}

// BuildRegistry constructs adapters for every provider and returns a populated
// registry along with a per-provider error map.
//
// A provider that fails to construct is reported rather than aborting startup:
// one misconfigured provider must not prevent the gateway from serving traffic
// through the providers that are configured correctly. The errors are surfaced
// on the admin provider-status endpoint and in the startup log.
func BuildRegistry(providers []domain.Provider, opts Options) (*Registry, map[string]error) {
	reg := NewRegistry()
	failures := map[string]error{}

	for _, p := range providers {
		adapter, err := NewAdapter(p, opts)
		if err != nil {
			failures[p.Name] = err
			continue
		}
		reg.Register(p, adapter)
	}
	return reg, failures
}
