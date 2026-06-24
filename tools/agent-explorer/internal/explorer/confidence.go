package explorer

import (
	"path/filepath"
	"sort"
	"strings"

	"agent-explorer/internal/planner"
	"agent-explorer/internal/tools"
)

func (e *Explorer) annotateHits(query string, hits []tools.Hit) {
	for i := range hits {
		hits[i].Score = e.rankHit(query, hits[i])
		hits[i].Score += int(hits[i].FusionScore * 100)
		hits[i].Confidence = confidenceBand(hits[i].Score)
		if hits[i].Family == "" {
			hits[i].Family = familyFromSource(hits[i].Source)
		}
	}
}

func confidenceBand(score int) string {
	switch {
	case score >= 95:
		return "high"
	case score >= 62:
		return "medium"
	default:
		return "low"
	}
}

func confidenceBandForPlan(plan planner.Plan, score int) string {
	switch plan.Intent {
	case "literal":
		switch {
		case score >= 92:
			return "high"
		case score >= 68:
			return "medium"
		default:
			return "low"
		}
	case "definition":
		switch {
		case score >= 90:
			return "high"
		case score >= 66:
			return "medium"
		default:
			return "low"
		}
	case "mixed":
		switch {
		case score >= 97:
			return "high"
		case score >= 72:
			return "medium"
		default:
			return "low"
		}
	case "behavior":
		switch {
		case score >= 96:
			return "high"
		case score >= 64:
			return "medium"
		default:
			return "low"
		}
	default:
		return confidenceBand(score)
	}
}

func hasConfidenceAtLeast(hits []tools.Hit, target string) bool {
	need := 0
	switch target {
	case "medium":
		need = 1
	case "high":
		need = 2
	}
	for _, hit := range hits {
		level := 0
		switch hit.Confidence {
		case "high":
			level = 2
		case "medium":
			level = 1
		}
		if level >= need {
			return true
		}
	}
	return false
}

func (e *Explorer) calibrateConfidenceBands(plan planner.Plan, hits []tools.Hit) []tools.Hit {
	if len(hits) == 0 {
		return hits
	}
	// Coverage ratio: how many of the plan's evidence slots have at least one
	// non-low candidate. Used as a harmonic factor (0.1 weight), not a binary gate.
	// Research (Google CACM 2023, Multi-Signal Fusion 2025): slot coverage contributes
	// independently — missing one slot should not zero out other hits' confidence.
	coverageRatio := e.slotCoverageRatio(plan, hits)
	diverseEnough := laneDiversity(hits) >= 2 || fileDiversity(hits) >= 2
	// Cross-service outlier: hits from a different service than the majority are penalized.
	// This kills false positives like PublishFailedRows (event-processor) when querying
	// RequeueStaleRunningWorkItems (sync-service).
	majoritySvc := majorityService(hits)
	for i := range hits {
		base := confidenceBandForPlan(plan, hits[i].Score)
		corroboration := corroborationCount(hits, hits[i], i)
		independent := independentEvidenceCount(hits, hits[i], i)
		exactish := strings.Contains(strings.ToLower(hits[i].Why), "exact symbol resolve") || strings.TrimSpace(hits[i].EvidenceType) == "literal"
		outlierSvc := majoritySvc != "" && hitService(hits[i].File) != "" && hitService(hits[i].File) != majoritySvc
		switch {
		case hits[i].EvidenceType == "trace":
			if corroboration >= 2 && independent >= 1 && coverageRatio >= 0.5 && hits[i].Score >= 82 {
				hits[i].Confidence = "medium"
			} else {
				hits[i].Confidence = "low"
			}
		case base == "high":
			// Exact symbol resolve → always "high" for definition/callers/literal.
			// No longer gated by coverageOK. Research: exact symbol match dominates.
			if exactish && (plan.Intent == "definition" || plan.Intent == "callers" || plan.Intent == "literal") {
				hits[i].Confidence = "high"
			} else if exactish && coverageRatio >= 0.5 && !outlierSvc {
				hits[i].Confidence = "high"
			} else if coverageRatio >= 0.7 && diverseEnough && corroboration >= 1 && !plan.Ambiguous {
				hits[i].Confidence = "high"
			} else {
				hits[i].Confidence = "medium"
			}
		case base == "medium":
			if exactish && coverageRatio >= 0.3 && !outlierSvc {
				hits[i].Confidence = "medium"
			} else if coverageRatio >= 0.5 && corroboration >= 1 && diverseEnough && !outlierSvc {
				hits[i].Confidence = "medium"
			} else {
				hits[i].Confidence = "low"
			}
		default:
			if exactish && corroboration >= 1 && !outlierSvc {
				hits[i].Confidence = "medium"
			} else {
				hits[i].Confidence = "low"
			}
		}
		// Outlier penalty: hits from unrelated services can never be "high"
		if outlierSvc && hits[i].Confidence == "high" {
			hits[i].Confidence = "medium"
		}
		// Ambiguous plan: downgrade high→medium when coverage is poor
		if plan.Ambiguous && hits[i].Confidence == "high" && coverageRatio < 0.6 {
			hits[i].Confidence = "medium"
		}
		// Mixed intent with very poor coverage: cap at medium
		if plan.Intent == "mixed" && coverageRatio < 0.3 && !exactish {
			hits[i].Confidence = "low"
		}
	}
	return hits
}

// slotCoverageRatio returns the fraction of weighted evidence slots that have
// at least one non-low candidate. Range: 0.0 (none covered) to 1.0 (all covered).

func (e *Explorer) slotCoverageRatio(plan planner.Plan, hits []tools.Hit) float64 {
	if len(plan.Slots) == 0 {
		return 1.0
	}
	need := 0
	have := 0
	for _, slot := range plan.Slots {
		if slot.Weight < 2 {
			continue
		}
		need++
		best := e.bestSlotEvidence(slot, hits)
		if best != nil && strings.TrimSpace(best.Confidence) != "low" {
			have++
		}
	}
	if need == 0 {
		return 1.0
	}
	return float64(have) / float64(need)
}

// majorityService returns the most frequent service name among top hits.

// majorityService detects the primary service from the most reliable hits.
// Uses only exact symbol matches or top-20%-scoring hits to avoid the
// "FP majority" problem: when cross-service false positives outnumber
// correct hits, using ALL hits picks the wrong majority.
// Research: CodeNav (2024) uses soft penalty during late fusion;
// LocAgent (2025) detects primary module before retrieval.
func majorityService(hits []tools.Hit) string {
	// Collect reliable hits: exact symbol resolves OR top-score tier
	sorted := make([]tools.Hit, len(hits))
	copy(sorted, hits)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Score > sorted[j].Score })

	var topThreshold int
	if len(sorted) > 0 {
		// Top 25% score percentile
		idx := len(sorted) / 4
		if idx == 0 {
			idx = 0
		}
		topThreshold = sorted[idx].Score
	}

	counts := map[string]int{}
	for _, hit := range hits {
		if hit.Score < topThreshold && !isExactSymbolMatch(hit) {
			continue
		}
		svc := hitService(hit.File)
		if svc != "" {
			counts[svc]++
		}
	}
	best := ""
	bestN := 0
	for svc, n := range counts {
		if n > bestN {
			bestN = n
			best = svc
		}
	}
	return best
}

// isExactSymbolMatch checks whether the hit is from an exact symbol resolve
// (high-confidence graph lookup, not fuzzy matching).
func isExactSymbolMatch(hit tools.Hit) bool {
	return strings.Contains(strings.ToLower(hit.Why), "exact symbol resolve")
}

// hitService extracts the service name from a file path.
// E.g., "services/sync-service/internal/..." → "sync-service".

func hitService(file string) string {
	path := filepath.ToSlash(file)
	if !strings.HasPrefix(path, "services/") {
		return ""
	}
	// Skip "services/" prefix
	rest := strings.TrimPrefix(path, "services/")
	// Extract first path segment (service name)
	if idx := strings.Index(rest, "/"); idx > 0 {
		return rest[:idx]
	}
	return rest
}

func corroborationCount(hits []tools.Hit, target tools.Hit, targetIdx int) int {
	count := 0
	targetFile := filepath.ToSlash(strings.ToLower(strings.TrimSpace(target.File)))
	targetSymbol := strings.ToLower(strings.TrimSpace(target.Symbol))
	targetFamily := strings.TrimSpace(target.Family)
	targetLane := strings.TrimSpace(target.Lane)
	for i, hit := range hits {
		if i == targetIdx {
			continue
		}
		sameFile := targetFile != "" && filepath.ToSlash(strings.ToLower(strings.TrimSpace(hit.File))) == targetFile
		sameSymbol := targetSymbol != "" && strings.EqualFold(strings.TrimSpace(hit.Symbol), targetSymbol)
		if !sameFile && !sameSymbol {
			continue
		}
		if strings.TrimSpace(hit.Family) == targetFamily && strings.TrimSpace(hit.Lane) == targetLane {
			continue
		}
		count++
	}
	return count
}

func independentEvidenceCount(hits []tools.Hit, target tools.Hit, targetIdx int) int {
	count := 0
	targetFile := filepath.ToSlash(strings.ToLower(strings.TrimSpace(target.File)))
	targetLane := strings.TrimSpace(target.Lane)
	targetFamily := strings.TrimSpace(target.Family)
	targetType := strings.TrimSpace(target.EvidenceType)
	for i, hit := range hits {
		if i == targetIdx {
			continue
		}
		if filepath.ToSlash(strings.ToLower(strings.TrimSpace(hit.File))) == targetFile {
			continue
		}
		if targetLane != "" && strings.TrimSpace(hit.Lane) == targetLane && targetFamily != "" && strings.TrimSpace(hit.Family) == targetFamily {
			continue
		}
		if targetType != "" && strings.TrimSpace(hit.EvidenceType) == targetType && targetLane != "" && strings.TrimSpace(hit.Lane) == targetLane {
			continue
		}
		if strings.TrimSpace(hit.Confidence) == "low" && hit.Score < 75 {
			continue
		}
		count++
	}
	return count
}

func (e *Explorer) coverageSatisfied(plan planner.Plan, hits []tools.Hit) bool {
	if len(plan.Slots) != 0 {
		need := 0
		have := 0
		usedFiles := map[string]bool{}
		for _, slot := range plan.Slots {
			if slot.Weight >= 2 {
				need++
			}
			best := e.bestSlotEvidence(slot, hits)
			if best == nil {
				continue
			}
			if slot.Weight >= 2 && strings.TrimSpace(best.Confidence) == "low" {
				continue
			}
			fileKey := filepath.ToSlash(strings.ToLower(strings.TrimSpace(best.File)))
			if plan.Intent == "mixed" && fileKey != "" && usedFiles[fileKey] && slot.Weight >= 2 {
				continue
			}
			if slot.Weight >= 2 {
				have++
				if fileKey != "" {
					usedFiles[fileKey] = true
				}
			}
		}
		if need > 0 {
			return have >= need
		}
	}
	if len(plan.Subqueries) <= 1 {
		return countProductionish(hits) > 0 || len(hits) > 0
	}
	covered := 0
	for _, sub := range plan.Subqueries {
		if e.subqueryCovered(sub, hits) {
			covered++
		}
	}
	return covered >= max(1, len(plan.Subqueries)-1)
}

func (e *Explorer) bestSlotEvidence(slot planner.EvidenceSlot, hits []tools.Hit) *tools.Hit {
	candidates := filterProductionish(hits, slot.Need)
	if len(candidates) == 0 {
		candidates = hits
	}
	bestIdx := -1
	bestScore := 0
	for i := range candidates {
		if slot.Role == "config" && !looksLikeConfigBlock(candidates[i]) {
			continue
		}
		score := e.slotMatchScore(slot, candidates[i])
		if score >= 4 && (candidates[i].Confidence == "medium" || candidates[i].Confidence == "high") {
			score += 4
		}
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return nil
	}
	return &candidates[bestIdx]
}
