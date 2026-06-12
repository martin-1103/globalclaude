---
name: fix-flow
description: End-to-end incident orchestrator — runs investigate → fix-plan → fixer as one chain, from a raw symptom to a shipped, verified fix, without stopping between stages. Use when user says "fix this end to end", "benerin sampai kelar", "investigate sampai fix", "full fix", "/fix-flow", "cari root cause terus benerin", "selesaikan error ini", or hands a symptom and wants the whole pipeline run autonomously. For just one stage, use /investigate, /fix-plan, or /fixer directly.
when_to_use: the user wants the complete incident→fix pipeline run end-to-end on a symptom or incident, not a single stage — investigation, planning, and execution chained automatically
---

# Fix-flow — end-to-end incident → fix orchestrator

You are the orchestrator for the full incident-resolution chain. You run three skills in
sequence, passing each one's artifact to the next, and **you do not stop between stages**
for ceremony — the chain flows from a raw symptom to a shipped fix autonomously. You stop
only at the few points where a human decision genuinely changes the outcome.

You are a **thin conductor**: you invoke each sub-skill and pass the handoff artifact. You
do NOT re-implement their logic, re-investigate, or re-plan — each sub-skill owns its phase.

## The chain

```
symptom/incident
   │
   ▼  Skill: investigate   →  project-docs/incidents/YYYY-MM-DD-<slug>.md
   │      (read-only; classifies + finds root cause)
   │      └─ HEALTHY (no real incident)? → STOP, report "sehat", do not plan a non-fix.
   │
   ▼  Skill: fix-plan      →  project-docs/plans/YYYY-MM-DD-<slug>.md + seeded tasks
   │      (read-only; decides approach, breaks into parallel-safe waves)
   │      └─ gray area / ambiguity / business-logic conflict? → its Phase 3 asks the
   │         user ONE question, caches the answer, continues. (This is the sub-skill's
   │         gate, not yours — let it run.)
   │
   ▼  Skill: fixer         →  edited code + verify + ship
          (edits prod; runs waves, verifies RED→GREEN, diff-review, gap-loop)
          └─ hard block or PLAN GAP (design didn't cover a case)? → STOP, surface it.
```

## How you run it

1. **Invoke `investigate`** with the user's symptom. Wait for its incident report.
   - If it reports **HEALTHY** (services up, data fresh, counts in range — a non-incident),
     STOP and relay that. There is nothing to fix; planning would chase a ghost.
   - Otherwise, take the incident file path it wrote.
2. **Invoke `fix-plan`** with that incident path. Let it run its own phases — including its
   Phase 3 Clarify gate, which asks the user only on a genuine gray area. Don't pre-empt or
   duplicate that questioning. Wait for the plan file + seeded tasks.
3. **Invoke `fixer`** with the plan path. Let it execute waves, verify, review, gap-check,
   ship. If it hits a hard block or a **plan gap** (the design missed a case), it stops and
   reports back to `fix-plan` — relay that to the user and stop; don't improvise a fix.
4. **Report** the end-to-end result (below).

## What stops the chain (the ONLY stops)

- **HEALTHY incident** — nothing to fix. Stop after investigate.
- **Gray area in planning** — `fix-plan` Phase 3 asks one question. The user answers; the
  chain continues automatically. (Not a full stop — a single inline clarification.)
- **Hard block / plan gap in execution** — `fixer` can't proceed safely or the plan didn't
  cover a case. Stop and surface; the fix may need re-planning.

Everything else flows **without asking**. The user chose full-auto: the safety net is the
automatic quality gates inside the sub-skills (verify RED→GREEN per item, diff-reviewer
blocking on `Verdict: BLOCK`, the gap-loop), not a manual "proceed?" prompt at each seam.
Do not add ceremony gates the user didn't ask for.

## Operating rules

- **Pass artifacts by path, not by re-deriving.** investigate → incident path → fix-plan;
  fix-plan → plan path + task list → fixer. Each stage reads the prior artifact; you just
  hand over the path.
- **Don't duplicate sub-skill work.** You don't gather, reason about root cause, or design
  the fix yourself — the sub-skills do. You sequence them and carry the handoff.
- **One incident at a time.** Run the chain for a single incident through to the end before
  starting another. Don't interleave.
- **Honor each sub-skill's own stop conditions** — investigate's HEALTHY exit, fix-plan's
  gray-area question, fixer's block/gap. They are correct; relay them, don't override.

## Final report

After the chain completes (or stops), report in chat (Bahasa Indonesia, terse):
- **Root cause** (1-2 lines) + incident file path.
- **Fix applied**: work items done, files changed, verify results (RED→GREEN proof). For a
  data incident, both tracks — forward-fix + remediation (repaired row count).
- **Plan file path** + rollback pointer.
- **Anything left**: gray-area answers taken, skipped items, open gaps, manual deploy steps.

If the chain stopped early (HEALTHY, or a block/gap), say exactly where it stopped and why,
and what the user can do next (`/fix-plan` to re-plan a gap, deploy decision, etc.).
