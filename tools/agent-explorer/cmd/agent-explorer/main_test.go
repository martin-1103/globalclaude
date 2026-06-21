package main

import (
	"testing"

	"agent-explorer/internal/explorer"
	"agent-explorer/internal/learning"
	"agent-explorer/internal/tools"
)

func TestClassifyEvalOutcomeNoHits(t *testing.T) {
	var tax evalTaxonomy
	classifyEvalOutcome(&tax, nil, evalCase{})
	if tax.NoHits != 1 {
		t.Fatalf("NoHits = %d, want 1", tax.NoHits)
	}
}

func TestClassifyEvalOutcomeWeakTop1(t *testing.T) {
	var tax evalTaxonomy
	hits := []tools.Hit{{File: "svc/auth.go", Confidence: "low"}}
	item := evalCase{ExpectTopPath: []string{"auth.go"}}
	classifyEvalOutcome(&tax, hits, item)
	if tax.WeakTop1 != 1 {
		t.Fatalf("WeakTop1 = %d, want 1", tax.WeakTop1)
	}
}

func TestClassifyEvalOutcomeBelowTop1Match(t *testing.T) {
	var tax evalTaxonomy
	hits := []tools.Hit{
		{File: "svc/wrong.go", Confidence: "high"},
		{File: "svc/right.go", Confidence: "medium"},
	}
	item := evalCase{ExpectAnyPath: []string{"right.go"}}
	classifyEvalOutcome(&tax, hits, item)
	if tax.MatchedOnlyBelowTop1 != 1 {
		t.Fatalf("MatchedOnlyBelowTop1 = %d, want 1", tax.MatchedOnlyBelowTop1)
	}
}

func TestClassifyEvalOutcomeRejectedTop1(t *testing.T) {
	var tax evalTaxonomy
	hits := []tools.Hit{{File: "svc/test_helper.go", Confidence: "high"}}
	item := evalCase{RejectTopPath: []string{"test_helper.go"}}
	classifyEvalOutcome(&tax, hits, item)
	if tax.RejectedTop1 != 1 {
		t.Fatalf("RejectedTop1 = %d, want 1", tax.RejectedTop1)
	}
}

func TestShouldAutoLearnPassGroundedMedium(t *testing.T) {
	result := explorer.Result{Hits: []tools.Hit{{Confidence: "medium"}}}
	if !shouldAutoLearn(result, true) {
		t.Fatalf("shouldAutoLearn() = false, want true")
	}
}

func TestShouldAutoLearnRejectWeakEvidence(t *testing.T) {
	result := explorer.Result{Hits: []tools.Hit{{Confidence: "low"}}}
	if shouldAutoLearn(result, false) {
		t.Fatalf("shouldAutoLearn() = true, want false")
	}
}

func TestMemoryMaintenanceNeededByEntryBudget(t *testing.T) {
	stats := learning.Stats{Entries: 1200, StaleEntries: 0}
	if !memoryMaintenanceNeeded(stats, 1000, false) {
		t.Fatalf("expected maintenance needed by entry budget")
	}
}

func TestMemoryMaintenanceNeededByStaleEntries(t *testing.T) {
	stats := learning.Stats{Entries: 10, StaleEntries: 2}
	if !memoryMaintenanceNeeded(stats, 1000, true) {
		t.Fatalf("expected maintenance needed by stale entries")
	}
}

func TestMemoryMaintenanceSummaryHealthy(t *testing.T) {
	stats := learning.Stats{Entries: 10, StaleEntries: 0}
	got := memoryMaintenanceSummary(stats, 1000, true)
	if got != "action_needed=false reason=healthy" {
		t.Fatalf("summary = %q", got)
	}
}

func TestMemoryMaintenanceSummaryUsesBudget(t *testing.T) {
	stats := learning.Stats{Entries: 1001, StaleEntries: 0}
	got := memoryMaintenanceSummary(stats, 1000, true)
	if got != "action_needed=true reason=entries>1000" {
		t.Fatalf("summary = %q", got)
	}
}
