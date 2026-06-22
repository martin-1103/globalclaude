package format

import (
	"strings"
	"testing"

	"agent-explorer/internal/explorer"
	"agent-explorer/internal/planner"
	"agent-explorer/internal/tools"
)

func TestMainAgentContractShape(t *testing.T) {
	result := explorer.Result{
		Plan: planner.Plan{Intent: "mixed", PrimaryTool: "graph_text", NeedCallGraph: true},
		Hits: []tools.Hit{
			{File: "/repo/a.go", LineStart: 10, LineEnd: 12, Symbol: "pkg.Auth", Why: "graph text search", Confidence: "medium"},
			{File: "/repo/b.go", LineStart: 20, LineEnd: 24, Symbol: "pkg.Handler", Why: "trace expansion", Confidence: "medium", EvidenceType: "trace", SupportRole: "callee"},
		},
		PrimaryHits: []tools.Hit{
			{File: "/repo/a.go", LineStart: 10, LineEnd: 12, Symbol: "pkg.Auth", Why: "graph text search", Confidence: "medium"},
		},
		TraceHits: []tools.Hit{
			{File: "/repo/b.go", LineStart: 20, LineEnd: 24, Symbol: "pkg.Handler", Why: "trace expansion", Confidence: "medium", EvidenceType: "trace", SupportRole: "callee"},
		},
	}
	out := FinalAnswer(result, false, false, true, false)
	for _, want := range []string{
		"retrieval_contract",
		"status=grounded",
		"planner_status=trace_required",
		"answerability=partial",
		"confidence_band=medium",
		"top_confidence=medium",
		"quality_flags=supporting_gap",
		"trace_summary:",
		"recommended_action=re-retrieve",
		"<final_answer>",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestFinalAnswerDedupesCitationsAndShowsReadableSummary(t *testing.T) {
	result := explorer.Result{
		Plan: planner.Plan{Intent: "definition", PrimaryTool: "graph"},
		Hits: []tools.Hit{
			{File: "/repo/a.go", LineStart: 10, LineEnd: 12, Symbol: "pkg.Auth", Why: "definition", Confidence: "high"},
			{File: "/repo/a.go", LineStart: 10, LineEnd: 12, Symbol: "pkg.Auth", Why: "definition", Confidence: "high"},
		},
		PrimaryHits: []tools.Hit{
			{File: "/repo/a.go", LineStart: 10, LineEnd: 12, Symbol: "pkg.Auth", Why: "definition", Confidence: "high"},
			{File: "/repo/a.go", LineStart: 10, LineEnd: 12, Symbol: "pkg.Auth", Why: "definition", Confidence: "high"},
		},
	}
	out := FinalAnswer(result, false, false, false, false)
	if strings.Count(out, "/repo/a.go:10-12") != 2 {
		t.Fatalf("expected one evidence line and one citation, got output:\n%s", out)
	}
	for _, want := range []string{"Retrieval Pack", "Quality Flags: none", "Confidence: high (top=high) | Action: reason"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in output:\n%s", want, out)
		}
	}
}
