package explorer

import (
	"context"
	"strings"
	"agent-explorer/internal/planner"
	"agent-explorer/internal/tools"
	"fmt"
	"path/filepath"
	"sort"
)

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
