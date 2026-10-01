package guardrails

import "testing"

func TestKillAndRevive(t *testing.T) {
	c := New()
	c.Kill("openai", "incident")
	if ok, _ := c.Killed("openai"); !ok {
		t.Fatalf("expected killed")
	}
	if err := c.CheckProvider("openai", "openai"); err == nil {
		t.Fatalf("expected error")
	}
	c.Revive("openai")
	if ok, _ := c.Killed("openai"); ok {
		t.Fatalf("expected revived")
	}
}

func TestHardCap(t *testing.T) {
	c := New()
	c.SetHardCap("t1", 0.01)
	if got, _ := c.HardCap("t1"); got != 0.01 {
		t.Fatalf("unexpected cap %v", got)
	}
	if eff := c.EffectiveCap("t1", 1.0); eff != 0.01 {
		t.Fatalf("tightest should win, got %v", eff)
	}
	if eff := c.EffectiveCap("t1", 0); eff != 0.01 {
		t.Fatalf("cap should apply when policy unbounded")
	}
}

func TestFallbackBlock(t *testing.T) {
	c := New()
	c.BlockFallback("p1", "compliance")
	if ok, _ := c.FallbackBlocked("p1"); !ok {
		t.Fatalf("expected blocked")
	}
	c.UnblockFallback("p1")
	if ok, _ := c.FallbackBlocked("p1"); ok {
		t.Fatalf("expected unblocked")
	}
}
