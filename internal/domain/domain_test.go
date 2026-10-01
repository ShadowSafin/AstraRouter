package domain

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestMatchModelPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		value   string
		want    bool
	}{
		{"empty pattern is a wildcard", "", "gpt-4o", true},
		{"star is a wildcard", "*", "anything", true},
		{"exact match", "gpt-4o", "gpt-4o", true},
		{"exact mismatch", "gpt-4o", "gpt-4o-mini", false},
		{"trailing glob", "gpt-4*", "gpt-4o-mini", true},
		{"trailing glob no match", "gpt-4*", "claude-3", false},
		{"leading glob", "*-8b-instruct", "meta-llama/llama-3-8b-instruct", true},
		// Model identifiers legitimately contain slashes, so a glob must be able to
		// span one. This is the case path.Match would get wrong.
		{"glob spanning a slash", "meta-llama/*", "meta-llama/llama-3-8b", true},
		{"single character wildcard", "gpt-?o", "gpt-4o", true},
		{"single character wildcard mismatch", "gpt-?o", "gpt-44o", false},
		{"question mark at end", "model-?", "model-a", true},
		{"pattern longer than value", "abcdef", "abc", false},
		{"multiple globs", "*-3-*", "meta-llama/llama-3-8b", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchModelPattern(tc.pattern, tc.value); got != tc.want {
				t.Fatalf("MatchModelPattern(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
			}
		})
	}
}

func TestMatchAnyModelPatternEmptyMatchesEverything(t *testing.T) {
	if !MatchAnyModelPattern(nil, "anything") {
		t.Fatal("an empty pattern list must be a wildcard")
	}
	if MatchAnyModelPattern([]string{"gpt-4*"}, "claude-3") {
		t.Fatal("a non-matching pattern list must not match")
	}
}

func TestErrorStatusMapping(t *testing.T) {
	tests := []struct {
		code   ErrorCode
		status int
	}{
		{ErrCodeInvalidRequest, http.StatusBadRequest},
		{ErrCodeAuthentication, http.StatusUnauthorized},
		{ErrCodePermission, http.StatusForbidden},
		{ErrCodeNotFound, http.StatusNotFound},
		{ErrCodeRateLimited, http.StatusTooManyRequests},
		{ErrCodeQuotaExceeded, http.StatusPaymentRequired},
		{ErrCodeTimeout, http.StatusGatewayTimeout},
		{ErrCodeUpstream, http.StatusBadGateway},
		{ErrCodeUnavailable, http.StatusBadGateway},
		{ErrCodeNotImplemented, http.StatusNotImplemented},
		{ErrCodeInternal, http.StatusInternalServerError},
	}

	for _, tc := range tests {
		t.Run(string(tc.code), func(t *testing.T) {
			err := NewError(tc.code, "boom")
			if got := err.HTTPStatus(); got != tc.status {
				t.Fatalf("HTTPStatus() = %d, want %d", got, tc.status)
			}
		})
	}
}

func TestErrorRetryAndFallbackDefaults(t *testing.T) {
	// Rate limits and timeouts are both retryable and fallback-eligible.
	rateLimited := NewError(ErrCodeRateLimited, "slow down")
	if !rateLimited.Retryable || !rateLimited.FallbackEligible {
		t.Fatal("a rate limit must be retryable and fallback-eligible")
	}

	// A malformed request is neither: every provider would fail identically, so
	// retrying or failing over would only add latency to a guaranteed failure.
	invalid := NewError(ErrCodeInvalidRequest, "bad field")
	if invalid.Retryable || invalid.FallbackEligible {
		t.Fatal("an invalid request must be neither retryable nor fallback-eligible")
	}

	// Context overflow is not retryable but is fallback-eligible: a model with a
	// larger window can serve it.
	tooLong := NewError(ErrCodeContextLength, "too long")
	if tooLong.Retryable {
		t.Fatal("a context length error must not be retried against the same model")
	}
	if !tooLong.FallbackEligible {
		t.Fatal("a context length error must be fallback-eligible")
	}
}

func TestAsErrorSynthesizesForForeignErrors(t *testing.T) {
	foreign := errors.New("something went wrong")
	normalized := AsError(foreign)
	if normalized == nil {
		t.Fatal("AsError must never return nil for a non-nil error")
	}
	if normalized.Code != ErrCodeInternal {
		t.Fatalf("Code = %q, want %q", normalized.Code, ErrCodeInternal)
	}
	if !errors.Is(normalized, foreign) {
		t.Fatal("the original error must remain reachable through errors.Is")
	}
}

func TestRetryPolicyBackoff(t *testing.T) {
	policy := RetryPolicy{
		MaxAttempts:    4,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     500 * time.Millisecond,
		Multiplier:     2,
	}

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 100 * time.Millisecond},
		{2, 200 * time.Millisecond},
		{3, 400 * time.Millisecond},
		// Clamped by MaxBackoff.
		{4, 500 * time.Millisecond},
		{9, 500 * time.Millisecond},
	}

	for _, tc := range tests {
		if got := policy.BackoffFor(tc.attempt); got != tc.want {
			t.Fatalf("BackoffFor(%d) = %s, want %s", tc.attempt, got, tc.want)
		}
	}
}

func TestRetryPolicyAllows(t *testing.T) {
	policy := DefaultRetryPolicy() // MaxAttempts 2
	rateLimited := NewError(ErrCodeRateLimited, "slow down")

	if !policy.Allows(1, rateLimited) {
		t.Fatal("the first retry must be allowed for a retryable error")
	}
	if policy.Allows(2, rateLimited) {
		t.Fatalf("attempt 2 of %d must not be allowed", policy.MaxAttempts)
	}
	if policy.Allows(1, NewError(ErrCodeInvalidRequest, "bad")) {
		t.Fatal("a non-retryable error must not be retried")
	}
}

func TestFallbackPolicyAllows(t *testing.T) {
	policy := DefaultFallbackPolicy()
	if !policy.Allows(NewError(ErrCodeUpstream, "5xx")) {
		t.Fatal("an upstream failure must trigger fallback")
	}
	if policy.Allows(NewError(ErrCodeInvalidRequest, "bad")) {
		t.Fatal("a client error must not trigger fallback")
	}

	disabled := DefaultFallbackPolicy()
	disabled.Enabled = false
	if disabled.Allows(NewError(ErrCodeUpstream, "5xx")) {
		t.Fatal("a disabled fallback policy must never allow failover")
	}

	// An explicit list overrides the per-error default, which is how an operator
	// forces failover for a provider whose classification is unreliable.
	explicit := FallbackPolicy{Enabled: true, MaxAttempts: 2, OnErrorCodes: []ErrorCode{ErrCodeInvalidRequest}}
	if !explicit.Allows(NewError(ErrCodeInvalidRequest, "bad")) {
		t.Fatal("an explicit error code list must override the default")
	}
	if explicit.Allows(NewError(ErrCodeUpstream, "5xx")) {
		t.Fatal("an explicit list must exclude codes it does not name")
	}
}

func TestRoutingPolicyNormalizeFillsDefaults(t *testing.T) {
	policy := &RoutingPolicy{
		Name: "test",
		Targets: []RouteTarget{
			{Model: "a"},
			{Model: "b"},
		},
	}
	policy.Normalize()

	if policy.Strategy != StrategyPriority {
		t.Fatalf("Strategy = %q, want %q", policy.Strategy, StrategyPriority)
	}
	if policy.Version != 1 {
		t.Fatalf("Version = %d, want 1", policy.Version)
	}
	if policy.Retry.MaxAttempts < 1 {
		t.Fatal("an unset retry policy must receive a default attempt count")
	}
	if policy.Timeout.Total <= 0 || policy.Timeout.PerAttempt <= 0 {
		t.Fatal("an unset timeout policy must receive defaults")
	}
	if policy.Timeout.PerAttempt > policy.Timeout.Total {
		t.Fatal("the default per-attempt timeout must not exceed the total")
	}
	if policy.Limits.MaxOutputTokens <= 0 {
		t.Fatal("an unset output limit must receive a default")
	}
	// List order becomes priority, so a hand-written ordered list needs no priority
	// fields.
	if policy.Targets[0].Priority != 1 || policy.Targets[1].Priority != 2 {
		t.Fatalf("target priorities = %d,%d, want 1,2",
			policy.Targets[0].Priority, policy.Targets[1].Priority)
	}
}

func TestPolicyMatchSpecificity(t *testing.T) {
	wildcard := PolicyMatch{}
	modelOnly := PolicyMatch{Models: []string{"gpt-4o"}}
	keyScoped := PolicyMatch{Models: []string{"gpt-4o"}, APIKeyIDs: []string{"key-1"}}

	if wildcard.Specificity() != 0 {
		t.Fatalf("a wildcard match must have zero specificity, got %d", wildcard.Specificity())
	}
	if !(keyScoped.Specificity() > modelOnly.Specificity()) {
		t.Fatal("a key-scoped match must be more specific than a model-only match")
	}
	if !(modelOnly.Specificity() > wildcard.Specificity()) {
		t.Fatal("a model match must be more specific than a wildcard")
	}
}

func TestFallbackChainTruncate(t *testing.T) {
	chain := FallbackChain{
		Targets: []RouteTarget{
			{Model: "a"}, {Model: "b"}, {Model: "c"},
		},
		MaxAttempts: 3,
	}

	truncated := chain.Truncate(2)
	if truncated.Length() != 2 {
		t.Fatalf("Length() = %d, want 2", truncated.Length())
	}
	if truncated.Primary().Model != "a" {
		t.Fatalf("Primary() = %q, want a", truncated.Primary().Model)
	}
	// The attempt cap must follow the truncation, or the executor would try to
	// reach a target that is no longer in the chain.
	if truncated.Limit() != 2 {
		t.Fatalf("Limit() = %d, want 2", truncated.Limit())
	}

	// The original must be untouched: the chain is passed by value but the slice
	// header is shared, so an in-place truncation would corrupt the original.
	if chain.Length() != 3 {
		t.Fatalf("the original chain was mutated: Length() = %d, want 3", chain.Length())
	}

	// Truncating to zero leaves an empty chain rather than panicking.
	if empty := chain.Truncate(0); !empty.Empty() || empty.Limit() != 0 {
		t.Fatalf("Truncate(0) produced length %d and limit %d, want 0 and 0",
			empty.Length(), empty.Limit())
	}
}

func TestTimeUsageAddAndNormalize(t *testing.T) {
	usage := TokenUsage{PromptTokens: 10, CompletionTokens: 5}
	normalized := usage.Normalize()
	if normalized.TotalTokens != 15 {
		t.Fatalf("TotalTokens = %d, want 15", normalized.TotalTokens)
	}

	// An explicitly reported total is preserved: some providers include reasoning
	// tokens that are not derivable from the two halves.
	explicit := TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 20}
	if got := explicit.Normalize().TotalTokens; got != 20 {
		t.Fatalf("an explicit total must be preserved, got %d", got)
	}

	combined := normalized.Add(TokenUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3, Estimated: true})
	if combined.PromptTokens != 11 || combined.CompletionTokens != 7 || combined.TotalTokens != 18 {
		t.Fatalf("Add produced %+v", combined)
	}
	if !combined.Estimated {
		t.Fatal("adding an estimated usage must mark the result estimated")
	}
}

func TestChatCompletionRequestValidate(t *testing.T) {
	valid := ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []ChatMessage{{Role: RoleUser, Content: NewTextContent("hello")}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a valid request was rejected: %v", err)
	}

	tests := []struct {
		name string
		req  ChatCompletionRequest
		code ErrorCode
	}{
		{
			name: "missing model",
			req:  ChatCompletionRequest{Messages: []ChatMessage{{Role: RoleUser, Content: NewTextContent("hi")}}},
			code: ErrCodeInvalidRequest,
		},
		{
			name: "no messages",
			req:  ChatCompletionRequest{Model: "gpt-4o"},
			code: ErrCodeInvalidRequest,
		},
		{
			name: "unknown role",
			req: ChatCompletionRequest{Model: "gpt-4o", Messages: []ChatMessage{
				{Role: "wizard", Content: NewTextContent("hi")},
			}},
			code: ErrCodeInvalidRequest,
		},
		{
			name: "empty content",
			req: ChatCompletionRequest{Model: "gpt-4o", Messages: []ChatMessage{
				{Role: RoleUser, Content: NewTextContent("")},
			}},
			code: ErrCodeInvalidRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate()
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if got := AsError(err).Code; got != tc.code {
				t.Fatalf("Code = %q, want %q", got, tc.code)
			}
		})
	}
}

func TestRequiredCapabilitiesDerivation(t *testing.T) {
	streaming := true
	parallel := true
	req := ChatCompletionRequest{
		Model:            "gpt-4o",
		Stream:           &streaming,
		Tools:            []Tool{{Type: "function", Function: FunctionDefinition{Name: "lookup"}}},
		ParallelToolCall: &parallel,
		Messages: []ChatMessage{
			{Role: RoleUser, Content: NewPartsContent(
				ContentPart{Type: PartText, Text: "what is this?"},
				ContentPart{Type: PartImageURL, ImageURL: &ImageURL{URL: "https://example.test/x.png"}},
			)},
		},
	}

	caps := NewCapabilitySet(req.RequiredCapabilities()...)
	for _, want := range []Capability{
		CapChat, CapStreaming, CapTools, CapParallelTool, CapVision,
	} {
		if !caps.Contains(want) {
			t.Fatalf("derived capabilities are missing %q: %v", want, caps.Slice())
		}
	}
}

func TestMessageContentRoundTrip(t *testing.T) {
	// String content must stay a string.
	var text MessageContent
	if err := json.Unmarshal([]byte(`"hello"`), &text); err != nil {
		t.Fatalf("unmarshal string content: %v", err)
	}
	if text.IsParts {
		t.Fatal("string content must not be marked as parts")
	}
	if text.PlainText() != "hello" {
		t.Fatalf("PlainText() = %q, want hello", text.PlainText())
	}
	encoded, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != `"hello"` {
		t.Fatalf("round-tripped string content = %s, want \"hello\"", encoded)
	}

	// Array content must stay an array, and unknown shapes must be preserved
	// verbatim rather than rejected.
	var parts MessageContent
	raw := `[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"https://x.test/a.png"}},{"type":"future_thing","payload":{"k":1}}]`
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		t.Fatalf("unmarshal parts content: %v", err)
	}
	if !parts.IsParts {
		t.Fatal("array content must be marked as parts")
	}
	if len(parts.Parts) != 3 {
		t.Fatalf("got %d parts, want 3", len(parts.Parts))
	}
	if !parts.HasImages() {
		t.Fatal("HasImages() must report true for content containing an image part")
	}
	if parts.PlainText() != "hi" {
		t.Fatalf("PlainText() = %q, want hi", parts.PlainText())
	}
	if len(parts.Parts[2].Raw) == 0 {
		t.Fatal("an unrecognized part must be preserved as raw JSON")
	}
}

func TestEvaluateStateThresholds(t *testing.T) {
	if state := EvaluateState(0, 0, 1); state != HealthHealthy {
		t.Fatalf("a provider with no failures must be healthy, got %q", state)
	}
	if state := EvaluateState(DegradeThreshold, 0, 1); state != HealthDegraded {
		t.Fatalf("a provider at the degrade threshold must be degraded, got %q", state)
	}
	if state := EvaluateState(DegradeThreshold*3, 0, 0); state != HealthUnhealthy {
		t.Fatalf("a provider at three times the degrade threshold must be unhealthy, got %q", state)
	}
}
