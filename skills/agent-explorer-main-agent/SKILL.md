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

Use strict contract first:

```bash
agent-explorer ask --repo "$REPO" --query "$QUERY" --main-agent --timeout 60
```

If binary not in `PATH`, use absolute path:

```bash
/var/pile/agent-explorer/agent-explorer ask --repo "$REPO" --query "$QUERY" --main-agent --timeout 60
```

## Output Contract

Expect:

- `retrieval_contract`
- `status=grounded|weak_evidence|abstain`
- `intent=...`
- `question_class=literal|lookup|behavior|multi-hop|trace`
- `confidence=high|medium|low|none`
- `primary_evidence`
- `supporting_evidence`
- `trace_evidence`
- `gaps`
- `recommended_action=reason|re-retrieve`
- `<final_answer>` citations

## Operating Rules

1. Retrieval first.
   Run `agent-explorer` before answering if code path not already certain.

2. Trust contract, not hope.
   - If `status=grounded` and `recommended_action=reason`, reason from evidence.
   - If `status=weak_evidence` or `recommended_action=re-retrieve`, do not overclaim.

3. Keep context small.
   Prefer `--main-agent`.
   Use `--json` only if caller truly needs structured parsing.

4. Escalate only when needed.
   If first retrieval weak:
   - narrow query
   - ask second retrieval with sharper wording
   - use `trace` command for caller/callee specific questions

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

Caller/callee trace:

```bash
agent-explorer trace --repo "$REPO" --query "$QUERY" --direction both
```

Machine-readable:

```bash
agent-explorer ask --repo "$REPO" --query "$QUERY" --json --timeout 60
```

Compact but less strict:

```bash
agent-explorer ask --repo "$REPO" --query "$QUERY" --agent-mode --timeout 60
```

## Common Mistakes

- Do not dump raw `rg` output when contract already grounded answer.
- Do not treat weak evidence as final truth.
- Do not ask retrieval engine to write long prose explanation first.
- Do not pass giant vague question if one sharper query would work.
- Do not ignore `gaps` or `recommended_action`.

## Quick Reference

- Best default: `agent-explorer ask --repo "$REPO" --query "$QUERY" --main-agent --timeout 60`
- Trace question: `agent-explorer trace --repo "$REPO" --query "$QUERY" --direction both`
- Weak result: rewrite query, rerun once, then reason only if grounded
