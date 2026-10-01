package cache

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/shadowsafin/corerouter/internal/domain"
)

// Store is the fast body backend (Redis in production, memory in tests).
type Store interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	DeletePrefix(ctx context.Context, prefix string) (int, error)
}

// Options configures caching.
type Options struct {
	Enabled          bool
	TTL              time.Duration
	MaxResponseBytes int
	// SemanticThreshold is the cosine similarity in [0,1] for a semantic hit.
	SemanticThreshold float64
	// PrefixLength bounds prefix-tier response reuse (short prompts only).
	PrefixLength int
	// ExactEnabled gates exact reuse independently of the master switch.
	ExactEnabled bool
	// SemanticEnabled gates the semantic tier.
	SemanticEnabled bool
	// PrefixEnabled gates the prefix tier.
	PrefixEnabled bool
	// MaxSemanticEntries bounds the in-process semantic index.
	MaxSemanticEntries int
	// BypassTools skips tool-carrying requests (default true).
	BypassTools bool
	// AllowNondeterministic permits caching temp>0 unseeded requests.
	AllowNondeterministic bool
	// BypassLiveData skips live-data-looking prompts (default true).
	BypassLiveData bool
}

// DefaultOptions returns production-minded defaults.
func DefaultOptions() Options {
	return Options{
		Enabled:           true,
		ExactEnabled:      true,
		SemanticEnabled:   true,
		PrefixEnabled:     true,
		TTL:               5 * time.Minute,
		MaxResponseBytes:  256 << 10,
		SemanticThreshold: 0.92,
		PrefixLength:      256,
		MaxSemanticEntries: 2000,
		BypassTools:       true,
		BypassLiveData:    true,
	}
}

// Cache is the lookup/store facade.
type Cache struct {
	store Store
	opts  Options

	mu sync.Mutex
	// memIndex tracks semantic entries for similarity search when the store
	// cannot enumerate keys (Redis SCAN is expensive on the hot path, so the
	// gateway keeps a small in-process embedding index).
	memIndex map[string]semanticEntry
	stats    domain.CacheStats
	lookups  int64
	lookupMS float64
}

type semanticEntry struct {
	key       string
	nsKey     string
	embedding map[string]float64
	body      []byte
	expiresAt time.Time
	tenant    string
	input     KeyInput
}

// New creates a cache.
func New(store Store, opts Options) *Cache {
	if opts.TTL <= 0 {
		opts.TTL = 5 * time.Minute
	}
	if opts.SemanticThreshold <= 0 {
		opts.SemanticThreshold = 0.92
	}
	if opts.PrefixLength <= 0 {
		opts.PrefixLength = 256
	}
	if opts.MaxSemanticEntries <= 0 {
		opts.MaxSemanticEntries = 2000
	}
	return &Cache{store: store, opts: opts, memIndex: map[string]semanticEntry{}}
}

// Enabled reports whether caching is active.
func (c *Cache) Enabled() bool { return c != nil && c.opts.Enabled }

// Options returns a copy of the options.
func (c *Cache) Options() Options {
	if c == nil {
		return DefaultOptions()
	}
	return c.opts
}

// Stats returns a copy of hit/miss counters.
func (c *Cache) Stats() domain.CacheStats {
	if c == nil {
		return domain.CacheStats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats
	total := s.ExactHits + s.ExactMisses + s.PrefixHits + s.SemanticHits
	if s.Misses > 0 {
		total = s.ExactHits + s.PrefixHits + s.SemanticHits + s.Misses
		if s.ExactMisses == 0 {
			s.ExactMisses = s.Misses
		}
	}
	if total > 0 {
		s.HitRate = float64(s.ExactHits+s.PrefixHits+s.SemanticHits) / float64(total)
	}
	s.Entries = int64(len(c.memIndex))
	if c.lookups > 0 {
		s.LookupMSAvg = c.lookupMS / float64(c.lookups)
	}
	if s.BypassByReason == nil {
		s.BypassByReason = map[string]int64{}
	}
	return s
}

// namespaced scopes a bare kind:key under the tenant namespace. All Phase 5
// bodies live under tenant:<id>:<kind>:<hash>; legacy bare keys are read for
// compatibility but never written.
func namespaced(tenantID, bare string) string {
	if tenantID == "" {
		return bare
	}
	return "tenant:" + tenantID + ":" + bare
}

// Lookup checks exact, then prefix (short prompts only), then semantic.
func (c *Cache) Lookup(ctx context.Context, in KeyInput, bypass bool, bypassReason string, sensitive bool) domain.CacheLookupResult {
	start := time.Now()
	if c == nil || !c.opts.Enabled || bypass || sensitive {
		if c != nil {
			c.recordBypass(bypassReason, sensitive)
		}
		reason := bypassReason
		if sensitive && reason == "" {
			reason = domain.CacheBypassSensitive
		}
		if reason == "" {
			reason = domain.CacheBypassDisabled
		}
		return domain.CacheLookupResult{Hit: false, BypassReason: reason, Trace: &domain.CacheTrace{Hit: false, BypassReason: reason}}
	}
	// Exact tier.
	if c.opts.ExactEnabled {
		exact := ExactKey(in)
		ns := namespaced(in.TenantID, exact)
		if body, ok, _ := c.get(ctx, ns); ok {
			if payload, meta, ok2 := decodePayload(body); ok2 {
				if validFor(meta, in) {
					c.recordHit(domain.CacheExact, meta)
					return hitResult(domain.CacheExact, ns, payload, meta, start, 0)
				}
			} else if len(body) > 0 {
				// Legacy raw body without an envelope.
				c.recordHit(domain.CacheExact, domain.CacheHitMeta{})
				return hitResult(domain.CacheExact, ns, body, domain.CacheHitMeta{}, start, 0)
			}
		}
		// Legacy bare key for rolling upgrades.
		if in.TenantID != "" {
			if body, ok, _ := c.get(ctx, exact); ok {
				if payload, meta, ok2 := decodePayload(body); ok2 {
					if validFor(meta, in) {
						c.recordHit(domain.CacheExact, meta)
						return hitResult(domain.CacheExact, exact, payload, meta, start, 0)
					}
				} else if len(body) > 0 {
					c.recordHit(domain.CacheExact, domain.CacheHitMeta{})
					return hitResult(domain.CacheExact, exact, body, domain.CacheHitMeta{}, start, 0)
				}
			}
		}
	}
	// Prefix tier: short prompts only. A shared 256-char head with a
	// different tail must never serve as a hit — that was the Phase 2
	// correctness gap this gate closes.
	if c.opts.PrefixEnabled && in.NormalizedLength() <= c.opts.PrefixLength {
		prefix := PrefixKey(in, c.opts.PrefixLength)
		ns := namespaced(in.TenantID, prefix)
		if body, ok, _ := c.get(ctx, ns); ok {
			if payload, meta, ok2 := decodePayload(body); ok2 {
				if validFor(meta, in) {
					c.recordHit(domain.CachePrefix, meta)
					return hitResult(domain.CachePrefix, ns, payload, meta, start, 0)
				}
			} else if len(body) > 0 {
				c.recordHit(domain.CachePrefix, domain.CacheHitMeta{})
				return hitResult(domain.CachePrefix, ns, body, domain.CacheHitMeta{}, start, 0)
			}
		}
	}
	// Semantic tier: cosine over word-bag embeddings, tenant-isolated.
	if c.opts.SemanticEnabled {
		emb := embed(in)
		bestKey, bestBody, bestSim, bestMeta := c.semanticSearch(in.TenantID, in.Model, emb)
		if bestSim >= c.opts.SemanticThreshold && bestBody != nil {
			if payload, meta, ok := decodePayload(bestBody); ok {
				if validFor(meta, in) {
					c.recordHit(domain.CacheSemantic, meta)
					return hitResult(domain.CacheSemantic, bestKey, payload, meta, start, bestSim)
				}
			} else {
				c.recordHit(domain.CacheSemantic, bestMeta)
				return hitResult(domain.CacheSemantic, bestKey, bestBody, bestMeta, start, bestSim)
			}
		}
	}
	c.recordMiss(start)
	return domain.CacheLookupResult{Hit: false, Trace: &domain.CacheTrace{Hit: false, LookupMS: msSince(start)}}
}

// StoreResponse writes exact + prefix (short only) + semantic entries as
// envelopes carrying serving metadata for future validation.
func (c *Cache) StoreResponse(ctx context.Context, in KeyInput, body []byte, sensitive bool) string {
	return c.StoreResponseWithMeta(ctx, in, body, sensitive, domain.CacheHitMeta{})
}

// StoreResponseWithMeta stores with explicit serving metadata.
func (c *Cache) StoreResponseWithMeta(ctx context.Context, in KeyInput, body []byte, sensitive bool, meta domain.CacheHitMeta) string {
	if c == nil || !c.opts.Enabled || sensitive {
		return ""
	}
	if len(body) == 0 {
		return ""
	}
	if c.opts.MaxResponseBytes > 0 && len(body) > c.opts.MaxResponseBytes {
		c.recordBypass(domain.CacheBypassTooLarge, false)
		return ""
	}
	now := time.Now()
	if meta.PromptHash == "" {
		meta.PromptHash = in.PromptHash()
	}
	if meta.StoredAt.IsZero() {
		meta.StoredAt = now
	}
	if meta.ExpiresAt.IsZero() {
		meta.ExpiresAt = now.Add(c.opts.TTL)
	}
	if meta.Model == "" {
		meta.Model = in.Model
	}
	if meta.Provider == "" {
		meta.Provider = in.Provider
	}
	if meta.PolicyID == "" {
		meta.PolicyID = in.PolicyID
	}
	if meta.EndpointID == "" {
		meta.EndpointID = in.EndpointID
	}
	if meta.ToolsHash == "" {
		meta.ToolsHash = in.ToolsHash()
	}
	envelope, err := json.Marshal(domain.CachedPayload{Body: body, Meta: meta})
	if err != nil {
		return ""
	}
	exact := ExactKey(in)
	_ = c.set(ctx, namespaced(in.TenantID, exact), envelope, c.opts.TTL)
	if in.NormalizedLength() <= c.opts.PrefixLength {
		prefix := PrefixKey(in, c.opts.PrefixLength)
		_ = c.set(ctx, namespaced(in.TenantID, prefix), envelope, c.opts.TTL)
	}
	// Semantic index.
	emb := embed(in)
	embHash := embeddingHash(emb)
	semKey := "semantic:" + embHash
	nsSem := namespaced(in.TenantID, semKey)
	_ = c.set(ctx, nsSem, envelope, c.opts.TTL)
	c.mu.Lock()
	c.memIndex[nsSem] = semanticEntry{
		key: semKey, nsKey: nsSem, embedding: emb, body: envelope,
		expiresAt: now.Add(c.opts.TTL), tenant: in.TenantID, input: in,
	}
	if len(c.memIndex) > c.opts.MaxSemanticEntries {
		now2 := time.Now()
		for k, e := range c.memIndex {
			if now2.After(e.expiresAt) {
				delete(c.memIndex, k)
			}
		}
	}
	c.mu.Unlock()
	return exact
}

// InvalidateTenant removes tenant-scoped entries (legacy entry point kept
// for compatibility; prefer Invalidate with an explicit scope).
func (c *Cache) InvalidateTenant(ctx context.Context, tenantID string) (int, error) {
	if c == nil {
		return 0, nil
	}
	if tenantID == "" {
		return c.Invalidate(ctx, InvalidateScope{Scope: "all", Reason: domain.CacheInvalidateManual})
	}
	return c.Invalidate(ctx, InvalidateScope{Scope: "tenant", TenantID: tenantID, Reason: domain.CacheInvalidateManual})
}

func (c *Cache) get(ctx context.Context, key string) ([]byte, bool, error) {
	if c == nil || c.store == nil {
		return nil, false, nil
	}
	return c.store.Get(ctx, key)
}

func (c *Cache) set(ctx context.Context, key string, body []byte, ttl time.Duration) error {
	if c == nil || c.store == nil {
		return nil
	}
	return c.store.Set(ctx, key, body, ttl)
}

func (c *Cache) semanticSearch(tenant, model string, emb map[string]float64) (string, []byte, float64, domain.CacheHitMeta) {
	c.mu.Lock()
	defer c.mu.Unlock()
	best := 0.0
	var bestKey string
	var bestBody []byte
	var bestMeta domain.CacheHitMeta
	now := time.Now()
	for k, e := range c.memIndex {
		if now.After(e.expiresAt) {
			delete(c.memIndex, k)
			continue
		}
		if tenant != "" && e.tenant != "" && e.tenant != tenant {
			continue
		}
		if model != "" && e.input.Model != "" && !equalFold(e.input.Model, model) {
			continue
		}
		sim := cosine(emb, e.embedding)
		if sim > best {
			best = sim
			bestKey = e.nsKey
			bestBody = e.body
			if payload, meta, ok := decodePayload(e.body); ok {
				_ = payload
				bestMeta = meta
			}
		}
	}
	return bestKey, bestBody, best, bestMeta
}

func (c *Cache) recordHit(kind domain.CacheKind, meta domain.CacheHitMeta) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch kind {
	case domain.CacheExact:
		c.stats.ExactHits++
	case domain.CachePrefix:
		c.stats.PrefixHits++
	case domain.CacheSemantic:
		c.stats.SemanticHits++
	}
	c.stats.ReuseCount++
	if meta.ProviderLatencyMS > 0 {
		c.stats.LatencySavedMS += meta.ProviderLatencyMS
	}
}

func (c *Cache) recordMiss(start time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats.ExactMisses++
	c.stats.Misses++
	c.lookups++
	c.lookupMS += msSince(start)
}

func (c *Cache) recordBypass(reason string, sensitive bool) {
	r := reason
	if sensitive && r == "" {
		r = domain.CacheBypassSensitive
	}
	if r == "" {
		r = domain.CacheBypassDisabled
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats.Bypasses++
	if c.stats.BypassByReason == nil {
		c.stats.BypassByReason = map[string]int64{}
	}
	c.stats.BypassByReason[r]++
	c.lookups++
}

func hitResult(kind domain.CacheKind, key string, body []byte, meta domain.CacheHitMeta, start time.Time, sim float64) domain.CacheLookupResult {
	elapsed := msSince(start)
	saved := meta.ProviderLatencyMS
	return domain.CacheLookupResult{
		Hit: true, Kind: kind, Body: body, Key: key, Similarity: sim,
		LookupMS: elapsed, ReuseCount: meta.ReuseCount + 1, LatencySavedMS: saved,
		Meta: &meta,
		Trace: &domain.CacheTrace{
			Hit: true, Kind: string(kind), Key: key, LookupMS: elapsed,
			Similarity: sim, ReuseCount: meta.ReuseCount + 1, LatencySavedMS: saved,
		},
	}
}

// decodePayload unwraps the Phase 5 envelope, falling back to raw bodies
// written before the envelope existed.
func decodePayload(raw []byte) ([]byte, domain.CacheHitMeta, bool) {
	var p domain.CachedPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, domain.CacheHitMeta{}, false
	}
	if len(p.Body) == 0 {
		return nil, domain.CacheHitMeta{}, false
	}
	return p.Body, p.Meta, true
}

// validFor checks the stored serving context against the current request.
// A provider/model/policy/tool change turns a would-be hit into a miss
// rather than serving a stale answer.
func validFor(meta domain.CacheHitMeta, in KeyInput) bool {
	if meta.Model != "" && !equalFold(meta.Model, in.Model) {
		return false
	}
	if meta.PolicyID != "" && in.PolicyID != "" && meta.PolicyID != in.PolicyID {
		return false
	}
	if th := in.ToolsHash(); meta.ToolsHash != "" && th != "" && meta.ToolsHash != th {
		return false
	}
	if meta.EndpointID != "" && in.EndpointID != "" && meta.EndpointID != in.EndpointID {
		return false
	}
	if !meta.ExpiresAt.IsZero() && time.Now().After(meta.ExpiresAt) {
		return false
	}
	return true
}

func equalFold(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) != len(b) {
		// Compare case-insensitively without importing strings in hot path
		// callers; lengths equal is required for fold equality here.
		return foldEq(a, b)
	}
	return foldEq(a, b)
}

func foldEq(a, b string) bool {
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func msSince(t time.Time) float64 {
	return float64(time.Since(t).Microseconds()) / 1000.0
}
