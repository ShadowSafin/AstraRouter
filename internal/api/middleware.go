package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/shadowsafin/synapass/internal/auth"
	"github.com/shadowsafin/synapass/internal/domain"
	"github.com/shadowsafin/synapass/internal/logging"
)

// contextKey is an unexported type so no other package can collide with the keys
// this package stores on a request context.
type contextKey string

const (
	ctxKeyRequestContext contextKey = "request_context"
	ctxKeyPrincipal      contextKey = "principal"
	ctxKeyLogger         contextKey = "logger"
)

// requestContext returns the request context attached by the identity middleware.
func requestContext(ctx context.Context) *domain.RequestContext {
	rc, _ := ctx.Value(ctxKeyRequestContext).(*domain.RequestContext)
	return rc
}

// principal returns the authenticated principal attached by the auth middleware.
func principal(ctx context.Context) *auth.Principal {
	p, _ := ctx.Value(ctxKeyPrincipal).(*auth.Principal)
	return p
}

// requestIDHeader is the header used to accept and echo a correlation id.
//
// Accepting a client-supplied id lets a caller correlate a gateway request with
// its own logs. It is not trusted for anything except correlation, and it is
// bounded in length so a client cannot inject a megabyte-long header into every
// log line.
const (
	requestIDHeader = "X-Request-ID"
	maxRequestIDLen = 128
	traceHeader     = "X-Trace-ID"
)

// ---------------------------------------------------------------------------
// Identity and tracing
// ---------------------------------------------------------------------------

// identityMiddleware assigns a request id, starts a span and builds the request
// context.
//
// Building the request context here, in one place, is what keeps handlers thin:
// every handler receives a fully populated context and never has to know how a
// correlation id or a client address is derived.
func (s *Server) identityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		requestID := sanitizeRequestID(r.Header.Get(requestIDHeader))
		if requestID == "" {
			requestID = domain.NewID()
		}

		// The span is started before the request context exists so the trace id can
		// be propagated into it, which is what makes the log line, the trace and the
		// response header all carry the same identifier.
		ctx, span := s.tracer.Start(r.Context(), spanName(r),
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.request.method", r.Method),
				attribute.String("url.path", r.URL.Path),
				attribute.String("synapass.request_id", requestID),
			),
		)
		defer span.End()

		rc := &domain.RequestContext{
			RequestID:   domain.RequestID(requestID),
			ReceivedAt:  start,
			ClientIP:    s.clientIP(r),
			UserAgent:   truncateString(r.UserAgent(), 512),
			Headers:     flattenHeaders(r.Header, s.redactor),
			RequestType: requestTypeFor(r.URL.Path),
			Labels:      map[string]string{},
		}
		if sc := span.SpanContext(); sc.IsValid() {
			rc.TraceID = sc.TraceID().String()
			rc.SpanID = sc.SpanID().String()
		}

		// Echo both identifiers so a client can quote them in a support request.
		w.Header().Set(requestIDHeader, requestID)
		if rc.TraceID != "" {
			w.Header().Set(traceHeader, rc.TraceID)
		}

		logger := s.logger.With(
			slog.String("request_id", requestID),
			slog.String("trace_id", rc.TraceID),
		)
		ctx = logging.WithLogger(ctx, logger)
		ctx = context.WithValue(ctx, ctxKeyRequestContext, rc)
		ctx = context.WithValue(ctx, ctxKeyLogger, logger)

		// The response wrapper records the status and byte count without buffering
		// the body, so streaming responses still work through it.
		recorder := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r.WithContext(ctx))

		s.finishRequest(span, recorder, rc, start)
	})
}

// finishRequest closes the span and emits the access log line.
func (s *Server) finishRequest(span trace.Span, rec *responseRecorder, rc *domain.RequestContext, start time.Time) {
	elapsed := time.Since(start)

	span.SetAttributes(
		attribute.Int("http.response.status_code", rec.status),
		attribute.Int64("http.response.body.size", rec.bytes),
	)
	if rec.status >= 500 {
		span.SetStatus(codes.Error, http.StatusText(rec.status))
	}

	attrs := logging.RequestAttrs{
		RequestID:  rc.RequestID.String(),
		TraceID:    rc.TraceID,
		TenantID:   rc.TenantID(),
		APIKeyID:   rc.APIKeyID(),
		LatencyMS:  elapsed.Milliseconds(),
		StatusCode: rec.status,
	}
	if rc.Resolution != nil {
		attrs.Provider = rc.Resolution.Chosen.ProviderName
		attrs.Model = rc.Resolution.Chosen.Model
		attrs.PolicyID = rc.Resolution.PolicyID
	}

	logger := logging.FromContext(context.Background(), s.logger)
	args := append(attrs.Args(),
		slog.String("method", rc.Labels["method"]),
		slog.String("path", rc.Labels["path"]),
	)

	switch {
	case rec.status >= 500:
		logger.Error("request completed with a server error", args...)
	case rec.status >= 400:
		logger.Warn("request completed with a client error", args...)
	case s.config.Logging.SlowRequestThreshold.Std() > 0 && elapsed > s.config.Logging.SlowRequestThreshold.Std():
		// A dedicated warning for slow-but-successful requests makes latency
		// regressions visible without a dashboard.
		logger.Warn("slow request", args...)
	default:
		logger.Info("request completed", args...)
	}
}

// spanName renders a low-cardinality span name.
//
// The raw path is deliberately not used: with thousands of request ids flowing
// through, a per-path span name would make the trace backend's index enormous.
// Named routes are used instead.
func spanName(r *http.Request) string {
	path := r.URL.Path
	switch {
	case strings.HasPrefix(path, "/v1/chat/completions"):
		return "POST /v1/chat/completions"
	case strings.HasPrefix(path, "/v1/models"):
		return "GET /v1/models"
	case strings.HasPrefix(path, "/admin/"):
		return r.Method + " /admin/*"
	case strings.HasPrefix(path, "/health"), strings.HasPrefix(path, "/ready"):
		return "GET /health"
	case strings.HasPrefix(path, pathMetrics):
		return "GET /metrics"
	default:
		return r.Method + " " + path
	}
}

// requestTypeFor maps a path onto the API surface it represents.
func requestTypeFor(path string) domain.RequestType {
	switch {
	case strings.HasPrefix(path, "/v1/chat/completions"):
		return domain.RequestTypeChatCompletion
	case strings.HasPrefix(path, "/v1/completions"):
		return domain.RequestTypeCompletion
	case strings.HasPrefix(path, "/v1/embeddings"):
		return domain.RequestTypeEmbedding
	default:
		return ""
	}
}

// sanitizeRequestID validates a client-supplied correlation id.
//
// A client-supplied value ends up in log lines and response headers, so it must be
// restricted to a safe character set. Anything else is discarded and replaced with
// a generated id rather than being sanitized in place, because a mangled id is
// worse than a fresh one.
func sanitizeRequestID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxRequestIDLen {
		return ""
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == ':':
		default:
			return ""
		}
	}
	return raw
}

// clientIP resolves the client address, honouring X-Forwarded-For only from a
// trusted proxy.
//
// Trusting the header unconditionally would let any caller forge its own address,
// which would poison rate limiting and abuse investigation. When no trusted proxy
// is configured the header is ignored entirely.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	if len(s.trustedProxies) == 0 {
		return host
	}
	remote := net.ParseIP(host)
	if remote == nil {
		return host
	}

	trusted := false
	for _, network := range s.trustedProxies {
		if network.Contains(remote) {
			trusted = true
			break
		}
	}
	if !trusted {
		return host
	}

	// Left-most entry is the original client; it is the first address a fully
	// trusted proxy chain saw.
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded == "" {
		return host
	}
	if idx := strings.Index(forwarded, ","); idx > 0 {
		return strings.TrimSpace(forwarded[:idx])
	}
	return strings.TrimSpace(forwarded)
}

// flattenHeaders renders headers as a map with credentials redacted.
func flattenHeaders(header http.Header, redactor *logging.Redactor) map[string]string {
	out := make(map[string]string, len(header))
	for name, values := range header {
		if len(values) == 0 {
			continue
		}
		out[name] = strings.Join(values, ", ")
	}
	return redactor.Redact(out)
}

// ---------------------------------------------------------------------------
// Panic recovery
// ---------------------------------------------------------------------------

// recoverMiddleware converts a panic into a 500 so one bad request cannot take the
// process down.
//
// The stack is logged at error level but never returned to the client: a stack trace
// in a response body leaks internal paths and file names.
func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				rc := requestContext(r.Context())
				logger := logging.FromContext(r.Context(), s.logger)
				if rc != nil {
					logger.Error("recovered from a panic",
						"panic", recovered,
						"request_id", rc.RequestID.String(),
						"path", r.URL.Path,
						"stack", stackTrace(),
					)
				} else {
					logger.Error("recovered from a panic", "panic", recovered, "path", r.URL.Path, "stack", stackTrace())
				}
				if s.metrics != nil {
					s.metrics.ObserveAuthFailure("panic")
				}
				writeError(w, domain.NewError(domain.ErrCodeInternal,
					"an internal error occurred while handling the request"), nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// CORS
// ---------------------------------------------------------------------------

// corsMiddleware allows the dashboard origin to call the API from a browser.
//
// The allowed list is explicit; a wildcard would let any site use a logged-in
// operator's browser to make administrative calls against the gateway.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	allowed := map[string]struct{}{}
	for _, origin := range s.config.HTTP.CORSAllowedOrigins {
		allowed[strings.ToLower(strings.TrimSpace(origin))] = struct{}{}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}

		_, ok := allowed[strings.ToLower(origin)]
		// A development wildcard is honoured only in a non-production
		// environment, which is also enforced by configuration validation.
		if _, wildcard := allowed["*"]; wildcard && s.config.IsDevelopment() {
			ok = true
		}
		if !ok {
			// An unlisted origin is not rejected with an error: it simply receives
			// no CORS headers, which is what the browser needs to block it.
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers",
			"Authorization, Content-Type, X-Request-ID, X-Synapass-Tenant")
		w.Header().Set("Access-Control-Expose-Headers",
			"X-Request-ID, X-Trace-ID, X-Synapass-Provider, Retry-After")
		w.Header().Set("Access-Control-Max-Age", "600")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Response recorder
// ---------------------------------------------------------------------------

// responseRecorder captures the status and byte count without buffering the body.
//
// A buffering recorder would break streaming, so this only observes the write
// calls. It implements http.Flusher so the SSE writer's type assertion succeeds.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

// WriteHeader records the status.
func (r *responseRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

// Write records the byte count.
func (r *responseRecorder) Write(p []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	return n, err
}

// Flush forwards to the underlying writer when it supports flushing.
func (r *responseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap exposes the underlying writer so http.ResponseController can reach
// optional interfaces such as SetWriteDeadline.
func (r *responseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Status returns the recorded status.
func (r *responseRecorder) Status() int { return r.status }

// BytesWritten returns the recorded byte count.
func (r *responseRecorder) BytesWritten() int64 { return r.bytes }

// truncateString bounds a string length.
func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
