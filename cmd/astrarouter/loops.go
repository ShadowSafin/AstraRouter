package main

import (
	"context"
	"time"

	"github.com/shadowsafin/astrarouter/internal/domain"
	"github.com/shadowsafin/astrarouter/internal/policy"
	"github.com/shadowsafin/astrarouter/internal/routing"
	"github.com/shadowsafin/astrarouter/internal/storage"
)

// startCatalogueRefresh periodically reloads the registry from the database.
//
// Without this, a provider or model added through the dashboard would not become
// routable until a restart, which is exactly the kind of operational friction that
// makes an operator stop trusting the platform.
func (a *App) startCatalogueRefresh(catalogue *routing.CachedCatalogue, registry *providerRegistryHolder) {
	interval := a.cfg.Cache.RegistryCacheTTL.Std()
	if interval <= 0 {
		interval = 30 * time.Second
	}
	// The refresh is cheap but pointless to run more often than every few seconds.
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}

	a.spawnLoop("catalogue-refresh", interval, func(ctx context.Context) error {
		if err := catalogue.Load(ctx); err != nil {
			return err
		}
		// Rebuilding the registry picks up a changed credential, a new model or a
		// disabled provider without a restart.
		return registry.reload(ctx)
	})
}

// startHealthProbes runs the active liveness probe loop.
//
// An active probe is what detects a provider that has gone dark while no traffic is
// flowing. Without it, a provider that died overnight is only discovered by the first
// unlucky user in the morning.
func (a *App) startHealthProbes() {
	interval := a.cfg.Routing.HealthCheckInterval.Std()
	if interval <= 0 {
		interval = 30 * time.Second
	}

	a.spawnLoop("provider-health", interval, func(ctx context.Context) error {
		// Only one replica should probe. A distributed lock keeps N replicas from
		// multiplying the probe traffic against every provider.
		lock, acquired, err := a.acquireLeaderLock(ctx, "provider-health", interval)
		if err != nil {
			return err
		}
		if !acquired {
			return nil
		}
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = lock.Release(releaseCtx)
		}()

		probeCtx, cancel := context.WithTimeout(ctx,
			a.cfg.Routing.HealthCheckTimeout.Std())
		defer cancel()

		for _, name := range a.adapters.Names() {
			adapter, ok := a.adapters.ByName(name)
			if !ok {
				continue
			}

			health := adapter.HealthCheck(probeCtx)
			a.health.RecordProbe(health)
			a.metrics.SetProviderHealth(health.ProviderName, string(health.State))

			if a.nats != nil {
				a.nats.PublishProviderHealth(&health)
			}
			// A provider that flips between states is worth a log line; one that
			// stays healthy is not.
			if health.State != domain.HealthHealthy {
				a.logger.Warn("provider health probe reported a problem",
					"provider", health.ProviderName,
					"state", health.State,
					"latency_ms", health.LatencyMS,
					"message", health.Message,
				)
			}

			if a.clickhouse != nil {
				a.clickhouse.WriteProviderSnapshot(ctx, &domain.ProviderStatusSnapshot{
					ID:           domain.NewID(),
					ProviderID:   health.ProviderID,
					ProviderName: health.ProviderName,
					State:        health.State,
					WindowMS:     interval.Milliseconds(),
					SuccessRate:  health.SuccessRate,
					ErrorRate:    health.ErrorRate,
					LatencyP50MS: health.LatencyMS,
					Message:      health.Message,
					CapturedAt:   health.CheckedAt,
				})
			}
		}
		return nil
	})
}

// startUsageReconciliation periodically flushes Redis budget counters to Postgres.
//
// Redis holds the live spend counter because it must be incremented on every
// request; Postgres holds the durable figure. Reconciling means a Redis flush costs
// at most one interval of accuracy rather than the whole period's spend history.
func (a *App) startUsageReconciliation() {
	if a.redis == nil || a.repos == nil || a.repos.Budgets == nil {
		return
	}

	a.spawnLoop("usage-reconcile", 5*time.Minute, func(ctx context.Context) error {
		tenants, err := a.repos.Tenants.List(ctx)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		for _, tenant := range tenants {
			for _, period := range []domain.BudgetPeriod{domain.BudgetDaily, domain.BudgetMonthly} {
				// The live counter is addressed by its rendered period label, which is
				// the same label the request path charges against.
				spent, err := a.redis.GetSpend(ctx, "tenant:"+tenant.ID, policy.PeriodKey(period, now))
				if err != nil {
					a.logger.Debug("failed to read reconciled spend",
						"tenant", tenant.Slug, "error", err)
					continue
				}
				if err := a.repos.Budgets.ReconcileSpend(ctx, tenant.ID, "tenant", "", period, spent); err != nil {
					a.logger.Debug("failed to reconcile spend",
						"tenant", tenant.Slug, "error", err)
				}
			}
		}
		return nil
	})
}

// spawnLoop runs fn on an interval until the app shuts down.
//
// Failures are logged and retried on the next tick rather than terminating the loop:
// a transient database error must not permanently disable provider health probing.
func (a *App) spawnLoop(name string, interval time.Duration, fn func(context.Context) error) {
	if a.cancelLoops == nil {
		ctx, cancel := context.WithCancel(context.Background())
		a.cancelLoops = cancel
		a.loopsCtx = ctx
	}

	ctx := a.loopsCtx
	a.loops.Add(1)

	go func() {
		defer a.loops.Done()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		a.logger.Debug("background loop started", "loop", name, "interval", interval)

		for {
			select {
			case <-ctx.Done():
				a.logger.Debug("background loop stopped", "loop", name)
				return
			case <-ticker.C:
				if err := fn(ctx); err != nil {
					a.logger.Warn("background loop iteration failed", "loop", name, "error", err)
				}
			}
		}
	}()
}

// acquireLeaderLock takes a lease so only one replica runs cluster-wide work.
func (a *App) acquireLeaderLock(ctx context.Context, name string, ttl time.Duration) (*storage.Lock, bool, error) {
	if a.redis == nil {
		// Without Redis every replica acts as leader, which is correct for the
		// single-instance deployment that has no Redis.
		return &storage.Lock{}, true, nil
	}
	// The lease is twice the interval so a slow iteration does not lose the lock
	// mid-run, and a crashed holder is replaced within two intervals.
	return a.redis.Acquire(ctx, name, ttl*2)
}
