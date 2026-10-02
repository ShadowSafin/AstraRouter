package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/shadowsafin/synapass/internal/analytics"
	"github.com/shadowsafin/synapass/internal/domain"
	"github.com/shadowsafin/synapass/internal/storage"
)

const (
	// analyticsDefaultWindow is used when the request names no range.
	analyticsDefaultWindow = 24 * time.Hour
	// analyticsCacheTTL bounds how stale a cached report may be. The window is
	// quantised to the bucket width (below), so every request inside a bucket
	// shares a key and the cache actually hits; the TTL then protects against a
	// burst of reloads re-running a dozen aggregates.
	analyticsCacheTTL = 20 * time.Second
	// analyticsTopN is how many rows each ranking table returns by default.
	analyticsTopN = 10
)

// reportCache is a process-local TTL cache for analytics reports.
//
// It is deliberately in-process and small: the reports are read-only aggregates
// over data that is already several seconds old, so a brief cache is free
// correctness-wise and removes almost all duplicate work when an operator
// reloads the page or several operators watch the same window. A shared cache
// would need invalidation across instances for no benefit at this scale.
var reportCache = &analyticsReportCache{entries: map[string]analyticsCacheEntry{}}

type analyticsCacheEntry struct {
	report  analytics.Report
	expires time.Time
}

type analyticsReportCache struct {
	mu      sync.Mutex
	entries map[string]analyticsCacheEntry
}

func (c *analyticsReportCache) get(key string) (analytics.Report, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expires) {
		if ok {
			delete(c.entries, key)
		}
		return analytics.Report{}, false
	}
	return entry.report, true
}

func (c *analyticsReportCache) put(key string, report analytics.Report) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Opportunistic sweep: the cache is tiny, and this keeps it from growing
	// without a background goroutine.
	if len(c.entries) > 256 {
		now := time.Now()
		for k, v := range c.entries {
			if now.After(v.expires) {
				delete(c.entries, k)
			}
		}
	}
	c.entries[key] = analyticsCacheEntry{report: report, expires: time.Now().Add(analyticsCacheTTL)}
}

// handleAdminAnalyticsReport serves GET /admin/v1/analytics/report.
//
// One composed payload rather than a dozen endpoints: the console renders every
// section at once, and returning them together means they all describe the same
// window and land in a single round trip.
func (s *Server) handleAdminAnalyticsReport(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutContext(r, 25*time.Second)
	defer cancel()
	rc := requestContext(ctx)

	if s.repos == nil || s.repos.Usage == nil {
		writeError(w, domain.NewError(domain.ErrCodeInternal, "the usage store is unavailable"),
			metaFromContext(rc, nil))
		return
	}

	window, err := parseTimeRange(r, analyticsDefaultWindow)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	window = quantizeWindow(window)

	top := parseIntParam(r, "top", analyticsTopN)
	if top <= 0 {
		top = analyticsTopN
	}
	sortKey := r.URL.Query().Get("sort")

	// The cache key is the resolved window plus the filters, so two requests for
	// the same bucket and the same filters share a report and no request can be
	// served a report built for a different filter set.
	key := r.URL.RawQuery + "|" + window.From.Format(time.RFC3339) + "|" + window.To.Format(time.RFC3339) + "|" + sortKey
	if cached, ok := reportCache.get(key); ok {
		writeJSON(w, http.StatusOK, cached)
		return
	}

	filter := encodeQueryFilter(r, "")
	filter.From, filter.To = window.From, window.To
	previousFilter := filter
	previousWindow := window.Previous()
	previousFilter.From, previousFilter.To = previousWindow.From, previousWindow.To

	summary, err := s.repos.Usage.Summary(ctx, filter)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	previous, err := s.repos.Usage.Summary(ctx, previousFilter)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}
	buckets, err := s.repos.Usage.Series(ctx, filter, window.Interval)
	if err != nil {
		writeError(w, err, metaFromContext(rc, nil))
		return
	}

	// The breakdowns. Each is a bounded GROUP BY; a failure in one must not
	// blank the whole report, so they degrade to empty with a log rather than
	// aborting — the summary and series are still worth showing.
	dimension := func(dim storage.AnalyticsDimension, order string) []domain.DimensionRow {
		rows, derr := s.repos.Usage.Grouped(ctx, filter, dim, order, top)
		if derr != nil {
			s.logger.Warn("analytics dimension failed", "dimension", string(dim), "error", derr)
			return nil
		}
		return analytics.Enrich(rows)
	}

	providers := dimension(storage.DimensionProvider, sortKey)
	models := dimension(storage.DimensionModel, sortKey)
	tenants := dimension(storage.DimensionTenant, sortKey)
	policies := dimension(storage.DimensionPolicy, "requests")
	requestTypes := dimension(storage.DimensionRequestType, "requests")
	outcomes := dimension(storage.DimensionOutcome, "requests")
	errors := dimension(storage.DimensionErrorCode, "requests")

	var cache domain.CacheSplit
	if split, cerr := s.repos.Usage.CacheSplit(ctx, filter); cerr != nil {
		s.logger.Warn("analytics cache split failed", "error", cerr)
	} else if split != nil {
		cache = *split
	}

	var routingRows []domain.StrategyRow
	if s.repos.Logs != nil {
		if rows, rerr := s.repos.Logs.StrategyMix(ctx, filter, 12); rerr != nil {
			s.logger.Warn("analytics routing mix failed", "error", rerr)
		} else {
			routingRows = rows
		}
	}

	report := analytics.Report{
		Window:       window,
		Interval:     window.Interval,
		Summary:      *summary,
		Previous:     *previous,
		Buckets:      buckets,
		Compared:     analytics.Compare(*summary, *previous),
		Providers:    providers,
		Models:       models,
		Tenants:      tenants,
		Policies:     policies,
		RequestTypes: requestTypes,
		Outcomes:     outcomes,
		Errors:       errors,
		Cache:        analytics.BuildCache(cache),
		Routing:      analytics.BuildRouting(routingRows),
		GeneratedAt:  time.Now().UTC(),
	}

	reportCache.put(key, report)
	writeJSON(w, http.StatusOK, report)
}

// quantizeWindow snaps a window's end down to a bucket boundary, keeping the
// width.
//
// Without this the window end is "now" to the nanosecond, so two requests a
// second apart resolve to different windows and nothing is ever cacheable. The
// cost is that a chart can lag by at most one bucket, which for an analytics
// view is not a cost at all — and it makes the numbers stable while an operator
// reads them.
func quantizeWindow(window domain.TimeRange) domain.TimeRange {
	width, ok := domain.ParseInterval(window.Interval)
	if !ok || width <= 0 {
		return window
	}
	to := window.To.Truncate(width)
	// A window narrower than one bucket would quantise to nothing; keep it.
	if !to.After(window.From) {
		return window
	}
	return domain.TimeRange{From: to.Add(-window.Duration()), To: to, Interval: window.Interval}
}
