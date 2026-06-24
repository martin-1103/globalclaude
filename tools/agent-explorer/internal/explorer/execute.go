package explorer

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"agent-explorer/internal/planner"
	"agent-explorer/internal/tools"
)

func (e *Explorer) maxToolFamilies() int {
	if e.profile.MaxToolFamilies > 0 {
		return e.profile.MaxToolFamilies
	}
	return 2
}

func (e *Explorer) executePlan(ctx context.Context, repo string, plan planner.Plan, anchors []tools.PathAnchor, slotAnchors map[string][]tools.PathAnchor) ([]tools.Hit, []string) {
	maxFamilies := e.maxToolFamilies()
	query := semanticTerm(plan)
	queries := e.effectiveQueries(plan)
	collected := make([]tools.Hit, 0, e.cfg.MaxSearchResults)
	warnings := []string{}
	warned := map[string]bool{}
	attempted := []string{}
	familiesUsed := 0
	addWarning := func(name string, err error) {
		if err == nil {
			return
		}
		msg := fmt.Sprintf("%s failed: %v", name, err)
		if warned[msg] {
			return
		}
		warned[msg] = true
		warnings = append(warnings, msg)
	}
	appendHits := func(newHits []tools.Hit) {
		collected = append(collected, newHits...)
		collected = e.dedupeHits(query, collected)
		e.annotateHits(query, collected)
	}
	exactSymbolPrepass := func() {
		if len(plan.SymbolHints) == 0 {
			return
		}
		hits := make([]tools.Hit, 0, len(plan.SymbolHints))
		scope := anchorsPathFilter(anchors)
		for _, hint := range plan.SymbolHints {
			hint = strings.TrimSpace(hint)
			if hint == "" {
				continue
			}
			hit, err := tools.ResolveSymbol(ctx, repo, e.cfg.CBMBinary, e.cfg.CBMCacheDir, hint, e.cfg.ToolTimeoutSeconds)
			if err == nil {
				hit.Why = "exact symbol resolve"
				hit.Lane = "graph"
				hit.Family = "graph"
				hits = append(hits, hit)
			}
			if result, err := tools.SearchGraphScoped(ctx, repo, e.cfg.CBMBinary, e.cfg.CBMCacheDir, hint, scope, e.cfg.ToolTimeoutSeconds, 3); err == nil {
				result = filterHitsByPathRegex(repo, result, scope)
				markLane(result, "graph")
				boostAnchored(repo, result, anchors)
				for i := range result {
					if exactSymbolNameMatch(hint, result[i]) {
						result[i].Why = "exact symbol resolve"
					} else if strings.TrimSpace(result[i].Why) == "" {
						result[i].Why = "exact graph hint search"
					}
				}
				hits = append(hits, result...)
			}
			if result, err := tools.SearchCodeScoped(ctx, repo, e.cfg.CBMBinary, e.cfg.CBMCacheDir, hint, scope, e.cfg.ToolTimeoutSeconds, 3); err == nil {
				markLane(result, "graph_text")
				boostAnchored(repo, result, anchors)
				for i := range result {
					if strings.TrimSpace(result[i].Why) == "" {
						result[i].Why = "exact identifier search"
					}
				}
				hits = append(hits, result...)
			}
		}
		if len(hits) != 0 {
			appendHits(hits)
		}
	}
	exactSymbolPrepass()
	var runToolTermsScopedWithAnchors func(name string, terms []string, pathFilter string, boostAnchors []tools.PathAnchor)
	runToolTermsScoped := func(name string, terms []string, pathFilter string) {
		runToolTermsScopedWithAnchors(name, terms, pathFilter, anchors)
	}
	fetchToolTermsScoped := func(name string, terms []string, pathFilter string, boostAnchors []tools.PathAnchor) ([]tools.Hit, error) {
		if name == "" {
			return nil, nil
		}
		var hits []tools.Hit
		var firstErr error
		switch name {
		case "graph":
			hits = parallelPerTerm(ctx, terms, func(term string) ([]tools.Hit, error) {
				result, err := tools.SearchGraphScoped(ctx, repo, e.cfg.CBMBinary, e.cfg.CBMCacheDir, term, pathFilter, e.cfg.ToolTimeoutSeconds, e.cfg.MaxSearchResults)
				result = filterHitsByPathRegex(repo, result, pathFilter)
				markLane(result, "graph")
				boostAnchored(repo, result, boostAnchors)
				return result, err
			}, func(err error) {
				if err != nil && firstErr == nil {
					firstErr = err
				}
			})
		case "graph_text":
			hits = parallelPerTerm(ctx, terms, func(term string) ([]tools.Hit, error) {
				result, err := tools.SearchCodeScoped(ctx, repo, e.cfg.CBMBinary, e.cfg.CBMCacheDir, term, pathFilter, e.cfg.ToolTimeoutSeconds, e.cfg.MaxSearchResults)
				result = filterHitsByPathRegex(repo, result, pathFilter)
				markLane(result, "graph_text")
				boostAnchored(repo, result, boostAnchors)
				return result, err
			}, func(err error) {
				if err != nil && firstErr == nil {
					firstErr = err
				}
			})
		case "semantic":
			hits = parallelPerTerm(ctx, terms, func(term string) ([]tools.Hit, error) {
				result, err := tools.SemanticSearch(ctx, repo, e.cfg.ClaudeContextCmd, term, e.cfg.ToolTimeoutSeconds, e.cfg.MaxSearchResults)
				markLane(result, "semantic")
				boostAnchored(repo, result, boostAnchors)
				return result, err
			}, func(err error) {
				if err != nil && firstErr == nil {
					firstErr = err
				}
			})
		case "rg":
			hits = parallelPerTerm(ctx, terms, func(term string) ([]tools.Hit, error) {
				var result []tools.Hit
				var err error
				if preferCodeOnlyRG(query, term) {
					result, err = tools.RGCode(ctx, repo, term, e.cfg.ToolTimeoutSeconds, e.cfg.MaxSearchResults)
				} else {
					result, err = tools.RG(ctx, repo, term, e.cfg.ToolTimeoutSeconds, e.cfg.MaxSearchResults)
				}
				markLane(result, "rg")
				boostAnchored(repo, result, boostAnchors)
				return result, err
			}, func(err error) {
				if err != nil && firstErr == nil {
					firstErr = err
				}
			})
		case "astgrep":
			result, err := tools.ASTGrep(ctx, repo, plan.ASTPattern, e.cfg.ToolTimeoutSeconds, e.cfg.MaxSearchResults)
			if err != nil {
				firstErr = err
			}
			markLane(result, "astgrep")
			boostAnchored(repo, result, boostAnchors)
			hits = append(hits, result...)
		}
		return hits, firstErr
	}
	runToolTermsScopedWithAnchors = func(name string, terms []string, pathFilter string, boostAnchors []tools.PathAnchor) {
		if name == "" {
			return
		}
		attempted = append(attempted, name)
		familiesUsed++
		hits, err := fetchToolTermsScoped(name, terms, pathFilter, boostAnchors)
		addWarning(name, err)
		appendHits(hits)
	}
	runToolTerms := func(name string, terms []string) {
		runToolTermsScopedWithAnchors(name, terms, "", anchors)
	}
	shouldAvoidScopedRG := func(toolName string, slot planner.EvidenceSlot, pathFilter string) bool {
		if toolName != "rg" || strings.TrimSpace(pathFilter) == "" {
			return false
		}
		if plan.Intent == "literal" || plan.Intent == "definition" {
			return true
		}
		return slot.Role == "config" || slot.Role == "core"
	}
	runTool := func(name string) { runToolTerms(name, queries) }
	runDualLane := func(a string, b string) {
		type toolResult struct {
			name string
			hits []tools.Hit
			err  error
		}
		results := make(chan toolResult, 2)
		runOne := func(name string) {
			var hits []tools.Hit
			var err error
			switch name {
			case "graph_text":
				hits = parallelPerTerm(ctx, queries, func(term string) ([]tools.Hit, error) {
					result, runErr := tools.SearchCode(ctx, repo, e.cfg.CBMBinary, e.cfg.CBMCacheDir, term, e.cfg.ToolTimeoutSeconds, e.cfg.MaxSearchResults)
					markLane(result, "graph_text")
					boostAnchored(repo, result, anchors)
					return result, runErr
				}, func(runErr error) {
					if runErr != nil && err == nil {
						err = runErr
					}
				})
			case "semantic":
				hits = parallelPerTerm(ctx, queries, func(term string) ([]tools.Hit, error) {
					result, runErr := tools.SemanticSearch(ctx, repo, e.cfg.ClaudeContextCmd, term, e.cfg.ToolTimeoutSeconds, e.cfg.MaxSearchResults)
					markLane(result, "semantic")
					boostAnchored(repo, result, anchors)
					return result, runErr
				}, func(runErr error) {
					if runErr != nil && err == nil {
						err = runErr
					}
				})
			default:
				err = fmt.Errorf("unsupported dual-lane tool: %s", name)
			}
			results <- toolResult{name: name, hits: hits, err: err}
		}
		attempted = append(attempted, a, b)
		familiesUsed += 2
		go runOne(a)
		go runOne(b)
		for i := 0; i < 2; i++ {
			result := <-results
			addWarning(result.name, result.err)
			appendHits(result.hits)
		}
	}

	if len(plan.Slots) != 0 {
		if e.cfg.ParallelRetrieval {
			type slotRun struct {
				slot      planner.EvidenceSlot
				toolName  string
				terms     []string
				scope     string
				anchorSet []tools.PathAnchor
			}
			grouped := map[string][]slotRun{}
			familyOrder := []string{}
			seenFamily := map[string]bool{}
			for _, slot := range plan.Slots {
				terms := e.slotQueriesWithPlan(slot, plan)
				toolName := slot.Tool
				anchorSet := anchors
				if scoped := slotAnchors[slotKey(slot)]; len(scoped) != 0 {
					anchorSet = scoped
				}
				scope := e.slotPathFilter(slot, anchorSet)
				if plan.Intent == "definition" || plan.Intent == "literal" {
					toolName = plan.PrimaryTool
				}
				if strings.TrimSpace(toolName) == "" {
					toolName = plan.PrimaryTool
				}
				run := slotRun{slot: slot, toolName: toolName, terms: terms, scope: scope, anchorSet: anchorSet}
				grouped[toolName] = append(grouped[toolName], run)
				if !seenFamily[toolName] {
					seenFamily[toolName] = true
					familyOrder = append(familyOrder, toolName)
				}
			}
			for _, family := range familyOrder {
				if familiesUsed >= maxFamilies {
					break
				}
				runs := grouped[family]
				results := parallelSlotRuns(ctx, runs, func(run slotRun) slotFetchResult {
					pathFilter := run.scope
					if shouldAvoidScopedRG(run.toolName, run.slot, run.scope) {
						pathFilter = ""
					}
					hits, err := fetchToolTermsScoped(run.toolName, run.terms, pathFilter, run.anchorSet)
					return slotFetchResult{slot: run.slot, toolName: run.toolName, terms: run.terms, scope: run.scope, anchorSet: run.anchorSet, hits: hits, err: err}
				})
				attempted = append(attempted, family)
				familiesUsed++
				for _, result := range results {
					addWarning(result.toolName, result.err)
					appendHits(result.hits)
				}
				for _, result := range results {
					if e.slotCovered(result.slot, collected) {
						continue
					}
					backup := e.slotBackupTool(result.slot, plan)
					if backup != "" && backup != result.toolName && familiesUsed < maxFamilies {
						pathFilter := result.scope
						if shouldAvoidScopedRG(backup, result.slot, result.scope) {
							pathFilter = ""
						}
						runToolTermsScopedWithAnchors(backup, result.terms, pathFilter, result.anchorSet)
					}
				}
				if familiesUsed >= maxFamilies && e.coverageSatisfied(plan, collected) && hasConfidenceAtLeast(collected, "medium") {
					return limitHits(collected, e.cfg.MaxSearchResults), warnings
				}
			}
		} else {
		for _, slot := range plan.Slots {
			terms := e.slotQueriesWithPlan(slot, plan)
			toolName := slot.Tool
			anchorSet := anchors
			if scoped := slotAnchors[slotKey(slot)]; len(scoped) != 0 {
				anchorSet = scoped
			}
			scope := e.slotPathFilter(slot, anchorSet)
			if plan.Intent == "definition" || plan.Intent == "literal" {
				toolName = plan.PrimaryTool
			}
			if strings.TrimSpace(toolName) == "" {
				toolName = plan.PrimaryTool
			}
			before := len(collected)
			if shouldAvoidScopedRG(toolName, slot, scope) {
				runToolTerms(toolName, terms)
			} else {
				runToolTermsScoped(toolName, terms, scope)
			}
			if len(collected) == before || !e.slotCovered(slot, collected) {
				backup := e.slotBackupTool(slot, plan)
				if backup != "" && backup != toolName && familiesUsed < maxFamilies {
					runToolTermsScoped(backup, terms, scope)
				}
			}
			if familiesUsed >= maxFamilies && e.coverageSatisfied(plan, collected) && hasConfidenceAtLeast(collected, "medium") {
				return limitHits(collected, e.cfg.MaxSearchResults), warnings
			}
		}
		}
		if len(collected) != 0 && e.coverageSatisfied(plan, collected) && hasConfidenceAtLeast(collected, "medium") {
			return limitHits(collected, e.cfg.MaxSearchResults), warnings
		}
		scope := anchorsPathFilter(anchors)
		if scope != "" {
			if plan.PrimaryTool == "rg" && (plan.Intent == "literal" || plan.Intent == "definition") {
				runToolTerms(plan.PrimaryTool, queries)
			} else {
				runToolTermsScoped(plan.PrimaryTool, queries, scope)
			}
			if len(collected) != 0 && e.coverageSatisfied(plan, collected) && hasConfidenceAtLeast(collected, "medium") {
				return limitHits(collected, e.cfg.MaxSearchResults), warnings
			}
			for _, backup := range plan.BackupTools {
				if familiesUsed >= maxFamilies {
					break
				}
				runToolTermsScoped(backup, queries, scope)
				if len(collected) != 0 && e.coverageSatisfied(plan, collected) && hasConfidenceAtLeast(collected, "medium") {
					return limitHits(collected, e.cfg.MaxSearchResults), warnings
				}
			}
		}
	}

	if e.shouldUseDualLane(plan) && plan.PrimaryTool == "semantic" && containsTool(plan.BackupTools, "graph_text") {
		runDualLane("semantic", "graph_text")
	} else {
		runTool(plan.PrimaryTool)
	}
	if shouldForceGraph(plan, attempted) && !containsTool(attempted, "graph") {
		runTool("graph")
	}
	if e.shouldStop(query, plan, collected, familiesUsed) && e.coverageSatisfied(plan, collected) {
		return limitHits(collected, e.cfg.MaxSearchResults), warnings
	}
	for _, backup := range plan.BackupTools {
		if len(collected) >= e.cfg.MaxSearchResults || familiesUsed >= maxFamilies {
			break
		}
		runTool(backup)
		if e.shouldStop(query, plan, collected, familiesUsed) && e.coverageSatisfied(plan, collected) {
			return limitHits(collected, e.cfg.MaxSearchResults), warnings
		}
	}
	if familiesUsed >= maxFamilies && !hasConfidenceAtLeast(collected, "medium") {
		replan, err := e.planner.Replan(ctx, repo, query, attempted, warnings)
		addWarning("replan", err)
		if err == nil {
			candidate := replan.PrimaryTool
			if containsTool(attempted, candidate) && len(replan.BackupTools) > 0 {
				candidate = replan.BackupTools[0]
			}
			if candidate != "" && !containsTool(attempted, candidate) {
				runTool(candidate)
			} else {
				warnings = append(warnings, fmt.Sprintf("stopped after %d tool families with low confidence; narrow query or use trace", maxFamilies))
			}
		} else {
			warnings = append(warnings, fmt.Sprintf("stopped after %d tool families with low confidence; narrow query or use trace", maxFamilies))
		}
	}
	return limitHits(collected, e.cfg.MaxSearchResults), warnings
}

func (e *Explorer) computeSlotAnchors(ctx context.Context, repo string, plan planner.Plan) map[string][]tools.PathAnchor {
	out := map[string][]tools.PathAnchor{}
	for _, slot := range plan.Slots {
		queries := e.slotQueries(slot)
		anchors := e.computeAnchorsForQueries(ctx, repo, queries)
		if len(anchors) != 0 {
			out[slotKey(slot)] = anchors
		}
	}
	return out
}

func (e *Explorer) slotQueries(slot planner.EvidenceSlot) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		for _, variant := range e.queryVariants(s) {
			if strings.TrimSpace(variant) == "" || seen[variant] {
				continue
			}
			seen[variant] = true
			out = append(out, variant)
		}
	}
	add(slot.Need)
	for _, hint := range slot.Hints {
		add(hint)
	}
	if len(out) == 0 {
		return []string{slot.Need}
	}
	if len(out) > 4 {
		return out[:4]
	}
	return out
}

func (e *Explorer) slotQueriesWithPlan(slot planner.EvidenceSlot, plan planner.Plan) []string {
	terms := e.slotQueries(slot)
	if plan.Intent != "definition" && plan.Intent != "literal" {
		return terms
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(terms)+len(plan.SearchTerms)+len(plan.SymbolHints))
	add := func(s string) {
		for _, variant := range e.queryVariants(s) {
			if strings.TrimSpace(variant) == "" || seen[variant] {
				continue
			}
			seen[variant] = true
			out = append(out, variant)
		}
	}
	for _, item := range plan.SymbolHints {
		add(item)
	}
	for _, item := range plan.SearchTerms {
		add(item)
	}
	for _, item := range terms {
		add(item)
	}
	if len(out) == 0 {
		return terms
	}
	if len(out) > 8 {
		return out[:8]
	}
	return out
}

func (e *Explorer) slotBackupTool(slot planner.EvidenceSlot, plan planner.Plan) string {
	switch slot.Role {
	case "validator", "injector", "consumer", "projection", "reconcile":
		if slot.Tool != "graph" {
			return "graph"
		}
		return "graph_text"
	case "detector":
		if slot.Tool != "graph_text" {
			return "graph_text"
		}
		return "graph"
	case "retry", "tuning":
		if slot.Tool != "graph" {
			return "graph"
		}
		return "graph_text"
	case "config":
		if slot.Tool != "rg" {
			return "rg"
		}
		return "graph_text"
	default:
		for _, backup := range plan.BackupTools {
			if backup != slot.Tool {
				return backup
			}
		}
	}
	return ""
}

func (e *Explorer) slotPathFilter(slot planner.EvidenceSlot, anchors []tools.PathAnchor) string {
	if len(anchors) == 0 {
		return ""
	}
	type candidate struct {
		path  string
		score int
	}
	var ranked []candidate
	slotTerms := e.slotScopeTerms(slot)
	for _, anchor := range anchors {
		path := strings.ToLower(filepath.ToSlash(anchor.Path))
		score := anchor.Score
		family := strings.ToLower(strings.TrimSpace(anchor.Family))
		score += pathOverlapScore(path, slotTerms) * 14
		score += pathSpecificityScore(path)
		score += slotFamilyBonus(slot.Role, family)
		if pathOverlapScore(path, slotTerms) == 0 && score < 12 {
			continue
		}
		ranked = append(ranked, candidate{path: anchor.Path, score: score})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].path < ranked[j].path
	})
	parts := []string{}
	limit := slotScopeLimit(slot.Role)
	if len(ranked) >= 2 && ranked[0].score >= ranked[1].score*2 {
		limit = 1
	}
	cutoff := 0
	if len(ranked) != 0 {
		cutoff = ranked[0].score / 2
	}
	for i, item := range ranked {
		if i >= limit {
			break
		}
		if cutoff > 0 && item.score < cutoff {
			continue
		}
		parts = append(parts, "^"+regexpQuotePath(item.path)+"/")
		parts = append(parts, "^"+regexpQuotePath(item.path)+"$")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "|")
}

func slotScopeLimit(role string) int {
	switch role {
	case "projection", "reconcile", "detector", "retry", "tuning":
		return 1
	case "consumer":
		return 2
	default:
		return 2
	}
}

func slotFamilyBonus(role string, family string) int {
	switch role {
	case "validator", "injector":
		if family == "graph_text" || family == "graph" {
			return 8
		}
	case "consumer":
		if family == "graph" {
			return 10
		}
		if family == "graph_text" {
			return 6
		}
	case "projection", "reconcile":
		if family == "graph" || family == "graph_text" {
			return 10
		}
	case "detector", "retry", "tuning":
		if family == "graph_text" {
			return 10
		}
		if family == "graph" {
			return 7
		}
		if family == "rg" {
			return 4
		}
	}
	return 0
}

func preferCodeOnlyRG(query string, term string) bool {
	text := strings.ToLower(strings.TrimSpace(query + " " + term))
	if hasAny(text, "authorization header", "missing authorization", "invalid authorization", "unauthorized", "forbidden") {
		return true
	}
	if hasAny(text, "request timeout", "read timeout", "write timeout", "http timeout", "readtimeout", "writetimeout", "dialtimeout", "pooltimeout") {
		return true
	}
	if hasAny(text, "validation error", "validation errors", "bindandvalidate", "writejsonerror", "bad request", "unprocessable entity") {
		return true
	}
	if hasAny(text, "audit log", "audit repository", "write audit", "insert audit", "llm_audit_log", "merge audit") {
		return true
	}
	if hasAny(text, "bearer", "token", "claims", "context", "middleware", "handler") {
		return true
	}
	if hasAny(text, "retry", "backoff", "requeue", "projection", "reconcile", "backfill", "parity") {
		return true
	}
	return false
}

func (e *Explorer) slotScopeTerms(slot planner.EvidenceSlot) []string {
	seen := map[string]bool{}
	var terms []string
	add := func(v string) {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" || seen[v] || len(v) < 3 || scoringStopword(v) {
			return
		}
		seen[v] = true
		terms = append(terms, v)
	}
	add(slot.Role)
	for _, token := range genericRoleTerms(slot.Role) {
		add(token)
	}
	for _, group := range e.conceptGroups(slot.Need + " " + strings.Join(slot.Hints, " ")) {
		for _, term := range group {
			add(term)
		}
	}
	return terms
}

func regexpQuotePath(path string) string {
	replacer := strings.NewReplacer(
		".", `\.`,
		"+", `\+`,
		"*", `\*`,
		"?", `\?`,
		"(", `\(`,
		")", `\)`,
		"[", `\[`,
		"]", `\]`,
		"{", `\{`,
		"}", `\}`,
		"^", `\^`,
		"$", `\$`,
		"|", `\|`,
	)
	return replacer.Replace(filepath.ToSlash(path))
}

func anchorsPathFilter(anchors []tools.PathAnchor) string {
	if len(anchors) == 0 {
		return ""
	}
	parts := []string{}
	for i, anchor := range anchors {
		if i >= 2 {
			break
		}
		if strings.TrimSpace(anchor.Path) == "" {
			continue
		}
		parts = append(parts, "^"+regexpQuotePath(anchor.Path)+"/")
		parts = append(parts, "^"+regexpQuotePath(anchor.Path)+"$")
	}
	return strings.Join(parts, "|")
}

func shouldForceGraph(plan planner.Plan, attempted []string) bool {
	if containsTool(attempted, "graph") {
		return false
	}
	if plan.NeedCallGraph {
		return true
	}
	return len(plan.Subqueries) > 1 && containsTool(plan.BackupTools, "graph")
}

func (e *Explorer) subqueryCovered(subquery string, hits []tools.Hit) bool {
	candidates := filterProductionish(hits, subquery)
	if len(candidates) == 0 {
		candidates = hits
	}
	for _, hit := range hits {
		if len(candidates) > 0 && !containsHit(candidates, hit) {
			continue
		}
		text := strings.ToLower(hit.File + " " + hit.Symbol + " " + hit.Snippet + " " + hit.Why)
		if e.conceptOverlap(subquery, text) >= 1 {
			return true
		}
	}
	return false
}

func (e *Explorer) slotCovered(slot planner.EvidenceSlot, hits []tools.Hit) bool {
	return e.bestSlotEvidence(slot, hits) != nil
}

func parallelPerTerm(ctx context.Context, terms []string, run func(term string) ([]tools.Hit, error), onErr func(error)) []tools.Hit {
	if len(terms) <= 1 {
		if len(terms) == 0 {
			return nil
		}
		hits, err := run(terms[0])
		if err != nil {
			onErr(err)
		}
		return hits
	}
	type result struct {
		hits []tools.Hit
		err  error
	}
	ch := make(chan result, len(terms))
	limit := parallelTermWorkers(len(terms))
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for _, term := range terms {
		wg.Add(1)
		go func(term string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			hits, err := run(term)
			ch <- result{hits: hits, err: err}
		}(term)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()
	merged := []tools.Hit{}
	for item := range ch {
		if item.err != nil {
			onErr(item.err)
		}
		merged = append(merged, item.hits...)
	}
	return merged
}

type slotFetchResult struct {
	slot      planner.EvidenceSlot
	toolName  string
	terms     []string
	scope     string
	anchorSet []tools.PathAnchor
	hits      []tools.Hit
	err       error
}

func parallelSlotRuns[T any](ctx context.Context, items []T, run func(T) slotFetchResult) []slotFetchResult {
	if len(items) == 0 {
		return nil
	}
	if len(items) == 1 {
		return []slotFetchResult{run(items[0])}
	}
	limit := parallelSlotWorkers(len(items))
	sem := make(chan struct{}, limit)
	ch := make(chan slotFetchResult, len(items))
	var wg sync.WaitGroup
	for _, item := range items {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(item T) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ch <- run(item)
		}(item)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()
	results := make([]slotFetchResult, 0, len(items))
	for result := range ch {
		results = append(results, result)
	}
	return results
}

func parallelTermWorkers(termCount int) int {
	switch {
	case termCount <= 1:
		return 1
	case termCount <= 3:
		return termCount
	case termCount <= 6:
		return 3
	default:
		return 4
	}
}

func parallelSlotWorkers(slotCount int) int {
	switch {
	case slotCount <= 1:
		return 1
	case slotCount <= 2:
		return slotCount
	case slotCount <= 4:
		return 2
	default:
		return 3
	}
}

func (e *Explorer) shouldUseDualLane(plan planner.Plan) bool {
	if plan.PrimaryTool != "semantic" {
		return false
	}
	if e.cfg.ParallelRetrieval {
		return true
	}
	if plan.Ambiguous {
		return true
	}
	if plan.Intent == "mixed" {
		return true
	}
	return plan.Intent == "behavior" && len(plan.Slots) >= 2
}
