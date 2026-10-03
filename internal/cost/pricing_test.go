package cost

import (
	"testing"
	"time"

	"github.com/shadowsafin/synapass/internal/domain"
)

func TestResolvePrecedence(t *testing.T) {
	at := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	versions := []domain.PricingVersion{
		{ID: "g", Scope: domain.PricingScopeGlobal, InputCostPerMillion: 1, EffectiveFrom: from},
		{ID: "p", Scope: domain.PricingScopeProvider, InputCostPerMillion: 2, EffectiveFrom: from},
		{ID: "m", Scope: domain.PricingScopeModel, InputCostPerMillion: 3, EffectiveFrom: from},
		{ID: "t", Scope: domain.PricingScopeTenant, InputCostPerMillion: 4, EffectiveFrom: from},
	}
	if got := Resolve(versions, at); got == nil || got.ID != "t" {
		t.Fatalf("resolved %+v, want tenant sheet", got)
	}
	// Without a tenant sheet the model sheet wins, and so on down.
	if got := Resolve(versions[:3], at); got == nil || got.ID != "m" {
		t.Fatalf("resolved %+v, want model sheet", got)
	}
	if got := Resolve(versions[:1], at); got == nil || got.ID != "g" {
		t.Fatalf("resolved %+v, want global sheet", got)
	}
}

func TestResolveEffectivenessWindow(t *testing.T) {
	oldTo := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	versions := []domain.PricingVersion{
		{ID: "old", Scope: domain.PricingScopeGlobal, InputCostPerMillion: 1,
			EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), EffectiveTo: &oldTo},
		{ID: "new", Scope: domain.PricingScopeGlobal, InputCostPerMillion: 2,
			EffectiveFrom: oldTo},
	}
	jan := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	if got := Resolve(versions, jan); got == nil || got.ID != "old" {
		t.Fatalf("january resolved %+v, want old sheet", got)
	}
	mar := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if got := Resolve(versions, mar); got == nil || got.ID != "new" {
		t.Fatalf("march resolved %+v, want new sheet", got)
	}
}

func TestResolveEmptyAndInvalid(t *testing.T) {
	if Resolve(nil, time.Now()) != nil {
		t.Fatal("empty candidates must resolve to nil (registry fallback)")
	}
	versions := []domain.PricingVersion{
		{ID: "bogus", Scope: "vendor", EffectiveFrom: time.Now().Add(-time.Hour)},
	}
	if Resolve(versions, time.Now()) != nil {
		t.Fatal("unknown scope must not resolve")
	}
}

func TestPriceForVersionCarriesProvenance(t *testing.T) {
	v := &domain.PricingVersion{ID: "v1", Scope: domain.PricingScopeModel,
		InputCostPerMillion: 0.15, Currency: ""}
	p := PriceForVersion(v)
	if p.PricingVersionID != "v1" || p.Source != "model" || p.Currency != "USD" {
		t.Fatalf("provenance lost: %+v", p)
	}
}

func TestPriceCacheRoundTripAndExpiry(t *testing.T) {
	c := NewPriceCache(time.Minute)
	now := time.Now()
	versions := []domain.PricingVersion{{ID: "v1", Scope: domain.PricingScopeGlobal}}
	key := PriceKey("t", "m", "p", now)
	if c.Get(key, now) != nil {
		t.Fatal("cold cache must miss")
	}
	c.Set(key, versions, now)
	if got := c.Get(key, now); len(got) != 1 {
		t.Fatal("warm cache must hit")
	}
	if c.Get(key, now.Add(2*time.Minute)) != nil {
		t.Fatal("expired entry must miss")
	}
}
