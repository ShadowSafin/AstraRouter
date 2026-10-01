package classifier

import (
	"testing"

	"github.com/corerouter/corerouter/internal/domain"
)

func TestClassifyChatDefault(t *testing.T) {
	c := New(DefaultOptions())
	req := &domain.ChatCompletionRequest{
		Model:    "gpt-4o-mini",
		Messages: []domain.ChatMessage{{Role: domain.RoleUser, Content: domain.NewTextContent("hello")}},
	}
	got := c.Classify(req, 10)
	if got.Task != domain.TaskChat {
		t.Fatalf("expected chat, got %s", got.Task)
	}
}

func TestClassifyCoding(t *testing.T) {
	c := New(DefaultOptions())
	req := &domain.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []domain.ChatMessage{{Role: domain.RoleUser, Content: domain.NewTextContent("```python\ndef foo():\n  pass\n```")}},
	}
	got := c.Classify(req, 100)
	if got.Task != domain.TaskCoding {
		t.Fatalf("expected coding, got %s signals=%v", got.Task, got.Signals)
	}
}

func TestClassifyToolUse(t *testing.T) {
	c := New(DefaultOptions())
	req := &domain.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []domain.ChatMessage{{Role: domain.RoleUser, Content: domain.NewTextContent("book a flight")}},
		Tools:    []domain.Tool{{Type: "function", Function: domain.FunctionDefinition{Name: "book"}}},
	}
	got := c.Classify(req, 50)
	if got.Task != domain.TaskToolUse {
		t.Fatalf("expected tool-use, got %s", got.Task)
	}
}

func TestClassifyStructured(t *testing.T) {
	c := New(DefaultOptions())
	req := &domain.ChatCompletionRequest{
		Model:          "gpt-4o",
		Messages:       []domain.ChatMessage{{Role: domain.RoleUser, Content: domain.NewTextContent("give me json")}},
		ResponseFormat: &domain.ResponseFormat{Type: "json_object"},
	}
	got := c.Classify(req, 50)
	if got.Task != domain.TaskStructuredOutput {
		t.Fatalf("expected structured, got %s", got.Task)
	}
}

func TestClassifyLongContext(t *testing.T) {
	c := New(DefaultOptions())
	req := &domain.ChatCompletionRequest{
		Model:    "gpt-4o",
		Messages: []domain.ChatMessage{{Role: domain.RoleUser, Content: domain.NewTextContent("hello")}},
	}
	got := c.Classify(req, 50000)
	if got.Task != domain.TaskLongContext {
		t.Fatalf("expected long_context, got %s", got.Task)
	}
}

func TestClassifySummarization(t *testing.T) {
	c := New(DefaultOptions())
	req := &domain.ChatCompletionRequest{
		Model:    "gpt-4o-mini",
		Messages: []domain.ChatMessage{{Role: domain.RoleUser, Content: domain.NewTextContent("Summarize this article for me please")}},
	}
	got := c.Classify(req, 500)
	if got.Task != domain.TaskSummarization {
		t.Fatalf("expected summarization, got %s", got.Task)
	}
}
