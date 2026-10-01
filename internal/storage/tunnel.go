package storage

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shadowsafin/corerouter/internal/domain"
)

// Tunnel session persistence. Rows are write-mostly and tiny: one insert at
// creation, updates on URL capture / state transitions, and reads for the
// status and history endpoints. The "single active session" invariant lives
// in the tunnel manager; the store stays a dumb, honest record keeper.

const tunnelSessionColumns = `id, COALESCE(tenant_id::text, ''), target, target_addr,
	public_url, status, started_at, url_at, stopped_at, stop_reason,
	last_error, reconnects, created_by, created_at, updated_at`

// TunnelSessionRepository stores temporary tunnel sessions.
type TunnelSessionRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewTunnelSessionRepository constructs the store.
func NewTunnelSessionRepository(pool *pgxpool.Pool, logger *slog.Logger) *TunnelSessionRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &TunnelSessionRepository{pool: pool, logger: logger}
}

// Create inserts a session in starting state.
func (r *TunnelSessionRepository) Create(ctx context.Context, s *domain.TunnelSession) (*domain.TunnelSession, error) {
	if s.ID == "" {
		s.ID = domain.NewID()
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO tunnel_sessions (id, tenant_id, target, target_addr, public_url,
			status, started_at, url_at, stopped_at, stop_reason, last_error,
			reconnects, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING `+tunnelSessionColumns,
		s.ID, nullableUUID(s.TenantID), s.Target, s.TargetAddr, s.PublicURL,
		string(s.Status), s.StartedAt, s.URLAt, s.StoppedAt, s.StopReason,
		s.LastError, s.Reconnects, s.CreatedBy)
	out, err := scanTunnelSession(row)
	if err != nil {
		return nil, wrapDBError("create tunnel session", err)
	}
	return out, nil
}

// Update rewrites a session's mutable fields by id.
func (r *TunnelSessionRepository) Update(ctx context.Context, s *domain.TunnelSession) (*domain.TunnelSession, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE tunnel_sessions
		   SET target = $2, target_addr = $3, public_url = $4, status = $5,
		       started_at = $6, url_at = $7, stopped_at = $8, stop_reason = $9,
		       last_error = $10, reconnects = $11, updated_at = now()
		 WHERE id = $1
		RETURNING `+tunnelSessionColumns,
		s.ID, s.Target, s.TargetAddr, s.PublicURL, string(s.Status),
		s.StartedAt, s.URLAt, s.StoppedAt, s.StopReason, s.LastError,
		s.Reconnects)
	out, err := scanTunnelSession(row)
	if isNotFound(err) {
		return nil, domain.NewError(domain.ErrCodeNotFound, "tunnel session not found")
	}
	if err != nil {
		return nil, wrapDBError("update tunnel session", err)
	}
	return out, nil
}

// GetByID returns a session by identifier, or nil when absent.
func (r *TunnelSessionRepository) GetByID(ctx context.Context, id string) (*domain.TunnelSession, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+tunnelSessionColumns+` FROM tunnel_sessions WHERE id = $1`, id)
	out, err := scanTunnelSession(row)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, wrapDBError("load tunnel session", err)
	}
	return out, nil
}

// Active returns the current non-terminal session, if any. At most one row
// matches because the manager never creates a second one while another is
// active; ordering by creation time makes the intent explicit regardless.
func (r *TunnelSessionRepository) Active(ctx context.Context) (*domain.TunnelSession, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+tunnelSessionColumns+` FROM tunnel_sessions
		 WHERE status IN ('starting', 'running')
		 ORDER BY created_at DESC LIMIT 1`)
	out, err := scanTunnelSession(row)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, wrapDBError("load active tunnel session", err)
	}
	return out, nil
}

// Recent returns the latest sessions, newest first.
func (r *TunnelSessionRepository) Recent(ctx context.Context, limit int) ([]domain.TunnelSession, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+tunnelSessionColumns+` FROM tunnel_sessions
		 ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, wrapDBError("list tunnel sessions", err)
	}
	defer rows.Close()
	var out []domain.TunnelSession
	for rows.Next() {
		s, err := scanTunnelSession(rows)
		if err != nil {
			return nil, wrapDBError("scan tunnel session", err)
		}
		out = append(out, *s)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("list tunnel sessions", err)
	}
	return out, nil
}

// MarkStaleStopped reconciles sessions left non-terminal by a previous
// process lifetime (crash, SIGKILL, deploy). It runs once at startup so a
// dead tunnel is never reported as running, and returns how many rows moved.
func (r *TunnelSessionRepository) MarkStaleStopped(ctx context.Context, reason string) (int, error) {
	if reason == "" {
		reason = "gateway restarted"
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE tunnel_sessions
		   SET status = 'stopped', stopped_at = now(),
		       stop_reason = $1, updated_at = now()
		 WHERE status IN ('starting', 'running')`, reason)
	if err != nil {
		return 0, wrapDBError("reconcile tunnel sessions", err)
	}
	return int(tag.RowsAffected()), nil
}

func scanTunnelSession(row interface {
	Scan(dest ...any) error
}) (*domain.TunnelSession, error) {
	var s domain.TunnelSession
	var status string
	if err := row.Scan(&s.ID, &s.TenantID, &s.Target, &s.TargetAddr,
		&s.PublicURL, &status, &s.StartedAt, &s.URLAt, &s.StoppedAt,
		&s.StopReason, &s.LastError, &s.Reconnects, &s.CreatedBy,
		&s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	s.Status = domain.TunnelStatus(status)
	return &s, nil
}
