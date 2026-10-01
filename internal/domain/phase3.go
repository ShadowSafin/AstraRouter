package domain

import "time"

// ManagedBy records who owns a catalogue row: the configuration bootstrapper
// or an operator working through the admin API.
//
// The bootstrapper must never overwrite an api-managed row on restart.
// Without this marker, editing a provider in the dashboard would be silently
// reverted by the next deploy, which is exactly the kind of betrayal that
// makes operators stop trusting a control plane.
type ManagedBy string

const (
	// ManagedByBootstrap marks rows owned by the configuration file seeder.
	ManagedByBootstrap ManagedBy = "bootstrap"
	// ManagedByAPI marks rows created or edited through the admin API.
	ManagedByAPI ManagedBy = "api"
)

// Valid reports whether the marker is a known owner.
func (m ManagedBy) Valid() bool {
	return m == ManagedByBootstrap || m == ManagedByAPI
}

// Environment marks where a provider or model is intended to run.
//
// Routing does not branch on this value today; it is operator metadata for
// dashboards, cost attribution, and for keeping test fixtures out of the
// production fallback chain by policy match.
type Environment string

const (
	// EnvProduction serves live traffic.
	EnvProduction Environment = "production"
	// EnvInternal serves first-party internal clients.
	EnvInternal Environment = "internal"
	// EnvExternal serves third-party traffic through a gateway deployment.
	EnvExternal Environment = "external"
	// EnvTest is fixtures and shadow traffic only, never production routing.
	EnvTest Environment = "test"
)

// Valid reports whether the environment is known.
func (e Environment) Valid() bool {
	switch e {
	case EnvProduction, EnvInternal, EnvExternal, EnvTest:
		return true
	default:
		return false
	}
}

// ProviderCredential is an encrypted upstream secret stored in Postgres.
//
// The plaintext never leaves the sealed envelope except inside the adapter
// builder, which decrypts it once per catalogue refresh. API responses carry
// only metadata: whether a credential exists, its label, and when it was set.
type ProviderCredential struct {
	ProviderID string `json:"provider_id"`
	// Name is an operator label such as "primary" or "rotated 2026-09".
	Name string `json:"name"`
	// Ciphertext and Nonce are the AES-256-GCM envelope. They are byte slices
	// rather than strings so a JSON marshal can never leak them accidentally.
	Ciphertext []byte    `json:"-"`
	Nonce      []byte    `json:"-"`
	KeyVersion int       `json:"key_version"`
	CreatedBy  string    `json:"created_by,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// CredentialMeta is the safe public view of a stored credential.
type CredentialMeta struct {
	ProviderID string    `json:"provider_id"`
	Name       string    `json:"name"`
	KeyVersion int       `json:"key_version"`
	CreatedBy  string    `json:"created_by,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Meta returns the safe public view of the credential.
func (c *ProviderCredential) Meta() CredentialMeta {
	return CredentialMeta{
		ProviderID: c.ProviderID,
		Name:       c.Name,
		KeyVersion: c.KeyVersion,
		CreatedBy:  c.CreatedBy,
		CreatedAt:  c.CreatedAt,
		UpdatedAt:  c.UpdatedAt,
	}
}

// TestCheck is one step of a provider connectivity test.
type TestCheck string

const (
	// TestConnectivity opens the provider's health endpoint.
	TestConnectivity TestCheck = "connectivity"
	// TestModels lists the provider's remote models.
	TestModels TestCheck = "models"
	// TestSample runs a minimal completion through the provider.
	TestSample TestCheck = "sample"
)

// Valid reports whether the check name is known.
func (c TestCheck) Valid() bool {
	switch c {
	case TestConnectivity, TestModels, TestSample:
		return true
	default:
		return false
	}
}

// ProviderTestResult is one durable step of a provider connectivity test.
//
// Results are stored so an operator can see when a provider was last verified
// and what failed, without re-running the test.
type ProviderTestResult struct {
	ID         string    `json:"id"`
	ProviderID string    `json:"provider_id"`
	Kind       TestCheck `json:"kind"`
	Success    bool      `json:"success"`
	LatencyMS  int64     `json:"latency_ms"`
	StatusCode int       `json:"status_code,omitempty"`
	Message    string    `json:"message,omitempty"`
	// Detail carries check-specific evidence: model counts and names for the
	// models check, the completion excerpt for the sample check.
	Detail    map[string]any `json:"detail,omitempty"`
	CreatedBy string         `json:"created_by,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// Phase 3 audit enumerations. The string values must match the
// audit_events CHECK constraints (see 0004_phase3.sql).
const (
	// AuditTest records a provider connectivity test run.
	AuditTest AuditAction = "test"
	// AuditEnable records enabling a provider, model, tenant or policy.
	AuditEnable AuditAction = "enable"
	// AuditDisable records disabling a provider, model, tenant or policy.
	AuditDisable AuditAction = "disable"
	// ResourceCredential is the audit subject for credential writes.
	ResourceCredential AuditResource = "credential"
	// ResourceTestResult is the audit subject for stored test runs.
	ResourceTestResult AuditResource = "test_result"
)
