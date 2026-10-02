package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// EnvPrefix is the namespace for AstraRouter's own environment variables. Every
// setting is reachable as AR_<PATH>, so a container or systemd unit can be fully
// configured without a config file.
const EnvPrefix = "AR_"

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
	envString(&c.App.Name, "AR_APP_NAME")
	envString(&c.App.Environment, "AR_ENVIRONMENT", "AR_ENV")
	envString(&c.App.InstanceID, "AR_INSTANCE_ID")
	envString(&c.App.Region, "AR_REGION")
	envString(&c.App.DefaultTenantSlug, "AR_DEFAULT_TENANT_SLUG")
	envString(&c.App.DefaultTenantName, "AR_DEFAULT_TENANT_NAME")
	envBool(&c.App.BootstrapFromConfig, "AR_BOOTSTRAP_FROM_CONFIG")

	// ---- http ----
	envString(&c.HTTP.Addr, "AR_HTTP_ADDR", "AR_LISTEN_ADDR", "PORT_ADDR")
	envDuration(&c.HTTP.ReadHeaderTimeout, "AR_HTTP_READ_HEADER_TIMEOUT")
	envDuration(&c.HTTP.ReadTimeout, "AR_HTTP_READ_TIMEOUT")
	envDuration(&c.HTTP.WriteTimeout, "AR_HTTP_WRITE_TIMEOUT")
	envDuration(&c.HTTP.IdleTimeout, "AR_HTTP_IDLE_TIMEOUT")
	envDuration(&c.HTTP.ShutdownTimeout, "AR_HTTP_SHUTDOWN_TIMEOUT")
	envInt64(&c.HTTP.MaxBodyBytes, "AR_HTTP_MAX_BODY_BYTES")
	envSlice(&c.HTTP.TrustedProxies, "AR_HTTP_TRUSTED_PROXIES")
	envSlice(&c.HTTP.CORSAllowedOrigins, "AR_HTTP_CORS_ALLOWED_ORIGINS", "AR_CORS_ALLOWED_ORIGINS")
	envBool(&c.HTTP.EnablePprof, "AR_HTTP_ENABLE_PPROF")

	// ---- database ----
	// A single DATABASE_URL is the conventional container spelling; the AR_
	// prefixed aliases exist so the gateway can share a host with other
	// services without variable collisions.
	envString(&c.Database.DSN, "AR_DATABASE_DSN", "AR_POSTGRES_DSN", "DATABASE_URL")
	envString(&c.Database.Host, "AR_POSTGRES_HOST", "AR_DATABASE_HOST")
	envInt(&c.Database.Port, "AR_POSTGRES_PORT", "AR_DATABASE_PORT")
	envString(&c.Database.User, "AR_POSTGRES_USER", "AR_DATABASE_USER")
	envString(&c.Database.Password, "AR_POSTGRES_PASSWORD", "AR_DATABASE_PASSWORD")
	envString(&c.Database.Name, "AR_POSTGRES_DB", "AR_POSTGRES_NAME", "AR_DATABASE_NAME")
	envString(&c.Database.SSLMode, "AR_POSTGRES_SSL_MODE", "AR_DATABASE_SSL_MODE")
	envInt32(&c.Database.MaxConns, "AR_POSTGRES_MAX_CONNS")
	envInt32(&c.Database.MinConns, "AR_POSTGRES_MIN_CONNS")
	envDuration(&c.Database.MaxConnLifetime, "AR_POSTGRES_MAX_CONN_LIFETIME")
	envDuration(&c.Database.MaxConnIdleTime, "AR_POSTGRES_MAX_CONN_IDLE_TIME")
	envDuration(&c.Database.ConnectTimeout, "AR_POSTGRES_CONNECT_TIMEOUT")
	envDuration(&c.Database.StatementTimeout, "AR_POSTGRES_STATEMENT_TIMEOUT")
	envBool(&c.Database.AutoMigrate, "AR_POSTGRES_AUTO_MIGRATE", "AR_AUTO_MIGRATE")
	envString(&c.Database.MigrationsDir, "AR_MIGRATIONS_DIR")

	// ---- redis ----
	envString(&c.Redis.Addr, "AR_REDIS_ADDR", "REDIS_URL")
	envString(&c.Redis.Username, "AR_REDIS_USERNAME")
	envString(&c.Redis.Password, "AR_REDIS_PASSWORD")
	envInt(&c.Redis.DB, "AR_REDIS_DB")
	envSlice(&c.Redis.SentinelAddrs, "AR_REDIS_SENTINEL_ADDRS")
	envString(&c.Redis.MasterName, "AR_REDIS_MASTER_NAME")
	envInt(&c.Redis.PoolSize, "AR_REDIS_POOL_SIZE")
	envDuration(&c.Redis.DialTimeout, "AR_REDIS_DIAL_TIMEOUT")
	envDuration(&c.Redis.ReadTimeout, "AR_REDIS_READ_TIMEOUT")
	envDuration(&c.Redis.WriteTimeout, "AR_REDIS_WRITE_TIMEOUT")
	envString(&c.Redis.KeyPrefix, "AR_REDIS_KEY_PREFIX")
	envBool(&c.Redis.TLS, "AR_REDIS_TLS")
	envBool(&c.Redis.Required, "AR_REDIS_REQUIRED")

	// ---- clickhouse ----
	envString(&c.ClickHouse.DSN, "AR_CLICKHOUSE_DSN")
	envString(&c.ClickHouse.Addr, "AR_CLICKHOUSE_ADDR")
	envString(&c.ClickHouse.Database, "AR_CLICKHOUSE_DB", "AR_CLICKHOUSE_DATABASE")
	envString(&c.ClickHouse.Username, "AR_CLICKHOUSE_USERNAME", "AR_CLICKHOUSE_USER")
	envString(&c.ClickHouse.Password, "AR_CLICKHOUSE_PASSWORD")
	envBool(&c.ClickHouse.TLS, "AR_CLICKHOUSE_TLS")
	envInt(&c.ClickHouse.BatchSize, "AR_CLICKHOUSE_BATCH_SIZE")
	envDuration(&c.ClickHouse.FlushInterval, "AR_CLICKHOUSE_FLUSH_INTERVAL")
	envInt(&c.ClickHouse.MaxRetries, "AR_CLICKHOUSE_MAX_RETRIES")
	envBool(&c.ClickHouse.Required, "AR_CLICKHOUSE_REQUIRED")

	// ---- nats ----
	envString(&c.NATS.URL, "AR_NATS_URL", "NATS_URL")
	envSlice(&c.NATS.URLs, "AR_NATS_URLS")
	envString(&c.NATS.Name, "AR_NATS_NAME")
	envString(&c.NATS.CredentialsFile, "AR_NATS_CREDENTIALS_FILE")
	envString(&c.NATS.NKeySeedFile, "AR_NATS_NKEY_SEED_FILE")
	envString(&c.NATS.Token, "AR_NATS_TOKEN")
	envBool(&c.NATS.JetStream, "AR_NATS_JETSTREAM")
	envInt(&c.NATS.StreamReplicas, "AR_NATS_STREAM_REPLICAS")
	envInt(&c.NATS.MaxReconnects, "AR_NATS_MAX_RECONNECTS")
	envDuration(&c.NATS.ReconnectWait, "AR_NATS_RECONNECT_WAIT")
	envBool(&c.NATS.Required, "AR_NATS_REQUIRED")

	// ---- auth ----
	envString(&c.Auth.HeaderName, "AR_AUTH_HEADER_NAME")
	envInt(&c.Auth.MinKeyLength, "AR_AUTH_MIN_KEY_LENGTH")
	envString(&c.Auth.KeyPrefix, "AR_AUTH_KEY_PREFIX")
	envDuration(&c.Auth.CacheTTL, "AR_AUTH_CACHE_TTL")
	envString(&c.Auth.AdminKeyEnv, "AR_ADMIN_KEY_ENV")
	// Accept the admin key directly as well, which is how the container image is
	// configured by most orchestrators.
	envString(&c.Auth.AdminKey, "AR_ADMIN_KEY")
	envString(&c.Auth.AllowAnonymousTenant, "AR_AUTH_ALLOW_ANONYMOUS_TENANT")

	// ---- routing defaults ----
	envString(&c.Routing.DefaultStrategy, "AR_ROUTING_DEFAULT_STRATEGY")
	envInt(&c.Routing.DefaultMaxAttempts, "AR_ROUTING_DEFAULT_MAX_ATTEMPTS")
	envInt(&c.Routing.DefaultRetry.MaxAttempts, "AR_ROUTING_RETRY_MAX_ATTEMPTS")
	envDuration(&c.Routing.DefaultRetry.InitialBackoff, "AR_ROUTING_RETRY_INITIAL_BACKOFF")
	envDuration(&c.Routing.DefaultRetry.MaxBackoff, "AR_ROUTING_RETRY_MAX_BACKOFF")
	envFloat(&c.Routing.DefaultRetry.Multiplier, "AR_ROUTING_RETRY_MULTIPLIER")
	envDuration(&c.Routing.DefaultTimeout.Total, "AR_ROUTING_TIMEOUT_TOTAL")
	envDuration(&c.Routing.DefaultTimeout.PerAttempt, "AR_ROUTING_TIMEOUT_PER_ATTEMPT")
	envDuration(&c.Routing.DefaultTimeout.Connect, "AR_ROUTING_TIMEOUT_CONNECT")
	envDuration(&c.Routing.DefaultTimeout.StreamIdle, "AR_ROUTING_TIMEOUT_STREAM_IDLE")
	envDuration(&c.Routing.DefaultTimeout.FirstToken, "AR_ROUTING_TIMEOUT_FIRST_TOKEN")
	envInt(&c.Routing.DefaultLimits.MaxOutputTokens, "AR_ROUTING_MAX_OUTPUT_TOKENS")
	envInt(&c.Routing.DefaultLimits.MaxPromptTokens, "AR_ROUTING_MAX_PROMPT_TOKENS")
	envInt(&c.Routing.DefaultLimits.RequestsPerMinute, "AR_ROUTING_REQUESTS_PER_MINUTE")
	envInt(&c.Routing.DefaultLimits.TokensPerMinute, "AR_ROUTING_TOKENS_PER_MINUTE")
	envFloat(&c.Routing.DefaultLimits.MaxCostPerRequestUSD, "AR_ROUTING_MAX_COST_PER_REQUEST_USD")
	envFloat(&c.Routing.DefaultLimits.DailyBudgetUSD, "AR_ROUTING_DAILY_BUDGET_USD")
	envFloat(&c.Routing.DefaultLimits.MonthlyBudgetUSD, "AR_ROUTING_MONTHLY_BUDGET_USD")
	envBool(&c.Routing.HealthCheckEnabled, "AR_ROUTING_HEALTH_CHECK_ENABLED")
	envDuration(&c.Routing.HealthCheckInterval, "AR_ROUTING_HEALTH_CHECK_INTERVAL")
	envDuration(&c.Routing.HealthCheckTimeout, "AR_ROUTING_HEALTH_CHECK_TIMEOUT")
	envDuration(&c.Routing.HealthWindow, "AR_ROUTING_HEALTH_WINDOW")

	// ---- cache ----
	envBool(&c.Cache.ResponseCache, "AR_CACHE_RESPONSE_ENABLED")
	envDuration(&c.Cache.ResponseTTL, "AR_CACHE_RESPONSE_TTL")
	envInt(&c.Cache.MaxResponseBytes, "AR_CACHE_MAX_RESPONSE_BYTES")
	envDuration(&c.Cache.ProviderHealthTTL, "AR_CACHE_PROVIDER_HEALTH_TTL")
	envDuration(&c.Cache.PolicyCacheTTL, "AR_CACHE_POLICY_TTL")
	envDuration(&c.Cache.RegistryCacheTTL, "AR_CACHE_REGISTRY_TTL")
	envBool(&c.Cache.SemanticEnabled, "AR_CACHE_SEMANTIC_ENABLED")
	envBool(&c.Cache.PrefixEnabled, "AR_CACHE_PREFIX_ENABLED")
	envFloat(&c.Cache.SemanticThreshold, "AR_CACHE_SEMANTIC_THRESHOLD")
	envInt(&c.Cache.PrefixLength, "AR_CACHE_PREFIX_LENGTH")
	envBool(&c.Cache.ExactEnabled, "AR_CACHE_EXACT_ENABLED")
	envBoolPtr(&c.Cache.BypassToolRequests, "AR_CACHE_BYPASS_TOOLS")
	envBool(&c.Cache.AllowNondeterministic, "AR_CACHE_ALLOW_NONDETERMINISTIC")
	envBoolPtr(&c.Cache.BypassLiveData, "AR_CACHE_BYPASS_LIVE_DATA")
	envInt(&c.Cache.MaxSemanticEntries, "AR_CACHE_MAX_SEMANTIC_ENTRIES")

	// ---- tools ----
	envBool(&c.Tools.GatewayExecution, "AR_TOOLS_GATEWAY_EXECUTION")

	// ---- tunnel ----
	envBool(&c.Tunnel.Enabled, "AR_TUNNEL_ENABLED")
	envString(&c.Tunnel.Binary, "AR_TUNNEL_BINARY")
	envString(&c.Tunnel.DefaultTarget, "AR_TUNNEL_DEFAULT_TARGET")
	envString(&c.Tunnel.DashboardTarget, "AR_TUNNEL_DASHBOARD_TARGET")
	envBool(&c.Tunnel.AllowCustomTargets, "AR_TUNNEL_ALLOW_CUSTOM_TARGETS")
	envBool(&c.Tunnel.AutoRestart, "AR_TUNNEL_AUTO_RESTART")
	envBool(&c.Tunnel.StopOnShutdown, "AR_TUNNEL_STOP_ON_SHUTDOWN")
	envDuration(&c.Tunnel.StartupTimeout, "AR_TUNNEL_STARTUP_TIMEOUT")

	// ---- phase 2: classifier / shaping / scoring / guardrails / eval ----
	envBool(&c.Classifier.Enabled, "AR_CLASSIFIER_ENABLED")
	envInt(&c.Classifier.LongContextTokens, "AR_CLASSIFIER_LONG_CONTEXT_TOKENS")
	envInt(&c.Classifier.BatchTokens, "AR_CLASSIFIER_BATCH_TOKENS")
	envBool(&c.Shaping.Enabled, "AR_SHAPING_ENABLED")
	envInt(&c.Shaping.MaxHistoryMessages, "AR_SHAPING_MAX_HISTORY")
	envInt(&c.Shaping.MaxPromptTokens, "AR_SHAPING_MAX_PROMPT_TOKENS")
	envString(&c.Shaping.SystemPrefix, "AR_SHAPING_SYSTEM_PREFIX")
	envString(&c.Shaping.SystemSuffix, "AR_SHAPING_SYSTEM_SUFFIX")
	envSlice(&c.Shaping.Guardrails, "AR_SHAPING_GUARDRAILS")
	envBool(&c.Scoring.Enabled, "AR_SCORING_ENABLED")
	envDuration(&c.Scoring.Window, "AR_SCORING_WINDOW")
	envFloat(&c.Scoring.SuccessWeight, "AR_SCORING_SUCCESS_WEIGHT")
	envFloat(&c.Scoring.LatencyWeight, "AR_SCORING_LATENCY_WEIGHT")
	envFloat(&c.Scoring.CostWeight, "AR_SCORING_COST_WEIGHT")
	envFloat(&c.Scoring.FeedbackWeight, "AR_SCORING_FEEDBACK_WEIGHT")
	envBool(&c.Guardrails.Enabled, "AR_GUARDRAILS_ENABLED")
	envBool(&c.Eval.Enabled, "AR_EVAL_ENABLED")
	envInt(&c.Eval.MaxRequests, "AR_EVAL_MAX_REQUESTS")
	envInt(&c.Eval.WorkerPool, "AR_EVAL_WORKER_POOL")

	// ---- telemetry ----
	envString(&c.Telemetry.ServiceName, "AR_OTEL_SERVICE_NAME")
	envString(&c.Telemetry.OTLPEndpoint, "AR_OTEL_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT")
	envBool(&c.Telemetry.OTLPInsecure, "AR_OTEL_INSECURE", "OTEL_EXPORTER_OTLP_INSECURE")
	envMap(&c.Telemetry.OTLPHeaders, "AR_OTEL_HEADERS")
	envBool(&c.Telemetry.TracesEnabled, "AR_OTEL_TRACES_ENABLED")
	envBool(&c.Telemetry.MetricsEnabled, "AR_OTEL_METRICS_ENABLED")
	envBool(&c.Telemetry.LogsEnabled, "AR_OTEL_LOGS_ENABLED")
	envFloat(&c.Telemetry.TraceSampleRatio, "AR_OTEL_TRACE_SAMPLE_RATIO")
	envBool(&c.Telemetry.PrometheusEnabled, "AR_PROMETHEUS_ENABLED")
	envString(&c.Telemetry.PrometheusPath, "AR_PROMETHEUS_PATH")
	envDuration(&c.Telemetry.ExportInterval, "AR_OTEL_EXPORT_INTERVAL")
	envInt(&c.Telemetry.TraceBufferSize, "AR_TELEMETRY_TRACE_BUFFER_SIZE")
	envBool(&c.Telemetry.PersistTraces, "AR_TELEMETRY_PERSIST_TRACES")

	// ---- logging ----
	envString(&c.Logging.Level, "AR_LOG_LEVEL", "LOG_LEVEL")
	envString(&c.Logging.Format, "AR_LOG_FORMAT", "LOG_FORMAT")
	envBool(&c.Logging.AddSource, "AR_LOG_ADD_SOURCE")
	envSlice(&c.Logging.RedactHeaders, "AR_LOG_REDACT_HEADERS")
	envBool(&c.Logging.LogRequestBodies, "AR_LOG_REQUEST_BODIES")
	envDuration(&c.Logging.SlowRequestThreshold, "AR_LOG_SLOW_REQUEST_THRESHOLD")

	// ---- admin ----
	envBool(&c.Admin.Enabled, "AR_ADMIN_ENABLED")
	envBool(&c.Admin.RequireScope, "AR_ADMIN_REQUIRE_SCOPE")
	envInt(&c.Admin.UsageRetentionDays, "AR_ADMIN_USAGE_RETENTION_DAYS")
	envInt(&c.Admin.TraceRetentionDays, "AR_ADMIN_TRACE_RETENTION_DAYS")
	envInt(&c.Admin.LogRetentionDays, "AR_ADMIN_LOG_RETENTION_DAYS")

	// Console operator login. A separate credential type from API keys: an
	// operator chooses a password, so it is stretched and lockable, while a key is
	// high-entropy material verified by digest lookup.
	envBool(&c.Admin.DashboardAuth.Enabled, "AR_ADMIN_DASHBOARD_ENABLED")
	envString(&c.Admin.DashboardAuth.CookieName, "AR_ADMIN_DASHBOARD_COOKIE_NAME")
	envBool(&c.Admin.DashboardAuth.CookieSecure, "AR_ADMIN_DASHBOARD_COOKIE_SECURE")
	envDuration(&c.Admin.DashboardAuth.SessionTTL, "AR_ADMIN_DASHBOARD_SESSION_TTL")
	envDuration(&c.Admin.DashboardAuth.IdleTTL, "AR_ADMIN_DASHBOARD_IDLE_TTL")
	envInt(&c.Admin.DashboardAuth.MinPasswordLength, "AR_ADMIN_DASHBOARD_MIN_PASSWORD_LENGTH")
	envInt(&c.Admin.DashboardAuth.MaxPasswordLength, "AR_ADMIN_DASHBOARD_MAX_PASSWORD_LENGTH")
	envInt(&c.Admin.DashboardAuth.MaxFailedAttempts, "AR_ADMIN_DASHBOARD_MAX_FAILED_ATTEMPTS")
	envDuration(&c.Admin.DashboardAuth.LockoutDuration, "AR_ADMIN_DASHBOARD_LOCKOUT_DURATION")
	envDuration(&c.Admin.DashboardAuth.MaxLockoutDuration, "AR_ADMIN_DASHBOARD_MAX_LOCKOUT_DURATION")

	return nil
}
