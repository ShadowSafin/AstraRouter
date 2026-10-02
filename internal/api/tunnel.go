package api

import (
	"context"
	"net/http"
	"time"

	"github.com/shadowsafin/astrarouter/internal/domain"
)

// TunnelService manages temporary public tunnels. It is an interface defined
// here, in the consuming package, so handlers stay testable without a
// cloudflared binary; *tunnel.Manager satisfies it unmodified.
type TunnelService interface {
	Create(ctx context.Context, target, tenantID, createdBy string) (*domain.TunnelSession, error)
	Stop(ctx context.Context, id, reason, actor string) (*domain.TunnelSession, error)
	Restart(ctx context.Context, target, tenantID, createdBy string) (*domain.TunnelSession, error)
	Status() *domain.TunnelSession
	BinaryAvailable() (string, error)
}

// tunnelDisabled fails requests when the feature is switched off. A disabled
// tunnel surface is a 501 with an enabling hint rather than a 404, so an
// operator hitting it from the dashboard learns what to change.
func (s *Server) tunnelDisabled() error {
	return domain.NewError(domain.ErrCodeNotImplemented,
		"temporary tunnels are disabled; set tunnel.enabled: true (or AR_TUNNEL_ENABLED=true) "+
			"and restart the gateway to use them (native installs also need cloudflared on PATH)")
}

// tunnelManager returns the manager or the disabled error when the feature is
// off or unwired. Every tunnel handler goes through here so the disabled
// behaviour is identical across the surface.
func (s *Server) tunnelManager() (TunnelService, error) {
	if s.config == nil || !s.config.Tunnel.Enabled {
		return nil, s.tunnelDisabled()
	}
	if s.tunnels == nil {
		return nil, s.tunnelDisabled()
	}
	return s.tunnels, nil
}

// handleTunnelCreate serves POST /admin/v1/tunnels/create.
//
// The body names what to expose: "gateway" (the inference API), "dashboard"
// (the UI), or an explicit loopback "host:port". Anything else is rejected
// before a process is spawned. Creation waits for cloudflared to print the
// public URL, so a 201 means the URL in the response is already usable.
func (s *Server) handleTunnelCreate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 90*time.Second)
	defer cancel()
	rc := requestContext(ctx)

	manager, err := s.tunnelManager()
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	var body struct {
		Target string `json:"target"`
	}
	_ = decodeLenient(r, 1<<16, &body)

	var tenantID, actor string
	if p := principal(ctx); p != nil {
		actor = p.Label()
	}
	if rc != nil {
		tenantID = rc.TenantID()
	}
	session, err := manager.Create(ctx, body.Target, tenantID, actor)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	s.audit(ctx, rc, domain.AuditCreate, domain.ResourceTunnel, session.ID,
		nil, map[string]any{"target": session.Target, "url": session.PublicURL})
	if s.nats != nil {
		event := &domain.TunnelEvent{Event: "created", Session: session, CreatedAt: domain.Now()}
		if rc != nil {
			event.RequestID = rc.RequestID.String()
		}
		s.nats.PublishTunnelStatus(event)
	}
	writeJSON(w, http.StatusCreated, session)
}

// handleTunnelStop serves POST /admin/v1/tunnels/stop.
//
// An empty id stops the active tunnel; a known terminal id returns its stored
// row so retries and double-clicks are safe. The public URL dies with the
// process.
func (s *Server) handleTunnelStop(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 30*time.Second)
	defer cancel()
	rc := requestContext(ctx)

	manager, err := s.tunnelManager()
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	_ = decodeLenient(r, 1<<16, &body)

	var actor string
	if p := principal(ctx); p != nil {
		actor = p.Label()
	}
	session, err := manager.Stop(ctx, body.ID, "operator-requested", actor)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	s.audit(ctx, rc, domain.AuditUpdate, domain.ResourceTunnel, session.ID,
		nil, map[string]any{"status": string(session.Status), "stop_reason": session.StopReason})
	if s.nats != nil {
		event := &domain.TunnelEvent{Event: "stopped", Session: session, CreatedAt: domain.Now()}
		if rc != nil {
			event.RequestID = rc.RequestID.String()
		}
		s.nats.PublishTunnelStatus(event)
	}
	writeJSON(w, http.StatusOK, session)
}

// handleTunnelRestart serves POST /admin/v1/tunnels/restart.
//
// A quick-tunnel URL is bound to its process, so a restart always mints a new
// public URL: the response names it explicitly rather than letting the caller
// assume the old one survived.
func (s *Server) handleTunnelRestart(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 120*time.Second)
	defer cancel()
	rc := requestContext(ctx)

	manager, err := s.tunnelManager()
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	var body struct {
		Target string `json:"target"`
	}
	_ = decodeLenient(r, 1<<16, &body)

	var tenantID, actor string
	if p := principal(ctx); p != nil {
		actor = p.Label()
	}
	if rc != nil {
		tenantID = rc.TenantID()
	}
	session, err := manager.Restart(ctx, body.Target, tenantID, actor)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	s.audit(ctx, rc, domain.AuditUpdate, domain.ResourceTunnel, session.ID,
		nil, map[string]any{"status": string(session.Status), "url": session.PublicURL})
	if s.nats != nil {
		event := &domain.TunnelEvent{Event: "restarted", Session: session, CreatedAt: domain.Now()}
		if rc != nil {
			event.RequestID = rc.RequestID.String()
		}
		s.nats.PublishTunnelStatus(event)
	}
	writeJSON(w, http.StatusOK, session)
}

// handleTunnelStatus serves GET /admin/v1/tunnels/status.
//
// It never fails: with the feature disabled or unwired it reports that fact,
// so dashboards and scripts can branch on one field instead of parsing
// errors.
func (s *Server) handleTunnelStatus(w http.ResponseWriter, r *http.Request) {
	_ = requestContext(r.Context())
	enabled := s.config != nil && s.config.Tunnel.Enabled
	var active *domain.TunnelSession
	binaryPath := ""
	binaryErr := ""
	if enabled && s.tunnels != nil {
		active = s.tunnels.Status()
		if path, err := s.tunnels.BinaryAvailable(); err != nil {
			binaryErr = err.Error()
		} else {
			binaryPath = path
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":          enabled,
		"binary_available": binaryErr == "",
		"binary_path":      binaryPath,
		"binary_error":     binaryErr,
		"active":           active,
	})
}

// handleTunnelCurrentURL serves GET /admin/v1/tunnels/current-url.
//
// The shape is stable whether or not a tunnel exists (url null when none),
// which is what makes it usable from scripts without error parsing.
func (s *Server) handleTunnelCurrentURL(w http.ResponseWriter, r *http.Request) {
	_ = requestContext(r.Context())
	var active *domain.TunnelSession
	if s.config != nil && s.config.Tunnel.Enabled && s.tunnels != nil {
		active = s.tunnels.Status()
	}
	out := map[string]any{
		"url":         nil,
		"status":      nil,
		"target":      nil,
		"target_addr": nil,
	}
	if active != nil {
		out["url"] = active.PublicURL
		out["status"] = string(active.Status)
		out["target"] = active.Target
		out["target_addr"] = active.TargetAddr
		out["session_id"] = active.ID
		out["started_at"] = active.StartedAt
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTunnelHistory serves GET /admin/v1/tunnels/history.
//
// Recent sessions, newest first: the audit trail of what was exposed, when,
// and how each session ended.
func (s *Server) handleTunnelHistory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 10*time.Second)
	defer cancel()
	rc := requestContext(ctx)
	if s.repos == nil || s.repos.Tunnels == nil {
		writeJSON(w, http.StatusOK, map[string]any{"sessions": []domain.TunnelSession{}})
		return
	}
	sessions, err := s.repos.Tunnels.Recent(ctx, parseIntParam(r, "limit", 20))
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}
