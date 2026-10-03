package cost

import (
	"sort"
	"sync"
	"time"

	"github.com/shadowsafin/synapass/internal/domain"
)

// scopeRank fixes resolution precedence: a tenant-negotiated sheet always
// wins over a model sheet, which wins over a provider sheet, which wins over
// the global default. Ranked once here so storage queries and in-memory
// resolution cannot disagree.
func scopeRank(s domain.PricingScope) int {
	switch s {
	case domain.PricingScopeTenant:
		return 4
	case domain.PricingScopeModel:
		return 3
	case domain.PricingScopeProvider:
		return 2
	case domain.PricingScopeGlobal:
		return 1
	default:
		return 0
	}
}

// Resolve picks the single version that prices a request at instant at from a
// candidate set. Candidates must already be scoped to this request (the right
// tenant, model, provider, plus the global); resolution then takes the
// highest-precedence scope whose sheet is effective at at, breaking ties by
// latest EffectiveFrom so a same-scope reissue supersedes cleanly.
//
// It returns nil when nothing is effective — the caller then falls back to
// the static registry price, which is itself a meaningful answer (an
// unpriced local runtime genuinely costs nothing) rather than an error.
func Resolve(versions []domain.PricingVersion, at time.Time) *domain.PricingVersion {
	var best *domain.PricingVersion
	bestRank := 0
	for i := range versions {
		v := &versions[i]
		if !v.Scope.Valid() || !v.EffectiveAt(at) {
			continue
		}
		rank := scopeRank(v.Scope)
		if best == nil || rank > bestRank ||
			(rank == bestRank && v.EffectiveFrom.After(best.EffectiveFrom)) {
			best = v
			bestRank = rank
		}
	}
	return best
}

// PriceForVersion converts a resolved version into the engine's Price,
// carrying provenance so the breakdown records exactly which sheet billed.
func PriceForVersion(v *domain.PricingVersion) Price {
	return Price{
		InputPerM:        v.InputCostPerMillion,
		OutputPerM:       v.OutputCostPerMillion,
		CachedInputPerM:  v.CachedInputCostPerMillion,
		BaseFeeUSD:       v.BaseFeeUSD,
		Currency:         currencyOrUSD(v.Currency),
		PricingVersionID: v.ID,
		Source:           string(v.Scope),
	}
}

func currencyOrUSD(c string) string {
	if c == "" {
		return "USD"
	}
	return c
}

// PriceCache is a small TTL cache for resolved prices on the request path.
// Versioned pricing must not add a database read to every inference request,
// so resolutions are memoized by tenant/model/provider minute-bucket: price
// sheets change on human timescales, and a stale read resolves itself within
// the TTL. The cache never invents prices — a miss returns "no version" and
// the caller falls back to registry pricing.
type PriceCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]priceEntry
}

type priceEntry struct {
	versions []domain.PricingVersion
	at       time.Time
}

// NewPriceCache builds a cache with the given entry TTL.
func NewPriceCache(ttl time.Duration) *PriceCache {
	if ttl <= 0 {
		ttl = time.Minute
	}
	return &PriceCache{ttl: ttl, entries: map[string]priceEntry{}}
}

// Key scopes one resolution: tenant, model row, provider, and the request's
// minute, so an effectiveness boundary mid-minute cannot serve a stale sheet
// for longer than the TTL.
func PriceKey(tenantID, modelID, providerID string, at time.Time) string {
	return tenantID + "\x00" + modelID + "\x00" + providerID + "\x00" +
		at.UTC().Truncate(time.Minute).Format(time.RFC3339)
}

// Get returns cached candidates for key, or nil on miss/expiry.
func (c *PriceCache) Get(key string, now time.Time) []domain.PricingVersion {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || now.Sub(e.at) > c.ttl {
		delete(c.entries, key)
		return nil
	}
	return e.versions
}

// Set memoizes candidates for key.
func (c *PriceCache) Set(key string, versions []domain.PricingVersion, now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Opportunistic eviction keeps a long-lived gateway from growing the map
	// without bound; expired entries are simply never read again.
	for k, e := range c.entries {
		if now.Sub(e.at) > c.ttl {
			delete(c.entries, k)
		}
	}
	c.entries[key] = priceEntry{versions: versions, at: now}
}

// SortVersions orders versions for deterministic API output: scope rank
// descending, then most recently effective first.
func SortVersions(versions []domain.PricingVersion) {
	sort.Slice(versions, func(i, j int) bool {
		ri, rj := scopeRank(versions[i].Scope), scopeRank(versions[j].Scope)
		if ri != rj {
			return ri > rj
		}
		return versions[i].EffectiveFrom.After(versions[j].EffectiveFrom)
	})
}
