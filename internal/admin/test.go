package admin

import (
	"context"
	"time"

	"github.com/shadowsafin/astrarouter/internal/domain"
	"github.com/shadowsafin/astrarouter/internal/providers"
)

// TestAdapter is the surface the connectivity test needs. It mirrors the
// providers.Adapter interface without importing the registry, so the runner
// stays testable with scripted fakes.
type TestAdapter interface {
	Name() string
	HealthCheck(ctx context.Context) domain.ProviderHealth
	ChatCompletion(ctx context.Context, req *providers.Request) (*providers.Response, error)
}

// ModelLister is implemented by adapters that can enumerate remote models
// (OpenAI, Anthropic, Ollama). The runner asserts it optionally: a kind
// without listing support skips the models check instead of failing it.
type ModelLister interface {
	ListModels(ctx context.Context) ([]string, error)
}

// TestModelSource lists stored models for sample-model selection.
type TestModelSource interface {
	ListByProvider(ctx context.Context, providerID string) ([]domain.Model, error)
}

// TestOptions configures one test run.
type TestOptions struct {
	// Checks selects the steps; empty means all three.
	Checks []domain.TestCheck
	// Model pins the sample completion to an upstream model name. Empty
	// selects the first usable stored model.
	Model string
	// Prompt overrides the sample prompt, capped at 500 characters.
	Prompt string
	// Timeout bounds each check. Zero means 20 seconds.
	Timeout time.Duration
	// ProviderID and ProviderName identify the subject for result rows.
	ProviderID   string
	ProviderName string
	// CreatedBy labels the run in history and audit.
	CreatedBy string
}

// DefaultSamplePrompt is the boring, cheap prompt used when the operator does
// not supply one. It asks for a single word so the cost is negligible.
const DefaultSamplePrompt = "Reply with the word ok and nothing else."

// maxSamplePrompt caps an operator-supplied prompt.
const maxSamplePrompt = 500

// maxListedModels caps how many remote names are kept in a result's detail.
const maxListedModels = 50

// Run executes the selected checks in order and returns one result row per
// check. A failing check does not abort the run: the operator wants the full
// picture (connectivity ok, listing broken, sample unauthenticated) in one go.
func Run(ctx context.Context, adapter TestAdapter, models TestModelSource, opts TestOptions) []domain.ProviderTestResult {
	checks := opts.Checks
	if len(checks) == 0 {
		checks = []domain.TestCheck{
			domain.TestConnectivity, domain.TestModels, domain.TestSample,
		}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}

	var out []domain.ProviderTestResult
	var remoteModels []string
	for _, check := range checks {
		step, cancel := context.WithTimeout(ctx, timeout)
		var res domain.ProviderTestResult
		switch check {
		case domain.TestConnectivity:
			res = runConnectivity(step, adapter, opts)
		case domain.TestModels:
			res, remoteModels = runModels(step, adapter, opts)
		case domain.TestSample:
			res = runSample(step, adapter, models, opts, remoteModels)
		default:
			res = newResult(opts, check, false, 0,
				"unknown check", map[string]any{"check": string(check)})
		}
		cancel()
		out = append(out, res)
	}
	return out
}

func newResult(opts TestOptions, kind domain.TestCheck, success bool,
	latencyMS int64, message string, detail map[string]any,
) domain.ProviderTestResult {
	return domain.ProviderTestResult{
		ID:         domain.NewID(),
		ProviderID: opts.ProviderID,
		Kind:       kind,
		Success:    success,
		LatencyMS:  latencyMS,
		Message:    message,
		Detail:     detail,
		CreatedBy:  opts.CreatedBy,
		CreatedAt:  domain.Now(),
	}
}

func runConnectivity(ctx context.Context, adapter TestAdapter, opts TestOptions) domain.ProviderTestResult {
	start := time.Now()
	health := adapter.HealthCheck(ctx)
	latency := time.Since(start).Milliseconds()
	if health.LatencyMS > 0 {
		latency = health.LatencyMS
	}
	ok := health.State == domain.HealthHealthy || health.State == domain.HealthDegraded
	msg := health.Message
	if ok && msg == "" {
		msg = "provider answered the health check"
	}
	return newResult(opts, domain.TestConnectivity, ok, latency, msg, map[string]any{
		"state": string(health.State),
	})
}

func runModels(ctx context.Context, adapter TestAdapter, opts TestOptions) (domain.ProviderTestResult, []string) {
	lister, ok := adapter.(ModelLister)
	if !ok {
		return newResult(opts, domain.TestModels, true, 0,
			"this provider kind does not expose remote model listing; skipped",
			map[string]any{"skipped": true}), nil
	}
	start := time.Now()
	names, err := lister.ListModels(ctx)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return newResult(opts, domain.TestModels, false, latency,
			"listing remote models failed: "+shortErr(err),
			map[string]any{"error": shortErr(err)}), nil
	}
	kept := names
	if len(kept) > maxListedModels {
		kept = kept[:maxListedModels]
	}
	return newResult(opts, domain.TestModels, true, latency,
		"remote model listing succeeded", map[string]any{
			"count":   len(names),
			"models":  kept,
			"partial": len(names) > len(kept),
		}), names
}

func runSample(ctx context.Context, adapter TestAdapter, models TestModelSource,
	opts TestOptions, remoteModels []string,
) domain.ProviderTestResult {
	model := opts.Model
	if model == "" && models != nil {
		stored, err := models.ListByProvider(ctx, opts.ProviderID)
		if err == nil {
			for _, m := range stored {
				if m.Status.Usable() {
					model = m.Name
					break
				}
			}
		}
	}
	if model == "" && len(remoteModels) > 0 {
		model = remoteModels[0]
	}
	if model == "" {
		return newResult(opts, domain.TestSample, false, 0,
			"no model available: add a model to the provider or pass one explicitly",
			map[string]any{"error": "no_model"})
	}

	prompt := opts.Prompt
	if prompt == "" {
		prompt = DefaultSamplePrompt
	}
	if len(prompt) > maxSamplePrompt {
		prompt = prompt[:maxSamplePrompt]
	}
	maxTokens := 16
	req := &providers.Request{
		Model: model,
		Params: &domain.ChatCompletionRequest{
			Model:     model,
			Messages:  []domain.ChatMessage{{Role: domain.RoleUser, Content: domain.NewTextContent(prompt)}},
			MaxTokens: &maxTokens,
		},
		Ref:       domain.ModelRef{ProviderID: opts.ProviderID, ProviderName: opts.ProviderName, Model: model},
		MaxTokens: maxTokens,
	}

	start := time.Now()
	resp, err := adapter.ChatCompletion(ctx, req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return newResult(opts, domain.TestSample, false, latency,
			"sample completion failed: "+shortErr(err),
			map[string]any{"error": shortErr(err), "model": model})
	}
	excerpt := ""
	if resp != nil {
		excerpt = resp.Content()
		if len(excerpt) > 200 {
			excerpt = excerpt[:200]
		}
	}
	return newResult(opts, domain.TestSample, true, latency,
		"sample completion succeeded", map[string]any{
			"model":   model,
			"excerpt": excerpt,
		})
}

// shortErr keeps provider errors readable in history; the full error stays in
// the server log with the request id.
func shortErr(err error) string {
	msg := err.Error()
	if len(msg) > 300 {
		return msg[:300]
	}
	return msg
}
