package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/shadowsafin/synapass/internal/domain"
)

// chiURLParam reads a path parameter.
func chiURLParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

// timeoutContext derives a bounded context from a request.
func timeoutContext(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(r.Context())
	}
	return context.WithTimeout(r.Context(), d)
}

// parseTimeRange reads from, to and interval query parameters.
//
// Three input forms are accepted for the boundaries, in order of precedence:
//
//	an RFC 3339 timestamp   absolute, used by a dashboard linking to a fixed window
//	a Go duration ("24h")   relative to now, used by the default views
//	nothing                 a sensible default window
//
// Accepting both absolute and relative forms means the same endpoint serves a live
// dashboard and a permalink without two implementations.
func parseTimeRange(r *http.Request, defaultWidth time.Duration) (domain.TimeRange, error) {
	now := time.Now().UTC()

	to, err := parseTimeParam(r.URL.Query().Get("to"), now)
	if err != nil {
		return domain.TimeRange{}, domain.Errorf(domain.ErrCodeInvalidRequest,
			"invalid 'to' parameter: %v", err)
	}
	if to.IsZero() {
		to = now
	}

	from, err := parseTimeParam(r.URL.Query().Get("from"), to)
	if err != nil {
		return domain.TimeRange{}, domain.Errorf(domain.ErrCodeInvalidRequest,
			"invalid 'from' parameter: %v", err)
	}
	if from.IsZero() {
		from = to.Add(-defaultWidth)
	}
	if !from.Before(to) {
		return domain.TimeRange{}, domain.NewError(domain.ErrCodeInvalidRequest,
			"'from' must be earlier than 'to'")
	}

	// An explicit interval wins; otherwise a width is chosen so the chart has a
	// useful number of points.
	interval := strings.TrimSpace(r.URL.Query().Get("interval"))
	if interval == "" {
		interval = domain.IntervalForWidth(to.Sub(from))
	}

	// Bound the window. An unbounded query is the easiest way for a dashboard to
	// accidentally ask a database to scan years of rows.
	const maxWindow = 400 * 24 * time.Hour
	if to.Sub(from) > maxWindow {
		return domain.TimeRange{}, domain.Errorf(domain.ErrCodeInvalidRequest,
			"the requested window exceeds the %s maximum", maxWindow)
	}

	return domain.TimeRange{From: from, To: to, Interval: interval}, nil
}

// parseTimeParam parses a single boundary.
//
// reference is the anchor for a relative duration; it is the range end for "from"
// and now for "to", so "from=24h" and "to=now" compose correctly.
func parseTimeParam(raw string, reference time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	if strings.EqualFold(raw, "now") {
		return time.Now().UTC(), nil
	}

	// RFC 3339 first: a duration string never contains a 'T', so the two forms are
	// unambiguous.
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.UTC(), nil
	}
	if parsed, err := time.Parse("2006-01-02", raw); err == nil {
		return parsed.UTC(), nil
	}

	if d, err := time.ParseDuration(raw); err == nil {
		// A negative offset is interpreted as a lookback from the reference time.
		if d < 0 {
			d = -d
		}
		return reference.Add(-d), nil
	}

	return time.Time{}, domain.Errorf(domain.ErrCodeInvalidRequest,
		"%q is not an RFC 3339 timestamp or a duration such as \"24h\"", raw)
}

// encodeQueryFilter builds a storage filter from request parameters.
func encodeQueryFilter(r *http.Request, tenantID string) storageFilter {
	query := r.URL.Query()

	return storageFilter{
		TenantID:  firstNonEmptyString(query.Get("tenant_id"), tenantID),
		APIKeyID:  query.Get("api_key_id"),
		Provider:  query.Get("provider"),
		Model:     query.Get("model"),
		Outcome:   domain.UsageOutcome(query.Get("outcome")),
		ErrorCode: domain.ErrorCode(query.Get("error_code")),
		Search:    query.Get("search"),
		Limit:     parseIntParam(r, "limit", 50),
		Offset:    parseIntParam(r, "offset", 0),
		StatusMin: parseIntParam(r, "status_min", 0),
	}
}
