package admin

import (
	"context"
	"strings"
	"time"

	"github.com/shadowsafin/corerouter/internal/domain"
)

// ModelStore is the persistence surface model sync needs. The storage model
// repository satisfies it unmodified; tests use an in-memory fake.
type ModelStore interface {
	ListByProvider(ctx context.Context, providerID string) ([]domain.Model, error)
	Upsert(ctx context.Context, m *domain.Model) (*domain.Model, error)
}

// SyncOptions configures one model sync run.
type SyncOptions struct {
	// ProviderID and ProviderName identify the sync subject.
	ProviderID   string
	ProviderName string
	// Environment is stamped on newly created rows, inheriting the provider's
	// environment so test fixtures do not leak into production routing.
	Environment domain.Environment
	// CreatedBy labels the run in history and audit.
	CreatedBy string
	// Timeout bounds the remote listing. Zero means 20 seconds.
	Timeout time.Duration
}

// SyncResult reports what one sync run did.
type SyncResult struct {
	ProviderID string   `json:"provider_id"`
	Created    []string `json:"created"`
	Skipped    []string `json:"skipped"`
	Total      int      `json:"total"`
}

// SyncModels discovers a provider's remote models and populates the registry.
//
// Every remote name absent from the registry becomes a model row owned by the
// API. Rows that already exist are left untouched: a re-sync must never revert
// operator edits to pricing, aliases, status or priority. Disabling or
// deleting a model therefore survives re-syncs, which is what makes the sync
// safe to run repeatedly.
func SyncModels(ctx context.Context, adapter TestAdapter, store ModelStore, opts SyncOptions) (*SyncResult, error) {
	lister, ok := adapter.(ModelLister)
	if !ok {
		return nil, domain.Errorf(domain.ErrCodeNotImplemented,
			"provider kind %q does not expose remote model listing", adapterKind(adapter))
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 20 * time.Second
	}
	step, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	names, err := lister.ListModels(step)
	if err != nil {
		return nil, domain.NewError(domain.ErrCodeUpstream,
			"listing remote models failed").Wrap(err)
	}

	existing := map[string]bool{}
	if store != nil {
		if stored, lerr := store.ListByProvider(ctx, opts.ProviderID); lerr == nil {
			for _, m := range stored {
				existing[strings.ToLower(m.Name)] = true
			}
		}
	}

	env := opts.Environment
	if env == "" {
		env = domain.EnvProduction
	}
	res := &SyncResult{ProviderID: opts.ProviderID, Total: len(names)}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if existing[strings.ToLower(name)] {
			res.Skipped = append(res.Skipped, name)
			continue
		}
		m := &domain.Model{
			ID:          domain.NewID(),
			ProviderID:  opts.ProviderID,
			Name:        name,
			DisplayName: name,
			Status:      domain.ModelActive,
			Priority:    100,
			Environment: env,
			ManagedBy:   domain.ManagedByAPI,
			CreatedAt:   domain.Now(),
			UpdatedAt:   domain.Now(),
		}
		if store == nil {
			res.Created = append(res.Created, name)
			continue
		}
		if _, uerr := store.Upsert(ctx, m); uerr != nil {
			return nil, domain.NewError(domain.ErrCodeInternal,
				"store discovered model").Wrap(uerr)
		}
		existing[strings.ToLower(name)] = true
		res.Created = append(res.Created, name)
	}
	return res, nil
}

// adapterKind best-efforts the adapter kind for error messages.
func adapterKind(a TestAdapter) string {
	type kinder interface{ Kind() domain.ProviderKind }
	if k, ok := a.(kinder); ok {
		return string(k.Kind())
	}
	return a.Name()
}
