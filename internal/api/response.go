package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/shadowsafin/astrarouter/internal/domain"
)

// writeJSON renders a response body.
//
// Encoding happens into a buffer before any header is written. That ordering is
// what allows an encoding failure to still produce a valid error response instead
// of a truncated body with a 200 status, which is the classic way a JSON handler
// fails silently.
func writeJSON(w http.ResponseWriter, status int, body any) {
	payload, err := json.Marshal(body)
	if err != nil {
		// Encoding our own response is a programming error, so the fallback is a
		// hand-written valid error body rather than another encoder call.
		http.Error(w, `{"error":{"message":"failed to encode the response","type":"server_error","code":"internal_error"}}`,
			http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(status)
	if _, err := w.Write(payload); err != nil {
		// A write failure here means the client is gone; there is nothing useful
		// left to do and logging at debug level avoids noise on every disconnect.
		slog.Default().Debug("failed to write response body", "error", err)
	}
}

// writeError renders a normalized error as an OpenAI-compatible envelope.
func writeError(w http.ResponseWriter, err error, meta *domain.ResponseMetadata) {
	normalized := domain.AsError(err)
	if normalized == nil {
		writeJSON(w, http.StatusInternalServerError, domain.ErrorResponse{
			Error: domain.ErrorBody{
				Message: "an internal error occurred",
				Type:    "server_error",
				Code:    string(domain.ErrCodeInternal),
			},
		})
		return
	}

	status := normalized.HTTPStatus()

	// The metadata block is attached to errors as well as successes, so a client
	// that logs the failure also captures which provider and policy were involved.
	body := struct {
		Error      domain.ErrorBody         `json:"error"`
		AstraRouter *domain.ResponseMetadata `json:"astrarouter,omitempty"`
	}{
		Error: domain.ErrorBody{
			Message: normalized.Message,
			Type:    normalized.ErrorType(),
			Param:   normalized.ErrorParam(),
			Code:    string(normalized.Code),
		},
		AstraRouter: meta,
	}

	// Some downstream proxies rewrite status codes, so the upstream status is
	// echoed in a header for callers that need the original.
	if normalized.Status > 0 && normalized.Status != status {
		w.Header().Set("X-AstraRouter-Upstream-Status", strconv.Itoa(normalized.Status))
	}
	if normalized.Provider != "" {
		w.Header().Set("X-AstraRouter-Provider", normalized.Provider)
	}
	if normalized.Retryable || normalized.FallbackEligible {
		w.Header().Set("X-AstraRouter-Retryable", strconv.FormatBool(normalized.Retryable))
	}

	writeJSON(w, status, body)
}

// sseWriter streams server-sent events.
//
// Writing SSE is deceptively fiddly: the response must be flushed after every
// event or a proxy will buffer the whole stream, and a client disconnect surfaces
// as a write error that must not be treated as a gateway fault.
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	// wroteHeader tracks whether the status line has been sent, because the first
	// write commits it.
	wroteHeader bool
	// bytes counts the payload written, used for the response size metric.
	bytes int64
}

// newSSEWriter prepares a response for streaming.
func newSSEWriter(w http.ResponseWriter) (*sseWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Without a flusher the stream would be buffered until completion, which
		// defeats the entire purpose. Failing loudly here is better than silently
		// degrading to a buffered response.
		return nil, domain.NewError(domain.ErrCodeInternal,
			"streaming is not supported by this server configuration")
	}

	// These headers are what stop intermediaries from buffering or transforming
	// the stream.
	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("Connection", "keep-alive")
	// Nginx needs this to disable response buffering for the location.
	header.Set("X-Accel-Buffering", "no")

	return &sseWriter{w: w, flusher: flusher}, nil
}

// WriteEvent writes one SSE data frame and flushes it.
func (s *sseWriter) WriteEvent(payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domain.NewError(domain.ErrCodeInternal, "failed to encode a stream event").Wrap(err)
	}
	return s.WriteRaw("data: " + string(encoded) + "\n\n")
}

// WriteRaw writes a pre-encoded frame and flushes it.
func (s *sseWriter) WriteRaw(frame string) error {
	if !s.wroteHeader {
		// 200 is correct even for a request that will fail mid-stream: the
		// provider call has already started and there is no meaningful status to
		// send. Failures before the first chunk are handled before this point.
		s.w.WriteHeader(http.StatusOK)
		s.wroteHeader = true
	}

	n, err := s.w.Write([]byte(frame))
	s.bytes += int64(n)
	if err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// WriteDone writes the OpenAI end-of-stream sentinel.
func (s *sseWriter) WriteDone() error {
	return s.WriteRaw("data: [DONE]\n\n")
}

// WriteStreamError reports a failure that occurred after the stream started.
//
// There is no status code left to change, so the failure is delivered as an error
// event followed by the terminator. A client that understands SSE sees a clean end
// of stream plus an error event; one that does not still sees a terminated stream
// rather than a hang.
func (s *sseWriter) WriteStreamError(err error) error {
	normalized := domain.AsError(err)
	body := domain.NewErrorResponse(normalized)
	encoded, marshalErr := json.Marshal(body)
	if marshalErr != nil {
		encoded = []byte(`{"error":{"message":"stream failed","type":"server_error"}}`)
	}
	// The explicit "error" event name lets clients subscribe to failures rather
	// than having to inspect every data frame.
	if err := s.WriteRaw("event: error\ndata: " + string(encoded) + "\n\n"); err != nil {
		return err
	}
	return s.WriteDone()
}

// BytesWritten returns the payload size.
func (s *sseWriter) BytesWritten() int64 { return s.bytes }

// WroteHeader reports whether the status line has been committed.
func (s *sseWriter) WroteHeader() bool { return s.wroteHeader }

// parseIntParam reads an integer query parameter with a default.
func parseIntParam(r *http.Request, name string, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

// parseBoolParam reads a boolean query parameter with a default.
func parseBoolParam(r *http.Request, name string, fallback bool) bool {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return value
}

// decodeJSONBody decodes a request body with a size limit.
func decodeJSONBody(r *http.Request, limit int64, target any) error {
	if r.Body == nil {
		return domain.NewError(domain.ErrCodeInvalidRequest, "a request body is required")
	}
	if limit > 0 {
		r.Body = http.MaxBytesReader(nil, r.Body, limit)
	}

	decoder := json.NewDecoder(r.Body)
	// Unknown fields are rejected: a typo in a parameter name would otherwise be
	// silently ignored, which produces a confusing "why is my setting ignored?".
	decoder.DisallowUnknownFields()
	decoder.UseNumber()

	if err := decoder.Decode(target); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return domain.Errorf(domain.ErrCodeInvalidRequest,
				"the request body exceeds the %d byte limit", maxBytesErr.Limit)
		}
		// A syntax error is the most common cause and deserves an actionable
		// message rather than the raw decoder text.
		return domain.Errorf(domain.ErrCodeInvalidRequest,
			"failed to parse the request body as JSON: %v", err)
	}
	return nil
}

// headerDebug opts a client into full routing internals on the inference
// surface. The default response carries stable attribution only (who answered,
// what was served, at what cost); policy names, strategies, task labels and
// route reasons stay behind this header so normal app clients never depend on
// operational details that change between deploys. The admin surface and the
// dashboard always see the full block.
const headerDebug = "X-AstraRouter-Debug"

// debugRequested reports whether the client asked for routing internals.
func debugRequested(r *http.Request) bool {
	if r == nil {
		return false
	}
	raw := r.Header.Get(headerDebug)
	if raw == "" {
		return false
	}
	value, err := strconv.ParseBool(raw)
	return err == nil && value
}

// publicMeta builds the client-facing metadata block: stable attribution by
// default, full routing internals in debug mode.
func publicMeta(r *http.Request, rc *domain.RequestContext, extra func(*domain.ResponseMetadata)) *domain.ResponseMetadata {
	return publicMetaFrom(rc, extra, debugRequested(r))
}

// publicMetaFrom is publicMeta with the decision made by the caller, for the
// handlers that compute it once and thread it through writers and helpers.
func publicMetaFrom(rc *domain.RequestContext, extra func(*domain.ResponseMetadata), debug bool) *domain.ResponseMetadata {
	meta := metaFromContext(rc, extra)
	if meta == nil || debug {
		return meta
	}
	return &domain.ResponseMetadata{
		RequestID:        meta.RequestID,
		Provider:         meta.Provider,
		RequestedModel:   meta.RequestedModel,
		RoutedModel:      meta.RoutedModel,
		FallbackUsed:     meta.FallbackUsed,
		CacheHit:         meta.CacheHit,
		CacheKind:        meta.CacheKind,
		LatencyMS:        meta.LatencyMS,
		EstimatedCostUSD: meta.EstimatedCostUSD,
		// The tool run summary is part of the stable contract, not an
		// operational detail: a client that asked for a tool run needs to know
		// whether the gateway ran it, how many steps it took and why it stopped.
		// Structured-output conformance travels with it for the same reason.
		ToolRun:    meta.ToolRun,
		Structured: meta.Structured,
		// Completion bounding is part of the same contract: a caller that
		// receives a shortened answer must be able to tell that it was capped
		// rather than finished, without re-running the request to find out.
		Completion: meta.Completion,
	}
}

// metaFromContext builds the full response metadata block, including routing
// internals. It is the right choice for the admin surface and for internal
// bookkeeping; the public inference surface uses publicMeta instead.
func metaFromContext(rc *domain.RequestContext, extra func(*domain.ResponseMetadata)) *domain.ResponseMetadata {
	if rc == nil {
		return nil
	}
	meta := &domain.ResponseMetadata{
		RequestID: rc.RequestID.String(),
		TraceID:   rc.TraceID,
	}
	if rc.Tenant != nil {
		// Tenant identity is not exposed in the response body by default; only the
		// request id and trace id are, because a response may be logged verbatim by
		// a client that should not learn internal identifiers.
	}
	if rc.Task.Task != "" {
		meta.Task = string(rc.Task.Task)
	}
	if rc.ShapePlan.Explain() != "shaping: none" {
		meta.Shaping = rc.ShapePlan.Explain()
	}
	if rc.Resolution != nil {
		// Provider attribution is deliberately NOT set here. The decision records
		// where the request was sent first, not who answered it: after a failover
		// the two differ, and reporting the chosen target as the serving one would
		// mislabel exactly the requests an operator most needs to trace. Callers
		// that know the outcome set Provider themselves.
		meta.PolicyID = rc.Resolution.PolicyID
		meta.PolicyName = rc.Resolution.PolicyName
		meta.RequestedModel = rc.RequestedModel
		meta.RoutedModel = rc.Resolution.Chosen.Model
		meta.Strategy = string(rc.Resolution.Strategy)
		meta.EstimatedCostUSD = rc.Resolution.EstimatedCost.USD
		meta.RouteReason = rc.Resolution.Reason
		meta.Degraded = rc.Resolution.Degraded
		if rc.Resolution.CacheKind != "" {
			meta.CacheKind = rc.Resolution.CacheKind
		}
	}
	if extra != nil {
		extra(meta)
	}
	return meta
}

// describeError renders an error for logs without exposing credentials.
func describeError(err error) string {
	if err == nil {
		return ""
	}
	normalized := domain.AsError(err)
	if normalized == nil {
		return ""
	}
	if normalized.Provider != "" {
		return fmt.Sprintf("%s: %s (provider=%s)", normalized.Code, normalized.Message, normalized.Provider)
	}
	return fmt.Sprintf("%s: %s", normalized.Code, normalized.Message)
}
