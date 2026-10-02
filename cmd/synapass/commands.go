package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/shadowsafin/synapass/internal/bootstrap"
	"github.com/shadowsafin/synapass/internal/config"
	"github.com/shadowsafin/synapass/internal/providers"
	"github.com/shadowsafin/synapass/internal/storage"
)

// runMigrate applies database migrations and exits.
//
// It exists as a separate command so a deployment can migrate in a controlled step
// instead of relying on every replica racing to migrate at startup, which is what
// automatic migration is for in a single-host install.
func runMigrate(cfg *config.Config, logger *slog.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	fmt.Printf("connecting to postgres at %s/%s\n", cfg.Database.Host, cfg.Database.Name)

	postgres, err := storage.NewPostgres(ctx, cfg.Database, logger)
	if err != nil {
		return &exitError{code: exitUnavailable, err: err}
	}
	defer postgres.Close()

	postgresResult, err := postgres.Migrate(ctx)
	if err != nil {
		return &exitError{code: exitFailure, err: err}
	}
	fmt.Printf("postgres: applied %d migration(s), skipped %d already-applied\n",
		len(postgresResult.Applied), len(postgresResult.Skipped))
	for _, applied := range postgresResult.Applied {
		fmt.Printf("  applied %s\n", applied)
	}

	// ClickHouse is migrated only when configured. Its DDL is not transactional, so
	// every statement is written to be idempotent and re-running is always safe.
	if cfg.ClickHouse.Addr != "" || cfg.ClickHouse.DSN != "" {
		clickhouse, cerr := storage.NewClickHouse(ctx, cfg.ClickHouse, logger)
		if cerr != nil {
			if cfg.ClickHouse.Required {
				return &exitError{code: exitUnavailable, err: cerr}
			}
			fmt.Printf("clickhouse: skipped (unavailable: %v)\n", cerr)
			return nil
		}
		defer clickhouse.Close()

		chResult, cerr := clickhouse.Migrate(ctx)
		if cerr != nil {
			return &exitError{code: exitFailure, err: cerr}
		}
		fmt.Printf("clickhouse: applied %d migration(s)\n", len(chResult.Applied))
	}

	fmt.Println("migrations complete")
	return nil
}

// healthProbeResponse mirrors the shape of GET /health.
type healthProbeResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// runHealthCheck probes a running instance.
//
// The check runs in-process rather than shelling out to curl, so the container
// image needs no extra tooling and the container healthcheck and the native systemd
// check use identical logic.
func runHealthCheck(cfg *config.Config, args []string) error {
	// A URL may be passed positionally, which is what the container HEALTHCHECK does,
	// because inside a container the configured listen address is usually a wildcard
	// that cannot be dialled directly.
	url := fmt.Sprintf("http://%s/health", normaliseAddr(cfg.HTTP.Addr))
	if len(args) > 0 && args[0] != "" {
		url = args[0]
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return &exitError{
			code: exitUnavailable,
			err:  fmt.Errorf("health check against %s failed: %w", url, err),
		}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode != http.StatusOK {
		return &exitError{
			code: exitUnavailable,
			err:  fmt.Errorf("health check returned %d: %s", resp.StatusCode, string(body)),
		}
	}

	var parsed healthProbeResponse
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Status != "" {
		fmt.Printf("ok: %s\n", parsed.Status)
	} else {
		fmt.Println("ok")
	}
	return nil
}

// normaliseAddr converts a listen address into something dialable.
//
// A server listening on ":8080" or "0.0.0.0:8080" is not reachable at that address
// from a client, so the wildcard host is replaced with loopback for probing.
func normaliseAddr(addr string) string {
	switch {
	case addr == "":
		return "127.0.0.1:8080"
	case addr[0] == ':':
		return "127.0.0.1" + addr
	case len(addr) >= 8 && addr[:8] == "0.0.0.0:":
		return "127.0.0.1:" + addr[8:]
	case len(addr) >= 3 && addr[:3] == "[::":
		return "127.0.0.1" + addr[3:]
	default:
		return addr
	}
}

// providerRegistryHolder owns the adapter registry and can rebuild it atomically.
//
// The registry is already safe for concurrent reads; the holder exists because
// reloading means constructing new adapters, and doing that in one place keeps
// credential resolution and HTTP client construction consistent across a reload.
type providerRegistryHolder struct {
	cfg      *config.Config
	registry *providers.Registry
	app      *App
}

// registry returns the current registry.
func (h *providerRegistryHolder) current() *providers.Registry { return h.registry }

// reload rebuilds the adapter registry from current database state.
func (h *providerRegistryHolder) reload(ctx context.Context) error {
	registry, failures, err := bootstrap.BuildAdapters(ctx, h.cfg, h.app.repos, h.app.logger)
	if err != nil {
		return err
	}

	// A transient database problem must not empty a working registry and take every
	// provider out of rotation.
	if registry.Len() == 0 && h.registry.Len() > 0 {
		h.app.logger.Warn("catalogue refresh produced no adapters; keeping the previous registry",
			"failures", len(failures))
		return nil
	}

	// Replace swaps the whole contents under a write lock, so a concurrent reader
	// sees either the old or the new registry and never a partially populated one.
	entries, adapters := registry.Snapshot()
	h.registry.Replace(entries, adapters)
	return nil
}
