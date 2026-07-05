---
name: agent-explorer-maintenance
description: Use when retrieval quality drifts, memory store grows too large, stale evidence is suspected, eval reports memory maintenance needed, or Claude needs to inspect/compact/clean retrieval memory for agent-explorer across any repo.
allowed-tools: Bash(/var/pile/agent-explorer/agent-explorer *),Bash(agent-explorer *)
---

# Agent Explorer Maintenance

## Overview

Use this skill to keep `agent-explorer` retrieval memory healthy. Goal: inspect memory size, stale evidence, maintenance need, then compact or clean with minimal risk.

## When to Use

- Eval prints `Memory Maintenance: action_needed=true`
- Retrieval starts biasing to old or wrong paths
- Memory store seems too large
- Stale evidence suspected after refactors or file moves
- Need to audit accepted/rejected path hotspots

## When NOT to Use

- User only wants one retrieval answer
- No evidence of drift, stale memory, or budget pressure
- Fresh repo with near-empty memory

## Default Audit Commands

Inspect memory:

```bash
agent-explorer memory --repo "$REPO" --limit 5
```

Check maintenance need:

```bash
agent-explorer memory-maintain --repo "$REPO"
```

## Safe Workflow

1. Audit first.
   Run:

```bash
agent-explorer memory --repo "$REPO" --limit 5
agent-explorer memory-maintain --repo "$REPO"
```

2. Read signals.
   Focus on:
   - `Entries`
   - `Stale Entries`
   - `Top Accepted Paths`
   - `Top Rejected Paths`
   - `Top Stale Paths`
   - `action_needed`

3. Compact carefully.
   Dry policy is from `memory-maintain`.
   Actual cleanup options:

```bash
agent-explorer memory-compact --repo "$REPO" --keep-recent 3
agent-explorer memory-compact --repo "$REPO" --keep-recent 3 --drop-stale
```

4. Policy-driven maintenance.
   If ready to apply maintenance policy:

```bash
agent-explorer memory-maintain --repo "$REPO" --apply
```

5. Re-check retrieval quality.
   After cleanup, run:

```bash
agent-explorer eval --repo "$REPO" --limit 5
```

## Decision Rules

- If `Stale Entries > 0`, prefer `--drop-stale`
- If `Entries` exceeds repo budget, compact even if stale is zero
- If top accepted path dominates heavily and quality drift exists, compact before trusting memory
- If eval already strong and memory only slightly large, avoid aggressive cleanup

## Common Mistakes

- Do not compact blindly before audit
- Do not drop stale without re-checking eval after cleanup
- Do not assume big memory means useful memory
- Do not ignore one path dominating memory if retrieval gets repetitive
- Do not run maintenance and stop there; always verify with retrieval or eval

## Quick Reference

- Audit:
```bash
agent-explorer memory --repo "$REPO" --limit 5
```

- Policy check:
```bash
agent-explorer memory-maintain --repo "$REPO"
```

- Safe compact:
```bash
agent-explorer memory-compact --repo "$REPO" --keep-recent 3
```

- Aggressive stale cleanup:
```bash
agent-explorer memory-compact --repo "$REPO" --keep-recent 3 --drop-stale
```

- Apply maintenance policy:
```bash
agent-explorer memory-maintain --repo "$REPO" --apply
```
