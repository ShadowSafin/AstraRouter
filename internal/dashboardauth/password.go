// Package dashboardauth authenticates human operators of the console.
//
// # Why a separate credential type
//
// The gateway already has API keys, and it would be tempting to reuse them here.
// That would be wrong in both directions: an API key is 256 bits of CSPRNG output
// verified by digest lookup, which is fast *because* there is no dictionary to
// attack. A chosen password has exactly the opposite profile, and verifying it
// with a fast hash would make the stored digest cheap to crack offline. So this
// package stores an Argon2id hash and accepts the cost on every login, which is
// also what makes a slow, deliberate brute force the only way in.
//
// # Why sessions are opaque
//
// A session is 32 bytes of CSPRNG output whose SHA-256 is stored. The token is
// only ever in the client cookie. A signed token (HMAC) would avoid a database
// lookup per request but would keep validity coupled to a single signing secret,
// and logging out would have no server-side record to revoke against. Revocation
// that leaves no trace is not revocation.
package dashboardauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters.
//
// The memory figure follows the OWASP password-storage recommendation of 19 MiB
// and two iterations. Three tuning parameters would allow a deployment to weaken
// hashing by accident, which is a far more likely outcome than one that needs
// more work: a smaller m would turn this into a crackable digest, a larger one
// would make login unresponsive on a small instance.
const (
	argonTime    uint32 = 2
	argonMemory  uint32 = 19 * 1024
	argonThreads uint8  = 2
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

// ErrInvalidHash is returned when a stored hash cannot be parsed.
var ErrInvalidHash = errors.New("stored password hash is not a valid argon2id PHC string")

// HashPassword derives an Argon2id hash and returns it in PHC string format:
//
//	$argon2id$v=19$m=19456,t=2,p=2$<salt>$<hash>
//
// The encoded parameters are what make the hash self-describing: raising the work
// factor later still verifies old hashes, because each one carries the cost it
// was created with.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	sum := argon2.IDKey(
		[]byte(password),
		salt,
		argonTime,
		argonMemory,
		argonThreads,
		argonKeyLen,
	)

	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemory,
		argonTime,
		argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum),
	), nil
}

// VerifyPassword reports whether password matches the stored PHC hash.
//
// The comparison inside argon2 is constant time. A malformed hash still runs a
// verification against a dummy digest rather than returning early, so a corrupt
// row costs the same as a wrong password and cannot be distinguished by timing.
func VerifyPassword(password, encoded string) (bool, error) {
	memory, timeCost, threads, salt, want, err := decodeHash(encoded)
	if err != nil {
		// Still spend the time. Returning early here would make a corrupted row
		// measurably faster than a wrong password.
		argon2.IDKey([]byte(password), []byte("invalid-salt-value"), argonTime, argonMemory, argonThreads, argonKeyLen)
		return false, err
	}

	got := argon2.IDKey([]byte(password), salt, timeCost, memory, threads, argonKeyLen)
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// decodeHash parses a PHC-formatted Argon2id string.
func decodeHash(encoded string) (memory, timeCost uint32, threads uint8, salt, hash []byte, err error) {
	parts := strings.Split(encoded, "$")
	// A leading empty element comes from the opening '$', so six parts is correct.
	if len(parts) != 6 || parts[0] != "" {
		return 0, 0, 0, nil, nil, ErrInvalidHash
	}
	if parts[1] != "argon2id" {
		return 0, 0, 0, nil, nil, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return 0, 0, 0, nil, nil, ErrInvalidHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil {
		return 0, 0, 0, nil, nil, ErrInvalidHash
	}
	// A stored hash with an absurd work factor would be a denial of service
	// against ourselves, since verifying it consumes that much memory.
	if memory > 1<<20 || timeCost > 64 || threads == 0 || threads > 64 {
		return 0, 0, 0, nil, nil, ErrInvalidHash
	}

	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return 0, 0, 0, nil, nil, ErrInvalidHash
	}
	if hash, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return 0, 0, 0, nil, nil, ErrInvalidHash
	}
	if len(salt) == 0 || len(hash) == 0 {
		return 0, 0, 0, nil, nil, ErrInvalidHash
	}
	return memory, timeCost, threads, salt, hash, nil
}

// HashSessionToken returns the stored representation of a session token.
//
// SHA-256 rather than Argon2 is correct here and would be wrong for a password:
// the input is 32 bytes of CSPRNG output, so there is nothing to guess. It is a
// lookup key, and making it slow would add latency to every authenticated page
// load for no security gain.
func HashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawStdEncoding.EncodeToString(sum[:])
}

// GenerateSessionToken returns a fresh opaque session token.
func GenerateSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	// base64url so the value is cookie-safe without escaping.
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ---------------------------------------------------------------------------
// Username and password rules
// ---------------------------------------------------------------------------

// NormalizeUsername lowercases and trims a username.
//
// Case folding at the boundary is what makes "Admin" and "admin" one account
// rather than two, which is what an operator expects from a login field.
func NormalizeUsername(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// PasswordRules reports whether a password is acceptable, and why not when it is
// not.
//
// The checks are length-first and deliberately shallow: no composition rules
// ("one uppercase, one symbol") and no dictionary check. Length is what actually
// resists offline cracking, and composition rules mostly produce predictable
// substitutions. A breached-password list would be a genuine improvement, but it
// needs a maintained corpus, so it is left as a clear seam rather than a stub
// that pretends to check.
type PasswordRules struct {
	MinLength int
	MaxLength int
}

// DefaultPasswordRules is the enforced policy.
func DefaultPasswordRules() PasswordRules {
	return PasswordRules{MinLength: 12, MaxLength: 256}
}

// ValidateUsername checks a username against the stored constraint.
func ValidateUsername(raw string) error {
	normalized := NormalizeUsername(raw)
	if len(normalized) < 3 || len(normalized) > 32 {
		return errors.New("username must be 3 to 32 characters")
	}
	for i := 0; i < len(normalized); i++ {
		c := normalized[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == '-':
		default:
			return errors.New("username may contain only lowercase letters, digits, dot, underscore and hyphen")
		}
	}
	if normalized[0] < 'a' || (normalized[0] > 'z' && normalized[0] < '0') {
		return errors.New("username must start with a letter or digit")
	}
	return nil
}

// ValidatePassword enforces the length policy and rejects the passwords that make
// a length rule worthless.
func ValidatePassword(password string, rules PasswordRules) error {
	if rules.MinLength == 0 {
		rules = DefaultPasswordRules()
	}
	// Count runes, not bytes: a passphrase written in a non-Latin script should
	// not be rejected for being "short" in bytes while long in characters.
	length := len([]rune(password))

	switch {
	case length < rules.MinLength:
		return fmt.Errorf("password must be at least %d characters", rules.MinLength)
	case length > rules.MaxLength:
		// The cap bounds Argon2 input. 256 is far above any real passphrase and far
		// below the point where hashing is measurable.
		return fmt.Errorf("password must be at most %d characters", rules.MaxLength)
	}

	if strings.TrimSpace(password) == "" {
		return errors.New("password must not be only whitespace")
	}
	if isCommonPassword(password) {
		return errors.New("this password is too common; choose something a stranger could not guess")
	}
	if isSingleCharacterClass(password) {
		return errors.New("password must mix letters, digits or symbols")
	}
	return nil
}

// commonPasswordStems are the openings of passwords that satisfy a length rule
// while being among the first things any attacker tries.
//
// The rule is a prefix match rather than an exact one. Exact matching only
// catches the literal values listed, so "password" is rejected while
// "password123456" sails through — which is the same password with a suffix and
// precisely what an attacker tries next. A prefix also catches the decorated
// forms people actually choose: "password!", "Password1", "qwerty2024".
//
// The stems are kept few and unambiguous so the rule does not start rejecting
// legitimate passphrases. An exact-match corpus check would be better still, but
// it needs a maintained breach list, so it is left as a clear seam rather than a
// stub that pretends to check.
var commonPasswordStems = []string{
	"password", "passwd", "qwerty", "letmein", "welcome",
	"administrator", "admin", "synapass", "changeme", "default",
	"iloveyou", "trustno", "sunshine", "abc123", "123456", "654321",
}

func isCommonPassword(password string) bool {
	lower := strings.ToLower(password)
	for _, stem := range commonPasswordStems {
		if strings.HasPrefix(lower, stem) {
			return true
		}
	}
	return false
}

// isSingleCharacterClass reports whether the password uses only one class of
// character, which a length requirement can otherwise be satisfied by padding
// ("aaaaaaaaaaaaaaaa").
func isSingleCharacterClass(password string) bool {
	var hasLetter, hasDigit, hasSymbol bool
	for _, r := range password {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		default:
			hasSymbol = true
		}
	}
	classes := 0
	for _, present := range []bool{hasLetter, hasDigit, hasSymbol} {
		if present {
			classes++
		}
	}
	return classes < 2
}
