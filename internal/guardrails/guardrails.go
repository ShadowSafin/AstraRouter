// Package guardrails implements safer production controls: kill switches,
// tenant emergency overrides, hard budget caps, fallback blocking, circuit
// breaker state and visible deny reasons.
package guardrails

import (
	"fmt"
	"sync"
	"time"

	"github.com/shadowsafin/synapass/internal/domain"
)

// Override kinds.
const (
	KindKillSwitch        = "kill_switch"
	KindTenantOverride    = "tenant_override"
	KindBudgetCap         = "budget_cap"
	KindFallbackBlock     = "fallback_block"
	KindCircuitOverride   = "circuit_override"
	KindRateLimitEscalate = "rate_limit_escalation"
)

// Controller holds in-memory override state backed by durable audit rows.
type Controller struct {
	mu sync.RWMutex
	// killed providers: provider name/ID -> reason.
	killed map[string]string
	// tenant overrides: tenantID -> override.
	tenantOverrides map[string]domain.AuditOverride
	// fallback blocked: policyID or tenantID -> reason.
	fallbackBlocked map[string]string
	// hard caps: tenantID -> max USD per request.
	hardCaps map[string]float64
	// circuit overrides: providerID -> forced open/closed.
	circuitForced map[string]bool
	// rate escalation: tenantID -> multiplier.
	rateEscalation map[string]float64
}

// New creates a controller.
func New() *Controller {
	return &Controller{
		killed:          map[string]string{},
		tenantOverrides: map[string]domain.AuditOverride{},
		fallbackBlocked: map[string]string{},
		hardCaps:        map[string]float64{},
		circuitForced:   map[string]bool{},
		rateEscalation:  map[string]float64{},
	}
}

// Kill switches off a provider immediately.
func (c *Controller) Kill(provider, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.killed[provider] = reason
}

// Revive removes a kill switch.
func (c *Controller) Revive(provider string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.killed, provider)
}

// Killed reports whether a provider is killed.
func (c *Controller) Killed(provider string) (bool, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.killed[provider]
	return ok, r
}

// SetTenantOverride installs a tenant emergency override.
func (c *Controller) SetTenantOverride(o domain.AuditOverride) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tenantOverrides[o.TenantID] = o
}

// ClearTenantOverride removes it.
func (c *Controller) ClearTenantOverride(tenantID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tenantOverrides, tenantID)
}

// TenantOverride returns the override, if any and unexpired.
func (c *Controller) TenantOverride(tenantID string) (domain.AuditOverride, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	o, ok := c.tenantOverrides[tenantID]
	if !ok {
		return domain.AuditOverride{}, false
	}
	if o.ExpiresAt != nil && time.Now().After(*o.ExpiresAt) {
		return domain.AuditOverride{}, false
	}
	return o, true
}

// BlockFallback prevents failover for a scope (policy or tenant).
func (c *Controller) BlockFallback(scope, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fallbackBlocked[scope] = reason
}

// UnblockFallback removes the block.
func (c *Controller) UnblockFallback(scope string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.fallbackBlocked, scope)
}

// FallbackBlocked reports whether failover is blocked for scope.
func (c *Controller) FallbackBlocked(scope string) (bool, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.fallbackBlocked[scope]
	return ok, r
}

// SetHardCap sets a hard per-request budget cap for a tenant (0 clears).
func (c *Controller) SetHardCap(tenantID string, maxUSD float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if maxUSD <= 0 {
		delete(c.hardCaps, tenantID)
		return
	}
	c.hardCaps[tenantID] = maxUSD
}

// HardCap returns the cap, if any.
func (c *Controller) HardCap(tenantID string) (float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.hardCaps[tenantID]
	return v, ok
}

// ForceCircuit pins a provider circuit open (true) or closed (false is clear).
func (c *Controller) ForceCircuit(providerID string, open bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.circuitForced[providerID] = open
}

// ClearCircuit removes the pin.
func (c *Controller) ClearCircuit(providerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.circuitForced, providerID)
}

// CircuitForced returns the pin, if any.
func (c *Controller) CircuitForced(providerID string) (bool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.circuitForced[providerID]
	return v, ok
}

// EscalateRate sets a rate-limit multiplier (<1 tightens).
func (c *Controller) EscalateRate(tenantID string, multiplier float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rateEscalation[tenantID] = multiplier
}

// RateMultiplier returns the escalation, default 1.
func (c *Controller) RateMultiplier(tenantID string) float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if v, ok := c.rateEscalation[tenantID]; ok && v > 0 {
		return v
	}
	return 1.0
}

// CheckProvider reports whether a provider may serve a request.
func (c *Controller) CheckProvider(providerID, providerName string) error {
	if c == nil {
		return nil
	}
	if ok, reason := c.Killed(providerID); ok {
		return domain.Errorf(domain.ErrCodeUnavailable, "provider %q is killed: %s", providerID, reason)
	}
	if ok, reason := c.Killed(providerName); ok {
		return domain.Errorf(domain.ErrCodeUnavailable, "provider %q is killed: %s", providerName, reason)
	}
	if open, pinned := c.CircuitForced(providerID); pinned && open {
		return domain.Errorf(domain.ErrCodeUnavailable, "provider %q circuit is forced open", providerID)
	}
	return nil
}

// CheckTenant reports whether a tenant is under emergency override that denies.
func (c *Controller) CheckTenant(tenantID string) error {
	if c == nil {
		return nil
	}
	o, ok := c.TenantOverride(tenantID)
	if !ok {
		return nil
	}
	if o.Kind == KindTenantOverride && !o.Enabled {
		return domain.Errorf(domain.ErrCodePermission, "tenant %q is suspended: %s", tenantID, o.Reason)
	}
	return nil
}

// EffectiveCap combines policy ceiling with hard caps (tightest wins).
func (c *Controller) EffectiveCap(tenantID string, policyCeiling float64) float64 {
	if c == nil {
		return policyCeiling
	}
	cap, ok := c.HardCap(tenantID)
	if !ok || cap <= 0 {
		return policyCeiling
	}
	if policyCeiling <= 0 || cap < policyCeiling {
		return cap
	}
	return policyCeiling
}

// DenyReason formats a visible admin deny reason.
func DenyReason(kind, target, reason string) string {
	return fmt.Sprintf("%s:%s: %s", kind, target, reason)
}
