package logs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-explorer/internal/explorer"
	"agent-explorer/internal/planner"
	"agent-explorer/internal/tools"
)

func TestSaveAskRun(t *testing.T) {
	baseDir := t.TempDir()
	dir, err := SaveAskRun(baseDir, AskRequest{Command: "ask", Repo: "/tmp/repo", Query: "where auth"}, explorer.Result{
		Query:       "where auth",
		Repo:        "/tmp/repo",
		Plan:        planner.Plan{Intent: "mixed", PrimaryTool: "graph_text", NeedCallGraph: true},
		Hits:        []tools.Hit{{File: "/tmp/repo/auth.go", LineStart: 10, LineEnd: 20, Symbol: "Auth", Score: 88, Confidence: "high"}},
		PrimaryHits: []tools.Hit{{File: "/tmp/repo/auth.go", LineStart: 10, LineEnd: 20}},
	}, nil)
	if err != nil {
		t.Fatalf("SaveAskRun: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "summary.txt"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		"top_hit: /tmp/repo/auth.go:10-20",
		"planner_status: trace_required",
		"answerability: partial",
		"confidence_band: medium",
		"top_confidence: high",
		"recommended_action: trace",
		"quality_flags: supporting_gap,trace_gap",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in summary: %s", want, text)
		}
	}
	if !strings.Contains(text, "top_hit: /tmp/repo/auth.go:10-20") {
		t.Fatalf("unexpected summary: %s", string(raw))
	}
}
