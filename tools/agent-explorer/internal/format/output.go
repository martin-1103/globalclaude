package format

import (
	"encoding/json"
	"fmt"
	"strings"

	"agent-explorer/internal/explorer"
	"agent-explorer/internal/tools"
)

func FinalAnswer(result explorer.Result, citationOnly bool, agentMode bool, debugRetrieval bool) string {
	if citationOnly {
		return citationBlock(result.Hits)
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
	b.WriteString("Retrieval Pack:\n")
	b.WriteString("Status: ")
	b.WriteString(retrievalStatus(result.Hits))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("Intent: %s\n", nonEmpty(result.Plan.Intent, "unknown")))
	b.WriteString(fmt.Sprintf("Primary: %s\n", nonEmpty(result.Plan.PrimaryTool, "unknown")))
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
		for i, hit := range result.Suppressed {
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
	for _, hit := range hits {
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
	b.WriteString("retrieval_pack\n")
	b.WriteString("status=" + retrievalStatus(result.Hits))
	b.WriteString(" primary=" + nonEmpty(result.Plan.PrimaryTool, "unknown"))
	if lane := dominantLane(result.Hits); lane != "" {
		b.WriteString(" lane=" + lane)
	}
	terms := compactTerms(result.Plan.SearchTerms)
	if len(terms) != 0 {
		b.WriteString(" terms=" + strings.Join(terms, "|"))
	}
	b.WriteString("\n")
	idx := 1
	idx = writeCompactEvidence(&b, idx, result.PrimaryHits, 3)
	idx = writeCompactEvidence(&b, idx, result.SupportingHits, 1)
	_ = writeCompactEvidence(&b, idx, result.TraceHits, 2)
	if debugRetrieval && len(result.Suppressed) != 0 {
		b.WriteString("Suppressed: ")
		for i, hit := range result.Suppressed {
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

func writeEvidenceSection(b *strings.Builder, hits []tools.Hit, label string, maxItems int) {
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
	for i, hit := range hits {
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
