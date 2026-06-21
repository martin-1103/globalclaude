# agent-explorer

Read-only terminal explorer for many repos, not one project only.

Current runtime:
- planner + answer composer via OpenAI-compatible chat API
- graph + graph-text via `codebase-memory-mcp`
- semantic retrieval via `claude-context` MCP + embeddings backend
- literal search via `rg`
- structural search via `ast-grep`

Goals:
- precision-first explore
- compact retrieval-pack terminal output
- bounded retrieval loop, not noisy fan-out
- evidence-first citations
- reusable across different codebases
- serve main agent/subagent as retrieval engine, not final reasoner

Best-practice retrieval shape:
- hybrid first-pass retrieval across lexical, graph, graph-text, semantic
- candidate fusion + reranking
- selective query decomposition for multi-part asks
- evidence typing: primary, supporting, trace, literal
- diversity + context budgeting before handoff to reasoner
- weak-evidence abstain signal instead of overclaiming

## Commands

```bash
agent-explorer ask --repo /path/to/repo --query "how auth middleware works"
agent-explorer trace --repo /path/to/repo --symbol ClaimsFromContext --direction inbound
agent-explorer eval --repo /path/to/repo --auto-learn
agent-explorer feedback --repo /path/to/repo --query "how auth middleware works" --accept-paths services/api/auth.go --reject-paths services/web/auth.ts
agent-explorer init-profile --repo /path/to/repo
agent-explorer init-eval --repo /path/to/repo
agent-explorer install
agent-explorer version
```

## Retrieval policy

- planner is outcome-first
- precision-first tool choice
- stop early on medium/high confidence
- max 2 tool families before stop or replan
- fallback only when confidence weak
- literal/config queries prefer `rg` first, then graph-text/graph for grounding
- mixed multi-hop queries must cover all important slots before they can become `grounded`
- semantic hits dedup per file
- output includes per-hit score + confidence band
- output includes retrieval status: `grounded`, `weak_evidence`, or `abstain`
- evidence hits are typed for downstream reasoners
- retrieval dedup uses fused ranking, not raw append order
- accepted/rejected memory shifts future anchor + hit ranking per repo
- LLM critic reranks top candidates after deterministic retrieval
- confidence calibration is intent-aware, not one threshold for all query classes
- dual-lane retrieval is adaptive: semantic+graph_text only for ambiguous or multi-part conceptual asks unless forced
- auto-learn ignores weak-evidence eval outcomes to reduce false memory imprinting
- feedback memory uses recency weighting so old wins do not dominate forever
- per-term retrieval fan-out is bounded by small worker pool to reduce latency spikes

## Output modes

- default: retrieval pack + scored hits + `<final_answer>`
- `--agent-mode`: ultra-compact retrieval pack for main agent/subagent
- `--explain`: optional short LLM summary on top of retrieval pack
- `--citation-only`: only `<final_answer>`
- `--json`: machine-readable payload

Default `ask` is retrieval-first.
Main agent should do reasoning on top of returned evidence.

## Config

Default config path:

```text
/var/pile/agent-explorer/config.json
```

Env overrides:
- `AGENT_EXPLORER_BASE_URL`
- `AGENT_EXPLORER_API_KEY`
- `AGENT_EXPLORER_MODEL`
- `AGENT_EXPLORER_CBM_BINARY`
- `AGENT_EXPLORER_CBM_CACHE_DIR`
- `AGENT_EXPLORER_CLAUDE_CONTEXT_COMMAND`
- `AGENT_EXPLORER_MEMORY_DIR`
- `AGENT_EXPLORER_TOOL_TIMEOUT_SECONDS`
- `AGENT_EXPLORER_MAX_SEARCH_RESULTS`
- `AGENT_EXPLORER_MAX_SNIPPET_LINES`
- `AGENT_EXPLORER_LLM_TIMEOUT_SECONDS`

## Eval suites

`eval` is repo-driven.

Lookup order when `--suite` omitted:

```text
<repo>/.agent-explorer-eval.json
<repo>/.agent-explorer/eval.json
/var/pile/agent-explorer/evals/template.generic.json
```

Use project-specific suite for real scoring. Generic template is scaffold only.
Critical regression subset available at:

```text
/var/pile/agent-explorer/evals/retrieval-critical-paths.json
```

`init-eval` now generates repo-local seed cases from live repo profile plus code graph when available.
`eval --auto-learn` appends accepted/rejected evidence from pass/fail cases into repo memory.
`eval` also prints retrieval metrics:
- `top1`: first hit matches expectation
- `top5`: any of first five hits matches expectation
- `weak_top1`: first hit exists but confidence is low
- `Status`: grounded / weak_evidence / abstain distribution across suite
- `Top1 Confidence`: high / medium / low / none distribution
- `Failure Taxonomy`: no hits, weak top1, wrong top1, rejected top1, expected hit only below top1

## Repo profiles

Optional per-repo profile:

```text
<repo>/.agent-explorer/profile.json
```

Use `init-profile` to scaffold one.

Profile controls:
- detected stack
- preferred primary tools
- disable semantic toggle
- query hints
- negative hints
- memory entry budget for maintenance policy
- optional `concept_overlays` for repo-local domain vocabulary

`concept_overlays` is where project-specific synonyms belong.
Do not hardcode repo domain terms inside core retrieval logic.

Example:

```json
{
  "concept_overlays": {
    "backfill": ["backfill", "delta_sync", "queue_backfill"],
    "reconcile": ["reconcile", "repair", "heal"],
    "projection": ["projection", "read_model", "materialized_view"]
  }
}
```

See:

```text
/var/pile/agent-explorer/examples/profile.with-concepts.json
```

Stack auto-detect supports Go, Node, Python, Rust, PHP, Java, Ruby.

## Adaptive memory

Generic self-learning base:
- accepted path/symbol memory
- rejected path/symbol memory
- topic-to-path memory per repo

Default flow:
- main agent calls `ask`
- main agent reasons over returned evidence pack
- team runs `eval --auto-learn` on real repo suites
- memory updates from pass/fail evidence

Manual `feedback` is admin/debug path, not required on every query.

Admin write feedback:

```bash
agent-explorer feedback \
  --repo /path/to/repo \
  --query "how backfill stalls are detected and where retry lives" \
  --accept-paths services/sync/internal/backfill/gap_detector.go \
  --reject-paths services/analytics/detect_anomaly.py
```

Effect:
- future ranking boosts accepted evidence
- future ranking penalizes repeated false positives
- anchor selection can reuse prior successful paths for similar topics

This is adaptive memory, not model weight training.

Audit and compact memory:

```bash
agent-explorer memory --repo /path/to/repo
agent-explorer memory-compact --repo /path/to/repo --keep-recent 3
agent-explorer memory-maintain --repo /path/to/repo --max-entries 1000
agent-explorer memory-maintain --repo /path/to/repo --apply
```

## Common pitfalls avoided

- pure semantic-first retrieval for exact code questions
- single-path retrieval with no lexical/graph fallback
- raw append-order ranking with no fusion
- duplicate hits from same file/function crowding out diversity
- handing too many weak hits to main reasoner
- forcing retrieval engine to be final reasoner

## Notes

- trace mode is compact caller/callee view via `trace_path`
- planner has ambiguity rules, stopping rules, negative examples
- runtime honors repo profile `max_tool_families`, not hardcoded `2`
- semantic search is embeddings-backed, not graph-only
- for theory/design direction, see `docs/self-learning-design.md`
- for best results on new repo: ensure code graph indexed, semantic backend reachable, then run `init-profile` and `init-eval`
