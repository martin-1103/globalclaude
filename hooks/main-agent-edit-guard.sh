#!/usr/bin/env bash
# main-agent-edit-guard.sh — PreToolUse(Edit|Write)
#
# Goal: force bounded code edits to the sonnet-editor subagent (cheap, isolated
# context) instead of the main Opus thread (expensive, accumulates in context).
#
# ENFORCE MODE: denies Edit/Write to code files from the main thread.
#
# Detection (proven in main-agent-write-guard.sh, verified live 2026-06-10):
#   CC flushes tool_use_id into <transcript>/subagents/agent-<id>.jsonl
#   ~100ms after PreToolUse fires. Poll up to ~200ms:
#     found   → subagent owns this edit → ALLOW
#     timeout → main agent              → DENY
#   No subagents dir / stale (>=3s) → main → DENY.
#
# Allowed even from main (escape hatch): ~/.claude/, *.md, */.claude/*, */.planning/*.
# Override: set ALLOW_MAIN_EDIT=1 in env to bypass entirely (emergency / intentional).
# Fail-open: parse error / missing fields → ALLOW.

set -euo pipefail

INPUT=$(cat)

logline() { :; }   # logging disabled

deny() {
  jq -nc '{hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"deny",permissionDecisionReason:"⛔ Code edit from main thread blocked. Spawn Agent(subagent_type=\"sonnet-editor\") to apply it; for many files, give each editor a disjoint file slice. (Plan/doc artifacts under project-docs/ are exempt — if you hit this on one, the path check is wrong.)"}}'
  exit 0
}

# Global override
[[ "${ALLOW_MAIN_EDIT:-}" == "1" ]] && { logline "ALLOW-OVERRIDE" "ALLOW_MAIN_EDIT=1"; exit 0; }

TOOL=$(printf '%s' "$INPUT" | jq -r '.tool_name // ""' 2>/dev/null || echo "")
[[ "$TOOL" == "Edit" || "$TOOL" == "Write" ]] || exit 0

FILE=$(printf '%s' "$INPUT" | jq -r '.tool_input.file_path // ""' 2>/dev/null || echo "")
[[ -n "$FILE" ]] || { logline "ALLOW-FAILOPEN" "no file_path"; exit 0; }

# Escape hatches — allowed from main thread.
# project-docs/ holds plan/incident/scratch artifacts (incl. *.tasks.json) that the
# main agent and plan-orchestrator OWN and write directly — they are coordination
# state, not product code. The gate guards CODE edits; these must never be blocked.
CLAUDE_HOME="${CLAUDE_CONFIG_DIR:-/root/.claude}"
if [[ "$FILE" == "$CLAUDE_HOME"* || "$FILE" == *.md || "$FILE" == */.claude/* || "$FILE" == */.planning/* || "$FILE" == */project-docs/* ]]; then
  logline "ALLOW-EXEMPT" "$TOOL $FILE"
  exit 0
fi

TP=$(printf '%s' "$INPUT" | jq -r '.transcript_path // ""' 2>/dev/null || echo "")
TID=$(printf '%s' "$INPUT" | jq -r '.tool_use_id // ""' 2>/dev/null || echo "")
if [[ -z "$TP" || -z "$TID" ]]; then
  logline "ALLOW-FAILOPEN" "$TOOL $FILE (no transcript/tid)"
  exit 0
fi

SUBDIR="${TP%.jsonl}/subagents"

[[ -d "$SUBDIR" ]] || deny "$FILE"

NEWEST=$(ls -t "$SUBDIR"/*.jsonl 2>/dev/null | head -1 || true)
[[ -n "$NEWEST" ]] || deny "$FILE"

NOW=$(date +%s 2>/dev/null || echo 0)
MTIME=$(stat -c %Y "$NEWEST" 2>/dev/null || echo 0)
AGE=$(( NOW - MTIME ))
(( AGE < 3 )) || deny "$FILE"

# Active subagent — poll for tool_use_id (~200ms)
MF=""
for _ in $(seq 1 8); do
  MF=$(grep -lF -- "$TID" "$SUBDIR"/*.jsonl 2>/dev/null | head -1 || true)
  [[ -n "$MF" ]] && break
  sleep 0.025
done

if [[ -n "$MF" ]]; then
  logline "ALLOW-SUBAGENT" "$TOOL $FILE (tid in $(basename "$MF"))"
  exit 0
fi

deny "$FILE"
