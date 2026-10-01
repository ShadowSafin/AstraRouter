package api

import (
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/shadowsafin/corerouter/internal/domain"
	"github.com/shadowsafin/corerouter/internal/version"
)

// healthResponse is the body of GET /health.
type healthResponse struct {
	Status  string            `json:"status"`
	Version version.Info      `json:"version"`
	Uptime  string            `json:"uptime"`
	Checks  map[string]string `json:"checks,omitempty"`
	// Components reports which optional subsystems are configured, so an operator
	// can tell "not configured" from "configured but unhealthy".
	Components map[string]bool `json:"components"`
}

// handleHealth reports process liveness.
//
// Liveness must not depend on downstream services. If it did, a brief Postgres
// blip would cause the orchestrator to kill every gateway replica, turning a
// recoverable dependency problem into a full outage.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Status:  "ok",
		Version: s.version,
		Uptime:  s.uptime().Round(time.Second).String(),
		Components: map[string]bool{
			"postgres":   s.postgres != nil,
			"redis":      s.redis != nil,
			"clickhouse": s.clickhouse != nil,
			"nats":       s.nats != nil,
			"tracing":    s.tracer.Enabled(),
		},
	})
}

// handleReady reports whether the instance can serve traffic.
//
// Readiness does check dependencies, because that is the question the load balancer
// is asking: should requests be sent here? A dependency failure returns 503 so the
// instance is drained rather than serving guaranteed failures.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 3*time.Second)
	defer cancel()

	checks := map[string]string{}
	ready := true

	if s.postgres != nil {
		if err := s.postgres.Ping(ctx); err != nil {
			checks["postgres"] = "unavailable: " + err.Error()
			// Postgres is the system of record, so its loss does make the instance
			// unready: authentication and policy reads would fail.
			ready = false
		} else {
			checks["postgres"] = "ok"
		}
	}

	if s.redis != nil {
		if err := s.redis.Ping(ctx); err != nil {
			// Redis is an optimisation. Losing it degrades rate limiting to the
			// in-process limiter but the gateway can still serve traffic, so it is
			// reported without failing readiness.
			checks["redis"] = "degraded: " + err.Error()
		} else {
			checks["redis"] = "ok"
		}
	}

	if s.adapters != nil {
		checks["providers"] = itoa(s.adapters.Len()) + " configured"
		if s.adapters.Len() == 0 {
			// With no usable provider the gateway can only return errors, so it
			// should not receive traffic.
			ready = false
		}
	}

	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}

	body := healthResponse{
		Status:  "ready",
		Version: s.version,
		Uptime:  s.uptime().Round(time.Second).String(),
		Checks:  checks,
	}
	if !ready {
		body.Status = "not_ready"
	}
	writeJSON(w, status, body)
}

// handleVersion reports build identity.
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		version.Info
		Uptime      string `json:"uptime"`
		GoRoutines  int    `json:"goroutines"`
		Environment string `json:"environment"`
	}{
		Info:        s.version,
		Uptime:      s.uptime().Round(time.Second).String(),
		GoRoutines:  runtime.NumGoroutine(),
		Environment: s.config.App.Environment,
	})
}

// handleListModels serves GET /v1/models.
//
// Only models that are actually servable are listed. A client that discovers a model
// here and then gets a routing failure for it would have no way to tell whether the
// problem was theirs, so the list is filtered to what the gateway will genuinely
// route.
func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := requestContext(ctx)

	source := s.modelSource()
	if source == nil {
		writeError(w, domain.NewError(domain.ErrCodeInternal, "the model registry is unavailable"),
			metaFromContext(rc, nil))
		return
	}

	models, err := source.List(ctx)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}

	// Aliases are expanded into their own entries so a client can request either the
	// upstream name or the alias, and can see that both exist.
	byID := map[string]domain.ModelObject{}
	for _, m := range models {
		if !m.Usable() {
			continue
		}
		object := modelObject(m)
		byID[m.Name] = object
		for _, alias := range m.Aliases {
			if alias == "" {
				continue
			}
			aliasObject := object
			aliasObject.ID = alias
			// An alias is owned by the first model that declares it, matching how the
			// routing engine resolves it.
			if _, exists := byID[alias]; !exists {
				byID[alias] = aliasObject
			}
		}
	}

	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	data := make([]domain.ModelObject, 0, len(ids))
	for _, id := range ids {
		data = append(data, byID[id])
	}

	writeJSON(w, http.StatusOK, domain.ModelListResponse{
		Object: "list",
		Data:   data,
	})
}

// handleGetModel serves GET /v1/models/{model}.
func (s *Server) handleGetModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc := requestContext(ctx)

	name := strings.TrimSpace(chiURLParam(r, "model"))
	if name == "" {
		writeError(w, domain.NewError(domain.ErrCodeInvalidRequest, "a model name is required"),
			metaFromContext(rc, nil))
		return
	}

	source := s.modelSource()
	if source == nil {
		writeError(w, domain.NewError(domain.ErrCodeInternal, "the model registry is unavailable"),
			metaFromContext(rc, nil))
		return
	}

	models, err := source.List(ctx)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}

	for _, m := range models {
		if !m.Usable() {
			continue
		}
		if strings.EqualFold(m.Name, name) {
			writeJSON(w, http.StatusOK, modelObject(m))
			return
		}
		for _, alias := range m.Aliases {
			if strings.EqualFold(alias, name) {
				object := modelObject(m)
				object.ID = alias
				writeJSON(w, http.StatusOK, object)
				return
			}
		}
	}

	writeError(w, domain.Errorf(domain.ErrCodeNotFound, "model %q is not registered", name),
		metaFromContext(rc, nil))
}

// modelObject renders a registry entry as an OpenAI model object.
func modelObject(m domain.Model) domain.ModelObject {
	capabilities := make([]string, 0, len(m.Capabilities))
	for _, c := range m.Capabilities {
		capabilities = append(capabilities, string(c))
	}

	return domain.ModelObject{
		ID:              m.Name,
		Object:          "model",
		Created:         m.CreatedAt.Unix(),
		OwnedBy:         firstNonEmptyString(m.ProviderName, m.ProviderID),
		Provider:        m.ProviderName,
		UpstreamModel:   m.Name,
		ContextWindow:   m.ContextWindow,
		MaxOutputTokens: m.MaxOutputTokens,
		InputCost:       m.InputCostPerMillion,
		OutputCost:      m.OutputCostPerMillion,
		Capabilities:    capabilities,
		Status:          string(m.Status),
	}
}
