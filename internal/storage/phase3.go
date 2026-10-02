package storage

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shadowsafin/astrarouter/internal/domain"
)

// CredentialRepository stores encrypted provider credentials.
//
// The repository is deliberately dumb about cryptography: it persists opaque
// envelopes and returns them unchanged. Sealing and opening happen in the
// admin service, so a change of cipher never touches SQL.
type CredentialRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewCredentialRepository builds the repository. It is also constructed inside
// NewRepositories; the constructor exists for tests that build stores piecemeal.
func NewCredentialRepository(pool *pgxpool.Pool, logger *slog.Logger) *CredentialRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &CredentialRepository{pool: pool, logger: logger}
}

// Set stores or replaces a provider's credential envelope.
func (r *CredentialRepository) Set(ctx context.Context, cred *domain.ProviderCredential) (*domain.CredentialMeta, error) {
	if cred.ProviderID == "" {
		return nil, domain.NewError(domain.ErrCodeInvalidRequest, "provider_id is required")
	}
	if len(cred.Ciphertext) == 0 || len(cred.Nonce) == 0 {
		return nil, domain.NewError(domain.ErrCodeInvalidRequest, "credential envelope is empty")
	}
	if cred.Name == "" {
		cred.Name = "default"
	}
	if cred.KeyVersion <= 0 {
		cred.KeyVersion = 1
	}
	if cred.CreatedAt.IsZero() {
		cred.CreatedAt = domain.Now()
	}

	var meta domain.CredentialMeta
	err := r.pool.QueryRow(ctx, `
		INSERT INTO provider_credentials (provider_id, name, ciphertext, nonce, key_version, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (provider_id) DO UPDATE
			SET name = EXCLUDED.name,
			    ciphertext = EXCLUDED.ciphertext,
			    nonce = EXCLUDED.nonce,
			    key_version = EXCLUDED.key_version,
			    created_by = EXCLUDED.created_by,
			    updated_at = now()
		RETURNING provider_id, name, key_version, created_by, created_at, updated_at`,
		cred.ProviderID, cred.Name, cred.Ciphertext, cred.Nonce, cred.KeyVersion,
		cred.CreatedBy).Scan(&meta.ProviderID, &meta.Name, &meta.KeyVersion,
		&meta.CreatedBy, &meta.CreatedAt, &meta.UpdatedAt)
	if err != nil {
		return nil, wrapDBError("store provider credential", err)
	}
	return &meta, nil
}

// Get returns the stored envelope, or nil when the provider has none.
func (r *CredentialRepository) Get(ctx context.Context, providerID string) (*domain.ProviderCredential, error) {
	var cred domain.ProviderCredential
	err := r.pool.QueryRow(ctx, `
		SELECT provider_id, name, ciphertext, nonce, key_version, created_by,
		       created_at, updated_at
		  FROM provider_credentials
		 WHERE provider_id = $1`, providerID).Scan(
		&cred.ProviderID, &cred.Name, &cred.Ciphertext, &cred.Nonce,
		&cred.KeyVersion, &cred.CreatedBy, &cred.CreatedAt, &cred.UpdatedAt)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, wrapDBError("load provider credential", err)
	}
	return &cred, nil
}

// Meta returns the safe public view of the stored credential, or nil.
func (r *CredentialRepository) Meta(ctx context.Context, providerID string) (*domain.CredentialMeta, error) {
	cred, err := r.Get(ctx, providerID)
	if err != nil || cred == nil {
		return nil, err
	}
	meta := cred.Meta()
	return &meta, nil
}

// PresentIDs returns the set of providers holding a stored credential, so the
// admin list can flag them without a query per row.
func (r *CredentialRepository) PresentIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := r.pool.Query(ctx, `SELECT provider_id::text FROM provider_credentials`)
	if err != nil {
		return nil, wrapDBError("list credential owners", err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, wrapDBError("scan credential owner", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("list credential owners", err)
	}
	return out, nil
}

// Delete removes a provider's stored credential.
func (r *CredentialRepository) Delete(ctx context.Context, providerID string) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM provider_credentials WHERE provider_id = $1`, providerID)
	if err != nil {
		return wrapDBError("delete provider credential", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrCodeNotFound, "provider credential not found")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Provider test results
// ---------------------------------------------------------------------------

// TestResultRepository stores provider connectivity test runs.
type TestResultRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewTestResultRepository builds the repository.
func NewTestResultRepository(pool *pgxpool.Pool, logger *slog.Logger) *TestResultRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &TestResultRepository{pool: pool, logger: logger}
}

// Insert records one test step.
func (r *TestResultRepository) Insert(ctx context.Context, res *domain.ProviderTestResult) error {
	if res.ID == "" {
		res.ID = domain.NewID()
	}
	if res.CreatedAt.IsZero() {
		res.CreatedAt = domain.Now()
	}
	detail, err := json.Marshal(orEmptyMapAny(res.Detail))
	if err != nil {
		return wrapDBError("encode test detail", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO provider_test_results (id, provider_id, kind, success, latency_ms,
			status_code, message, detail, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		res.ID, res.ProviderID, string(res.Kind), res.Success, res.LatencyMS,
		res.StatusCode, res.Message, detail, res.CreatedBy)
	if err != nil {
		return wrapDBError("record provider test result", err)
	}
	return nil
}

// ListRecent returns a provider's latest test steps, newest first.
func (r *TestResultRepository) ListRecent(ctx context.Context, providerID string, limit int) ([]domain.ProviderTestResult, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, provider_id::text, kind, success, latency_ms, status_code,
		       message, detail, created_by, created_at
		  FROM provider_test_results
		 WHERE provider_id = $1
		 ORDER BY created_at DESC
		 LIMIT $2`, providerID, limit)
	if err != nil {
		return nil, wrapDBError("list provider test results", err)
	}
	defer rows.Close()

	var out []domain.ProviderTestResult
	for rows.Next() {
		var (
			res    domain.ProviderTestResult
			kind   string
			detail []byte
		)
		if err := rows.Scan(&res.ID, &res.ProviderID, &kind, &res.Success,
			&res.LatencyMS, &res.StatusCode, &res.Message, &detail,
			&res.CreatedBy, &res.CreatedAt); err != nil {
			return nil, wrapDBError("scan provider test result", err)
		}
		res.Kind = domain.TestCheck(kind)
		if len(detail) > 0 {
			_ = json.Unmarshal(detail, &res.Detail)
		}
		out = append(out, res)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("list provider test results", err)
	}
	return out, nil
}

// orEmptyMapAny renders a nil detail map as an empty object.
func orEmptyMapAny(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
