---
name: debug-session
description: Debug existing Claude Code session/chat-history transcripts to find why things broke — subagent stuck or never spawned, skill didn't trigger, tool/hook errors. Scans ~/.claude/projects/*.jsonl transcripts and subagent transcripts, flags only real anomalies (benign hook-redirects filtered out). Triggers on "debug session", "subagent stuck", "subagent ga jalan/spawn", "skill ga ke-trigger", "kenapa subagent macet", "cek session history", "analisa transcript", "/debug-session".
---

# Debug Session — transcript anomaly scanner

Inspect past Claude Code sessions to diagnose harness-level failures that don't
show up in app logs: subagents that hang or never spawn, skills that didn't fire,
tool/hook errors.

## Where the data lives

```
~/.claude/projects/<slug>/<sessionId>.jsonl                 main transcript
~/.claude/projects/<slug>/<sessionId>/subagents/agent-*.jsonl   subagent transcripts
```

`<slug>` = cwd with `/` and `.` replaced by `-` (e.g. `/www/wwwroot/gass/be` →
`-www-wwwroot-gass-be`). Each `.jsonl` line is one record (`type`: user, assistant,
system, mode, attachment, …). Tool calls live inside `message.content[]` as
`tool_use` / `tool_result`. Subagents get their own transcript files; the main
transcript has NO `isSidechain` lines.

## How anomalies are detected

| Symptom | Signal |
|---|---|
| Subagent **stuck** | `Agent`/`Task` `tool_use` with no matching `tool_result` (unpaired id), OR subagent transcript present but no final `stopReason` / non-assistant last record |
| Subagent **spawn fail** | `Agent` calls in main but `subagents/` dir empty/missing, OR `Agent` tool_result `is_error` (e.g. `Team "x" does not exist`) |
| **Skill didn't trigger** | `Skill` tool_result `is_error`, or `hookErrors` on dispatch. (Empty `skill_calls` alone = informational, not flagged.) |
| **Tool/hook errors** | any non-benign `tool_result.is_error` + `hookErrors` |

**Benign noise is filtered** (not flagged): `main gather blocked` (ALLOW_MAIN_GATHER
hook), opus-edit `DILARANG` redirect, WebFetch→ctx redirect, "File has not been read
yet", "No such tool available", user rejections. These are the harness steering and
get retried — counted as `benign-errs=N` but never raise an anomaly.

## Usage

Script: `analyze.py` (in this skill dir). Always run via `python3`.

```bash
# scan all sessions in CURRENT project, report only anomalies + summary
python3 ~/.claude/skills/debug-session/analyze.py

# scan a specific project slug
python3 ~/.claude/skills/debug-session/analyze.py --project -www-wwwroot-gass-be

# deep-dive one session (by id or file path)
python3 ~/.claude/skills/debug-session/analyze.py --session b56f00c9-4b4c-462d-824f-d0cbea9f67c0

# dump a subagent's dispatch prompt + final output (or reveal it's stuck)
python3 ~/.claude/skills/debug-session/analyze.py --dump \
  ~/.claude/projects/-www-wwwroot-gass-be/<sessionId>/subagents/agent-<id>.jsonl

# every project, machine-readable
python3 ~/.claude/skills/debug-session/analyze.py --all-projects --json
```

Flags: `--verbose` (show details on clean sessions too), `--json` (raw report list).

## Workflow

1. Run the no-arg scan → see which sessions carry real anomalies.
2. For a flagged session, the footer prints the exact `--dump` command for its
   first stuck subagent. Run it to read what the subagent was told and whether it
   produced a final answer.
3. `agents=N/Mfiles` mismatch is the spawn check: N Agent calls vs M transcript
   files. `8/8` healthy; `1/0` = spawn never wrote a transcript.

## Notes / limits

- Uses only the stdlib — no deps. Safe to run read-only against live transcripts.
- "Stuck" via unpaired tool_use also matches a session interrupted mid-flight or
  still running. Cross-check the timestamp range before concluding it's a bug.
- A subagent file with 0 corresponding `Agent` calls (e.g. `0/1files`) usually
  means it was dispatched by a skill/Team flow, not a direct Agent tool_use —
  not a fault.
