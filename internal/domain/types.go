// Package domain holds Synapass's first-class domain objects.
//
// Everything in this package is deliberately dependency-free: it imports only
// the standard library. Services (routing, policy, storage, providers) depend on
// these types, never the other way round. That keeps the core model stable while
// transports, datastores and providers evolve independently.
package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// NewID returns a new time-ordered-free unique identifier for domain objects.
// UUIDv4 is used rather than a database sequence so identifiers can be minted
// before a round trip, which keeps the request path free of write-then-read
// dependencies.
func NewID() string {
	return uuid.NewString()
}

// Now returns the canonical timestamp for persisted records. It is a function
// rather than a direct time.Now call so tests can reason about ordering.
func Now() time.Time {
	return time.Now().UTC()
}

// Capability is a coarse tag describing what a provider or model can do.
// Capabilities are matched structurally by the routing engine; they are the
// main extension point for adding providers with unusual feature sets.
type Capability string

const (
	CapChat         Capability = "chat"
	CapStreaming    Capability = "streaming"
	CapTools        Capability = "tools"
	CapParallelTool Capability = "parallel_tools"
	CapVision       Capability = "vision"
	CapJSONMode     Capability = "json_mode"
	CapJSONSchema   Capability = "json_schema"
	CapEmbeddings   Capability = "embeddings"
	CapLongContext  Capability = "long_context"
	CapReasoning    Capability = "reasoning"
	CapSeed         Capability = "seed"
)

// CapabilitySet is a set of capabilities supporting fast membership tests.
type CapabilitySet map[Capability]struct{}

// NewCapabilitySet builds a set from a slice of capabilities.
func NewCapabilitySet(caps ...Capability) CapabilitySet {
	s := make(CapabilitySet, len(caps))
	for _, c := range caps {
		if c == "" {
			continue
		}
		s[c] = struct{}{}
	}
	return s
}

// Has reports whether the set contains every capability in want.
func (s CapabilitySet) Has(want ...Capability) bool {
	for _, c := range want {
		if _, ok := s[c]; !ok {
			return false
		}
	}
	return true
}

// Contains reports whether a single capability is present.
func (s CapabilitySet) Contains(c Capability) bool {
	_, ok := s[c]
	return ok
}

// Slice returns the capabilities in a stable, sorted order. Determinism matters
// because capability lists are persisted and compared in tests.
func (s CapabilitySet) Slice() []Capability {
	out := make([]Capability, 0, len(s))
	for c := range s {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// RequestType identifies the OpenAI-compatible surface a request arrived on.
type RequestType string

const (
	RequestTypeChatCompletion RequestType = "chat.completions"
	RequestTypeCompletion     RequestType = "completions"
	RequestTypeEmbedding      RequestType = "embeddings"
)

// Valid reports whether the request type is one Synapass understands.
func (r RequestType) Valid() bool {
	switch r {
	case RequestTypeChatCompletion, RequestTypeCompletion, RequestTypeEmbedding:
		return true
	default:
		return false
	}
}

// Status is the lifecycle state shared by tenants, keys, providers and models.
type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
	StatusRevoked  Status = "revoked"
	StatusPending  Status = "pending"
	StatusDegraded Status = "degraded"
	StatusDown     Status = "down"
)

// IsUsable reports whether a record in this state may serve traffic.
func (s Status) IsUsable() bool {
	return s == StatusActive || s == StatusDegraded
}

// TokenUsage is provider-reported or estimated token accounting.
type TokenUsage struct {
	PromptTokens     int  `json:"prompt_tokens"`
	CompletionTokens int  `json:"completion_tokens"`
	TotalTokens      int  `json:"total_tokens"`
	Estimated        bool `json:"estimated"`
	// CachedPromptTokens is reported by providers with prompt caching enabled.
	CachedPromptTokens int `json:"cached_prompt_tokens,omitempty"`
}

// Normalize fills in TotalTokens when a provider reports only the two halves.
// It preserves explicitly reported totals because some providers include
// reasoning tokens in a way that is not derivable from the other fields.
func (u TokenUsage) Normalize() TokenUsage {
	if u.TotalTokens == 0 {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return u
}

// Add accumulates usage across attempts.
func (u TokenUsage) Add(other TokenUsage) TokenUsage {
	return TokenUsage{
		PromptTokens:       u.PromptTokens + other.PromptTokens,
		CompletionTokens:   u.CompletionTokens + other.CompletionTokens,
		TotalTokens:        u.TotalTokens + other.TotalTokens,
		CachedPromptTokens: u.CachedPromptTokens + other.CachedPromptTokens,
		Estimated:          u.Estimated || other.Estimated,
	}
}

// Cost is a monetary amount in US dollars, stored as float64 because provider
// price sheets are quoted per-token and precision beyond micro-dollars is not
// meaningful for a gateway. All arithmetic funnels through these helpers so a
// future switch to a fixed-point representation is a single-file change.
type Cost struct {
	USD float64 `json:"usd"`
}

// Add returns the sum of two costs.
func (c Cost) Add(o Cost) Cost { return Cost{USD: c.USD + o.USD} }

// AddUSD returns the cost increased by amount.
func (c Cost) AddUSD(amount float64) Cost { return Cost{USD: c.USD + amount} }

// IsZero reports whether the cost is exactly zero.
func (c Cost) IsZero() bool { return c.USD == 0 }

// GreaterThan reports whether the cost exceeds other.
func (c Cost) GreaterThan(other Cost) bool { return c.USD > other.USD }

// String renders the cost with enough precision for logs and API responses.
func (c Cost) String() string { return fmt.Sprintf("$%.6f", c.USD) }

// Duration aliases time.Duration for readability in policy definitions.
type Duration = time.Duration

// ModelRef names a provider-specific model. It is the unit the routing engine
// resolves to and the provider adapters execute against.
type ModelRef struct {
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	// Model is the name the upstream provider expects, e.g. "gpt-4o-mini".
	Model string `json:"model"`
	// Alias is the name the client asked for, e.g. "fast". It is recorded so
	// routing decisions can be explained after the fact.
	Alias string `json:"alias,omitempty"`
}

// String renders a ref as "provider/model", the canonical human form used in
// logs, metrics labels and error messages.
func (m ModelRef) String() string {
	p := m.ProviderName
	if p == "" {
		p = m.ProviderID
	}
	return fmt.Sprintf("%s/%s", p, m.Model)
}

// Key returns a stable map key for the ref.
func (m ModelRef) Key() string {
	return strings.Join([]string{m.ProviderID, m.Model}, "\x00")
}

// RequestID is a correlation identifier propagated across HTTP, traces, logs
// and async jobs. It is a distinct type to prevent mixing it with other IDs.
type RequestID string

// String implements fmt.Stringer.
func (r RequestID) String() string { return string(r) }
