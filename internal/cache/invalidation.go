package cache

import (
	"context"
	"strings"
)

// InvalidateScope describes one flush operation. TenantID is required for
// tenant/model/provider scopes; empty means global (admin "flush all").
type InvalidateScope struct {
	Scope    string
	TenantID string
	Model    string
	Provider string
	Key      string
	Reason   string
}

// tenantPrefix returns the Redis namespace prefix for a tenant. All cache
// bodies live under "response:<ns>" where <ns> starts with the tenant, so a
// tenant flush touches exactly that tenant's keys and nothing else.
func tenantPrefix(tenantID string) string {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return ""
	}
	return "tenant:" + tenantID + ":"
}

// Invalidate removes entries for a scope and returns the removed count.
// Tenant isolation is structural: global exact/prefix/semantic prefixes are
// never deleted for a tenant-scoped flush, only that tenant's namespace.
func (c *Cache) Invalidate(ctx context.Context, scope InvalidateScope) (int, error) {
	if c == nil {
		return 0, nil
	}
	reason := scope.Reason
	if reason == "" {
		reason = "manual_flush"
	}
	removed := 0
	switch scope.Scope {
	case "", "all":
		if c.store != nil {
			for _, p := range []string{"tenant:", "exact:", "prefix:", "semantic:"} {
				n, err := c.store.DeletePrefix(ctx, p)
				removed += n
				if err != nil {
					return removed, err
				}
			}
		}
		c.mu.Lock()
		removed += len(c.memIndex)
		c.memIndex = map[string]semanticEntry{}
		c.stats.Invalidations++
		c.mu.Unlock()
		return removed, nil
	case "tenant":
		prefix := tenantPrefix(scope.TenantID)
		if prefix == "" {
			return c.Invalidate(ctx, InvalidateScope{Scope: "all", Reason: reason})
		}
		if c.store != nil {
			n, err := c.store.DeletePrefix(ctx, "response:"+prefix)
			removed += n
			if err != nil {
				return removed, err
			}
			// Legacy un-namespaced keys cannot be attributed; leave them.
		}
		c.mu.Lock()
		for k, e := range c.memIndex {
			if e.tenant == scope.TenantID {
				delete(c.memIndex, k)
				removed++
			}
		}
		c.stats.Invalidations++
		c.mu.Unlock()
		return removed, nil
	case "key":
		if c.store != nil && scope.Key != "" {
			// Exact-key flush: delete the tenant-namespaced variants plus the
			// legacy bare key for compatibility.
			for _, k := range []string{scope.Key, "tenant:" + scope.TenantID + ":" + scope.Key} {
				if k == "" || k == "tenant::" {
					continue
				}
				if _, err := c.store.DeletePrefix(ctx, "response:"+k); err != nil {
					return removed, err
				}
				removed++
			}
		}
		c.mu.Lock()
		for k := range c.memIndex {
			if scope.Key != "" && strings.Contains(k, scope.Key) {
				delete(c.memIndex, k)
				removed++
			}
		}
		c.stats.Invalidations++
		c.mu.Unlock()
		return removed, nil
	case "model", "provider":
		// Model/provider changes cannot be mapped to key hashes without an
		// index, so the safe action is a tenant-scoped flush when a tenant is
		// given, otherwise a global flush. The reason records what changed so
		// the audit trail stays honest about the blast radius.
		if scope.TenantID != "" {
			return c.Invalidate(ctx, InvalidateScope{Scope: "tenant", TenantID: scope.TenantID, Reason: reason})
		}
		return c.Invalidate(ctx, InvalidateScope{Scope: "all", Reason: reason})
	default:
		return c.Invalidate(ctx, InvalidateScope{Scope: "all", Reason: reason})
	}
}
