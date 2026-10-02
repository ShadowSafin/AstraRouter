// Package routing implements AstraRouter's deterministic routing engine and the
// executor that carries a request through its fallback chain.
//
// # Division of responsibility
//
// The engine answers "where should this request go?". The executor answers "what
// actually happened when we tried?". Both are pure with respect to their inputs:
// they consult interfaces for catalogues, policies and health, and they take a
// clock and a sleep function so their behaviour is fully reproducible in tests.
//
// Nothing in this package opens a database connection, reads the environment, or
// knows that HTTP exists. That is what makes the routing rules testable as plain
// functions, which is the only way a routing engine can be trusted in production.
package routing

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/shadowsafin/astrarouter/internal/domain"
)

// Catalogue supplies the provider and model registry to the engine.
//
// It is an interface defined here, in the consuming package, rather than in the
// storage package: routing declares the narrow view of the registry it needs, and
// storage satisfies it. That inversion is what lets routing be tested with an
// in-memory catalogue and no database.
type Catalogue interface {
	// Models returns every registered model, including disabled ones. Filtering
	// is the engine's job, because it must explain why a model was excluded.
	Models(ctx context.Context) ([]domain.Model, error)
	// Providers returns every configured provider, including disabled ones.
	Providers(ctx context.Context) ([]domain.Provider, error)
}

// PolicyResolver selects the routing policy for a request.
//
// The resolver never returns nil: when nothing matches it must synthesize the
// default policy, so the engine has exactly one code path.
type PolicyResolver interface {
	Resolve(ctx context.Context, rc *domain.RequestContext) (*domain.RoutingPolicy, error)
}

// HealthProvider reports provider health to the engine.
type HealthProvider interface {
	// Health returns the current assessment for a provider id.
	Health(providerID string) (domain.ProviderHealth, bool)
}

// Observer receives the outcome of routing and execution.
//
// It is deliberately fire-and-forget: an implementation must not block the
// request path, and a failure to record must never fail a request. The concrete
// implementation buffers and flushes asynchronously.
type Observer interface {
	// RecordDecision is called once, as soon as a route is chosen.
	RecordDecision(ctx context.Context, rc *domain.RequestContext, decision *domain.RouteDecision)
	// RecordAttempt is called after every provider call.
	RecordAttempt(ctx context.Context, rc *domain.RequestContext, attempt domain.TraceAttempt)
	// RecordOutcome is called once, when the request finishes.
	RecordOutcome(ctx context.Context, rc *domain.RequestContext, trace *domain.RequestTrace)
}

// NopObserver discards all observations. It is the default so a nil observer can
// never panic on the request path.
type NopObserver struct{}

// RecordDecision implements Observer.
func (NopObserver) RecordDecision(context.Context, *domain.RequestContext, *domain.RouteDecision) {}

// RecordAttempt implements Observer.
func (NopObserver) RecordAttempt(context.Context, *domain.RequestContext, domain.TraceAttempt) {}

// RecordOutcome implements Observer.
func (NopObserver) RecordOutcome(context.Context, *domain.RequestContext, *domain.RequestTrace) {}

// StaticCatalogue is an immutable in-memory catalogue.
//
// It has three uses: unit tests, the bootstrap path (where providers and models
// come from the config file rather than the database), and as a read-through
// cache backing the Postgres catalogue. Because it never mutates after
// construction it needs no locking on the read path.
type StaticCatalogue struct {
	models    []domain.Model
	providers []domain.Provider

	byProviderID map[string]domain.Provider
	byProviderNm map[string]domain.Provider
	// byProviderModels indexes models by provider id for fast lookups.
	byProviderModels map[string][]domain.Model
	// byName indexes models by their own name and by every alias they declare.
	byName map[string][]domain.Model
}

// NewStaticCatalogue builds an index over the supplied registry.
func NewStaticCatalogue(models []domain.Model, providers []domain.Provider) *StaticCatalogue {
	c := &StaticCatalogue{
		models:           models,
		providers:        providers,
		byProviderID:     map[string]domain.Provider{},
		byProviderNm:     map[string]domain.Provider{},
		byProviderModels: map[string][]domain.Model{},
		byName:           map[string][]domain.Model{},
	}
	for _, p := range providers {
		c.byProviderID[p.ID] = p
		c.byProviderNm[p.Name] = p
	}
	for _, m := range models {
		c.byProviderModels[m.ProviderID] = append(c.byProviderModels[m.ProviderID], m)

		// A model is reachable by its upstream name and by each alias. Both are
		// indexed so a client can request either.
		for _, key := range append([]string{m.Name}, m.Aliases...) {
			key = strings.ToLower(strings.TrimSpace(key))
			if key == "" {
				continue
			}
			c.byName[key] = append(c.byName[key], m)
		}
	}
	return c
}

// Models implements Catalogue.
func (c *StaticCatalogue) Models(context.Context) ([]domain.Model, error) {
	return c.models, nil
}

// Providers implements Catalogue.
func (c *StaticCatalogue) Providers(context.Context) ([]domain.Provider, error) {
	return c.providers, nil
}

// ProviderByID returns a provider by identifier.
func (c *StaticCatalogue) ProviderByID(id string) (domain.Provider, bool) {
	p, ok := c.byProviderID[id]
	return p, ok
}

// ProviderByName returns a provider by name.
func (c *StaticCatalogue) ProviderByName(name string) (domain.Provider, bool) {
	p, ok := c.byProviderNm[name]
	return p, ok
}

// Provider resolves a provider by id first, then by name, mirroring how routing
// targets are written.
func (c *StaticCatalogue) Provider(idOrName string) (domain.Provider, bool) {
	if p, ok := c.byProviderID[idOrName]; ok {
		return p, true
	}
	p, ok := c.byProviderNm[idOrName]
	return p, ok
}

// ModelsByProvider returns the models registered on a provider.
func (c *StaticCatalogue) ModelsByProvider(providerID string) []domain.Model {
	return c.byProviderModels[providerID]
}

// FindModels returns every model reachable by a name or alias, in a stable
// order. Sorting matters because candidate ordering feeds the priority strategy:
// an unstable order would make routing non-reproducible across restarts.
func (c *StaticCatalogue) FindModels(nameOrAlias string) []domain.Model {
	key := strings.ToLower(strings.TrimSpace(nameOrAlias))
	if key == "" {
		return nil
	}
	// Exact index hit first, then glob patterns over every known name.
	if matches, ok := c.byName[key]; ok {
		return sortedModels(matches)
	}

	var out []domain.Model
	for _, m := range c.models {
		if domain.MatchModelPattern(key, strings.ToLower(m.Name)) {
			out = append(out, m)
			continue
		}
		for _, alias := range m.Aliases {
			if domain.MatchModelPattern(key, strings.ToLower(alias)) {
				out = append(out, m)
				break
			}
		}
	}
	return sortedModels(out)
}

// sortedModels returns a deterministically ordered copy.
func sortedModels(models []domain.Model) []domain.Model {
	if len(models) == 0 {
		return nil
	}
	out := make([]domain.Model, len(models))
	copy(out, models)
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProviderName != out[j].ProviderName {
			return out[i].ProviderName < out[j].ProviderName
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// CachedCatalogue wraps a slow catalogue with a mutex-guarded snapshot.
//
// The registry changes rarely and is read on every request, so re-reading it from
// Postgres per request would add a query to the hot path for no benefit. Reload
// is explicit (driven by the admin API and by a periodic refresh) so an operator
// can see exactly when new configuration takes effect.
type CachedCatalogue struct {
	source Catalogue
	ttl    int64 // nanoseconds; advisory, refresh is caller-driven

	mu       sync.RWMutex
	models   []domain.Model
	provider []domain.Provider
	loaded   bool
}

// NewCachedCatalogue wraps source.
func NewCachedCatalogue(source Catalogue) *CachedCatalogue {
	return &CachedCatalogue{source: source}
}

// Load refreshes the snapshot from the source.
func (c *CachedCatalogue) Load(ctx context.Context) error {
	models, err := c.source.Models(ctx)
	if err != nil {
		return err
	}
	providers, err := c.source.Providers(ctx)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.models = models
	c.provider = providers
	c.loaded = true
	return nil
}

// Models implements Catalogue.
func (c *CachedCatalogue) Models(context.Context) ([]domain.Model, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.loaded {
		return nil, nil
	}
	return c.models, nil
}

// Providers implements Catalogue.
func (c *CachedCatalogue) Providers(context.Context) ([]domain.Provider, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.loaded {
		return nil, nil
	}
	return c.provider, nil
}

// Snapshot returns an indexed view of the current snapshot, which is what the
// engine actually queries during candidate construction.
func (c *CachedCatalogue) Snapshot() *StaticCatalogue {
	c.mu.RLock()
	models := c.models
	providers := c.provider
	c.mu.RUnlock()
	return NewStaticCatalogue(models, providers)
}
