package config

import "testing"

func TestLoadRuntimeDefaultsMemoryEntryBudget(t *testing.T) {
	profile := RepoProfile{}
	if profile.MemoryEntryBudget != 0 {
		t.Fatalf("expected zero-value profile budget before defaulting")
	}
	if profile.MemoryEntryBudget <= 0 {
		profile.MemoryEntryBudget = 1000
	}
	if profile.MemoryEntryBudget != 1000 {
		t.Fatalf("profile memory budget = %d, want 1000", profile.MemoryEntryBudget)
	}
}

func TestRepoProfileDefaultsStaleLineDistance(t *testing.T) {
	profile := RepoProfile{}
	if profile.StaleLineDistance != 0 {
		t.Fatalf("expected zero-value stale line distance before defaulting")
	}
	if profile.StaleLineDistance <= 0 {
		profile.StaleLineDistance = 40
	}
	if profile.StaleLineDistance != 40 {
		t.Fatalf("profile stale line distance = %d, want 40", profile.StaleLineDistance)
	}
}
