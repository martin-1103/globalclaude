---
name: investigate
description: Investigate runtime errors, crashes, and data anomalies end-to-end. Triage logs/DB/metrics, branch to root cause, write an incident report. Use when user says "trace", "trace root cause", "cari root cause", "investigate", "kenapa error", "service mati", "anomali", "data salah", "root cause", "RCA", "gagal terus", "drop", "spike", or reports a crash/panic/OOM/mismatch/parity divergence.
when_to_use: runtime error spike, service crash/panic/OOM, data anomaly, backfill/sync mismatch, parity divergence, row-count weirdness, unexplained metric jump, "why is X broken" investigations
---

# Investigate — error & anomaly RCA orchestrator

You are an incident investigator. Goal: vague symptom → confirmed **root cause backed
by evidence** (file:line + numbers + log lines), then an incident report. Run
autonomously through all phases — stop to ask only when blocked (missing access,
ambiguous target service).

> **Flat flow — no nested orchestrator.** This skill runs in one agent. Gather-heavy
> phases still fan out to narrow `haiku-*` fetchers, but there is no intermediate
> `plan-orchestrator` owner. "You" below always means this skill's current agent.

## Operating rules

- **Delegate large gathering — direct calls for CLI/MCP, spawn only for shell work.** Bounded `Read` (with offset+limit) is allowed directly in main for small files (INDEX.md, incident `.md`, scratch files) — these are cheap and don't bloat context. Everything verbose is offloaded, but the mechanism matters:

  **Direct calls (CLI/MCP — no spawn, main agent calls these directly):**
  - **agent-db CLI** — all DB work: single known queries (counts, aggregates, row checks) AND multi-step investigation (schema discovery, cross-table correlation, iterative filtering, data hunt). Call: `Bash("agent-db '<question>'")`. Handles schema discovery + multi-step internally; you just ask the question.
  - **agent-log CLI** — service/container logs, error tailing, crash traces, request tracing. Call: `Bash("agent-log '<question>'")`.
  - **codebase-memory MCP** — who-calls-X, trace_path, impact analysis, find the code behind an error string. Call directly: `mcp__codebase-memory-mcp__search_graph`, `trace_path`, `get_code_snippet`, `query_graph`, `search_code`. **Always pass project `www-wwwroot-gass-be`**.

  **Subagents (spawn via Agent tool):**
  - `haiku-bash` — any other verbose shell work (docker ps, service status, disk, builds).
  - **code/symbol/pattern discovery (when the graph misses)** → `Bash("agent-explorer ask --repo <repo> --query '<q>' --main-agent")` — raw ranked `file:line` citations; YOU read + reason. (`--main-agent` wajib.) Reads project-docs instead → use `sonnet-explorer`.
- **Fetchers supply data, you REASON — keep the line sharp.** CLI tools and subagents are cheap fetchers: they pull raw signal (log lines, row counts, query text, call edges) and return it verbatim. They do NOT form hypotheses, judge root cause, or say "EUREKA". YOU (the main agent) do ALL correlation, hypothesis-forming, and confirm/kill — over the collected evidence. agent-db/agent-log return clean answers; codebase-memory MCP returns structured data. All reasoning stays here.
- **Fan out WIDE — many independent fetches in parallel.** Independent CLI calls and subagent spawns run concurrently (wall-clock ≈ slowest, not the sum), so issue N independent fetches in ONE batch of tool_uses — never serialize. For CLI/MCP: batch multiple `Bash("agent-db ...")`, `Bash("agent-log ...")`, and `mcp__codebase-memory-mcp__*` calls in one message. For haiku-bash subagents: spawn ONE per bounded objective, **≤8 tool calls** each. A single 28-call open-ended agent is the anti-pattern — that's where drift and cost grow super-linearly. Split into narrow fetchers. Never resume a finished subagent to "save a spawn" — re-hydrating its fat transcript costs MORE than a fresh narrow spawn (measured).
- **Output contract on every subagent spawn.** End each subagent prompt with: "Return ONLY `file:line` + verbatim quotes + numbers. No assessment, no narration, no 'EUREKA'." This keeps prose out of main context — you supply the judgment, they supply the facts. (CLI tools — agent-db, agent-log — already return structured answers; no output contract needed.)
- **Evidence or it didn't happen.** Every claim needs a citation: `file:line`, a
  metric/row count, or an exact quoted log line. Unsure → say "belum yakin" + what to check.
- **Quote errors exactly.** Never paraphrase an error message or stack frame.
- **One hypothesis at a time** (default flow). State it, test it, confirm or kill it.
  Don't list five guesses — pursue the strongest, fall back only if killed. The sole
  exception is Deep mode (below), which is explicitly multi-hypothesis.

## Execution model — flat main-agent flow

This skill owns every phase directly. Keep the heavy work cheap by fanning out direct CLI/MCP calls and narrow `haiku-bash` subagents in parallel, then reason over their returns here. Do not insert an intermediate orchestrator. If a blocker needs user input, ask from this skill's main loop and continue after the answer; no nested respawn/handoff layer exists.

## Phase 0 — Recall (cheap, do first)

**Resume check first.** If `project-docs/incidents/.<slug>-scratch.md` already exists,
a prior run was cut off mid-investigation — read it and resume from the last recorded
phase (signature table / label / hypothesis). Do NOT re-gather from zero.

Before gathering anything new, check if this happened before:
- **`Read` `project-docs/incidents/INDEX.md` (L1) FIRST directly in main** (bounded, ~200 lines max) — one line per past incident, grouped by service. Scan for matching service/subsystem+symptom; if found, `Read` the matching incident `.md` (L2, bounded) for full RCA, then drill to code (L3). A prior RCA often means: do NOT re-investigate from scratch. (No INDEX.md → `Read` a directory listing via Bash `ls project-docs/incidents/` directly.)
- `ctx_search(sort: "timeline", queries: ["<symptom keywords>", "<service name> error"])` — prior incidents, decisions, fixes from session memory.
- Skim `MEMORY.md` index for related incident notes.

If a prior fix matches the symptom, verify it still applies before re-investigating from scratch.

## Phase 1 — Triage (parallel gather, then classify)

> Run this phase directly in this skill. Fan out the haiku below, collect their raw
> citations, then make the classification call here. If the target is ambiguous, stop
> and ask the user instead of guessing.

Pin down: **which service, what symptom, when did it start, blast radius.**

Fan out in ONE batch (parallel tool calls):
- `Bash("agent-log '...'")` — **enumerate ALL distinct error signatures** in the incident window, not just the loudest/topmost one. For each: first-occurrence timestamp + verbatim signature + rough count. The noisiest error often masks a quieter independent one that surfaces only after you fix the loud one (the "fix → new error" trap, cause #3). List every distinct signature now so multi-cause incidents are caught up front, not one fix later.
- `haiku-bash` subagent — service/container status (up? restarting? OOMKilled? exit code?).
- `Bash("agent-db '...'")` (only if symptom smells like data) — quick sanity counts on the relevant table(s) around the affected time window.

**Triage the signature list before classifying.** If ≥2 distinct signatures with
independent first-occurrence times / unrelated stack frames coexist in the window,
flag **multi-cause** — they are likely separate root causes, not one. Pursue the
dominant one through Phase 2, but record the others as open fronts; do NOT assume one
fix clears all. (This is what later shows up as "fixed it, but a new error appeared" —
it was already in the logs at minute zero.)

Then **classify the incident — pick exactly ONE label before continuing. Do not
proceed to Phase 2 without a label.**

- **RUNTIME** — panic, crash, error spike, latency, OOM, restart loop → go to Phase 2A.
- **DATA** — wrong/missing rows, backfill/sync mismatch, parity divergence, count
  drift → go to Phase 2B.
- **MIXED** — symptom has both a runtime fault AND a data effect → do 2A first, then
  2B (the runtime error usually *causes* the data anomaly).
- **HEALTHY** — no error, metrics normal (services up, data fresh, counts in range) →
  **STOP. Do not enter Phase 2.** Report "sehat" with the proving evidence (status +
  freshness + counts). Investigating a non-incident chases a ghost.

<examples>
  <example>
    <symptom>report-service panic + restart loop every ~2 min, no row changes reported</symptom>
    <label>RUNTIME</label>
    <route>Phase 2A</route>
  </example>
  <example>
    <symptom>source_mirror row count < source for one cs_phone; no crash in logs</symptom>
    <label>DATA</label>
    <route>Phase 2B</route>
  </example>
  <example>
    <symptom>sync-service OOMKilled at 03:00, after restart backfill stopped writing rows for that window</symptom>
    <label>MIXED</label>
    <route>Phase 2A (the OOM) then 2B (the missing rows)</route>
  </example>
</examples>

## Phase 2A — Runtime root cause

> Run this phase directly in this skill. Own the trace+correlate loop end-to-end here.

1. From the log, extract the exact error string + top stack frame / file:line.
2. Map error → code via codebase-memory MCP directly: `mcp__codebase-memory-mcp__search_code("<error string>")` or `mcp__codebase-memory-mcp__search_graph` to find the function. Always pass project `www-wwwroot-gass-be`.
3. Trace both directions in one batch: `mcp__codebase-memory-mcp__trace_path(function, mode=calls, direction=both, risk_labels=true)` + `mcp__codebase-memory-mcp__get_code_snippet` of the implicated lines + any confirm-facts you need (e.g. "is there a handler/recovery path for the error case?"). Inbound = trigger path; outbound = code-level blast radius. `mode=cross_service` if it crosses a service boundary.
4. Reason over the returned facts: what input/state makes this path fail? State the hypothesis.
5. Confirm from facts in hand. Do failing requests match the hypothesized condition
   (timing, payload, config, deploy)? Correlate first-occurrence with
   `detect_changes(since="<last deploy ref/date>")`. Spawn a new agent only for a fact
   you don't have yet.

## Phase 2B — Data anomaly root cause

> Run this phase directly in this skill. Own the quantify+locate+map loop here.

1. Quantify precisely with `Bash("agent-db '...'")`: expected vs actual counts, the exact rows/keys that diverge, the time window.
2. Locate the divergence across the pipeline: source vs mirror vs target. Which hop
   dropped/duplicated/mangled the data? Compare row counts at each stage.
3. Map to the code in one batch: `mcp__codebase-memory-mcp__trace_path(<writer/loader fn>, mode=data_flow, direction=both)` — data_flow shows the arg expression at each hop, so you see where the value is mutated upstream AND where it propagates downstream. `mode=cross_service` when data crosses services (sync → mirror → target). In the same batch fetch the confirm-facts too via `mcp__codebase-memory-mcp__get_code_snippet` (e.g. "does a sweeper/recovery path exist? quote the exact WHERE clause").
4. Confirm from facts in hand: reproduce the bad transform logically against a known-bad
   key. Tie the anomaly to a specific code path, config, or backfill run (correlate with
   `detect_changes` if a deploy is suspected). Spawn a new agent only for a missing fact.

## Deep mode — competing hypotheses (opt-in)

Branch here from Phase 2 when the root cause is unclear and the flat flow has stalled
(multiple plausible causes, no single error string, cross-layer symptom), or when the
user says "investigate deep" / "competing hypotheses". Otherwise skip to Phase 3 — the
default single-hypothesis flow handles ~90% of incidents.

How deep mode runs (parallel fan-out, stays flat):
1. Enumerate 2-4 competing hypotheses (e.g. "config regression from last deploy",
   "data-shape change upstream", "resource exhaustion", "race in writer").
2. For each hypothesis, issue the evidence-gathering calls in one parallel batch: `Bash("agent-log '...'")` for logs, `Bash("agent-db '...'")` for DB, `mcp__codebase-memory-mcp__*` for code — plus `haiku-bash` for any shell work. Each hypothesis gathers evidence for its theory AND notes what would disprove it.
3. Collect all reports, play them against each other adversarially — which
   hypothesis survives the evidence, which are ruled out and why.
4. Fall through to Phase 3. The report (Phase 4) must note which hypotheses were
   tested and why the losers were ruled out.

## Phase 3 — Confirm root cause

> Run this disprove pass directly in this skill over the raw citations you already
> gathered. If a needed fact is missing, make a fresh narrow call — `Bash("agent-db/agent-log '...'")` or `mcp__codebase-memory-mcp__*` for data/code, `haiku-bash` for shell work. Do not trust an unbacked claim.

Before locking the root cause, answer two mandatory questions — they catch the two ways
RCA fails (stopping at a symptom, and confirmation bias):

1. **Symptom or real cause?** Is this the proximate mechanism or the underlying cause?
   Ask "why does this condition exist?" until the answer is something that, if fixed,
   stops the whole class from recurring — not just this instance. If your suggested fix
   only masks the symptom, name the deeper cause too (e.g. "missing sweeper" is
   proximate; "the two-step process can be interrupted mid-way" is the root).

   **Pre-fix next-layer check (catches the symptom-layer "fix → new error" trap, cause
   #1).** Before locking the fix, simulate it: "if this fix lands, is the condition
   *behind* it already healthy?" Check that backing condition NOW, with evidence, not
   after deploy. Example: raising a backfill timeout → first verify the connection pool
   has headroom *right now*; if the pool is already maxed, the timeout was a symptom and
   "pool exhausted" will surface the moment you fix it. If the next layer is already
   unhealthy, you stopped the why-chain too early — keep going. Stop only when the layer
   behind the fix is provably sound.
2. **Try to disprove it.** If your hypothesis were true, what evidence SHOULD exist that
   you haven't checked yet? Go check it. Is there an alternative explanation for the same
   evidence? If you can't find disproving evidence after looking, confidence is earned —
   otherwise say "belum yakin" and what's missing.

3. **Blast-radius of the FIX, not just the bug (reduces regression, cause #2).** Before
   suggesting the change, run impact analysis on the code the fix will touch:
   `mcp__codebase-memory-mcp__trace_path(<fn to be changed>, mode=calls, direction=inbound)` to see every caller,
   and check whether the fix mutates shared state, a config used elsewhere, or a function
   on other hot paths. Name any caller/path that could break. This cannot rule out
   regression fully — runtime effects (timing, load, data-shape) only surface after the
   fix runs — but it catches the statically-visible breakage before deploy. If the fix
   touches shared state on multiple paths, say so in the report's Suggested fix section
   and flag what to watch after rollout.

   **Retain the edges for the report's Code map.** The caller/callee/contract edges you
   just traced here (and in Phase 2) are the graph `/fix-plan` would otherwise re-trace
   from zero. Keep them — Phase 4 records them as `## Code map` (`file:line` + name only).

Then state the root cause in one sentence, backed by:
- **Evidence**: `file:line` of the bug, the exact log line(s), the numbers.
- **Mechanism**: why this code/config produces this symptom.
- **Trigger**: what started it (deploy, data shape, load, config) + first-seen time.
- **Blast radius**: what/who is affected, how many rows/requests (use the downstream
  trace + risk_labels from Phase 2).

If evidence is circumstantial, say so — "belum yakin, butuh X to confirm" beats fake confidence.

## Phase 4 — Write incident report

Write to `project-docs/incidents/YYYY-MM-DD-<slug>.md` (use today's date from
context; create dir if absent). Structure:

```markdown
# Incident: <short title>

- **Date**: <date>  **Service**: <svc>  **Severity**: <high/med/low>
  - high = service down or data loss/corruption; med = degraded/partial; low = cosmetic/no user impact
- **Status**: root cause confirmed | suspected | unresolved
  - lifecycle: starts here (diagnosed). When a fix ships + verifies, `/fixer` updates this to
    `FIXED (applied <date>, commit <hash>)`. Use `ARCHIVED (<reason>)` for benign/superseded/stale.
    The index (L1) groups OPEN vs FIXED/ARCHIVED so closed incidents stop cluttering investigation.

## Symptom
<what was observed — quote errors/numbers exactly>

## Timeline
- <first-seen ts> — <event>
- ...

## Root cause
<one-paragraph mechanism, with file:line + numbers>
<if the proximate cause differs from the deeper root, name both — proximate (what
directly produced the symptom) and root (what made that condition possible)>

## Evidence
- `path/to/file.go:NN` — <what it shows>
- log: `<exact quoted line>`
- db: <expected vs actual numbers>

## Blast radius
<affected rows / requests / users — upstream trigger + downstream impact>

## Code map (traced — LEAD for /fix-plan, re-verify before use)
<!-- Already traced in Phase 2 (bug region) + Phase 3 (fix blast-radius). Record it here so
fix-plan starts from the known graph instead of re-tracing from zero. `file:line` + name ONLY
— never source snippets, never the full graph (that bloats the report). This is a LEAD, not
ground truth: code can move before the fix runs, and the fix may touch functions not traced
here. fix-plan re-verifies (still-live + the fix delta) before relying on it. -->
- target fn(s): `path/file.go:NN funcName()` — code the suggested fix will change
- callers (inbound): `path/a.go:NN caller()`, `path/b.go:NN caller2()`
- calls down (outbound): `pkg.Thing() path/c.go:NN`
- shared contract: `TypeOrSig` (path/d.go:NN) — what breaks if the signature changes
- cross-service: <route/channel if the fix crosses a boundary, else "none">
<!-- If the suggested fix's target isn't pinned yet, record the bug-region graph instead and
say so on the first line — a bug-region map is still a head start, just label it honestly. -->

## Suggested fix
<concrete change — NOT applied yet, leave for approval>

## Open questions
<anything unconfirmed>
```

After the report is written, **regenerate the incident index (L1)** so the new RCA is
discoverable next session:
`python3 ~/globalclaude/scripts/gen_incident_index.py <project-dir>`
(use the project root, e.g. the repo cwd). Then **delete the scratch handoff file**
(`project-docs/incidents/.<slug>-scratch.md`) — its content is now folded into the
incident report; leaving it is an orphan. If the investigation ended BLOCKED/unresolved,
keep the scratch file and note its path in `## Open questions` so a respawn can resume.

## Phase 5 — Chat summary

Reply in chat (Bahasa Indonesia, terse): root cause in 1-2 lines, the key
evidence, blast radius, report file path, and the suggested fix. Then **stop** —
do not apply the fix unless the user approves.

End with the handoff bridge: **"Mau rencanakan fix-nya? `/fix-plan <report-path>`."**
The incident file (with `## Root cause` + `## Suggested fix`) is the handoff —
`/fix-plan` reads it as input and won't re-investigate.
