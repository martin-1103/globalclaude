package explorer

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"agent-explorer/internal/planner"
	"agent-explorer/internal/tools"
)

func (e *Explorer) rerankWithCritic(ctx context.Context, query string, plan planner.Plan, hits []tools.Hit) []tools.Hit {
	if e.llm == nil || len(hits) < 2 {
		return hits
	}
	if plan.Intent == "literal" || plan.Intent == "definition" {
		return hits
	}
	for _, hit := range hits {
		if strings.Contains(strings.ToLower(hit.Why), "exact symbol resolve") {
			return hits
		}
	}
	if len(plan.SymbolHints) != 0 && plan.PrimaryTool == "graph" {
		return hits
	}
	limit := min(len(hits), 6)
	candidates := hits[:limit]
	type candidate struct {
		ID         string `json:"id"`
		File       string `json:"file"`
		Symbol     string `json:"symbol,omitempty"`
		Why        string `json:"why,omitempty"`
		Score      int    `json:"score"`
		Confidence string `json:"confidence,omitempty"`
	}
	payload := make([]candidate, 0, len(candidates))
	for i, hit := range candidates {
		payload = append(payload, candidate{
			ID:         fmt.Sprintf("H%d", i+1),
			File:       hit.File,
			Symbol:     hit.Symbol,
			Why:        hit.Why,
			Score:      hit.Score,
			Confidence: hit.Confidence,
		})
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return hits
	}
	systemPrompt := `You rerank code exploration candidates.
Goal: choose evidence that best answers query, not generic similar code.
Prefer candidates that:
- match core entity/object of query
- align with requested behavior/path/consumer/reconcile/etc
- look like implementation, not nearby utility
- reduce false positive drift across unrelated subsystems

Return strict JSON only:
{"ranked_ids":["H2","H1"],"reason":"short"}`
	userPrompt := fmt.Sprintf("Query: %s\nIntent: %s\nSlots: %v\nCandidates JSON: %s", query, plan.Intent, plan.Slots, string(body))
	reply, err := e.llm.Chat(ctx, systemPrompt, userPrompt)
	if err != nil {
		return hits
	}
	var parsed struct {
		RankedIDs []string `json:"ranked_ids"`
		Reason    string   `json:"reason"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(reply)), &parsed); err != nil || len(parsed.RankedIDs) == 0 {
		return hits
	}
	byID := map[string]tools.Hit{}
	for i, hit := range candidates {
		byID[fmt.Sprintf("H%d", i+1)] = hit
	}
	reranked := make([]tools.Hit, 0, len(hits))
	seen := map[string]bool{}
	for _, id := range parsed.RankedIDs {
		hit, ok := byID[strings.TrimSpace(id)]
		if !ok {
			continue
		}
		key := dedupeKey(hit)
		if seen[key] {
			continue
		}
		seen[key] = true
		reranked = append(reranked, hit)
	}
	for _, hit := range hits {
		key := dedupeKey(hit)
		if seen[key] {
			continue
		}
		reranked = append(reranked, hit)
	}
	return reranked
}

func filterHitsByPathRegex(repo string, hits []tools.Hit, pathFilter string) []tools.Hit {
	pathFilter = strings.TrimSpace(pathFilter)
	if pathFilter == "" || len(hits) == 0 {
		return hits
	}
	re, err := regexp.Compile(pathFilter)
	if err != nil {
		return hits
	}
	filtered := make([]tools.Hit, 0, len(hits))
	for _, hit := range hits {
		rel := anchorRel(repo, hit.File)
		if re.MatchString(rel) {
			filtered = append(filtered, hit)
		}
	}
	return filtered
}

func (e *Explorer) enrichHits(query string, hits []tools.Hit) []tools.Hit {
	for i := range hits {
		if hits[i].LineStart <= 0 {
			hits[i].LineStart = 1
		}
		if hits[i].LineEnd < hits[i].LineStart {
			hits[i].LineEnd = hits[i].LineStart
		}
		if hits[i].Snippet == "" {
			snippet, start, end, err := tools.ReadSnippet(hits[i].File, hits[i].LineStart, e.cfg.MaxSnippetLines)
			if err == nil {
				hits[i].Snippet = strings.TrimSpace(snippet)
				hits[i].LineStart = start
				hits[i].LineEnd = end
			}
		}
		hits[i].Score = e.rankHit(query, hits[i])
		hits[i].Confidence = confidenceBand(hits[i].Score)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		iScore := hits[i].Score
		jScore := hits[j].Score
		if iScore != jScore {
			return iScore > jScore
		}
		if hits[i].File == hits[j].File {
			return hits[i].LineStart < hits[j].LineStart
		}
		return hits[i].File < hits[j].File
	})
	return hits
}

func (e *Explorer) ensureSubqueryCoverage(plan planner.Plan, hits []tools.Hit) []tools.Hit {
	if len(plan.Subqueries) <= 1 || len(hits) <= 1 {
		return hits
	}
	selected := make([]tools.Hit, 0, len(plan.Subqueries))
	seen := map[string]bool{}
	for _, sub := range plan.Subqueries {
		bestIdx := -1
		bestScore := -1
		for i, hit := range hits {
			text := strings.ToLower(hit.File + " " + hit.Symbol + " " + hit.Snippet + " " + hit.Why)
			if e.conceptOverlap(sub, text) == 0 {
				continue
			}
			subScore := e.rankHit(sub, hit) + e.conceptOverlap(sub, text)*20
			if subScore > bestScore {
				bestScore = subScore
				bestIdx = i
			}
		}
		if bestIdx >= 0 {
			key := dedupeKey(hits[bestIdx])
			if !seen[key] {
				seen[key] = true
				selected = append(selected, hits[bestIdx])
			}
		}
	}
	if len(selected) == 0 {
		return hits
	}
	out := make([]tools.Hit, 0, len(hits))
	out = append(out, selected...)
	for _, hit := range hits {
		key := dedupeKey(hit)
		if seen[key] {
			continue
		}
		out = append(out, hit)
	}
	return out
}

func (e *Explorer) ensureSlotCoverage(plan planner.Plan, hits []tools.Hit) []tools.Hit {
	if len(plan.Slots) <= 1 || len(hits) <= 1 {
		return hits
	}
	selected := make([]tools.Hit, 0, len(plan.Slots))
	seenKey := map[string]bool{}
	seenFile := map[string]bool{}
	for _, slot := range plan.Slots {
		bestIdx := -1
		bestScore := 0
		for i, hit := range hits {
			score := e.slotMatchScore(slot, hit)
			if score <= 0 {
				continue
			}
			if plan.Intent == "mixed" && strings.TrimSpace(hit.SupportRole) != "" && strings.TrimSpace(hit.SupportRole) != slot.Role {
				score -= 8
			}
			if seenFile[hit.File] {
				score -= 3
			}
			if score > bestScore {
				bestScore = score
				bestIdx = i
			}
		}
		if bestIdx < 0 {
			continue
		}
		key := dedupeKey(hits[bestIdx])
		if seenKey[key] {
			continue
		}
		seenKey[key] = true
		seenFile[hits[bestIdx].File] = true
		selected = append(selected, hits[bestIdx])
	}
	if len(selected) == 0 {
		return hits
	}
	out := make([]tools.Hit, 0, len(hits))
	out = append(out, selected...)
	for _, hit := range hits {
		key := dedupeKey(hit)
		if seenKey[key] {
			continue
		}
		out = append(out, hit)
	}
	return out
}

func filterProductionish(hits []tools.Hit, query string) []tools.Hit {
	out := make([]tools.Hit, 0, len(hits))
	lq := strings.ToLower(query)
	allowTests := strings.Contains(lq, "test") || strings.Contains(lq, "e2e") || strings.Contains(lq, "harness")
	allowInfra := strings.Contains(lq, "docker") || strings.Contains(lq, "compose") || strings.Contains(lq, "mysql") || strings.Contains(lq, "infra") || strings.Contains(lq, "nginx") || strings.Contains(lq, "helm")
	allowShell := strings.Contains(lq, "shell") || strings.Contains(lq, "script") || strings.Contains(lq, ".sh")
	allowModules := strings.Contains(lq, "module") || strings.Contains(lq, "dependency") || strings.Contains(lq, "go.work") || strings.Contains(lq, "sum")
	allowProto := strings.Contains(lq, "proto") || strings.Contains(lq, "protobuf") || strings.Contains(lq, "grpc")
	allowSQL := strings.Contains(lq, "sql") || strings.Contains(lq, "migration") || strings.Contains(lq, "ddl") || strings.Contains(lq, "query")
	allowDocs := strings.Contains(lq, "docs") || strings.Contains(lq, "document") || strings.Contains(lq, "workflow") || strings.Contains(lq, "runbook") || strings.Contains(lq, "report")
	for _, hit := range hits {
		path := strings.ToLower(filepath.ToSlash(hit.File))
		base := strings.ToLower(filepath.Base(path))
		symbol := strings.ToLower(hit.Symbol)
		if !allowTests {
			if strings.Contains(path, "/tests/") || strings.Contains(path, "/test/") || strings.Contains(path, "/test-py/") || strings.Contains(path, "/gass_test/") || strings.Contains(base, "_test.") || strings.HasPrefix(base, "test_") || strings.HasPrefix(symbol, "test") || strings.Contains(symbol, ".test") || strings.Contains(path, "run_tests") || strings.Contains(path, "e2e-harness") || strings.Contains(path, "/scenario/") || strings.Contains(path, "/scripts/") || strings.Contains(path, "deprecated") || strings.Contains(path, "/_archived/") || strings.Contains(path, "/archived/") || strings.Contains(path, "/lib/site-packages/") || strings.Contains(path, "/site-packages/") || strings.Contains(path, "/vendor/") || strings.Contains(base, "verify_") {
				continue
			}
		}
		if strings.HasPrefix(base, "fix_") || strings.HasPrefix(base, "tmp") || strings.HasPrefix(base, "scratch") || strings.HasPrefix(base, "debug_") {
			continue
		}
		if strings.HasSuffix(base, ".old") {
			continue
		}
		if rootArtifactPath(path) {
			continue
		}
		if !allowShell && (strings.HasSuffix(path, ".sh") || strings.Contains(path, "/scripts/")) {
			continue
		}
		if !allowModules && (strings.HasSuffix(path, ".sum") || strings.HasSuffix(path, ".lock")) {
			continue
		}
		if !allowProto && (strings.HasSuffix(path, ".proto") || strings.Contains(path, ".pb.go")) {
			continue
		}
		if !allowSQL && strings.HasSuffix(path, ".sql") {
			continue
		}
		if !allowInfra && (strings.HasPrefix(path, "compose/") || strings.HasPrefix(path, "deploy/") || strings.HasPrefix(path, "helm/") || strings.HasSuffix(path, ".yml") || strings.HasSuffix(path, ".yaml")) {
			continue
		}
		if strings.HasSuffix(path, ".html") {
			continue
		}
		if !hasAny(lq, "web", "frontend", "ui", "static", "javascript", "react", "tsx") && (strings.HasPrefix(path, "resource/") || strings.Contains(path, "/resource/") || strings.Contains(path, "/web/") || strings.Contains(path, "/src/components/")) {
			continue
		}
		if !allowDocs && (strings.Contains(path, "/project-docs/") || strings.Contains(path, "/workflow") || strings.Contains(path, "/workflows/") || strings.Contains(path, "/reports/")) {
			continue
		}
		if strings.Contains(path, "/docs/") || strings.HasSuffix(path, ".md") {
			continue
		}
		out = append(out, hit)
	}
	return out
}

func countProductionish(hits []tools.Hit) int {
	return len(filterProductionish(hits, ""))
}

func rootArtifactPath(path string) bool {
	path = strings.ToLower(filepath.ToSlash(path))
	if strings.Count(path, "/") <= 4 {
		base := filepath.Base(path)
		if strings.HasPrefix(base, "review-") || strings.HasPrefix(base, "research") || strings.HasPrefix(base, "scout-") || strings.HasPrefix(base, "ops-context") || strings.HasPrefix(base, "visitor-repair-trace") {
			return true
		}
	}
	return false
}

func filterRoleMisaligned(query string, hits []tools.Hit) []tools.Hit {
	roles := inferredRolesFromQuery(query)
	if len(roles) == 0 {
		return hits
	}
	filtered := make([]tools.Hit, 0, len(hits))
	for _, hit := range hits {
		text := strings.ToLower(hit.File + " " + hit.Symbol + " " + hit.Snippet + " " + hit.Why)
		matched := false
		for _, role := range roles {
			if roleHitScore(role, text, strings.ToLower(hit.File)) > 0 {
				matched = true
				break
			}
		}
		if matched {
			filtered = append(filtered, hit)
		}
	}
	if len(filtered) == 0 {
		return hits
	}
	return filtered
}

func (e *Explorer) filterSlotMisaligned(plan planner.Plan, hits []tools.Hit) []tools.Hit {
	if len(plan.Slots) == 0 || len(hits) == 0 {
		return hits
	}
	filtered := make([]tools.Hit, 0, len(hits))
	for _, hit := range hits {
		best := 0
		for _, slot := range plan.Slots {
			score := e.slotMatchScore(slot, hit)
			if score > best {
				best = score
			}
		}
		if best > 0 {
			filtered = append(filtered, hit)
		}
	}
	if len(filtered) == 0 {
		return hits
	}
	return filtered
}
