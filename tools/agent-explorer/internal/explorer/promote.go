package explorer

import (
	"context"
	"strings"
	"agent-explorer/internal/planner"
	"agent-explorer/internal/tools"
	"path/filepath"
	"sort"
)

func promoteGroundedLiteralEvidence(plan planner.Plan, hits []tools.Hit) []tools.Hit {
	if len(hits) == 0 {
		return hits
	}
	preferLiteral := plan.Intent == "literal" || plan.Intent == "definition" || plan.PrimaryTool == "rg"
	if !preferLiteral {
		for _, slot := range plan.Slots {
			if slot.Role == "config" || slot.Role == "core" {
				preferLiteral = true
				break
			}
		}
	}
	if !preferLiteral {
		return hits
	}
	for i := range hits {
		if strings.TrimSpace(hits[i].Family) != "rg" {
			continue
		}
		path := strings.ToLower(filepath.ToSlash(hits[i].File))
		snippet := strings.ToLower(strings.TrimSpace(hits[i].Snippet))
		if hits[i].Score >= 60 {
			hits[i].EvidenceType = "primary"
		}
		if hasConfigRole(plan) {
			if (strings.Contains(path, "/server/") || strings.Contains(path, "/http")) && hasAny(snippet, "readheadertimeout", "writetimeout", "readtimeout", "timeout:") && !strings.Contains(snippet, "redis") {
				hits[i].EvidenceType = "primary"
			}
			if strings.Contains(snippet, "redis") && !hasAny(snippet, "http.server", "http.client") {
				hits[i].EvidenceType = "supporting"
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		pi := hits[i].EvidenceType == "primary"
		pj := hits[j].EvidenceType == "primary"
		if pi != pj {
			return pi
		}
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].File == hits[j].File {
			return hits[i].LineStart < hits[j].LineStart
		}
		return hits[i].File < hits[j].File
	})
	return hits
}

func (e *Explorer) needsLexicalRescue(plan planner.Plan, hits []tools.Hit) bool {
	if plan.PrimaryTool != "rg" {
		return false
	}
	if len(hits) == 0 {
		return true
	}
	if hasConfidenceAtLeast(hits, "medium") {
		return false
	}
	if plan.Intent == "definition" || plan.Intent == "literal" {
		return true
	}
	for _, slot := range plan.Slots {
		if slot.Role == "config" || slot.Role == "core" {
			return true
		}
	}
	return false
}

func (e *Explorer) lexicalRescue(ctx context.Context, repo string, query string, plan planner.Plan) []tools.Hit {
	terms := rescueTerms(plan)
	if len(terms) == 0 {
		return nil
	}
	var hits []tools.Hit
	for _, term := range terms {
		result, err := tools.RGCode(ctx, repo, term, e.cfg.ToolTimeoutSeconds, max(4, e.cfg.MaxSearchResults))
		if err != nil {
			continue
		}
		for i := range result {
			result[i].Lane = "rg"
			result[i].Family = "rg"
			result[i].Why = "literal rescue match"
		}
		hits = append(hits, result...)
	}
	if len(hits) == 0 {
		return nil
	}
	hits = filterIgnoredHits(repo, hits)
	hits = filterProductionish(hits, query)
	hits = e.dedupeHits(query, hits)
	hits = e.enrichHits(query, hits)
	out := make([]tools.Hit, 0, len(hits))
	for _, hit := range hits {
		if hit.Score > 20 {
			out = append(out, hit)
		}
	}
	return limitHits(out, min(e.cfg.MaxSearchResults, 8))
}

func rescueTerms(plan planner.Plan) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 8)
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			return
		}
		lv := strings.ToLower(v)
		if len(v) < 4 {
			return
		}
		if strings.Contains(v, " ") && !strings.ContainsAny(v, ":._") {
			return
		}
		if scoringStopword(lv) {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	for _, item := range plan.SymbolHints {
		add(item)
	}
	for _, item := range plan.SearchTerms {
		add(item)
	}
	if len(out) > 6 {
		return out[:6]
	}
	return out
}

func wantsTraceFocus(query string, plan planner.Plan) bool {
	if !plan.NeedCallGraph {
		return false
	}
	q := strings.ToLower(strings.TrimSpace(query))
	return hasAny(q, "trace", "caller", "callers", "callee", "callees", "who calls", "what calls", "called by")
}

func promoteTraceEvidence(query string, plan planner.Plan, hits []tools.Hit) []tools.Hit {
	if !wantsTraceFocus(query, plan) || len(hits) == 0 {
		return hits
	}
	for i := range hits {
		if hits[i].EvidenceType == "trace" {
			hits[i].EvidenceType = "primary"
			if hits[i].Score < 82 {
				hits[i].Score += 28
			}
		} else if strings.Contains(strings.ToLower(hits[i].Why), "exact symbol resolve") {
			hits[i].EvidenceType = "supporting"
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		traceI := hits[i].EvidenceType == "primary" && strings.TrimSpace(hits[i].Lane) == "trace"
		traceJ := hits[j].EvidenceType == "primary" && strings.TrimSpace(hits[j].Lane) == "trace"
		if traceI != traceJ {
			return traceI
		}
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].File == hits[j].File {
			return hits[i].LineStart < hits[j].LineStart
		}
		return hits[i].File < hits[j].File
	})
	return hits
}

func exactGroundedHit(plan planner.Plan, hits []tools.Hit) bool {
	for _, hit := range hits {
		exactish := strings.Contains(strings.ToLower(hit.Why), "exact symbol resolve") || strings.TrimSpace(hit.EvidenceType) == "literal"
		if !exactish {
			continue
		}
		if plan.Intent == "definition" || plan.Intent == "literal" {
			return true
		}
	}
	return false
}

func shouldSuppressHit(hit tools.Hit, highMedium int) bool {
	path := strings.ToLower(filepath.ToSlash(hit.File))
	if highMedium >= 2 && hit.Confidence == "low" {
		return true
	}
	if strings.HasSuffix(path, ".sh") || strings.HasSuffix(path, ".sum") || strings.HasSuffix(path, ".lock") {
		return true
	}
	if strings.HasSuffix(path, ".old") || strings.Contains(path, "/project-docs/") || strings.Contains(path, "/workflow") || strings.Contains(path, "/workflows/") {
		return true
	}
	if strings.Contains(path, "/reports/") || strings.HasSuffix(path, ".html") {
		return true
	}
	if strings.HasSuffix(path, ".md") {
		return true
	}
	if rootArtifactPath(path) {
		return true
	}
	if strings.Contains(path, "/resource/") || strings.HasPrefix(path, "resource/") {
		return true
	}
	if highMedium >= 1 && strings.Contains(path, "/konsep/") {
		return true
	}
	if highMedium >= 2 && (strings.HasPrefix(filepath.Base(path), "fix_") || strings.Contains(path, "/web/")) && hit.Confidence != "high" {
		return true
	}
	return false
}

func preferProductionHits(query string, hits []tools.Hit) []tools.Hit {
	prod := filterProductionish(hits, query)
	if len(prod) == 0 {
		return hits
	}
	return prod
}
