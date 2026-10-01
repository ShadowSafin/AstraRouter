// Package admin implements the Phase 3 management plane: input validation for
// the admin CRUD API, encrypted provider-credential storage, and the provider
// connectivity test runner.
//
// The package holds no HTTP code and no database handles: handlers decode,
// call in here (or through the storage repositories), and encode. That keeps
// the validation, cryptography and test logic unit-testable without a server.
package admin

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/hkdf"

	"github.com/corerouter/corerouter/internal/domain"
)

// CredentialsKeyEnv names the environment variable carrying the explicit data
// key for provider-credential encryption.
const CredentialsKeyEnv = "CR_CREDENTIALS_KEY"

// keyInfo identifies the key version stamped on sealed envelopes.
const currentKeyVersion = 1

// Store seals and opens provider credentials with AES-256-GCM.
//
// One random nonce per seal means the same secret stored twice produces
// different ciphertext, so equality of stored values leaks nothing.
type Store struct {
	key []byte
}

// KeyMaterial derives the 32-byte data key.
//
// An explicit CR_CREDENTIALS_KEY wins when set (raw 32 bytes, hex, or base64).
// Otherwise the key is derived from the admin key via HKDF-SHA256, so a stock
// deployment encrypts credentials without any new configuration. Deriving
// from the admin key ties credential recovery to admin-key stability: rotate
// the admin key and previously stored secrets become unreadable, which the
// operator is told at rotation time.
func KeyMaterial(explicitKey, adminKey string) ([]byte, error) {
	if strings.TrimSpace(explicitKey) != "" {
		key, err := parseKey(strings.TrimSpace(explicitKey))
		if err != nil {
			return nil, domain.NewError(domain.ErrCodeInvalidRequest,
				"CR_CREDENTIALS_KEY is not a valid 32-byte key (raw, hex or base64)").Wrap(err)
		}
		return key, nil
	}
	if strings.TrimSpace(adminKey) == "" {
		return nil, domain.NewError(domain.ErrCodeUnavailable,
			"credential storage needs CR_CREDENTIALS_KEY or a configured admin key")
	}
	out := make([]byte, 32)
	kdf := hkdf.New(sha256.New, []byte(adminKey), nil, []byte("corerouter/provider-credentials/v1"))
	if _, err := kdf.Read(out); err != nil {
		return nil, domain.NewError(domain.ErrCodeInternal, "derive the credential key").Wrap(err)
	}
	return out, nil
}

// KeyMaterialFromEnv resolves key material from the environment, honouring an
// explicit CR_CREDENTIALS_KEY over the admin key fallback.
func KeyMaterialFromEnv(adminKeyEnv string) ([]byte, error) {
	adminKey := ""
	if adminKeyEnv != "" {
		adminKey = strings.TrimSpace(os.Getenv(adminKeyEnv))
	}
	return KeyMaterial(strings.TrimSpace(os.Getenv(CredentialsKeyEnv)), adminKey)
}

// parseKey accepts a raw 32-byte string, 64 hex characters, or base64.
func parseKey(raw string) ([]byte, error) {
	if len(raw) == 32 {
		return []byte(raw), nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(raw); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	return nil, fmt.Errorf("expected 32 raw bytes, 64 hex characters or base64 of 32 bytes")
}

// NewStore builds a sealing store over 32 bytes of key material.
func NewStore(key []byte) (*Store, error) {
	if len(key) != 32 {
		return nil, domain.NewError(domain.ErrCodeInvalidRequest,
			"credential key must be exactly 32 bytes")
	}
	cp := make([]byte, 32)
	copy(cp, key)
	return &Store{key: cp}, nil
}

// Seal encrypts a plaintext secret into a storage envelope.
func (s *Store) Seal(plaintext string) (*domain.ProviderCredential, error) {
	if s == nil {
		return nil, domain.NewError(domain.ErrCodeUnavailable, "credential store is not configured")
	}
	secret := strings.TrimSpace(plaintext)
	if secret == "" {
		return nil, domain.NewError(domain.ErrCodeInvalidRequest, "credential must not be empty")
	}
	if len(secret) > 8*1024 {
		return nil, domain.NewError(domain.ErrCodeInvalidRequest, "credential exceeds 8 KiB")
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, domain.NewError(domain.ErrCodeInternal, "initialize the credential cipher").Wrap(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, domain.NewError(domain.ErrCodeInternal, "initialize the credential cipher").Wrap(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, domain.NewError(domain.ErrCodeInternal, "generate the credential nonce").Wrap(err)
	}
	return &domain.ProviderCredential{
		Ciphertext: gcm.Seal(nil, nonce, []byte(secret), nil),
		Nonce:      nonce,
		KeyVersion: currentKeyVersion,
	}, nil
}

// Open decrypts a stored envelope. The plaintext lives only in the returned
// string; callers must not log it.
func (s *Store) Open(cred *domain.ProviderCredential) (string, error) {
	if s == nil {
		return "", domain.NewError(domain.ErrCodeUnavailable, "credential store is not configured")
	}
	if cred == nil || len(cred.Ciphertext) == 0 || len(cred.Nonce) == 0 {
		return "", domain.NewError(domain.ErrCodeInvalidRequest, "credential envelope is empty")
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", domain.NewError(domain.ErrCodeInternal, "initialize the credential cipher").Wrap(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", domain.NewError(domain.ErrCodeInternal, "initialize the credential cipher").Wrap(err)
	}
	plain, err := gcm.Open(nil, cred.Nonce, cred.Ciphertext, nil)
	if err != nil {
		return "", domain.NewError(domain.ErrCodeAuthentication, "the stored credential cannot be decrypted with the current key").Wrap(err)
	}
	return string(plain), nil
}
