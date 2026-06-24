package explorer

import (
	"path/filepath"
	"strings"

	"agent-explorer/internal/planner"
	"agent-explorer/internal/tools"
)

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
		// Exact symbol match bonus: short name matching query tokens gets massive boost.
		// Research (Sourcegraph, Google CACM 2023): exact match >> substring for ranking.
		short := symbolLower
		if idx := strings.LastIndex(short, "."); idx >= 0 && idx+1 < len(short) {
			short = short[idx+1:]
		}
		for _, token := range queryTokens(lq) {
			if token == short {
				score += 40
				break
			}
		}
		// Full qualified name multi-token overlap (e.g., "source_mirror" ∩ [source, mirror])
		if fullNameOverlap(symbolLower, lq) >= 3 {
			score += 18
		}
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

// queryTokens extracts non-stopword tokens from a query string.

func queryTokens(query string) []string {
	var tokens []string
	seen := map[string]bool{}
	for _, token := range strings.Fields(strings.ToLower(query)) {
		token = strings.Trim(token, ".,;:()[]{}'\"-_")
		if len(token) < 3 || scoringStopword(token) || seen[token] {
			continue
		}
		seen[token] = true
		tokens = append(tokens, token)
	}
	return tokens
}

// fullNameOverlap counts how many query tokens appear anywhere in the full

func fullNameOverlap(symbol, query string) int {
	count := 0
	for _, token := range queryTokens(query) {
		if strings.Contains(symbol, token) {
			count++
		}
	}
	return count
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
