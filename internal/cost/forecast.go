package cost

import (
	"math"
	"time"

	"github.com/shadowsafin/synapass/internal/domain"
)

// DaySpend is one observed day of spend, the only input forecasting needs.
type DaySpend struct {
	Day  time.Time
	Cost float64
}

// Forecast projects month-end spend from observed daily totals. The method is
// deliberately boring — a trailing-7-day mean plus a least-squares trend —
// because a finance projection must be explainable in one sentence, and the
// uncertainty band matters more than the point estimate.
//
// Days with zero spend still count as observed (an idle weekend is signal,
// not missing data); only the trailing window is used, so a pricing change
// last week stops haunting the projection after seven days.
func Forecast(days []DaySpend, now time.Time) domain.CostForecast {
	fc := domain.CostForecast{GeneratedAt: now.UTC()}
	if len(days) == 0 {
		return fc
	}
	now = now.UTC()

	window := days
	if len(window) > 7 {
		window = window[len(window)-7:]
	}
	fc.DaysObserved = len(window)

	var sum float64
	for _, d := range window {
		sum += d.Cost
	}
	mean := sum / float64(len(window))
	fc.DailyAverageUSD = RoundMicro(mean)

	// Least-squares slope over day indices 0..n-1: the direction spend is
	// heading, in USD per day.
	var slope float64
	if len(window) > 1 {
		var sxy, sxx float64
		n := float64(len(window))
		meanX := (n - 1) / 2
		for i, d := range window {
			x := float64(i) - meanX
			sxy += x * (d.Cost - mean)
			sxx += x * x
		}
		if sxx > 0 {
			slope = sxy / sxx
		}
	}
	fc.TrendUSDPerDay = RoundMicro(slope)

	// Residual standard deviation around the trend line: the honest error bar.
	var rss float64
	for i, d := range window {
		predicted := mean + slope*(float64(i)-(float64(len(window))-1)/2)
		rss += (d.Cost - predicted) * (d.Cost - predicted)
	}
	std := 0.0
	if len(window) > 2 {
		std = math.Sqrt(rss / float64(len(window)-2))
	}

	// Project from month start: observed month-to-date plus trend-paced days
	// remaining. Past days keep their actuals; only the future is modeled.
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, 0)
	daysInMonth := monthEnd.Sub(monthStart).Hours() / 24
	var mtd float64
	observedDays := 0
	for _, d := range days {
		day := d.Day.UTC().Truncate(24 * time.Hour)
		if !day.Before(monthStart) && day.Before(monthEnd) {
			mtd += d.Cost
			observedDays++
		}
	}
	remaining := daysInMonth - float64(observedDays)
	if remaining < 0 {
		remaining = 0
	}
	dailyRate := mean + slope*float64(len(window))/2
	projected := mtd + dailyRate*remaining
	if projected < mtd {
		projected = mtd
	}
	fc.ProjectedMonthUSD = RoundMicro(projected)
	band := std * math.Sqrt(remaining)
	fc.ProjectedMonthLowUSD = RoundMicro(math.Max(0, projected-band))
	fc.ProjectedMonthHighUSD = RoundMicro(projected + band)
	return fc
}
