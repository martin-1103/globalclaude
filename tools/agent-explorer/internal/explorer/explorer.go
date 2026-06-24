package explorer

import (
	"context"
	"fmt"
	"strings"

	"agent-explorer/internal/config"
	"agent-explorer/internal/learning"
	"agent-explorer/internal/llm"
	"agent-explorer/internal/planner"
	"agent-explorer/internal/tools"
)

// Result is the exploration output returned to the caller.
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

// Explorer is the main exploration engine. It coordinates planning, anchor computation,
// evidence retrieval, deduplication, reranking, confidence calibration, and explanation.
type Explorer struct {
	cfg     config.Config
	profile config.RepoProfile
	planner *planner.Planner
	llm     *llm.Client
	memory  learning.Summary
}

// New creates an Explorer with the given configuration.
func New(cfg config.Config, profile config.RepoProfile, plan *planner.Planner, client *llm.Client) *Explorer {
	return &Explorer{cfg: cfg, profile: profile, planner: plan, llm: client}
}

// Run executes the full exploration pipeline: plan → anchors → execute → expand →
// filter → dedupe → enrich → rerank → calibrate → explain.
func (e *Explorer) Run(ctx context.Context, repo string, query string, includeExplanation bool) (Result, error) {
	summary, _ := learning.LoadSummary(e.cfg.MemoryDir, repo, learning.ValidationOptions{
		CBMBinary:      e.cfg.CBMBinary,
		CBMCacheDir:    e.cfg.CBMCacheDir,
		TimeoutSeconds: e.cfg.ToolTimeoutSeconds,
		LineDistance:   e.profile.StaleLineDistance,
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

// composeExplanation generates a human-readable summary of the exploration results
// using the LLM. When LLM is unavailable, falls back to a templated message.
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
