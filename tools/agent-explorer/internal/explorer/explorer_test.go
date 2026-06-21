package explorer

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"agent-explorer/internal/config"
	"agent-explorer/internal/planner"
	"agent-explorer/internal/tools"
)

func TestMaxToolFamiliesUsesProfileOverride(t *testing.T) {
	exp := &Explorer{profile: config.RepoProfile{MaxToolFamilies: 3}}
	if got := exp.maxToolFamilies(); got != 3 {
		t.Fatalf("maxToolFamilies() = %d, want 3", got)
	}
}

func TestMaxToolFamiliesDefaultsToTwo(t *testing.T) {
	exp := &Explorer{profile: config.RepoProfile{}}
	if got := exp.maxToolFamilies(); got != 2 {
		t.Fatalf("maxToolFamilies() = %d, want 2", got)
	}
}

func TestShouldUseDualLaneMixedSemantic(t *testing.T) {
	exp := &Explorer{}
	plan := planner.Plan{Intent: "mixed", PrimaryTool: "semantic"}
	if !exp.shouldUseDualLane(plan) {
		t.Fatalf("shouldUseDualLane() = false, want true")
	}
}

func TestShouldUseDualLaneLiteralSemanticFalse(t *testing.T) {
	exp := &Explorer{}
	plan := planner.Plan{Intent: "literal", PrimaryTool: "semantic"}
	if exp.shouldUseDualLane(plan) {
		t.Fatalf("shouldUseDualLane() = true, want false")
	}
}

func TestConfidenceBandForPlanMixedStricterThanDefault(t *testing.T) {
	plan := planner.Plan{Intent: "mixed"}
	if got := confidenceBandForPlan(plan, 95); got != "medium" {
		t.Fatalf("confidenceBandForPlan(mixed,95) = %s, want medium", got)
	}
}

func TestParallelTermWorkersBounded(t *testing.T) {
	if got := parallelTermWorkers(2); got != 2 {
		t.Fatalf("parallelTermWorkers(2) = %d, want 2", got)
	}
	if got := parallelTermWorkers(5); got != 3 {
		t.Fatalf("parallelTermWorkers(5) = %d, want 3", got)
	}
	if got := parallelTermWorkers(10); got != 4 {
		t.Fatalf("parallelTermWorkers(10) = %d, want 4", got)
	}
}

func TestParallelPerTermRespectsWorkerLimit(t *testing.T) {
	terms := []string{"a", "b", "c", "d", "e", "f", "g"}
	var current int32
	var maxSeen int32
	hits := parallelPerTerm(context.Background(), terms, func(term string) ([]tools.Hit, error) {
		now := atomic.AddInt32(&current, 1)
		for {
			prev := atomic.LoadInt32(&maxSeen)
			if now <= prev || atomic.CompareAndSwapInt32(&maxSeen, prev, now) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&current, -1)
		return nil, nil
	}, func(error) {})
	if len(hits) != 0 {
		t.Fatalf("expected no hits, got %d", len(hits))
	}
	if maxSeen > 4 {
		t.Fatalf("max concurrent workers = %d, want <= 4", maxSeen)
	}
}

func TestParallelSlotWorkersBounded(t *testing.T) {
	if got := parallelSlotWorkers(1); got != 1 {
		t.Fatalf("parallelSlotWorkers(1) = %d, want 1", got)
	}
	if got := parallelSlotWorkers(3); got != 2 {
		t.Fatalf("parallelSlotWorkers(3) = %d, want 2", got)
	}
	if got := parallelSlotWorkers(8); got != 3 {
		t.Fatalf("parallelSlotWorkers(8) = %d, want 3", got)
	}
}
