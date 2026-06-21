package planner

import (
	"testing"

	"agent-explorer/internal/config"
)

func TestHeuristicSlotsAuthMiddlewareDefinition(t *testing.T) {
	slots := heuristicSlots("where auth middleware defined")
	if len(slots) == 0 {
		t.Fatalf("expected slots")
	}
	if slots[0].Role != "validator" {
		t.Fatalf("expected first slot validator, got %q", slots[0].Role)
	}
}

func TestHeuristicSlotsAuthTraceQuery(t *testing.T) {
	slots := heuristicSlots("trace how auth middleware validates token and where claims are consumed")
	if len(slots) < 2 {
		t.Fatalf("expected multiple slots, got %d", len(slots))
	}
	foundValidator := false
	foundConsumer := false
	for _, slot := range slots {
		if slot.Role == "validator" {
			foundValidator = true
		}
		if slot.Role == "consumer" || slot.Role == "injector" {
			foundConsumer = true
		}
	}
	if !foundValidator || !foundConsumer {
		t.Fatalf("expected validator and consumer/injector slots, got %+v", slots)
	}
}

func TestHeuristicSlotsClaimsStoredQuery(t *testing.T) {
	slots := heuristicSlots("where request context gets claims stored")
	if len(slots) == 0 {
		t.Fatalf("expected slots")
	}
	found := false
	for _, slot := range slots {
		if slot.Role == "injector" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected injector slot, got %+v", slots)
	}
}

func TestHeuristicPlanAuthMiddlewareDefinitionPrefersGraphText(t *testing.T) {
	plan := heuristicPlan("where auth middleware defined", config.RepoProfile{})
	if plan.PrimaryTool != "graph" {
		t.Fatalf("expected graph primary, got %q", plan.PrimaryTool)
	}
}

func TestHeuristicPlanClaimsStoredPrefersGraphText(t *testing.T) {
	plan := heuristicPlan("where request context gets claims stored", config.RepoProfile{})
	if plan.PrimaryTool != "graph_text" {
		t.Fatalf("expected graph_text primary, got %q", plan.PrimaryTool)
	}
}
