package replay

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shadowsafin/synapass/internal/domain"
	"github.com/shadowsafin/synapass/internal/providers"
)

type fakeStore struct {
	jobs    map[string]*domain.ReplayJob
	runs    map[string]*domain.EvaluationRun
	results []*domain.EvaluationResult
}

func newFakeStore() *fakeStore {
	return &fakeStore{jobs: map[string]*domain.ReplayJob{}, runs: map[string]*domain.EvaluationRun{}}
}

func (f *fakeStore) CreateJob(_ context.Context, job *domain.ReplayJob) error {
	f.jobs[job.ID] = job
	return nil
}
func (f *fakeStore) UpdateJob(_ context.Context, job *domain.ReplayJob) error {
	f.jobs[job.ID] = job
	return nil
}
func (f *fakeStore) GetJob(_ context.Context, id string) (*domain.ReplayJob, error) {
	return f.jobs[id], nil
}
func (f *fakeStore) ListJobs(_ context.Context, _ string, _ int) ([]domain.ReplayJob, error) {
	return nil, nil
}
func (f *fakeStore) CreateRun(_ context.Context, run *domain.EvaluationRun) error {
	f.runs[run.ID] = run
	return nil
}
func (f *fakeStore) UpdateRun(_ context.Context, run *domain.EvaluationRun) error {
	f.runs[run.ID] = run
	return nil
}
func (f *fakeStore) AddResult(_ context.Context, res *domain.EvaluationResult) error {
	f.results = append(f.results, res)
	return nil
}
func (f *fakeStore) ListResults(_ context.Context, _ string, _ int) ([]domain.EvaluationResult, error) {
	return nil, nil
}

type fakeLogs struct {
	entries map[string]*domain.RequestLog
}

func (f *fakeLogs) GetByRequestID(_ context.Context, id string) (*domain.RequestLog, error) {
	return f.entries[id], nil
}
func (f *fakeLogs) List(_ context.Context, _ ListFilter) ([]domain.RequestLog, error) {
	return nil, nil
}

type fakePayloads struct {
	entries map[string]*domain.RequestPayload
}

func (f *fakePayloads) Get(_ context.Context, id string) (*domain.RequestPayload, error) {
	return f.entries[id], nil
}

type fakeAdapter struct {
	name    string
	enabled bool
	output  string
	usage   domain.TokenUsage
	err     error
	calls   int
}

func (f *fakeAdapter) Name() string                              { return f.name }
func (f *fakeAdapter) Kind() domain.ProviderKind                 { return domain.ProviderOpenAICompatible }
func (f *fakeAdapter) Capabilities() domain.CapabilitySet        { return domain.CapabilitySet{} }
func (f *fakeAdapter) HealthCheck(context.Context) domain.ProviderHealth {
	return domain.ProviderHealth{State: domain.HealthHealthy}
}
func (f *fakeAdapter) Enabled() bool { return f.enabled }
func (f *fakeAdapter) ChatCompletion(_ context.Context, req *providers.Request) (*providers.Response, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &providers.Response{
		Model:   req.Model,
		Choices: []domain.Choice{{Message: &domain.ChatMessage{Role: domain.RoleAssistant, Content: domain.NewTextContent(f.output)}}},
		Usage:   f.usage,
	}, nil
}
func (f *fakeAdapter) ChatCompletionStream(_ context.Context, req *providers.Request, _ providers.StreamHandler) (*providers.Response, error) {
	return f.ChatCompletion(context.Background(), req)
}

type fakeAdapters struct {
	byName map[string]*fakeAdapter
}

func (f *fakeAdapters) Resolve(idOrName string) (providers.Adapter, bool) {
	a, ok := f.byName[idOrName]
	if !ok || a == nil {
		return nil, false
	}
	return a, true
}

// stubPricer prices at 0.5/M input and 2.0/M output, the sheet the cost
// test below asserts against.
func stubPricer(_ context.Context, _, _, _ string, usage domain.TokenUsage) float64 {
	return float64(usage.PromptTokens)/1_000_000*0.5 +
		float64(usage.CompletionTokens)/1_000_000*2.0
}

func testPayload() *domain.RequestPayload {
	return &domain.RequestPayload{
		RequestID: "req_1",
		Model:     "flash",
		Messages:  []domain.ChatMessage{{Role: domain.RoleUser, Content: domain.NewTextContent("hi")}},
	}
}

func TestRunnerReplaysAgainstOriginalTarget(t *testing.T) {
	store := newFakeStore()
	job := &domain.ReplayJob{ID: "job1", Status: "queued", RequestIDs: []string{"req_1"}, MaxRequests: 10}
	store.jobs[job.ID] = job
	adapter := &fakeAdapter{name: "bynara", enabled: true, output: "hello",
		usage: domain.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}
	r := NewRunner(RunnerDeps{
		Payloads:  &fakePayloads{entries: map[string]*domain.RequestPayload{"req_1": testPayload()}},
		Logs:      &fakeLogs{entries: map[string]*domain.RequestLog{"req_1": {RequestID: "req_1", Provider: "bynara", RequestedModel: "flash", RoutedModel: "flash"}}},
		Store:    store,
		Adapters: &fakeAdapters{byName: map[string]*fakeAdapter{"bynara": adapter}},
		Pricer:   stubPricer,
	})
	run, err := r.Run(context.Background(), "job1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" {
		t.Fatalf("status = %q, want completed (error %q)", run.Status, run.Error)
	}
	if adapter.calls != 1 {
		t.Fatalf("adapter calls = %d, want 1", adapter.calls)
	}
	if len(store.results) != 1 {
		t.Fatalf("results = %d, want 1", len(store.results))
	}
	res := store.results[0]
	if res.EvaluationID != run.ID {
		t.Error("result is not linked to the run")
	}
	if res.OutputPreview != "hello" {
		t.Errorf("preview = %q, want hello", res.OutputPreview)
	}
	// 10 prompt + 5 completion at 0.5/2.0 per million = 0.000015.
	if diff := res.CostUSD - 0.000015; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("cost = %v, want 0.000015", res.CostUSD)
	}
	if res.Metrics["total_tokens"] != 15 {
		t.Errorf("total_tokens metric = %v, want 15", res.Metrics["total_tokens"])
	}
	if job.Status != "completed" || job.Progress != 1 || job.Total != 1 {
		t.Errorf("job = %+v, want completed 1/1", job)
	}
}

func TestRunnerCrossProductTargets(t *testing.T) {
	store := newFakeStore()
	job := &domain.ReplayJob{ID: "job1", Status: "queued", RequestIDs: []string{"req_1"},
		Providers: []string{"a", "b"}, Models: []string{"m1", "m2"}, MaxRequests: 10}
	store.jobs[job.ID] = job
	a := &fakeAdapter{name: "a", enabled: true, output: "A"}
	b := &fakeAdapter{name: "b", enabled: true, output: "B"}
	r := NewRunner(RunnerDeps{
		Payloads: &fakePayloads{entries: map[string]*domain.RequestPayload{"req_1": testPayload()}},
		Logs:     &fakeLogs{entries: map[string]*domain.RequestLog{"req_1": {RequestID: "req_1", Provider: "a", RequestedModel: "m1"}}},
		Store:    store,
		Adapters: &fakeAdapters{byName: map[string]*fakeAdapter{"a": a, "b": b}},
	})
	run, err := r.Run(context.Background(), "job1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" {
		t.Fatalf("status = %q (%q)", run.Status, run.Error)
	}
	if a.calls+b.calls != 4 {
		t.Fatalf("calls = %d, want 4 (2 providers x 2 models)", a.calls+b.calls)
	}
	if job.Total != 4 || job.Progress != 4 {
		t.Errorf("job total/progress = %d/%d, want 4/4", job.Total, job.Progress)
	}
}

func TestRunnerMissingPayloadRecordsErrorResult(t *testing.T) {
	store := newFakeStore()
	job := &domain.ReplayJob{ID: "job1", Status: "queued", RequestIDs: []string{"req_1"}, MaxRequests: 10}
	store.jobs[job.ID] = job
	adapter := &fakeAdapter{name: "a", enabled: true, output: "A"}
	r := NewRunner(RunnerDeps{
		Payloads: &fakePayloads{entries: map[string]*domain.RequestPayload{}},
		Logs:     &fakeLogs{entries: map[string]*domain.RequestLog{"req_1": {RequestID: "req_1", Provider: "a", RequestedModel: "m"}}},
		Store:    store,
		Adapters: &fakeAdapters{byName: map[string]*fakeAdapter{"a": adapter}},
	})
	run, err := r.Run(context.Background(), "job1")
	if err != nil {
		t.Fatal(err)
	}
	// Nothing executed successfully, but the failure is recorded, not silent.
	if run.Status != "failed" {
		t.Fatalf("status = %q, want failed", run.Status)
	}
	if adapter.calls != 0 {
		t.Fatalf("adapter calls = %d, want 0", adapter.calls)
	}
	if len(store.results) != 1 {
		t.Fatalf("results = %d, want 1 error row", len(store.results))
	}
	if job.Status != "failed" || job.Progress != 1 {
		t.Errorf("job = %+v, want failed 1/1", job)
	}
}

func TestRunnerUnknownProvider(t *testing.T) {
	store := newFakeStore()
	job := &domain.ReplayJob{ID: "job1", Status: "queued", RequestIDs: []string{"req_1"},
		Providers: []string{"ghost"}, Models: []string{"m"}, MaxRequests: 10}
	store.jobs[job.ID] = job
	r := NewRunner(RunnerDeps{
		Payloads: &fakePayloads{entries: map[string]*domain.RequestPayload{"req_1": testPayload()}},
		Logs:     &fakeLogs{entries: map[string]*domain.RequestLog{"req_1": {RequestID: "req_1", Provider: "a", RequestedModel: "m"}}},
		Store:    store,
		Adapters: &fakeAdapters{byName: map[string]*fakeAdapter{}},
	})
	run, err := r.Run(context.Background(), "job1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" {
		t.Fatalf("status = %q, want failed", run.Status)
	}
	if !contains(store.results[0].OutputPreview, "unknown provider") {
		t.Errorf("preview = %q, want unknown-provider error", store.results[0].OutputPreview)
	}
}

func TestRunnerProviderErrorIsPartialSuccess(t *testing.T) {
	store := newFakeStore()
	job := &domain.ReplayJob{ID: "job1", Status: "queued",
		RequestIDs: []string{"req_1", "req_2"}, MaxRequests: 10}
	store.jobs[job.ID] = job
	bad := &fakeAdapter{name: "a", enabled: true, err: errors.New("boom")}
	good := &fakeAdapter{name: "b", enabled: true, output: "ok",
		usage: domain.TokenUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}}
	r := NewRunner(RunnerDeps{
		Payloads: &fakePayloads{entries: map[string]*domain.RequestPayload{
			"req_1": testPayload(),
			"req_2": {RequestID: "req_2", Messages: []domain.ChatMessage{{Role: domain.RoleUser, Content: domain.NewTextContent("yo")}}},
		}},
		Logs: &fakeLogs{entries: map[string]*domain.RequestLog{
			"req_1": {RequestID: "req_1", Provider: "a", RequestedModel: "m"},
			"req_2": {RequestID: "req_2", Provider: "b", RequestedModel: "m"},
		}},
		Store:    store,
		Adapters: &fakeAdapters{byName: map[string]*fakeAdapter{"a": bad, "b": good}},
	})
	run, err := r.Run(context.Background(), "job1")
	if err != nil {
		t.Fatal(err)
	}
	// req_1 failed upstream, req_2 succeeded. Both record rows and the job
	// completes with the failure noted on the run.
	if len(store.results) != 2 {
		t.Fatalf("results = %d, want 2", len(store.results))
	}
	if run.Status != "completed" {
		t.Fatalf("status = %q, want completed", run.Status)
	}
	if run.Error == "" {
		t.Error("run error should note the failed execution")
	}
	if job.Progress != 2 || job.Total != 2 {
		t.Errorf("job = %+v, want 2/2", job)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
