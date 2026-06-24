# Implementation Plan: reasonix as a fixer worker lane

- **Date**: 2026-06-22  **Source**: conversation — "orchestrator = Claude (/fixer), worker = reasonix, paralel"
- **Risk**: med (new tooling, edits the fixer skill's executor flow; no prod code touched)
- **Status**: ready for execution
- **Tasks**: `/root/globalclaude/plans/2026-06-22-reasonix-fixer-worker-lane.tasks.json` (master)
- **Lane split**: 100% strong-editor (new-file authoring + skill edit, all need judgment)

## Context (why)

agent-plan-worker's tool-loop (built earlier this session) makes the worker edit files,
but reasonix is a more mature multi-tool coding agent already on this host
(`/www/server/nodejs/.../reasonix`). Rather than replace the worker wholesale (rejected —
see Alternatives), use reasonix AS the editor inside the existing `/fixer` orchestration.

Claude (`/fixer`) stays the orchestrator: it already owns wave sequencing, file-disjoint
checks, verify RED→GREEN, status write-back, and the diff-review gate (Phase 1b,
strong-editor lane). The ONLY change is step 2 of that lane: instead of spawning a
`sonnet-editor`/`opus-coder` subagent to edit, spawn a `reasonix-runner` subagent that
drives `reasonix run`. Everything else in the lane is unchanged.

**Recon facts (verified this session, drove the design):**
- `reasonix run -dir <root> -metrics M.json "<task>"`; stdout markers: `  -> tool {args}`,
  `  · N tok · in X (cached/new) · out Y · ¥cost` (per-step boundary), `  ▎ thinking`.
- NO per-invocation boundary flag — `workspace_root`/`allow_write` are global config only;
  `doctor` shows `write_roots /var/pile` (reasonix may write anywhere under /var/pile).
  → boundary must be enforced SOFT by the adapter (post-hoc git-diff check), not by a flag.
- Exit code unreliable: success=0, bad-model=1, BUT **max-steps-hit also =0** → cannot
  trust exit code for done-vs-incomplete. Verify (RED→GREEN) is mandatory.
- `-metrics` JSON written at EXIT only (not incremental) → stuck-detection needs live
  stdout poll / timeout, not metrics.
- NO built-in diff/git/rollback/undo → adapter wraps its own (git checkpoint per task).

## Requirement

**What**: Add a reasonix execution lane to `/fixer`. Build (1) a deterministic
exec+summary script, (2) a runner subagent, (3) the fixer integration.

**Scope IN**: `reasonix-wrap` script; `reasonix-runner` agent def; fixer SKILL.md reasonix
lane; soft-boundary check; deterministic summary; stuck timeout.

**Scope OUT**: editing reasonix itself; editing product code (gass-be); replacing
agent-plan-worker (it stays as the other lane); LLM-based summarization.

## Acceptance criteria

- `reasonix-wrap <repo> <task> --files a.go,b.go --verify "<cmd>"` runs reasonix, prints a
  COMPACT deterministic summary (header + tool-count aggregate + anomalies + files-touched
  + verify result + answer), writes raw to a log file, exits with a status the caller can
  branch on (`done|failed|out_of_bounds|timeout`). → manual run on a toy repo.
- Summary size does NOT grow linearly with step count (50-step run → ~constant summary).
- Out-of-bounds write (file outside `--files`) is detected via git-diff and surfaced.
- A reasonix run that exceeds a wall-clock budget is killed and reported `timeout`.
- `reasonix-runner` subagent returns ONLY the summary+status (raw verbose stays in its
  context, dropped on return) — no verdict/interpretation.
- `/fixer` Phase 1b routes `execution_lane:"reasonix"` items to `reasonix-runner` in
  parallel (one message, N tool calls), keeps verify + status + diff-review gate.

## Design

- **reasonix-wrap** (`/usr/local/bin/reasonix-wrap`, python3, zero deps): git-checkpoint
  (stash or rev-parse HEAD) → `reasonix run -dir <repo> -metrics <tmp> "<change + 'only
  edit these files: …'>"` with wall-clock `timeout` → run `--verify` → `git diff
  --name-only` vs `--files` (soft boundary) → parse stdout markers + metrics.json into a
  compact summary (strip ANSI; aggregate tool calls by name; list only anomaly lines —
  bash exit≠0, retries, gate/error markers; final non-dim text = answer). Print summary,
  write raw to `<repo>/.reasonix-runs/<id>.log`. Exit status mapped: done/failed/
  out_of_bounds/timeout. ALL parsing deterministic, NO LLM.
- **reasonix-runner** (`/root/.claude/agents/reasonix-runner.md`, model haiku, tools
  Bash+Read): given task spec, run reasonix-wrap, relay its summary + status VERBATIM.
  Mirrors haiku-bash discipline: return data, never a verdict. Context isolation = the
  verbose reasonix stdout never enters main context.
- **fixer SKILL.md**: in Phase 1b, add lane routing: `execution_lane:"reasonix"` →
  spawn `reasonix-runner` (parallel, file-disjoint within wave) instead of
  sonnet-editor/opus-coder. Steps 1,3,4,5 (disjoint-check, verify, status write-back,
  diff-review) UNCHANGED. Document the soft-boundary caveat + that diff-review gate is the
  safety net for reasonix's broad write_roots.

## Alternatives rejected

- **Replace agent-plan-worker wholesale (Go adapter)** — reasonix lacks per-task hard
  boundary, reliable exit codes, and rollback; rebuilding all three in a standalone Go
  executor is large and yields only SOFT boundary anyway. Using Claude/`fixer` as
  orchestrator gives the same parallelism with the gates already built.
- **LLM-based summary** — fabrication risk (CLAUDE.md: small models overclaim); reasonix
  output is already structured (markers + metrics.json) so deterministic parse is safer.
- **Single-layer (script only, no subagent)** — main context would eat each parallel
  run's output; subagent isolates it. Both layers justified.

## Deploy order

T1 (script) before T2 (runner calls it) before T3 (fixer calls runner). T4 validates T1.

## Work items

> Full spec in `.tasks.json`. Human index:

- T1 — `reasonix-wrap` script: exec + soft-boundary + timeout + deterministic summary (wave 1)
- T2 — `reasonix-runner` agent def (wave 2, needs T1)
- T3 — fixer SKILL.md reasonix lane in Phase 1b (wave 2, needs T1)
- T4 — validate reasonix-wrap on a toy repo: large-step summary stays compact, bounds +
  timeout fire (wave 3, needs T1; test track)

## Verification

- T4 is the end-to-end proof: run reasonix-wrap on a toy multi-step task, assert compact
  summary + correct status mapping + bounds detection + timeout kill.
- Fixer integration: dry-check the SKILL.md routing reads correctly (no code path to unit
  test; it's a prompt — review for the routing rule + unchanged gates).

## Rollback

- Tooling only: `rm /usr/local/bin/reasonix-wrap`, `rm reasonix-runner.md`, revert
  SKILL.md. No prod impact. agent-plan-worker lane untouched = fixer still works without
  reasonix.

## Open questions

- Soft boundary is post-hoc (detect+rollback after the fact), not prevention. Acceptable
  because diff-review gate + verify gate run before the wave advances. If reasonix's broad
  write_roots becomes a real problem, revisit: generate a per-task config.toml with
  `workspace_root` set (hard boundary) — deferred until proven needed.
- Stuck detection: T1 uses a wall-clock timeout (simplest). Live-progress poll (step
  marker increments) deferred unless timeout proves too coarse.
- maxToolTurns/maxSteps for reasonix per task — default, tune from real runs.
