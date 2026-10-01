package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// Tenant is the top-level ownership boundary. Every other durable record either
// belongs to a tenant or is global platform configuration.
type Tenant struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	// Status gates all traffic for the tenant's keys.
	Status Status `json:"status"`
	// Plan is free-form commercial metadata (e.g. "pro", "internal").
	Plan string `json:"plan,omitempty"`
	// DefaultRoutingPolicyID is applied when no request-level policy matches.
	DefaultRoutingPolicyID string `json:"default_routing_policy_id,omitempty"`
	// Labels carry operator-defined metadata for filtering and reporting.
	Labels map[string]string `json:"labels,omitempty"`
	// CreatedAt and UpdatedAt are always UTC.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Usable reports whether the tenant may currently serve traffic.
func (t *Tenant) Usable() bool { return t.Status == StatusActive }

// Scope is a permission granted to an API key. Scopes keep a key issued for an
// application from reaching administrative surface area.
type Scope string

const (
	// ScopeInference allows /v1/chat/completions and friends.
	ScopeInference Scope = "inference"
	// ScopeReadUsage allows reading own usage and request logs.
	ScopeReadUsage Scope = "usage:read"
	// ScopeReadModels allows listing the model registry.
	ScopeReadModels Scope = "models:read"
	// ScopeAdminPolicies allows managing routing policies.
	ScopeAdminPolicies Scope = "policies:admin"
	// ScopeAdminProviders allows managing provider configuration.
	ScopeAdminProviders Scope = "providers:admin"
	// ScopeAdminKeys allows minting and revoking API keys.
	ScopeAdminKeys Scope = "keys:admin"
	// ScopeAdminTenants allows cross-tenant administration.
	ScopeAdminTenants Scope = "tenants:admin"
	// ScopeAdminAll is a superuser grant for platform operators.
	ScopeAdminAll Scope = "*"
)

// ScopeSet is a set of scopes with an operator-friendly string form.
type ScopeSet map[Scope]struct{}

// NewScopeSet builds a scope set from raw strings, ignoring blanks.
func NewScopeSet(raw ...string) ScopeSet {
	s := make(ScopeSet, len(raw))
	for _, r := range raw {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		s[Scope(r)] = struct{}{}
	}
	return s
}

// Has reports whether the set grants scope. ScopeAdminAll grants everything.
func (s ScopeSet) Has(scope Scope) bool {
	if _, ok := s[ScopeAdminAll]; ok {
		return true
	}
	_, ok := s[scope]
	return ok
}

// Slice returns scopes in a stable sorted order for persistence.
func (s ScopeSet) Slice() []string {
	out := make([]string, 0, len(s))
	for sc := range s {
		out = append(out, string(sc))
	}
	sortStrings(out)
	return out
}

// APIKeyStatus is the lifecycle state of a key.
type APIKeyStatus string

const (
	APIKeyActive  APIKeyStatus = "active"
	APIKeyRevoked APIKeyStatus = "revoked"
	APIKeyExpired APIKeyStatus = "expired"
)

// APIKey is a credential presented as a bearer token on inference calls.
//
// Plaintext keys are never stored. KeyHash is a SHA-256 digest of the full
// token, and Prefix is a short non-secret excerpt used purely for display and
// for narrowing candidate rows before the constant-time hash comparison.
type APIKey struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
	// Prefix is the display form, e.g. "cr_live_7f3a".
	Prefix string `json:"prefix"`
	// KeyHash is the hex-encoded SHA-256 of the full token. Never serialized
	// to clients: the struct tag keeps it out of JSON responses.
	KeyHash string `json:"-"`
	// Scopes are the grants attached to the key.
	Scopes []string     `json:"scopes"`
	Status APIKeyStatus `json:"status"`
	// ExpiresAt is optional; a nil value means the key does not expire.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// LastUsedAt is maintained asynchronously to keep the request path fast.
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	// RoutingPolicyID optionally pins the key to a specific policy.
	RoutingPolicyID string     `json:"routing_policy_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
	// CreatedBy is the operator or key that minted this credential.
	CreatedBy string `json:"created_by,omitempty"`
}

// ScopeSet returns the key's grants as a set.
func (k *APIKey) ScopeSet() ScopeSet {
	raw := make([]string, len(k.Scopes))
	copy(raw, k.Scopes)
	return NewScopeSet(raw...)
}

// Can reports whether the key grants scope. It does not consider key status.
func (k *APIKey) Can(scope Scope) bool { return k.ScopeSet().Has(scope) }

// IsUsable reports whether the key is currently valid at the given instant.
// Expiry is evaluated lazily so a key can lapse without a scheduled job.
func (k *APIKey) IsUsable(now time.Time) bool {
	if k.Status != APIKeyActive {
		return false
	}
	if k.ExpiresAt != nil && !now.Before(*k.ExpiresAt) {
		return false
	}
	return true
}

// PrefixLength is the number of characters of the plaintext token retained for
// display. It must be long enough to identify a key in a list and short enough
// to be useless to an attacker.
const PrefixLength = 16

// HashKey returns the hex SHA-256 digest used for key lookup.
//
// A plain SHA-256 is appropriate here (rather than bcrypt/argon2) because the
// token is 256 bits of CSPRNG output, not a human-chosen password: there is no
// dictionary to attack, and the gateway must verify keys on every request
// without a deliberate key-stretching cost.
func HashKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// DisplayPrefix returns the non-secret display prefix of a token.
func DisplayPrefix(plaintext string) string {
	if len(plaintext) <= PrefixLength {
		return plaintext
	}
	return plaintext[:PrefixLength]
}

// sortStrings is a tiny insertion sort used to avoid importing sort in every
// file of this package while keeping output deterministic.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
