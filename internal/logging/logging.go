// Package logging provides AstraRouter's structured logger and the context
// plumbing that correlates log lines with requests and traces.
//
// The logger is log/slog from the standard library. A dedicated wrapper adds
// three things the raw handler does not: configuration from the config file, a
// request-scoped child logger carried on the context, and header redaction that
// is applied centrally rather than at every call site.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/shadowsafin/astrarouter/internal/config"
)

// Level parses a configuration level into a slog level.
func Level(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Options customizes logger construction. Every field is optional so callers
// can construct a logger for tests with no arguments.
type Options struct {
	// Writer is the destination, defaulting to stdout.
	Writer io.Writer
	// Level overrides cfg.Level when non-zero.
	Level slog.Level
	// Format overrides cfg.Format when non-empty.
	Format string
	// AddSource overrides cfg.AddSource.
	AddSource bool
	// Service and Instance are attached to every record. They duplicate OTel
	// resource attributes deliberately: logs are often shipped by a different
	// agent than traces, and a log line that cannot identify its origin is
	// useless during an incident.
	Service  string
	Instance string
	// Environment distinguishes production from development records.
	Environment string
}

// New builds a logger from configuration.
func New(cfg config.LoggingConfig, opts Options) *slog.Logger {
	w := opts.Writer
	if w == nil {
		w = os.Stdout
	}

	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		format = strings.ToLower(strings.TrimSpace(cfg.Format))
	}

	lvl := opts.Level
	if lvl == 0 {
		lvl = Level(cfg.Level)
	}

	handlerOpts := &slog.HandlerOptions{
		Level:     lvl,
		AddSource: opts.AddSource || cfg.AddSource,
		// Rename the built-in keys so log pipelines can index "severity" and
		// "message" consistently regardless of which slog version is in use.
		ReplaceAttr: replaceAttr,
	}

	var handler slog.Handler
	switch format {
	case "console", "text":
		handler = slog.NewTextHandler(w, handlerOpts)
	default:
		handler = slog.NewJSONHandler(w, handlerOpts)
	}

	attrs := []any{}
	if opts.Service != "" {
		attrs = append(attrs, slog.String("service", opts.Service))
	}
	if opts.Instance != "" {
		attrs = append(attrs, slog.String("instance", opts.Instance))
	}
	if opts.Environment != "" {
		attrs = append(attrs, slog.String("environment", opts.Environment))
	}

	logger := slog.New(handler)
	if len(attrs) > 0 {
		logger = logger.With(attrs...)
	}
	return logger
}

// replaceAttr normalizes a few attribute keys and renders durations readably.
func replaceAttr(_ []string, a slog.Attr) slog.Attr {
	switch a.Key {
	case slog.LevelKey:
		a.Key = "severity"
	case slog.MessageKey:
		a.Key = "message"
	case slog.TimeKey:
		a.Key = "timestamp"
	}
	if d, ok := a.Value.Any().(time.Duration); ok {
		// Durations in JSON are otherwise emitted as an opaque nanosecond
		// integer, which is unreadable in a log viewer.
		return slog.String(a.Key, d.String())
	}
	return a
}

// contextKey is an unexported type so no other package can collide with our
// context keys.
type contextKey struct{ name string }

var loggerKey = &contextKey{name: "logger"}

// WithLogger returns a context carrying logger.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// FromContext returns the request-scoped logger, or the supplied default when
// none is present. Handlers always have a default from dependency injection, so
// call sites never have to handle a nil logger.
func FromContext(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok && l != nil {
		return l
	}
	if fallback != nil {
		return fallback
	}
	// A last-resort logger rather than a panic: logging must never be the
	// component that takes down the gateway.
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// With returns a context whose logger has the given attributes attached.
func With(ctx context.Context, fallback *slog.Logger, attrs ...any) context.Context {
	return WithLogger(ctx, FromContext(ctx, fallback).With(attrs...))
}

// RequestAttrs is the canonical attribute set for a request-scoped log line.
// Centralizing it keeps log field names stable across packages, which is what
// makes dashboard queries and Loki filters reliable.
type RequestAttrs struct {
	RequestID      string
	TraceID        string
	TenantID       string
	APIKeyID       string
	Provider       string
	Model          string
	RequestedModel string
	PolicyID       string
	Attempts       int
	FallbackUsed   bool
	LatencyMS      int64
	StatusCode     int
}

// Args renders the attributes as slog arguments, omitting empty values so log
// lines stay compact.
func (r RequestAttrs) Args() []any {
	args := make([]any, 0, 20)
	if r.RequestID != "" {
		args = append(args, slog.String("request_id", r.RequestID))
	}
	if r.TraceID != "" {
		args = append(args, slog.String("trace_id", r.TraceID))
	}
	if r.TenantID != "" {
		args = append(args, slog.String("tenant_id", r.TenantID))
	}
	if r.APIKeyID != "" {
		args = append(args, slog.String("api_key_id", r.APIKeyID))
	}
	if r.Provider != "" {
		args = append(args, slog.String("provider", r.Provider))
	}
	if r.Model != "" {
		args = append(args, slog.String("model", r.Model))
	}
	if r.RequestedModel != "" {
		args = append(args, slog.String("requested_model", r.RequestedModel))
	}
	if r.PolicyID != "" {
		args = append(args, slog.String("policy_id", r.PolicyID))
	}
	if r.Attempts > 0 {
		args = append(args, slog.Int("attempts", r.Attempts))
	}
	args = append(args,
		slog.Bool("fallback_used", r.FallbackUsed),
		slog.Int64("latency_ms", r.LatencyMS),
	)
	if r.StatusCode > 0 {
		args = append(args, slog.Int("status_code", r.StatusCode))
	}
	return args
}

// Redactor removes configured headers from a header map before logging.
type Redactor struct {
	// blocked is the lower-cased set of header names to drop.
	blocked map[string]struct{}
}

// NewRedactor builds a redactor from a configuration list. The common
// credential headers are always blocked, even if the operator's list omits them:
// leaking an Authorization header into a log aggregator is severe enough that it
// must not depend on configuration being right.
func NewRedactor(headers []string) *Redactor {
	blocked := map[string]struct{}{
		"authorization":        {},
		"x-api-key":            {},
		"api-key":              {},
		"cookie":               {},
		"set-cookie":           {},
		"proxy-authorization":  {},
		"x-amz-security-token": {},
	}
	for _, h := range headers {
		blocked[strings.ToLower(strings.TrimSpace(h))] = struct{}{}
	}
	return &Redactor{blocked: blocked}
}

// Redact returns a copy of headers with sensitive values replaced by a fixed
// marker. The key is retained so a missing credential is still diagnosable.
func (r *Redactor) Redact(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		if r == nil {
			out[k] = v
			continue
		}
		if _, blocked := r.blocked[strings.ToLower(k)]; blocked {
			out[k] = "[REDACTED]"
			continue
		}
		out[k] = v
	}
	return out
}

// SafeError renders an error for logging without leaking credentials that may
// be embedded in a provider error message (some upstreams echo the request).
func SafeError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	for _, marker := range []string{"sk-", "Bearer ", "api_key=", "api-key=", "apikey="} {
		if idx := strings.Index(msg, marker); idx >= 0 {
			end := idx + len(marker)
			// Truncate the remainder of the token up to the next separator.
			rest := msg[end:]
			if cut := strings.IndexAny(rest, " \t\n\"',;}"); cut >= 0 {
				rest = rest[cut:]
			} else {
				rest = ""
			}
			msg = msg[:end] + "[REDACTED]" + rest
		}
	}
	return msg
}

// Fatal logs at error level and returns, leaving process exit to the caller.
// Exiting here would make the function untestable and hide the call site.
func Fatal(logger *slog.Logger, msg string, args ...any) {
	logger.Error(msg, args...)
}

// ParseLevel is a convenience for tests and for the CLI's --log-level flag. It
// reports whether the name was recognised so callers can reject typos.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q", name)
	}
}
