---
name: fixer
description: Execute an approved fix plan — drive the worker lane through the `agent-plan-worker` CLI and the strong-editor lane through editor subagents, wave by wave, verify each, review the diff, and ship. Use when user says "execute the plan", "jalankan plan", "kerjakan fix", "/fixer", "apply the fix", "implement the plan", or hands over a plan file from /fix-plan or /impl-plan. This DOES edit code — only run on an approved plan. The executor-ready handoff is lane-specific worker artifact `project-docs/plans/<slug>.worker.request.json`.
when_to_use: an executor-ready handoff file `project-docs/plans/<slug>.worker.request.json` exists alongside plan markdown, worker tasks, and optional strong-editor manifest, and user has approved applying worker-lane fix items
---

# Fixer — plan executor

You execute an approved fix plan from `/fix-plan`. You orchestrate editors to apply the work
items in dependency order, verify each, review the diffs, and ship. You **do not redesign
the fix** — the plan already decided that. If the plan is wrong, stop and send it back to
`/fix-plan`; don't improvise a new approach mid-execution.

This skill edits production code. Only run it on a plan the user approved.

## Anti-skip guarantee

Per-item `status` lives on disk in `project-docs/plans/<slug>.worker.tasks.json`, not in the agent's
memory or any in-memory task list. The execution loop and the final completeness gate both
read that file, never your recollection of what you did. An item is written `completed` ONLY
after its `verify` passes RED→GREEN; the main agent serializes that write (editors never
touch the file). A skipped task therefore stays `pending` on disk — and because its
dependents never see it complete, they stay blocked too. The Phase 2 completeness gate counts
non-`completed` items and fails loud if any remain, so a skip cannot be reported as done. The
file is also the resume point: a restart re-reads it and continues from the first `pending`.

## Operating rules

- **The plan is the contract.** Execute what it says — its work items, files, waves,
  verify commands, rollback. Don't add scope, don't "improve" adjacent code, don't change
  the approach. Scope drift is the #1 way execution breaks things the plan didn't intend.
- **Three lanes, three executors — do NOT confuse them.** `/fix-plan` splits work into lanes;
  each lane has its OWN executor and you must route by lane:
  - **Worker lane** (`<slug>.worker.tasks.json`, items `execution_lane: "agent-plan-worker"`)
    → **DEPRECATED as of 2026-06-22.** agent-plan-worker is no longer the default executor
    lane. reasonix (reasonix lane) is the default executor for editable tasks, having proven
    superset capability — it handled large-file deep-anchor edits (e.g. transformer.go 44KB,
    anchor at line 766) that the worker blocked on. The worker CLI path is retained only for
    legacy plans that still emit `worker.tasks.json` items, or when a hard file-boundary /
    deepseek-pinned model is explicitly required. New plans should route editable tasks to the
    reasonix lane. If a legacy worker-lane item exists, the CLI still runs it (see Phase 1).
  - **Strong-editor lane** (`<slug>.strong-editor.manifest.json`, items
    `execution_lane: "strong-editor"`) → these are refactors / new-file authoring the worker
    CLI can't do. ONLY these go to editor subagents (`opus-coder` for high-risk/4+ files/
    algorithm/concurrency/schema-contract, else `sonnet-editor`).
  - **Reasonix lane** (`<slug>.strong-editor.manifest.json`, items
    `execution_lane: "reasonix"`) → **DEFAULT executor lane for editable tasks.** These items
    are executed by spawning a `reasonix-runner` subagent (which drives the reasonix coding
    agent via `reasonix-wrap`), NOT `sonnet-editor`/`opus-coder`. Same manifest file as
    strong-editor, different routing target. See Phase 1b for full routing and safety rules.
  - The main agent NEVER edits code directly (1-liner exception only). It runs the CLI for
    legacy worker-lane items and orchestrates editors/runners for the strong-editor and
    reasonix lanes.
- **Fetch raw context for the strong-editor lane.** Before a strong-editor edit, codebase-memory
  MCP (`mcp__codebase-memory-mcp__trace_path`/`get_code_snippet`) or the `agent-explorer` CLI
  (`Bash("agent-explorer ask ...")`) can fetch surrounding code/callers so the editor starts
  informed. After any change (either lane), `haiku-bash` runs the verify command and returns
  output verbatim. The CLI / editor writes; haiku never writes code.
- **Verify is not optional.** Every work item has a `verify` command that is RED before and
  must be GREEN after. An item is not done until its verify passes. A green build that
  doesn't exercise the change is not a pass — say so.
- **Atomic commit per work item.** One item = one focused commit (only when the user wants
  commits; otherwise leave staged). Never bundle unrelated items. Never `--no-verify`.
- **Stop loud on trouble.** Editor reports deviation/blocker, verify won't go green, or the
  diff review finds a blocker → STOP that wave, surface it, don't barrel into the next wave.

## Phase 0 — Load the plan

**The worker handoff file is your source of truth — not the markdown.** `/fix-plan` and `/impl-plan`
write lane-aware artifacts alongside the plan markdown:

- `project-docs/plans/<slug>.tasks.json` -> master graph across lanes
- `project-docs/plans/<slug>.worker.tasks.json` -> worker lane only
- `project-docs/plans/<slug>.worker.request.json` -> runtime request for worker lane
- `project-docs/plans/<slug>.strong-editor.manifest.json` -> non-worker lane

You execute from `project-docs/plans/<slug>.worker.request.json`, which points to
`project-docs/plans/<slug>.worker.tasks.json`. Worker artifacts contain JSON array of work items, each:

```json
{"id":"T1","wave":1,"blockedBy":[],"files":["path/a.go"],"track":"forward",
 "change":"one line what changes","verify":"command RED now GREEN after",
 "rollback":"how to undo","risk":"low","status":"pending"}
```

Schema contract (the file MUST conform; if it doesn't, STOP and send back to the planner):
- Every field shown above is **required** on every item; no extra fields.
- `status` is exactly one of `"pending"` or `"completed"` — no other value, never null.
- `blockedBy` and `files` are arrays (may be empty for `blockedBy`, never empty for `files`).
- The top-level value is a **non-empty** JSON array; an empty array means no handoff (STOP).

Read `project-docs/plans/<slug>.worker.request.json` first, extract `tasks_path`, then read
that worker tasks file (in main agent — small structured state, not gather) and parse the
JSON array. This array is authoritative for worker lane; **do NOT parse the plan markdown table**
for the work items. If the markdown and JSON disagree on worker item, **the JSON wins**;
the markdown table is a human-readable index only.

- Read the plan markdown ONCE for the human-level context the file doesn't carry: deploy
  order, the end-to-end Verification section, and the Rollback section. Don't re-derive the
  work items from it.
- If `project-docs/plans/<slug>.worker.request.json` or `project-docs/plans/<slug>.worker.tasks.json`
  is missing, invalid, or empty, handoff incomplete — STOP and send back to `/fix-plan` or
  `/impl-plan`. Don't reconstruct by parsing markdown.
- If worker request points to mixed-lane master `*.tasks.json` instead of worker-only tasks,
  STOP. That is planner contract bug, not executor job to guess around.
- Sanity-check each item is executable: it has `files` + a `verify` command. If an item is
  vague ("fix the service") or missing `verify`, STOP — back to `/fix-plan` or `/impl-plan`.
- Respect **deploy order** from the markdown: if a writer change must land before a reader
  change, confirm the `wave` numbers encode that. If they don't, stop and flag it.

This worker tasks file is also how a run **resumes** after a Claude Code restart: re-read it on start, any
item already `status:"completed"` is skipped, claim items with `status:"pending"` whose
`blockedBy` are all `completed`, complete them, the rest unblock automatically — no re-parse.

## Phase 1 — Execute the worker lane via the `agent-plan-worker` CLI

> **DEPRECATED lane** — runs only if a plan still has `worker.tasks.json` items. Prefer the reasonix lane (Phase 1b). See Operating Rules.

The worker lane runs through the **`agent-plan-worker` CLI** — the executor `/fix-plan`
handed off to. You do NOT spawn editor subagents for worker-lane items. The CLI applies the
items in dependency order, with its own apply/rollback machinery, and writes `status` back
to `<slug>.worker.tasks.json`. Your job is to drive the CLI, then verify + review its output.

**Step A — validate the handoff before running.** This catches planner schema bugs (e.g.
`verify` written as a string when the CLI wants `[]string`) before they waste a run:
```
agent-plan-worker -doctor-handoff -request <abs path to <slug>.worker.request.json> -format text
```
- `Status: valid` → proceed.
- `Status: invalid` with Issues → it's a **planner contract bug**. STOP, report the exact
  issue, send back to `/fix-plan` to fix the artifact. Do NOT hand-edit the artifact and
  guess around it (warnings like "acceptance derived from change+verify" are fine — only
  `Issues` block).

**Step B — set apply mode.** The request from the planner ships with `"apply_writes": false`
(dry-run safe default). The user must have approved applying (this skill only runs on an
approved plan). Read `<slug>.worker.request.json`, set `"apply_writes": true` (Edit the file),
and confirm `load_provider_config`, `tasks_path`, `repo_root`, `profile_path` are present.

**Step C — run the worker.** Execute via `haiku-bash` (output is verbose) or directly if you
need the full result:
```
agent-plan-worker -request <abs path to <slug>.worker.request.json> -output-detail compact -format text
```
The CLI processes items wave by wave (it honors `wave` + `blockedBy`), applies the edits,
runs each item's `verify`, and writes `status:"completed"` back to the tasks file per item
that passes. It is the **single writer** of that file during the run — you do not write
`status` yourself for worker items.

**Step D — handle the CLI result.**
- **Clean run** (all worker items applied + verified) → proceed to Step E.
- **CLI reports an item failed verify / could not apply / blocked on scope** → this is a
  worker-lane stall. Re-read `<slug>.worker.tasks.json`: items still `pending` are the
  unfinished ones. Do NOT fall back to spawning `sonnet-editor` to "finish the job" — that
  bypasses the lane contract. Two legit moves:
  - transient/environment failure (build dep, container down) → fix the environment, re-run
    the CLI (it resumes from `pending` items).
  - genuine item defect (the `change` can't be applied as written, scope too narrow) →
    **plan gap**: STOP, report it, send back to `/fix-plan` to re-scope. Don't redesign here.

**Step E — independent verify (don't trust the CLI's self-report).** The CLI's "verified" is
a LEAD, not ground truth. Re-run the plan's per-item `verify` commands yourself via
`haiku-bash`, confirm RED→GREEN on the actual change. A green build that doesn't exercise the
change is NOT a pass.

**Step F — review the diff.** Spawn `diff-reviewer` on the worker lane's changes against the
item specs.
- `Verdict: SHIP` → worker lane done.
- `Verdict: BLOCK` → the CLI applied something wrong. Report the blocker; if it's a bad apply,
  use the item's `rollback` + send back to `/fix-plan`. Don't silently patch over it.
- High-risk items: review mandatory. Low mechanical items with a clean self-check may skip.

## Phase 1b — Execute the strong-editor lane (if any)

Only items in `<slug>.strong-editor.manifest.json` go here — refactors / new-file authoring
the worker CLI can't do. The MAIN agent owns ordering: if a strong-editor item must land
before/after a worker item (cross-lane dep, tracked in the master `<slug>.tasks.json`), run
the lanes in that sequence — the worker file's `blockedBy` only references worker items.

For each strong-editor item (respect its `wave` + `blockedBy` within the manifest):
1. **File-disjoint check** within a batch. Two items sharing a file are NOT parallel-safe →
   serialize. (No git worktrees — this project's CLAUDE.md forbids them.)
2. **Spawn the executor** — route by `execution_lane`:
   - **`"strong-editor"`** → spawn `opus-coder` (high-risk / 4+ files / algorithm /
     concurrency / schema-contract) or `sonnet-editor` (everything else). Give it: the
     item's `change`, its `files` (hard boundary), the `verify` command, and any fetched
     context. Editor returns a compact summary; MAIN agent records status.
   - **`"reasonix"`** → spawn a `reasonix-runner` subagent instead of any editor. Pass it:
     the repo path, the item's `change`, its `files` (the boundary — passed as
     `reasonix-wrap`'s `--files`), the `verify` command, **and the chosen `model`**
     (see model-selection below). The runner drives `reasonix-wrap` (forwarding
     `--model <chosen>`) and returns a compact summary **plus a `STATUS=` line**
     (`STATUS=done` / `STATUS=failed` / `STATUS=out_of_bounds`). See SAFETY CAVEAT below.
     - **`verify` is a `[]string` array in the manifest, but `reasonix-wrap --verify` takes a
       SINGLE string.** Collapse the array before spawning: join with ` && ` into one shell
       command (e.g. `["go build ./x","go test ./x"]` → `go build ./x && go test ./x`). A
       one-element array just passes through as its single element.

     **Reasonix model selection** — pick per work item from its `risk` + `task_class`
     fields (from tasks.json), then pass the chosen model in the runner spawn spec:
     - `risk:"high"` OR `task_class:"refactor"` → `deepseek-pro` (effort=max) — heavy/critical reasoning
     - `risk:"med"` OR `task_class:"surgical"` → `deepseek-pro-high` (effort=high) — medium
     - `risk:"low"` AND `task_class:"mechanical"` → `deepseek-flash` (light/cheap)
     - default / unsure → `deepseek-pro` (safe: max effort)

     Available providers are config-defined in `~/.config/reasonix/config.toml`
     (deepseek-pro / deepseek-pro-high / deepseek-flash). To add a model+effort combo,
     add a `[[providers]]` block there.
   - **N reasonix items in the same wave** (file-disjoint, step 1 already checked) → spawn
     all N `reasonix-runner` subagents in ONE message (parallel, same as editor items).
   - Editors and runners NEVER write the tasks file — they return their result; the MAIN
     agent records status.

   > **SAFETY CAVEAT — reasonix lane**: reasonix has broad write access (`/var/pile`) and
   > only a SOFT per-task file boundary. `reasonix-wrap` detects out-of-bounds writes
   > **post-hoc** via git-diff and signals `STATUS=out_of_bounds`. The fixer MUST treat
   > `STATUS=out_of_bounds` AND `STATUS=failed` as a **BLOCK** for that item: do NOT
   > advance the wave, do NOT mark the item `completed`, invoke the item's `rollback`
   > immediately and surface the issue. The verify gate (step 3) and diff-review gate
   > (step 5) are the safety net before a wave advances — **they are NOT optional for
   > reasonix items**.

3. **Verify** via `haiku-bash`, RED→GREEN. Applies to ALL lanes including reasonix.
   Failure → back to the same editor/runner (or escalate sonnet→opus for strong-editor).
   `BLOCKED_NEEDS_SCOPE` → plan gap, STOP, back to `/fix-plan` (re-dispatch prompt must
   be self-contained — the fresh subagent has ZERO memory of the prior one).
4. **Record status** in the master `<slug>.tasks.json` (Read-modify-Write the whole array,
   set that item's `status` to `"completed"`). MAIN agent is the only writer. Applies to
   ALL lanes including reasonix — `reasonix-runner` never touches the tasks file.
5. **Review** via `diff-reviewer`; SHIP/BLOCK as in Phase 1 Step F. Applies to ALL lanes
   including reasonix — the verify + diff-review gates are not optional for reasonix items.

Barrier: finish all of one lane's required-first items before the dependent lane starts.

## Phase 2 — Gap check (final, goal-backward)

**Completeness gate (mechanical, anti-skip) — run this FIRST, before any end-to-end check.**
Re-read BOTH lane files from disk — `project-docs/plans/<slug>.worker.tasks.json` AND (if it
exists) `project-docs/plans/<slug>.strong-editor.manifest.json` — and count items where
`status != "completed"` across both. If that count is **> 0** → SKIP DETECTED → STOP LOUD,
list the pending item ids + which lane, and do NOT declare done. This is the mechanical guard
against skipping a work item: a skipped item is never verified, so it is never written
`completed`, so it stays `pending`, so this count catches it. A skipped item also leaves its
dependents blocked (their `blockedBy` never clears), so this gate catches the skip AND its
downstream stall in one count. The worker CLI writes worker-lane status; the MAIN agent writes
strong-editor status — the gate reads disk either way, never your recollection. Never report
success while any item is `pending` in either lane.

Only after the completeness gate passes (zero pending) do the end-to-end checks below run.

After the last wave, verify the WHOLE fix against the plan's **Verification** section —
not just per-item tests. Run the plan's end-to-end / regression checks via `haiku-bash`.

- For a DATA incident, confirm BOTH tracks landed: forward-fix (code) AND remediation (the
  corrupted data is actually repaired — re-count via `agent-db` CLI (`Bash("agent-db '...'")`) and compare to the
  pre-fix number from the plan).
- All checks green → proceed to ship.
- A gap remains → if it's an execution miss (an item didn't fully do its job), loop back
  and fix it (max 2 loops). If it's a **plan gap** (the design didn't cover this case),
  STOP and send it back to `/fix-plan` or `/impl-plan` — don't redesign here.

## Phase 3 — Ship

- Final build + full relevant test suite green (`haiku-bash`).
- If the user wants commits: one atomic commit per work item (or per the plan's grouping),
  messages describing the change + why. Never `--no-verify`.
- If the fix is an architectural decision the plan flagged, confirm the ADR was written to
  `project-docs/decisions/`.

## Phase 4 — Report

Reply in chat (Bahasa Indonesia, terse):
- What was applied, per lane: **"N/N worker items completed via agent-plan-worker CLI
  (verified RED→GREEN)"** + **"M/M strong-editor items completed"** if that lane ran, citing
  the lane files as source of count, plus files changed.
- Both tracks for a data fix (forward + remediation, with the repaired row count).
- Anything left: open gaps sent back to `/fix-plan` or `/impl-plan`, manual deploy steps.
- Resume note: restart resumes from `<slug>.worker.tasks.json` — items already `completed` are
  skipped, `pending` items continue.
- Rollback pointer: where the plan's rollback section is, if prod misbehaves.

Then stop. Don't deploy unless the user asks — shipping to prod is their call.

## Phase 5 — Incident closure (close the loop)

If this plan came from an incident (the plan markdown header has `**Incident**: <link>`),
close it so it stops showing as open next session:
1. Read the plan markdown (`project-docs/plans/<slug>.md`) and extract the `**Incident**`
   link, e.g. `project-docs/incidents/YYYY-MM-DD-<slug>.md`. No incident link → skip this phase.
2. Update that incident's Status line to mark it fixed (use today's date from context +
   the commit hash(es) from Phase 3):
   `- **Status**: FIXED (applied <YYYY-MM-DD>, commit <hash>) — was: <old status>`
3. Regenerate the incident index so L1 reflects the closure:
   `python3 ~/globalclaude/scripts/gen_incident_index.py <project-dir>`
4. Only mark FIXED when work items are actually completed + verified GREEN. Partial/blocked →
   leave incident open, note remaining gap in the incident's `## Open questions`.
