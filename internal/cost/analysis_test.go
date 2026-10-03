package cost

import (
	"math"
	"testing"
	"time"

	"github.com/shadowsafin/synapass/internal/domain"
)

func TestEvaluateOnPace(t *testing.T) {
	// Fifteen of thirty-one days in with half the budget spent: utilization
	// 0.5, burn ~1.03, projected ~103.3 (pace held to month end).
	now := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	b := domain.Budget{TenantID: "t", Scope: "tenant", Period: domain.BudgetMonthly, Enforced: true}
	st := Evaluate(b, 50, 100, now)
	if math.Abs(st.Utilization-0.5) > 1e-6 {
		t.Fatalf("utilization = %v", st.Utilization)
	}
	if math.Abs(st.BurnRate-1.033) > 0.05 {
		t.Fatalf("burn = %v, want ~1.03", st.BurnRate)
	}
	if math.Abs(st.ProjectedUSD-103.33) > 1 {
		t.Fatalf("projected = %v, want ~103.3", st.ProjectedUSD)
	}
	if st.Exhausted {
		t.Fatal("must not be exhausted")
	}
}

func TestEvaluateBurningTooFast(t *testing.T) {
	// 80% spent one quarter through the month: burn ~3.2x, projected ~4x.
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	b := domain.Budget{TenantID: "t", Scope: "tenant", Period: domain.BudgetMonthly}
	st := Evaluate(b, 80, 100, now)
	if st.BurnRate < 2.5 {
		t.Fatalf("burn = %v, want well above 1", st.BurnRate)
	}
	if st.ProjectedUSD <= 100 {
		t.Fatalf("projected = %v, want overshoot", st.ProjectedUSD)
	}
}

func TestEvaluateExhausted(t *testing.T) {
	now := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	b := domain.Budget{TenantID: "t", Scope: "tenant", Period: domain.BudgetMonthly}
	st := Evaluate(b, 120, 100, now)
	if !st.Exhausted || st.RemainingUSD != 0 {
		t.Fatalf("exhausted = %v remaining = %v", st.Exhausted, st.RemainingUSD)
	}
}

func TestCrossedThresholdsExactlyOnce(t *testing.T) {
	thresholds := []float64{10, 50, 90}
	fired := map[float64]bool{10: true}
	crossed := CrossedThresholds(thresholds, 60, fired)
	if len(crossed) != 1 || crossed[0] != 50 {
		t.Fatalf("crossed = %v, want [50]", crossed)
	}
}

func TestForecastFlatSpend(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	var days []DaySpend
	for i := 9; i >= 0; i-- {
		days = append(days, DaySpend{Day: now.AddDate(0, 0, -i), Cost: 10})
	}
	fc := Forecast(days, now)
	if math.Abs(fc.DailyAverageUSD-10) > 1e-9 {
		t.Fatalf("average = %v", fc.DailyAverageUSD)
	}
	// 10 observed days at $10 plus 21 remaining at $10 = $310.
	if math.Abs(fc.ProjectedMonthUSD-310) > 1 {
		t.Fatalf("projected = %v, want ~310", fc.ProjectedMonthUSD)
	}
	if fc.ProjectedMonthLowUSD > fc.ProjectedMonthUSD || fc.ProjectedMonthHighUSD < fc.ProjectedMonthUSD {
		t.Fatal("band must contain the projection")
	}
}

func TestForecastEmpty(t *testing.T) {
	fc := Forecast(nil, time.Now())
	if fc.ProjectedMonthUSD != 0 || fc.DaysObserved != 0 {
		t.Fatalf("empty forecast = %+v", fc)
	}
}

func TestDetectSpike(t *testing.T) {
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	var points []SeriesPoint
	for i := 14; i >= 1; i-- {
		points = append(points, SeriesPoint{Dimension: "model", Key: "m",
			Day: now.AddDate(0, 0, -i), Cost: 20})
	}
	points = append(points, SeriesPoint{Dimension: "model", Key: "m", Day: now, Cost: 250})
	found := Detect(points, now)
	if len(found) != 1 {
		t.Fatalf("anomalies = %d, want 1", len(found))
	}
	if found[0].Severity != domain.AnomalyCritical || math.Abs(found[0].Ratio-12.5) > 1e-9 {
		t.Fatalf("anomaly = %+v", found[0])
	}
}

func TestDetectIgnoresNoise(t *testing.T) {
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	var points []SeriesPoint
	for i := 10; i >= 0; i-- {
		points = append(points, SeriesPoint{Dimension: "model", Key: "m",
			Day: now.AddDate(0, 0, -i), Cost: 1})
	}
	if found := Detect(points, now); len(found) != 0 {
		t.Fatalf("noise flagged: %+v", found)
	}
}

func TestSavingsFloors(t *testing.T) {
	if FallbackSaving(0.01, 0.05) != 0 {
		t.Fatal("expensive fallback must score zero, not negative")
	}
	if got := FallbackSaving(0.05, 0.01); math.Abs(got-0.04) > 1e-9 {
		t.Fatalf("fallback saving = %v", got)
	}
	if RoutingSaving(0.01, 0.02) != 0 {
		t.Fatal("costly shaping must score zero")
	}
	s := Savings{CacheUSD: 1, RoutingUSD: 2, FallbackUSD: 3}
	if s.Total() != 6 {
		t.Fatalf("total = %v", s.Total())
	}
}
