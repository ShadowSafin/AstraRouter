// Package tunnel manages temporary public tunnels (Cloudflare quick tunnels).
//
// A quick tunnel mints a disposable *.trycloudflare.com URL that proxies to
// one local AstraRouter service — no port forwarding, no DNS, no account. The
// manager owns exactly one cloudflared child process: it spawns it, captures
// the public URL from its log output, supervises it, and tears it down on
// request or shutdown. Session rows in PostgreSQL are the audit trail.
//
// The package is deliberately isolated from routing, providers and storage:
// it depends on domain for session types and on a narrow Store interface that
// the storage layer satisfies, so it stays unit-testable with a fake binary
// and a fake store.
package tunnel

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/shadowsafin/astrarouter/internal/domain"
)

// Store persists tunnel sessions. *storage.TunnelSessionRepository satisfies
// it unmodified; tests use an in-memory fake.
type Store interface {
	Create(ctx context.Context, s *domain.TunnelSession) (*domain.TunnelSession, error)
	Update(ctx context.Context, s *domain.TunnelSession) (*domain.TunnelSession, error)
	GetByID(ctx context.Context, id string) (*domain.TunnelSession, error)
	Active(ctx context.Context) (*domain.TunnelSession, error)
	Recent(ctx context.Context, limit int) ([]domain.TunnelSession, error)
	MarkStaleStopped(ctx context.Context, reason string) (int, error)
}

// Config tunes the manager. It mirrors config.TunnelConfig without importing
// the config package, so the tunnel package has no deployment opinions.
type Config struct {
	// Binary is the cloudflared executable (path or PATH name).
	Binary string
	// DefaultTarget names the target used when a request is empty.
	DefaultTarget string
	// DashboardTarget is the local address behind the "dashboard" target.
	DashboardTarget string
	// AllowCustomTargets permits explicit loopback host:port targets.
	AllowCustomTargets bool
	// AutoRestart respawns an unexpectedly-exited process under the same
	// session, counting reconnects.
	AutoRestart bool
	// StartupTimeout bounds the wait for the public URL after spawn.
	StartupTimeout time.Duration
}

// Event is a lifecycle transition published to the optional OnEvent hook
// (metrics, NATS and logs are attached by the caller, not by this package).
type Event struct {
	// Kind is created, url, stopped or failed.
	Kind    string
	Session *domain.TunnelSession
}

// quickURL matches the public URL cloudflared prints, e.g.
// "https://steel-pandas-happen.trycloudflare.com". Both current suffixes are
// accepted so a Cloudflare-side rename does not silently break capture.
var quickURL = regexp.MustCompile(`https://[A-Za-z0-9-]+\.(trycloudflare\.com|cfargotunnel\.com)`)

// Manager owns the single active tunnel process.
type Manager struct {
	cfg         Config
	gatewayAddr string
	store       Store
	logger      *slog.Logger
	onEvent     func(Event)

	mu     sync.Mutex
	active *domain.TunnelSession
	cmd    *exec.Cmd
	// cancel stops supervision (operator stop or shutdown). It is non-nil
	// only while a process is supervised.
	cancel context.CancelFunc
	// stoppedByOperator distinguishes an intentional stop from a crash in the
	// supervisor, which decides between a terminal row and a restart.
	stoppedByOperator bool
}

// NewManager builds a manager. gatewayAddr is the local address behind the
// "gateway" target (e.g. http://127.0.0.1:8080); a store may be nil, in which
// case sessions are tracked in memory only.
func NewManager(cfg Config, gatewayAddr string, store Store, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Binary == "" {
		cfg.Binary = "cloudflared"
	}
	if cfg.DefaultTarget == "" {
		cfg.DefaultTarget = domain.TunnelTargetGateway
	}
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = 60 * time.Second
	}
	return &Manager{cfg: cfg, gatewayAddr: gatewayAddr, store: store, logger: logger}
}

// SetOnEvent attaches the lifecycle hook. It is called synchronously from the
// manager; the hook must not call back into the manager.
func (m *Manager) SetOnEvent(hook func(Event)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onEvent = hook
}

// BinaryAvailable reports the resolved cloudflared path, or an actionable
// error telling the operator how to provide it.
func (m *Manager) BinaryAvailable() (string, error) {
	path, err := exec.LookPath(m.cfg.Binary)
	if err != nil {
		return "", domain.NewError(domain.ErrCodeNotFound,
			"cloudflared is not installed or not on PATH (tunnel.binary="+m.cfg.Binary+") "+
				"; install it from https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/ "+
				"or set tunnel.binary to its absolute path")
	}
	return path, nil
}

// ResolveTarget maps a requested target to the local address cloudflared
// proxies to. Named targets come from configuration; anything else must be an
// explicit loopback host:port, otherwise the tunnel could be pointed at
// infrastructure AstraRouter does not own.
func (m *Manager) ResolveTarget(target string) (name, addr string, err error) {
	if target == "" {
		target = m.cfg.DefaultTarget
	}
	switch target {
	case domain.TunnelTargetGateway:
		if m.gatewayAddr == "" {
			return "", "", domain.NewError(domain.ErrCodeInternal,
				`the "gateway" tunnel target has no local address configured`)
		}
		return domain.TunnelTargetGateway, m.gatewayAddr, nil
	case domain.TunnelTargetDashboard:
		dash := m.cfg.DashboardTarget
		if dash == "" {
			dash = "http://127.0.0.1:3000"
		}
		return domain.TunnelTargetDashboard, dash, nil
	default:
		if !m.cfg.AllowCustomTargets {
			return "", "", domain.NewError(domain.ErrCodePermission,
				"custom tunnel targets are disabled; use \"gateway\" or \"dashboard\"")
		}
		host, port, ok := splitHostPort(target)
		if !ok || !isLoopbackHost(host) || !validPort(port) {
			return "", "", domain.NewError(domain.ErrCodeInvalidRequest,
				"tunnel target "+quote(target)+" is not \"gateway\", \"dashboard\" or a loopback host:port")
		}
		return target, "http://" + host + ":" + port, nil
	}
}

// Status returns a copy of the active session, or nil when nothing is
// exposed. The copy is what keeps callers from mutating manager state.
func (m *Manager) Status() *domain.TunnelSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		return nil
	}
	cp := *m.active
	return &cp
}

// ParseURL extracts the first quick-tunnel URL from one output line. It is a
// package-level function so the capture rule is unit-testable without a
// process.
func ParseURL(line string) string {
	return quickURL.FindString(line)
}

// Create spawns a tunnel for target and waits for the public URL.
//
// Exactly one session may be active: a second create while one is running or
// starting fails with a 409, and tells the caller to stop or restart instead.
// The returned session is running; a failure to capture the URL fails the
// session row and returns the underlying reason.
func (m *Manager) Create(ctx context.Context, target, tenantID, createdBy string) (*domain.TunnelSession, error) {
	name, addr, err := m.ResolveTarget(target)
	if err != nil {
		return nil, err
	}
	binary, err := m.BinaryAvailable()
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.active != nil && !m.active.Status.Terminal() {
		active := *m.active
		m.mu.Unlock()
		dup := domain.NewError(domain.ErrCodeInvalidRequest,
			"a tunnel is already "+string(active.Status)+" ("+active.PublicURL+"); "+
				"stop it before creating another, or restart it")
		dup.Status = 409
		return nil, dup
	}
	m.mu.Unlock()

	now := domain.Now()
	session := &domain.TunnelSession{
		TenantID: tenantID, Target: name, TargetAddr: addr,
		Status: domain.TunnelStarting, StartedAt: &now,
		CreatedBy: createdBy,
	}
	if m.store != nil {
		saved, err := m.store.Create(ctx, session)
		if err != nil {
			return nil, err
		}
		session = saved
	} else if session.ID == "" {
		session.ID = domain.NewID()
	}

	if err := m.spawn(ctx, session, binary); err != nil {
		return nil, err
	}
	m.emit(Event{Kind: "created", Session: m.Status()})
	return m.Status(), nil
}

// spawn starts cloudflared and blocks until the URL is captured or the
// startup timeout elapses. The supervisor goroutine it starts owns the rest
// of the process lifetime.
func (m *Manager) spawn(ctx context.Context, session *domain.TunnelSession, binary string) error {
	args := []string{"tunnel", "--no-autoupdate", "--url", session.TargetAddr}
	cmd := exec.Command(binary, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		m.failSession(ctx, session, "could not capture tunnel output: "+err.Error())
		return domain.NewError(domain.ErrCodeInternal, "could not start the tunnel process").Wrap(err)
	}
	// cloudflared logs to stderr; stdout is merged so no output is ever lost
	// to a closed pipe.
	cmd.Stdout = cmd.Stderr
	if err := cmd.Start(); err != nil {
		m.failSession(ctx, session, "cloudflared failed to start: "+err.Error())
		return domain.NewError(domain.ErrCodeUpstream,
			"cloudflared failed to start: "+err.Error())
	}

	sctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	// A second create cannot interleave here: Create holds no lock across the
	// DB write, but it re-checks m.active under this same lock.
	if m.active != nil && !m.active.Status.Terminal() && m.active.ID != session.ID {
		m.mu.Unlock()
		cancel()
		_ = cmd.Process.Kill()
		dup := domain.NewError(domain.ErrCodeInvalidRequest,
			"a tunnel became active while this one was starting; stop it first, or restart")
		dup.Status = 409
		return dup
	}
	m.cmd = cmd
	m.cancel = cancel
	m.stoppedByOperator = false
	m.active = session
	m.mu.Unlock()

	urlCh := make(chan string, 1)
	errCh := make(chan string, 1)
	tail := &lineRing{limit: 20}
	go func() {
		scanner := bufio.NewScanner(stderr)
		// A log line longer than the default 64k buffer must not kill the
		// supervisor; grow the buffer instead of dropping the process.
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			tail.add(line)
			if url := ParseURL(line); url != "" {
				select {
				case urlCh <- url:
				default:
				}
			}
			// Keep only the tail in memory, but surface process errors in
			// logs as they happen (URLs excluded: they are minted values,
			// safe to log, but noisy at info).
			if strings.HasPrefix(line, "ERR") || strings.Contains(line, " failed ") {
				m.logger.Warn("tunnel process output", "session", session.ID, "line", line)
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case errCh <- err.Error():
			default:
			}
		}
	}()

	go m.supervise(sctx, session.ID, binary, tail)

	timeout := m.cfg.StartupTimeout
	select {
	case url := <-urlCh:
		return m.markRunning(ctx, session.ID, url)
	case <-time.After(timeout):
		m.terminateLocked("startup timeout waiting for the public URL: " + tail.joined("; "))
		m.failSession(ctx, session, "cloudflared did not print a public URL within "+timeout.String())
		return domain.NewError(domain.ErrCodeTimeout,
			"cloudflared did not print a public URL within "+timeout.String()+
				"; is outbound HTTPS to Cloudflare reachable?")
	case <-ctx.Done():
		m.terminateLocked("creation cancelled")
		m.failSession(ctx, session, "creation cancelled")
		return ctx.Err()
	}
}

// markRunning records the captured URL and flips the session to running.
func (m *Manager) markRunning(ctx context.Context, id, url string) error {
	now := domain.Now()
	m.mu.Lock()
	if m.active == nil || m.active.ID != id {
		m.mu.Unlock()
		return domain.NewError(domain.ErrCodeInternal, "the tunnel session changed while starting")
	}
	m.active.PublicURL = url
	m.active.URLAt = &now
	m.active.Status = domain.TunnelRunning
	m.active.LastError = ""
	cp := *m.active
	m.mu.Unlock()

	if m.store != nil {
		if _, err := m.store.Update(ctx, &cp); err != nil {
			m.logger.Warn("failed to persist the running tunnel session",
				"session", id, "error", err)
		}
	}
	m.logger.Info("tunnel is public", "session", id, "url", url, "target", cp.TargetAddr)
	m.emit(Event{Kind: "url", Session: &cp})
	return nil
}

// supervise waits for the process and either restarts it (unexpected exit
// with auto-restart) or records the terminal state.
func (m *Manager) supervise(sctx context.Context, id, binary string, tail *lineRing) {
	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	waitErr := cmd.Wait()

	m.mu.Lock()
	if m.active == nil || m.active.ID != id {
		// Superseded by a newer session; nothing to record.
		m.mu.Unlock()
		return
	}
	byOperator := m.stoppedByOperator
	m.cmd = nil
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	cp := *m.active
	m.mu.Unlock()

	// A managed stop already persisted its row in Stop; the supervisor must
	// not rewrite it.
	if byOperator {
		return
	}

	detail := tail.joined("; ")
	if waitErr != nil {
		detail = firstNonEmpty(waitErr.Error(), detail)
	}
	if m.cfg.AutoRestart {
		select {
		case <-sctx.Done():
			// Shutting down: fall through to failure handling.
		case <-time.After(5 * time.Second):
			m.mu.Lock()
			stillActive := m.active != nil && m.active.ID == id && !m.stoppedByOperator
			m.mu.Unlock()
			if stillActive {
				cp.Reconnects++
				m.persist(id, func(s *domain.TunnelSession) {
					s.Reconnects = cp.Reconnects
					s.LastError = "process exited (" + detail + "); restarting"
				})
				m.logger.Warn("tunnel process exited unexpectedly; restarting",
					"session", id, "reconnect", cp.Reconnects, "error", detail)
				if err := m.spawn(context.Background(), &cp, binary); err != nil {
					m.logger.Warn("tunnel restart failed", "session", id, "error", err)
				} else {
					m.emit(Event{Kind: "restarted", Session: m.Status()})
				}
				return
			}
		}
	}

	reason := "process exited unexpectedly: " + detail
	m.persist(id, func(s *domain.TunnelSession) {
		now := domain.Now()
		s.Status = domain.TunnelFailed
		s.StoppedAt = &now
		s.StopReason = "failed"
		s.LastError = reason
	})
	m.mu.Lock()
	if m.active != nil && m.active.ID == id {
		now := domain.Now()
		m.active.Status = domain.TunnelFailed
		m.active.StoppedAt = &now
		m.active.StopReason = "failed"
		m.active.LastError = reason
		cp2 := *m.active
		m.active = nil
		m.mu.Unlock()
		m.emit(Event{Kind: "failed", Session: &cp2})
		return
	}
	m.mu.Unlock()
}

// Stop tears the active session down. Stopping an already-terminal or
// unknown session is a no-op returning the stored row, so retries and
// double-clicks are safe.
func (m *Manager) Stop(ctx context.Context, id, reason, actor string) (*domain.TunnelSession, error) {
	if reason == "" {
		reason = "operator-requested"
	}
	m.mu.Lock()
	if m.active == nil || (id != "" && m.active.ID != id) {
		m.mu.Unlock()
		if id == "" {
			return nil, domain.NewError(domain.ErrCodeNotFound, "no tunnel is running")
		}
		if m.store != nil {
			if s, err := m.store.GetByID(ctx, id); err == nil && s != nil {
				return s, nil
			}
		}
		return nil, domain.NewError(domain.ErrCodeNotFound, "tunnel session not found")
	}
	cp := *m.active
	m.stoppedByOperator = true
	m.mu.Unlock()

	m.terminateLocked("operator stop by " + actor)
	now := domain.Now()
	cp.Status = domain.TunnelStopped
	cp.StoppedAt = &now
	cp.StopReason = reason
	cp.LastError = ""
	if m.store != nil {
		if _, err := m.store.Update(ctx, &cp); err != nil {
			m.logger.Warn("failed to persist the stopped tunnel session",
				"session", cp.ID, "error", err)
		}
	}
	m.mu.Lock()
	if m.active != nil && m.active.ID == cp.ID {
		m.active = nil
	}
	m.mu.Unlock()

	m.logger.Info("tunnel stopped", "session", cp.ID, "reason", reason, "actor", actor)
	out := cp
	m.emit(Event{Kind: "stopped", Session: &out})
	return &out, nil
}

// Restart stops the active session and creates a fresh one. A quick-tunnel
// URL is bound to its process, so a restart always mints a new public URL —
// the old one stops working, which the response makes explicit.
func (m *Manager) Restart(ctx context.Context, target, tenantID, createdBy string) (*domain.TunnelSession, error) {
	m.mu.Lock()
	prev := ""
	if m.active != nil {
		prev = m.active.Target
	}
	m.mu.Unlock()
	if target == "" {
		target = prev
	}
	if _, err := m.Stop(ctx, "", "restart", createdBy); err != nil {
		if !isNotFound(err) {
			return nil, err
		}
	}
	return m.Create(ctx, target, tenantID, createdBy)
}

// terminateLocked kills the process and waits for the supervisor to observe
// the exit. Kill (rather than a polite signal) is deliberate: it behaves
// identically on Linux, Docker and Windows, and a quick-tunnel URL dies with
// its process either way. The supervisor reconciles all state.
func (m *Manager) terminateLocked(detail string) {
	m.mu.Lock()
	cmd := m.cmd
	cancel := m.cancel
	m.stoppedByOperator = true
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		m.logger.Warn("tunnel process did not exit after kill", "detail", detail)
		<-done
	}
}

// failSession records a session that never reached running.
func (m *Manager) failSession(ctx context.Context, session *domain.TunnelSession, reason string) {
	now := domain.Now()
	session.Status = domain.TunnelFailed
	session.StoppedAt = &now
	session.StopReason = "failed"
	session.LastError = reason
	if m.store != nil {
		if _, err := m.store.Update(ctx, session); err != nil {
			m.logger.Warn("failed to persist the failed tunnel session",
				"session", session.ID, "error", err)
		}
	}
	m.mu.Lock()
	if m.active != nil && m.active.ID == session.ID {
		m.active = nil
	}
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.cmd = nil
	m.mu.Unlock()
	cp := *session
	m.emit(Event{Kind: "failed", Session: &cp})
}

// persist applies a mutation to the stored and in-memory session best-effort.
func (m *Manager) persist(id string, mutate func(*domain.TunnelSession)) {
	m.mu.Lock()
	if m.active == nil || m.active.ID != id {
		m.mu.Unlock()
		return
	}
	mutate(m.active)
	cp := *m.active
	m.mu.Unlock()
	if m.store != nil {
		if _, err := m.store.Update(context.Background(), &cp); err != nil {
			m.logger.Warn("failed to persist the tunnel session",
				"session", id, "error", err)
		}
	}
}

func (m *Manager) emit(e Event) {
	m.mu.Lock()
	hook := m.onEvent
	m.mu.Unlock()
	if hook != nil {
		hook(e)
	}
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var derr *domain.Error
	if errors.As(err, &derr) {
		return derr.Code == domain.ErrCodeNotFound
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func quote(s string) string {
	return "\"" + s + "\""
}

// lineRing keeps the tail of process output for failure reports without
// retaining unbounded logs in memory.
type lineRing struct {
	mu    sync.Mutex
	lines []string
	limit int
}

func (r *lineRing) add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.limit <= 0 {
		r.limit = 20
	}
	r.lines = append(r.lines, line)
	if len(r.lines) > r.limit {
		r.lines = r.lines[len(r.lines)-r.limit:]
	}
}

func (r *lineRing) joined(sep string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, sep)
}
