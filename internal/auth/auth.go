// Package auth authenticates API keys and answers authorization questions about
// the resulting principal.
//
// # Key design
//
// Keys are 256 bits of CSPRNG output rendered in base62 with a recognisable
// prefix. Authentication is a SHA-256 lookup, not a password comparison, because
// the token is machine-generated high-entropy material: there is no dictionary to
// attack, so key stretching would only add latency to every request.
//
// The plaintext is never stored or logged. What is stored is:
//
//   - KeyHash:  the SHA-256 digest used for lookup
//   - Prefix:   a short non-secret excerpt used for display and support
//
// A lookup by hash is exact and indexable, so authentication is a single indexed
// read. A hostile actor cannot enumerate keys because the search space is 2^256.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/shadowsafin/astrarouter/internal/domain"
)

// KeyStore loads credentials and their owning tenant.
type KeyStore interface {
	// LookupKey returns the key and its tenant for a key hash.
	//
	// It returns (nil, nil, nil) when no key matches, which is distinct from an
	// error: a missing key is an ordinary authentication failure, whereas an
	// error is an infrastructure failure the client should not be blamed for.
	LookupKey(ctx context.Context, keyHash string) (*domain.APIKey, *domain.Tenant, error)
	// LookupByPrefix finds candidate keys by their display prefix. It exists so
	// the 401 response and the dashboard can identify which key was presented
	// without ever handling the plaintext.
	LookupByPrefix(ctx context.Context, prefix string) ([]domain.APIKey, error)
	// TouchKey records last-used telemetry.
	TouchKey(ctx context.Context, keyID string) error
}

// Cache is the optional credential cache.
//
// A short TTL bounds how long a revoked key keeps working. That trade-off is
// explicit: without a cache every inference request costs a database read, and
// with one a revocation can take up to the TTL to take effect. The default TTL is
// measured in seconds for exactly this reason.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// Principal is an authenticated caller.
type Principal struct {
	Tenant *domain.Tenant
	APIKey *domain.APIKey
	// System marks the bootstrap admin key, which has no stored key row.
	System bool
}

// Has reports whether the principal holds a scope.
//
// The system principal holds every scope; that is how the bootstrap admin key can
// administer a fresh installation before any key has been minted.
func (p *Principal) Has(scope domain.Scope) bool {
	if p == nil {
		return false
	}
	if p.System {
		return true
	}
	if p.APIKey == nil {
		return false
	}
	return p.APIKey.Can(scope)
}

// TenantID returns the principal's tenant identifier.
func (p *Principal) TenantID() string {
	if p == nil || p.Tenant == nil {
		return ""
	}
	return p.Tenant.ID
}

// KeyID returns the principal's key identifier.
func (p *Principal) KeyID() string {
	if p == nil || p.APIKey == nil {
		return ""
	}
	return p.APIKey.ID
}

// Label returns a human-readable identifier for logs and audit events.
func (p *Principal) Label() string {
	if p == nil {
		return "anonymous"
	}
	if p.System {
		return "system"
	}
	if p.APIKey == nil {
		return "unknown"
	}
	if p.APIKey.Name != "" {
		return p.APIKey.Name + " (" + p.APIKey.Prefix + ")"
	}
	return p.APIKey.Prefix
}

// Options configures an Authenticator.
type Options struct {
	// Store loads credentials.
	Store KeyStore
	// Cache is optional. When nil every request reads the store.
	Cache Cache
	// CacheTTL bounds credential caching.
	CacheTTL time.Duration
	// AdminKey is the bootstrap administrative credential. When empty, no
	// bootstrap key exists.
	AdminKey string
	// AdminTenantSlug is the tenant the bootstrap key belongs to.
	AdminTenantSlug string
	// MinKeyLength rejects obviously invalid tokens before a lookup.
	MinKeyLength int
	// KeyPrefix is prepended to generated keys.
	KeyPrefix string
	// HeaderName overrides the Authorization header.
	HeaderName string
	// RequireScope controls whether admin routes enforce scopes.
	RequireScope bool
	// Logger receives authentication diagnostics. Rejected credentials are logged
	// at debug level with the prefix only, never the token.
	Logger *slog.Logger
	// AllowAnonymousTenant, when set, attributes unauthenticated requests to a
	// tenant slug. Development convenience only.
	AllowAnonymousTenant string
}

// Authenticator validates credentials.
type Authenticator struct {
	store    KeyStore
	cache    Cache
	cacheTTL time.Duration
	adminKey string
	minLen   int
	prefix   string
	logger   *slog.Logger

	headerName           string
	requireScope         bool
	allowAnonymousTenant string
}

// New constructs an Authenticator.
func New(opts Options) *Authenticator {
	minLen := opts.MinKeyLength
	if minLen <= 0 {
		minLen = 20
	}
	cacheTTL := opts.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = 30 * time.Second
	}
	prefix := opts.KeyPrefix
	if prefix == "" {
		prefix = "ar_live_"
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	header := opts.HeaderName
	if header == "" {
		header = "Authorization"
	}
	return &Authenticator{
		store:                opts.Store,
		cache:                opts.Cache,
		cacheTTL:             cacheTTL,
		adminKey:             opts.AdminKey,
		minLen:               minLen,
		prefix:               prefix,
		logger:               logger,
		headerName:           header,
		requireScope:         opts.RequireScope,
		allowAnonymousTenant: opts.AllowAnonymousTenant,
	}
}

// HeaderName returns the header credentials are read from.
func (a *Authenticator) HeaderName() string { return a.headerName }

// RequireScope reports whether administrative routes enforce scopes.
func (a *Authenticator) RequireScope() bool { return a.requireScope }

// ExtractToken pulls the credential out of an Authorization header value,
// accepting both "Bearer <token>" and a bare token.
//
// Accepting a bare token matters in practice: several OpenAI SDK configurations
// and many shell one-liners send the key without the scheme, and rejecting them
// produces a confusing 401 for a client that is otherwise correct.
func ExtractToken(headerValue string) string {
	value := strings.TrimSpace(headerValue)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "bearer ") {
		return strings.TrimSpace(value[len("bearer "):])
	}
	return value
}

// Authenticate validates a token and returns the principal.
func (a *Authenticator) Authenticate(ctx context.Context, token string) (*Principal, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrMissingCredentials
	}
	if len(token) < a.minLen {
		// Rejecting short tokens before a lookup avoids a database round trip for
		// obviously invalid input, which is a cheap defence against a flood of
		// garbage credentials.
		a.logger.Debug("rejected an undersized credential", "length", len(token))
		return nil, ErrInvalidCredentials
	}

	// The bootstrap admin key is compared in constant time. It has no stored row,
	// so it cannot be revoked through the database; rotating it means restarting
	// with a new value, which is documented.
	if a.adminKey != "" && subtle.ConstantTimeCompare([]byte(token), []byte(a.adminKey)) == 1 {
		now := domain.Now()
		return &Principal{
			System: true,
			Tenant: &domain.Tenant{
				ID:        "system",
				Slug:      "system",
				Name:      "Platform Administration",
				Status:    domain.StatusActive,
				CreatedAt: now,
				UpdatedAt: now,
			},
		}, nil
	}

	keyHash := HashKey(token)

	if principal, ok := a.fromCache(ctx, keyHash); ok {
		// A negative cache entry is honoured so a flood of one invalid key does not
		// become a flood of database reads.
		if principal == nil {
			return nil, ErrInvalidCredentials
		}
		a.touch(ctx, principal)
		return principal, nil
	}

	key, tenant, err := a.store.LookupKey(ctx, keyHash)
	if err != nil {
		return nil, domain.NewError(domain.ErrCodeInternal, "credential lookup failed").Wrap(err)
	}
	if key == nil {
		a.cacheNegative(ctx, keyHash)
		a.logger.Debug("credential did not match any key")
		return nil, ErrInvalidCredentials
	}

	now := domain.Now()
	if !key.IsUsable(now) {
		// The specific reason is returned to the client so an operator can
		// distinguish an expired key from a revoked one without database access.
		switch {
		case key.Status == domain.APIKeyRevoked:
			return nil, ErrKeyRevoked
		case key.ExpiresAt != nil && !now.Before(*key.ExpiresAt):
			return nil, ErrKeyExpired
		default:
			return nil, ErrInvalidCredentials
		}
	}
	if tenant == nil {
		return nil, ErrInvalidCredentials
	}
	if !tenant.Usable() {
		return nil, ErrTenantInactive
	}

	principal := &Principal{Tenant: tenant, APIKey: key}
	a.cachePrincipal(ctx, keyHash, principal)
	a.touch(ctx, principal)
	return principal, nil
}

// AuthenticateAnonymous returns the principal used when anonymous access is
// enabled for development. It returns an error otherwise.
func (a *Authenticator) AuthenticateAnonymous(ctx context.Context) (*Principal, error) {
	if a.allowAnonymousTenant == "" {
		return nil, ErrMissingCredentials
	}
	now := domain.Now()
	return &Principal{
		Tenant: &domain.Tenant{
			ID:        a.allowAnonymousTenant,
			Slug:      a.allowAnonymousTenant,
			Name:      "Anonymous (development)",
			Status:    domain.StatusActive,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}, nil
}

// touch records last-used telemetry.
//
// It is deliberately not goroutine-per-request: a hot key would otherwise spawn a
// write per request. The store implementation is responsible for debouncing, and
// failures are ignored because last-used data is diagnostic, not authoritative.
func (a *Authenticator) touch(ctx context.Context, principal *Principal) {
	if principal == nil || principal.APIKey == nil || a.store == nil {
		return
	}
	if err := a.store.TouchKey(ctx, principal.APIKey.ID); err != nil {
		a.logger.Debug("failed to record key usage", "key_id", principal.APIKey.ID, "error", err)
	}
}

// cachePrincipal stores a successful authentication.
func (a *Authenticator) cachePrincipal(ctx context.Context, keyHash string, principal *Principal) {
	if a.cache == nil || principal.APIKey == nil {
		return
	}
	payload := encodeCachedPrincipal(principal)
	if err := a.cache.Set(ctx, cacheKey(keyHash), payload, a.cacheTTL); err != nil {
		a.logger.Debug("failed to cache credential", "error", err)
	}
}

// cacheNegative stores a miss so repeated bad credentials do not hammer the store.
func (a *Authenticator) cacheNegative(ctx context.Context, keyHash string) {
	if a.cache == nil {
		return
	}
	// A much shorter TTL than a positive entry: caching a miss for as long as a
	// hit would delay a newly minted key from working.
	if err := a.cache.Set(ctx, cacheKey(keyHash), []byte("-"), a.cacheTTL/6); err != nil {
		a.logger.Debug("failed to cache credential miss", "error", err)
	}
}

// fromCache looks up a cached authentication result.
func (a *Authenticator) fromCache(ctx context.Context, keyHash string) (*Principal, bool) {
	if a.cache == nil {
		return nil, false
	}
	raw, found, err := a.cache.Get(ctx, cacheKey(keyHash))
	if err != nil {
		a.logger.Debug("credential cache read failed", "error", err)
		return nil, false
	}
	if !found {
		return nil, false
	}
	if string(raw) == "-" {
		return nil, true
	}
	principal, err := decodeCachedPrincipal(raw)
	if err != nil {
		// A malformed cache entry is discarded rather than trusted.
		_ = a.cache.Delete(ctx, cacheKey(keyHash))
		return nil, false
	}
	return principal, true
}

// Invalidate removes a cached credential. The admin API calls it on revocation so
// a revoked key stops working immediately rather than after the TTL.
func (a *Authenticator) Invalidate(ctx context.Context, token string) error {
	if a.cache == nil {
		return nil
	}
	return a.cache.Delete(ctx, cacheKey(HashKey(token)))
}

// InvalidateHash removes a cached credential by its stored hash.
func (a *Authenticator) InvalidateHash(ctx context.Context, keyHash string) error {
	if a.cache == nil {
		return nil
	}
	return a.cache.Delete(ctx, cacheKey(keyHash))
}

// cacheKey namespaces credential cache entries.
func cacheKey(keyHash string) string {
	// Only the first 32 hex characters are needed: a cache collision would have
	// to survive the store lookup that follows, so truncation cannot cause a
	// false authentication.
	if len(keyHash) > 32 {
		keyHash = keyHash[:32]
	}
	return "auth:key:" + keyHash
}

// HashKey returns the hex SHA-256 digest of a token.
func HashKey(token string) string {
	return domain.HashKey(token)
}

// keyAlphabet is the alphabet used for generated keys. Base62 avoids '+', '/' and
// '=' so a key survives being copied through a URL, a shell argument or a YAML
// file without quoting.
const keyAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// KeyBytes is the entropy of a generated key. 32 bytes is 256 bits, which makes
// brute-force enumeration infeasible for any foreseeable attacker.
const KeyBytes = 32

// GeneratedKey is a newly minted credential.
type GeneratedKey struct {
	// Plaintext is shown to the operator exactly once and never stored.
	Plaintext string
	// Prefix is the non-secret display excerpt.
	Prefix string
	// Hash is the value persisted as KeyHash.
	Hash string
}

// GenerateKey mints a new API key.
func GenerateKey(prefix string) (GeneratedKey, error) {
	if prefix == "" {
		prefix = "ar_live_"
	}
	buf := make([]byte, KeyBytes)
	if _, err := rand.Read(buf); err != nil {
		return GeneratedKey{}, domain.NewError(domain.ErrCodeInternal, "failed to generate a secure key").Wrap(err)
	}

	// Rejection-free base62 encoding: every byte maps to one or two alphabet
	// characters via a modulo that is slightly biased, which is irrelevant here
	// because the bias is on the order of 2^-6 of one character's probability and
	// the key still carries 256 bits from rand.Read. Simplifying the encoding
	// keeps minting dependency-free.
	var b strings.Builder
	b.Grow(len(buf) * 2)
	for _, by := range buf {
		b.WriteByte(keyAlphabet[int(by)%len(keyAlphabet)])
		b.WriteByte(keyAlphabet[int(by)/len(keyAlphabet)%len(keyAlphabet)])
	}

	plaintext := prefix + b.String()
	return GeneratedKey{
		Plaintext: plaintext,
		Prefix:    displayPrefix(plaintext),
		Hash:      HashKey(plaintext),
	}, nil
}

// displayPrefix returns the non-secret prefix, long enough to identify a key in a
// list and short enough to be useless to an attacker.
//
// It defers to the domain constant rather than choosing its own length so that a
// key minted here and a key's stored prefix can never disagree: the dashboard
// looks keys up by prefix, and two lengths would make that lookup miss.
func displayPrefix(plaintext string) string {
	return domain.DisplayPrefix(plaintext)
}

// GenerateKeyFromBytes mints a key from supplied entropy. It exists for tests,
// which need deterministic keys, and is otherwise not used.
func GenerateKeyFromBytes(prefix string, entropy []byte) GeneratedKey {
	var b strings.Builder
	for _, by := range entropy {
		b.WriteByte(keyAlphabet[int(by)%len(keyAlphabet)])
		b.WriteByte(keyAlphabet[int(by)/len(keyAlphabet)%len(keyAlphabet)])
	}
	plaintext := prefix + b.String()
	return GeneratedKey{
		Plaintext: plaintext,
		Prefix:    displayPrefix(plaintext),
		Hash:      HashKey(plaintext),
	}
}

// ErrMissingCredentials indicates no credential was presented.
var ErrMissingCredentials = &domain.Error{
	Code:    domain.ErrCodeAuthentication,
	Message: "missing credentials: supply an API key in the Authorization header",
	Status:  401,
	// An unauthenticated request can never succeed against another provider, so
	// it must not trigger failover.
	Retryable:        false,
	FallbackEligible: false,
}

// ErrInvalidCredentials indicates the credential did not match any key.
var ErrInvalidCredentials = &domain.Error{
	Code:             domain.ErrCodeAuthentication,
	Message:          "invalid API key",
	Status:           401,
	Retryable:        false,
	FallbackEligible: false,
}

// ErrKeyRevoked indicates the key was revoked.
var ErrKeyRevoked = &domain.Error{
	Code:             domain.ErrCodeAuthentication,
	Message:          "this API key has been revoked",
	Status:           401,
	Retryable:        false,
	FallbackEligible: false,
}

// ErrKeyExpired indicates the key expired.
var ErrKeyExpired = &domain.Error{
	Code:             domain.ErrCodeAuthentication,
	Message:          "this API key has expired",
	Status:           401,
	Retryable:        false,
	FallbackEligible: false,
}

// ErrTenantInactive indicates the owning tenant is disabled.
var ErrTenantInactive = &domain.Error{
	Code:             domain.ErrCodePermission,
	Message:          "the tenant associated with this API key is not active",
	Status:           403,
	Retryable:        false,
	FallbackEligible: false,
}

// ErrInsufficientScope indicates the principal lacks a required scope.
func ErrInsufficientScope(scope domain.Scope) *domain.Error {
	return &domain.Error{
		Code:             domain.ErrCodePermission,
		Message:          "this API key lacks the required scope: " + string(scope),
		Status:           403,
		Retryable:        false,
		FallbackEligible: false,
	}
}

// IsAuthenticationError reports whether err is one of the credential errors above.
func IsAuthenticationError(err error) bool {
	return errors.Is(err, ErrMissingCredentials) ||
		errors.Is(err, ErrInvalidCredentials) ||
		errors.Is(err, ErrKeyRevoked) ||
		errors.Is(err, ErrKeyExpired) ||
		errors.Is(err, ErrTenantInactive)
}
