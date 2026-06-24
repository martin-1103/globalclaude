package explorer

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"agent-explorer/internal/learning"
	"agent-explorer/internal/planner"
	"agent-explorer/internal/tools"
)

func pathOverlapScore(path string, terms []string) int {
	score := 0
	segments := pathSegments(path)
	for _, term := range terms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" || len(term) < 3 {
			continue
		}
		for _, segment := range segments {
			if segment == term {
				score += 3
				continue
			}
			if strings.Contains(segment, term) || strings.Contains(term, segment) {
				score += 2
			}
		}
	}
	return score
}

func pathSpecificityScore(path string) int {
	depth := len(pathSegments(path))
	switch {
	case depth >= 5:
		return 8
	case depth >= 3:
		return 4
	default:
		return 0
	}
}

func pathSegments(path string) []string {
	raw := strings.FieldsFunc(strings.ToLower(filepath.ToSlash(path)), func(r rune) bool {
		return r == '/' || r == '_' || r == '-' || r == '.'
	})
	out := make([]string, 0, len(raw))
	for _, token := range raw {
		token = strings.TrimSpace(token)
		if len(token) < 2 || scoringStopword(token) {
			continue
		}
		out = append(out, token)
	}
	return out
}

func (e *Explorer) computeAnchors(ctx context.Context, repo string, plan planner.Plan) []tools.PathAnchor {
	queries := e.effectiveQueries(plan)
	return e.computeAnchorsForQueries(ctx, repo, queries)
}

func (e *Explorer) computeAnchorsForQueries(ctx context.Context, repo string, queries []string) []tools.PathAnchor {
	if len(queries) == 0 {
		return nil
	}
	type anchorMeta struct {
		score      int
		family     string
		why        string
		familySeen map[string]int
	}
	scoreByDir := map[string]*anchorMeta{}
	addHits := func(hits []tools.Hit, base int, family string, why string) {
		for _, hit := range filterProductionish(hits, "") {
			dir := anchorDir(repo, hit.File)
			if dir == "" || !anchorEligiblePath(dir) {
				continue
			}
			meta := scoreByDir[dir]
			if meta == nil {
				meta = &anchorMeta{familySeen: map[string]int{}}
				scoreByDir[dir] = meta
			}
			meta.score += base + max(hit.Score/20, 0)
			meta.familySeen[family] += base
			if meta.family == "" || meta.familySeen[family] > meta.familySeen[meta.family] {
				meta.family = family
				meta.why = why
			}
			if family == "rg" || family == "graph_text" {
				meta.score += 4
			}
			parent := filepath.ToSlash(filepath.Dir(dir))
			if parent != "." && parent != "" && parent != dir {
				parentMeta := scoreByDir[parent]
				if parentMeta == nil {
					parentMeta = &anchorMeta{familySeen: map[string]int{}}
					scoreByDir[parent] = parentMeta
				}
				parentMeta.score += max(base/3, 1)
				parentMeta.familySeen[family] += max(base/3, 1)
				if parentMeta.family == "" || parentMeta.familySeen[family] > parentMeta.familySeen[parentMeta.family] {
					parentMeta.family = family
					parentMeta.why = why
				}
			}
		}
	}
	for _, subquery := range queries {
		if strings.TrimSpace(subquery) == "" {
			continue
		}
		variants := e.queryVariants(subquery)
		if len(variants) == 0 {
			variants = []string{subquery}
		}
		for _, variant := range variants {
			if hits, err := tools.SearchCode(ctx, repo, e.cfg.CBMBinary, e.cfg.CBMCacheDir, variant, e.cfg.ToolTimeoutSeconds, 4); err == nil {
				e.annotateHits(variant, hits)
				addHits(hits, 8, "graph_text", "graph_text anchor")
			}
			if hits, err := tools.SearchGraph(ctx, repo, e.cfg.CBMBinary, e.cfg.CBMCacheDir, variant, e.cfg.ToolTimeoutSeconds, 4); err == nil {
				e.annotateHits(variant, hits)
				addHits(hits, 6, "graph", "graph anchor")
			}
		}
		for _, seed := range seedPhrases(subquery) {
			if !literalAnchorSeed(seed) {
				continue
			}
			if hits, err := tools.RGCode(ctx, repo, seed, e.cfg.ToolTimeoutSeconds, 3); err == nil {
				e.annotateHits(seed, hits)
				addHits(hits, 5, "rg", "literal anchor")
			}
		}
		// Semantic runs concurrently with CBM — independent tool, no shared binary
		// Uses context.WithoutCancel to avoid cascading parent deadline.
		var mu sync.Mutex
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			detachedCtx := context.WithoutCancel(ctx)
			if hits, err := tools.SemanticSearch(detachedCtx, repo, e.cfg.ClaudeContextCmd, subquery, e.cfg.ToolTimeoutSeconds, 2); err == nil {
				e.annotateHits(subquery, hits)
				mu.Lock()
				addHits(filterProductionish(hits, subquery), 2, "semantic", "semantic anchor")
				mu.Unlock()
			}
		}()
		// Fallback: if still no anchors, retry semantic synchronously
		if len(scoreByDir) == 0 {
			wg.Wait()
			if len(scoreByDir) == 0 {
				if hits, err := tools.SemanticSearch(ctx, repo, e.cfg.ClaudeContextCmd, subquery, e.cfg.ToolTimeoutSeconds, 2); err == nil {
					e.annotateHits(subquery, hits)
					addHits(filterProductionish(hits, subquery), 2, "semantic", "semantic fallback anchor")
				}
			}
		} else {
			// CBM found anchors — let semantic finish in background
			go func() { wg.Wait() }()
		}
	}
	if len(scoreByDir) == 0 {
		for _, subquery := range queries {
			if strings.TrimSpace(subquery) == "" {
				continue
			}
			if hits, err := tools.SemanticSearch(ctx, repo, e.cfg.ClaudeContextCmd, subquery, e.cfg.ToolTimeoutSeconds, 2); err == nil {
				e.annotateHits(subquery, hits)
				addHits(filterProductionish(hits, subquery), 2, "semantic", "semantic fallback anchor")
			}
		}
	}
	anchors := make([]tools.PathAnchor, 0, len(scoreByDir))
	for path, meta := range scoreByDir {
		if meta == nil || meta.score <= 0 {
			continue
		}
		meta.score += e.memoryPathBias(strings.ToLower(filepath.ToSlash(path)))
		anchors = append(anchors, tools.PathAnchor{Path: path, Score: meta.score, Family: meta.family, Why: meta.why})
	}
	for _, path := range learning.TopTopicPaths(e.memory, strings.Join(queries, " "), 2) {
		found := false
		for i := range anchors {
			if strings.EqualFold(filepath.ToSlash(anchors[i].Path), filepath.ToSlash(path)) {
				anchors[i].Score += 20
				found = true
				break
			}
		}
		if !found && anchorEligiblePath(path) {
			anchors = append(anchors, tools.PathAnchor{Path: path, Score: 20, Family: "memory", Why: "accepted memory"})
		}
	}
	sort.SliceStable(anchors, func(i, j int) bool {
		if anchors[i].Score != anchors[j].Score {
			return anchors[i].Score > anchors[j].Score
		}
		return anchors[i].Path < anchors[j].Path
	})
	if len(anchors) > 6 {
		anchors = anchors[:6]
	}
	return anchors
}

func literalAnchorSeed(seed string) bool {
	seed = strings.TrimSpace(strings.ToLower(seed))
	if seed == "" {
		return false
	}
	if strings.Contains(seed, " ") {
		return true
	}
	return strings.Contains(seed, "_") || strings.Contains(seed, "-")
}

func seedPhrases(query string) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	replacer := strings.NewReplacer(",", " ", ".", " ", ";", " ", ":", " ", "(", " ", ")", " ")
	tokens := strings.Fields(replacer.Replace(query))
	stop := map[string]bool{
		"how": true, "where": true, "what": true, "when": true, "which": true, "why": true,
		"and": true, "the": true, "are": true, "is": true, "was": true, "were": true,
		"find": true, "show": true, "trace": true, "logic": true, "code": true, "file": true,
	}
	core := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if len(token) < 4 || stop[token] || !seedTokenOK(token) {
			continue
		}
		core = append(core, token)
	}
	if len(core) == 0 {
		return []string{query}
	}
	out := []string{}
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(core) >= 2 {
		for i := 0; i < len(core)-1; i++ {
			add(core[i] + " " + core[i+1])
		}
	}
	if len(core) >= 3 {
		add(strings.Join(core[:3], " "))
		add(strings.Join(core[len(core)-3:], " "))
	}
	add(core[0])
	if len(core) > 1 {
		add(core[1])
	}
	if len(out) > 5 {
		return out[:5]
	}
	return out
}

func seedTokenOK(token string) bool {
	hasLetter := false
	for _, r := range token {
		if r >= 'a' && r <= 'z' {
			hasLetter = true
			continue
		}
		if r >= '0' && r <= '9' {
			return false
		}
		if r != '_' && r != '-' {
			return false
		}
	}
	return hasLetter
}

func planTerms(plan planner.Plan) []string {
	if len(plan.SearchTerms) != 0 {
		return plan.SearchTerms
	}
	if len(plan.SymbolHints) != 0 {
		return plan.SymbolHints
	}
	return []string{plan.Intent}
}

func semanticTerm(plan planner.Plan) string {
	terms := planTerms(plan)
	if len(terms) == 0 {
		return plan.Intent
	}
	return terms[0]
}

func (e *Explorer) effectiveQueries(plan planner.Plan) []string {
	base := []string{}
	base = append(base, plan.SymbolHints...)
	base = append(base, plan.SearchTerms...)
	if len(plan.Slots) != 0 {
		var out []string
		seen := map[string]bool{}
		for _, part := range base {
			for _, variant := range e.queryVariants(part) {
				if seen[variant] {
					continue
				}
				seen[variant] = true
				out = append(out, variant)
			}
		}
		for _, slot := range plan.Slots {
			parts := []string{slot.Need}
			parts = append(parts, slot.Hints...)
			for _, part := range parts {
				for _, variant := range e.queryVariants(part) {
					if seen[variant] {
						continue
					}
					seen[variant] = true
					out = append(out, variant)
				}
			}
		}
		if len(out) != 0 {
			return out
		}
	}
	if len(plan.Subqueries) != 0 {
		var out []string
		seen := map[string]bool{}
		for _, part := range base {
			for _, variant := range e.queryVariants(part) {
				if seen[variant] {
					continue
				}
				seen[variant] = true
				out = append(out, variant)
			}
		}
		for _, sub := range plan.Subqueries {
			for _, variant := range e.queryVariants(sub) {
				if seen[variant] {
					continue
				}
				seen[variant] = true
				out = append(out, variant)
			}
		}
		return out
	}
	var out []string
	seen := map[string]bool{}
	for _, term := range append(base, planTerms(plan)...) {
		for _, variant := range e.queryVariants(term) {
			if seen[variant] {
				continue
			}
			seen[variant] = true
			out = append(out, variant)
		}
	}
	return out
}

func hitMatchesAnchors(repo string, hit tools.Hit, anchors []tools.PathAnchor) bool {
	rel := anchorRel(repo, hit.File)
	for _, anchor := range anchors {
		anchorPath := filepath.ToSlash(strings.TrimSpace(anchor.Path))
		if anchorPath == "" {
			continue
		}
		if rel == anchorPath || strings.HasPrefix(rel, anchorPath+"/") {
			return true
		}
	}
	return false
}

func boostAnchored(repo string, hits []tools.Hit, anchors []tools.PathAnchor) {
	for i := range hits {
		if hitMatchesAnchors(repo, hits[i], anchors) {
			hits[i].Score += 12
		}
	}
}

func anchorDir(repo string, file string) string {
	rel := anchorRel(repo, file)
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." || dir == "" {
		return rel
	}
	return dir
}

func anchorRel(repo string, file string) string {
	rel := file
	if relative, err := filepath.Rel(repo, file); err == nil {
		rel = relative
	}
	return filepath.ToSlash(rel)
}

func anchorEligiblePath(path string) bool {
	path = strings.ToLower(filepath.ToSlash(path))
	if path == "" {
		return false
	}
	if strings.Contains(path, "/_archived/") || strings.Contains(path, "/archived/") || strings.Contains(path, "/lib/site-packages/") || strings.Contains(path, "/site-packages/") || strings.Contains(path, "/vendor/") {
		return false
	}
	if strings.HasPrefix(path, "scripts/") || strings.HasPrefix(path, "reports/") || strings.HasPrefix(path, "docs/") {
		return false
	}
	return strings.Contains(path, "services/") || strings.Contains(path, "/internal/") || strings.Contains(path, "/pkg/") || strings.Contains(path, "/cmd/") || strings.Contains(path, "/src/") || strings.HasPrefix(path, "pkg/") || strings.HasPrefix(path, "cmd/") || strings.HasPrefix(path, "internal/")
}
