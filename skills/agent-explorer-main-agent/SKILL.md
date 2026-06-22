---
name: agent-explorer-main-agent
description: Use when code exploration needs compact, high-precision evidence for reasoning, especially for questions like "where is X", "how does Y work", "what calls Z", "trace this flow", "find exact error", or multi-file logic questions where noisy grep output would hurt accuracy. Use when Claude should retrieve evidence first, then reason from citations.
allowed-tools: Bash(/var/pile/agent-explorer/agent-explorer *),Bash(agent-explorer *)
---

# Agent Explorer Main Agent

## Overview

Use `agent-explorer` as retrieval engine, not final thinker. Goal: return short, typed, confidence-aware evidence pack with citations, then do reasoning on top.

## When to Use

- User asks code comprehension across more than one file
- User asks definition, behavior, caller/callee, retry path, auth flow, config locus, worker flow
- User wants exact error/message location with citations
- Repo large enough that raw grep or many file reads would add noise

## When NOT to Use

- One known file already open and answer obvious there
- Simple literal grep in one exact path
- Pure code-writing task with zero exploration needed

## Default Command

```bash
agent-explorer ask --repo "$REPO" --query "$QUERY" --main-agent --timeout 90
```

`$REPO` HARUS absolute path (mis. `/www/wwwroot/gass/be`), BUKAN slug — slug bikin `chdir: no such file or directory`.

`--main-agent` WAJIB — tanpanya output verbose (human-readable default), field `gaps` dan
`recommended_action` tidak ada, contract parsing tidak reliable.

Jangan pakai `--agent-mode` — itu format berbeda (`retrieval_pack`, tanpa `gaps`/`recommended_action`).

If binary not in `PATH`, use absolute path:

```bash
/var/pile/agent-explorer/agent-explorer ask --repo "$REPO" --query "$QUERY" --main-agent --timeout 90
```

## Output Contract (`--main-agent` format)

Header: `retrieval_contract`

Fields (satu baris per field):
- `status=grounded|weak_evidence|abstain`
- `intent=...`
- `question_class=literal|lookup|behavior|multi-hop|trace`
- `confidence=high|medium|low|none`
- `primary_evidence`, `supporting_evidence`, `trace_evidence` (sections hits)
- `gaps=...` — apa yang tidak ditemukan
- `recommended_action=reason|re-retrieve` — sinyal eksplisit dari retrieval engine

Diakhiri `<final_answer>` block dengan citations `file:line [symbol]`.

Jangan parse `retrieval_pack` header — itu output `--agent-mode` (flag berbeda, field berbeda).

## Operating Rules

1. Retrieval first.
   Run `agent-explorer` before answering if code path not already certain.

2. Trust contract, not hope.
   - If `status=grounded` and `recommended_action=reason`, reason from evidence.
   - If `status=weak_evidence` or `recommended_action=re-retrieve`, do not overclaim.

3. Keep context small.
   Prefer `--citation-only` untuk output ringkas.
   Use `--json` kalau butuh parsing terstruktur.

4. Escalate only when needed — protokol wajib, jangan skip.
   If first retrieval `weak_evidence` or `abstain`:
   - WAJIB retry sekali dengan query yang lebih sharp/spesifik (symbol name eksak, bukan konsep)
   - Kalau retry kedua masih weak → BARU fallback ke tool lain
   - Fallback order: codebase-memory MCP `trace_path`/`search_graph` (caller/callee, exact symbol) → rg/grep (literal string)
   - JANGAN langsung abandon ke MCP setelah hit pertama weak — retry dulu.

5. Preserve evidence boundaries.
   Retrieval engine finds code evidence.
   Claude does synthesis, tradeoff analysis, and final explanation.

## Query Rewriting

Rewrite vague asks into sharper retrieval queries.

Examples:

- `how auth works`
  -> `trace how auth middleware validates token and where claims are consumed`

- `where retry logic`
  -> `where retry logic lives`

- `timeout config`
  -> `where request timeout configured`

- `who uses claims`
  -> `which funcs call ClaimsFromContext`

- `exact unauthorized message`
  -> `find exact unauthorized error message`

## Fallback Commands

Caller/callee (sharper query):
```bash
agent-explorer ask --repo "$REPO" --query "which funcs call X" --main-agent --timeout 90
```

Machine-readable (structured parsing):
```bash
agent-explorer ask --repo "$REPO" --query "$QUERY" --main-agent --json --timeout 90
```

Citation-only (ringkas):
```bash
agent-explorer ask --repo "$REPO" --query "$QUERY" --main-agent --citation-only --timeout 90
```

## Common Mistakes

- Do not dump raw `rg` output when contract already grounded answer.
- Do not treat weak evidence as final truth.
- Do not ask retrieval engine to write long prose explanation first.
- Do not pass giant vague question if one sharper query would work.
- Do not ignore `gaps` or `recommended_action`.
- Do not pass slug as `--repo` — must be absolute path.

## Quick Reference

- Best default: `agent-explorer ask --repo "$REPO" --query "$QUERY" --main-agent --timeout 90`
- Compact output: add `--citation-only`
- Structured output: add `--json`
- Caller/callee: sharper query via `ask` OR codebase-memory MCP `trace_path`
- Weak result: rewrite query, rerun once, then reason only if `recommended_action=reason`
