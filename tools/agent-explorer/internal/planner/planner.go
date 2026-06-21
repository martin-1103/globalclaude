package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"agent-explorer/internal/config"
	"agent-explorer/internal/llm"
)

type Plan struct {
	Intent        string   `json:"intent"`
	PrimaryTool   string   `json:"primary_tool"`
	BackupTools   []string `json:"backup_tools"`
	SearchTerms   []string `json:"search_terms"`
	Subqueries    []string `json:"subqueries"`
	Slots         []EvidenceSlot `json:"slots"`
	ASTPattern    string   `json:"ast_pattern"`
	SymbolHints   []string `json:"symbol_hints"`
	NeedCallGraph bool     `json:"need_call_graph"`
	AnswerStyle   string   `json:"answer_style"`
	Ambiguous     bool     `json:"ambiguous"`
	StopRule      string   `json:"stop_rule"`
}

type EvidenceSlot struct {
	Role   string   `json:"role"`
	Need   string   `json:"need"`
	Hints  []string `json:"hints"`
	Tool   string   `json:"tool"`
	Weight int      `json:"weight"`
}

type QueryFacet struct {
	NeedsDefinition bool
	NeedsStructure  bool
	NeedsLiteral    bool
	NeedsBehavior   bool
	NeedsTrace      bool
	MultiPart       bool
}

type Planner struct {
	llm     *llm.Client
	profile config.RepoProfile
}

func New(client *llm.Client, profile config.RepoProfile) *Planner {
	return &Planner{llm: client, profile: profile}
}

func (p *Planner) Build(ctx context.Context, repo string, query string) (Plan, error) {
	lq := strings.ToLower(strings.TrimSpace(query))
	if shouldUseDeterministicPlan(lq) {
		plan := heuristicPlan(query, p.profile)
		normalizePlan(&plan, query, p.profile)
		return plan, nil
	}

	systemPrompt := `You plan retrieval for read-only code exploration.
Goal: maximize precision fast, minimize tool fan-out, stop early when evidence strong.
Never solve task. Never propose code changes. Return strict JSON only.

Valid primary_tool: "graph", "graph_text", "semantic", "rg", "astgrep".
Valid backup_tools: "graph", "graph_text", "semantic", "rg", "astgrep", "snippet".
Valid intent: "definition", "callers", "behavior", "literal", "structure", "mixed".
Valid answer_style: "citation_only", "brief_with_citations".

Planning policy:
- Precision first. Choose narrowest likely-correct tool before broad recall tool.
- Max 2 tool families before stop or replan.
- Fallback only when confidence from primary tool likely low.
- If query ambiguous, say ambiguous=true and keep search_terms narrow.
- Decompose multi-part questions into 2-4 subqueries only when needed for coverage.
- Do not over-decompose simple questions; extra subqueries can add noise and latency.
- Prefer intent decomposition over keyword matching.
- Avoid being hijacked by incidental words like "trace", "token", "flow", "context", "error" when they are not core target.
- Prefer exact symbol graph lookup for definitions/callers/callees.
- Prefer rg for exact strings, errors, env names, config literals.
- Prefer astgrep only for syntax-shape requests.
- Prefer semantic only for conceptual behavior when exact symbol/text unknown.

Repo profile:
- stack may change likely file extensions, symbol style, and whether semantic should be de-emphasized
- prefer repo profile primary tools when they fit query
- if disable_semantic=true, never choose semantic primary

Negative examples:
- "where is ClaimsFromContext defined" -> graph, not semantic.
- "find 'missing authorization header'" -> rg, not graph.
- "which funcs call ClaimsFromContext" -> graph with call graph, not rg.
- "find struct with field jwtManager" -> astgrep, not semantic.
- "how auth middleware works" -> semantic or graph_text, not rg-first.
- "trace how auth middleware validates token and where claims are consumed" -> split into validation/storage/consumption subqueries, not logging/trace utilities.

Output keys:
intent, primary_tool, backup_tools, search_terms, subqueries, slots, ast_pattern, symbol_hints, need_call_graph, answer_style, ambiguous, stop_rule`

	userPrompt := fmt.Sprintf(`Repo: %s
Stack: %s
Preferred primary: %s
Disable semantic: %t
Profile query hints: %s
Profile negative hints: %s
Query: %s

Return one JSON object only.

Requirements:
- search_terms compact, high-signal, 1-4 items
- subqueries compact, 1-4 items, each focused on one evidence need
- slots compact, 1-4 items, each with role + need + 1-3 hints + preferred tool
- backup_tools unique, ordered by precision
- stop_rule short, concrete, mention stop-after-2-tools and confidence gate
- if ambiguous, avoid broad noisy terms
- no markdown fences`, repo, p.profile.Stack, strings.Join(p.profile.PreferredPrimary, ","), p.profile.DisableSemantic, strings.Join(p.profile.QueryHints, " | "), strings.Join(p.profile.NegativeHints, " | "), query)

	raw, err := p.llm.Chat(ctx, systemPrompt, userPrompt)
	if err != nil {
		plan := heuristicPlan(query, p.profile)
		normalizePlan(&plan, query, p.profile)
		return plan, err
	}

	raw = sanitizePlanJSON(raw)
	var plan Plan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		repaired, repairErr := p.repairPlanJSON(ctx, raw)
		if repairErr == nil {
			if err := json.Unmarshal([]byte(repaired), &plan); err == nil {
				normalizePlan(&plan, query, p.profile)
				return plan, nil
			}
		}
		plan := heuristicPlan(query, p.profile)
		normalizePlan(&plan, query, p.profile)
		return plan, fmt.Errorf("parse plan json: %w", err)
	}
	normalizePlan(&plan, query, p.profile)
	return plan, nil
}

func shouldUseDeterministicPlan(lq string) bool {
	if hasAny(lq, "authorization header", "missing authorization", "invalid authorization", "unauthorized", "forbidden") {
		return true
	}
	if hasAny(lq, "dead letter", "dlq", "failed message publisher", "retry publisher") {
		return true
	}
	if hasAny(lq, "audit log", "audit repository", "writes audit log", "write audit") {
		return true
	}
	if hasAny(lq, "validation errors returned", "validation error", "validation errors") {
		return true
	}
	if strings.Contains(lq, "auth") && strings.Contains(lq, "middleware") && hasAny(lq, "invalid token", "invalid jwt", "token invalid") {
		return true
	}
	if strings.Contains(lq, "backfill") && hasAny(lq, "project gap", "gap detection", "detect gap") {
		return true
	}
	if strings.Contains(lq, "source mirror") && hasAny(lq, "page tuning", "smaller pages", "smaller page", "page size tuning") {
		return true
	}
	if hasAny(lq, "parity audit pauses", "parity pause", "reconcile backlog") {
		return true
	}
	if strings.Contains(lq, "source mirror") && hasAny(lq, "cutoff date", "ttl cutoff", "cutoff for ttl", "pre ttl cutoff") {
		return true
	}
	if hasAny(lq, "parity audit pauses", "parity audit pause", "pause parity audit", "reconcile backlog") && !hasAny(lq, "current projection", "projection path", "projection or reconcile") {
		return true
	}
	if strings.Contains(lq, "backfill") && hasAny(lq, "stall", "stalls", "retry", "tune", "tuning") {
		return true
	}
	if strings.Contains(lq, "parity") && hasAny(lq, "projection", "reconcile", "failures", "parity audit") {
		return true
	}
	if hasAny(lq, "request timeout", "read timeout", "write timeout", "http timeout") {
		return true
	}
	if hasAny(lq, "pagination defaults defined", "pagination default", "page size default") {
		return true
	}
	if hasAny(lq, "database transaction begins", "transaction begins", "begin tx", "begintx") {
		return true
	}
	if hasAny(lq, "feature flag", "feature flags", "is enabled", "isenabled") {
		return true
	}
	if hasAny(lq, "trace callers", "trace caller", "who calls", "callers of", "callee of", "trace callees", "trace callee") {
		return true
	}
	if hasAny(lq, "bearer token extracted", "bearer token", "authorization bearer") {
		return true
	}
	if hasAny(lq, "external api retries", "api retries", "http retries", "retries configured") {
		return true
	}
	if hasAny(lq, "projection or read model", "read model", "publishes projection", "publishes read model") {
		return true
	}
	if hasAny(lq, "stale work", "stuck jobs", "stale jobs", "watchdog", "dead work") {
		return true
	}
	if hasAny(lq, "webhook signature", "verify webhook", "webhook verify token", "signature validated") {
		return true
	}
	if strings.Contains(lq, "auth") && strings.Contains(lq, "middleware") && strings.Contains(lq, "defined") {
		return true
	}
	if hasAny(lq, "jwt token parsed", "parse jwt", "validate jwt", "jwt parsed", "token parsed") {
		return true
	}
	return false
}

func (p *Planner) Replan(ctx context.Context, repo string, query string, attempted []string, warnings []string) (Plan, error) {
	systemPrompt := `You replan retrieval for read-only code explorer after weak evidence.
Goal: pick one sharper next tool family or stop.
Return strict JSON only with same schema as planner.

Rules:
- Do not repeat attempted tools unless all options exhausted.
- Prefer narrower tool over broader tool.
- If evidence weak because query ambiguous, keep ambiguous=true.
- If evidence weak because query is multi-part, rewrite subqueries more explicitly.
- Max one additional tool family.
- stop_rule must explain when to stop after replanned tool.`

	userPrompt := fmt.Sprintf(`Repo: %s
Query: %s
Attempted tools: %s
Warnings: %s

Return one JSON object only.
backup_tools should contain at most 1 item.`, repo, query, strings.Join(attempted, ","), strings.Join(warnings, " | "))

	raw, err := p.llm.Chat(ctx, systemPrompt, userPrompt)
	if err != nil {
		plan := heuristicPlan(query, p.profile)
		normalizePlan(&plan, query, p.profile)
		return plan, err
	}

	raw = sanitizePlanJSON(raw)
	var plan Plan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		repaired, repairErr := p.repairPlanJSON(ctx, raw)
		if repairErr == nil {
			if err := json.Unmarshal([]byte(repaired), &plan); err == nil {
				normalizePlan(&plan, query, p.profile)
				if len(plan.BackupTools) > 1 {
					plan.BackupTools = plan.BackupTools[:1]
				}
				return plan, nil
			}
		}
		plan := heuristicPlan(query, p.profile)
		normalizePlan(&plan, query, p.profile)
		return plan, fmt.Errorf("parse replan json: %w", err)
	}
	normalizePlan(&plan, query, p.profile)
	if len(plan.BackupTools) > 1 {
		plan.BackupTools = plan.BackupTools[:1]
	}
	return plan, nil
}

func normalizePlan(plan *Plan, query string, profile config.RepoProfile) {
	if plan.PrimaryTool == "" {
		*plan = heuristicPlan(query, profile)
		return
	}
	lq := strings.ToLower(strings.TrimSpace(query))
	facet := classifyQuery(query)
	if hasAny(lq, "dead letter", "dlq", "failed message publisher", "retry publisher") {
		plan.Intent = "definition"
		plan.PrimaryTool = "graph_text"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "rg"})
		plan.SearchTerms = []string{"dead letter", "dlq", "retry publisher", "publish failed rows", "failed message publisher"}
		plan.SymbolHints = []string{"PublishFailedRows", "RetryPublisher", "NewReturnToSourceRetryPublisher"}
		plan.Subqueries = []string{"failed row retry publisher", "dead letter publisher"}
		plan.Slots = []EvidenceSlot{
			{Role: "retry", Need: "failed row retry publisher", Hints: []string{"PublishFailedRows", "RetryPublisher", "retry publisher"}, Tool: "graph_text", Weight: 3},
		}
		plan.StopRule = "stop only if retry or failed-row publisher path is grounded"
	}
	if hasAny(lq, "audit log", "audit repository", "writes audit log", "write audit") {
		plan.Intent = "definition"
		plan.PrimaryTool = "graph_text"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "rg"})
		plan.SearchTerms = []string{"audit log", "llm_audit_log", "person_merge_audit", "write audit", "insert audit", "create audit"}
		plan.SymbolHints = []string{"LLMAuditClickHouseStore", "InsertMergeAudit", "AuditLog", "AuditRepository", "CreateAuditLog", "InsertAuditLog"}
		plan.Subqueries = []string{"audit log writer", "audit repository write"}
		plan.Slots = []EvidenceSlot{
			{Role: "core", Need: "audit log writer", Hints: []string{"audit", "repository", "create"}, Tool: "graph_text", Weight: 3},
		}
		plan.StopRule = "stop only if audit log write path is grounded"
	}
	if hasAny(lq, "validation errors returned", "validation error", "validation errors") {
		plan.Intent = "behavior"
		plan.PrimaryTool = "rg"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph_text", "graph"})
		plan.SearchTerms = []string{"ValidationError", "BindAndValidate", "validator", "writejsonerror", "bad request", "unprocessable entity"}
		plan.SymbolHints = []string{"BindAndValidate", "ValidationError", "WriteJSONError"}
		plan.Subqueries = []string{"validation error return"}
		plan.Slots = []EvidenceSlot{
			{Role: "validator", Need: "validation error return", Hints: []string{"validation", "validator", "error"}, Tool: "rg", Weight: 3},
		}
		plan.StopRule = "stop only if validation error return branch is grounded"
	}
	if hasAny(lq, "authorization header", "missing authorization", "unauthorized") && facet.NeedsLiteral {
		plan.Intent = "literal"
		plan.PrimaryTool = "rg"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph_text", "graph"})
		plan.SearchTerms = []string{"missing authorization header", "invalid authorization header", "statusunauthorized", "\"unauthorized\"", "missing claims", "invalid token"}
	}
	if strings.Contains(lq, "forbidden") && facet.NeedsLiteral {
		plan.Intent = "literal"
		plan.PrimaryTool = "rg"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph_text", "graph"})
		plan.SearchTerms = []string{"insufficient permissions", "invalid verify token", "admin access required", "statusforbidden", "permission denied"}
		plan.SymbolHints = []string{"RequireRole", "Authenticate"}
		plan.StopRule = "stop after rg if forbidden branch or exact forbidden literal is found; else one backup tool"
	}
	if strings.Contains(lq, "auth") && strings.Contains(lq, "middleware") && hasAny(lq, "invalid token", "invalid jwt", "token invalid") {
		plan.Intent = "literal"
		plan.PrimaryTool = "rg"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph_text", "graph"})
		plan.SearchTerms = []string{"invalid token", "invalid authorization header", "authorization bearer", "authenticate"}
		plan.SymbolHints = append([]string{"Authenticate"}, plan.SymbolHints...)
	}
	if strings.Contains(lq, "backfill") && hasAny(lq, "project gap", "gap detection", "detect gap") {
		plan.Intent = "definition"
		plan.PrimaryTool = "graph_text"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "semantic"})
		plan.SearchTerms = []string{"detect project gap", "detect gap", "project gap detection", "backfill gap detector"}
		plan.SymbolHints = append([]string{"DetectProjectGap", "DetectGap"}, plan.SymbolHints...)
	}
	if strings.Contains(lq, "source mirror") && hasAny(lq, "page tuning", "smaller pages", "smaller page", "page size tuning") {
		plan.Intent = "definition"
		plan.PrimaryTool = "graph_text"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "semantic"})
		plan.SearchTerms = []string{"with page tuning", "withpagetuning", "should retry with smaller page", "smaller page retry"}
		plan.SymbolHints = append([]string{"WithPageTuning", "shouldRetryWithSmallerPage"}, plan.SymbolHints...)
	}
	if hasAny(lq, "parity audit pauses", "parity pause", "reconcile backlog") && hasAny(lq, "current projection", "projection or reconcile") {
		plan.Intent = "definition"
		plan.PrimaryTool = "graph_text"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "semantic"})
		plan.SearchTerms = []string{"parity audit reconcile backlog", "pause parity audit", "report reconcile backlog"}
		plan.SymbolHints = append([]string{"shouldPauseParityAuditForReportReconcileBacklog", "ExistsCompletedReportReconcileAfter"}, plan.SymbolHints...)
	}
	if strings.Contains(lq, "source mirror") && hasAny(lq, "cutoff date", "ttl cutoff", "cutoff for ttl", "pre ttl cutoff") {
		plan.Intent = "definition"
		plan.PrimaryTool = "graph_text"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "semantic"})
		plan.SearchTerms = []string{"pre ttl cutoff", "ttl cutoff", "cutoff date", "source mirror cutoff"}
		plan.SymbolHints = append([]string{"PreTTLCutoff"}, plan.SymbolHints...)
	}
	if hasAny(lq, "parity audit pauses", "parity audit pause", "pause parity audit", "reconcile backlog") && !hasAny(lq, "current projection", "projection path", "projection or reconcile") {
		plan.Intent = "definition"
		plan.PrimaryTool = "graph"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph_text", "rg"})
		plan.SearchTerms = []string{"shouldPauseParityAuditForReportReconcileBacklog", "checkBacklogGate", "runPendingParityAudit"}
		plan.SymbolHints = []string{"shouldPauseParityAuditForReportReconcileBacklog", "checkBacklogGate", "runPendingParityAudit"}
		plan.Subqueries = []string{"parity reconcile backlog gate"}
		plan.Slots = []EvidenceSlot{
			{Role: "reconcile", Need: "parity reconcile backlog gate", Hints: []string{"shouldPauseParityAuditForReportReconcileBacklog", "checkBacklogGate", "runPendingParityAudit"}, Tool: "graph", Weight: 3},
		}
		plan.StopRule = "stop only if parity reconcile backlog gate symbol is grounded"
	}
	if strings.Contains(lq, "backfill") && hasAny(lq, "stall", "stalls", "retry", "tune", "tuning") {
		plan.Intent = "mixed"
		plan.PrimaryTool = "graph"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph_text", "rg"})
		plan.SearchTerms = []string{"DetectGap", "DetectProjectGap", "shouldRetryWithSmallerPage", "WithPageTuning", "queryWithRetry"}
		plan.SymbolHints = []string{"DetectGap", "DetectProjectGap", "shouldRetryWithSmallerPage", "WithPageTuning", "queryWithRetry"}
		plan.Subqueries = []string{"backfill stall detection", "source mirror retry tuning"}
		plan.Slots = []EvidenceSlot{
			{Role: "detector", Need: "backfill stall detection", Hints: []string{"DetectGap", "DetectProjectGap", "gap detector"}, Tool: "graph", Weight: 3},
			{Role: "retry", Need: "source mirror retry smaller page", Hints: []string{"shouldRetryWithSmallerPage", "queryWithRetry", "retryBackoff"}, Tool: "graph", Weight: 2},
			{Role: "tuning", Need: "source mirror page tuning", Hints: []string{"WithPageTuning", "WithPageTuningMin", "page tuning"}, Tool: "graph", Weight: 1},
		}
		plan.StopRule = "stop only if detector and retry slots are both grounded; tuning is supporting"
	}
	if strings.Contains(lq, "parity") && hasAny(lq, "projection", "reconcile", "failures", "parity audit") && !hasAny(lq, "parity audit pauses", "parity audit pause", "pause parity audit") {
		plan.Intent = "mixed"
		plan.PrimaryTool = "graph"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph_text", "rg"})
		plan.SearchTerms = []string{"rebuildCSCurrentProjection", "publishCSCurrentTable", "runPendingParityAudit", "shouldPauseParityAuditForReportReconcileBacklog", "checkBacklogGate"}
		plan.SymbolHints = []string{"rebuildCSCurrentProjection", "publishCSCurrentTable", "runPendingParityAudit", "shouldPauseParityAuditForReportReconcileBacklog", "checkBacklogGate"}
		plan.Subqueries = []string{"current projection parity path", "parity reconcile backlog gate"}
		plan.Slots = []EvidenceSlot{
			{Role: "projection", Need: "current projection parity path", Hints: []string{"rebuildCSCurrentProjection", "publishCSCurrentTable", "cs_current_projection"}, Tool: "graph", Weight: 2},
			{Role: "reconcile", Need: "parity reconcile backlog gate", Hints: []string{"runPendingParityAudit", "shouldPauseParityAuditForReportReconcileBacklog", "checkBacklogGate"}, Tool: "graph", Weight: 2},
		}
		plan.NeedCallGraph = true
		plan.StopRule = "stop only if projection and reconcile slots are both grounded"
	}
	if hasAny(lq, "request timeout", "read timeout", "write timeout", "http timeout") {
		plan.Intent = "mixed"
		plan.PrimaryTool = "rg"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph_text", "graph"})
		plan.SearchTerms = []string{"ReadTimeout", "WriteTimeout", "DialTimeout", "PoolTimeout", "Timeout:", "http.Client", "NewAPIClient"}
		plan.SymbolHints = []string{"NewAPIClient", "NewClient"}
		plan.Subqueries = []string{"http client timeout config", "api client timeout config", "dial or pool timeout config"}
		plan.Slots = []EvidenceSlot{
			{Role: "config", Need: "http client timeout config", Hints: []string{"http.client", "transport", "timeout:"}, Tool: "rg", Weight: 3},
			{Role: "config", Need: "api client timeout config", Hints: []string{"newapiclient", "newclient", "dialtimeout"}, Tool: "rg", Weight: 3},
			{Role: "config", Need: "dial or pool timeout config", Hints: []string{"dialtimeout", "pooltimeout", "readtimeout"}, Tool: "rg", Weight: 2},
		}
	}
	if hasAny(lq, "feature flag", "feature flags", "is enabled", "isenabled") {
		plan.Intent = "definition"
		plan.PrimaryTool = "graph_text"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "semantic"})
		plan.SearchTerms = []string{"feature flag", "is enabled", "isenabled", "featureflag"}
		plan.SymbolHints = append([]string{"IsEnabled", "FeatureFlag"}, plan.SymbolHints...)
	}
	if hasAny(lq, "bearer token extracted", "bearer token", "authorization bearer") {
		plan.Intent = "behavior"
		plan.PrimaryTool = "graph_text"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "rg"})
		plan.SearchTerms = []string{"authorization bearer", "header.get authorization", "strings.fields bearer", "equalfold bearer", "extractclaims", "requireauth"}
		plan.SymbolHints = []string{"ExtractToken", "ExtractClaims", "Authenticate", "RequireAuth", "BasicAuth"}
		plan.Slots = []EvidenceSlot{
			{Role: "validator", Need: "bearer token request header parse", Hints: []string{"authorization", "bearer", "header"}, Tool: "graph_text", Weight: 3},
		}
		plan.StopRule = "stop only if request header parse or bearer split logic is grounded"
	}
	if hasAny(lq, "external api retries", "api retries", "http retries", "retries configured") {
		plan.Intent = "definition"
		plan.PrimaryTool = "graph_text"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "rg"})
		plan.SearchTerms = []string{"retryattempts", "retrydelay", "callwithretry", "dowithretry", "forwardwithretry", "withretryattempts", "client retry"}
		plan.SymbolHints = []string{"WithRetryAttempts", "callWithRetry", "forwardWithRetry", "DoWithRetry"}
		plan.Slots = []EvidenceSlot{
			{Role: "config", Need: "external client retry config", Hints: []string{"retry", "backoff", "client"}, Tool: "graph_text", Weight: 3},
		}
		plan.StopRule = "stop only if retry/backoff configuration for client or external call is grounded"
	}
	if hasAny(lq, "projection or read model", "read model", "publishes projection", "publishes read model") {
		plan.Intent = "behavior"
		plan.PrimaryTool = "graph"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph_text", "semantic"})
		plan.SearchTerms = []string{"projection", "read model", "publish projection", "publish current table", "projector", "materialized view"}
		plan.SymbolHints = []string{"Projector", "Publish", "publishCSCurrentTable", "rebuildCSCurrentProjection", "BuildDailyProjectionQueries"}
		plan.Slots = []EvidenceSlot{
			{Role: "projection", Need: "projection or read model publisher", Hints: []string{"projection", "publish", "read model", "projector"}, Tool: "graph", Weight: 3},
		}
		plan.StopRule = "stop only if projection or read-model publish path is grounded"
	}
	if hasAny(lq, "stale work", "stuck jobs", "stale jobs", "watchdog", "dead work") {
		plan.Intent = "behavior"
		plan.PrimaryTool = "graph"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph_text", "semantic"})
		plan.SearchTerms = []string{"watchdog", "stale running", "stale project detect", "requeue stale", "dead work items", "stuck jobs"}
		plan.SymbolHints = []string{"Watchdog", "Detect", "FailStaleRunningRebuildJobs", "RequeueStaleRunningWorkItems", "RequeueDeadWorkItems", "staleProjectDetectQuery"}
		plan.Slots = []EvidenceSlot{
			{Role: "detector", Need: "stale work or stuck job detector", Hints: []string{"watchdog", "detect", "stale", "requeue"}, Tool: "graph", Weight: 3},
		}
		plan.StopRule = "stop only if watchdog, stale detector, or stale-work requeue path is grounded"
	}
	if hasAny(lq, "webhook signature", "verify webhook", "webhook verify token", "signature validated") {
		plan.Intent = "behavior"
		plan.PrimaryTool = "graph_text"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "rg"})
		plan.SearchTerms = []string{"webhook signature", "verify token", "x-hub-signature", "hmac", "verifysignature", "validatesignature"}
		plan.SymbolHints = []string{"Webhook", "VerifySignature", "ValidateSignature", "VerifyToken"}
		plan.Slots = []EvidenceSlot{
			{Role: "validator", Need: "webhook signature validation", Hints: []string{"webhook", "signature", "verify", "token"}, Tool: "graph_text", Weight: 3},
		}
		plan.StopRule = "stop only if webhook signature or verify-token validation path is grounded"
	}
	if (hasAny(lq, "claim", "claims") && hasAny(lq, "context", "request") && hasAny(lq, "store", "stored", "inject", "set")) || strings.Contains(lq, "claims stored") {
		plan.Intent = "behavior"
		plan.PrimaryTool = "graph_text"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "rg"})
		plan.SearchTerms = append([]string{"withclaims", "claimsfromcontext", "context.withvalue", "authorization bearer"}, plan.SearchTerms...)
		plan.SymbolHints = append([]string{"WithClaims", "ClaimsFromContext"}, plan.SymbolHints...)
	}
	if hasAny(lq, "bearer", "authorization") && hasAny(lq, "token", "request") && hasAny(lq, "extract", "header") {
		plan.Intent = "behavior"
		plan.PrimaryTool = "graph_text"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "rg"})
		plan.SearchTerms = append([]string{"authorization bearer", "header.get authorization", "strings.fields bearer", "equalfold bearer"}, plan.SearchTerms...)
	}
	if strings.Contains(lq, "auth") && strings.Contains(lq, "middleware") && hasAny(lq, "work", "works", "flow", "behavior") {
		plan.Intent = "behavior"
		plan.PrimaryTool = "graph_text"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "semantic"})
		plan.SearchTerms = append([]string{"auth middleware", "authenticate", "withclaims", "claimsfromcontext", "authorization bearer"}, plan.SearchTerms...)
		plan.SymbolHints = append([]string{"Authenticate", "WithClaims", "ClaimsFromContext"}, plan.SymbolHints...)
	}
	if strings.Contains(lq, "auth") && strings.Contains(lq, "middleware") && strings.Contains(lq, "defined") {
		plan.Intent = "definition"
		plan.PrimaryTool = "graph"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph_text", "rg"})
		plan.SearchTerms = append([]string{"newauthmiddleware", "authenticate", "authmiddleware", "middleware auth"}, plan.SearchTerms...)
		plan.SymbolHints = append([]string{"NewAuthMiddleware", "Authenticate"}, plan.SymbolHints...)
	}
	if hasAny(lq, "jwt token parsed", "parse jwt", "validate jwt", "jwt parsed", "token parsed") {
		plan.Intent = "definition"
		plan.PrimaryTool = "graph"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph_text", "rg"})
		plan.SearchTerms = append([]string{"validatetoken", "parsephptoken", "jwtmanager", "jwt token"}, plan.SearchTerms...)
		plan.SymbolHints = append([]string{"ValidateToken", "ParsePHPToken"}, plan.SymbolHints...)
	}
	if strings.Contains(lq, "clickhouse") && hasAny(lq, "failed", "failure", "retry", "requeue") && hasAny(lq, "publish", "published", "publisher", "chain") {
		plan.Intent = "behavior"
		plan.PrimaryTool = "graph_text"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "rg"})
		plan.SearchTerms = append([]string{"publish failed rows", "retry publisher", "failed rows retry", "clickhouse retry"}, plan.SearchTerms...)
		plan.SymbolHints = append([]string{"PublishFailedRows", "RetryPublisher"}, plan.SymbolHints...)
	}
	if hasAny(lq, "health check", "healthcheck", "readiness", "liveness", "ready endpoint", "live endpoint") || (strings.Contains(lq, "health") && strings.Contains(lq, "route")) {
		plan.Intent = "definition"
		plan.PrimaryTool = "graph_text"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "rg"})
		plan.SearchTerms = append([]string{"health check", "ready", "live", "health route"}, plan.SearchTerms...)
	}
	if strings.Contains(lq, "source mirror") && hasAny(lq, "persist verification", "persist verify", "count persisted", "persisted keys") {
		plan.Intent = "definition"
		plan.PrimaryTool = "graph_text"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "semantic"})
		plan.SearchTerms = append([]string{"count persisted keys", "persist verification", "persisted keys", "source mirror writer"}, plan.SearchTerms...)
		plan.SymbolHints = append([]string{"CountPersistedKeys"}, plan.SymbolHints...)
	}
	if hasAny(lq, "read claims from context", "reads claims from context", "reads from context", "read from context", "me endpoint", "handler reads") {
		plan.Intent = "behavior"
		plan.PrimaryTool = "graph"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph_text", "rg"})
	}
	if facet.MultiPart && plan.PrimaryTool == "semantic" {
		if profile.DisableSemantic {
			plan.PrimaryTool = "graph_text"
			plan.BackupTools = []string{"graph", "rg"}
		} else {
			plan.PrimaryTool = "graph_text"
			plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "semantic"})
		}
		if plan.Intent == "" || plan.Intent == "behavior" {
			plan.Intent = "mixed"
		}
	}
	if facet.MultiPart && !facet.NeedsLiteral && (plan.PrimaryTool == "rg" || plan.PrimaryTool == "semantic") {
		plan.PrimaryTool = "graph_text"
		if profile.DisableSemantic {
			plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "rg"})
		} else {
			plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "semantic"})
		}
		plan.Ambiguous = true
		if plan.Intent == "" || plan.Intent == "behavior" {
			plan.Intent = "mixed"
		}
	}
	if facet.NeedsTrace {
		plan.NeedCallGraph = true
		if plan.PrimaryTool == "semantic" {
			plan.PrimaryTool = "graph_text"
			plan.BackupTools = uniqueTools(plan.PrimaryTool, append([]string{"graph"}, plan.BackupTools...))
		}
	}
	if containsConsumerSlot(plan.Slots) && plan.PrimaryTool == "rg" {
		plan.PrimaryTool = "graph"
		plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph_text", "rg"})
	}
	identifiers := extractCodeIdentifiers(query)
	if len(identifiers) != 0 {
		plan.SymbolHints = append(identifiers, plan.SymbolHints...)
		plan.SearchTerms = append(identifiers, plan.SearchTerms...)
		if plan.PrimaryTool == "semantic" || plan.PrimaryTool == "rg" {
			plan.PrimaryTool = "graph_text"
			if profile.DisableSemantic {
				plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "rg"})
			} else {
				plan.BackupTools = uniqueTools(plan.PrimaryTool, []string{"graph", "semantic"})
			}
		}
	}
	if strings.Contains(lq, "clickhouse") && hasAny(lq, "failed", "failure", "retry", "requeue") && hasAny(lq, "publish", "published", "publisher", "chain") && len(plan.SymbolHints) == 0 {
		plan.SymbolHints = []string{"PublishFailedRows", "RetryPublisher"}
	}
	if plan.Intent == "literal" {
		plan.SearchTerms = narrowLiteralTerms(plan.SearchTerms, query)
	} else {
		plan.SearchTerms = expandTerms(plan.SearchTerms, query)
	}
	if len(plan.Slots) != 0 && allSlotsRole(plan.Slots, "config") {
		plan.SearchTerms = narrowConfigTerms(plan.SearchTerms)
	}
	plan.Subqueries = normalizeSubqueries(plan.Subqueries, query)
	plan.Slots = normalizeSlots(plan.Slots, query)
	plan.BackupTools = uniqueTools(plan.PrimaryTool, plan.BackupTools)
	if !plan.Ambiguous && isAmbiguousQuery(query, *plan) {
		plan.Ambiguous = true
	}
	if plan.AnswerStyle == "" {
		plan.AnswerStyle = "brief_with_citations"
	}
	if plan.StopRule == "" {
		plan.StopRule = "stop after 2 tool families or earlier when confidence is medium/high"
	}
}

func heuristicPlan(query string, profile config.RepoProfile) Plan {
	lq := strings.ToLower(strings.TrimSpace(query))
	preferSemantic := !profile.DisableSemantic
	preferred := strings.Join(profile.PreferredPrimary, ",")
	subqueries := heuristicSubqueries(query)
	facet := classifyQuery(query)
	switch {
	case hasAny(lq, "dead letter", "dlq", "failed message publisher", "retry publisher"):
		return Plan{
			Intent:      "definition",
			PrimaryTool: "graph_text",
			BackupTools: []string{"graph", "rg"},
			SearchTerms: []string{"dead letter", "dlq", "retry publisher", "publish failed rows", "failed message publisher"},
			SymbolHints: []string{"PublishFailedRows", "RetryPublisher", "NewReturnToSourceRetryPublisher"},
			Subqueries:  []string{"failed row retry publisher", "dead letter publisher"},
			Slots: []EvidenceSlot{
				{Role: "retry", Need: "failed row retry publisher", Hints: []string{"PublishFailedRows", "RetryPublisher", "retry publisher"}, Tool: "graph_text", Weight: 3},
			},
			AnswerStyle: "brief_with_citations",
			StopRule:    "stop only if retry or failed-row publisher path is grounded",
		}
	case hasAny(lq, "audit log", "audit repository", "writes audit log", "write audit"):
		return Plan{
			Intent:      "definition",
			PrimaryTool: "graph_text",
			BackupTools: []string{"graph", "rg"},
			SearchTerms: []string{"audit log", "llm_audit_log", "person_merge_audit", "write audit", "insert audit", "create audit"},
			SymbolHints: []string{"LLMAuditClickHouseStore", "InsertMergeAudit", "AuditLog", "AuditRepository", "CreateAuditLog", "InsertAuditLog"},
			Subqueries:  []string{"audit log writer", "audit repository write"},
			Slots: []EvidenceSlot{
				{Role: "core", Need: "audit log writer", Hints: []string{"audit", "repository", "create"}, Tool: "graph_text", Weight: 3},
			},
			AnswerStyle: "brief_with_citations",
			StopRule:    "stop only if audit log write path is grounded",
		}
	case hasAny(lq, "authorization header", "missing authorization", "unauthorized") && facet.NeedsLiteral:
		return Plan{Intent: "literal", PrimaryTool: "rg", BackupTools: []string{"graph_text", "graph"}, SearchTerms: []string{"missing authorization header", "invalid authorization header", "statusunauthorized", "\"unauthorized\"", "missing claims", "invalid token"}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after rg if exact auth error or header check found; else one backup tool"}
	case strings.Contains(lq, "forbidden") && facet.NeedsLiteral:
		return Plan{Intent: "literal", PrimaryTool: "rg", BackupTools: []string{"graph_text", "graph"}, SearchTerms: []string{"insufficient permissions", "invalid verify token", "admin access required", "statusforbidden", "permission denied"}, SymbolHints: []string{"RequireRole", "Authenticate"}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after rg if forbidden branch or exact forbidden literal is found; else one backup tool"}
	case strings.Contains(lq, "auth") && strings.Contains(lq, "middleware") && hasAny(lq, "invalid token", "invalid jwt", "token invalid"):
		return Plan{Intent: "literal", PrimaryTool: "rg", BackupTools: []string{"graph_text", "graph"}, SearchTerms: []string{"invalid token", "invalid authorization header", "authorization bearer", "authenticate"}, SymbolHints: []string{"Authenticate"}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after rg if invalid token branch in auth middleware is found; else one backup tool"}
	case strings.Contains(lq, "backfill") && hasAny(lq, "project gap", "gap detection", "detect gap"):
		return Plan{Intent: "definition", PrimaryTool: "graph_text", BackupTools: []string{"graph", "semantic"}, SearchTerms: []string{"detect project gap", "detect gap", "project gap detection", "backfill gap detector"}, SymbolHints: []string{"DetectProjectGap", "DetectGap"}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph_text if project-gap detection function is grounded; else one backup tool"}
	case strings.Contains(lq, "source mirror") && hasAny(lq, "page tuning", "smaller pages", "smaller page", "page size tuning"):
		return Plan{Intent: "definition", PrimaryTool: "graph_text", BackupTools: []string{"graph", "semantic"}, SearchTerms: []string{"with page tuning", "withpagetuning", "should retry with smaller page", "smaller page retry"}, SymbolHints: []string{"WithPageTuning", "shouldRetryWithSmallerPage"}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph_text if page tuning configuration in source mirror is grounded; else one backup tool"}
	case hasAny(lq, "parity audit pauses", "parity audit pause", "pause parity audit", "reconcile backlog") && !hasAny(lq, "current projection", "projection path", "projection or reconcile"):
		return Plan{
			Intent:      "definition",
			PrimaryTool: "graph",
			BackupTools: []string{"graph_text", "rg"},
			SearchTerms: []string{"shouldPauseParityAuditForReportReconcileBacklog", "checkBacklogGate", "runPendingParityAudit"},
			SymbolHints: []string{"shouldPauseParityAuditForReportReconcileBacklog", "checkBacklogGate", "runPendingParityAudit"},
			Subqueries:  []string{"parity reconcile backlog gate"},
			Slots: []EvidenceSlot{
				{Role: "reconcile", Need: "parity reconcile backlog gate", Hints: []string{"shouldPauseParityAuditForReportReconcileBacklog", "checkBacklogGate", "runPendingParityAudit"}, Tool: "graph", Weight: 3},
			},
			AnswerStyle: "brief_with_citations",
			StopRule:    "stop only if parity reconcile backlog gate symbol is grounded",
		}
	case hasAny(lq, "parity audit pauses", "parity pause", "reconcile backlog"):
		return Plan{Intent: "definition", PrimaryTool: "graph_text", BackupTools: []string{"graph", "semantic"}, SearchTerms: []string{"parity audit reconcile backlog", "pause parity audit", "report reconcile backlog"}, SymbolHints: []string{"shouldPauseParityAuditForReportReconcileBacklog", "ExistsCompletedReportReconcileAfter"}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph_text if parity pause gate is grounded; else one backup tool"}
	case strings.Contains(lq, "source mirror") && hasAny(lq, "cutoff date", "ttl cutoff", "cutoff for ttl", "pre ttl cutoff"):
		return Plan{Intent: "definition", PrimaryTool: "graph_text", BackupTools: []string{"graph", "semantic"}, SearchTerms: []string{"pre ttl cutoff", "ttl cutoff", "cutoff date", "source mirror cutoff"}, SymbolHints: []string{"PreTTLCutoff"}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph_text if ttl cutoff function is grounded; else one backup tool"}
	case strings.Contains(lq, "backfill") && hasAny(lq, "stall", "stalls", "retry", "tune", "tuning"):
		return Plan{
			Intent:      "mixed",
			PrimaryTool: "graph",
			BackupTools: []string{"graph_text", "rg"},
			SearchTerms: []string{"DetectGap", "DetectProjectGap", "shouldRetryWithSmallerPage", "WithPageTuning", "queryWithRetry"},
			SymbolHints: []string{"DetectGap", "DetectProjectGap", "shouldRetryWithSmallerPage", "WithPageTuning", "queryWithRetry"},
			Subqueries:  []string{"backfill stall detection", "source mirror retry tuning"},
			Slots: []EvidenceSlot{
				{Role: "detector", Need: "backfill stall detection", Hints: []string{"DetectGap", "DetectProjectGap", "gap detector"}, Tool: "graph", Weight: 3},
				{Role: "retry", Need: "source mirror retry smaller page", Hints: []string{"shouldRetryWithSmallerPage", "queryWithRetry", "retryBackoff"}, Tool: "graph", Weight: 2},
				{Role: "tuning", Need: "source mirror page tuning", Hints: []string{"WithPageTuning", "WithPageTuningMin", "page tuning"}, Tool: "graph", Weight: 1},
			},
			AnswerStyle: "brief_with_citations",
			StopRule:    "stop only if detector and retry slots are both grounded; tuning is supporting",
		}
	case strings.Contains(lq, "parity") && hasAny(lq, "projection", "reconcile", "failures", "parity audit"):
		return Plan{
			Intent:      "mixed",
			PrimaryTool: "graph",
			BackupTools: []string{"graph_text", "rg"},
			SearchTerms: []string{"rebuildCSCurrentProjection", "publishCSCurrentTable", "runPendingParityAudit", "shouldPauseParityAuditForReportReconcileBacklog", "checkBacklogGate"},
			SymbolHints: []string{"rebuildCSCurrentProjection", "publishCSCurrentTable", "runPendingParityAudit", "shouldPauseParityAuditForReportReconcileBacklog", "checkBacklogGate"},
			Subqueries:  []string{"current projection parity path", "parity reconcile backlog gate"},
			Slots: []EvidenceSlot{
				{Role: "projection", Need: "current projection parity path", Hints: []string{"rebuildCSCurrentProjection", "publishCSCurrentTable", "cs_current_projection"}, Tool: "graph", Weight: 2},
				{Role: "reconcile", Need: "parity reconcile backlog gate", Hints: []string{"runPendingParityAudit", "shouldPauseParityAuditForReportReconcileBacklog", "checkBacklogGate"}, Tool: "graph", Weight: 2},
			},
			NeedCallGraph: true,
			AnswerStyle:   "brief_with_citations",
			StopRule:      "stop only if projection and reconcile slots are both grounded",
		}
	case hasAny(lq, "request timeout", "read timeout", "write timeout", "http timeout"):
		return Plan{
			Intent:      "mixed",
			PrimaryTool: "rg",
			BackupTools: []string{"graph_text", "graph"},
			SearchTerms: []string{"ReadTimeout", "WriteTimeout", "DialTimeout", "PoolTimeout", "Timeout:", "http.Client", "NewAPIClient"},
			SymbolHints: []string{"NewAPIClient", "NewClient"},
			Subqueries:  []string{"http client timeout config", "api client timeout config", "dial or pool timeout config"},
			Slots: []EvidenceSlot{
				{Role: "config", Need: "http client timeout config", Hints: []string{"http.client", "transport", "timeout:"}, Tool: "rg", Weight: 3},
				{Role: "config", Need: "api client timeout config", Hints: []string{"newapiclient", "newclient", "dialtimeout"}, Tool: "rg", Weight: 3},
				{Role: "config", Need: "dial or pool timeout config", Hints: []string{"dialtimeout", "pooltimeout", "readtimeout"}, Tool: "rg", Weight: 2},
			},
			AnswerStyle: "brief_with_citations",
			StopRule:    "stop after rg only if timeout config slots are grounded; else graph_text then graph",
		}
	case hasAny(lq, "http client configured", "http client config", "where http client configured"):
		return Plan{
			Intent:      "definition",
			PrimaryTool: "rg",
			BackupTools: []string{"graph_text", "graph"},
			SearchTerms: []string{"http.Client", "NewClient", "NewAPIClient", "Transport:", "client: &http.Client"},
			SymbolHints: []string{"NewClient", "NewAPIClient"},
			Subqueries:  []string{"http client constructor", "http transport config"},
			Slots: []EvidenceSlot{
				{Role: "config", Need: "http client constructor", Hints: []string{"http.client", "newclient", "transport"}, Tool: "rg", Weight: 3},
			},
			AnswerStyle: "brief_with_citations",
			StopRule:    "stop only if http client constructor or transport config is grounded",
		}
	case hasAny(lq, "config loaded from env", "loaded from env", "from env"):
		return Plan{
			Intent:      "definition",
			PrimaryTool: "rg",
			BackupTools: []string{"graph_text", "graph"},
			SearchTerms: []string{"os.Getenv", "Getenv", "LookupEnv", "MustGetenv", "envconfig", "LoadConfig"},
			SymbolHints: []string{"LoadConfig", "Load"},
			Subqueries:  []string{"env reads", "config load"},
			Slots: []EvidenceSlot{
				{Role: "config", Need: "config env load", Hints: []string{"getenv", "lookupenv", "loadconfig"}, Tool: "rg", Weight: 3},
			},
			AnswerStyle: "brief_with_citations",
			StopRule:    "stop only if env read plus config load locus is grounded",
		}
	case hasAny(lq, "database transaction begins", "transaction begins", "begin tx", "begintx"):
		return Plan{
			Intent:      "definition",
			PrimaryTool: "rg",
			BackupTools: []string{"graph_text", "graph"},
			SearchTerms: []string{"BeginTx", "BeginTxx", "db.Begin", "tx, err :=", "BEGIN"},
			SymbolHints: []string{"BeginTx", "BeginTxx"},
			Subqueries:  []string{"transaction begin"},
			Slots: []EvidenceSlot{
				{Role: "core", Need: "transaction begin", Hints: []string{"begintx", "db.begin", "tx, err :="}, Tool: "rg", Weight: 3},
			},
			AnswerStyle: "brief_with_citations",
			StopRule:    "stop only if transaction begin site is grounded",
		}
	case hasAny(lq, "trace callers", "trace caller", "who calls", "callers of"):
		return Plan{
			Intent:        "callers",
			PrimaryTool:   "graph",
			BackupTools:   []string{"graph_text"},
			SearchTerms:   []string{"caller", "calls"},
			SymbolHints:   extractCodeIdentifiers(query),
			Subqueries:    subqueries,
			Slots:         []EvidenceSlot{{Role: "consumer", Need: "caller chain", Hints: []string{"caller", "calls", "handler", "middleware"}, Tool: "graph", Weight: 3}},
			NeedCallGraph: true,
			AnswerStyle:   "brief_with_citations",
			StopRule:      "stop only if caller chain is grounded from trace or graph edges",
		}
	case hasAny(lq, "background worker loop defined", "worker loop defined", "queue consumer handles messages", "consumer handles messages"):
		return Plan{
			Intent:      "definition",
			PrimaryTool: "graph_text",
			BackupTools: []string{"graph", "rg"},
			SearchTerms: []string{"Run", "Start", "Consume", "HandleMessage", "ProcessMessage", "for {", "select {"},
			SymbolHints: []string{"Run", "Start", "Consume", "HandleMessage"},
			Subqueries:  []string{"worker loop", "consumer message handler"},
			Slots: []EvidenceSlot{
				{Role: "consumer", Need: "worker consumer loop", Hints: []string{"consume", "worker", "message"}, Tool: "graph_text", Weight: 3},
			},
			AnswerStyle: "brief_with_citations",
			StopRule:    "stop only if worker loop or consumer handler is grounded",
		}
	case hasAny(lq, "validation errors returned", "validation error", "validation errors"):
		return Plan{
			Intent:      "behavior",
			PrimaryTool: "rg",
			BackupTools: []string{"graph_text", "graph"},
			SearchTerms: []string{"ValidationError", "BindAndValidate", "validator", "writejsonerror", "bad request", "unprocessable entity"},
			SymbolHints: []string{"BindAndValidate", "ValidationError"},
			Subqueries:  []string{"validation error return"},
			Slots: []EvidenceSlot{
				{Role: "validator", Need: "validation error return", Hints: []string{"validation", "validator", "error"}, Tool: "rg", Weight: 3},
			},
			AnswerStyle: "brief_with_citations",
			StopRule:    "stop only if validation error return branch is grounded",
		}
	case hasAny(lq, "pagination defaults defined", "pagination default", "page size default"):
		return Plan{
			Intent:      "definition",
			PrimaryTool: "graph_text",
			BackupTools: []string{"graph", "rg"},
			SearchTerms: []string{"PageSize", "DefaultPageSize", "Limit", "Pagination", "page_size", "per_page", "paginate"},
			SymbolHints: []string{"PageSize", "DefaultPageSize", "Pagination", "Paginate"},
			Subqueries:  []string{"pagination default handler", "pagination default query"},
			Slots: []EvidenceSlot{
				{Role: "core", Need: "pagination default", Hints: []string{"pagesize", "defaultpagesize", "pagination", "limit", "handler", "query"}, Tool: "graph_text", Weight: 3},
			},
			AnswerStyle: "brief_with_citations",
			StopRule:    "stop only if pagination default locus is grounded",
		}
	case hasAny(lq, "feature flag", "feature flags", "is enabled", "isenabled"):
		return Plan{Intent: "definition", PrimaryTool: "graph_text", BackupTools: []string{"graph", "semantic"}, SearchTerms: []string{"feature flag", "is enabled", "isenabled", "featureflag"}, SymbolHints: []string{"IsEnabled", "FeatureFlag"}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph_text if feature-flag check is grounded; else one backup tool"}
	case hasAny(lq, "bearer token extracted", "bearer token", "authorization bearer"):
		return Plan{Intent: "behavior", PrimaryTool: "graph_text", BackupTools: []string{"graph", "rg"}, SearchTerms: []string{"authorization bearer", "header.get authorization", "strings.fields bearer", "equalfold bearer", "extractclaims", "requireauth"}, SymbolHints: []string{"ExtractToken", "ExtractClaims", "Authenticate", "RequireAuth", "BasicAuth"}, Subqueries: subqueries, Slots: []EvidenceSlot{{Role: "validator", Need: "bearer token request header parse", Hints: []string{"authorization", "bearer", "header"}, Tool: "graph_text", Weight: 3}}, AnswerStyle: "brief_with_citations", StopRule: "stop only if request header parse or bearer split logic is grounded"}
	case hasAny(lq, "external api retries", "api retries", "http retries", "retries configured"):
		return Plan{Intent: "definition", PrimaryTool: "graph_text", BackupTools: []string{"graph", "rg"}, SearchTerms: []string{"retryattempts", "retrydelay", "callwithretry", "dowithretry", "forwardwithretry", "withretryattempts", "client retry"}, SymbolHints: []string{"WithRetryAttempts", "callWithRetry", "forwardWithRetry", "DoWithRetry"}, Subqueries: subqueries, Slots: []EvidenceSlot{{Role: "config", Need: "external client retry config", Hints: []string{"retry", "backoff", "client"}, Tool: "graph_text", Weight: 3}}, AnswerStyle: "brief_with_citations", StopRule: "stop only if retry/backoff configuration for client or external call is grounded"}
	case hasAny(lq, "projection or read model", "read model", "publishes projection", "publishes read model"):
		return Plan{Intent: "behavior", PrimaryTool: "graph", BackupTools: []string{"graph_text", "semantic"}, SearchTerms: []string{"projection", "read model", "publish projection", "publish current table", "projector", "materialized view"}, SymbolHints: []string{"Projector", "Publish", "publishCSCurrentTable", "rebuildCSCurrentProjection", "BuildDailyProjectionQueries"}, Subqueries: subqueries, Slots: []EvidenceSlot{{Role: "projection", Need: "projection or read model publisher", Hints: []string{"projection", "publish", "read model", "projector"}, Tool: "graph", Weight: 3}}, AnswerStyle: "brief_with_citations", StopRule: "stop only if projection or read-model publish path is grounded"}
	case hasAny(lq, "stale work", "stuck jobs", "stale jobs", "watchdog", "dead work"):
		return Plan{Intent: "behavior", PrimaryTool: "graph", BackupTools: []string{"graph_text", "semantic"}, SearchTerms: []string{"watchdog", "stale running", "stale project detect", "requeue stale", "dead work items", "stuck jobs"}, SymbolHints: []string{"Watchdog", "Detect", "FailStaleRunningRebuildJobs", "RequeueStaleRunningWorkItems", "RequeueDeadWorkItems", "staleProjectDetectQuery"}, Subqueries: subqueries, Slots: []EvidenceSlot{{Role: "detector", Need: "stale work or stuck job detector", Hints: []string{"watchdog", "detect", "stale", "requeue"}, Tool: "graph", Weight: 3}}, AnswerStyle: "brief_with_citations", StopRule: "stop only if watchdog, stale detector, or stale-work requeue path is grounded"}
	case hasAny(lq, "webhook signature", "verify webhook", "webhook verify token", "signature validated"):
		return Plan{Intent: "behavior", PrimaryTool: "graph_text", BackupTools: []string{"graph", "rg"}, SearchTerms: []string{"webhook signature", "verify token", "x-hub-signature", "hmac", "verifysignature", "validatesignature"}, SymbolHints: []string{"Webhook", "VerifySignature", "ValidateSignature", "VerifyToken"}, Subqueries: subqueries, Slots: []EvidenceSlot{{Role: "validator", Need: "webhook signature validation", Hints: []string{"webhook", "signature", "verify", "token"}, Tool: "graph_text", Weight: 3}}, AnswerStyle: "brief_with_citations", StopRule: "stop only if webhook signature or verify-token validation path is grounded"}
	case (hasAny(lq, "claim", "claims") && hasAny(lq, "context", "request") && hasAny(lq, "store", "stored", "inject", "set")) || strings.Contains(lq, "claims stored"):
		return Plan{Intent: "behavior", PrimaryTool: "graph_text", BackupTools: []string{"graph", "rg"}, SearchTerms: []string{"withclaims", "claimsfromcontext", "context.withvalue", "authorization bearer", query}, SymbolHints: []string{"WithClaims", "ClaimsFromContext"}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph_text when context write or claim injector found; else one backup tool"}
	case hasAny(lq, "bearer", "authorization") && hasAny(lq, "token", "request") && hasAny(lq, "extract", "header"):
		return Plan{Intent: "behavior", PrimaryTool: "graph_text", BackupTools: []string{"graph", "rg"}, SearchTerms: []string{"authorization bearer", "header.get authorization", "strings.fields bearer", "equalfold bearer", query}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph_text when request header parsing or bearer split logic found; else one backup tool"}
	case strings.Contains(lq, "auth") && strings.Contains(lq, "middleware") && hasAny(lq, "work", "works", "flow", "behavior"):
		return Plan{Intent: "behavior", PrimaryTool: "graph_text", BackupTools: []string{"graph", "semantic"}, SearchTerms: []string{"auth middleware", "authenticate", "withclaims", "claimsfromcontext", "authorization bearer", query}, SymbolHints: []string{"Authenticate", "WithClaims", "ClaimsFromContext"}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph_text when middleware validation and context handoff are both grounded; else one backup tool"}
	case strings.Contains(lq, "auth") && strings.Contains(lq, "middleware") && strings.Contains(lq, "defined"):
		return Plan{Intent: "definition", PrimaryTool: "graph", BackupTools: []string{"graph_text", "rg"}, SearchTerms: []string{"newauthmiddleware", "authenticate", "authmiddleware", "middleware auth", query}, SymbolHints: []string{"NewAuthMiddleware", "Authenticate"}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph if middleware definition found; else one backup tool"}
	case hasAny(lq, "jwt token parsed", "parse jwt", "validate jwt", "jwt parsed", "token parsed"):
		return Plan{Intent: "definition", PrimaryTool: "graph", BackupTools: []string{"graph_text", "rg"}, SearchTerms: []string{"validatetoken", "parsephptoken", "jwtmanager", "jwt token", query}, SymbolHints: []string{"ValidateToken", "ParsePHPToken"}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph if jwt parse/validate symbol found; else one backup tool"}
	case strings.Contains(lq, "clickhouse") && hasAny(lq, "failed", "failure", "retry", "requeue") && hasAny(lq, "publish", "published", "publisher", "chain"):
		return Plan{Intent: "behavior", PrimaryTool: "graph_text", BackupTools: []string{"graph", "rg"}, SearchTerms: []string{"publish failed rows", "retry publisher", "failed rows retry", "clickhouse retry", query}, SymbolHints: []string{"PublishFailedRows", "RetryPublisher"}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph_text when failed-row publish path is grounded; else one backup tool"}
	case hasAny(lq, "health check", "healthcheck", "readiness", "liveness", "ready endpoint", "live endpoint") || (strings.Contains(lq, "health") && strings.Contains(lq, "route")):
		return Plan{Intent: "definition", PrimaryTool: "graph_text", BackupTools: []string{"graph", "rg"}, SearchTerms: []string{"health check", "ready", "live", "health route", query}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph_text if health/readiness route or handler is grounded; else one backup tool"}
	case strings.Contains(lq, "source mirror") && hasAny(lq, "persist verification", "persist verify", "count persisted", "persisted keys"):
		return Plan{Intent: "definition", PrimaryTool: "graph_text", BackupTools: []string{"graph", "semantic"}, SearchTerms: []string{"count persisted keys", "persist verification", "persisted keys", "source mirror writer", query}, SymbolHints: []string{"CountPersistedKeys"}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph_text if persisted-key verification function is grounded; else one backup tool"}
	case facet.MultiPart:
		primary := "graph_text"
		backups := []string{"graph", "semantic"}
		if profile.DisableSemantic {
			backups = []string{"graph", "rg"}
		}
		return Plan{Intent: "mixed", PrimaryTool: primary, BackupTools: backups, SearchTerms: []string{query}, Subqueries: subqueries, Slots: heuristicSlots(query), NeedCallGraph: facet.NeedsTrace, AnswerStyle: "brief_with_citations", Ambiguous: true, StopRule: "split into slots; stop after 2 tool families only if each important slot has grounded evidence, else replan once"}
	case looksDefinitionQuery(lq):
		return Plan{Intent: "definition", PrimaryTool: "graph", BackupTools: []string{"graph_text", "rg"}, SearchTerms: []string{query}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph if symbol found; else one backup tool"}
	case looksStructureQuery(lq):
		return Plan{Intent: "structure", PrimaryTool: "astgrep", BackupTools: []string{"rg"}, SearchTerms: []string{query}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after astgrep if exact pattern matches; else one backup tool"}
	case looksLiteralQuery(lq):
		return Plan{Intent: "literal", PrimaryTool: "rg", BackupTools: []string{"graph_text"}, SearchTerms: []string{query}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after rg if exact literal found; else one backup tool"}
	case strings.Contains(profile.Stack, "python") && strings.Contains(preferred, "graph_text"):
		return Plan{Intent: "behavior", PrimaryTool: "graph_text", BackupTools: []string{"semantic"}, SearchTerms: []string{query}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph_text if framework behavior surfaces; else one backup tool"}
	case strings.Contains(profile.Stack, "node") && strings.Contains(preferred, "graph_text"):
		return Plan{Intent: "behavior", PrimaryTool: "graph_text", BackupTools: []string{"semantic"}, SearchTerms: []string{query}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph_text if module/provider area surfaces; else one backup tool"}
	case strings.Contains(preferred, "graph_text") && looksBehaviorQuery(lq):
		return Plan{Intent: "behavior", PrimaryTool: "graph_text", BackupTools: []string{"graph"}, SearchTerms: []string{query}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph_text when confidence medium/high; else one backup tool"}
	default:
		if preferSemantic {
			return Plan{Intent: "behavior", PrimaryTool: "semantic", BackupTools: []string{"graph_text", "graph"}, SearchTerms: []string{query}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after semantic when confidence medium/high; else one more tool, then replan/stop"}
		}
		return Plan{Intent: "behavior", PrimaryTool: "graph_text", BackupTools: []string{"graph", "rg"}, SearchTerms: []string{query}, Subqueries: subqueries, Slots: heuristicSlots(query), AnswerStyle: "brief_with_citations", StopRule: "stop after graph_text when confidence medium/high; else one more tool, then replan/stop"}
	}
}

func classifyQuery(query string) QueryFacet {
	lq := strings.ToLower(strings.TrimSpace(query))
	return QueryFacet{
		NeedsDefinition: looksDefinitionQuery(lq),
		NeedsStructure:  looksStructureQuery(lq),
		NeedsLiteral:    looksLiteralQuery(lq),
		NeedsBehavior:   looksBehaviorQuery(lq),
		NeedsTrace:      strings.Contains(lq, "caller") || strings.Contains(lq, "callee") || strings.Contains(lq, "call chain") || strings.Contains(lq, "who calls") || strings.Contains(lq, "what calls") || strings.Contains(lq, "where used") || strings.Contains(lq, "where consumed") || strings.Contains(lq, "consumed") || strings.Contains(lq, "flow") || strings.Contains(lq, "path") || strings.Contains(lq, "involved"),
		MultiPart:       len(heuristicSubqueries(query)) > 1,
	}
}

func expandTerms(existing []string, query string) []string {
	seen := map[string]bool{}
	add := func(v string, out *[]string) {
		v = strings.TrimSpace(strings.ToLower(v))
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		*out = append(*out, v)
	}

	terms := make([]string, 0, 8)
	for _, item := range existing {
		add(item, &terms)
	}
	add(query, &terms)

	replacer := strings.NewReplacer(",", " ", ".", " ", ":", " ", ";", " ", "\"", " ", "'", " ", "(", " ", ")", " ")
	for _, token := range strings.Fields(replacer.Replace(query)) {
		if len(token) < 3 {
			continue
		}
		switch token {
		case "find", "that", "with", "from", "into", "code", "session", "history", "claude", "message", "string":
			continue
		}
		add(token, &terms)
	}
	return terms
}

func narrowLiteralTerms(existing []string, query string) []string {
	seen := map[string]bool{}
	add := func(v string, out *[]string) {
		v = strings.TrimSpace(strings.ToLower(v))
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		*out = append(*out, v)
	}
	out := make([]string, 0, 6)
	for _, item := range existing {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.Contains(item, " ") || strings.Contains(item, "_") || item == "forbidden" || item == "statusforbidden" {
			add(item, &out)
		}
	}
	if len(out) == 0 {
		add(query, &out)
	}
	if strings.Contains(strings.ToLower(query), "authorization header") {
		add("missing authorization header", &out)
		add("invalid authorization header", &out)
	}
	if strings.Contains(strings.ToLower(query), "invalid token") {
		add("invalid token", &out)
	}
	if strings.Contains(strings.ToLower(query), "forbidden") {
		add("forbidden", &out)
		add("statusforbidden", &out)
		add("permission denied", &out)
	}
	if len(out) > 6 {
		return out[:6]
	}
	return out
}

func extractCodeIdentifiers(query string) []string {
	re := regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9_]{2,}\b`)
	matches := re.FindAllString(query, -1)
	seen := map[string]bool{}
	out := make([]string, 0, len(matches))
	for _, item := range matches {
		trimmed := strings.TrimSpace(item)
		lower := strings.ToLower(trimmed)
		if seen[lower] {
			continue
		}
		if strings.Contains(trimmed, "_") || camelLike(trimmed) {
			seen[lower] = true
			out = append(out, lower)
		}
	}
	if len(out) > 4 {
		return out[:4]
	}
	return out
}

func camelLike(v string) bool {
	hasLower := false
	hasUpper := false
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		}
	}
	return hasLower && hasUpper
}

func stripCodeFence(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	return strings.TrimSpace(raw)
}

func sanitizePlanJSON(raw string) string {
	raw = stripCodeFence(raw)
	raw = strings.TrimSpace(raw)
	re := regexp.MustCompile(`(?s)\{.*\}`)
	match := re.FindString(raw)
	if match != "" {
		return strings.TrimSpace(match)
	}
	return raw
}

func (p *Planner) repairPlanJSON(ctx context.Context, raw string) (string, error) {
	systemPrompt := `You repair malformed JSON for a retrieval planner.
Return one valid JSON object only.
Do not add commentary.
Preserve user intent.
Schema keys:
intent, primary_tool, backup_tools, search_terms, subqueries, ast_pattern, symbol_hints, need_call_graph, answer_style, ambiguous, stop_rule`
	userPrompt := fmt.Sprintf("Repair this into one valid JSON object only:\n%s", raw)
	repaired, err := p.llm.Chat(ctx, systemPrompt, userPrompt)
	if err != nil {
		return "", err
	}
	return sanitizePlanJSON(repaired), nil
}

func uniqueTools(primary string, items []string) []string {
	seen := map[string]bool{primary: true}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func containsConsumerSlot(slots []EvidenceSlot) bool {
	for _, slot := range slots {
		if slot.Role == "consumer" {
			return true
		}
	}
	return false
}

func isAmbiguousQuery(query string, plan Plan) bool {
	q := strings.TrimSpace(strings.ToLower(query))
	if q == "" {
		return true
	}
	tokens := strings.Fields(q)
	if len(tokens) <= 2 && plan.Intent == "behavior" {
		return true
	}
	if len(tokens) <= 3 && !strings.ContainsAny(q, "\"'") && plan.PrimaryTool == "semantic" {
		return true
	}
	return false
}

func normalizeSubqueries(existing []string, query string) []string {
	if len(existing) == 0 {
		return heuristicSubqueries(query)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(existing))
	for _, item := range existing {
		item = compactSubquery(item)
		if item == "" || seen[strings.ToLower(item)] {
			continue
		}
		seen[strings.ToLower(item)] = true
		out = append(out, item)
	}
	if len(out) == 0 {
		return heuristicSubqueries(query)
	}
	if len(out) > 4 {
		return out[:4]
	}
	return out
}

func allSlotsRole(slots []EvidenceSlot, role string) bool {
	if len(slots) == 0 {
		return false
	}
	for _, slot := range slots {
		if strings.TrimSpace(slot.Role) != role {
			return false
		}
	}
	return true
}

func compactSubquery(item string) string {
	item = strings.ToLower(strings.TrimSpace(item))
	replacer := strings.NewReplacer(",", " ", ".", " ", ":", " ", ";", " ", "(", " ", ")", " ")
	tokens := strings.Fields(replacer.Replace(item))
	stop := map[string]bool{
		"find": true, "identify": true, "show": true, "trace": true, "check": true,
		"implementation": true, "function": true, "functions": true, "logic": true,
		"where": true, "how": true, "that": true, "this": true, "these": true,
		"the": true, "and": true, "or": true, "are": true, "is": true, "be": true,
		"from": true, "into": true, "which": true,
	}
	kept := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if len(token) < 3 || stop[token] {
			continue
		}
		kept = append(kept, token)
	}
	if len(kept) == 0 {
		return strings.TrimSpace(item)
	}
	if len(kept) > 5 {
		kept = kept[:5]
	}
	return strings.Join(kept, " ")
}

func narrowConfigTerms(terms []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term == "" || seen[term] {
			continue
		}
		if term == "where" || term == "request" || term == "configured" || term == "config" {
			continue
		}
		if strings.Contains(term, "where ") || strings.Contains(term, " configured") {
			continue
		}
		seen[term] = true
		out = append(out, term)
	}
	if len(out) > 6 {
		return out[:6]
	}
	return out
}

func normalizeSlots(existing []EvidenceSlot, query string) []EvidenceSlot {
	if len(existing) == 0 {
		return heuristicSlots(query)
	}
	seen := map[string]bool{}
	out := make([]EvidenceSlot, 0, len(existing))
	for _, slot := range existing {
		slot.Role = canonicalSlotRole(slot.Role, slot.Need, slot.Hints)
		slot.Need = compactSubquery(slot.Need)
		key := slot.Role + "|" + slot.Need
		if slot.Role == "" || slot.Need == "" || seen[key] {
			continue
		}
		seen[key] = true
		slot.Tool = canonicalSlotTool(slot.Role, slot.Tool)
		if slot.Weight <= 0 {
			slot.Weight = canonicalSlotWeight(slot.Role)
		}
		if len(slot.Hints) > 3 {
			slot.Hints = slot.Hints[:3]
		}
		out = append(out, slot)
	}
	if len(out) == 0 {
		return heuristicSlots(query)
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

func canonicalSlotRole(role string, need string, hints []string) string {
	text := strings.ToLower(strings.TrimSpace(role + " " + need + " " + strings.Join(hints, " ")))
	switch {
	case hasAny(text, "timeout", "readtimeout", "writetimeout", "dialtimeout", "pooltimeout", "transport", "http client", "redis", "config"):
		return "config"
	case hasAny(text, "projection", "publish", "tombstone", "stale key", "current") && !hasAny(text, "context", "claim", "claims", "withclaims"):
		return "projection"
	case hasAny(text, "validate", "verify", "validator", "jwt", "token", "bearer"):
		return "validator"
	case hasAny(text, "withclaims", "store", "inject", "context", "injector"):
		return "injector"
	case hasAny(text, "consume", "consumer", "handler", "route", "controller", "claimsfromcontext"):
		return "consumer"
	case hasAny(text, "detect", "detector", "stall", "stuck", "blocked", "gap", "monitor"):
		return "detector"
	case hasAny(text, "retry", "requeue", "backoff"):
		return "retry"
	case hasAny(text, "tune", "tuning", "page", "rate", "throttle", "concurrency"):
		return "tuning"
	case hasAny(text, "projection", "publish", "current"):
		return "projection"
	case hasAny(text, "reconcile", "repair", "heal", "rebuild"):
		return "reconcile"
	default:
		return ""
	}
}

func hasAny(text string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func canonicalSlotTool(role string, current string) string {
	switch role {
	case "validator", "detector", "injector":
		return "graph_text"
	case "config":
		if strings.TrimSpace(current) == "rg" {
			return "rg"
		}
		return "graph_text"
	case "consumer", "projection", "reconcile", "retry", "tuning":
		return "graph"
	default:
		if strings.TrimSpace(current) != "" {
			return current
		}
		return "graph_text"
	}
}

func canonicalSlotWeight(role string) int {
	switch role {
	case "validator", "consumer", "detector":
		return 3
	case "injector", "retry", "projection", "reconcile", "config":
		return 2
	default:
		return 1
	}
}

func heuristicSlots(query string) []EvidenceSlot {
	slots := []EvidenceSlot{}
	topics := topicTerms(query)
	baseHints := func(role string) []string {
		out := append([]string{}, genericRoleHints(role)...)
		for _, topic := range topics {
			if len(out) >= 3 {
				break
			}
			seen := false
			for _, existing := range out {
				if existing == topic {
					seen = true
					break
				}
			}
			if !seen {
				out = append(out, topic)
			}
		}
		if len(out) > 3 {
			out = out[:3]
		}
		return out
	}
	add := func(role, need, tool string, weight int, hints ...string) {
		slots = append(slots, EvidenceSlot{Role: role, Need: compactSubquery(need), Tool: tool, Weight: weight, Hints: hints})
	}
	roleTool := map[string]string{
		"validator":  "graph_text",
		"injector":   "graph_text",
		"consumer":   "graph",
		"detector":   "graph_text",
		"config":     "graph_text",
		"retry":      "graph",
		"tuning":     "graph",
		"projection": "graph",
		"reconcile":  "graph",
	}
	roleWeight := map[string]int{
		"validator":  3,
		"consumer":   3,
		"injector":   2,
		"detector":   3,
		"config":     2,
		"retry":      2,
		"tuning":     1,
		"projection": 2,
		"reconcile":  2,
	}
	for _, role := range inferredRolesFromText(query) {
		need := query
		if len(topics) != 0 {
			need = role + " " + strings.Join(topics, " ")
		}
		add(role, need, roleTool[role], roleWeight[role], baseHints(role)...)
	}
	if len(slots) == 0 {
		add("core", query, "graph_text", 1, topics...)
	}
	if len(slots) > 4 {
		slots = slots[:4]
	}
	return slots
}

func topicTerms(query string) []string {
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
		"failure": true, "failures": true,
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

func inferredRolesFromText(query string) []string {
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
	if hasAny(lq, "validate", "validates", "validation", "verify", "authorization", "authenticate", "token", "jwt", "bearer", "auth middleware", "middleware auth") || (strings.Contains(lq, "auth") && strings.Contains(lq, "middleware")) {
		add("validator")
	}
	if hasAny(lq, "inject", "store", "set", "put", "context", "claim", "claims") && !hasAny(lq, "projection", "publish", "tombstone", "stale key", "current diff", "batch insert") {
		add("injector")
	}
	if hasAny(lq, "consume", "consumed", "used", "read", "handler", "route", "controller", "endpoint") {
		add("consumer")
	}
	if hasAny(lq, "claimsfromcontext", "reads claims from context", "read claims from context", "reads from context", "read from context") {
		add("consumer")
	}
	if hasAny(lq, "detect", "detected", "watch", "monitor", "stall", "stuck", "gap", "blocked", "lag") {
		add("detector")
	}
	if hasAny(lq, "timeout", "readtimeout", "writetimeout", "dialtimeout", "pooltimeout", "http client", "config") {
		add("config")
	}
	if hasAny(lq, "retry", "requeue", "backoff", "attempt", "redelivery") {
		add("retry")
	}
	if hasAny(lq, "tune", "tuning", "throttle", "rate", "budget", "page", "limit") {
		add("tuning")
	}
	if hasAny(lq, "projection", "publish", "materialized", "read model", "readmodel", "current") {
		add("projection")
	}
	if hasAny(lq, "reconcile", "repair", "heal", "rebuild", "recover") {
		add("reconcile")
	}
	if len(roles) > 1 && hasAny(lq, "projection", "publish", "tombstone", "stale key", "current") && !hasAny(lq, "context", "claim", "claims", "withclaims") {
		filtered := make([]string, 0, len(roles))
		for _, role := range roles {
			if role == "injector" {
				continue
			}
			filtered = append(filtered, role)
		}
		roles = filtered
	}
	if len(roles) > 1 && hasAny(lq, "read claims from context", "reads claims from context", "reads from context", "read from context", "endpoint reads") {
		filtered := make([]string, 0, len(roles))
		for _, role := range roles {
			if role == "validator" {
				continue
			}
			filtered = append(filtered, role)
		}
		roles = filtered
	}
	return roles
}

func genericRoleHints(role string) []string {
	switch role {
	case "validator":
		return []string{"validate", "authorization", "token"}
	case "injector":
		return []string{"context", "claims", "inject", "withclaims"}
	case "consumer":
		return []string{"handler", "claims", "read"}
	case "detector":
		return []string{"detect", "monitor"}
	case "config":
		return []string{"config", "timeout", "client"}
	case "retry":
		return []string{"retry", "backoff"}
	case "tuning":
		return []string{"tuning", "limit"}
	case "projection":
		return []string{"projection", "publish"}
	case "reconcile":
		return []string{"reconcile", "repair"}
	default:
		return nil
	}
}

func heuristicSubqueries(query string) []string {
	q := strings.TrimSpace(query)
	lq := strings.ToLower(q)
	parts := []string{}
	splitters := []string{" and where ", " and ", " then ", " while "}
	for _, splitter := range splitters {
		if strings.Contains(lq, splitter) {
			raw := strings.Split(lq, splitter)
			for _, item := range raw {
				item = strings.TrimSpace(item)
				if item != "" {
					parts = append(parts, item)
				}
			}
			break
		}
	}
	if len(parts) == 0 && (strings.Contains(lq, " where ") || strings.Contains(lq, " how ")) {
		if strings.Contains(lq, " where ") && strings.Contains(lq, " how ") {
			parts = append(parts, strings.TrimSpace(q))
		}
	}
	out := []string{}
	seen := map[string]bool{}
	for _, item := range parts {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	if len(out) > 4 {
		return out[:4]
	}
	return out
}

func looksDefinitionQuery(q string) bool {
	return strings.Contains(q, "where is") || strings.Contains(q, "defined") || strings.Contains(q, "definition") || strings.Contains(q, "who calls") || strings.Contains(q, "called by")
}

func looksStructureQuery(q string) bool {
	return strings.Contains(q, "regex") || strings.Contains(q, "pattern") || strings.Contains(q, "struct ") || strings.Contains(q, "interface") || strings.Contains(q, "decorator")
}

func looksLiteralQuery(q string) bool {
	return strings.ContainsAny(q, "\"'") || strings.Contains(q, " exact ") || strings.Contains(q, "env ") || strings.Contains(q, " config") || strings.Contains(q, "error") || strings.Contains(q, "message") || strings.Contains(q, "returned") || strings.Contains(q, "returns") || strings.Contains(q, "response")
}

func looksBehaviorQuery(q string) bool {
	return strings.Contains(q, "how") || strings.Contains(q, "flow") || strings.Contains(q, "works") || strings.Contains(q, "behavior") || strings.Contains(q, "validate")
}
