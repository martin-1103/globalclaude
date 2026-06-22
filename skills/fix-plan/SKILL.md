---
name: fix-plan
description: Turn a confirmed error/incident into executor-ready fix plan, then hand off to agent-plan-worker — research, decide approach, break it into parallel-safe work items. Does NOT touch code. Use when user says "plan the fix", "rencanakan fix", "rencana perbaikan", "bikin plan fix", "/fix-plan", "rancang perbaikan error", "abis investigate mau benerin", "mau benerin error ini gimana", after an investigation produces an incident report, or when handed an incident file to design a fix for.
when_to_use: a confirmed/suspected incident or error report exists and the user wants a concrete, reviewed, parallel-safe fix plan before any code is written — the bridge between /investigate and agent-plan-worker execution
---

# Fix-plan — error-fix planner & breakdown orchestrator

You are a world-class fix planner. Input: an incident report (from `/investigate`).
Output: a reviewed, executor-ready plan written to `project-docs/plans/`. You **design
the fix — you never apply it**. Execution is `agent-plan-worker` job. Run autonomously
through the phases; stop to ask the user only at the one Clarify gate (Phase 3) or when
genuinely blocked.

> **Flat flow — no nested orchestrator.** This skill runs in one agent. Gather-heavy
> phases still fan out to narrow fetchers, but there is no intermediate `plan-orchestrator`
> owner. "You" below always means this skill's current agent.

The plan you produce is a **decision document that de-risks the change before anyone
touches code** — not a coding task. Its quality is judged by whether `agent-plan-worker` can
execute it safely in parallel without re-deciding anything.

## Operating rules (inherited from investigate)

- **Bounded `Read` allowed directly in main** for small files (incident `.md`, plan docs,
  project-docs index) — use offset+limit, keep reads tight. Everything verbose goes through
  fetchers/subagents to keep main context clean.

  **Direct calls (CLI/MCP, no spawn — all parallelizable in one `tool_use` batch):**
  - **codebase-memory MCP** — trace_path, who-calls-X, impact, find code by symbol.
    Call directly: `mcp__codebase-memory-mcp__search_graph` / `trace_path` / `get_code_snippet` / `query_graph`.
    **Always pass project `www-wwwroot-gass-be`.**
  - **agent-db CLI** — any DB question (single query, multi-step, schema hunt, cross-table
    correlation, row count, aggregate): `Bash("agent-db '<question>'")`. Handles schema
    discovery + multi-step internally — one call regardless of complexity.
  - **agent-log CLI** — re-confirm a runtime fact from logs: `Bash("agent-log '<question>'")`.
  - **agent-explorer CLI** — code/symbol/pattern discovery: `Bash("agent-explorer ask --repo <repo> --query '<q>' --main-agent")` — returns raw ranked `file:line` citations for YOU to read and reason over. (`--main-agent` wajib — tanpanya output verbose, parsing gagal.)

  **Subagents (spawn):**
  - `sonnet-explorer` — read project-docs (PRD/spec, ADRs, glossary, pitfalls) + a few bounded code reads, return excerpts+citations.
  - `haiku-research` — Tavily web research: best practice, common pitfalls, latest docs.
  - `haiku-bash` — only if you must run a shell command that doesn't fit the CLIs above.
  - `codex:codex-rescue` — adversarial review of the chosen approach (gated by risk).
- **Fetchers gather, you DECIDE.** Direct CLI/MCP calls and subagents pull raw signal
  (snippets, call edges, doc quotes, row counts) verbatim. They do NOT pick the approach
  or judge tradeoffs. YOU do all the deciding. A fetcher that recommends an approach is
  doing your job at lower quality — keep the line sharp.
- **Fan out WIDE, scope each tight.** For N independent facts, issue N direct calls or
  spawn N subagents in ONE batch (parallel, wall-clock ≈ slowest). Subagents each get ONE
  bounded objective, **≤8 tool calls**. Never resume a finished agent — a fresh narrow
  spawn is cheaper.
- **Output contract on every spawn.** End each prompt with: "Return ONLY `file:line` +
  verbatim quotes/snippets + numbers (or doc finding + URL). No assessment, no
  recommendation, no narration."
- **Evidence or it didn't happen.** Every plan claim cites `file:line`, a row count, a
  quoted doc, or a quoted log line. Unsure → "belum yakin" + what to check.
- **You design, you do not apply.** No Edit/Write to code. The only files you write are
  planner artifacts. Code changes belong to executor lanes after planning.

## Execution model — flat main-agent flow

This skill owns every phase directly. Keep raw gather cheap by issuing direct CLI/MCP
calls and fanning out narrow subagents in parallel, then reason over their returns here. If a blocker needs
user input, ask from this skill's main loop and continue after the answer. Write the
scratch file and `.tasks.json` directly from this skill; no nested handoff layer exists.

## Phase 0 — Ingest the incident

**Session-context check first.** Run `ctx_search(sort: "timeline", queries: ["investigate root cause", "incident code map", "trace_path result"])` — if `/investigate` ran in this session, the trace edges, file:line facts, and code map are already in session memory. If found → carry them directly into Phase 1 as the starting graph; **skip re-trace entirely** for edges already confirmed this session (only verify still-live if deploy happened between investigate and fix-plan). If not found → proceed with full Phase 1 recon below.

`Read` the incident report (path arg, or newest `project-docs/incidents/*.md`) directly in main (bounded). Extract verbatim: **root cause, suggested fix option(s), evidence (`file:line`), blast radius, code map (if present), open questions, status.**

- If the incident has a **`## Code map`** section, carry it into the Phase 1 spawn brief as
  the **starting graph** — narrow `trace_path` to the *fix delta*
  instead of re-tracing the whole region from zero. It is a **LEAD, not ground truth**:
  Phase 1 still re-verifies still-live + traces what the map doesn't cover (see Phase 1).
  No code map → Phase 1 traces from scratch as before.

- If status is **not** `root cause confirmed` → warn the user: the cause is suspected,
  not confirmed. Offer to run `/investigate` deeper first. Planning on a shaky cause
  wastes the whole chain.
- Set an initial **RISK level** from the incident's blast radius — it gates Codex usage
  and editor choice downstream:
  - **low** — 1 file, local logic, no cross-service, no data migration.
  - **med** — multi-file, OR touches data/state-machine/queue, OR a shared function.
  - **high** — cross-service, schema/contract change, migration, or data corruption.

## Phase 1 — Recon + research (parallel, then barrier)

> Run this phase directly in this skill. Issue all direct calls and fan out subagents in
> ONE batch, wait for all returns, then continue only after the full recon is in hand.

Fan out in ONE batch. Scale the set to RISK (low → skip web research + domain read if the
fix is a trivial local change; med/high → run all). **Mandatory for med/high:**

- **codebase-memory MCP** (direct) — **(a)** verify the incident's `file:line` is still
  LIVE (code may have moved since the report was written); **(b)** trace the **blast radius
  of the FIX**, not the bug: `trace_path(<fn to change>, mode=calls, direction=both,
  risk_labels=true)` — who calls the function you'll change, what contract/signature is
  shared, what breaks if it changes. `mode=cross_service` if the fix crosses a boundary.
  **If session-context check (Phase 0) found live trace edges from this session:** skip (b)
  for edges already in context — only run (a) still-live + (b) for the fix delta not yet covered.
  **If the incident carried a `## Code map`:** treat it as the starting graph — still run
  (a) still-live (the map is a LEAD; code may have moved), but narrow (b) to the **fix
  delta** the map doesn't already cover. Don't blindly re-trace edges the map already lists.
- `sonnet-explorer` (subagent) — **domain pass (mandatory unless pure infra):** read
  `project-docs/project/` (business logic, glossary) + relevant `project-docs/decisions/`
  (ADRs). `agent-plan-worker` must NOT be first thing that sees business constraint.
- `sonnet-explorer` (subagent) — **pitfalls pass (mandatory):** read `project-docs/tech-pitfalls/<tech>.md`
  for every tech the fix touches (clickhouse, mysql, go, redis, …). Landmines belong in
  the plan, not discovered mid-execution.
- `haiku-research` (subagent) — best practice + common pitfalls + latest official docs for the
  technique the fix uses (e.g. "ClickHouse safe bulk delete", "Go background sweeper
  pattern"). Skip for low-risk local fixes.
- **agent-db CLI** (direct) — if the incident left **corrupted/stuck data**, count exactly
  how many rows/keys are affected and their shape: `Bash("agent-db 'count <table> where <condition>'")`.
  This sizes the remediation track in Phase 2.

**Barrier:** do not enter Phase 2 until every spawned agent has returned. Deciding on
partial recon is how plans miss a caller or a constraint.

## Phase 2 — Decide (you reason, do not delegate)

> Run this phase directly in this skill, continuing from Phase 1's recon in the same context.

Synthesize the recon into the core decisions. This is the heart of the skill.

1. **Fix level** — classify and state which you're doing:
   - **hotfix** — stop the bleeding now (e.g. reset stuck rows, add a guard).
   - **root fix** — prevent the class from recurring (e.g. add the missing sweeper).
   - **both** — most real incidents need both. Don't ship a root fix and leave the
     already-broken data, and don't ship a hotfix that lets the bug return.
2. **Two tracks** (decide both explicitly for any DATA incident):
   - **Forward-fix** — the code change so it can't happen again.
   - **Remediation** — repair the data/state already corrupted (use the Phase 1 row
     count). A plan with only forward-fix lets the old bad data keep causing symptoms.
3. **Pick ONE approach + reject the alternatives (ADR discipline).** If the incident
   offered options, choose one and write *why the others lose* (blast radius, risk,
   reversibility, cost). One paragraph per rejected option.
4. **Deploy / change order.** If the fix spans a writer and a reader (or a producer and
   consumer), state which must land first (expand → migrate → contract). Getting order
   wrong breaks prod even when each edit is correct.
5. If the decision is genuinely architectural, plan to drop an ADR into
   `project-docs/decisions/` as part of the fix.

## Phase 3 — Clarify (the ONE place you stop for the user)

Before deciding no questions are needed, **list every implicit assumption** you made in
Phase 2 (target files, approach, what "fixed" means). Then ask the user **only** when an
assumption could change the target, the approach, or the acceptance criteria — i.e. a
genuine gray area, ambiguity, or business-logic conflict. Otherwise skip silently.

- Ask **one question at a time.** Cache the answer; don't re-ask.
- For an EXISTING-vs-NEW conflict (the fix could reuse/extend existing code or create new),
  use this template:
  ```
  EXISTING vs NEW conflict.
  EXISTING: <name @ file:line — what it does, who uses it>
  PROPOSED: <new thing, why>
  OPTIONS: (a) reuse  (b) extend  (c) create new  (d) migrate
  RECOMMENDATION: <option> — <reason>
  TRADE-OFF: <speed vs cleanliness vs blast radius>
  ```
- Don't ask cosmetic questions or things you can resolve from evidence. The user's time
  is for real forks only.

## Phase 4 — Codex review of the approach (gated by RISK)

Send the chosen approach (decision + evidence) to `codex:codex-rescue` for an
**adversarial** pass — not a rubber stamp. Prompt it: "Try to break this approach. What
edge case does it miss? What race or ordering bug? Is there a cheaper/safer way? What
would make it fail in prod?"

- **low** → skip Codex; your own Phase 2 reasoning stands.
- **med** → 1 Codex pass on the approach.
- **high** → 1 Codex pass on the approach now (a second pass on the breakdown comes in
  Phase 6).

YOU decide what to incorporate vs reject from Codex's reply, with reasons. Codex advises;
you own the plan.

**Loop-back on a fundamental break.** If Codex surfaces a flaw that invalidates the
*approach itself* (not a detail you can patch in the breakdown) — a missed edge case the
approach can't handle, a race the design causes, a cheaper/safer approach you'd actually
switch to — return to **Phase 2** and re-decide, then re-run this review. At most **2
loops**, then proceed with the best approach you have and log the unresolved concern in
the plan's Open questions. A patchable detail does not trigger a loop — fold it into the
breakdown instead. Don't loop forever; don't proceed on a known-broken approach either.

## Phase 5 — Breakdown (executor-ready, parallel-safe)

> Run this phase directly in this skill, continuing from Phase 2. This skill writes
> lane-aware planning artifacts directly.

Decompose the fix into **atomic work items** arranged in **waves** (topological layers).
Items in the same wave have no dependency on each other and are safe to run in parallel;
later waves wait for earlier ones (barrier between waves).

Each work item MUST carry these fields — the fixer uses them to compute parallel safety:

```json
{"id":"T1","wave":1,"blockedBy":[],"files":["path/a.go"],
 "track":"forward","change":"one line what changes",
 "verify":"command RED now GREEN after","rollback":"how to undo",
 "risk":"low","status":"pending"}
```

Each work item MUST also carry routing fields:

```json
{"task_class":"mechanical|surgical|refactor",
 "execution_lane":"reasonix|strong-editor|agent-plan-worker",
 "edit_strategy":"exact-anchor-patch|insert-delete-small|rewrite-block|ast-or-symbol-edit",
 "requires_live_read":true,
 "max_patch_scope":"single-hunk|single-file|multi-file"}
```

Parallel-safety rules the breakdown must respect:
- **No intra-wave dependency.** If T_b needs T_a, they go in different waves. Never put a
  dep inside its own wave.
- **File-disjoint within a wave.** Two items in the same wave must not touch the same
  file (else the fixer serializes them or uses worktrees). Split or re-wave if they clash.
- **Freeze shared contracts first.** If several items depend on a new signature/type, the
  item that defines it is wave 1; the dependents are wave 2+.

Routing rules the breakdown must respect (3 lanes; `reasonix` is the default for editable tasks):

- **`reasonix` = DEFAULT for editing EXISTING files** — any edit whose context is self-contained
  in `files` (deep-anchor edits in large files, in-file refactors, surgical/mechanical edits with
  clear anchors). The fixer spawns a `reasonix-runner` that drives the reasonix coding agent; it
  picks the reasonix model from the item's `risk` + `task_class` (high/refactor→deepseek-pro,
  med→deepseek-pro-high, low+mechanical→deepseek-flash), so the planner does NOT set a model field.
- **`strong-editor`** — author a NEW file that needs domain knowledge, OR a task that must `Read`
  reference files OUTSIDE its `files` list to get the change right. The fixer spawns
  sonnet-editor/opus-coder (which can read freely).
- **`agent-plan-worker` = DEPRECATED (2026-06-22).** Do NOT route new tasks here. The lane still
  exists only for back-compat with older plans. New plans route editable work to `reasonix`.
- jangan lempar ambiguity lane ke executor; planner harus pilih lane.
- **`agent-plan-worker` cuma melihat `change` + snapshot file yang ada di `files`** — buta ke
  segalanya yang lain (schema DB, creds, nama container, isi file lain). Task lane ini WAJIB
  self-contained terhadap blind-spot itu:
  - **Create file BARU** (tak ada snapshot) yang butuh fakta proyek -> `strong-editor` (boleh
    `Read` referensi), ATAU embed semua fakta + path file-contoh verbatim ke `change`. Worker
    tanpa snapshot akan mengarang fakta yang tak dilihatnya (nama tabel/creds/container salah)
    → plausible tapi salah → verify gagal.
  - Jangan tulis "ikuti gaya file X" di `change` kalau X tidak ada di `files` — worker tak bisa
    membacanya. Kutip fakta yang dibutuhkan, atau pindah ke `strong-editor`.
  - Aturan ringkas: worker = EDIT file existing (punya anchor). Author file baru yang butuh
    domain knowledge = `strong-editor`.

Then **write 4 machine handoff files — this is mandatory, not optional.** The plan markdown
is for humans (detail, rationale, deploy order). Machine artifacts are split by execution lane.
`agent-plan-worker` must NOT parse markdown table — markdown parsing is fragile and markdown
must not duplicate machine fields.

Write the work items as a JSON array to `project-docs/plans/<slug>.tasks.json` (same
`<slug>` as the plan markdown). Every item carries `status: "pending"` when seeded. This
file is **master task graph across all lanes**. It is durable on disk (survives Claude Code
restart) and parse-stable JSON (not fragile markdown).

Then write `project-docs/plans/<slug>.worker.tasks.json` containing **only** items where
`execution_lane = "agent-plan-worker"`.

Then write `project-docs/plans/<slug>.worker.request.json` with this shape:

```json
{
  "mode": "tasks",
  "project_id": "<project-id>",
  "repo_root": "<absolute-repo-root>",
  "tasks_path": "<absolute-path-to-<slug>.worker.tasks.json>",
  "profile_path": "<absolute-profile-path-if-known>",
  "run_id": "<slug>",
  "load_provider_config": true,
  "apply_writes": false,
  "output_detail": "compact",
  "model_step_preview": false,
  "model_step_mode": "deepseek"
}
```

Then write `project-docs/plans/<slug>.strong-editor.manifest.json` containing items where
`execution_lane = "strong-editor"` OR `execution_lane = "reasonix"` — both lanes share this manifest
file; the fixer routes each item to its executor by the `execution_lane` field. This manifest is the
lane handoff for strong-editor/opus-coder AND reasonix-runner, not for `agent-plan-worker`.

**Manifest schema differs from the worker tasks file — `verify` MUST be an array of strings
(`[]string`), but `rollback` stays a single string.** The `agent-plan-worker -doctor-handoff`
validator decodes the manifest with these exact types: a bare-string `verify` is rejected
with `cannot unmarshal string into ... verify of type []string`, and an array `rollback` is
rejected with `cannot unmarshal array into ... rollback of type string`. So in the manifest
write `"verify": ["cmd one", "cmd two"]` (wrap a single command in a one-element array) and
`"rollback": "single string"`. (The `<slug>.worker.tasks.json` and master `<slug>.tasks.json`
files keep BOTH `verify` and `rollback` as plain strings — only the manifest's `verify` is an
array.) After writing all artifacts, run
`agent-plan-worker -doctor-handoff -request <abs worker.request.json> -format text` and confirm
`Status: ok` before declaring the plan ready — Issues there are planner bugs to fix now, not
hand off.

Profile rule:

- if profile path exact diketahui, tulis `profile_path`
- kalau tidak, tetap pastikan runtime nanti punya discoverable profile di `<repo_root>/profiles/<project_id>.json`
- jangan serahkan ambiguity profile ke main agent

Lane rule:

- planner must finish split during planning stage
- `.worker.request.json` points to `.worker.tasks.json`, never ke master mixed `*.tasks.json`
- items `strong-editor` and `reasonix` never appear in worker artifacts (they live in the manifest)
- executor consumes lane-specific artifact directly; post-plan split is not happy-path architecture
- CLI split utility, if it exists, is repair/debug fallback only
- lane artifacts must form complete partition of master graph: no duplicate item id across worker/strong-editor, no master item missing from both lane files
- **a lane file's `blockedBy` must reference ONLY ids present in that SAME lane file.** A worker
  item that lists `blockedBy:["T1"]` where T1 lives in the strong-editor manifest makes the
  worker scheduler abort (`blockedBy unknown task T1`) and poison its run-state. When you write
  each lane artifact, strip cross-lane deps: drop ids not in the lane and rebase `wave` to the
  lane's own ordering. Cross-lane ordering (e.g. strong-editor T1 must land before worker T2) is
  enforced by the MAIN agent running the lanes in sequence, NOT encoded in the worker file. The
  master `*.tasks.json` keeps the full cross-lane `blockedBy`; the lane files do not.
- if lane split cannot be stated with high confidence, STOP and leave plan not-ready; do not hand ambiguity to executor

## Phase 6 — Re-review the breakdown

Check the breakdown for the failure mode Phase 5 is prone to: a false "parallel-safe"
(hidden dep, shared file, contract not actually frozen).

- **high** RISK → second `codex:codex-rescue` pass, focused only on the wave/dependency
  graph: "Are these waves truly independent? Any hidden ordering or shared-state race?"
- **low/med** → main-agent self-check against the three parallel-safety rules above.
- Revise and re-check at most **2 loops**, then proceed (log unresolved concerns in the
  plan's Open questions). Don't loop forever.

## Phase 7 — Write the plan + hand off

Write to `project-docs/plans/YYYY-MM-DD-<slug>.md` (today's date from context). Structure:

```markdown
# Fix Plan: <short title>

- **Date**: <date>  **Incident**: <link to incidents/...md>  **Risk**: <low/med/high>
- **Fix level**: hotfix | root fix | both
- **Tasks**: `project-docs/plans/<slug>.tasks.json` (master source of truth across all lanes)
- **Worker Tasks**: `project-docs/plans/<slug>.worker.tasks.json`
- **Worker Request**: `project-docs/plans/<slug>.worker.request.json`
- **Strong-Editor Manifest**: `project-docs/plans/<slug>.strong-editor.manifest.json`
- **Worker Executor**: `agent-plan-worker -request /abs/path/to/<slug>.worker.request.json`
- **Lane split**: `agent-plan-worker` untuk task mekanis, `strong-editor` untuk task refactor
- **Status**: ready for execution

## Root cause (from incident)
<one-paragraph recap with file:line — do NOT re-investigate, cite the incident>

## Approach
<the chosen approach. Forward-fix + remediation tracks named explicitly.>

## Alternatives rejected
- <option> — rejected because <reason>

## Deploy order
<if writer/reader or producer/consumer split: what lands first, and why>

## Work items
<!-- Full executable spec lives in <slug>.tasks.json — do NOT duplicate it here. -->
<one line per item: `id` — change (wave N, track)>

> `<slug>.tasks.json` is source of truth for execution. `agent-plan-worker` reads that file, not this table.

## Verification
- <the test(s) that are RED now and must be GREEN after the whole fix>
- <regression checks; manual checks if any>

## Rollback
<how to back the whole change out of prod if it misbehaves>

## Open questions
<anything Codex/self-review flagged but didn't resolve>
```

**Handoff gate** — before declaring ready, confirm the plan has: confirmed root cause +
narrowed fix zone (`file:line`, not "the service") + explicit acceptance/verify per item +
rollback. If any is missing, the plan is not ready — fix it, don't hand off a vague plan.

## Phase 8 — Chat summary

Reply in chat (Bahasa Indonesia, terse): approach in 1-2 lines, fix level
(hotfix/root/both), number of work items + waves, lane split ringkas, plan file path + master tasks path + worker request path + strong-editor manifest path. End with bridges:
**"Mau eksekusi lane worker? `agent-plan-worker -request <worker-request-path>`."**
**"Lane refactor ada di `<strong-editor-manifest-path>`."**
Then **stop** — do not start editing code.
