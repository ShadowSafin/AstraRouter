// Package providers contains CoreRouter's provider adapters.
//
// # Design contract
//
// An Adapter performs exactly ONE attempt against ONE upstream. It is
// responsible for:
//
//   - translating a normalized request into the provider's wire format
//   - applying credentials and per-attempt timeouts
//   - decoding both streaming and non-streaming responses
//   - extracting token usage when the provider reports it
//   - translating every failure into a domain.Error
//
// An Adapter is explicitly NOT responsible for retries across attempts or for
// moving between providers. Those decisions need cross-provider context (the
// fallback chain, the remaining time budget, the cost ceiling, health of the
// other candidates) and belong to the routing executor. Splitting them this way
// means retry and fallback semantics are defined once, for every provider,
// instead of once per adapter where they would inevitably diverge.
//
// The only retrying an adapter does is at the transport level, inside net/http,
// and only for connection failures on requests that were never written.
package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"

	"github.com/corerouter/corerouter/internal/domain"
)

// Request is a single completion request bound to a concrete model.
type Request struct {
	// Model is the upstream model name to send. The adapter never consults the
	// registry; the router has already resolved the alias.
	Model string
	// Params carries the client's normalized request.
	Params *domain.ChatCompletionRequest
	// Ref identifies the provider and model, used for error annotation and
	// telemetry labels.
	Ref domain.ModelRef
	// MaxTokens is the completion allowance the router computed from the
	// client's request and the policy ceiling.
	MaxTokens int
	// PromptTokens is the router's prompt-size estimate. Adapters use it only
	// when a provider fails to report usage, so that accounting degrades to an
	// estimate instead of to zero.
	PromptTokens int
	// Stream selects the streaming code path.
	Stream bool
	// Extra carries adapter-specific options for providers with settings the
	// OpenAI schema does not cover (for example Ollama's `options` block).
	Extra map[string]any
}

// Response is the normalized result of an attempt.
type Response struct {
	// ID is the provider's completion identifier, preserved for support cases.
	ID string
	// Model is the model the provider reports having served.
	Model string
	// Choices are the completion candidates.
	Choices []domain.Choice
	// Usage is provider-reported or estimated token accounting.
	Usage domain.TokenUsage
	// Created is the provider's unix creation timestamp, or the gateway's when
	// the provider does not supply one.
	Created int64
	// SystemFingerprint is passed through when present.
	SystemFingerprint string
	// Raw is the provider's raw response body, retained for debugging and for
	// future replay without re-calling the provider.
	Raw json.RawMessage
}

// Content returns the first choice's message text.
func (r *Response) Content() string {
	if r == nil || len(r.Choices) == 0 || r.Choices[0].Message == nil {
		return ""
	}
	return r.Choices[0].Message.Content.PlainText()
}

// FinishReason returns the first choice's finish reason.
func (r *Response) FinishReason() domain.FinishReason {
	if r == nil || len(r.Choices) == 0 || r.Choices[0].FinishReason == nil {
		return ""
	}
	return *r.Choices[0].FinishReason
}

// Chunk is one incremental piece of a streaming response, normalized across
// providers. Anthropic's content_block_delta and Ollama's NDJSON lines both
// reduce to this shape.
type Chunk struct {
	// ID is the completion identifier, repeated on every chunk by convention.
	ID string
	// Model is the serving model.
	Model string
	// Created is the completion timestamp.
	Created int64
	// Index is the choice index this delta belongs to.
	Index int
	// Delta is the incremental content for this chunk.
	Delta domain.ChatMessage
	// FinishReason is set on the final content chunk, when the provider
	// announces it.
	FinishReason *domain.FinishReason
	// Usage is set on the usage chunk when the provider reports it.
	Usage *domain.TokenUsage
	// SystemFingerprint is passed through when present.
	SystemFingerprint string
}

// StreamHandler receives each chunk. Returning an error aborts the stream; the
// adapter treats the error as caller-initiated (typically a client disconnect)
// and propagates it verbatim rather than wrapping it as a provider failure.
type StreamHandler func(chunk Chunk) error

// Adapter is the interface every provider implementation satisfies.
//
// Adding a provider means implementing this interface and registering it in
// NewAdapter. No routing, policy or API code changes.
type Adapter interface {
	// Name returns the configured provider name.
	Name() string
	// Kind returns the provider kind, used for labels and capability defaults.
	Kind() domain.ProviderKind
	// Capabilities returns the provider-wide capability set.
	Capabilities() domain.CapabilitySet
	// ChatCompletion performs a single non-streaming attempt.
	ChatCompletion(ctx context.Context, req *Request) (*Response, error)
	// ChatCompletionStream performs a single streaming attempt, invoking
	// handler for each chunk. The returned Response contains the accumulated
	// choices and usage, so callers get a complete record either way.
	ChatCompletionStream(ctx context.Context, req *Request, handler StreamHandler) (*Response, error)
	// HealthCheck probes the provider and reports its state.
	HealthCheck(ctx context.Context) domain.ProviderHealth
	// Enabled reports whether the provider is configured to serve traffic.
	Enabled() bool
}

// Registry holds the configured adapters, keyed by provider id and name.
//
// The registry is consulted on every routed request, so lookups are map-based
// and lock-free after construction. Reload replaces the whole map under a write
// lock rather than mutating entries, which makes concurrent reads safe without
// holding a lock across a provider call.
type Registry struct {
	mu sync.RWMutex
	// byID and byName are separate maps rather than one map with two key
	// shapes, because ids and names could otherwise collide.
	byID   map[string]Adapter
	byName map[string]Adapter
	// providers keeps the configuration so the registry can report health and
	// metadata without a database round trip.
	providers map[string]domain.Provider
	// order is the deterministic name ordering used by List.
	order []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		byID:      map[string]Adapter{},
		byName:    map[string]Adapter{},
		providers: map[string]domain.Provider{},
	}
}

// Register adds or replaces an adapter.
func (r *Registry) Register(p domain.Provider, a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[p.ID] = a
	if _, exists := r.byName[p.Name]; !exists {
		r.order = append(r.order, p.Name)
		sort.Strings(r.order)
	}
	r.byName[p.Name] = a
	r.providers[p.ID] = p
}

// Replace swaps the entire registry contents. It is used by the configuration
// reload path so a reload is atomic from a reader's perspective.
func (r *Registry) Replace(entries map[string]domain.Provider, adapters map[string]Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID = make(map[string]Adapter, len(adapters))
	r.byName = make(map[string]Adapter, len(adapters))
	r.providers = make(map[string]domain.Provider, len(entries))
	r.order = r.order[:0]
	for id, p := range entries {
		a, ok := adapters[id]
		if !ok {
			continue
		}
		r.byID[id] = a
		r.byName[p.Name] = a
		r.providers[id] = p
		r.order = append(r.order, p.Name)
	}
	sort.Strings(r.order)
}

// ByID returns the adapter for a provider id.
func (r *Registry) ByID(id string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.byID[id]
	return a, ok
}

// ByName returns the adapter for a provider name.
func (r *Registry) ByName(name string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.byName[name]
	return a, ok
}

// Resolve looks up an adapter by id first, then by name, which lets routing
// targets reference whichever identifier the policy author preferred.
func (r *Registry) Resolve(idOrName string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if a, ok := r.byID[idOrName]; ok {
		return a, true
	}
	a, ok := r.byName[idOrName]
	return a, ok
}

// Provider returns the stored configuration for a provider id.
func (r *Registry) Provider(id string) (domain.Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[id]
	return p, ok
}

// Providers returns every configured provider, ordered by name.
func (r *Registry) Providers() []domain.Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Index by name once so the ordering pass is linear instead of quadratic.
	byName := make(map[string]domain.Provider, len(r.providers))
	for _, p := range r.providers {
		byName[p.Name] = p
	}

	out := make([]domain.Provider, 0, len(r.order))
	for _, name := range r.order {
		if p, ok := byName[name]; ok {
			out = append(out, p)
		}
	}
	return out
}

// Snapshot returns the current provider configurations and adapters, keyed by
// provider id. It exists so a reload can build a fresh set and swap them in
// atomically, rather than mutating entries one at a time.
func (r *Registry) Snapshot() (map[string]domain.Provider, map[string]Adapter) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providers := make(map[string]domain.Provider, len(r.providers))
	for id, p := range r.providers {
		providers[id] = p
	}
	adapters := make(map[string]Adapter, len(r.byID))
	for id, a := range r.byID {
		adapters[id] = a
	}
	return providers, adapters
}

// Len returns the number of registered providers.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byID)
}

// Names returns the configured provider names in sorted order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// HTTPDoer abstracts the HTTP client so adapters can be tested against a stub
// transport without a network.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}
