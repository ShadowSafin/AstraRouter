package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/corerouter/corerouter/internal/domain"
)

// ---------------------------------------------------------------------------
// Test transport
//
// Adapters talk to upstreams over an injectable HTTPDoer, so these tests exercise
// the real translation code with no network and no mocking framework. The
// transport records the request that was made, which is what lets a test assert on
// the wire format rather than on internal state.
// ---------------------------------------------------------------------------

type stubTransport struct {
	mu       sync.Mutex
	respond  func(req *http.Request) *http.Response
	requests []*http.Request
	bodies   [][]byte
	err      error
}

func newStubTransport(respond func(*http.Request) *http.Response) *stubTransport {
	return &stubTransport{respond: respond}
}

func (s *stubTransport) Do(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
		// The adapter may not re-read the body, but restoring it keeps the request
		// usable for any follow-up assertion.
		req.Body = io.NopCloser(strings.NewReader(string(body)))
	}
	s.requests = append(s.requests, req)
	s.bodies = append(s.bodies, body)
	respond := s.respond
	err := s.err
	s.mu.Unlock()

	if err != nil {
		return nil, err
	}
	return respond(req), nil
}

func (s *stubTransport) lastRequest(t *testing.T) *http.Request {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("no request was made")
	}
	return s.requests[len(s.requests)-1]
}

func (s *stubTransport) lastBody(t *testing.T) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bodies) == 0 {
		t.Fatal("no request body was captured")
	}
	var decoded map[string]any
	if err := json.Unmarshal(s.bodies[len(s.bodies)-1], &decoded); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return decoded
}

func (s *stubTransport) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// jsonResponse builds a buffered response.
func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// sseResponse builds a streaming response from raw SSE text.
func sseResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "OK",
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// providerFor builds a minimal provider configuration.
func providerFor(name string, kind domain.ProviderKind, baseURL string) domain.Provider {
	return domain.Provider{
		ID:        "p-" + name,
		Name:      name,
		Kind:      kind,
		BaseURL:   baseURL,
		Status:    domain.StatusActive,
		AuthStyle: domain.AuthBearer,
	}
}

// anthropicProvider builds an Anthropic provider exactly as the configuration
// layer would when the operator did not name an auth style: the style is left
// empty so the kind decides it. Persisting a concrete "bearer" here is the bug
// this shape is chosen to avoid.
func anthropicProvider() domain.Provider {
	p := providerFor("anthropic", domain.ProviderAnthropic, "https://api.anthropic.com/v1")
	p.AuthStyle = ""
	return p
}

// adapterFor constructs an adapter over the supplied transport.
func adapterFor(t *testing.T, p domain.Provider, transport *stubTransport) Adapter {
	t.Helper()
	p.APIKeyInline = "sk-test-credential"
	adapter, err := NewAdapter(p, Options{Client: transport})
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	return adapter
}

// chatRequest builds a normalized request.
func chatRequest() *Request {
	temperature := 0.2
	return &Request{
		Model: "gpt-4o",
		Params: &domain.ChatCompletionRequest{
			Model:       "gpt-4o",
			Temperature: &temperature,
			Messages: []domain.ChatMessage{
				{Role: domain.RoleSystem, Content: domain.NewTextContent("be terse")},
				{Role: domain.RoleUser, Content: domain.NewTextContent("hello")},
			},
		},
		Ref:          domain.ModelRef{ProviderName: "openai", Model: "gpt-4o"},
		MaxTokens:    128,
		PromptTokens: 12,
	}
}

const openAISuccessBody = `{
  "id": "chatcmpl-1",
  "object": "chat.completion",
  "created": 1750000000,
  "model": "gpt-4o-2024-11-20",
  "system_fingerprint": "fp_abc",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "hi there"},
    "finish_reason": "stop"
  }],
  "usage": {"prompt_tokens": 12, "completion_tokens": 4, "total_tokens": 16,
            "prompt_tokens_details": {"cached_tokens": 5}}
}`

// ---------------------------------------------------------------------------
// OpenAI wire format
// ---------------------------------------------------------------------------

func TestOpenAIAdapterSendsTheExpectedWireRequest(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, openAISuccessBody)
	})
	adapter := adapterFor(t, providerFor("openai", domain.ProviderOpenAI, "https://api.openai.com/v1"), transport)

	if _, err := adapter.ChatCompletion(context.Background(), chatRequest()); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	req := transport.lastRequest(t)
	if got, want := req.URL.String(), "https://api.openai.com/v1/chat/completions"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
	if req.Method != http.MethodPost {
		t.Errorf("method = %q", req.Method)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-test-credential" {
		t.Errorf("authorization = %q", got)
	}
	if got := req.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("content type = %q", got)
	}
	if got := req.Header.Get("User-Agent"); !strings.HasPrefix(got, "CoreRouter/") {
		t.Errorf("user agent = %q", got)
	}

	body := transport.lastBody(t)
	if body["model"] != "gpt-4o" {
		t.Errorf("model = %v", body["model"])
	}
	if body["max_tokens"] != float64(128) {
		t.Errorf("max_tokens = %v, want 128", body["max_tokens"])
	}
	if body["temperature"] != 0.2 {
		t.Errorf("temperature = %v", body["temperature"])
	}
	if _, present := body["stream"]; present {
		t.Error("a non-streaming request must not set stream")
	}
	if _, present := body["stream_options"]; present {
		t.Error("a non-streaming request must not set stream_options")
	}

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %v", body["messages"])
	}
	first := messages[0].(map[string]any)
	if first["role"] != "system" {
		t.Errorf("the system message must be forwarded in place for OpenAI: %v", first)
	}
}

func TestOpenAIAdapterNormalizesTheResponse(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, openAISuccessBody)
	})
	adapter := adapterFor(t, providerFor("openai", domain.ProviderOpenAI, "https://api.openai.com/v1"), transport)

	response, err := adapter.ChatCompletion(context.Background(), chatRequest())
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if response.ID != "chatcmpl-1" {
		t.Errorf("id = %q", response.ID)
	}
	if response.Content() != "hi there" {
		t.Errorf("content = %q", response.Content())
	}
	if response.FinishReason() != domain.FinishStop {
		t.Errorf("finish reason = %q", response.FinishReason())
	}
	if response.SystemFingerprint != "fp_abc" {
		t.Errorf("fingerprint = %q", response.SystemFingerprint)
	}
	if response.Usage.PromptTokens != 12 || response.Usage.CompletionTokens != 4 {
		t.Errorf("usage = %+v", response.Usage)
	}
	if response.Usage.TotalTokens != 16 {
		t.Errorf("total tokens = %d, want 16", response.Usage.TotalTokens)
	}
	if response.Usage.CachedPromptTokens != 5 {
		t.Errorf("cached prompt tokens = %d, want 5", response.Usage.CachedPromptTokens)
	}
	if response.Usage.Estimated {
		t.Error("provider-reported usage must not be marked estimated")
	}
	if len(response.Raw) == 0 {
		t.Error("the raw body must be retained for debugging and replay")
	}
}

func TestOpenAICompatibleKindOmitsOpenAISpecificFields(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, openAISuccessBody)
	})
	adapter := adapterFor(t, providerFor("local", domain.ProviderOpenAICompatible, "http://localhost:8000/v1"), transport)

	req := chatRequest()
	seed := 7
	req.Params.Seed = &seed
	if _, err := adapter.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	body := transport.lastBody(t)
	// A generic OpenAI-compatible server may reject unknown fields outright, so
	// OpenAI-only extensions are withheld from it.
	if _, present := body["seed"]; present {
		t.Error("seed is an OpenAI/vLLM extension and must not be sent to a generic server")
	}
}

func TestOpenAIVLLMKindSendsSeed(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, openAISuccessBody)
	})
	adapter := adapterFor(t, providerFor("vllm", domain.ProviderVLLM, "http://localhost:8000/v1"), transport)

	req := chatRequest()
	seed := 7
	req.Params.Seed = &seed
	if _, err := adapter.ChatCompletion(context.Background(), req); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if got := transport.lastBody(t)["seed"]; got != float64(7) {
		t.Errorf("seed = %v, want 7", got)
	}
}

func TestOpenAIAdapterNormalizesHTTPErrors(t *testing.T) {
	cases := []struct {
		name             string
		status           int
		body             string
		wantCode         domain.ErrorCode
		wantRetryable    bool
		wantFallbackable bool
	}{
		{
			name: "rate limited", status: http.StatusTooManyRequests,
			body:             `{"error":{"message":"slow down","type":"rate_limit_error"}}`,
			wantCode:         domain.ErrCodeRateLimited,
			wantRetryable:    true,
			wantFallbackable: true,
		},
		{
			name: "unauthorized", status: http.StatusUnauthorized,
			body:             `{"error":{"message":"bad key","type":"invalid_request_error"}}`,
			wantCode:         domain.ErrCodeAuthentication,
			wantRetryable:    false,
			wantFallbackable: true,
		},
		{
			name: "server error", status: http.StatusInternalServerError,
			body:             `{"error":{"message":"boom"}}`,
			wantCode:         domain.ErrCodeUpstream,
			wantRetryable:    true,
			wantFallbackable: true,
		},
		{
			name: "context length", status: http.StatusBadRequest,
			body:             `{"error":{"message":"too long","code":"context_length_exceeded"}}`,
			wantCode:         domain.ErrCodeContextLength,
			wantRetryable:    false,
			wantFallbackable: true,
		},
		{
			name: "bad request", status: http.StatusBadRequest,
			body:             `{"error":{"message":"messages is required","type":"invalid_request_error"}}`,
			wantCode:         domain.ErrCodeInvalidRequest,
			wantRetryable:    false,
			wantFallbackable: false,
		},
		{
			// A 503 from the upstream is an upstream failure. ErrCodeUnavailable is
			// reserved for a provider the gateway itself has judged unhealthy, which
			// is a routing fact rather than something the provider told us.
			name: "unavailable", status: http.StatusServiceUnavailable,
			body:             `{"error":{"message":"overloaded"}}`,
			wantCode:         domain.ErrCodeUpstream,
			wantRetryable:    true,
			wantFallbackable: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transport := newStubTransport(func(*http.Request) *http.Response {
				return jsonResponse(tc.status, tc.body)
			})
			adapter := adapterFor(t, providerFor("openai", domain.ProviderOpenAI, "https://api.openai.com/v1"), transport)

			_, err := adapter.ChatCompletion(context.Background(), chatRequest())
			if err == nil {
				t.Fatal("expected an error")
			}

			normalized := domain.AsError(err)
			if normalized.Code != tc.wantCode {
				t.Errorf("code = %q, want %q (%v)", normalized.Code, tc.wantCode, err)
			}
			if normalized.Retryable != tc.wantRetryable {
				t.Errorf("retryable = %v, want %v", normalized.Retryable, tc.wantRetryable)
			}
			if normalized.FallbackEligible != tc.wantFallbackable {
				t.Errorf("fallback eligible = %v, want %v", normalized.FallbackEligible, tc.wantFallbackable)
			}
			if normalized.Provider != "openai" {
				t.Errorf("provider = %q, want the failing provider for attribution", normalized.Provider)
			}
			if normalized.Attempt != 1 {
				t.Errorf("attempt = %d, want 1", normalized.Attempt)
			}
		})
	}
}

func TestOpenAIAdapterRejectsSuccessCarryingAnErrorObject(t *testing.T) {
	// Some compatible servers return 200 with an error body. Treating that as a
	// success would hand the client an empty completion.
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, `{"error":{"message":"model is loading","type":"server_error"}}`)
	})
	adapter := adapterFor(t, providerFor("local", domain.ProviderOpenAICompatible, "http://localhost:8000/v1"), transport)

	_, err := adapter.ChatCompletion(context.Background(), chatRequest())
	if code := domain.AsError(err).Code; code != domain.ErrCodeUpstream {
		t.Fatalf("code = %q, want upstream_error", code)
	}
}

func TestOpenAIAdapterRejectsUndecodableBody(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, `<html>a proxy got in the way</html>`)
	})
	adapter := adapterFor(t, providerFor("openai", domain.ProviderOpenAI, "https://api.openai.com/v1"), transport)

	_, err := adapter.ChatCompletion(context.Background(), chatRequest())
	normalized := domain.AsError(err)
	if normalized.Code != domain.ErrCodeUpstream {
		t.Fatalf("code = %q, want upstream_error", normalized.Code)
	}
	if !strings.Contains(normalized.Message, "could not be decoded") {
		t.Errorf("message = %q", normalized.Message)
	}
}

func TestOpenAIAdapterNormalizesTransportFailure(t *testing.T) {
	transport := &stubTransport{err: context.DeadlineExceeded}
	adapter := adapterFor(t, providerFor("openai", domain.ProviderOpenAI, "https://api.openai.com/v1"), transport)

	_, err := adapter.ChatCompletion(context.Background(), chatRequest())
	normalized := domain.AsError(err)
	if normalized.Code != domain.ErrCodeTimeout {
		t.Fatalf("code = %q, want timeout", normalized.Code)
	}
	if !normalized.Retryable {
		t.Error("a timeout should be retryable")
	}
}

// ---------------------------------------------------------------------------
// OpenAI streaming
// ---------------------------------------------------------------------------

const openAIStreamBody = "data: {\"id\":\"chatcmpl-2\",\"model\":\"gpt-4o\",\"created\":1750000000,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello \"}}]}\n\n" +
	"data: {\"id\":\"chatcmpl-2\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"world\"}}]}\n\n" +
	": keepalive\n\n" +
	"data: {\"id\":\"chatcmpl-2\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"id\":\"chatcmpl-2\",\"model\":\"gpt-4o\",\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":2,\"total_tokens\":14}}\n\n" +
	"data: [DONE]\n\n"

func TestOpenAIAdapterStreamsAndAccumulates(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return sseResponse(openAIStreamBody)
	})
	adapter := adapterFor(t, providerFor("openai", domain.ProviderOpenAI, "https://api.openai.com/v1"), transport)

	var delivered []Chunk
	response, err := adapter.ChatCompletionStream(context.Background(), chatRequest(), func(chunk Chunk) error {
		delivered = append(delivered, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}

	if len(delivered) != 3 {
		t.Fatalf("delivered %d chunks, want 3 (the keepalive and the usage frame are not chunks)", len(delivered))
	}
	if got := delivered[0].Delta.Text(); got != "Hello " {
		t.Errorf("first delta = %q", got)
	}
	if got := delivered[1].Delta.Text(); got != "world" {
		t.Errorf("second delta = %q", got)
	}
	if delivered[2].FinishReason == nil || *delivered[2].FinishReason != domain.FinishStop {
		t.Errorf("terminal finish reason = %v", delivered[2].FinishReason)
	}

	// The assembled response must be usable as a complete record so the request
	// generates the same usage accounting as a buffered one.
	if response.Content() != "Hello world" {
		t.Errorf("assembled content = %q", response.Content())
	}
	if response.Usage.TotalTokens != 14 {
		t.Errorf("assembled usage = %+v", response.Usage)
	}
	if response.Usage.Estimated {
		t.Error("usage reported by the provider must not be estimated")
	}
	if response.ID != "chatcmpl-2" {
		t.Errorf("assembled id = %q", response.ID)
	}

	req := transport.lastRequest(t)
	if got := req.Header.Get("Accept"); got != "text/event-stream" {
		t.Errorf("accept = %q", got)
	}
	body := transport.lastBody(t)
	if body["stream"] != true {
		t.Errorf("stream = %v, want true", body["stream"])
	}
	// Streaming usage is requested so a streamed request is billable with exact
	// numbers rather than an estimate.
	if _, present := body["stream_options"]; !present {
		t.Error("streaming requests to OpenAI should request usage")
	}
}

func TestOpenAICompatibleStreamingOmitsStreamOptions(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return sseResponse(openAIStreamBody)
	})
	adapter := adapterFor(t, providerFor("local", domain.ProviderOpenAICompatible, "http://localhost:8000/v1"), transport)

	if _, err := adapter.ChatCompletionStream(context.Background(), chatRequest(), func(Chunk) error { return nil }); err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}

	if _, present := transport.lastBody(t)["stream_options"]; present {
		t.Error("stream_options is not universally supported and must not be sent to a generic server")
	}
}

func TestOpenAIAdapterSurfacesHandlerFailureAsCallerCancellation(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return sseResponse(openAIStreamBody)
	})
	adapter := adapterFor(t, providerFor("openai", domain.ProviderOpenAI, "https://api.openai.com/v1"), transport)

	sentinel := &domain.Error{Code: domain.ErrCodeCanceled, Message: "client went away"}
	calls := 0
	_, err := adapter.ChatCompletionStream(context.Background(), chatRequest(), func(Chunk) error {
		calls++
		return sentinel
	})
	if err == nil {
		t.Fatal("expected the handler error to abort the stream")
	}
	// The adapter must not reclassify a caller-initiated abort as a provider
	// failure, or the executor would fail over for a client that has gone away.
	if !errorsIs(err, sentinel) {
		t.Errorf("the handler's error must be propagated verbatim, got %v", err)
	}
	if calls != 1 {
		t.Errorf("handler calls = %d, want 1 before the abort", calls)
	}
}

func TestOpenAIAdapterReportsMidStreamInterruption(t *testing.T) {
	// The body ends without a [DONE] marker and the reader fails, which is what a
	// dropped connection looks like.
	transport := newStubTransport(func(*http.Request) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(&failingReader{data: "data: {\"id\":\"x\",\"choices\":[]}\n\n"}),
		}
	})
	adapter := adapterFor(t, providerFor("openai", domain.ProviderOpenAI, "https://api.openai.com/v1"), transport)

	_, err := adapter.ChatCompletionStream(context.Background(), chatRequest(), func(Chunk) error { return nil })
	if err == nil {
		t.Fatal("a truncated stream must be reported as a failure")
	}
	if code := domain.AsError(err).Code; code != domain.ErrCodeUpstream {
		t.Errorf("code = %q, want upstream_error", code)
	}
}

// failingReader yields its payload and then fails, simulating a dropped connection.
type failingReader struct {
	data string
	read bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.ErrUnexpectedEOF
	}
	r.read = true
	return copy(p, r.data), nil
}

// ---------------------------------------------------------------------------
// Anthropic translation
// ---------------------------------------------------------------------------

const anthropicSuccessBody = `{
  "id": "msg_1",
  "type": "message",
  "role": "assistant",
  "model": "claude-3-5-sonnet-20241022",
  "content": [{"type": "text", "text": "hi there"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 12, "output_tokens": 4}
}`

func TestAnthropicAdapterTranslatesTheRequest(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, anthropicSuccessBody)
	})
	adapter := adapterFor(t, anthropicProvider(), transport)

	if _, err := adapter.ChatCompletion(context.Background(), chatRequest()); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	req := transport.lastRequest(t)
	if got, want := req.URL.String(), "https://api.anthropic.com/v1/messages"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
	// Anthropic authenticates with a header, not a bearer token.
	if got := req.Header.Get("x-api-key"); got != "sk-test-credential" {
		t.Errorf("x-api-key = %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("anthropic must not receive a bearer token, got %q", got)
	}
	if got := req.Header.Get("anthropic-version"); got == "" {
		t.Error("the anthropic-version header is required")
	}

	body := transport.lastBody(t)
	// The system prompt is hoisted out of the message list into a top-level
	// field, which is the substantive part of the translation.
	if body["system"] != "be terse" {
		t.Errorf("system = %v, want the hoisted system prompt", body["system"])
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %v, want only the non-system turn", body["messages"])
	}
	if first := messages[0].(map[string]any); first["role"] != "user" {
		t.Errorf("first message = %v", first)
	}
	// max_tokens is mandatory for the Messages API.
	if body["max_tokens"] != float64(128) {
		t.Errorf("max_tokens = %v, want 128", body["max_tokens"])
	}
	if body["model"] != "gpt-4o" {
		t.Errorf("model = %v (the router, not the adapter, resolves the upstream name)", body["model"])
	}
}

// samplingRequest extends the standard request with the extended sampling
// controls.
func samplingRequest() *Request {
	req := chatRequest()
	topK := 40
	minP := 0.05
	repPenalty := 1.1
	req.Params.TopK = &topK
	req.Params.MinP = &minP
	req.Params.RepetitionPenalty = &repPenalty
	return req
}

func TestOpenAICompatibleForwardsExtendedSamplingControls(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, openAISuccessBody)
	})
	adapter := adapterFor(t,
		providerFor("compat", domain.ProviderOpenAICompatible, "https://router.invalid/v1"), transport)

	if _, err := adapter.ChatCompletion(context.Background(), samplingRequest()); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	body := transport.lastBody(t)
	if body["top_k"] != float64(40) {
		t.Errorf("top_k = %v, want 40", body["top_k"])
	}
	if body["min_p"] != 0.05 {
		t.Errorf("min_p = %v, want 0.05", body["min_p"])
	}
	if body["repetition_penalty"] != 1.1 {
		t.Errorf("repetition_penalty = %v, want 1.1", body["repetition_penalty"])
	}
}

func TestOpenAIProperDropsExtendedSamplingControls(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, openAISuccessBody)
	})
	adapter := adapterFor(t,
		providerFor("openai", domain.ProviderOpenAI, "https://api.openai.com/v1"), transport)

	if _, err := adapter.ChatCompletion(context.Background(), samplingRequest()); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	// OpenAI's own API has no top_k/min_p/repetition_penalty: sending them
	// would 400 every request that sets them.
	body := transport.lastBody(t)
	for _, field := range []string{"top_k", "min_p", "repetition_penalty"} {
		if _, present := body[field]; present {
			t.Errorf("OpenAI proper must not receive %s, got %v", field, body[field])
		}
	}
}

func TestAnthropicAdapterForwardsTopK(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, anthropicSuccessBody)
	})
	adapter := adapterFor(t, anthropicProvider(), transport)

	if _, err := adapter.ChatCompletion(context.Background(), samplingRequest()); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	body := transport.lastBody(t)
	if body["top_k"] != float64(40) {
		t.Errorf("top_k = %v, want 40", body["top_k"])
	}
}

func TestAnthropicAdapterNormalizesTheResponse(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, anthropicSuccessBody)
	})
	adapter := adapterFor(t, anthropicProvider(), transport)

	response, err := adapter.ChatCompletion(context.Background(), chatRequest())
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if response.Content() != "hi there" {
		t.Errorf("content = %q", response.Content())
	}
	// end_turn is Anthropic's word for a normal stop.
	if response.FinishReason() != domain.FinishStop {
		t.Errorf("finish reason = %q, want stop", response.FinishReason())
	}
	if response.Usage.PromptTokens != 12 || response.Usage.CompletionTokens != 4 {
		t.Errorf("usage = %+v", response.Usage)
	}
	if response.Usage.TotalTokens != 16 {
		t.Errorf("total tokens = %d, want the two halves summed", response.Usage.TotalTokens)
	}
}

func TestAnthropicAdapterNormalizesOverloadedError(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusServiceUnavailable, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	})
	adapter := adapterFor(t, anthropicProvider(), transport)

	_, err := adapter.ChatCompletion(context.Background(), chatRequest())
	normalized := domain.AsError(err)
	if normalized.Code != domain.ErrCodeUpstream {
		t.Fatalf("code = %q, want upstream_error", normalized.Code)
	}
	if !normalized.FallbackEligible {
		t.Error("an overloaded provider is the canonical case for failover")
	}
	if !normalized.Retryable {
		t.Error("an overloaded provider is worth retrying")
	}
}

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

func TestNewAdapterRejectsUnsupportedKinds(t *testing.T) {
	if _, err := NewAdapter(domain.Provider{Name: "x", Kind: ""}, Options{}); err == nil {
		t.Error("a provider with no kind must be rejected")
	}
	if _, err := NewAdapter(domain.Provider{Name: "x", Kind: "mystery"}, Options{}); err == nil {
		t.Error("an unknown kind must be rejected rather than silently defaulted")
	}
}

func TestBuildRegistryReportsFailuresWithoutAborting(t *testing.T) {
	providersList := []domain.Provider{
		providerFor("good", domain.ProviderOpenAI, "https://api.openai.com/v1"),
		{ID: "p-bad", Name: "bad", Kind: "mystery"},
		providerFor("also-good", domain.ProviderOpenAICompatible, "http://localhost:8000/v1"),
	}

	registry, failures := BuildRegistry(providersList, Options{Client: newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, openAISuccessBody)
	})})

	if len(failures) != 1 {
		t.Fatalf("failures = %v, want exactly the misconfigured provider", failures)
	}
	if _, ok := failures["bad"]; !ok {
		t.Errorf("the failing provider should be reported by name: %v", failures)
	}
	if registry.Len() != 2 {
		t.Errorf("registry size = %d, want the two usable providers", registry.Len())
	}
	if _, ok := registry.ByName("good"); !ok {
		t.Error("a working provider must still be registered")
	}
}

func TestDefaultCapabilitiesCoverLocalRuntimes(t *testing.T) {
	// A local runtime is assumed to stream and speak JSON mode; whether it can
	// call tools is a per-model fact the registry narrows.
	for _, kind := range []domain.ProviderKind{domain.ProviderOllama, domain.ProviderVLLM} {
		caps := DefaultCapabilities(kind)
		if !caps.Contains(domain.CapChat) {
			t.Errorf("%s must support chat", kind)
		}
		if !caps.Contains(domain.CapStreaming) {
			t.Errorf("%s must support streaming", kind)
		}
	}

	if DefaultCapabilities(domain.ProviderOpenAICompatible).Contains(domain.CapTools) {
		t.Error("tools are not assumed for an unknown compatible server")
	}
	if !DefaultCapabilities(domain.ProviderOpenAI).Contains(domain.CapTools) {
		t.Error("the OpenAI API supports tools")
	}
}

func TestAnthropicAdapterHonoursAnExplicitAuthStyle(t *testing.T) {
	// An operator who explicitly configures a style keeps control of it; the
	// kind-based default only fills a gap.
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, anthropicSuccessBody)
	})
	explicit := providerFor("anthropic", domain.ProviderAnthropic, "https://api.anthropic.com/v1")
	explicit.AuthStyle = domain.AuthBearer

	adapter := adapterFor(t, explicit, transport)
	if _, err := adapter.ChatCompletion(context.Background(), chatRequest()); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if got := transport.lastRequest(t).Header.Get("Authorization"); got == "" {
		t.Error("an explicit bearer style must be applied")
	}
}

func TestDefaultAuthStyleIsPerKind(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, openAISuccessBody)
	})

	openai := providerFor("openai", domain.ProviderOpenAI, "https://api.openai.com/v1")
	openai.AuthStyle = ""
	if _, err := adapterFor(t, openai, transport).ChatCompletion(context.Background(), chatRequest()); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if got := transport.lastRequest(t).Header.Get("Authorization"); got != "Bearer sk-test-credential" {
		t.Errorf("an unset style on an OpenAI provider must resolve to bearer, got %q", got)
	}

	ollama := providerFor("ollama", domain.ProviderOllama, "http://localhost:11434/v1")
	ollama.AuthStyle = ""
	ollamaTransport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, `{"message":{"role":"assistant","content":"hi"},"done":true}`)
	})
	if _, err := adapterFor(t, ollama, ollamaTransport).ChatCompletion(context.Background(), chatRequest()); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if got := ollamaTransport.lastRequest(t).Header.Get("Authorization"); got != "" {
		t.Errorf("a local runtime must receive no credential, got %q", got)
	}
}

func TestAdapterEnabledFollowsProviderStatus(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, openAISuccessBody)
	})

	active := providerFor("openai", domain.ProviderOpenAI, "https://api.openai.com/v1")
	adapter := adapterFor(t, active, transport)
	if !adapter.Enabled() {
		t.Error("an active provider must be enabled")
	}

	disabled := providerFor("openai", domain.ProviderOpenAI, "https://api.openai.com/v1")
	disabled.Status = domain.StatusDisabled
	adapter = adapterFor(t, disabled, transport)
	if adapter.Enabled() {
		t.Error("a disabled provider must not serve traffic")
	}
}

func TestAdapterHealthCheckUsesTheProviderModelsEndpoint(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, `{"data":[{"id":"gpt-4o"}]}`)
	})
	adapter := adapterFor(t, providerFor("openai", domain.ProviderOpenAI, "https://api.openai.com/v1"), transport)

	health := adapter.HealthCheck(context.Background())
	if health.State != domain.HealthHealthy {
		t.Errorf("state = %q (%s)", health.State, health.Message)
	}
	if got := transport.lastRequest(t).URL.Path; got != "/v1/models" {
		t.Errorf("probe path = %q, want /v1/models", got)
	}
}

func TestAdapterHealthCheckReportsFailure(t *testing.T) {
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusUnauthorized, `{"error":{"message":"bad key"}}`)
	})
	adapter := adapterFor(t, providerFor("openai", domain.ProviderOpenAI, "https://api.openai.com/v1"), transport)

	health := adapter.HealthCheck(context.Background())
	if health.State == domain.HealthHealthy {
		t.Errorf("an unauthenticated probe must not report healthy: %+v", health)
	}
	if health.Message == "" {
		t.Error("a failed probe must explain itself")
	}
}

func TestOllamaAdapterMapsExtendedSamplingControls(t *testing.T) {
	ollama := providerFor("ollama", domain.ProviderOllama, "http://localhost:11434")
	ollama.AuthStyle = ""
	transport := newStubTransport(func(*http.Request) *http.Response {
		return jsonResponse(http.StatusOK, `{"message":{"role":"assistant","content":"hi"},"done":true}`)
	})
	if _, err := adapterFor(t, ollama, transport).ChatCompletion(context.Background(), samplingRequest()); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	body := transport.lastBody(t)
	options, ok := body["options"].(map[string]any)
	if !ok {
		t.Fatalf("options = %v, want the sampling block", body["options"])
	}
	if options["top_k"] != float64(40) {
		t.Errorf("options.top_k = %v, want 40", options["top_k"])
	}
	if options["min_p"] != 0.05 {
		t.Errorf("options.min_p = %v, want 0.05", options["min_p"])
	}
	if options["repeat_penalty"] != 1.1 {
		t.Errorf("options.repeat_penalty = %v, want 1.1", options["repeat_penalty"])
	}
}

// errorsIs reports whether target is err or is wrapped by it, without importing
// errors into every assertion.
func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
