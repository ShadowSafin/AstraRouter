// Package feedback records explicit user feedback and feeds provider scoring.
package feedback

import (
	"sync"

	"github.com/shadowsafin/astrarouter/internal/domain"
)

// Store persists feedback events.
type Store interface {
	Insert(event *domain.FeedbackEvent) error
	ListByRequest(requestID string) ([]domain.FeedbackEvent, error)
	AverageForProvider(provider string, limit int) (float64, int, error)
}

// MemoryStore is an in-process store for tests and single-replica installs.
type MemoryStore struct {
	mu     sync.Mutex
	events []domain.FeedbackEvent
}

// NewMemory creates a memory store.
func NewMemory() *MemoryStore { return &MemoryStore{} }

// Insert adds an event.
func (m *MemoryStore) Insert(e *domain.FeedbackEvent) error {
	if e == nil {
		return nil
	}
	if e.ID == "" {
		e.ID = domain.NewID()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = domain.Now()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, *e)
	return nil
}

// ListByRequest returns events for a request.
func (m *MemoryStore) ListByRequest(requestID string) ([]domain.FeedbackEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.FeedbackEvent{}
	for _, e := range m.events {
		if e.RequestID.String() == requestID {
			out = append(out, e)
		}
	}
	return out, nil
}

// AverageForProvider is a stub: provider attribution lives on usage rows,
// so this reports the global average. The SQL store implements the real join.
func (m *MemoryStore) AverageForProvider(_ string, _ int) (float64, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events) == 0 {
		return 0, 0, nil
	}
	sum := 0.0
	for _, e := range m.events {
		sum += e.Score
	}
	return sum / float64(len(m.events)), len(m.events), nil
}

// NormalizeScore maps 1..5 ratings and -1/0/1 votes onto -1..1.
func NormalizeScore(v float64) float64 {
	switch {
	case v >= 4:
		return 1
	case v == 3:
		return 0
	case v <= 2 && v >= 1:
		return -1
	case v > 1 || v < -1:
		if v > 0 {
			return 1
		}
		return -1
	default:
		return v
	}
}
