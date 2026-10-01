// Package classifier infers the workload class of a request before routing.
//
// The classifier is deterministic and rules-first: the same request always
// yields the same task. Heuristics inspect message text, tool declarations,
// response format, token counts and streaming flags. An optional Python service
// can refine the result asynchronously, but live routing never blocks on it.
package classifier

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shadowsafin/corerouter/internal/domain"
)

// Options tunes classification thresholds.
type Options struct {
	// LongContextTokens marks a request as long-context.
	LongContextTokens int
	// BatchTokens marks a request as batch/offline when the prompt is huge
	// and the client did not ask for streaming.
	BatchTokens int
}

// DefaultOptions returns production-minded thresholds.
func DefaultOptions() Options {
	return Options{LongContextTokens: 32000, BatchTokens: 64000}
}

// Classifier infers task types.
type Classifier struct {
	opts Options
}

// New creates a classifier.
func New(opts Options) *Classifier {
	if opts.LongContextTokens <= 0 {
		opts.LongContextTokens = 32000
	}
	if opts.BatchTokens <= 0 {
		opts.BatchTokens = 64000
	}
	return &Classifier{opts: opts}
}

// Classify infers the task for a chat completion request.
func (c *Classifier) Classify(req *domain.ChatCompletionRequest, promptTokens int) domain.TaskClassification {
	if c == nil {
		c = New(DefaultOptions())
	}
	now := time.Now().UTC()
	signals := []string{}
	secondary := []domain.TaskType{}

	text := ""
	msgCount := 0
	if req != nil {
		text = req.PromptText()
		msgCount = len(req.Messages)
	}
	lower := strings.ToLower(text)
	task := domain.TaskChat
	conf := 0.6

	// Tool-use is structural: tools present means tool-use regardless of text.
	if req != nil && len(req.Tools) > 0 {
		task = domain.TaskToolUse
		conf = 1.0
		signals = append(signals, "tools_present")
	} else if req != nil && req.ResponseFormat != nil && (req.ResponseFormat.Type == "json_object" || req.ResponseFormat.Type == "json_schema") {
		task = domain.TaskStructuredOutput
		conf = 1.0
		signals = append(signals, "response_format_structured")
	} else if hasImages(req) {
		// Images keep chat as primary but note vision; task stays chat.
		signals = append(signals, "vision_content")
	}

	// Long-context overrides when the prompt is huge.
	if promptTokens >= c.opts.LongContextTokens {
		secondary = append(secondary, task)
		task = domain.TaskLongContext
		conf = 0.9
		signals = append(signals, "long_prompt")
	}
	// Batch/offline: huge non-streaming prompt.
	if req != nil && !req.Streaming() && promptTokens >= c.opts.BatchTokens {
		secondary = append(secondary, task)
		task = domain.TaskBatch
		conf = 0.8
		signals = append(signals, "batch_candidate")
	}

	// Text heuristics only when no structural signal won.
	if len(signals) == 0 || task == domain.TaskChat {
		if t, sig, ok := textTask(lower, text); ok {
			if task != domain.TaskChat && task != domain.TaskLongContext && task != domain.TaskBatch {
				secondary = append(secondary, t)
			} else {
				task = t
				conf = 0.8
			}
			signals = append(signals, sig)
		}
	}

	// High-priority interactive: short streaming chat.
	if req != nil && req.Streaming() && promptTokens < 4000 && task == domain.TaskChat {
		secondary = append(secondary, domain.TaskInteractive)
		signals = append(signals, "streaming_short")
	}

	// Code detection is additive: code in a chat request is still chat, but the
	// coding signal helps routing prefer code-capable models.
	if containsCode(lower) && task == domain.TaskChat {
		task = domain.TaskCoding
		conf = 0.8
		signals = append(signals, "code_markers")
	}

	if len(signals) == 0 {
		signals = append(signals, "default_chat")
	}

	return domain.TaskClassification{
		Task:         task,
		Secondary:    dedupeTasks(secondary),
		Confidence:   conf,
		Signals:      signals,
		PromptTokens: promptTokens,
		MessageCount: msgCount,
		ClassifiedAt: now,
		Source:       "rules",
	}
}

// ClassifyContext classifies from an already-built RequestContext.
func (c *Classifier) ClassifyContext(rc *domain.RequestContext, req *domain.ChatCompletionRequest) domain.TaskClassification {
	promptTokens := 0
	if rc != nil {
		promptTokens = rc.PromptTokens
	}
	return c.Classify(req, promptTokens)
}

func hasImages(req *domain.ChatCompletionRequest) bool {
	if req == nil {
		return false
	}
	for _, m := range req.Messages {
		if m.Content.HasImages() {
			return true
		}
	}
	return false
}

func textTask(lower, original string) (domain.TaskType, string, bool) {
	switch {
	case containsAny(lower, []string{"summariz", "tl;dr", "tldr", "summarize this", "give me the gist"}):
		return domain.TaskSummarization, "summarize_keywords", true
	case containsAny(lower, []string{"translat", "translate to", "en français", "auf deutsch", "al español"}):
		return domain.TaskTranslation, "translate_keywords", true
	case containsAny(lower, []string{"extract", "pull out", "list all", "find all", "parse the", "ner ", "named entit"}):
		return domain.TaskExtraction, "extract_keywords", true
	case containsAny(lower, []string{"prove", "step by step", "chain of thought", "reason about", "logic puzzle", "theorem", "qed"}):
		return domain.TaskReasoning, "reasoning_keywords", true
	case isMostlyCode(original):
		return domain.TaskCoding, "code_shape", true
	default:
		return "", "", false
	}
}

func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

func containsCode(lower string) bool {
	markers := []string{"```", "func ", "def ", "import ", "package ", "class ", "const ", "let ", "fn ", "public static", "select ", "=>", "```python", "```go", "```js"}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

func isMostlyCode(s string) bool {
	if s == "" {
		return false
	}
	lines := strings.Split(s, "\n")
	codeLines := 0
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "import ") || strings.HasPrefix(t, "func ") || strings.HasPrefix(t, "def ") || strings.HasPrefix(t, "package ") || strings.Contains(t, "{") && strings.Contains(t, "}") || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") {
			codeLines++
		}
	}
	if len(lines) == 0 {
		return false
	}
	return float64(codeLines)/float64(len(lines)) > 0.4 && utf8.RuneCountInString(s) > 200
}

func dedupeTasks(in []domain.TaskType) []domain.TaskType {
	seen := map[domain.TaskType]struct{}{}
	out := []domain.TaskType{}
	for _, t := range in {
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}
