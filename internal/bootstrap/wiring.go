package bootstrap

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/shadowsafin/astrarouter/internal/admin"
	"github.com/shadowsafin/astrarouter/internal/config"
	"github.com/shadowsafin/astrarouter/internal/domain"
	"github.com/shadowsafin/astrarouter/internal/providers"
	"github.com/shadowsafin/astrarouter/internal/storage"
	"github.com/shadowsafin/astrarouter/internal/telemetry"
)

// AdapterOptions builds provider options from configuration.
func AdapterOptions(cfg *config.Config, logger *slog.Logger) providers.Options {
	return providers.Options{
		Logger: logger,
		Timeouts: domain.TimeoutPolicy{
			Total:      cfg.Routing.DefaultTimeout.Total.Std(),
			PerAttempt: cfg.Routing.DefaultTimeout.PerAttempt.Std(),
			Connect:    cfg.Routing.DefaultTimeout.Connect.Std(),
			StreamIdle: cfg.Routing.DefaultTimeout.StreamIdle.Std(),
			FirstToken: cfg.Routing.DefaultTimeout.FirstToken.Std(),
		},
	}
}

// BuildAdapters constructs the adapter registry from the stored catalogue.
//
// Construction failures are returned per provider rather than aborting: one
// provider with a typo in its base URL must not prevent the gateway from serving
// traffic through the others, and the failure is reported on the dashboard so the
// operator can see exactly what is broken.
func BuildAdapters(
	ctx context.Context,
	cfg *config.Config,
	repos *storage.Repositories,
	logger *slog.Logger,
) (*providers.Registry, map[string]error, error) {
	stored, err := repos.Providers.List(ctx)
	if err != nil {
		return nil, nil, err
	}

	// Credentials resolve in a fixed precedence: an explicit environment
	// binding wins (it is what zero-config deployments use), then the
	// encrypted credential stored through the admin API, then nothing. The
	// request path never reads the environment; everything is resolved here,
	// once per catalogue refresh.
	credStore, credErr := credentialStore(cfg)
	if credErr != nil {
		logger.Warn("encrypted provider credentials are unavailable; only environment-bound credentials will resolve",
			"error", credErr)
	}
	resolved := make([]domain.Provider, 0, len(stored))
	for _, p := range stored {
		if p.APIKeyEnv != "" {
			p.APIKeyInline = credentialFromEnv(p.APIKeyEnv)
		} else if credStore != nil && repos.Credentials != nil {
			if env, err := repos.Credentials.Get(ctx, p.ID); err != nil {
				logger.Warn("failed to load a stored provider credential",
					"provider", p.Name, "error", err)
			} else if env != nil {
				secret, err := credStore.Open(env)
				if err != nil {
					logger.Warn("failed to decrypt a stored provider credential",
						"provider", p.Name, "error", err)
				} else {
					p.APIKeyInline = secret
				}
			}
		}
		resolved = append(resolved, p)
	}

	registry, failures := providers.BuildRegistry(resolved, AdapterOptions(cfg, logger))
	for name, err := range failures {
		logger.Warn("provider adapter could not be constructed and will be excluded from routing",
			"provider", name, "error", err)
	}
	return registry, failures, nil
}

// credentialStore builds the sealing store for database-held provider
// credentials. An explicit AR_CREDENTIALS_KEY wins; otherwise the data key is
// derived from the admin key, so stock deployments need no new configuration.
func credentialStore(cfg *config.Config) (*admin.Store, error) {
	key, err := admin.KeyMaterial(
		strings.TrimSpace(os.Getenv(admin.CredentialsKeyEnv)),
		ResolveAdminKey(cfg))
	if err != nil {
		return nil, err
	}
	return admin.NewStore(key)
}

// credentialFromEnv reads a credential from the environment, trimming whitespace
// so a value pasted with a trailing newline still authenticates.
func credentialFromEnv(name string) string {
	return trimSpace(osGetenv(name))
}

// RegistryAvailability adapts the adapter registry to the routing engine's
// availability check.
type RegistryAvailability struct {
	registry *providers.Registry
}

// NewRegistryAvailability constructs an availability check.
func NewRegistryAvailability(registry *providers.Registry) *RegistryAvailability {
	return &RegistryAvailability{registry: registry}
}

// Available reports whether a provider has a usable adapter.
func (a *RegistryAvailability) Available(providerID, providerName string) bool {
	if a == nil || a.registry == nil {
		return false
	}
	if providerID != "" {
		if _, ok := a.registry.ByID(providerID); ok {
			return true
		}
	}
	if providerName != "" {
		_, ok := a.registry.ByName(providerName)
		return ok
	}
	return false
}

// SinkOptions configures the telemetry sink.
type SinkOptions struct {
	Repos      *storage.Repositories
	ClickHouse *storage.ClickHouse
	NATS       *storage.NATS
	// WriteTimeout bounds a single record write.
	WriteTimeout time.Duration
	Logger       *slog.Logger
}

// Sink persists telemetry records to their respective stores.
//
// Routing every record to exactly one primary destination is deliberate:
//
//	usage       Postgres, the billing system of record
//	request log Postgres, the dashboard's debugging view
//	trace       ClickHouse, which is built for this volume
//
// NATS receives copies for downstream consumers rather than acting as the primary
// path, because a message broker is not a queryable store and losing a usage event
// must not depend on a consumer being up.
type Sink struct {
	repos      *storage.Repositories
	clickhouse *storage.ClickHouse
	nats       *storage.NATS
	logger     *slog.Logger

	writeTimeout time.Duration
}

// NewSink constructs a telemetry sink.
func NewSink(opts SinkOptions) *Sink {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	timeout := opts.WriteTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Sink{
		repos:        opts.Repos,
		clickhouse:   opts.ClickHouse,
		nats:         opts.NATS,
		logger:       logger,
		writeTimeout: timeout,
	}
}

// WriteUsage implements telemetry.Sink.
func (s *Sink) WriteUsage(ctx context.Context, rec *domain.UsageRecord) error {
	if rec == nil {
		return nil
	}
	// The analytics copy is written even when the authoritative insert fails, so a
	// Postgres incident does not also blind the dashboards.
	if s.clickhouse != nil {
		s.clickhouse.WriteUsage(ctx, rec)
	}
	if s.nats != nil {
		s.nats.PublishUsage(rec)
	}
	if s.repos == nil || s.repos.Usage == nil {
		return nil
	}
	return s.repos.Usage.Insert(ctx, rec)
}

// WriteRequestLog implements telemetry.Sink.
func (s *Sink) WriteRequestLog(ctx context.Context, entry *domain.RequestLog) error {
	if entry == nil {
		return nil
	}
	if s.nats != nil {
		s.nats.PublishRequestLog(entry)
	}
	if s.repos == nil || s.repos.Logs == nil {
		return nil
	}
	return s.repos.Logs.Insert(ctx, entry)
}

// WriteTrace implements telemetry.Sink.
func (s *Sink) WriteTrace(ctx context.Context, trace *domain.RequestTrace) error {
	if trace == nil {
		return nil
	}
	if s.clickhouse != nil {
		s.clickhouse.WriteTrace(ctx, trace)
		if trace.Decision != nil && s.repos != nil {
			// Route decision analytics are the basis of the routing performance view.
			s.clickhouse.WriteRouteDecision(ctx, nil, trace.Decision)
		}
	}
	if s.nats != nil {
		s.nats.PublishTrace(trace)
	}
	return nil
}

// Compile-time assertions that the wiring satisfies the interfaces the services
// declare. A change to either interface then fails the build here, at the seam,
// rather than at the call site.
var (
	_ telemetry.Sink = (*Sink)(nil)
)
