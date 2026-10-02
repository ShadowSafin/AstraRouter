package admin

import (
	"context"
	"testing"
	"time"

	"github.com/shadowsafin/astrarouter/internal/domain"
	"github.com/shadowsafin/astrarouter/internal/providers"
)

func TestSealOpenRoundTrip(t *testing.T) {
	key, err := KeyMaterial("0123456789abcdef0123456789abcdef", "")
	if err != nil {
		t.Fatalf("key material: %v", err)
	}
	store, err := NewStore(key)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	env, err := store.Seal("sk-secret-value")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if len(env.Ciphertext) == 0 || len(env.Nonce) == 0 {
		t.Fatal("expected a populated envelope")
	}
	plain, err := store.Open(env)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if plain != "sk-secret-value" {
		t.Fatalf("round trip mismatch: %q", plain)
	}
}

func TestSealRejectsEmpty(t *testing.T) {
	store, err := NewStore(make([]byte, 32))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.Seal("   "); err == nil {
		t.Fatal("expected an error sealing a blank secret")
	}
}

func TestKeyDerivationDeterministic(t *testing.T) {
	first, err := KeyMaterial("", "admin-key-one")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	second, err := KeyMaterial("", "admin-key-one")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("derivation must be deterministic for a stable admin key")
	}
	other, err := KeyMaterial("", "admin-key-two")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if string(first) == string(other) {
		t.Fatal("different admin keys must derive different data keys")
	}
	if _, err := KeyMaterial("", ""); err == nil {
		t.Fatal("expected an error with no key material at all")
	}
}

func TestKeyDerivationDecryptsOwnCiphertext(t *testing.T) {
	key, err := KeyMaterial("", "admin-key-one")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	store, err := NewStore(key)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	env, err := store.Seal("up-123")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	other, err := NewStore(mustKey(t, "admin-key-two"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := other.Open(env); err == nil {
		t.Fatal("a different data key must not open the envelope")
	}
}

func mustKey(t *testing.T, admin string) []byte {
	t.Helper()
	key, err := KeyMaterial("", admin)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	return key
}

func TestValidateProvider(t *testing.T) {
	good := &domain.Provider{Name: "acme", Kind: domain.ProviderOpenAI, BaseURL: "https://api.acme.test/v1"}
	if err := ValidateProvider(good); err != nil {
		t.Fatalf("valid provider rejected: %v", err)
	}
	// Anthropic without an explicit header is valid: the kind supplies the
	// x-api-key default.
	anthropic := &domain.Provider{Name: "claude", Kind: domain.ProviderAnthropic,
		BaseURL: "https://api.anthropic.com", AuthStyle: domain.AuthHeader}
	if err := ValidateProvider(anthropic); err != nil {
		t.Fatalf("valid anthropic provider rejected: %v", err)
	}
	cases := []domain.Provider{
		{Kind: domain.ProviderOpenAI, BaseURL: "https://x.test"},
		{Name: "x", Kind: "nope", BaseURL: "https://x.test"},
		{Name: "x", Kind: domain.ProviderOpenAI, BaseURL: "ftp://x.test"},
		{Name: "x", Kind: domain.ProviderOpenAI, BaseURL: "https://x.test", Weight: -1},
		{Name: "x", Kind: domain.ProviderOpenAI, BaseURL: "https://x.test", AuthStyle: "token"},
		{Name: "x", Kind: domain.ProviderOpenAI, BaseURL: "https://x.test", Status: "melting"},
	}
	for i, c := range cases {
		if err := ValidateProvider(&c); err == nil {
			t.Fatalf("case %d: expected a validation error", i)
		}
	}
}

func TestNormalizeKeyScopes(t *testing.T) {
	scopes, err := NormalizeKeyScopes(nil)
	if err != nil || len(scopes) != 1 || scopes[0] != "inference" {
		t.Fatalf("default scopes = %v, %v", scopes, err)
	}
	if _, err := NormalizeKeyScopes([]string{"launch-missiles"}); err == nil {
		t.Fatal("expected an error for an unknown scope")
	}
}

// fakeAdapter scripts the three test surfaces.
type fakeAdapter struct {
	name     string
	health   domain.ProviderHealth
	models   []string
	listErr  error
	listing  bool
	respErr  error
	respText string
}

func (f *fakeAdapter) Name() string { return f.name }

func (f *fakeAdapter) HealthCheck(context.Context) domain.ProviderHealth { return f.health }

func (f *fakeAdapter) ChatCompletion(context.Context, *providers.Request) (*providers.Response, error) {
	if f.respErr != nil {
		return nil, f.respErr
	}
	return &providers.Response{Choices: []domain.Choice{{Message: &domain.ChatMessage{
		Role:    domain.RoleAssistant,
		Content: domain.NewTextContent(f.respText),
	}}}}, nil
}

func (f *fakeAdapter) ListModels(context.Context) ([]string, error) {
	return f.models, f.listErr
}

type fakeModels struct {
	models []domain.Model
}

// minimalAdapter implements only the core adapter surface, proving the
// runner skips the models check for kinds without listing support.
type minimalAdapter struct {
	health   domain.ProviderHealth
	respText string
}

func (m *minimalAdapter) Name() string { return "local" }

func (m *minimalAdapter) HealthCheck(context.Context) domain.ProviderHealth { return m.health }

func (m *minimalAdapter) ChatCompletion(context.Context, *providers.Request) (*providers.Response, error) {
	return &providers.Response{Choices: []domain.Choice{{Message: &domain.ChatMessage{
		Role:    domain.RoleAssistant,
		Content: domain.NewTextContent(m.respText),
	}}}}, nil
}

func (f *fakeModels) ListByProvider(context.Context, string) ([]domain.Model, error) {
	return f.models, nil
}

func TestRunAllChecks(t *testing.T) {
	adapter := &fakeAdapter{
		name:     "acme",
		health:   domain.ProviderHealth{State: domain.HealthHealthy, LatencyMS: 12},
		models:   []string{"a-1", "a-2"},
		listing:  true,
		respText: "ok",
	}
	models := &fakeModels{models: []domain.Model{
		{ID: "m1", Name: "a-1", Status: domain.ModelActive},
	}}
	results := Run(context.Background(), adapter, models, TestOptions{
		ProviderID: "p1", Timeout: time.Second,
	})
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for _, res := range results {
		if !res.Success {
			t.Fatalf("check %s failed: %s", res.Kind, res.Message)
		}
		if res.ProviderID != "p1" || res.ID == "" {
			t.Fatalf("result missing identity: %+v", res)
		}
	}
}

func TestRunSkipsListingWithoutSupport(t *testing.T) {
	adapter := &minimalAdapter{
		health:   domain.ProviderHealth{State: domain.HealthHealthy},
		respText: "ok",
	}
	// The minimal fake must not expose ListModels; assert at runtime.
	if _, ok := any(adapter).(ModelLister); ok {
		t.Fatal("test fake unexpectedly implements ModelLister")
	}
	results := Run(context.Background(), adapter, &fakeModels{}, TestOptions{
		Checks:     []domain.TestCheck{domain.TestModels},
		ProviderID: "p1",
	})
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("expected a skipped-but-successful models check: %+v", results)
	}
}

func TestRunSampleWithoutModel(t *testing.T) {
	adapter := &fakeAdapter{
		name:   "acme",
		health: domain.ProviderHealth{State: domain.HealthHealthy},
	}
	results := Run(context.Background(), adapter, &fakeModels{}, TestOptions{
		Checks:     []domain.TestCheck{domain.TestSample},
		ProviderID: "p1",
	})
	if len(results) != 1 || results[0].Success {
		t.Fatalf("expected a failed sample check: %+v", results)
	}
}

// memModelStore is an in-memory ModelStore.
type memModelStore struct {
	rows []domain.Model
}

func (m *memModelStore) ListByProvider(context.Context, string) ([]domain.Model, error) {
	return m.rows, nil
}

func (m *memModelStore) Upsert(_ context.Context, model *domain.Model) (*domain.Model, error) {
	m.rows = append(m.rows, *model)
	return model, nil
}

func TestSyncModelsCreatesMissing(t *testing.T) {
	adapter := &fakeAdapter{name: "acme", models: []string{"a-1", "a-2", "a-1"}}
	store := &memModelStore{rows: []domain.Model{{ID: "x", Name: "a-0"}}}
	res, err := SyncModels(context.Background(), adapter, store, SyncOptions{
		ProviderID: "p1", Environment: domain.EnvTest,
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	// The duplicate remote name is created once and skipped once.
	if len(res.Created) != 2 || len(res.Skipped) != 1 || res.Total != 3 {
		t.Fatalf("unexpected result: %+v", res)
	}
	for _, m := range store.rows[1:] {
		if m.ProviderID != "p1" {
			t.Fatalf("row missing provider link: %+v", m)
		}
		if m.ManagedBy != domain.ManagedByAPI {
			t.Fatalf("synced row must be api-managed: %+v", m)
		}
	}
	if store.rows[1].Environment != domain.EnvTest {
		t.Fatalf("synced row must inherit the environment: %+v", store.rows[1])
	}
}

func TestSyncModelsSkipsExisting(t *testing.T) {
	adapter := &fakeAdapter{name: "acme", models: []string{"a-1", "a-2"}}
	store := &memModelStore{rows: []domain.Model{
		{ID: "x", Name: "a-1", Status: domain.ModelDisabled, InputCostPerMillion: 5},
	}}
	res, err := SyncModels(context.Background(), adapter, store, SyncOptions{ProviderID: "p1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(res.Created) != 1 || len(res.Skipped) != 1 || res.Skipped[0] != "a-1" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if store.rows[0].Status != domain.ModelDisabled || store.rows[0].InputCostPerMillion != 5 {
		t.Fatal("re-sync must not touch operator-edited rows")
	}
}

func TestSyncModelsWithoutListing(t *testing.T) {
	adapter := &minimalAdapter{health: domain.ProviderHealth{State: domain.HealthHealthy}}
	if _, err := SyncModels(context.Background(), adapter, &memModelStore{}, SyncOptions{}); err == nil {
		t.Fatal("expected not_implemented for a kind without listing support")
	}
}
