// Package scoring ranks providers and models from real usage.
//
// Scores are explainable blends of success rate, latency, cost efficiency and
// feedback, not opaque ML. The same inputs always yield the same scores, which
// keeps routing reproducible and the dashboard honest.
package scoring

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/corerouter/corerouter/internal/domain"
)

// Observation is one request outcome fed into scoring.
type Observation struct {
	Provider     string
	ProviderID   string
	Model        string
	Task         domain.TaskType
	Success      bool
	Timeout      bool
	Refusal      bool
	LatencyMS    int64
	CostUSD      float64
	FallbackUsed bool
	Feedback     float64 // -1..1, 0 means no feedback
	At           time.Time
}

// Weights blends the signals. All weights are in [0,1]; they need not sum to 1.
type Weights struct {
	Success  float64
	Latency  float64
	Cost     float64
	Feedback float64
}

// DefaultWeights prefers reliability, then speed, then cost.
func DefaultWeights() Weights {
	return Weights{Success: 0.5, Latency: 0.25, Cost: 0.15, Feedback: 0.1}
}

// Engine aggregates observations into scores.
type Engine struct {
	mu      sync.Mutex
	weights Weights
	obs     []Observation
	maxObs  int
}

// New creates a scoring engine.
func New(w Weights) *Engine {
	if w.Success == 0 && w.Latency == 0 && w.Cost == 0 && w.Feedback == 0 {
		w = DefaultWeights()
	}
	return &Engine{weights: w, maxObs: 10000}
}

// Record adds an observation.
func (e *Engine) Record(o Observation) {
	if e == nil {
		return
	}
	if o.At.IsZero() {
		o.At = time.Now().UTC()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.obs = append(e.obs, o)
	if len(e.obs) > e.maxObs {
		e.obs = e.obs[len(e.obs)-e.maxObs:]
	}
}

// ProviderScores ranks providers over recent observations.
func (e *Engine) ProviderScores(window time.Duration) []domain.ProviderScore {
	e.mu.Lock()
	obs := append([]Observation{}, e.obs...)
	w := e.weights
	e.mu.Unlock()

	cutoff := time.Now().Add(-window)
	byProvider := map[string][]Observation{}
	names := map[string]string{}
	for _, o := range obs {
		if o.At.Before(cutoff) {
			continue
		}
		byProvider[o.Provider] = append(byProvider[o.Provider], o)
		if o.Provider != "" {
			names[o.Provider] = o.Provider
		}
	}
	out := make([]domain.ProviderScore, 0, len(byProvider))
	for provider, list := range byProvider {
		out = append(out, scoreProvider(provider, names[provider], list, w, window))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		pi := out[i].ProviderName
		if pi == "" {
			pi = out[i].ProviderID
		}
		pj := out[j].ProviderName
		if pj == "" {
			pj = out[j].ProviderID
		}
		return pi < pj
	})
	return out
}

// ModelScores ranks models.
func (e *Engine) ModelScores(window time.Duration) []domain.ModelScore {
	e.mu.Lock()
	obs := append([]Observation{}, e.obs...)
	w := e.weights
	e.mu.Unlock()

	cutoff := time.Now().Add(-window)
	byModel := map[string][]Observation{}
	for _, o := range obs {
		if o.At.Before(cutoff) {
			continue
		}
		key := o.Provider + "\x00" + o.Model
		byModel[key] = append(byModel[key], o)
	}
	out := make([]domain.ModelScore, 0, len(byModel))
	for _, list := range byModel {
		first := list[0]
		out = append(out, scoreModel(first.Provider, first.Model, list, w, window))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return out[i].ProviderName < out[j].ProviderName
	})
	return out
}

func scoreProvider(provider, name string, list []Observation, w Weights, window time.Duration) domain.ProviderScore {
	n := len(list)
	success, timeouts, refusals, fallbacks := 0, 0, 0, 0
	var latSum int64
	var costSum float64
	successCost := 0.0
	successN := 0
	feedbackSum := 0.0
	feedbackN := 0
	byTask := map[string][]Observation{}
	for _, o := range list {
		if o.Success {
			success++
			successCost += o.CostUSD
			successN++
		}
		if o.Timeout {
			timeouts++
		}
		if o.Refusal {
			refusals++
		}
		if o.FallbackUsed {
			fallbacks++
		}
		latSum += o.LatencyMS
		costSum += o.CostUSD
		if o.Feedback != 0 {
			feedbackSum += o.Feedback
			feedbackN++
		}
		byTask[string(o.Task)] = append(byTask[string(o.Task)], o)
		_ = costSum
	}
	successRate := 0.0
	if n > 0 {
		successRate = float64(success) / float64(n)
	}
	timeoutRate := float64(timeouts) / float64(max(n, 1))
	refusalRate := float64(refusals) / float64(max(n, 1))
	fallbackRate := float64(fallbacks) / float64(max(n, 1))
	avgLat := 0.0
	if n > 0 {
		avgLat = float64(latSum) / float64(n)
	}
	costPerSuccess := 0.0
	if successN > 0 {
		costPerSuccess = successCost / float64(successN)
	}
	feedback := 0.0
	if feedbackN > 0 {
		feedback = feedbackSum / float64(feedbackN) // -1..1
	}
	// Normalize latency: 0ms -> 1.0, 10s+ -> ~0. Scale: 1/(1+lat/2000).
	latScore := 1.0 / (1.0 + avgLat/2000.0)
	// Normalize cost: cheaper is better, 1/(1+cost*1000).
	costScore := 1.0 / (1.0 + costPerSuccess*1000.0)
	feedbackNorm := (feedback + 1.0) / 2.0 // 0..1
	if feedbackN == 0 {
		feedbackNorm = 0.5 // neutral when no feedback
	}
	total := w.Success + w.Latency + w.Cost + w.Feedback
	if total == 0 {
		total = 1
	}
	score := (w.Success*successRate + w.Latency*latScore + w.Cost*costScore + w.Feedback*feedbackNorm) / total
	// Penalize fallback-heavy providers slightly.
	score *= 1.0 - 0.1*fallbackRate

	taskScores := map[string]float64{}
	for task, tl := range byTask {
		s := 0
		for _, o := range tl {
			if o.Success {
				s++
			}
		}
		taskScores[task] = float64(s) / float64(len(tl))
	}
	expl := fmt.Sprintf("success=%.2f lat=%.0fms cost/success=$%.4f fallbacks=%.2f (n=%d)", successRate, avgLat, costPerSuccess, fallbackRate, n)
	return domain.ProviderScore{
		ID: domain.NewID(), ProviderID: provider, ProviderName: name,
		Window: window.String(), Requests: n, SuccessRate: successRate,
		TimeoutRate: timeoutRate, RefusalRate: refusalRate, AvgLatencyMS: avgLat,
		CostPerSuccess: costPerSuccess, FallbackRate: fallbackRate,
		FeedbackScore: feedback, ByTask: taskScores, Score: round6(score),
		Explanation: expl, UpdatedAt: time.Now().UTC(),
	}
}

func scoreModel(provider, model string, list []Observation, w Weights, window time.Duration) domain.ModelScore {
	n := len(list)
	success := 0
	var latSum int64
	successCost := 0.0
	successN := 0
	for _, o := range list {
		if o.Success {
			success++
			successCost += o.CostUSD
			successN++
		}
		latSum += o.LatencyMS
	}
	successRate := float64(success) / float64(max(n, 1))
	avgLat := 0.0
	if n > 0 {
		avgLat = float64(latSum) / float64(n)
	}
	costPerSuccess := 0.0
	if successN > 0 {
		costPerSuccess = successCost / float64(successN)
	}
	latScore := 1.0 / (1.0 + avgLat/2000.0)
	costScore := 1.0 / (1.0 + costPerSuccess*1000.0)
	total := w.Success + w.Latency + w.Cost
	if total == 0 {
		total = 1
	}
	score := (w.Success*successRate + w.Latency*latScore + w.Cost*costScore) / total
	expl := fmt.Sprintf("success=%.2f lat=%.0fms cost/success=$%.4f (n=%d)", successRate, avgLat, costPerSuccess, n)
	return domain.ModelScore{
		ID: domain.NewID(), ProviderName: provider, Model: model,
		Window: window.String(), Requests: n, SuccessRate: successRate,
		AvgLatencyMS: avgLat, CostPerSuccess: costPerSuccess,
		Score: round6(score), Explanation: expl, UpdatedAt: time.Now().UTC(),
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func round6(f float64) float64 {
	return float64(int(f*1e6+0.5)) / 1e6
}
