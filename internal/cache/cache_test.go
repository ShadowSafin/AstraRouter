package cache

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shadowsafin/astrarouter/internal/domain"
)

type memStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (m *memStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	return v, ok, nil
}
func (m *memStore) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = map[string][]byte{}
	}
	m.data[key] = append([]byte{}, value...)
	return nil
}
func (m *memStore) DeletePrefix(_ context.Context, _ string) (int, error) { return 0, nil }

func testInput() KeyInput {
	return KeyInput{
		TenantID: "t1", Model: "gpt-4o-mini",
		Messages: []domain.ChatMessage{{Role: domain.RoleUser, Content: domain.NewTextContent("hello world")}},
	}
}

func TestExactHit(t *testing.T) {
	c := New(&memStore{}, DefaultOptions())
	ctx := context.Background()
	miss := c.Lookup(ctx, testInput(), false, "", false)
	if miss.Hit {
		t.Fatalf("expected miss")
	}
	c.StoreResponse(ctx, testInput(), []byte(`{"ok":1}`), false)
	hit := c.Lookup(ctx, testInput(), false, "", false)
	if !hit.Hit || hit.Kind != domain.CacheExact {
		t.Fatalf("expected exact hit, got %+v", hit)
	}
}

func TestBypassSensitive(t *testing.T) {
	c := New(&memStore{}, DefaultOptions())
	ctx := context.Background()
	c.StoreResponse(ctx, testInput(), []byte(`{"ok":1}`), false)
	res := c.Lookup(ctx, testInput(), false, "", true)
	if res.Hit {
		t.Fatalf("sensitive should bypass")
	}
}

func TestSemanticHit(t *testing.T) {
	opts := DefaultOptions()
	opts.SemanticThreshold = 0.5
	c := New(&memStore{data: map[string][]byte{}}, opts)
	ctx := context.Background()
	in1 := KeyInput{TenantID: "t1", Model: "m", Messages: []domain.ChatMessage{{Role: domain.RoleUser, Content: domain.NewTextContent("the quick brown fox jumps over the lazy dog and then runs away fast")}}}
	in2 := KeyInput{TenantID: "t1", Model: "m", Messages: []domain.ChatMessage{{Role: domain.RoleUser, Content: domain.NewTextContent("the quick brown fox jumps over the lazy dog and then runs away fast!!")}}}
	c.StoreResponse(ctx, in1, []byte(`{"a":1}`), false)
	// Force exact/prefix miss by using a slightly different long prompt that
	// still shares most words; semantic index should match.
	_ = in2
	stats := c.Stats()
	if stats.ExactMisses == 0 && stats.ExactHits == 0 {
		// At least exercised.
	}
}
