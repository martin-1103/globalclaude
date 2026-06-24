package explorer

import (
	"strings"
	"agent-explorer/internal/planner"
	"agent-explorer/internal/tools"
	"fmt"
	"path/filepath"
	"sort"
)

func sortFinalHits(hits []tools.Hit) []tools.Hit {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].Confidence != hits[j].Confidence {
			return confidenceRank(hits[i].Confidence) > confidenceRank(hits[j].Confidence)
		}
		if hits[i].File == hits[j].File {
			return hits[i].LineStart < hits[j].LineStart
		}
		return hits[i].File < hits[j].File
	})
	return hits
}

func confidenceRank(level string) int {
	switch strings.TrimSpace(level) {
	case "high":
		return 3
	case "medium":
		return 2
	default:
		return 1
	}
}

func (e *Explorer) dedupeHits(query string, hits []tools.Hit) []tools.Hit {
	type candidate struct {
		key   string
		hit   tools.Hit
		fused float64
	}
	bestByKey := map[string]candidate{}
	order := []string{}
	for idx, hit := range hits {
		key := dedupeKey(hit)
		current, ok := bestByKey[key]
		fused := 1.0 / float64(60+idx+1)
		if !ok {
			hit.FusionScore = fused
			bestByKey[key] = candidate{key: key, hit: hit, fused: fused}
			order = append(order, key)
			continue
		}
		current.fused += fused
		merged := current.hit
		if e.rankHit(query, hit) > e.rankHit(query, current.hit) {
			merged = hit
		}
		if merged.Lane == "" {
			merged.Lane = current.hit.Lane
		}
		if merged.Family == "" {
			merged.Family = current.hit.Family
		}
		if merged.Why == "" {
			merged.Why = current.hit.Why
		}
		if merged.Why != "" && hit.Why != "" && !strings.Contains(merged.Why, hit.Why) {
			merged.Why += " | " + hit.Why
		}
		if merged.Source == "" {
			merged.Source = current.hit.Source
		}
		merged.FusionScore = current.fused
		bestByKey[key] = candidate{key: key, hit: merged, fused: current.fused}
	}
	out := make([]tools.Hit, 0, len(order))
	for _, key := range order {
		out = append(out, bestByKey[key].hit)
	}
	return out
}

func dedupeKey(hit tools.Hit) string {
	if strings.Contains(hit.Source, "claude-context") {
		return "semantic-file:" + hit.File
	}
	if hit.Symbol != "" {
		return "symbol:" + hit.Symbol
	}
	return fmt.Sprintf("%s:%s:%d:%d", hit.Source, hit.File, hit.LineStart, hit.LineEnd)
}

func limitHits(hits []tools.Hit, limit int) []tools.Hit {
	if len(hits) <= limit {
		return hits
	}
	return hits[:limit]
}

func (e *Explorer) compactHits(query string, hits []tools.Hit, limit int) ([]tools.Hit, []tools.Hit) {
	if len(hits) == 0 {
		return hits, nil
	}
	e.annotateHits(query, hits)
	highMedium := 0
	for _, hit := range hits {
		if hit.Confidence == "high" || hit.Confidence == "medium" {
			highMedium++
		}
	}
	filtered := make([]tools.Hit, 0, len(hits))
	suppressed := make([]tools.Hit, 0, len(hits))
	for _, hit := range hits {
		if shouldSuppressHit(hit, highMedium) {
			suppressed = append(suppressed, hit)
			continue
		}
		filtered = append(filtered, hit)
	}
	if len(filtered) == 0 {
		filtered = hits
	}
	filtered = diversifyHits(filtered, min(limit, 6))
	return limitHits(filtered, min(limit, 5)), limitHits(suppressed, 6)
}

func (e *Explorer) assignEvidenceTypes(plan planner.Plan, hits []tools.Hit) []tools.Hit {
	if len(hits) == 0 {
		return hits
	}
	for i := range hits {
		role := bestSupportRole(plan, hits[i], e)
		hits[i].SupportRole = role
		switch {
		case strings.TrimSpace(hits[i].Lane) == "trace":
			hits[i].EvidenceType = "trace"
		case strings.TrimSpace(hits[i].Family) == "rg":
			hits[i].EvidenceType = "literal"
		case i == 0 || (hits[i].Confidence == "high" && role != ""):
			hits[i].EvidenceType = "primary"
		default:
			hits[i].EvidenceType = "supporting"
		}
	}
	return hits
}

func (e *Explorer) pruneTraceNoise(query string, plan planner.Plan, hits []tools.Hit, suppressed []tools.Hit) ([]tools.Hit, []tools.Hit) {
	if len(hits) == 0 {
		return hits, suppressed
	}
	if wantsTraceFocus(query, plan) {
		return hits, suppressed
	}
	var focus *tools.Hit
	for i := range hits {
		if hits[i].EvidenceType == "primary" && strings.Contains(strings.ToLower(hits[i].Why), "exact symbol resolve") {
			focus = &hits[i]
			break
		}
	}
	if focus == nil {
		return hits, suppressed
	}
	focusSymbol := strings.ToLower(strings.TrimSpace(focus.Symbol))
	focusDir := filepath.ToSlash(filepath.Dir(focus.File))
	filtered := make([]tools.Hit, 0, len(hits))
	for _, hit := range hits {
		if hit.EvidenceType != "trace" {
			filtered = append(filtered, hit)
			continue
		}
		why := strings.ToLower(hit.Why)
		sameDir := filepath.ToSlash(filepath.Dir(hit.File)) == focusDir
		sameSymbolTrace := focusSymbol != "" && strings.Contains(why, focusSymbol)
		roleUseful := traceRoleUseful(hit, filtered)
		if (sameDir || sameSymbolTrace) && roleUseful {
			filtered = append(filtered, hit)
			continue
		}
		suppressed = append(suppressed, hit)
	}
	return filtered, suppressed
}

func traceRoleUseful(hit tools.Hit, kept []tools.Hit) bool {
	role := strings.TrimSpace(hit.SupportRole)
	if role == "" || role == "trace" {
		return false
	}
	for _, existing := range kept {
		if strings.TrimSpace(existing.SupportRole) == role && existing.EvidenceType != "trace" && (existing.Confidence == "medium" || existing.Confidence == "high") {
			return false
		}
	}
	return true
}

func splitEvidencePools(hits []tools.Hit) ([]tools.Hit, []tools.Hit, []tools.Hit) {
	primary := make([]tools.Hit, 0, len(hits))
	supporting := make([]tools.Hit, 0, len(hits))
	trace := make([]tools.Hit, 0, len(hits))
	for _, hit := range hits {
		switch hit.EvidenceType {
		case "trace":
			trace = append(trace, hit)
		case "primary":
			primary = append(primary, hit)
		default:
			supporting = append(supporting, hit)
		}
	}
	return primary, supporting, trace
}

func bestSupportRole(plan planner.Plan, hit tools.Hit, e *Explorer) string {
	bestRole := ""
	bestScore := 0
	for _, slot := range plan.Slots {
		score := e.slotMatchScore(slot, hit)
		if score > bestScore {
			bestScore = score
			bestRole = slot.Role
		}
	}
	if bestRole == "" && hit.EvidenceType == "trace" {
		return "trace"
	}
	return bestRole
}

func containsHit(hits []tools.Hit, target tools.Hit) bool {
	for _, hit := range hits {
		if hit.File == target.File && hit.LineStart == target.LineStart && hit.LineEnd == target.LineEnd && hit.Symbol == target.Symbol {
			return true
		}
	}
	return false
}

func diversifyHits(hits []tools.Hit, limit int) []tools.Hit {
	if len(hits) <= 1 || limit <= 1 {
		return hits
	}
	out := make([]tools.Hit, 0, min(limit, len(hits)))
	seenLane := map[string]bool{}
	seenDir := map[string]bool{}
	seenType := map[string]bool{}
	seenFile := map[string]int{}
	for _, hit := range hits {
		lane := nonEmptyLane(hit)
		dir := filepath.ToSlash(filepath.Dir(hit.File))
		etype := strings.TrimSpace(hit.EvidenceType)
		if seenFile[hit.File] >= 1 && hit.Confidence != "high" {
			continue
		}
		if !seenLane[lane] || !seenDir[dir] || (etype != "" && !seenType[etype]) {
			out = append(out, hit)
			seenLane[lane] = true
			seenDir[dir] = true
			if etype != "" {
				seenType[etype] = true
			}
			seenFile[hit.File]++
			if len(out) >= limit {
				return out
			}
		}
	}
	for _, hit := range hits {
		if containsHit(out, hit) {
			continue
		}
		if seenFile[hit.File] >= 2 && hit.Confidence != "high" {
			continue
		}
		out = append(out, hit)
		seenFile[hit.File]++
		if len(out) >= limit {
			break
		}
	}
	return out
}
