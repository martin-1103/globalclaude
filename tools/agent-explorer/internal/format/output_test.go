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
		Plan: planner.Plan{Intent: "mixed", PrimaryTool: "graph_text"},
		Hits: []tools.Hit{
			{File: "/repo/a.go", LineStart: 10, LineEnd: 12, Symbol: "pkg.Auth", Why: "graph text search", Confidence: "medium"},
		},
		PrimaryHits: []tools.Hit{
			{File: "/repo/a.go", LineStart: 10, LineEnd: 12, Symbol: "pkg.Auth", Why: "graph text search", Confidence: "medium"},
		},
	}
	out := FinalAnswer(result, false, false, true, false)
	for _, want := range []string{"retrieval_contract", "status=grounded", "primary_evidence:", "recommended_action=reason", "<final_answer>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in output:\n%s", want, out)
		}
	}
}
