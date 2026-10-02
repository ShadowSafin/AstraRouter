package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// EnvPrefix is the namespace for CoreRouter's own environment variables. Every
// setting is reachable as CR_<PATH>, so a container or systemd unit can be fully
// configured without a config file.
const EnvPrefix = "CR_"

// firstEnv returns the value of the first environment variable that is set to a
// non-empty string.
func firstEnv(names ...string) string {
	for _, n := range names {
		if v, ok := os.LookupEnv(n); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func envString(dst *string, names ...string) {
	if v := firstEnv(names...); v != "" {
		*dst = v
	}
}

func envBool(dst *bool, names ...string) {
	v := firstEnv(names...)
	if v == "" {
		return
	}
	if b, err := strconv.ParseBool(strings.ToLower(v)); err == nil {
		*dst = b
	}
}

// envBoolPtr sets an optional *bool from the environment, allocating only
// when the variable is present so a missing var keeps the nil default.
func envBoolPtr(dst **bool, names ...string) {
	v := firstEnv(names...)
	if v == "" {
		return
	}
	if b, err := strconv.ParseBool(strings.ToLower(v)); err == nil {
		*dst = &b
	}
}

func envInt(dst *int, names ...string) {
	v := firstEnv(names...)
	if v == "" {
		return
	}
	if n, err := strconv.Atoi(v); err == nil {
		*dst = n
	}
}

func envInt32(dst *int32, names ...string) {
	v := firstEnv(names...)
	if v == "" {
		return
	}
	if n, err := strconv.ParseInt(v, 10, 32); err == nil {
		*dst = int32(n)
	}
}

func envInt64(dst *int64, names ...string) {
	v := firstEnv(names...)
	if v == "" {
		return
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		*dst = n
	}
}

func envFloat(dst *float64, names ...string) {
	v := firstEnv(names...)
	if v == "" {
		return
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		*dst = f
	}
}

func envDuration(dst *Duration, names ...string) {
	v := firstEnv(names...)
	if v == "" {
		return
	}
	if d, err := time.ParseDuration(v); err == nil {
		*dst = Duration(d)
		return
	}
	// Bare numbers are seconds, matching the config file's behaviour.
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		*dst = Duration(time.Duration(secs * float64(time.Second)))
	}
}

// envSlice splits a comma-separated list, trimming whitespace and dropping empty
// entries so trailing commas in a systemd Environment= line are harmless.
func envSlice(dst *[]string, names ...string) {
	v := firstEnv(names...)
	if v == "" {
		return
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	*dst = out
}

// envMap parses "key=value,key=value" pairs.
func envMap(dst *map[string]string, names ...string) {
	v := firstEnv(names...)
	if v == "" {
		return
	}
	out := map[string]string{}
	for _, pair := range strings.Split(v, ",") {
		k, val, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		k, val = strings.TrimSpace(k), strings.TrimSpace(val)
		if k == "" {
			continue
		}
		out[k] = val
	}
	*dst = out
}

// applyEnv overlays environment variables onto the configuration.
//
// The mapping is written out explicitly rather than derived by reflecting over
// struct tags. That is more code, but it means the set of environment variables
// is discoverable by reading one file, and a renamed struct field cannot
// silently change the operator-facing interface.
func (c *Config) applyEnv() error {
	// ---- app ----
	envString(&c.App.Name, "CR_APP_NAME")
	envString(&c.App.Environment, "CR_ENVIRONMENT", "CR_ENV")
	envString(&c.App.InstanceID, "CR_INSTANCE_ID")
	envString(&c.App.Region, "CR_REGION")
	envString(&c.App.DefaultTenantSlug, "CR_DEFAULT_TENANT_SLUG")
	envString(&c.App.DefaultTenantName, "CR_DEFAULT_TENANT_NAME")
	envBool(&c.App.BootstrapFromConfig, "CR_BOOTSTRAP_FROM_CONFIG")

	// ---- http ----
	envString(&c.HTTP.Addr, "CR_HTTP_ADDR", "CR_LISTEN_ADDR", "PORT_ADDR")
	envDuration(&c.HTTP.ReadHeaderTimeout, "CR_HTTP_READ_HEADER_TIMEOUT")
	envDuration(&c.HTTP.ReadTimeout, "CR_HTTP_READ_TIMEOUT")
	envDuration(&c.HTTP.WriteTimeout, "CR_HTTP_WRITE_TIMEOUT")
	envDuration(&c.HTTP.IdleTimeout, "CR_HTTP_IDLE_TIMEOUT")
	envDuration(&c.HTTP.ShutdownTimeout, "CR_HTTP_SHUTDOWN_TIMEOUT")
	envInt64(&c.HTTP.MaxBodyBytes, "CR_HTTP_MAX_BODY_BYTES")
	envSlice(&c.HTTP.TrustedProxies, "CR_HTTP_TRUSTED_PROXIES")
	envSlice(&c.HTTP.CORSAllowedOrigins, "CR_HTTP_CORS_ALLOWED_ORIGINS", "CR_CORS_ALLOWED_ORIGINS")
	envBool(&c.HTTP.EnablePprof, "CR_HTTP_ENABLE_PPROF")

	// ---- database ----
	// A single DATABASE_URL is the conventional container spelling; the CR_
	// prefixed aliases exist so the gateway can share a host with other
	// services without variable collisions.
	envString(&c.Database.DSN, "CR_DATABASE_DSN", "CR_POSTGRES_DSN", "DATABASE_URL")
	envString(&c.Database.Host, "CR_POSTGRES_HOST", "CR_DATABASE_HOST")
	envInt(&c.Database.Port, "CR_POSTGRES_PORT", "CR_DATABASE_PORT")
	envString(&c.Database.User, "CR_POSTGRES_USER", "CR_DATABASE_USER")
	envString(&c.Database.Password, "CR_POSTGRES_PASSWORD", "CR_DATABASE_PASSWORD")
	envString(&c.Database.Name, "CR_POSTGRES_DB", "CR_POSTGRES_NAME", "CR_DATABASE_NAME")
	envString(&c.Database.SSLMode, "CR_POSTGRES_SSL_MODE", "CR_DATABASE_SSL_MODE")
	envInt32(&c.Database.MaxConns, "CR_POSTGRES_MAX_CONNS")
	envInt32(&c.Database.MinConns, "CR_POSTGRES_MIN_CONNS")
	envDuration(&c.Database.MaxConnLifetime, "CR_POSTGRES_MAX_CONN_LIFETIME")
	envDuration(&c.Database.MaxConnIdleTime, "CR_POSTGRES_MAX_CONN_IDLE_TIME")
	envDuration(&c.Database.ConnectTimeout, "CR_POSTGRES_CONNECT_TIMEOUT")
	envDuration(&c.Database.StatementTimeout, "CR_POSTGRES_STATEMENT_TIMEOUT")
	envBool(&c.Database.AutoMigrate, "CR_POSTGRES_AUTO_MIGRATE", "CR_AUTO_MIGRATE")
	envString(&c.Database.MigrationsDir, "CR_MIGRATIONS_DIR")

	// ---- redis ----
	envString(&c.Redis.Addr, "CR_REDIS_ADDR", "REDIS_URL")
	envString(&c.Redis.Username, "CR_REDIS_USERNAME")
	envString(&c.Redis.Password, "CR_REDIS_PASSWORD")
	envInt(&c.Redis.DB, "CR_REDIS_DB")
	envSlice(&c.Redis.SentinelAddrs, "CR_REDIS_SENTINEL_ADDRS")
	envString(&c.Redis.MasterName, "CR_REDIS_MASTER_NAME")
	envInt(&c.Redis.PoolSize, "CR_REDIS_POOL_SIZE")
	envDuration(&c.Redis.DialTimeout, "CR_REDIS_DIAL_TIMEOUT")
	envDuration(&c.Redis.ReadTimeout, "CR_REDIS_READ_TIMEOUT")
	envDuration(&c.Redis.WriteTimeout, "CR_REDIS_WRITE_TIMEOUT")
	envString(&c.Redis.KeyPrefix, "CR_REDIS_KEY_PREFIX")
	envBool(&c.Redis.TLS, "CR_REDIS_TLS")
	envBool(&c.Redis.Required, "CR_REDIS_REQUIRED")

	// ---- clickhouse ----
	envString(&c.ClickHouse.DSN, "CR_CLICKHOUSE_DSN")
	envString(&c.ClickHouse.Addr, "CR_CLICKHOUSE_ADDR")
	envString(&c.ClickHouse.Database, "CR_CLICKHOUSE_DB", "CR_CLICKHOUSE_DATABASE")
	envString(&c.ClickHouse.Username, "CR_CLICKHOUSE_USERNAME", "CR_CLICKHOUSE_USER")
	envString(&c.ClickHouse.Password, "CR_CLICKHOUSE_PASSWORD")
	envBool(&c.ClickHouse.TLS, "CR_CLICKHOUSE_TLS")
	envInt(&c.ClickHouse.BatchSize, "CR_CLICKHOUSE_BATCH_SIZE")
	envDuration(&c.ClickHouse.FlushInterval, "CR_CLICKHOUSE_FLUSH_INTERVAL")
	envInt(&c.ClickHouse.MaxRetries, "CR_CLICKHOUSE_MAX_RETRIES")
	envBool(&c.ClickHouse.Required, "CR_CLICKHOUSE_REQUIRED")

	// ---- nats ----
	envString(&c.NATS.URL, "CR_NATS_URL", "NATS_URL")
	envSlice(&c.NATS.URLs, "CR_NATS_URLS")
	envString(&c.NATS.Name, "CR_NATS_NAME")
	envString(&c.NATS.CredentialsFile, "CR_NATS_CREDENTIALS_FILE")
	envString(&c.NATS.NKeySeedFile, "CR_NATS_NKEY_SEED_FILE")
	envString(&c.NATS.Token, "CR_NATS_TOKEN")
	envBool(&c.NATS.JetStream, "CR_NATS_JETSTREAM")
	envInt(&c.NATS.StreamReplicas, "CR_NATS_STREAM_REPLICAS")
	envInt(&c.NATS.MaxReconnects, "CR_NATS_MAX_RECONNECTS")
	envDuration(&c.NATS.ReconnectWait, "CR_NATS_RECONNECT_WAIT")
	envBool(&c.NATS.Required, "CR_NATS_REQUIRED")

	// ---- auth ----
	envString(&c.Auth.HeaderName, "CR_AUTH_HEADER_NAME")
	envInt(&c.Auth.MinKeyLength, "CR_AUTH_MIN_KEY_LENGTH")
	envString(&c.Auth.KeyPrefix, "CR_AUTH_KEY_PREFIX")
	envDuration(&c.Auth.CacheTTL, "CR_AUTH_CACHE_TTL")
	envString(&c.Auth.AdminKeyEnv, "CR_ADMIN_KEY_ENV")
	// Accept the admin key directly as well, which is how the container image is
	// configured by most orchestrators.
	envString(&c.Auth.AdminKey, "CR_ADMIN_KEY")
	envString(&c.Auth.AllowAnonymousTenant, "CR_AUTH_ALLOW_ANONYMOUS_TENANT")

	// ---- routing defaults ----
	envString(&c.Routing.DefaultStrategy, "CR_ROUTING_DEFAULT_STRATEGY")
	envInt(&c.Routing.DefaultMaxAttempts, "CR_ROUTING_DEFAULT_MAX_ATTEMPTS")
	envInt(&c.Routing.DefaultRetry.MaxAttempts, "CR_ROUTING_RETRY_MAX_ATTEMPTS")
	envDuration(&c.Routing.DefaultRetry.InitialBackoff, "CR_ROUTING_RETRY_INITIAL_BACKOFF")
	envDuration(&c.Routing.DefaultRetry.MaxBackoff, "CR_ROUTING_RETRY_MAX_BACKOFF")
	envFloat(&c.Routing.DefaultRetry.Multiplier, "CR_ROUTING_RETRY_MULTIPLIER")
	envDuration(&c.Routing.DefaultTimeout.Total, "CR_ROUTING_TIMEOUT_TOTAL")
	envDuration(&c.Routing.DefaultTimeout.PerAttempt, "CR_ROUTING_TIMEOUT_PER_ATTEMPT")
	envDuration(&c.Routing.DefaultTimeout.Connect, "CR_ROUTING_TIMEOUT_CONNECT")
	envDuration(&c.Routing.DefaultTimeout.StreamIdle, "CR_ROUTING_TIMEOUT_STREAM_IDLE")
	envDuration(&c.Routing.DefaultTimeout.FirstToken, "CR_ROUTING_TIMEOUT_FIRST_TOKEN")
	envInt(&c.Routing.DefaultLimits.MaxOutputTokens, "CR_ROUTING_MAX_OUTPUT_TOKENS")
	envInt(&c.Routing.DefaultLimits.MaxPromptTokens, "CR_ROUTING_MAX_PROMPT_TOKENS")
	envInt(&c.Routing.DefaultLimits.RequestsPerMinute, "CR_ROUTING_REQUESTS_PER_MINUTE")
	envInt(&c.Routing.DefaultLimits.TokensPerMinute, "CR_ROUTING_TOKENS_PER_MINUTE")
	envFloat(&c.Routing.DefaultLimits.MaxCostPerRequestUSD, "CR_ROUTING_MAX_COST_PER_REQUEST_USD")
	envFloat(&c.Routing.DefaultLimits.DailyBudgetUSD, "CR_ROUTING_DAILY_BUDGET_USD")
	envFloat(&c.Routing.DefaultLimits.MonthlyBudgetUSD, "CR_ROUTING_MONTHLY_BUDGET_USD")
	envBool(&c.Routing.HealthCheckEnabled, "CR_ROUTING_HEALTH_CHECK_ENABLED")
	envDuration(&c.Routing.HealthCheckInterval, "CR_ROUTING_HEALTH_CHECK_INTERVAL")
	envDuration(&c.Routing.HealthCheckTimeout, "CR_ROUTING_HEALTH_CHECK_TIMEOUT")
	envDuration(&c.Routing.HealthWindow, "CR_ROUTING_HEALTH_WINDOW")

	// ---- cache ----
	envBool(&c.Cache.ResponseCache, "CR_CACHE_RESPONSE_ENABLED")
	envDuration(&c.Cache.ResponseTTL, "CR_CACHE_RESPONSE_TTL")
	envInt(&c.Cache.MaxResponseBytes, "CR_CACHE_MAX_RESPONSE_BYTES")
	envDuration(&c.Cache.ProviderHealthTTL, "CR_CACHE_PROVIDER_HEALTH_TTL")
	envDuration(&c.Cache.PolicyCacheTTL, "CR_CACHE_POLICY_TTL")
	envDuration(&c.Cache.RegistryCacheTTL, "CR_CACHE_REGISTRY_TTL")
	envBool(&c.Cache.SemanticEnabled, "CR_CACHE_SEMANTIC_ENABLED")
	envBool(&c.Cache.PrefixEnabled, "CR_CACHE_PREFIX_ENABLED")
	envFloat(&c.Cache.SemanticThreshold, "CR_CACHE_SEMANTIC_THRESHOLD")
	envInt(&c.Cache.PrefixLength, "CR_CACHE_PREFIX_LENGTH")
	envBool(&c.Cache.ExactEnabled, "CR_CACHE_EXACT_ENABLED")
	envBoolPtr(&c.Cache.BypassToolRequests, "CR_CACHE_BYPASS_TOOLS")
	envBool(&c.Cache.AllowNondeterministic, "CR_CACHE_ALLOW_NONDETERMINISTIC")
	envBoolPtr(&c.Cache.BypassLiveData, "CR_CACHE_BYPASS_LIVE_DATA")
	envInt(&c.Cache.MaxSemanticEntries, "CR_CACHE_MAX_SEMANTIC_ENTRIES")

	// ---- tools ----
	envBool(&c.Tools.GatewayExecution, "CR_TOOLS_GATEWAY_EXECUTION")

	// ---- tunnel ----
	envBool(&c.Tunnel.Enabled, "CR_TUNNEL_ENABLED")
	envString(&c.Tunnel.Binary, "CR_TUNNEL_BINARY")
	envString(&c.Tunnel.DefaultTarget, "CR_TUNNEL_DEFAULT_TARGET")
	envString(&c.Tunnel.DashboardTarget, "CR_TUNNEL_DASHBOARD_TARGET")
	envBool(&c.Tunnel.AllowCustomTargets, "CR_TUNNEL_ALLOW_CUSTOM_TARGETS")
	envBool(&c.Tunnel.AutoRestart, "CR_TUNNEL_AUTO_RESTART")
	envBool(&c.Tunnel.StopOnShutdown, "CR_TUNNEL_STOP_ON_SHUTDOWN")
	envDuration(&c.Tunnel.StartupTimeout, "CR_TUNNEL_STARTUP_TIMEOUT")

	// ---- phase 2: classifier / shaping / scoring / guardrails / eval ----
	envBool(&c.Classifier.Enabled, "CR_CLASSIFIER_ENABLED")
	envInt(&c.Classifier.LongContextTokens, "CR_CLASSIFIER_LONG_CONTEXT_TOKENS")
	envInt(&c.Classifier.BatchTokens, "CR_CLASSIFIER_BATCH_TOKENS")
	envBool(&c.Shaping.Enabled, "CR_SHAPING_ENABLED")
	envInt(&c.Shaping.MaxHistoryMessages, "CR_SHAPING_MAX_HISTORY")
	envInt(&c.Shaping.MaxPromptTokens, "CR_SHAPING_MAX_PROMPT_TOKENS")
	envString(&c.Shaping.SystemPrefix, "CR_SHAPING_SYSTEM_PREFIX")
	envString(&c.Shaping.SystemSuffix, "CR_SHAPING_SYSTEM_SUFFIX")
	envSlice(&c.Shaping.Guardrails, "CR_SHAPING_GUARDRAILS")
	envBool(&c.Scoring.Enabled, "CR_SCORING_ENABLED")
	envDuration(&c.Scoring.Window, "CR_SCORING_WINDOW")
	envFloat(&c.Scoring.SuccessWeight, "CR_SCORING_SUCCESS_WEIGHT")
	envFloat(&c.Scoring.LatencyWeight, "CR_SCORING_LATENCY_WEIGHT")
	envFloat(&c.Scoring.CostWeight, "CR_SCORING_COST_WEIGHT")
	envFloat(&c.Scoring.FeedbackWeight, "CR_SCORING_FEEDBACK_WEIGHT")
	envBool(&c.Guardrails.Enabled, "CR_GUARDRAILS_ENABLED")
	envBool(&c.Eval.Enabled, "CR_EVAL_ENABLED")
	envInt(&c.Eval.MaxRequests, "CR_EVAL_MAX_REQUESTS")
	envInt(&c.Eval.WorkerPool, "CR_EVAL_WORKER_POOL")

	// ---- telemetry ----
	envString(&c.Telemetry.ServiceName, "CR_OTEL_SERVICE_NAME")
	envString(&c.Telemetry.OTLPEndpoint, "CR_OTEL_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT")
	envBool(&c.Telemetry.OTLPInsecure, "CR_OTEL_INSECURE", "OTEL_EXPORTER_OTLP_INSECURE")
	envMap(&c.Telemetry.OTLPHeaders, "CR_OTEL_HEADERS")
	envBool(&c.Telemetry.TracesEnabled, "CR_OTEL_TRACES_ENABLED")
	envBool(&c.Telemetry.MetricsEnabled, "CR_OTEL_METRICS_ENABLED")
	envBool(&c.Telemetry.LogsEnabled, "CR_OTEL_LOGS_ENABLED")
	envFloat(&c.Telemetry.TraceSampleRatio, "CR_OTEL_TRACE_SAMPLE_RATIO")
	envBool(&c.Telemetry.PrometheusEnabled, "CR_PROMETHEUS_ENABLED")
	envString(&c.Telemetry.PrometheusPath, "CR_PROMETHEUS_PATH")
	envDuration(&c.Telemetry.ExportInterval, "CR_OTEL_EXPORT_INTERVAL")
	envInt(&c.Telemetry.TraceBufferSize, "CR_TELEMETRY_TRACE_BUFFER_SIZE")
	envBool(&c.Telemetry.PersistTraces, "CR_TELEMETRY_PERSIST_TRACES")

	// ---- logging ----
	envString(&c.Logging.Level, "CR_LOG_LEVEL", "LOG_LEVEL")
	envString(&c.Logging.Format, "CR_LOG_FORMAT", "LOG_FORMAT")
	envBool(&c.Logging.AddSource, "CR_LOG_ADD_SOURCE")
	envSlice(&c.Logging.RedactHeaders, "CR_LOG_REDACT_HEADERS")
	envBool(&c.Logging.LogRequestBodies, "CR_LOG_REQUEST_BODIES")
	envDuration(&c.Logging.SlowRequestThreshold, "CR_LOG_SLOW_REQUEST_THRESHOLD")

	// ---- admin ----
	envBool(&c.Admin.Enabled, "CR_ADMIN_ENABLED")
	envBool(&c.Admin.RequireScope, "CR_ADMIN_REQUIRE_SCOPE")
	envInt(&c.Admin.UsageRetentionDays, "CR_ADMIN_USAGE_RETENTION_DAYS")
	envInt(&c.Admin.TraceRetentionDays, "CR_ADMIN_TRACE_RETENTION_DAYS")
	envInt(&c.Admin.LogRetentionDays, "CR_ADMIN_LOG_RETENTION_DAYS")

	// Console operator login. A separate credential type from API keys: an
	// operator chooses a password, so it is stretched and lockable, while a key is
	// high-entropy material verified by digest lookup.
	envBool(&c.Admin.DashboardAuth.Enabled, "CR_ADMIN_DASHBOARD_ENABLED")
	envString(&c.Admin.DashboardAuth.CookieName, "CR_ADMIN_DASHBOARD_COOKIE_NAME")
	envBool(&c.Admin.DashboardAuth.CookieSecure, "CR_ADMIN_DASHBOARD_COOKIE_SECURE")
	envDuration(&c.Admin.DashboardAuth.SessionTTL, "CR_ADMIN_DASHBOARD_SESSION_TTL")
	envDuration(&c.Admin.DashboardAuth.IdleTTL, "CR_ADMIN_DASHBOARD_IDLE_TTL")
	envInt(&c.Admin.DashboardAuth.MinPasswordLength, "CR_ADMIN_DASHBOARD_MIN_PASSWORD_LENGTH")
	envInt(&c.Admin.DashboardAuth.MaxPasswordLength, "CR_ADMIN_DASHBOARD_MAX_PASSWORD_LENGTH")
	envInt(&c.Admin.DashboardAuth.MaxFailedAttempts, "CR_ADMIN_DASHBOARD_MAX_FAILED_ATTEMPTS")
	envDuration(&c.Admin.DashboardAuth.LockoutDuration, "CR_ADMIN_DASHBOARD_LOCKOUT_DURATION")
	envDuration(&c.Admin.DashboardAuth.MaxLockoutDuration, "CR_ADMIN_DASHBOARD_MAX_LOCKOUT_DURATION")

	return nil
}
