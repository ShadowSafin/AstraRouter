package shaping

import (
	"testing"

	"github.com/shadowsafin/synapass/internal/domain"
)

func TestNormalizeMergesSystem(t *testing.T) {
	p := New(DefaultOptions())
	req := &domain.ChatCompletionRequest{
		Model: "m",
		Messages: []domain.ChatMessage{
			{Role: domain.RoleSystem, Content: domain.NewTextContent("a")},
			{Role: domain.RoleSystem, Content: domain.NewTextContent("b")},
			{Role: domain.RoleUser, Content: domain.NewTextContent("hi")},
		},
	}
	plan := domain.PromptShapePlan{NormalizeSystem: true}
	out, shape := p.Apply(req, plan)
	if len(out.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out.Messages))
	}
	if shape.TokensIn == 0 || shape.TokensOut == 0 {
		t.Fatalf("expected token counts")
	}
}

func TestTrimHistory(t *testing.T) {
	p := New(DefaultOptions())
	msgs := []domain.ChatMessage{{Role: domain.RoleSystem, Content: domain.NewTextContent("sys")}}
	for i := 0; i < 10; i++ {
		msgs = append(msgs, domain.ChatMessage{Role: domain.RoleUser, Content: domain.NewTextContent("hello")})
	}
	req := &domain.ChatCompletionRequest{Model: "m", Messages: msgs}
	plan := domain.PromptShapePlan{MaxHistoryMessages: 5}
	out, shape := p.Apply(req, plan)
	if len(out.Messages) != 5 {
		t.Fatalf("expected 5, got %d", len(out.Messages))
	}
	if shape.Truncated == 0 {
		t.Fatalf("expected truncated count")
	}
}

func TestPlanForLongContext(t *testing.T) {
	p := New(DefaultOptions())
	plan := p.PlanFor(domain.TaskClassification{Task: domain.TaskLongContext}, nil, nil)
	if !plan.TrimContext || !plan.SummarizeHistory {
		t.Fatalf("long context should trim+summarize: %+v", plan)
	}
}
