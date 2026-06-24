package explorer

import (
	"strings"
	"agent-explorer/internal/planner"
	"agent-explorer/internal/tools"
	"path/filepath"
	"regexp"
)

func slotKey(slot planner.EvidenceSlot) string {
	return strings.TrimSpace(slot.Role) + "|" + strings.TrimSpace(slot.Need)
}

func hasConfigRole(plan planner.Plan) bool {
	for _, slot := range plan.Slots {
		if slot.Role == "config" {
			return true
		}
	}
	return false
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

func containsTool(items []string, tool string) bool {
	for _, item := range items {
		if item == tool {
			return true
		}
	}
	return false
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

func markLane(hits []tools.Hit, lane string) {
	for i := range hits {
		hits[i].Lane = lane
	}
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

func nonEmptyLane(hit tools.Hit) string {
	if strings.TrimSpace(hit.Lane) != "" {
		return hit.Lane
	}
	if strings.TrimSpace(hit.Family) != "" {
		return hit.Family
	}
	return hit.Source
}
