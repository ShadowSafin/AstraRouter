// Package analytics aggregates telemetry for cost/latency trends and provider
// performance views. It operates over durable stores, not live traffic.
package analytics

import (
	"sort"
	"time"

	"github.com/shadowsafin/corerouter/internal/domain"
)

// Point is one trend bucket.
type Point struct {
	Start        time.Time `json:"start"`
	Requests     int64     `json:"requests"`
	CostUSD      float64   `json:"cost_usd"`
	AvgLatency   float64   `json:"avg_latency_ms"`
	ErrorRate    float64   `json:"error_rate"`
	CacheHitRate float64   `json:"cache_hit_rate,omitempty"`
}

// Trends builds cost/latency series from usage rows.
func Trends(rows []domain.UsageRecord, bucket time.Duration) []Point {
	if bucket <= 0 {
		bucket = time.Hour
	}
	buckets := map[int64]*Point{}
	for _, r := range rows {
		key := r.CreatedAt.Truncate(bucket).Unix()
		p, ok := buckets[key]
		if !ok {
			p = &Point{Start: time.Unix(key, 0).UTC()}
			buckets[key] = p
		}
		p.Requests++
		p.CostUSD += r.Cost.USD
		p.AvgLatency += float64(r.LatencyMS)
		if r.Outcome == domain.OutcomeError || r.Outcome == domain.OutcomeRejected {
			p.ErrorRate += 1
		}
		if r.CacheHit {
			p.CacheHitRate += 1
		}
	}
	out := make([]Point, 0, len(buckets))
	for _, p := range buckets {
		if p.Requests > 0 {
			p.AvgLatency /= float64(p.Requests)
			p.ErrorRate /= float64(p.Requests)
			p.CacheHitRate /= float64(p.Requests)
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// ProviderPerformance summarizes per-provider quality for the dashboard.
func ProviderPerformance(rows []domain.UsageRecord) []domain.ProviderBreakdownRow {
	byProvider := map[string]*domain.ProviderBreakdownRow{}
	for _, r := range rows {
		row, ok := byProvider[r.Provider]
		if !ok {
			row = &domain.ProviderBreakdownRow{Provider: r.Provider}
			byProvider[r.Provider] = row
		}
		row.Requests++
		row.TotalTokens += int64(r.Usage.TotalTokens)
		row.CostUSD += r.Cost.USD
		row.AvgLatencyMS += float64(r.LatencyMS)
		if r.Outcome == domain.OutcomeError {
			row.Errors++
		}
		if r.FallbackUsed {
			row.Fallbacks++
		}
	}
	out := make([]domain.ProviderBreakdownRow, 0, len(byProvider))
	for _, row := range byProvider {
		if row.Requests > 0 {
			row.AvgLatencyMS /= float64(row.Requests)
			row.ErrorRate = float64(row.Errors) / float64(row.Requests)
		}
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Requests > out[j].Requests })
	return out
}
