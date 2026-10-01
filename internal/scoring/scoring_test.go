package scoring

import (
	"testing"
	"time"

	"github.com/corerouter/corerouter/internal/domain"
)

func TestRankPrefersReliable(t *testing.T) {
	e := New(DefaultWeights())
	for i := 0; i < 10; i++ {
		e.Record(Observation{Provider: "good", Model: "m", Success: true, LatencyMS: 200, CostUSD: 0.001, At: time.Now()})
	}
	for i := 0; i < 10; i++ {
		e.Record(Observation{Provider: "bad", Model: "m", Success: i < 3, LatencyMS: 2000, CostUSD: 0.01, At: time.Now()})
	}
	scores := e.ProviderScores(time.Hour)
	if len(scores) != 2 {
		t.Fatalf("expected 2, got %d", len(scores))
	}
	got := scores[0].ProviderName
	if got == "" {
		got = scores[0].ProviderID
	}
	if got != "good" {
		t.Fatalf("expected good first, got %s (%+v)", got, scores)
	}
	if scores[0].Explanation == "" {
		t.Fatalf("expected explanation")
	}
}

func TestCostAwareness(t *testing.T) {
	e := New(Weights{Success: 0.1, Latency: 0.1, Cost: 0.8, Feedback: 0})
	for i := 0; i < 5; i++ {
		e.Record(Observation{Provider: "cheap", Model: "m", Success: true, LatencyMS: 500, CostUSD: 0.0001, Task: domain.TaskChat, At: time.Now()})
		e.Record(Observation{Provider: "pricey", Model: "m", Success: true, LatencyMS: 500, CostUSD: 0.05, Task: domain.TaskChat, At: time.Now()})
	}
	scores := e.ProviderScores(time.Hour)
	got := scores[0].ProviderName
	if got == "" {
		got = scores[0].ProviderID
	}
	if got != "cheap" {
		t.Fatalf("expected cheap first")
	}
}
