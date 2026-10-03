package cost

import (
	"math"
	"testing"

	"github.com/shadowsafin/synapass/internal/domain"
)

// gpt4oMiniSheet mirrors a real mid-2025 price sheet: $0.15/M in, $0.60/M
// out, $0.075/M cached. Tests use realistic magnitudes so rounding bugs show
// up at realistic sizes rather than hiding in toy numbers.
func gpt4oMiniSheet() Price {
	return Price{InputPerM: 0.15, OutputPerM: 0.60, CachedInputPerM: 0.075, Currency: "USD", Source: "registry"}
}

func TestComputeSplitsFreshAndCachedInput(t *testing.T) {
	usage := domain.TokenUsage{PromptTokens: 2000, CompletionTokens: 500, CachedPromptTokens: 1500}
	bd := Compute(usage, gpt4oMiniSheet(), nil)

	// 500 fresh * 0.15/M = 0.000075; 1500 cached * 0.075/M = 0.0001125;
	// 500 out * 0.60/M = 0.0003. Total 0.0004875 -> 0.000488.
	want := 0.000488
	if math.Abs(bd.TotalUSD-want) > 1e-9 {
		t.Fatalf("total = %v, want %v", bd.TotalUSD, want)
	}
	if len(bd.Lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(bd.Lines))
	}
	var footed float64
	for _, l := range bd.Lines {
		footed += l.AmountUSD
	}
	if RoundMicro(footed) != bd.TotalUSD {
		t.Fatalf("lines foot to %v, total is %v", footed, bd.TotalUSD)
	}
}

func TestComputeUnpricedRuntimeIsZero(t *testing.T) {
	usage := domain.TokenUsage{PromptTokens: 10000, CompletionTokens: 2000}
	bd := Compute(usage, Price{Source: "registry"}, nil)
	if bd.TotalUSD != 0 || len(bd.Lines) != 0 {
		t.Fatalf("unpriced runtime billed %+v", bd)
	}
}

func TestComputeBaseFeePerBillableAttempt(t *testing.T) {
	price := gpt4oMiniSheet()
	price.BaseFeeUSD = 0.001
	attempts := []AttemptUsage{
		{Label: "attempt 1", Usage: domain.TokenUsage{PromptTokens: 1000}, BillBaseFee: true},
		{Label: "attempt 2", Usage: domain.TokenUsage{PromptTokens: 1000, CompletionTokens: 100}, BillBaseFee: true},
	}
	bd := Compute(domain.TokenUsage{}, price, attempts)
	// Input: 2000 * 0.15/M = 0.0003; output: 100 * 0.6/M = 0.00006;
	// fees: 2 * 0.001 = 0.002. Total 0.00236.
	if math.Abs(bd.TotalUSD-0.00236) > 1e-9 {
		t.Fatalf("total = %v, want 0.00236", bd.TotalUSD)
	}
	fees := 0
	for _, l := range bd.Lines {
		if l.Kind == domain.CostLineBaseFee {
			fees++
		}
	}
	if fees != 2 {
		t.Fatalf("base fee lines = %d, want 2", fees)
	}
}

func TestComputeFailedAttemptStillBillsItsTokens(t *testing.T) {
	// A retry consumed a full prompt on the failed attempt and again on the
	// success: both prompts bill, because the provider metered both.
	price := gpt4oMiniSheet()
	attempts := []AttemptUsage{
		{Label: "attempt 1", Usage: domain.TokenUsage{PromptTokens: 1000}, BillBaseFee: true},
		{Label: "attempt 2", Usage: domain.TokenUsage{PromptTokens: 1000, CompletionTokens: 50}, BillBaseFee: true},
	}
	bd := Compute(domain.TokenUsage{}, price, attempts)
	// 2000 in * 0.15 = 0.0003; 50 out * 0.6 = 0.00003 -> 0.00033.
	if math.Abs(bd.TotalUSD-0.00033) > 1e-9 {
		t.Fatalf("total = %v, want 0.00033", bd.TotalUSD)
	}
}

func TestComputeCachedClampedToPrompt(t *testing.T) {
	usage := domain.TokenUsage{PromptTokens: 100, CachedPromptTokens: 5000}
	bd := Compute(usage, gpt4oMiniSheet(), nil)
	// All 100 billed cached: 100 * 0.075/M = 0.0000075 -> 0.000008.
	if math.Abs(bd.TotalUSD-0.000008) > 1e-9 {
		t.Fatalf("total = %v, want 0.000008", bd.TotalUSD)
	}
}

func TestEstimateIgnoresCache(t *testing.T) {
	// The projection cannot know cache state, so it prices everything fresh:
	// 2000 * 0.15/M = 0.0003.
	bd := Estimate(2000, 0, gpt4oMiniSheet())
	if !bd.Estimated {
		t.Fatal("estimate not flagged")
	}
	if math.Abs(bd.TotalUSD-0.0003) > 1e-9 {
		t.Fatalf("estimate = %v, want 0.0003", bd.TotalUSD)
	}
}

func TestAccuracyScores(t *testing.T) {
	if got := Accuracy(0.001, 0.001); got != 1 {
		t.Fatalf("exact = %v, want 1", got)
	}
	if got := Accuracy(0, 0); got != 1 {
		t.Fatalf("free/free = %v, want 1", got)
	}
	if got := Accuracy(0.001, 0); got != 0 {
		t.Fatalf("phantom estimate = %v, want 0", got)
	}
	if got := Accuracy(0.0011, 0.001); math.Abs(got-0.9) > 1e-9 {
		t.Fatalf("10%% drift = %v, want 0.9", got)
	}
	if got := Accuracy(0.005, 0.001); got != 0 {
		t.Fatalf("wild drift = %v, want 0", got)
	}
}

func TestRoundMicro(t *testing.T) {
	if got := RoundMicro(0.0004875); math.Abs(got-0.000488) > 1e-12 {
		t.Fatalf("round = %v", got)
	}
	if RoundMicro(0) != 0 || RoundMicro(math.NaN()) != 0 {
		t.Fatal("zero/NaN must stay zero")
	}
}
