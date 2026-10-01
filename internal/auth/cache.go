package auth

import (
	"encoding/json"

	"github.com/shadowsafin/corerouter/internal/domain"
)

// cachedPrincipal is the wire form of a cached authentication result.
//
// The tenant and key are stored as their domain JSON forms. KeyHash is excluded
// by its struct tag, so a leaked cache entry cannot be replayed to look up
// another key, and the plaintext token is never present in the first place.
type cachedPrincipal struct {
	Tenant *domain.Tenant `json:"tenant"`
	APIKey *domain.APIKey `json:"api_key"`
	// Version allows the cache payload to change shape without an operator having
	// to flush Redis: an entry with an unexpected version is discarded.
	Version int `json:"v"`
}

// cachedPrincipalVersion is the current cache payload version.
const cachedPrincipalVersion = 1

// encodeCachedPrincipal serializes a principal for caching.
//
// A marshalling failure cannot lose authentication: the encoder returns nil and
// the caller skips the cache write, leaving the next request to read the store.
func encodeCachedPrincipal(p *Principal) []byte {
	if p == nil || p.APIKey == nil {
		return nil
	}
	raw, err := json.Marshal(cachedPrincipal{
		Tenant:  p.Tenant,
		APIKey:  p.APIKey,
		Version: cachedPrincipalVersion,
	})
	if err != nil {
		return nil
	}
	return raw
}

// decodeCachedPrincipal deserializes a cached principal.
func decodeCachedPrincipal(raw []byte) (*Principal, error) {
	var entry cachedPrincipal
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, err
	}
	if entry.Version != cachedPrincipalVersion || entry.APIKey == nil {
		return nil, errStaleCacheEntry
	}
	return &Principal{Tenant: entry.Tenant, APIKey: entry.APIKey}, nil
}

// errStaleCacheEntry indicates a cache payload that must be refetched.
var errStaleCacheEntry = &domain.Error{
	Code:    domain.ErrorCode("stale_cache_entry"),
	Message: "cached credential entry is not usable",
}
