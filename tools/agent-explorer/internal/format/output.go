package format

import (
	"encoding/json"
	"fmt"
	"strings"

	"agent-explorer/internal/explorer"
	"agent-explorer/internal/tools"
)

func FinalAnswer(result explorer.Result, citationOnly bool, agentMode bool, mainAgentMode bool, debugRetrieval bool) string {
	if citationOnly {
		return citationBlock(result.Hits)
	}
	if mainAgentMode {
		return mainAgentAnswer(result)
	}
	if agentMode {
		return agentAnswer(result, debugRetrieval)
	}

	var b strings.Builder
	if strings.TrimSpace(result.Explanation) != "" {
		b.WriteString("Summary: ")
		b.WriteString(result.Explanation)
		b.WriteString("\n")
	}
	meta := contractMeta(result)
	b.WriteString("Retrieval Pack\n")
	b.WriteString(fmt.Sprintf("Status: %s | Planner: %s | Answerability: %s\n", meta.Status, meta.PlannerStatus, meta.Answerability))
	b.WriteString(fmt.Sprintf("Confidence: %s (top=%s) | Action: %s\n", meta.ConfidenceBand, meta.TopConfidence, meta.RecommendedAction))
	b.WriteString(fmt.Sprintf("Intent: %s | Primary: %s\n", nonEmpty(result.Plan.Intent, "unknown"), nonEmpty(result.Plan.PrimaryTool, "unknown")))
	b.WriteString("Quality Flags: ")
	b.WriteString(strings.Join(meta.QualityFlags, ", "))
	b.WriteString("\n")
	terms := compactTerms(result.Plan.SearchTerms)
	if len(terms) != 0 {
		b.WriteString("Terms: ")
		b.WriteString(strings.Join(terms, " | "))
		b.WriteString("\n")
	}
	if lane := dominantLane(result.Hits); lane != "" {
		b.WriteString("Lane: ")
		b.WriteString(lane)
		b.WriteString("\n")
	}
	b.WriteString("Top Hits:\n")
	writeEvidenceSection(&b, result.PrimaryHits, "Primary", 3)
	writeEvidenceSection(&b, result.SupportingHits, "Supporting", 2)
	if summary := traceSummary(result.TraceHits); summary != "" {
		b.WriteString("Trace Summary:\n")
		b.WriteString(summary)
		b.WriteString("\n")
	}
	writeEvidenceSection(&b, result.TraceHits, "Trace", 2)
	if len(result.Hits) == 0 {
		b.WriteString("none\n")
	}
	if len(result.Warnings) != 0 {
		b.WriteString("Warnings:\n")
		for _, warning := range result.Warnings {
			b.WriteString("- ")
			b.WriteString(warning)
			b.WriteString("\n")
		}
	}
	if debugRetrieval && len(result.Suppressed) != 0 {
		b.WriteString("Suppressed:\n")
		for i, hit := range uniqueDisplayHits(result.Suppressed) {
			if i >= 4 {
				break
			}
			b.WriteString(fmt.Sprintf("- [%s %d %s] %s:%d-%d\n", strings.ToUpper(hit.Confidence), hit.Score, nonEmpty(hit.Lane, hit.Family, hit.Source), hit.File, hit.LineStart, hit.LineEnd))
		}
	}
	b.WriteString(citationBlock(result.Hits))
	return strings.TrimSpace(b.String())
}

func Trace(result tools.TraceResult) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Trace: %s (%s)\n", result.Symbol, result.Direction))
	b.WriteString(fmt.Sprintf("Summary: callers=%d callees=%d\n", len(result.Callers), len(result.Callees)))
	if len(result.Callers) != 0 {
		b.WriteString("Callers:\n")
		for _, step := range result.Callers {
			b.WriteString(fmt.Sprintf("- %s", traceLabel(step)))
			if step.File != "" {
				b.WriteString(fmt.Sprintf(" [%s:%d-%d]", step.File, step.LineStart, step.LineEnd))
			}
			b.WriteString("\n")
		}
	}
	if len(result.Callees) != 0 {
		b.WriteString("Callees:\n")
		for _, step := range result.Callees {
			b.WriteString(fmt.Sprintf("- %s", traceLabel(step)))
			if step.File != "" {
				b.WriteString(fmt.Sprintf(" [%s:%d-%d]", step.File, step.LineStart, step.LineEnd))
			}
			b.WriteString("\n")
		}
	}
	if len(result.Callers) == 0 && len(result.Callees) == 0 {
		b.WriteString("No callers/callees found.")
	}
	return strings.TrimSpace(b.String())
}

func JSON(v any) (string, error) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func citationBlock(hits []tools.Hit) string {
	var b strings.Builder
	b.WriteString("<final_answer>\n")
	for _, hit := range limitCitations(hits, len(hits)) {
		b.WriteString(fmt.Sprintf("%s:%d-%d", hit.File, hit.LineStart, hit.LineEnd))
		if hit.Symbol != "" {
			b.WriteString(fmt.Sprintf(" (%s)", hit.Symbol))
		}
		b.WriteString("\n")
	}
	b.WriteString("</final_answer>")
	return b.String()
}

func nonEmpty(items ...string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return ""
}

func dominantLane(hits []tools.Hit) string {
	if len(hits) == 0 {
		return ""
	}
	counts := map[string]int{}
	bestLane := ""
	bestCount := 0
	for _, hit := range hits {
		if strings.TrimSpace(hit.Lane) == "" {
			continue
		}
		counts[hit.Lane]++
		if counts[hit.Lane] > bestCount {
			bestCount = counts[hit.Lane]
			bestLane = hit.Lane
		}
	}
	return bestLane
}

func agentAnswer(result explorer.Result, debugRetrieval bool) string {
	var b strings.Builder
	if strings.TrimSpace(result.Explanation) != "" {
		b.WriteString(result.Explanation)
		b.WriteString("\n")
	}
	meta := contractMeta(result)
	b.WriteString("retrieval_pack\n")
	b.WriteString("status=" + meta.Status)
	b.WriteString(" planner_status=" + meta.PlannerStatus)
	b.WriteString(" answerability=" + meta.Answerability)
	b.WriteString(" confidence_band=" + meta.ConfidenceBand)
	b.WriteString(" top_confidence=" + meta.TopConfidence)
	b.WriteString(" primary=" + nonEmpty(result.Plan.PrimaryTool, "unknown"))
	if lane := dominantLane(result.Hits); lane != "" {
		b.WriteString(" lane=" + lane)
	}
	terms := compactTerms(result.Plan.SearchTerms)
	if len(terms) != 0 {
		b.WriteString(" terms=" + strings.Join(terms, "|"))
	}
	b.WriteString(" quality_flags=" + strings.Join(meta.QualityFlags, ","))
	b.WriteString("\n")
	idx := 1
	idx = writeCompactEvidence(&b, idx, result.PrimaryHits, 3)
	idx = writeCompactEvidence(&b, idx, result.SupportingHits, 1)
	_ = writeCompactEvidence(&b, idx, result.TraceHits, 2)
	if summary := inlineTraceSummary(result.TraceHits); summary != "" {
		b.WriteString("trace_summary=" + summary + "\n")
	}
	if debugRetrieval && len(result.Suppressed) != 0 {
		b.WriteString("Suppressed: ")
		for i, hit := range uniqueDisplayHits(result.Suppressed) {
			if i >= 3 {
				break
			}
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(fmt.Sprintf("%s:%d-%d", hit.File, hit.LineStart, hit.LineEnd))
		}
		b.WriteString("\n")
	}
	b.WriteString(citationBlock(result.Hits))
	return strings.TrimSpace(b.String())
}

func mainAgentAnswer(result explorer.Result) string {
	var b strings.Builder
	primary := result.PrimaryHits
	if len(primary) == 0 {
		primary = limitCitations(result.Hits, 2)
	}
	supporting := result.SupportingHits
	if len(supporting) == 0 {
		supporting = fallbackSupportingHits(result.Hits, primary, 1)
	}
	meta := contractMeta(result)
	b.WriteString("retrieval_contract\n")
	b.WriteString("status=" + meta.Status)
	b.WriteString(" planner_status=" + meta.PlannerStatus)
	b.WriteString(" answerability=" + meta.Answerability)
	b.WriteString(" intent=" + nonEmpty(result.Plan.Intent, "unknown"))
	b.WriteString(" question_class=" + questionClass(result))
	b.WriteString(" confidence_band=" + meta.ConfidenceBand)
	b.WriteString(" top_confidence=" + meta.TopConfidence)
	b.WriteString(" primary=" + nonEmpty(result.Plan.PrimaryTool, "unknown"))
	if lane := dominantLane(result.Hits); lane != "" {
		b.WriteString(" lane=" + lane)
	}
	b.WriteString("\n")
	b.WriteString("quality_flags=" + strings.Join(meta.QualityFlags, ",") + "\n")
	writeContractSection(&b, "primary_evidence", primary, 2)
	writeContractSection(&b, "supporting_evidence", supporting, 1)
	if summary := traceSummary(result.TraceHits); summary != "" {
		b.WriteString("trace_summary:\n")
		b.WriteString(summary)
		b.WriteString("\n")
	}
	writeContractSection(&b, "trace_evidence", result.TraceHits, 1)
	gaps := resultGaps(result)
	b.WriteString("gaps:\n")
	if len(gaps) == 0 {
		b.WriteString("- none\n")
	} else {
		for _, gap := range gaps {
			b.WriteString("- " + gap + "\n")
		}
	}
	b.WriteString("recommended_action=" + meta.RecommendedAction)
	b.WriteString("\n")
	b.WriteString(citationBlock(limitCitations(result.Hits, 4)))
	return strings.TrimSpace(b.String())
}

func shortSymbol(symbol string) string {
	if strings.TrimSpace(symbol) == "" {
		return ""
	}
	parts := strings.Split(symbol, ".")
	if len(parts) == 0 {
		return symbol
	}
	return parts[len(parts)-1]
}

func traceLabel(step tools.TraceStep) string {
	name := nonEmpty(step.QualifiedName, step.Name)
	short := shortSymbol(name)
	if short == "" {
		short = name
	}
	return fmt.Sprintf("hop %d %s", step.Hop, short)
}

func retrievalStatus(hits []tools.Hit) string {
	if len(hits) == 0 {
		return "abstain"
	}
	top := hits[0]
	if top.Confidence == "high" || top.Confidence == "medium" {
		return "grounded"
	}
	return "weak_evidence"
}

func RetrievalStatus(hits []tools.Hit) string {
	return retrievalStatus(hits)
}

func PlannerStatus(result explorer.Result) string {
	return plannerStatus(result)
}

func RecommendedAction(result explorer.Result) string {
	return recommendedAction(result)
}

func Answerability(result explorer.Result) string {
	return answerability(result)
}

func ConfidenceBand(result explorer.Result) string {
	return confidenceBand(result)
}

func TopConfidence(result explorer.Result) string {
	return topConfidence(result.Hits)
}

func QualityFlags(result explorer.Result) []string {
	return qualityFlags(result)
}

func TraceSummary(result explorer.Result) string {
	return inlineTraceSummary(result.TraceHits)
}

func compactTerms(terms []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" || seen[strings.ToLower(term)] {
			continue
		}
		if len(term) < 4 {
			continue
		}
		if len(term) > 48 {
			continue
		}
		lower := strings.ToLower(term)
		if lower == "where" || lower == "what" || lower == "which" || lower == "trace" || lower == "find" || lower == "show" {
			continue
		}
		seen[lower] = true
		out = append(out, term)
		if len(out) >= 5 {
			break
		}
	}
	return out
}

func questionClass(result explorer.Result) string {
	switch {
	case result.Plan.NeedCallGraph || len(result.TraceHits) != 0:
		return "trace"
	case result.Plan.Intent == "mixed":
		return "multi-hop"
	case result.Plan.Intent == "literal":
		return "literal"
	case result.Plan.Intent == "definition":
		return "lookup"
	default:
		return "behavior"
	}
}

func topConfidence(hits []tools.Hit) string {
	if len(hits) == 0 {
		return "none"
	}
	return nonEmpty(hits[0].Confidence, "none")
}

type contractSummary struct {
	Status            string
	PlannerStatus     string
	Answerability     string
	ConfidenceBand    string
	TopConfidence     string
	RecommendedAction string
	QualityFlags      []string
}

func contractMeta(result explorer.Result) contractSummary {
	return contractSummary{
		Status:            retrievalStatus(result.Hits),
		PlannerStatus:     plannerStatus(result),
		Answerability:     answerability(result),
		ConfidenceBand:    confidenceBand(result),
		TopConfidence:     topConfidence(result.Hits),
		RecommendedAction: recommendedAction(result),
		QualityFlags:      qualityFlags(result),
	}
}

func plannerStatus(result explorer.Result) string {
	switch {
	case result.Plan.Ambiguous:
		return "ambiguous"
	case result.Plan.NeedCallGraph:
		return "trace_required"
	case len(result.Plan.Slots) > 1:
		return "multi_slot"
	default:
		return "targeted"
	}
}

func answerability(result explorer.Result) string {
	switch {
	case len(result.Hits) == 0:
		return "unanswerable"
	case retrievalStatus(result.Hits) == "weak_evidence":
		return "partial"
	case needsSupportingEvidence(result) && len(result.SupportingHits) == 0:
		return "partial"
	case result.Plan.NeedCallGraph && len(result.TraceHits) == 0:
		return "partial"
	case len(result.Warnings) != 0:
		return "partial"
	default:
		return "answerable"
	}
}

func confidenceBand(result explorer.Result) string {
	switch answerability(result) {
	case "unanswerable":
		return "none"
	case "partial":
		if topConfidence(result.Hits) == "high" {
			return "medium"
		}
		return topConfidence(result.Hits)
	default:
		return topConfidence(result.Hits)
	}
}

func qualityFlags(result explorer.Result) []string {
	flags := []string{}
	if len(result.Hits) == 0 {
		flags = append(flags, "no_hits")
	}
	if retrievalStatus(result.Hits) == "weak_evidence" {
		flags = append(flags, "weak_top_confidence")
	}
	if result.Plan.Ambiguous {
		flags = append(flags, "ambiguous_plan")
	}
	if needsSupportingEvidence(result) && len(result.SupportingHits) == 0 {
		flags = append(flags, "supporting_gap")
	}
	if result.Plan.NeedCallGraph && len(result.TraceHits) == 0 {
		flags = append(flags, "trace_gap")
	}
	if len(result.Warnings) != 0 {
		flags = append(flags, "warnings_present")
	}
	if len(flags) == 0 {
		return []string{"none"}
	}
	return flags
}

func writeContractSection(b *strings.Builder, label string, hits []tools.Hit, maxItems int) {
	b.WriteString(label + ":\n")
	hits = uniqueDisplayHits(hits)
	if len(hits) == 0 {
		b.WriteString("- none\n")
		return
	}
	count := 0
	for _, hit := range hits {
		if count >= maxItems {
			break
		}
		b.WriteString(fmt.Sprintf("- %s:%d-%d", hit.File, hit.LineStart, hit.LineEnd))
		if hit.Symbol != "" {
			b.WriteString(" " + shortSymbol(hit.Symbol))
		}
		b.WriteString(" :: " + nonEmpty(hit.Why, hit.EvidenceType, "evidence"))
		b.WriteString("\n")
		count++
	}
	if count == 0 {
		b.WriteString("- none\n")
	}
}

func resultGaps(result explorer.Result) []string {
	gaps := []string{}
	if len(result.Hits) == 0 {
		return []string{"no retrieval evidence"}
	}
	if retrievalStatus(result.Hits) != "grounded" {
		gaps = append(gaps, "top evidence not grounded")
	}
	if result.Plan.Intent == "mixed" && len(result.SupportingHits) == 0 {
		gaps = append(gaps, "missing supporting evidence for multi-hop query")
	}
	if result.Plan.NeedCallGraph && len(result.TraceHits) == 0 {
		gaps = append(gaps, "missing trace evidence")
	}
	if len(result.Warnings) != 0 {
		gaps = append(gaps, result.Warnings[0])
	}
	if len(gaps) > 2 {
		gaps = gaps[:2]
	}
	return gaps
}

func recommendedAction(result explorer.Result) string {
	switch {
	case len(result.Hits) == 0:
		return "clarify_or_retrieve"
	case result.Plan.Ambiguous:
		return "clarify"
	case retrievalStatus(result.Hits) == "weak_evidence":
		return "re-retrieve"
	case result.Plan.NeedCallGraph && len(result.TraceHits) == 0:
		return "trace"
	case needsSupportingEvidence(result) && len(result.SupportingHits) == 0:
		return "re-retrieve"
	case len(result.Warnings) != 0:
		return "verify"
	default:
		return "reason"
	}
}

func traceSummary(hits []tools.Hit) string {
	parts := make([]string, 0, 2)
	if callers := collectTraceSymbols(hits, "caller"); len(callers) != 0 {
		parts = append(parts, "- callers: "+strings.Join(callers, " <- "))
	}
	if callees := collectTraceSymbols(hits, "callee"); len(callees) != 0 {
		parts = append(parts, "- callees: "+strings.Join(callees, " -> "))
	}
	return strings.Join(parts, "\n")
}

func inlineTraceSummary(hits []tools.Hit) string {
	summary := traceSummary(hits)
	summary = strings.ReplaceAll(summary, "\n", " | ")
	summary = strings.ReplaceAll(summary, "- ", "")
	return strings.TrimSpace(summary)
}

func collectTraceSymbols(hits []tools.Hit, wantRole string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 3)
	for _, hit := range uniqueDisplayHits(hits) {
		if hit.EvidenceType != "trace" {
			continue
		}
		if wantRole != "" && strings.TrimSpace(hit.SupportRole) != wantRole {
			continue
		}
		symbol := shortSymbol(nonEmpty(hit.Symbol, hit.File))
		if symbol == "" || seen[symbol] {
			continue
		}
		seen[symbol] = true
		out = append(out, symbol)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

func uniqueDisplayHits(hits []tools.Hit) []tools.Hit {
	seen := map[string]bool{}
	out := make([]tools.Hit, 0, len(hits))
	for _, hit := range hits {
		key := displayHitKey(hit)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, hit)
	}
	return out
}

func needsSupportingEvidence(result explorer.Result) bool {
	return result.Plan.Intent == "mixed" || len(result.Plan.Slots) > 1
}

func fallbackSupportingHits(allHits []tools.Hit, primary []tools.Hit, maxItems int) []tools.Hit {
	if maxItems <= 0 {
		return nil
	}
	primaryKeys := map[string]bool{}
	for _, hit := range uniqueDisplayHits(primary) {
		primaryKeys[displayHitKey(hit)] = true
	}
	out := make([]tools.Hit, 0, maxItems)
	for _, hit := range uniqueDisplayHits(allHits) {
		if primaryKeys[displayHitKey(hit)] {
			continue
		}
		if hit.EvidenceType == "trace" || hit.SupportRole == "caller" || hit.SupportRole == "callee" {
			continue
		}
		out = append(out, hit)
		if len(out) >= maxItems {
			break
		}
	}
	return out
}

func displayHitKey(hit tools.Hit) string {
	return fmt.Sprintf("%s:%d:%d:%s:%s", hit.File, hit.LineStart, hit.LineEnd, hit.Symbol, hit.Why)
}

func limitCitations(hits []tools.Hit, maxItems int) []tools.Hit {
	hits = uniqueDisplayHits(hits)
	if len(hits) <= maxItems {
		return hits
	}
	return hits[:maxItems]
}

func writeEvidenceSection(b *strings.Builder, hits []tools.Hit, label string, maxItems int) {
	hits = uniqueDisplayHits(hits)
	if len(hits) == 0 || maxItems <= 0 {
		return
	}
	b.WriteString(label)
	b.WriteString(":\n")
	for i, hit := range hits {
		if i >= maxItems {
			break
		}
		b.WriteString(fmt.Sprintf("%d. [%s %d %s %s] %s:%d-%d", i+1, strings.ToUpper(hit.Confidence), hit.Score, nonEmpty(hit.EvidenceType, "supporting"), nonEmpty(hit.Lane, hit.Family, hit.Source), hit.File, hit.LineStart, hit.LineEnd))
		if hit.Symbol != "" {
			b.WriteString(" ")
			b.WriteString(shortSymbol(hit.Symbol))
		}
		b.WriteString("\n")
		if hit.Why != "" {
			b.WriteString("   why: ")
			b.WriteString(hit.Why)
			b.WriteString("\n")
		}
	}
}

func writeCompactEvidence(b *strings.Builder, idx int, hits []tools.Hit, maxItems int) int {
	for i, hit := range uniqueDisplayHits(hits) {
		if i >= maxItems {
			break
		}
		b.WriteString(fmt.Sprintf("%d. [%s %d %s] %s:%d-%d", idx, strings.ToUpper(hit.Confidence), hit.Score, nonEmpty(hit.EvidenceType, "supporting"), hit.File, hit.LineStart, hit.LineEnd))
		if hit.Symbol != "" {
			b.WriteString(fmt.Sprintf(" (%s)", shortSymbol(hit.Symbol)))
		}
		if hit.Why != "" {
			b.WriteString(" :: ")
			b.WriteString(hit.Why)
		}
		b.WriteString("\n")
		idx++
	}
	return idx
}
