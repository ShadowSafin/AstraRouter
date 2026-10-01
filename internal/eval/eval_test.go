package eval

import "testing"

func TestCompareRanksBestFirst(t *testing.T) {
	e := New()
	res := e.Compare(CompareInput{
		RequestID: "r1",
		Reference: "the quick brown fox",
		Outputs: []ProviderOutput{
			{Provider: "a", Model: "m", Output: "the quick brown fox"},
			{Provider: "b", Model: "m", Output: "something entirely different here"},
		},
	})
	if res.Winner != "a/m" {
		t.Fatalf("expected a/m winner, got %s", res.Winner)
	}
	if len(res.Ranking) != 2 || res.Ranking[0].Score < res.Ranking[1].Score {
		t.Fatalf("ranking not descending: %+v", res.Ranking)
	}
}

func TestRegression(t *testing.T) {
	e := New()
	if !e.CheckRegression(0.9, 0.5, 0.05) {
		t.Fatalf("expected regression")
	}
	if e.CheckRegression(0.9, 0.88, 0.05) {
		t.Fatalf("unexpected regression")
	}
}

func TestGoldenThreshold(t *testing.T) {
	e := New()
	cases := []GoldenCase{{ID: "c1", Reference: "hello world", MinScore: 0.9}}
	results := e.RunGoldens(cases, map[string]string{"c1": "completely unrelated answer xyz"})
	if len(results) != 1 || !results[0].IsRegression {
		t.Fatalf("expected regression flag: %+v", results)
	}
}
