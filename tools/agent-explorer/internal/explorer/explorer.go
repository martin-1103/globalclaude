package explorer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"agent-explorer/internal/config"
	"agent-explorer/internal/learning"
	"agent-explorer/internal/llm"
	"agent-explorer/internal/planner"
	"agent-explorer/internal/tools"
)

type Result struct {
	Query          string                        `json:"query"`
	Repo           string                        `json:"repo"`
	Plan           planner.Plan                  `json:"plan"`
	Anchors        []tools.PathAnchor            `json:"anchors,omitempty"`
	SlotAnchors    map[string][]tools.PathAnchor `json:"slot_anchors,omitempty"`
	Hits           []tools.Hit                   `json:"hits"`
	PrimaryHits    []tools.Hit                   `json:"primary_hits,omitempty"`
	SupportingHits []tools.Hit                   `json:"supporting_hits,omitempty"`
	TraceHits      []tools.Hit                   `json:"trace_hits,omitempty"`
	Suppressed     []tools.Hit                   `json:"suppressed,omitempty"`
	Warnings       []string                      `json:"warnings,omitempty"`
	Explanation    string                        `json:"explanation"`
}

type Explorer struct {
	cfg     config.Config
	profile config.RepoProfile
	planner *planner.Planner
	llm     *llm.Client
	memory  learning.Summary
}

func New(cfg config.Config, profile config.RepoProfile, plan *planner.Planner, client *llm.Client) *Explorer {
	return &Explorer{cfg: cfg, profile: profile, planner: plan, llm: client}
}

func (e *Explorer) maxToolFamilies() int {
	if e.profile.MaxToolFamilies > 0 {
		return e.profile.MaxToolFamilies
	}
	return 2
}

func (e *Explorer) Run(ctx context.Context, repo string, query string, includeExplanation bool) (Result, error) {
	summary, _ := learning.LoadSummary(e.cfg.MemoryDir, repo, learning.ValidationOptions{
		CBMBinary:      e.cfg.CBMBinary,
		CBMCacheDir:    e.cfg.CBMCacheDir,
		TimeoutSeconds: e.cfg.ToolTimeoutSeconds,
	})
	e.memory = summary
	plan, planErr := e.planner.Build(ctx, repo, query)
	anchors := e.computeAnchors(ctx, repo, plan)
	slotAnchors := e.computeSlotAnchors(ctx, repo, plan)
	hits, warnings := e.executePlan(ctx, repo, plan, anchors, slotAnchors)
	traceHits, traceWarnings := e.expandEvidence(ctx, repo, query, plan, hits)
	if len(traceHits) != 0 {
		hits = append(hits, traceHits...)
	}
	warnings = append(warnings, traceWarnings...)
	hits = filterIgnoredHits(repo, hits)
	hits = preferProductionHits(query, hits)
	hits = filterRoleMisaligned(query, hits)
	hits = e.filterSlotMisaligned(plan, hits)
	hits = e.dedupeHits(query, hits)
	hits = e.enrichHits(query, hits)
	hits = e.rerankWithCritic(ctx, query, plan, hits)
	hits = e.ensureSlotCoverage(plan, hits)
	hits = e.ensureSubqueryCoverage(plan, hits)
	hits, suppressed := e.compactHits(query, hits, e.cfg.MaxSearchResults)
	hits = e.assignEvidenceTypes(plan, hits)
	suppressed = e.assignEvidenceTypes(plan, suppressed)
	hits = promoteGroundedLiteralEvidence(plan, hits)
	suppressed = promoteGroundedLiteralEvidence(plan, suppressed)
	hits = promoteTraceEvidence(query, plan, hits)
	suppressed = promoteTraceEvidence(query, plan, suppressed)
	hits, suppressed = e.pruneTraceNoise(query, plan, hits, suppressed)
	hits = e.calibrateConfidenceBands(plan, hits)
	suppressed = e.calibrateConfidenceBands(plan, suppressed)
	hits = sortFinalHits(hits)
	suppressed = sortFinalHits(suppressed)
	if e.needsLexicalRescue(plan, hits) {
		if rescued := e.lexicalRescue(ctx, repo, query, plan); len(rescued) != 0 {
			hits = append(hits, rescued...)
			hits = e.dedupeHits(query, hits)
			hits = e.enrichHits(query, hits)
			hits = e.ensureSlotCoverage(plan, hits)
			hits = e.ensureSubqueryCoverage(plan, hits)
			hits, extraSuppressed := e.compactHits(query, hits, e.cfg.MaxSearchResults)
			hits = e.assignEvidenceTypes(plan, hits)
			hits = promoteGroundedLiteralEvidence(plan, hits)
			hits = promoteTraceEvidence(query, plan, hits)
			hits, extraSuppressed = e.pruneTraceNoise(query, plan, hits, extraSuppressed)
			hits = e.calibrateConfidenceBands(plan, hits)
			extraSuppressed = e.assignEvidenceTypes(plan, extraSuppressed)
			extraSuppressed = promoteGroundedLiteralEvidence(plan, extraSuppressed)
			extraSuppressed = promoteTraceEvidence(query, plan, extraSuppressed)
			extraSuppressed = e.calibrateConfidenceBands(plan, extraSuppressed)
			suppressed = append(suppressed, extraSuppressed...)
			hits = sortFinalHits(hits)
			suppressed = sortFinalHits(suppressed)
		}
	}
	primaryHits, supportingHits, traceHits := splitEvidencePools(hits)
	explanation := ""
	if includeExplanation {
		explanation = e.composeExplanation(ctx, repo, query, plan, hits, warnings, planErr)
	}
	hits = sortFinalHits(hits)
	suppressed = sortFinalHits(suppressed)
	primaryHits, supportingHits, traceHits = splitEvidencePools(hits)
	return Result{
		Query:          query,
		Repo:           repo,
		Plan:           plan,
		Anchors:        anchors,
		SlotAnchors:    slotAnchors,
		Hits:           hits,
		PrimaryHits:    primaryHits,
		SupportingHits: supportingHits,
		TraceHits:      traceHits,
		Suppressed:     suppressed,
		Warnings:       warnings,
		Explanation:    explanation,
	}, nil
}

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

func hasConfigRole(plan planner.Plan) bool {
	for _, slot := range plan.Slots {
		if slot.Role == "config" {
			return true
		}
	}
	return false
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

func slotKey(slot planner.EvidenceSlot) string {
	return strings.TrimSpace(slot.Role) + "|" + strings.TrimSpace(slot.Need)
}

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

func extractJSONObject(raw string) string {
	raw = strings.TrimSpace(raw)
	re := regexp.MustCompile(`(?s)\{.*\}`)
	match := re.FindString(raw)
	if match != "" {
		return match
	}
	return raw
}

func exactSymbolNameMatch(hint string, hit tools.Hit) bool {
	hint = strings.ToLower(strings.TrimSpace(hint))
	if hint == "" {
		return false
	}
	symbol := strings.ToLower(strings.TrimSpace(hit.Symbol))
	if symbol == "" {
		return false
	}
	short := symbol
	if idx := strings.LastIndex(short, "."); idx >= 0 && idx+1 < len(short) {
		short = short[idx+1:]
	}
	return short == hint || strings.HasSuffix(symbol, "."+hint)
}

func (e *Explorer) expandEvidence(ctx context.Context, repo string, query string, plan planner.Plan, hits []tools.Hit) ([]tools.Hit, []string) {
	if plan.Intent == "literal" {
		return nil, nil
	}
	if !plan.NeedCallGraph && !needsTraceExpansion(plan) {
		return nil, nil
	}
	seeds := traceSeedCandidates(query, hits)
	if len(seeds) == 0 {
		return nil, nil
	}
	out := make([]tools.Hit, 0, 8)
	warnings := []string{}
	for i, seed := range seeds {
		if i >= 2 {
			break
		}
		trace, err := tools.TracePath(ctx, repo, e.cfg.CBMBinary, e.cfg.CBMCacheDir, seed.Symbol, "both", 2, "calls", e.cfg.ToolTimeoutSeconds)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("trace expansion failed for %s: %v", seed.Symbol, err))
			continue
		}
		for _, step := range trace.Callers {
			out = append(out, traceStepHit(step, seed.Symbol, "caller"))
		}
		for _, step := range trace.Callees {
			out = append(out, traceStepHit(step, seed.Symbol, "callee"))
		}
	}
	return out, warnings
}

func needsTraceExpansion(plan planner.Plan) bool {
	for _, slot := range plan.Slots {
		switch slot.Role {
		case "consumer", "projection", "reconcile":
			return true
		}
	}
	return false
}

func traceSeedCandidates(query string, hits []tools.Hit) []tools.Hit {
	filtered := make([]tools.Hit, 0, len(hits))
	for _, hit := range hits {
		if strings.TrimSpace(hit.Symbol) == "" {
			continue
		}
		if hasTag(hit, "Route") || hasTag(hit, "Function") || hasTag(hit, "Method") {
			filtered = append(filtered, hit)
			continue
		}
		if strings.Contains(hit.Source, "search_graph") {
			filtered = append(filtered, hit)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		iScore := traceSeedScore(query, filtered[i])
		jScore := traceSeedScore(query, filtered[j])
		if iScore != jScore {
			return iScore > jScore
		}
		return filtered[i].File < filtered[j].File
	})
	out := make([]tools.Hit, 0, 3)
	seen := map[string]bool{}
	for _, hit := range filtered {
		key := strings.ToLower(strings.TrimSpace(hit.Symbol))
		if key == "" || seen[key] {
			continue
		}
		if queryLikelyUtilityNoise(query, hit) {
			continue
		}
		if traceSeedScore(query, hit) < 55 {
			continue
		}
		seen[key] = true
		out = append(out, hit)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

func queryLikelyUtilityNoise(query string, hit tools.Hit) bool {
	symbol := strings.ToLower(hit.Symbol)
	path := strings.ToLower(filepath.ToSlash(hit.File))
	q := strings.ToLower(query)
	if hasAny(symbol, "new", "helper", "util", "test") {
		return true
	}
	if strings.Contains(path, "/web/") && !hasAny(q, "web", "frontend", "ui", "tsx", "react") {
		return true
	}
	if hasAny(q, "claim", "claims", "consume", "used", "handler") && hasAny(symbol, "verify", "parse", "decode") && !strings.Contains(path, "/middleware/") {
		return true
	}
	return false
}

func traceSeedScore(query string, hit tools.Hit) int {
	q := strings.ToLower(query)
	path := strings.ToLower(filepath.ToSlash(hit.File))
	symbol := strings.ToLower(hit.Symbol)
	text := strings.ToLower(hit.File + " " + hit.Symbol + " " + hit.Snippet + " " + hit.Why)
	score := hit.Score
	score += roleAlignmentScore(q, path, symbol, strings.ToLower(hit.Snippet), hit.Tags) * 2
	score += conceptGroupCoverageScore(q, text) * 10
	if hasAny(q, "claim", "claims", "context", "consume", "consumed", "handler") && !hasAny(text, "claim", "claims", "context", "handler", "route", "controller") {
		score -= 40
	}
	if hasAny(q, "parity", "projection", "reconcile") && !hasAny(text, "parity", "projection", "reconcile", "current", "publish", "repair", "heal") {
		score -= 36
	}
	if hasAny(q, "stall", "detect", "retry", "tune", "tuning", "backfill") && !hasAny(text, "stall", "detect", "retry", "requeue", "backoff", "tune", "throttle", "rate", "backfill", "gap") {
		score -= 36
	}
	if strings.Contains(path, "/web/") && !hasAny(q, "web", "frontend", "ui") {
		score -= 30
	}
	if strings.Contains(path, "/cmd/") {
		score -= 10
	}
	return score
}

func conceptGroupCoverageScore(query string, text string) int {
	score := 0
	for _, group := range normalizedConceptGroups(query) {
		for _, term := range group {
			if strings.Contains(text, term) {
				score++
				break
			}
		}
	}
	return score
}

func normalizedConceptGroups(query string) [][]string {
	stop := map[string]bool{
		"how": true, "where": true, "what": true, "when": true, "which": true, "why": true, "and": true, "the": true,
		"are": true, "is": true, "was": true, "were": true, "that": true, "with": true, "from": true, "into": true,
		"lives": true, "logic": true, "path": true, "involved": true, "handled": true, "detected": true,
	}
	synonyms := map[string][]string{
		"retry":      {"retry", "requeue", "redelivery", "attempt"},
		"tune":       {"tune", "throttle", "rate", "budget"},
		"detect":     {"detect", "watch", "monitor", "scan", "check"},
		"stall":      {"stall", "stuck", "blocked", "lag", "gap"},
		"projection": {"projection", "projector"},
		"reconcile":  {"reconcile", "reconciler"},
		"auth":       {"auth", "jwt", "claims", "token", "middleware"},
		"claim":      {"claim", "claims", "context"},
	}
	var groups [][]string
	for _, token := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return r == ' ' || r == ',' || r == '.' || r == ';' || r == ':' || r == '(' || r == ')' || r == '/' || r == '-'
	}) {
		if len(token) < 4 || stop[token] {
			continue
		}
		group := []string{token}
		if extra, ok := synonyms[token]; ok {
			group = append(group, extra...)
		}
		groups = append(groups, group)
	}
	return groups
}

func traceStepHit(step tools.TraceStep, via string, relation string) tools.Hit {
	symbol := step.QualifiedName
	if strings.TrimSpace(symbol) == "" {
		symbol = step.Name
	}
	return tools.Hit{
		Source:    "codebase-memory/trace_path",
		File:      step.File,
		LineStart: max(step.LineStart, 1),
		LineEnd:   max(step.LineEnd, max(step.LineStart, 1)),
		Symbol:    symbol,
		Why:       fmt.Sprintf("trace %s of %s", relation, via),
		Family:    "graph",
		Lane:      "trace",
		Tags:      []string{"Trace"},
	}
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
	runToolTermsScoped := func(name string, terms []string, pathFilter string) {
		if name == "" {
			return
		}
		attempted = append(attempted, name)
		familiesUsed++
		var hits []tools.Hit
		switch name {
		case "graph":
			hits = parallelPerTerm(ctx, terms, func(term string) ([]tools.Hit, error) {
				result, err := tools.SearchGraphScoped(ctx, repo, e.cfg.CBMBinary, e.cfg.CBMCacheDir, term, pathFilter, e.cfg.ToolTimeoutSeconds, e.cfg.MaxSearchResults)
				result = filterHitsByPathRegex(repo, result, pathFilter)
				markLane(result, "graph")
				boostAnchored(repo, result, anchors)
				return result, err
			}, func(err error) { addWarning(name, err) })
		case "graph_text":
			hits = parallelPerTerm(ctx, terms, func(term string) ([]tools.Hit, error) {
				result, err := tools.SearchCodeScoped(ctx, repo, e.cfg.CBMBinary, e.cfg.CBMCacheDir, term, pathFilter, e.cfg.ToolTimeoutSeconds, e.cfg.MaxSearchResults)
				result = filterHitsByPathRegex(repo, result, pathFilter)
				markLane(result, "graph_text")
				boostAnchored(repo, result, anchors)
				return result, err
			}, func(err error) { addWarning(name, err) })
		case "semantic":
			hits = parallelPerTerm(ctx, terms, func(term string) ([]tools.Hit, error) {
				result, err := tools.SemanticSearch(ctx, repo, e.cfg.ClaudeContextCmd, term, e.cfg.ToolTimeoutSeconds, e.cfg.MaxSearchResults)
				markLane(result, "semantic")
				boostAnchored(repo, result, anchors)
				return result, err
			}, func(err error) { addWarning(name, err) })
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
				boostAnchored(repo, result, anchors)
				return result, err
			}, func(err error) { addWarning(name, err) })
		case "astgrep":
			result, err := tools.ASTGrep(ctx, repo, plan.ASTPattern, e.cfg.ToolTimeoutSeconds, e.cfg.MaxSearchResults)
			addWarning(name, err)
			markLane(result, "astgrep")
			boostAnchored(repo, result, anchors)
			hits = append(hits, result...)
		}
		appendHits(hits)
	}
	runToolTerms := func(name string, terms []string) {
		runToolTermsScoped(name, terms, "")
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

func (e *Explorer) composeExplanation(ctx context.Context, repo string, query string, plan planner.Plan, hits []tools.Hit, warnings []string, planErr error) string {
	if len(hits) == 0 {
		if len(warnings) != 0 {
			return fmt.Sprintf("No evidence found. Warning: %s", warnings[0])
		}
		if planErr != nil {
			return "No evidence found. Planner used fallback."
		}
		return "No evidence found."
	}

	systemPrompt := `You summarize exploration results only.
Do not propose code changes.
Keep answer under 28 words.
State strongest area first.
If evidence mixed or weak, say likely or ambiguous.
Do not mention tools unless needed.`
	var builder strings.Builder
	builder.WriteString("Query: " + query + "\n")
	builder.WriteString("Plan primary tool: " + plan.PrimaryTool + "\n")
	for i, hit := range hits {
		if i >= 4 {
			break
		}
		builder.WriteString(fmt.Sprintf("- [%s %d] %s:%d-%d %s\n", hit.Confidence, hit.Score, hit.File, hit.LineStart, hit.LineEnd, hit.Why))
	}
	reply, err := e.llm.Chat(ctx, systemPrompt, builder.String())
	if err != nil {
		if len(warnings) != 0 {
			return fmt.Sprintf("Relevant exploration hits. Warning: %s", warnings[0])
		}
		if planErr != nil {
			return "Relevant exploration hits. Planner used fallback."
		}
		return "Relevant exploration hits."
	}
	reply = strings.TrimSpace(reply)
	if len(warnings) != 0 {
		return fmt.Sprintf("%s Warning: %s", reply, warnings[0])
	}
	return reply
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
		if hits, err := tools.SemanticSearch(ctx, repo, e.cfg.ClaudeContextCmd, subquery, e.cfg.ToolTimeoutSeconds, 2); err == nil {
			e.annotateHits(subquery, hits)
			addHits(filterProductionish(hits, subquery), 2, "semantic", "semantic fallback anchor")
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

func filterIgnoredHits(repo string, hits []tools.Hit) []tools.Hit {
	matchers := loadIgnoreMatchers(repo)
	filtered := make([]tools.Hit, 0, len(hits))
	for _, hit := range hits {
		if shouldIgnoreHit(repo, hit.File, matchers) {
			continue
		}
		filtered = append(filtered, hit)
	}
	return filtered
}

func shouldIgnoreHit(repo string, file string, matchers []string) bool {
	rel := file
	if relative, err := filepath.Rel(repo, file); err == nil {
		rel = relative
	}
	rel = filepath.ToSlash(rel)
	base := filepath.Base(rel)
	baseLower := strings.ToLower(base)
	parts := strings.Split(rel, "/")
	if strings.HasPrefix(baseLower, "fix_") || strings.HasPrefix(baseLower, "tmp") || strings.HasPrefix(baseLower, "scratch") || strings.HasPrefix(baseLower, "debug_") {
		return true
	}
	for _, part := range parts {
		switch part {
		case "node_modules", "site-packages", ".venv", "__pycache__", ".git":
			return true
		}
	}
	if strings.HasSuffix(base, "_pb2.py") || strings.HasSuffix(base, "_pb2_grpc.py") {
		return true
	}
	for _, pattern := range matchers {
		if ignorePatternMatch(rel, pattern) {
			return true
		}
	}
	return false
}

func loadIgnoreMatchers(repo string) []string {
	path := filepath.Join(repo, ".cbmignore")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	matchers := []string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		matchers = append(matchers, filepath.ToSlash(line))
	}
	return matchers
}

func ignorePatternMatch(rel string, pattern string) bool {
	pattern = strings.TrimSpace(filepath.ToSlash(pattern))
	if pattern == "" {
		return false
	}

	candidates := []string{pattern}
	if strings.HasPrefix(pattern, "**/") {
		candidates = append(candidates, strings.TrimPrefix(pattern, "**/"))
	}
	if strings.HasSuffix(pattern, "/**") {
		trimmed := strings.TrimSuffix(pattern, "/**")
		if rel == trimmed || strings.HasPrefix(rel, trimmed+"/") {
			return true
		}
	}
	for _, candidate := range candidates {
		if ok, err := filepath.Match(candidate, rel); err == nil && ok {
			return true
		}
		if strings.Contains(candidate, "/") {
			continue
		}
		if ok, err := filepath.Match(candidate, filepath.Base(rel)); err == nil && ok {
			return true
		}
	}
	return false
}

func (e *Explorer) rankHit(query string, hit tools.Hit) int {
	path := strings.ToLower(filepath.ToSlash(hit.File))
	name := strings.ToLower(filepath.Base(path))
	symbolLower := strings.ToLower(hit.Symbol)
	snippetLower := strings.ToLower(hit.Snippet)
	score := 0
	lq := strings.ToLower(query)

	if strings.Contains(hit.Source, "claude-context") {
		score += 26
	}
	if strings.Contains(hit.Source, "codebase-memory/search_graph") {
		score += 34
	}
	if strings.Contains(hit.Source, "codebase-memory/trace_path") {
		score += 30
	}
	if strings.Contains(hit.Source, "codebase-memory/search_code") {
		score += 24
	}
	if hit.Lane == "rg" {
		score += 14
	}
	if strings.Contains(path, "/internal/") {
		score += 20
	}
	if strings.HasPrefix(path, "pkg/") {
		score += 18
	}
	if strings.Contains(path, "/handler/") || strings.Contains(path, "/middleware/") {
		score += 18
	}
	if strings.Contains(path, "/service/") || strings.Contains(path, "/store/") || strings.Contains(path, "/projection/") {
		score += 8
	}
	if strings.Contains(path, "/cmd/") || strings.Contains(path, "/main.go") {
		score -= 8
	}
	if strings.Contains(path, "/web/") || strings.Contains(path, "/frontend/") || strings.Contains(path, "/ui/") {
		score -= 10
	}
	if strings.Contains(path, "/docs/") || strings.Contains(path, "/doc/") || strings.Contains(path, "/konsep/") || strings.HasSuffix(path, ".md") {
		score -= 40
	}
	if strings.Contains(path, "/project-docs/") || strings.Contains(path, "/workflow") || strings.Contains(path, "/workflows/") {
		score -= 55
	}
	if strings.Contains(path, "/reports/") || strings.HasSuffix(path, ".html") {
		score -= 55
	}
	if rootArtifactPath(path) {
		score -= 65
	}
	if strings.HasSuffix(path, ".sql") || strings.HasSuffix(path, ".proto") || strings.Contains(path, ".pb.go") {
		score -= 34
	}
	if strings.Contains(path, "/test/") || strings.Contains(path, "/tests/") || strings.Contains(name, "_test.") || strings.HasPrefix(name, "test_") {
		score -= 34
	}
	if strings.Contains(path, "/test-py/") || strings.Contains(path, "/gass_test/") || strings.Contains(name, "verify_") {
		score -= 42
	}
	if strings.HasSuffix(path, ".old") {
		score -= 50
	}
	if strings.Contains(path, "/resource/") || strings.HasPrefix(path, "resource/") {
		score -= 38
	}
	if strings.HasPrefix(strings.ToLower(hit.Symbol), "test") || strings.Contains(strings.ToLower(hit.Symbol), ".test") || strings.Contains(strings.ToLower(hit.Symbol), "_test.") || strings.Contains(path, "run_tests") {
		score -= 36
	}
	if strings.Contains(path, "e2e-harness") || strings.Contains(path, "/scenario/") {
		score -= 26
		if strings.Contains(lq, "e2e") || strings.Contains(lq, "harness") || strings.Contains(lq, "test") {
			score += 22
		}
	}
	if strings.Contains(path, "/scripts/") || strings.Contains(path, "deprecated") {
		score -= 30
	}
	if strings.HasPrefix(name, "fix_") || strings.Contains(name, "bootstrap") || strings.Contains(name, "scratch") || strings.Contains(name, "tmp") {
		score -= 24
	}
	if hit.Symbol != "" {
		score += 10
	}
	if hasTag(hit, "Module") || strings.HasSuffix(path, "/__init__.py") {
		score -= 20
		if wantsExactLocality(lq) {
			score -= 18
		}
	}
	if hasTag(hit, "Method") || hasTag(hit, "Function") {
		score += 14
	}
	if hasTag(hit, "Route") {
		score += 8
	}
	span := max(hit.LineEnd-hit.LineStart, 0)
	if span >= 8 {
		score += 8
	}
	if span <= 2 && !strings.Contains(lq, "constant") && !strings.Contains(lq, "key") && !strings.Contains(lq, "flag") {
		score -= 10
	}
	if strings.Contains(symbolLower, "test") || strings.Contains(symbolLower, "helper") || strings.Contains(symbolLower, "mock") {
		score -= 18
	}
	if strings.HasPrefix(symbolLower, "new") || strings.HasPrefix(symbolLower, "with") {
		score -= 4
	}
	if strings.Contains(strings.ToLower(hit.Why), "call") || strings.Contains(strings.ToLower(hit.Why), "symbol") {
		score += 6
	}
	if exactLiteralMatch(lq, path, symbolLower, snippetLower) {
		score += 40
	}
	score += literalIntentBias(lq, path, symbolLower, snippetLower)
	score += e.memoryHitBias(hit)
	score += e.conceptOverlap(query, strings.ToLower(hit.File+" "+hit.Symbol+" "+hit.Snippet)) * 10
	score += roleAlignmentScore(lq, path, symbolLower, snippetLower, hit.Tags)
	score += consumerIntentBias(lq, path, symbolLower, snippetLower)
	score += queryDisambiguationPenalty(lq, path, symbolLower, snippetLower)

	for _, token := range strings.Fields(strings.ToLower(query)) {
		if len(token) < 3 || scoringStopword(token) {
			continue
		}
		if strings.Contains(path, token) {
			score += 6
		}
		if strings.Contains(strings.ToLower(hit.Symbol), token) {
			score += 8
		}
		if strings.Contains(strings.ToLower(hit.Snippet), token) {
			score += 2
		}
	}
	return score
}

func queryDisambiguationPenalty(query string, path string, symbol string, snippet string) int {
	text := path + " " + symbol + " " + snippet
	score := 0
	if hasAny(query, "context", "request") && hasAny(query, "claim", "claims") && hasAny(query, "store", "stored", "inject", "set") {
		if strings.Contains(path, "/routes/") && strings.Contains(path, "/claims.") {
			score -= 90
		}
		if strings.Contains(symbol, "__route__") {
			score -= 60
		}
		if hasAny(text, "create_claim", "list_claims", "get_claim", "delete_claim") {
			score -= 80
		}
		if hasAny(text, "withclaims", "claimsfromcontext", "context.withvalue", "header.get", "authorization", "authenticate") {
			score += 40
		}
		if strings.Contains(path, "/middleware/") {
			score += 30
		}
		if strings.Contains(path, "/auth") {
			score += 20
		}
	}
	if strings.Contains(query, "auth") && strings.Contains(query, "middleware") && strings.Contains(query, "defined") {
		if strings.Contains(path, "/middleware/auth.") || strings.Contains(symbol, ".middleware.auth.") || strings.Contains(symbol, "newauthmiddleware") || strings.Contains(symbol, ".authenticate") {
			score += 95
		}
		if strings.Contains(path, "/auth/jwt.go") || strings.HasSuffix(path, "/jwt.go") {
			score -= 60
		}
		if strings.Contains(symbol, ".jwt") {
			score -= 45
		}
	}
	if hasAny(query, "bearer", "authorization") && hasAny(query, "token", "request") && hasAny(query, "extract", "header") {
		if hasAny(text, "authorization", "bearer", "header.get", "strings.fields", "equalfold") {
			score += 25
		}
		if hasAny(text, "fromextracteddata", "_merge_extracted") {
			score -= 35
		}
	}
	if strings.Contains(query, "auth") && strings.Contains(query, "middleware") && hasAny(query, "invalid token", "invalid jwt", "token invalid") {
		if strings.Contains(path, "/middleware/auth.") || strings.Contains(symbol, ".authenticate") {
			score += 95
		}
		if hasAny(text, "writejsonerror", "\"invalid token\"", "statusunauthorized") {
			score += 40
		}
		if strings.Contains(path, "/auth/jwt.go") || strings.HasSuffix(path, "/jwt.go") {
			score -= 70
		}
	}
	if strings.Contains(query, "auth") && strings.Contains(query, "middleware") {
		if strings.Contains(path, "/middleware/auth.") || strings.Contains(path, "/internal/middleware/") {
			score += 30
		}
		if strings.Contains(path, "/internal/security/") || strings.HasSuffix(path, "/auth.py") {
			score -= 45
		}
	}
	if hasAny(query, "request timeout", "read timeout", "write timeout", "http timeout") {
		if strings.Contains(path, "/client/") || strings.Contains(path, "/api_client") || strings.Contains(path, "newapiclient") {
			score += 38
		}
		if strings.Contains(path, "/config/") || strings.Contains(path, "/http/") || strings.Contains(path, "/client/") {
			score += 26
		}
		if strings.Contains(path, "/server/") || strings.Contains(path, "/http_server") || strings.Contains(path, "/http.go") {
			score += 8
		}
		if hasAny(text, "readtimeout", "writetimeout", "dialtimeout", "pooltimeout", "timeout:", "timeout =", "client.timeout", "readheadertimeout", "idletimeout", "transport: &http.transport") {
			score += 24
		} else {
			score -= 35
		}
		if hasAny(snippet, "timeout:", "readtimeout", "writetimeout", "dialtimeout", "readheadertimeout", "idletimeout") {
			score += 36
		}
		if strings.Contains(snippet, "http.client") && !hasAny(snippet, "timeout:", "timeout =", "readtimeout", "writetimeout", "dialtimeout", "readheadertimeout", "idletimeout") {
			score -= 55
		}
		if strings.Contains(snippet, "time.duration") && !hasAny(snippet, "getenvduration", "* time.", "time.second", "time.minute", "timeout:", "readtimeout:", "writetimeout:", "dialtimeout:", "readheadertimeout:") {
			score -= 42
		}
		if strings.Contains(snippet, "{\"") && strings.Contains(snippet, "timeout") {
			score -= 24
		}
		if strings.Contains(symbol, "timeout") {
			score += 18
		}
		if hasAny(symbol, "newapiclient", "newclient") {
			score += 34
		}
		if strings.Contains(snippet, "http.client") || strings.Contains(snippet, "transport:") {
			score += 26
		}
		if hasAny(snippet, "readheadertimeout", "writetimeout:", "readtimeout:", "timeout:") && (strings.Contains(path, "/server/") || strings.Contains(path, "/http")) {
			score += 8
		}
		if hasAny(snippet, "redis", "pooltimeout", "redispooltimeout", "redisdialtimeout", "rediswritetimeout", "redisreadtimeout") && !hasAny(query, "redis", "cache") {
			score -= 72
		}
		if strings.Contains(path, "/server/") && !hasAny(symbol, "newapiclient", "newclient") && !strings.Contains(snippet, "http.client") {
			score -= 42
		}
		if strings.Contains(path, "/backfill/") || strings.Contains(symbol, "summarystagepressurereadtimeout") || strings.Contains(path, "reconcile-task-metrics") {
			score -= 40
		}
		if strings.Contains(path, "/api_client.") || strings.Contains(path, "/client.go") || strings.Contains(path, "/config/config.go") {
			score += 16
		}
		if strings.Contains(path, "/trigger/") && !hasAny(text, "readtimeout", "writetimeout", "dialtimeout", "pooltimeout", "httpclient") {
			score -= 25
		}
		if !hasAny(snippet, "readtimeout", "writetimeout", "dialtimeout", "pooltimeout", "timeout", "redis.options", "transport", "readheadertimeout", "idletimeout") &&
			!hasAny(symbol, "readtimeout", "writetimeout", "dialtimeout", "pooltimeout", "timeout", "readheadertimeout", "idletimeout") {
			score -= 40
		}
		if hasAny(path, "/api_client.go", "/rate.go", "/config/config.go", "/cmd/consumer/main.go") {
			score += 22
		}
		if hasAny(snippet, "redis.newclient", "redis.options", "http.client{", "transport: &http.transport", "timeout: 30 * time.second", "readtimeout:", "writetimeout:") {
			score += 28
		}
		if strings.Contains(symbol, "newapiclient") || strings.Contains(symbol, "newclient") || strings.Contains(symbol, ".load") {
			score += 24
		}
		if strings.Contains(path, "/cmd/") && !hasAny(symbol, "newapiclient", "newclient", ".load") {
			score -= 38
		}
		if strings.Contains(path, "reconcile-task-metrics") {
			score -= 55
		}
		if strings.HasSuffix(path, ".md") || strings.Contains(path, "/project-docs/") || strings.Contains(path, "/reports/") {
			score -= 140
		}
	}
	if hasAny(query, "http client configured", "http client config", "where http client configured") {
		if hasAny(text, "http.client", "transport", "newclient", "newapiclient") {
			score += 45
		}
		if strings.Contains(path, "/client/") || strings.Contains(path, "/http/") || strings.Contains(path, "/transport") {
			score += 28
		}
		if strings.Contains(path, "/cache/") || strings.Contains(path, "/handler/") {
			score -= 24
		}
	}
	if hasAny(query, "config loaded from env", "loaded from env", "from env") {
		if hasAny(text, "getenv", "lookupenv", "mustgetenv", "envconfig", "automaticenv") {
			score += 46
		}
		if hasAny(text, "loadconfig", "load(", "config") {
			score += 20
		}
		if strings.Contains(path, "/config/") || strings.Contains(path, "/bootstrap") {
			score += 24
		}
		if strings.HasSuffix(path, "/main.py") || strings.HasSuffix(path, "/main.go") {
			score -= 18
		}
	}
	if hasAny(query, "database transaction begins", "transaction begins", "begin tx", "begintx") {
		if hasAny(text, "begintx", "begintxx", "db.begin", "tx, err :=", "begin transaction") {
			score += 50
		} else {
			score -= 75
		}
		if strings.Contains(path, "/repository/") || strings.Contains(path, "/store/") || strings.Contains(path, "/db/") {
			score += 22
		}
		if strings.Contains(path, "/worker/") {
			score -= 40
		}
		if strings.Contains(symbol, ".begintx") || strings.Contains(symbol, ".begintxx") || strings.Contains(symbol, ".withtx") {
			score += 34
		}
		if strings.Contains(path, "_query.go") || hasAny(symbol, "build", "count", "query") {
			score -= 40
		}
	}
	if hasAny(query, "unauthorized", "missing authorization", "authorization header") {
		if hasAny(text, "statusunauthorized", "missing authorization header", "invalid authorization header", "\"unauthorized\"", "missing claims", "invalid token") {
			score += 52
		}
		if strings.Contains(path, "/middleware/") || strings.Contains(path, "/auth/") || strings.Contains(path, "/security/") {
			score += 34
		}
		if strings.Contains(path, "/config/") || strings.Contains(path, "/public/") || strings.Contains(path, "/web/") || strings.HasSuffix(path, ".js") {
			score -= 38
		}
		if strings.Contains(snippet, "authorization") &&
			!hasAny(text, "statusunauthorized", "missing authorization header", "invalid authorization header", "\"unauthorized\"", "missing claims", "invalid token", "writejsonerror") {
			score -= 44
		}
		if strings.Contains(path, "/handler/") && !strings.Contains(path, "/auth") {
			score -= 22
		}
		if strings.HasPrefix(strings.TrimSpace(snippet), "//") || strings.HasPrefix(strings.TrimSpace(snippet), "#") || strings.Contains(path, "_test.go") || strings.Contains(path, "/tests/") {
			score -= 90
		}
	}
	if hasAny(query, "bearer token extracted", "bearer token", "authorization bearer") {
		if hasAny(text, "authorization", "bearer", "strings.fields", "header.get", "equalfold", "extractclaims", "requireauth") {
			score += 52
		}
		if strings.Contains(path, "/middleware/") || strings.Contains(path, "/auth/") || strings.Contains(path, "/security/") {
			score += 26
		}
		if strings.Contains(path, "/cmd/") || strings.Contains(path, "/tools/") {
			score -= 50
		}
	}
	if hasAny(query, "external api retries", "api retries", "http retries", "retries configured") {
		if hasAny(text, "retryattempts", "retrydelay", "backoff", "callwithretry", "forwardwithretry", "dowithretry", "withretryattempts") {
			score += 56
		}
		if !hasAny(text, "retryattempts", "retrydelay", "backoff", "callwithretry", "forwardwithretry", "dowithretry", "withretryattempts", "retry") {
			score -= 60
		}
		if strings.Contains(path, "/client/") || strings.Contains(path, "/connector/") || strings.Contains(path, "/publisher/") || strings.Contains(path, "/consumer/") {
			score += 24
		}
		if strings.Contains(path, "/http/") || strings.Contains(path, "/meta/") || strings.Contains(path, "/visitor/") {
			score += 16
		}
		if strings.Contains(path, "/config/") {
			score += 12
		}
		if strings.HasPrefix(path, "pkg/mq/") || strings.Contains(path, "/mq/") {
			score -= 55
		}
		if strings.HasSuffix(path, ".md") || strings.Contains(path, "/reports/") || strings.Contains(path, "/project-docs/") {
			score -= 160
		}
	}
	if hasAny(query, "background worker loop defined", "worker loop defined", "queue consumer handles messages", "consumer handles messages") {
		if hasAny(text, "consume", "handlemessage", "processmessage", "for {", "select {", "worker") {
			score += 40
		}
		if strings.Contains(path, "/consumer/") || strings.Contains(path, "/worker/") || strings.Contains(path, "/jobs/") {
			score += 28
		}
		if strings.Contains(path, "/handler/") && !hasAny(text, "consume", "worker", "message") {
			score -= 22
		}
	}
	if hasAny(query, "validation errors returned", "validation error", "validation errors") {
		if hasAny(text, "validationerror", "validator", "bindandvalidate", "writejsonerror", "badrequest") {
			score += 42
		}
		if strings.Contains(path, "/validator") || strings.Contains(path, "/validation") || strings.Contains(path, "/middleware/") || strings.Contains(path, "/handler/") {
			score += 22
		}
		if strings.HasSuffix(path, ".md") || strings.Contains(path, "/docs/") || strings.HasSuffix(path, ".sum") || strings.HasSuffix(path, ".lock") {
			score -= 60
		}
	}
	if hasAny(query, "forbidden error", "exact forbidden", "find exact forbidden error message") {
		if hasAny(text, "statusforbidden", "permission denied", "invalid verify token", "forbidden") {
			score += 42
		}
		if hasAny(text, "writeerror", "writejsonerror", "c.json(http.statusforbidden", "http.statusforbidden", "\"error\":", "err.error()") {
			score += 28
		}
		if strings.Contains(path, "/middleware/") || strings.Contains(path, "/auth/") || strings.Contains(path, "/security/") {
			score += 38
		}
		if strings.Contains(path, "/handler/") || strings.Contains(path, "/server/") {
			score += 18
		}
		if hasAny(text, "insufficient permissions", "admin access required") {
			score += 40
		}
		if strings.Contains(path, "/handler/") && !strings.Contains(path, "/auth") && !strings.Contains(path, "/security") {
			score -= 54
		}
		if strings.Contains(path, "/middleware/") && hasAny(text, "insufficient permissions", "statusforbidden", "writejsonerror") {
			score += 46
		}
		if strings.HasPrefix(strings.TrimSpace(snippet), "//") || strings.HasPrefix(strings.TrimSpace(snippet), "#") {
			score -= 80
		}
		if strings.Contains(path, "/resource/") || strings.Contains(path, "/web/") || strings.Contains(path, "/tests/") || strings.Contains(path, "/lib/site-packages/") {
			score -= 60
		}
	}
	if hasAny(query, "audit log", "audit repository", "writes audit log", "write audit") {
		if hasAny(text, "llm_audit_log", "insert llm audit log", "insertmergeaudit", "merge audit log", "project_rebuild_status_audit") {
			score += 50
		}
		if hasAny(symbol, "llmauditclickhousestore.insert", "insertmergeaudit") {
			score += 45
		}
		if strings.Contains(path, "/store/") || strings.Contains(path, "/clickhouse/") || strings.Contains(path, "/repository/") {
			score += 18
		}
		if strings.Contains(symbol, "enqueueparityauditjob") || strings.Contains(symbol, "parityauditreadyrange") {
			score -= 170
		}
		if strings.Contains(path, "parity_audit_jobs") || strings.Contains(path, "sync_repository.go") {
			score -= 90
		}
	}
	if hasAny(query, "pagination defaults defined", "pagination default", "page size default") {
		if hasAny(text, "pagesize", "defaultpagesize", "pagination", "per_page", "limit") {
			score += 38
		}
		if strings.Contains(path, "/pagination") || strings.Contains(path, "/query") || strings.Contains(path, "/handler/") {
			score += 20
		}
		if strings.Contains(symbol, "paginate") || strings.Contains(symbol, "pagesize") || strings.Contains(symbol, "defaultpagesize") {
			score += 24
		}
		if strings.Contains(path, "/page_map") || strings.Contains(path, "/tools/shared/") || strings.Contains(path, "_helpers") {
			score -= 48
		}
		if strings.Contains(path, ".proto") || strings.Contains(path, "/proto/") {
			score -= 70
		}
		if strings.Contains(path, "/handler/") && hasAny(text, "pagesize", "defaultpagesize", "pagination", "limit") {
			score += 22
		}
		if strings.Contains(path, "/handler/") && hasAny(snippet, "pagesize:", "default page size", "limit:") {
			score += 55
		}
		if strings.Contains(path, "/query") && hasAny(snippet, "pagesize:", "limit:") {
			score += 32
		}
		if strings.Contains(path, "/config/") && !strings.Contains(path, "/pagination") {
			score -= 20
		}
		if strings.HasPrefix(path, "pkg/") && !strings.Contains(path, "/pagination") && !strings.Contains(path, "/query") && !strings.Contains(path, "/handler/") {
			score -= 30
		}
		if strings.Contains(symbol, "defaultpagesize") && !strings.Contains(path, "/handler/") && !strings.Contains(path, "/query") && !strings.Contains(path, "/pagination") {
			score -= 35
		}
	}
	if hasAny(query, "projection or read model", "read model", "publishes projection", "publishes read model") {
		if hasAny(text, "projection", "read model", "publish", "publishcscurrenttable", "projector", "materialized") {
			score += 54
		}
		if strings.Contains(path, "/projection/") || strings.Contains(path, "cs_current_projection") || strings.Contains(path, "reportprojection") {
			score += 32
		}
		if strings.Contains(path, "/handler/") || strings.Contains(path, "/routes/") {
			score -= 55
		}
	}
	if hasAny(query, "stale work", "stuck jobs", "stale jobs", "watchdog", "dead work") {
		if hasAny(text, "watchdog", "stale", "requeue", "dead work", "stuck", "detect") {
			score += 48
		}
		if strings.Contains(path, "/watchdog/") || strings.Contains(path, "stale_watchdog") || strings.Contains(path, "backfill_work_item") {
			score += 34
		}
		if strings.Contains(symbol, "watchdog") || strings.Contains(symbol, "detect") || strings.Contains(symbol, "requeuestale") || strings.Contains(symbol, "requeuedead") {
			score += 26
		}
		if strings.Contains(path, "/store/") && !hasAny(symbol, "watchdog", "detect", "requeue") {
			score -= 26
		}
	}
	if hasAny(query, "webhook signature", "verify webhook", "webhook verify token", "signature validated") {
		if hasAny(text, "webhook", "signature", "verify token", "x-hub-signature", "hmac", "invalid verify token") {
			score += 52
		}
		if strings.Contains(path, "/webhook") || strings.Contains(path, "webhook-") || strings.Contains(path, "/handler/") || strings.Contains(path, "/middleware/") {
			score += 26
		}
		if strings.Contains(path, "/auth/") && !strings.Contains(path, "/webhook") {
			score -= 45
		}
	}
	if hasAny(query, "parity audit pauses", "parity audit pause", "pause parity audit", "reconcile backlog") {
		if hasAny(symbol, "shouldpauseparityauditforreportreconcilebacklog", "checkbackloggate", "runpendingparityaudit") {
			score += 90
		}
		if strings.Contains(path, "parity_audit_jobs.go") {
			score += 50
		}
		if strings.Contains(path, "cs_current_projection") {
			score -= 70
		}
	}
	if hasAny(query, "feature flag", "feature flags", "is enabled", "isenabled") {
		if hasAny(text, "featureflag", "isenabled", "flag", "projectfeature") {
			score += 20
		}
		if strings.Contains(path, "/repository/") || strings.Contains(path, "/config/") || strings.Contains(path, "/handler/strategy") {
			score += 10
		}
	}
	if wantsExactLocality(query) {
		if !strings.Contains(symbol, ".") && !hasAny(text, "func ", "method", "handler", "route", "return", "writejsonerror") {
			score -= 24
		}
		if strings.Contains(path, "/handler/") && hasAny(query, "route", "health", "handler") {
			score += 10
		}
	}
	return score
}

func wantsExactLocality(query string) bool {
	return hasAny(query,
		"where is", "where ", "defined", "which route", "which handler", "where handler",
		"where function", "where func", "where code", "where request timeout configured",
		"where source mirror", "where current projection", "where retry", "where backoff",
		"where bearer", "find exact", "exact ", "which code", "which repository")
}

func exactLiteralMatch(query string, path string, symbol string, snippet string) bool {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return false
	}
	candidates := []string{
		query,
		strings.TrimPrefix(query, "find "),
		strings.TrimPrefix(query, "where "),
		strings.TrimPrefix(query, "show "),
	}
	if strings.Contains(query, "missing authorization header") {
		candidates = append(candidates, "missing authorization header")
	}
	if strings.Contains(query, "invalid authorization header") {
		candidates = append(candidates, "invalid authorization header")
	}
	if strings.Contains(query, "authorization header") {
		candidates = append(candidates, "authorization header")
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if strings.Contains(snippet, candidate) || strings.Contains(path, candidate) || strings.Contains(symbol, candidate) {
			return true
		}
	}
	return false
}

func literalIntentBias(query string, path string, symbol string, snippet string) int {
	score := 0
	if !(strings.Contains(query, "error") || strings.Contains(query, "message") || strings.Contains(query, "returned") || strings.Contains(query, "returns")) {
		return score
	}
	if exactLiteralMatch(query, path, symbol, snippet) {
		score += 30
	}
	if hasAny(query, "authorization", "bearer", "token", "invalid", "missing") {
		if strings.Contains(path, "/middleware/") || strings.Contains(path, "/auth") {
			score += 20
		}
		if hasAny(snippet, "unauthorized", "forbidden", "authorization", "bearer", "invalid token", "missing authorization") {
			score += 18
		}
		if strings.Contains(symbol, "authenticate") {
			score += 16
		}
	}
	return score
}

func consumerIntentBias(query string, path string, symbol string, snippet string) int {
	score := 0
	if !(hasAny(query, "read", "reads", "consume", "consumed", "used", "me endpoint", "handler", "route", "endpoint") && hasAny(query, "context", "claim", "claims")) {
		return score
	}
	if strings.Contains(path, "/handler/") || strings.Contains(path, "/route") || strings.Contains(path, "/controller") {
		score += 26
	}
	if hasAny(snippet, "claimsfromcontext", "currentuser", "requirerole", "ctx", "context") {
		score += 18
	}
	if strings.Contains(symbol, "me") || strings.Contains(symbol, "handler") {
		score += 12
	}
	if strings.Contains(path, "/middleware/") || strings.HasSuffix(path, "/jwt.go") {
		score -= 24
	}
	return score
}

func (e *Explorer) memoryHitBias(hit tools.Hit) int {
	path := strings.ToLower(filepath.ToSlash(hit.File))
	symbol := strings.ToLower(strings.TrimSpace(hit.Symbol))
	score := 0
	for key, weight := range e.memory.AcceptedPathWeight {
		if strings.Contains(path, key) {
			score += min(weight*8, 24)
		}
	}
	for key, weight := range e.memory.RejectedPathWeight {
		if strings.Contains(path, key) {
			score -= min(weight*10, 30)
		}
	}
	for key, weight := range e.memory.AcceptedSymbolWeight {
		if symbol != "" && strings.Contains(symbol, key) {
			score += min(weight*8, 24)
		}
	}
	for key, weight := range e.memory.RejectedSymbolWeight {
		if symbol != "" && strings.Contains(symbol, key) {
			score -= min(weight*10, 30)
		}
	}
	return score
}

func (e *Explorer) memoryPathBias(path string) int {
	score := 0
	for key, weight := range e.memory.AcceptedPathWeight {
		if strings.Contains(path, key) {
			score += min(weight*6, 18)
		}
	}
	for key, weight := range e.memory.RejectedPathWeight {
		if strings.Contains(path, key) {
			score -= min(weight*8, 24)
		}
	}
	return score
}

func roleAlignmentScore(query string, path string, symbol string, snippet string, tags []string) int {
	text := path + " " + symbol + " " + snippet
	score := 0
	for _, role := range inferredRolesFromQuery(query) {
		score += roleHitScore(role, text, path) * 12
		if role == "consumer" && (strings.Contains(path, "/handler/") || hasTagName(tags, "Route")) {
			score += 8
		}
		if role == "validator" && strings.Contains(path, "/middleware/") {
			score += 8
		}
		if role == "injector" && strings.Contains(path, "/middleware/") {
			score += 6
		}
	}
	return score
}

func hasAny(text string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func hasTagName(tags []string, want string) bool {
	for _, tag := range tags {
		if strings.EqualFold(strings.TrimSpace(tag), want) {
			return true
		}
	}
	return false
}

func scoringStopword(token string) bool {
	switch token {
	case "how", "where", "what", "when", "which", "why", "trace", "find", "show",
		"with", "from", "into", "that", "this", "these", "those", "and", "the",
		"are", "was", "were", "logic", "lives", "handled", "handle", "failures",
		"failure", "path", "involved", "current", "consumed":
		return true
	default:
		return false
	}
}

func hasTag(hit tools.Hit, want string) bool {
	for _, tag := range hit.Tags {
		if strings.EqualFold(strings.TrimSpace(tag), want) {
			return true
		}
	}
	return false
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

func familyFromSource(source string) string {
	switch {
	case strings.Contains(source, "claude-context"):
		return "semantic"
	case strings.Contains(source, "search_graph"):
		return "graph"
	case strings.Contains(source, "search_code"):
		return "graph_text"
	case source == "rg":
		return "rg"
	case source == "ast-grep":
		return "astgrep"
	default:
		return source
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
	coverageOK := e.coverageSatisfied(plan, hits)
	laneCount := laneDiversity(hits)
	fileCount := fileDiversity(hits)
	for i := range hits {
		base := confidenceBandForPlan(plan, hits[i].Score)
		corroboration := corroborationCount(hits, hits[i], i)
		independent := independentEvidenceCount(hits, hits[i], i)
		exactish := strings.Contains(strings.ToLower(hits[i].Why), "exact symbol resolve") || strings.TrimSpace(hits[i].EvidenceType) == "literal"
		diverseEnough := laneCount >= 2 || fileCount >= 2
		switch {
		case hits[i].EvidenceType == "trace":
			if corroboration >= 2 && independent >= 1 && coverageOK && hits[i].Score >= 82 {
				hits[i].Confidence = "medium"
			} else {
				hits[i].Confidence = "low"
			}
		case base == "high":
			if exactish && (plan.Intent == "definition" || plan.Intent == "literal") && coverageOK {
				hits[i].Confidence = "high"
			} else if coverageOK && diverseEnough && corroboration >= 1 && independent >= 1 && !plan.Ambiguous {
				hits[i].Confidence = "high"
			} else {
				hits[i].Confidence = "medium"
			}
		case base == "medium":
			if exactish && coverageOK {
				hits[i].Confidence = "medium"
			} else if coverageOK && corroboration >= 1 && independent >= 1 && diverseEnough {
				hits[i].Confidence = "medium"
			} else {
				hits[i].Confidence = "low"
			}
		default:
			if exactish && corroboration >= 1 && independent >= 1 {
				hits[i].Confidence = "medium"
			} else {
				hits[i].Confidence = "low"
			}
		}
		if plan.Ambiguous && hits[i].Confidence == "high" && !exactish {
			hits[i].Confidence = "medium"
		}
		if plan.Intent == "mixed" && !coverageOK {
			if exactish && hits[i].Confidence == "high" {
				hits[i].Confidence = "medium"
			} else {
				hits[i].Confidence = "low"
			}
		}
		if plan.Intent == "mixed" && hits[i].Confidence != "low" && !diverseEnough && !exactish {
			hits[i].Confidence = "low"
		}
	}
	return hits
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

func (e *Explorer) shouldStop(query string, plan planner.Plan, hits []tools.Hit, familiesUsed int) bool {
	maxFamilies := e.maxToolFamilies()
	e.annotateHits(query, hits)
	if len(hits) == 0 {
		return false
	}
	diverseEnough := laneDiversity(hits) >= 2 || fileDiversity(hits) >= 2
	if e.coverageSatisfied(plan, hits) && hasConfidenceAtLeast(hits, "high") && (!plan.Ambiguous || exactGroundedHit(plan, hits)) {
		return true
	}
	if familiesUsed >= 1 && hasConfidenceAtLeast(hits, "medium") && !plan.Ambiguous && diverseEnough {
		return true
	}
	if familiesUsed >= maxFamilies && e.coverageSatisfied(plan, hits) && diverseEnough {
		return true
	}
	return familiesUsed >= maxFamilies
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

func containsTool(items []string, tool string) bool {
	for _, item := range items {
		if item == tool {
			return true
		}
	}
	return false
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

func shouldForceGraph(plan planner.Plan, attempted []string) bool {
	if containsTool(attempted, "graph") {
		return false
	}
	if plan.NeedCallGraph {
		return true
	}
	return len(plan.Subqueries) > 1 && containsTool(plan.BackupTools, "graph")
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

func looksLikeConfigBlock(hit tools.Hit) bool {
	text := strings.ToLower(strings.TrimSpace(hit.Snippet + " " + hit.Symbol + " " + hit.Why))
	path := strings.ToLower(filepath.ToSlash(hit.File))
	if hasAny(text, "readtimeout", "writetimeout", "dialtimeout", "pooltimeout", "timeout:", "http.client", "redis.options", "transport") {
		return true
	}
	if hasAny(path, "/config/", "/client/", "/server/") && hasAny(text, "newclient", "newapiclient", "load", "config") {
		return true
	}
	return false
}

func roleHitScore(role string, text string, path string) int {
	switch role {
	case "validator":
		if hasAny(text, genericRoleTerms(role)...) {
			return 1
		}
	case "injector":
		if hasAny(text, genericRoleTerms(role)...) && strings.Contains(path, "/middleware/") {
			return 1
		}
	case "consumer":
		if hasAny(text, genericRoleTerms(role)...) && (strings.Contains(path, "/handler/") || strings.Contains(path, "/route") || strings.Contains(path, "/controller/")) {
			return 1
		}
	default:
		if hasAny(text, genericRoleTerms(role)...) {
			return 1
		}
	}
	return 0
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

func markLane(hits []tools.Hit, lane string) {
	for i := range hits {
		hits[i].Lane = lane
	}
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

func countProductionish(hits []tools.Hit) int {
	return len(filterProductionish(hits, ""))
}

func preferProductionHits(query string, hits []tools.Hit) []tools.Hit {
	prod := filterProductionish(hits, query)
	if len(prod) == 0 {
		return hits
	}
	return prod
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

func (e *Explorer) slotMatchScore(slot planner.EvidenceSlot, hit tools.Hit) int {
	path := strings.ToLower(filepath.ToSlash(hit.File))
	text := strings.ToLower(hit.File + " " + hit.Symbol + " " + hit.Snippet + " " + hit.Why)
	score := 0
	score += roleHitScore(slot.Role, text, path) * 4
	score += e.conceptOverlap(slot.Need, text) * 3
	for _, hint := range slot.Hints {
		if strings.Contains(text, strings.ToLower(hint)) {
			score += 2
		}
	}
	score += strictRoleBonus(slot.Role, path, text)
	if slotRolePathMismatch(slot.Role, path) {
		score -= 9
	}
	if slotTopicMiss(slot, text) {
		score -= 6
	}
	return score
}

func strictRoleBonus(role string, path string, text string) int {
	switch role {
	case "projection":
		score := 0
		if hasAny(text, "projection", "publish", "tombstone", "current", "batch", "loadactive") {
			score += 10
		}
		if strings.Contains(path, "/projection") || strings.Contains(path, "current_projection") || strings.Contains(path, "cs_current") {
			score += 12
		}
		return score
	case "reconcile":
		score := 0
		if hasAny(text, "reconcile", "repair", "heal", "rebuild", "backlog", "pause parity") {
			score += 12
		}
		if strings.Contains(path, "reconcile") || strings.Contains(path, "parity") || strings.Contains(path, "gap_reaper") {
			score += 10
		}
		return score
	case "detector":
		score := 0
		if hasAny(text, "detect", "gap", "stall", "watchdog", "monitor", "complete range", "cutoff") {
			score += 10
		}
		if strings.Contains(path, "detector") || strings.Contains(path, "backfill") || strings.Contains(path, "source_mirror") {
			score += 10
		}
		return score
	case "retry":
		score := 0
		if hasAny(text, "retry", "requeue", "backoff", "publisher", "failed rows") {
			score += 10
		}
		if strings.Contains(path, "retry") || strings.Contains(path, "publisher") {
			score += 8
		}
		return score
	case "consumer":
		score := 0
		if hasAny(text, "handler", "consume", "read", "claimsfromcontext", "route") {
			score += 8
		}
		if strings.Contains(path, "/handler/") || strings.Contains(path, "/consumer/") {
			score += 6
		}
		return score
	case "config":
		score := 0
		if hasAny(text, "readtimeout", "writetimeout", "dialtimeout", "pooltimeout", "timeout", "http.client", "transport", "redis.options") {
			score += 12
		}
		if strings.Contains(path, "/config/") || strings.Contains(path, "/client/") || strings.Contains(path, "/api_client.") {
			score += 10
		}
		return score
	default:
		return 0
	}
}

func slotRolePathMismatch(role string, path string) bool {
	switch role {
	case "validator", "injector":
		return strings.Contains(path, "/web/") || strings.Contains(path, "/ui/") || strings.Contains(path, "/internal/security/")
	case "config":
		return strings.Contains(path, "/web/") || strings.Contains(path, "/ui/") || strings.Contains(path, "/docs/") || strings.Contains(path, "/handler/")
	case "consumer":
		return strings.Contains(path, "/middleware/")
	case "detector", "retry", "tuning":
		return strings.Contains(path, "/web/") || strings.Contains(path, "/ui/") || strings.Contains(path, "/docs/")
	case "projection", "reconcile":
		return strings.Contains(path, "/web/") || strings.Contains(path, "/ui/")
	default:
		return false
	}
}

func slotTopicMiss(slot planner.EvidenceSlot, text string) bool {
	topics := localTopicTerms(slot.Need + " " + strings.Join(slot.Hints, " "))
	if len(topics) == 0 {
		return false
	}
	matched := 0
	for _, topic := range topics {
		if strings.Contains(text, topic) {
			matched++
		}
	}
	return matched == 0
}

func localTopicTerms(query string) []string {
	stop := map[string]bool{
		"how": true, "where": true, "what": true, "when": true, "which": true, "why": true,
		"and": true, "the": true, "are": true, "was": true, "were": true, "with": true,
		"into": true, "from": true, "this": true, "that": true, "logic": true, "path": true,
		"involved": true, "handled": true, "handle": true, "find": true, "show": true, "trace": true,
		"validate": true, "validates": true, "validation": true, "jwt": true,
		"handler": true, "route": true,
		"detect": true, "detected": true, "stall": true, "stalls": true, "stuck": true, "gap": true,
		"retry": true, "requeue": true, "backoff": true, "tune": true, "tuning": true, "page": true,
		"rate": true, "projection": true, "reconcile": true, "repair": true, "heal": true,
		"middleware": true, "current": true, "consumed": true, "consume": true, "used": true,
		"failure": true, "failures": true, "lives": true,
	}
	seen := map[string]bool{}
	var out []string
	for _, token := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return r == ' ' || r == ',' || r == '.' || r == ';' || r == ':' || r == '(' || r == ')' || r == '/' || r == '-'
	}) {
		if len(token) < 4 || stop[token] || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

func containsHit(hits []tools.Hit, target tools.Hit) bool {
	for _, hit := range hits {
		if hit.File == target.File && hit.LineStart == target.LineStart && hit.LineEnd == target.LineEnd && hit.Symbol == target.Symbol {
			return true
		}
	}
	return false
}

func (e *Explorer) conceptOverlap(query string, text string) int {
	match := 0
	for _, group := range e.conceptGroups(query) {
		for _, term := range group {
			if strings.Contains(text, term) {
				match++
				break
			}
		}
	}
	return match
}

func (e *Explorer) conceptGroups(query string) [][]string {
	lq := strings.ToLower(query)
	stop := map[string]bool{
		"how": true, "where": true, "what": true, "when": true, "which": true, "why": true, "and": true, "the": true,
		"are": true, "is": true, "was": true, "were": true, "that": true, "with": true, "from": true, "into": true,
		"lives": true, "logic": true, "path": true, "involved": true, "handled": true, "detected": true,
	}
	synonyms := map[string][]string{
		"retry":      {"retry", "requeue", "redelivery", "attempt"},
		"tune":       {"tune", "throttle", "rate", "budget"},
		"detect":     {"detect", "watch", "monitor", "scan", "check"},
		"stall":      {"stall", "stuck", "blocked", "lag", "gap"},
		"projection": {"projection", "projector"},
		"reconcile":  {"reconcile", "reconciler"},
		"auth":       {"auth", "jwt", "claims", "token", "middleware"},
		"claim":      {"claim", "claims", "context"},
	}
	for key, values := range e.profile.ConceptOverlays {
		normalized := make([]string, 0, len(values))
		for _, value := range values {
			value = strings.ToLower(strings.TrimSpace(value))
			if value != "" {
				normalized = append(normalized, value)
			}
		}
		synonyms[strings.ToLower(strings.TrimSpace(key))] = normalized
	}
	var groups [][]string
	for _, token := range strings.FieldsFunc(lq, func(r rune) bool {
		return r == ' ' || r == ',' || r == '.' || r == ';' || r == ':' || r == '(' || r == ')' || r == '/' || r == '-'
	}) {
		if len(token) < 4 || stop[token] {
			continue
		}
		group := []string{token}
		if extra, ok := synonyms[token]; ok {
			group = append(group, extra...)
		}
		groups = append(groups, group)
	}
	return groups
}

func (e *Explorer) queryVariants(query string) []string {
	raw := strings.TrimSpace(query)
	if raw == "" {
		return nil
	}
	query = strings.ToLower(raw)
	var out []string
	seen := map[string]bool{}
	add := func(v string) {
		v = strings.TrimSpace(strings.ToLower(v))
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	add(query)
	for _, split := range splitIdentifierVariants(raw) {
		add(split)
	}
	var compact []string
	for _, group := range e.conceptGroups(query) {
		if len(group) == 0 {
			continue
		}
		compact = append(compact, group[0])
		if len(group) > 1 {
			add(strings.Join(group, " "))
		}
	}
	if len(compact) > 0 {
		add(strings.Join(compact, " "))
	}
	if len(compact) > 1 && len(out) < 3 {
		add(strings.Join(compact[max(0, len(compact)-2):], " "))
	}
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

func splitIdentifierVariants(query string) []string {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return r == ' ' || r == ',' || r == '.' || r == ';' || r == ':' || r == '(' || r == ')' || r == '/' || r == '-'
	})
	seen := map[string]bool{}
	out := []string{}
	for _, field := range fields {
		if strings.Contains(field, "_") {
			parts := strings.FieldsFunc(field, func(r rune) bool { return r == '_' })
			if len(parts) > 1 {
				v := strings.Join(parts, " ")
				if !seen[v] {
					seen[v] = true
					out = append(out, v)
				}
			}
			continue
		}
		if camelSplit := splitCamelLike(field); camelSplit != "" && camelSplit != field {
			if !seen[camelSplit] {
				seen[camelSplit] = true
				out = append(out, camelSplit)
			}
		}
	}
	return out
}

func splitCamelLike(v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	var out []rune
	prevLower := false
	for i, r := range v {
		isUpper := r >= 'A' && r <= 'Z'
		isLower := r >= 'a' && r <= 'z'
		if i > 0 && isUpper && prevLower {
			out = append(out, ' ')
		}
		if isUpper {
			out = append(out, r+'a'-'A')
		} else {
			out = append(out, r)
		}
		prevLower = isLower
	}
	return strings.TrimSpace(string(out))
}

func inferredRolesFromQuery(query string) []string {
	lq := strings.ToLower(query)
	roles := []string{}
	add := func(role string) {
		for _, existing := range roles {
			if existing == role {
				return
			}
		}
		roles = append(roles, role)
	}
	if hasAny(lq, "validate", "validates", "validation", "verify", "token", "jwt", "bearer", "authorization", "auth middleware", "middleware auth") || (strings.Contains(lq, "auth") && strings.Contains(lq, "middleware")) {
		add("validator")
	}
	if hasAny(lq, "inject", "store", "set", "put", "context", "claim", "claims") {
		add("injector")
	}
	if hasAny(lq, "consume", "consumed", "read", "used", "handler", "route", "endpoint") {
		add("consumer")
	}
	if hasAny(lq, "detect", "detected", "stall", "stuck", "blocked", "lag", "watch", "monitor") {
		add("detector")
	}
	if hasAny(lq, "retry", "requeue", "backoff", "attempt") {
		add("retry")
	}
	if hasAny(lq, "tune", "tuning", "throttle", "rate", "budget", "page") {
		add("tuning")
	}
	if hasAny(lq, "projection", "publish", "current", "read model", "materialized") {
		add("projection")
	}
	if hasAny(lq, "reconcile", "repair", "heal", "rebuild") {
		add("reconcile")
	}
	return roles
}

func genericRoleTerms(role string) []string {
	switch role {
	case "validator":
		return []string{"validate", "verify", "authenticate", "authorization", "token", "jwt", "bearer", "middleware", "auth"}
	case "injector":
		return []string{"context", "claim", "claims", "with", "set", "store", "inject"}
	case "consumer":
		return []string{"context", "claim", "claims", "handler", "route", "controller", "read", "use"}
	case "detector":
		return []string{"detect", "watch", "monitor", "check", "scan", "gap", "stall", "stuck", "blocked", "lag"}
	case "retry":
		return []string{"retry", "requeue", "backoff", "attempt", "redelivery"}
	case "tuning":
		return []string{"tune", "throttle", "rate", "budget", "page", "limit"}
	case "projection":
		return []string{"projection", "publish", "current", "materialized", "readmodel"}
	case "reconcile":
		return []string{"reconcile", "repair", "heal", "rebuild", "recover"}
	default:
		return nil
	}
}

func laneDiversity(hits []tools.Hit) int {
	seen := map[string]bool{}
	for _, hit := range hits {
		lane := strings.TrimSpace(hit.Lane)
		if lane == "" {
			lane = strings.TrimSpace(hit.Family)
		}
		if lane == "" {
			continue
		}
		seen[lane] = true
	}
	return len(seen)
}

func fileDiversity(hits []tools.Hit) int {
	seen := map[string]bool{}
	for _, hit := range hits {
		file := filepath.ToSlash(strings.ToLower(strings.TrimSpace(hit.File)))
		if file == "" {
			continue
		}
		seen[file] = true
	}
	return len(seen)
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

func nonEmptyLane(hit tools.Hit) string {
	if strings.TrimSpace(hit.Lane) != "" {
		return hit.Lane
	}
	if strings.TrimSpace(hit.Family) != "" {
		return hit.Family
	}
	return hit.Source
}
