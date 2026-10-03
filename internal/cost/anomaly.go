package cost

import (
	"sort"
	"time"

	"github.com/shadowsafin/synapass/internal/domain"
)

// Anomaly thresholds: a day's spend is anomalous when it exceeds BOTH a
// multiple of its baseline and an absolute floor. The multiple catches
// relative spikes on quiet dimensions; the floor keeps a $2 Tuesday on a $0
// dimension from paging anyone. Both constants are deliberately boring and
// documented so operators can reason about what "anomaly" means here.
const (
	// AnomalyRatioWarn flags days spending 3x baseline or more.
	AnomalyRatioWarn = 3.0
	// AnomalyRatioCrit flags days spending 10x baseline or more.
	AnomalyRatioCrit = 10.0
	// AnomalyFloorUSD ignores deviations under ten dollars: below that the
	// baseline is noise and the spike is not worth an operator's glance.
	AnomalyFloorUSD = 10.0
	// AnomalyBaselineDays is the trailing window the median baseline is drawn
	// from; two weeks absorb weekly seasonality without fossilizing.
	AnomalyBaselineDays = 14
)

// SeriesPoint is one day of spend for one dimension value.
type SeriesPoint struct {
	Dimension string
	Key       string
	Day       time.Time
	Cost      float64
}

// Detect flags anomalous days across dimension series. The baseline is the
// median of the preceding up-to-14 days — a median, not a mean, so that one
// earlier spike does not launder the next one. The evaluated day is always
// the latest day in each series; history is baseline, never verdict.
func Detect(points []SeriesPoint, now time.Time) []domain.CostAnomaly {
	bySeries := map[string][]SeriesPoint{}
	for _, p := range points {
		k := p.Dimension + "\x00" + p.Key
		bySeries[k] = append(bySeries[k], p)
	}

	var out []domain.CostAnomaly
	for _, series := range bySeries {
		if len(series) == 0 {
			continue
		}
		sort.Slice(series, func(i, j int) bool { return series[i].Day.Before(series[j].Day) })
		latest := series[len(series)-1]

		var baseline []float64
		cutoff := latest.Day.AddDate(0, 0, -AnomalyBaselineDays)
		for _, p := range series[:len(series)-1] {
			if !p.Day.Before(cutoff) {
				baseline = append(baseline, p.Cost)
			}
		}
		if len(baseline) < 3 {
			continue
		}
		expected := median(baseline)
		if expected <= 0 || latest.Cost < AnomalyFloorUSD {
			continue
		}
		ratio := latest.Cost / expected
		var severity domain.AnomalySeverity
		switch {
		case ratio >= AnomalyRatioCrit:
			severity = domain.AnomalyCritical
		case ratio >= AnomalyRatioWarn:
			severity = domain.AnomalyWarning
		default:
			continue
		}
		dayStart := latest.Day.UTC().Truncate(24 * time.Hour)
		out = append(out, domain.CostAnomaly{
			ID:          domain.NewID(),
			Dimension:   latest.Dimension,
			Key:         latest.Key,
			WindowFrom:  dayStart,
			WindowTo:    dayStart.AddDate(0, 0, 1),
			ObservedUSD: RoundMicro(latest.Cost),
			ExpectedUSD: RoundMicro(expected),
			Ratio:       RoundMicro(ratio),
			Severity:    severity,
			DetectedAt:  now.UTC(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ratio > out[j].Ratio })
	return out
}

func median(values []float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
