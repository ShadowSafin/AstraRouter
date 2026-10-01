package providers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/corerouter/corerouter/internal/domain"
)

// baseAdapter holds the behaviour every adapter shares: credentials, request
// construction, capability reporting and the default health probe.
//
// Concrete adapters embed it and override only the wire-format specifics. This
// is the one place credentials are read and the one place the HTTP client is
// configured, so a provider cannot accidentally bypass either.
type baseAdapter struct {
	provider   domain.Provider
	credential string
	client     HTTPDoer
	opts       Options
	baseURL    string
	logger     *slog.Logger
}

// newBaseAdapter validates the provider configuration and resolves credentials.
func newBaseAdapter(p domain.Provider, opts Options) (*baseAdapter, error) {
	base, err := resolveBaseURL(p.BaseURL)
	if err != nil {
		return nil, err
	}
	opts = opts.withDefaults()

	client := opts.Client
	if client == nil {
		client = NewHTTPClient(opts.Timeouts)
	}
	logger := opts.Logger
	if logger == nil {
		// A discarded logger keeps call sites free of nil checks. Adapters log
		// at debug level only, so the cost of a real logger is negligible.
		logger = slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}

	return &baseAdapter{
		provider:   p,
		credential: resolveCredential(p),
		client:     client,
		opts:       opts,
		baseURL:    base,
		logger:     logger,
	}, nil
}

// resolveCredential returns the provider's API key.
//
// Precedence is inline first, then the environment variable named by APIKeyEnv.
// Inline wins because the bootstrap layer only populates it when the operator
// wrote the literal secret into configuration, which is unambiguous intent.
func resolveCredential(p domain.Provider) string {
	if p.APIKeyInline != "" {
		return p.APIKeyInline
	}
	if p.APIKeyEnv != "" {
		return strings.TrimSpace(os.Getenv(p.APIKeyEnv))
	}
	return ""
}

// discardWriter swallows log output.
type discardWriter struct{}

// Write implements io.Writer.
func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// Name returns the provider name.
func (b *baseAdapter) Name() string { return b.provider.Name }

// Kind returns the provider kind.
func (b *baseAdapter) Kind() domain.ProviderKind { return b.provider.Kind }

// Provider returns the provider configuration.
func (b *baseAdapter) Provider() domain.Provider { return b.provider }

// Enabled reports whether the provider is configured to serve traffic. A
// provider is enabled unless it is explicitly disabled or revoked.
func (b *baseAdapter) Enabled() bool {
	switch b.provider.Status {
	case domain.StatusActive, domain.StatusDegraded, "":
		return true
	default:
		return false
	}
}

// Capabilities returns the provider capability set, falling back to the
// natural set for the kind when the operator did not enumerate one.
func (b *baseAdapter) Capabilities() domain.CapabilitySet {
	if len(b.provider.Capabilities) > 0 {
		return domain.NewCapabilitySet(b.provider.Capabilities...)
	}
	return DefaultCapabilities(b.provider.Kind)
}

// Timeout returns the effective timeout policy for this provider.
func (b *baseAdapter) Timeout() domain.TimeoutPolicy {
	t := b.opts.Timeouts.Normalize()
	if b.provider.TimeoutMS > 0 {
		t.PerAttempt = time.Duration(b.provider.TimeoutMS) * time.Millisecond
		if t.PerAttempt > t.Total {
			t.Total = t.PerAttempt
		}
	}
	return t
}

// DefaultCapabilities returns the capability set a provider kind has unless the
// operator narrows it.
//
// These defaults are intentionally optimistic about local runtimes: a vLLM or
// Ollama deployment may serve a model with no tool support, but that is a
// per-model fact the registry captures, whereas the runtime itself generally
// supports streaming and JSON mode. The registry's per-model capability list is
// the narrower filter, and routing intersects the two.
func DefaultCapabilities(kind domain.ProviderKind) domain.CapabilitySet {
	switch kind {
	case domain.ProviderOpenAI:
		return domain.NewCapabilitySet(
			domain.CapChat, domain.CapStreaming, domain.CapTools, domain.CapParallelTool,
			domain.CapVision, domain.CapJSONMode, domain.CapJSONSchema, domain.CapEmbeddings,
			domain.CapLongContext, domain.CapReasoning, domain.CapSeed,
		)
	case domain.ProviderAnthropic:
		return domain.NewCapabilitySet(
			domain.CapChat, domain.CapStreaming, domain.CapTools, domain.CapParallelTool,
			domain.CapVision, domain.CapJSONMode, domain.CapLongContext, domain.CapReasoning,
		)
	case domain.ProviderVLLM:
		return domain.NewCapabilitySet(
			domain.CapChat, domain.CapStreaming, domain.CapJSONMode, domain.CapLongContext, domain.CapSeed,
		)
	case domain.ProviderOllama:
		return domain.NewCapabilitySet(
			domain.CapChat, domain.CapStreaming, domain.CapLongContext, domain.CapSeed,
		)
	case domain.ProviderOpenAICompatible:
		return domain.NewCapabilitySet(
			domain.CapChat, domain.CapStreaming, domain.CapJSONMode, domain.CapLongContext,
		)
	default:
		return domain.NewCapabilitySet(domain.CapChat, domain.CapStreaming)
	}
}

// newRequest builds an authenticated request against the provider.
func (b *baseAdapter) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	full := buildURL(b.baseURL, path)

	var reader *strings.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	} else {
		reader = strings.NewReader("")
	}

	req, err := http.NewRequestWithContext(ctx, method, full, reader)
	if err != nil {
		return nil, domain.NewError(domain.ErrCodeInternal, "failed to build provider request").Wrap(err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", b.opts.UserAgent)

	// Static provider headers are applied before credentials so an operator
	// cannot accidentally override the auth header through a custom header.
	for k, v := range b.provider.Headers {
		req.Header.Set(k, v)
	}

	applyAuth(req, b.provider, b.credential)

	// OpenAI scopes a key to an organization or project through headers.
	if b.provider.Organization != "" {
		req.Header.Set("OpenAI-Organization", b.provider.Organization)
	}
	if b.provider.Project != "" {
		req.Header.Set("OpenAI-Project", b.provider.Project)
	}

	return req, nil
}

// postJSON sends a JSON body and returns the buffered response for non-streaming
// calls, normalizing every failure.
func (b *baseAdapter) postJSON(ctx context.Context, path string, model string, attempt int, payload any) (*http.Response, []byte, error) {
	body, err := encodeJSON(payload)
	if err != nil {
		return nil, nil, err
	}

	req, err := b.newRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, nil, err
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, nil, NormalizeTransportError(b.provider.Name, model, attempt, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := ReadBody(resp, b.opts.MaxErrorBodyBytes)
		return resp, nil, NormalizeHTTPError(b.provider.Name, model, attempt, resp.StatusCode, errBody, resp.Header)
	}

	respBody, err := readLimited(resp.Body, b.opts.MaxResponseBytes)
	closeBody(resp)
	if err != nil {
		return resp, nil, NormalizeDecodeError(b.provider.Name, model, attempt, err, nil)
	}
	return resp, respBody, nil
}

// postStream sends a JSON body and returns the live response for streaming.
//
// The caller owns the body and must close it. Unlike postJSON this does not
// buffer, because buffering would defeat streaming.
func (b *baseAdapter) postStream(ctx context.Context, path string, model string, attempt int, payload any) (*http.Response, error) {
	body, err := encodeJSON(payload)
	if err != nil {
		return nil, err
	}

	req, err := b.newRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, NormalizeTransportError(b.provider.Name, model, attempt, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := ReadBody(resp, b.opts.MaxErrorBodyBytes)
		return nil, NormalizeHTTPError(b.provider.Name, model, attempt, resp.StatusCode, errBody, resp.Header)
	}
	return resp, nil
}

// getJSON performs an authenticated GET and decodes the body.
func (b *baseAdapter) getJSON(ctx context.Context, path string, v any) (int, error) {
	req, err := b.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 0, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return 0, NormalizeTransportError(b.provider.Name, "", 1, err)
	}
	defer closeBody(resp)

	body, err := readLimited(resp.Body, b.opts.MaxResponseBytes)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, NormalizeHTTPError(b.provider.Name, "", 1, resp.StatusCode, body, resp.Header)
	}
	if v != nil {
		if err := decodeJSON(body, v); err != nil {
			return resp.StatusCode, NormalizeDecodeError(b.provider.Name, "", 1, err, body)
		}
	}
	return resp.StatusCode, nil
}

// probeResult is the outcome of a health probe.
type probeResult struct {
	State   domain.HealthState
	Latency time.Duration
	Message string
	// AuthFailed distinguishes a credential problem from an availability
	// problem, because they page different people.
	AuthFailed bool
}

// probe performs the default GET-based health probe.
func (b *baseAdapter) probe(ctx context.Context, path string) probeResult {
	ctx, cancel := contextWithAttempt(ctx, b.opts.Timeouts.Connect)
	defer cancel()

	start := time.Now()
	status, err := b.getJSON(ctx, path, nil)
	latency := time.Since(start)

	if err != nil {
		code := domain.AsError(err).Code
		authFailed := code == domain.ErrCodeAuthentication
		state := domain.HealthUnhealthy
		if code == domain.ErrCodeNotFound {
			// The endpoint is missing but the host answered: the provider is
			// reachable, which is what a health probe is really asking.
			state = domain.HealthHealthy
		}
		return probeResult{State: state, Latency: latency, Message: err.Error(), AuthFailed: authFailed}
	}
	_ = status
	return probeResult{State: domain.HealthHealthy, Latency: latency}
}

// HealthCheck runs the provider's default probe and renders a ProviderHealth
// record.
func (b *baseAdapter) HealthCheck(ctx context.Context) domain.ProviderHealth {
	start := time.Now()
	res := b.probe(ctx, b.healthPath())

	health := domain.ProviderHealth{
		ProviderID:     b.provider.ID,
		ProviderName:   b.provider.Name,
		State:          res.State,
		CheckedAt:      domain.Now(),
		LatencyMS:      res.Latency.Milliseconds(),
		ProbeLatencyMS: res.Latency.Milliseconds(),
		Message:        res.Message,
		Source:         "active_probe",
	}
	switch res.State {
	case domain.HealthHealthy:
		health.SuccessRate = 1
	default:
		health.SuccessRate = 0
		health.ErrorRate = 1
		health.ConsecutiveFailures = 1
	}
	if health.LatencyMS == 0 {
		health.LatencyMS = time.Since(start).Milliseconds()
	}
	return health
}

// healthPath returns the default probe path for the provider kind. Adapters
// override this when the provider exposes a dedicated health endpoint.
func (b *baseAdapter) healthPath() string { return "/models" }

// closeBody drains and closes a response body so the underlying connection can be
// reused. Failing to drain is the most common cause of connection churn in a Go
// HTTP client.
func closeBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}

// rawJSON is a helper for retaining a response body as raw JSON.
func rawJSON(body []byte) json.RawMessage {
	if len(body) == 0 {
		return nil
	}
	out := make(json.RawMessage, len(body))
	copy(out, body)
	return out
}
