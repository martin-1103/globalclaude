#!/usr/bin/env bash
# main-agent-edit-guard.sh — PreToolUse(Edit|Write)
#
# Goal: force bounded code edits to the sonnet-editor subagent (cheap, isolated
# context) instead of the main thread (expensive, accumulates in context).
#
# ENFORCE MODE: denies Edit/Write to code files from the main thread.
#
# Detection (verified live 2026-06-12 via payload probe):
#   PreToolUse payloads from a SUBAGENT carry `agent_id` + `agent_type`;
#   payloads from the MAIN thread do not. Deterministic, zero-latency —
#   replaces the old transcript-poll heuristic (poll 200ms + mtime 3s),
#   which mis-classified working subagents as main 343x across sessions
#   and triggered editor-spawn cascades.
#
# Allowed even from main (escape hatch): ~/.claude/, *.md, */.claude/*,
# */.planning/*, */project-docs/* (plan coordination artifacts, not code).
# Override: set ALLOW_MAIN_EDIT=1 in env to bypass entirely (human use).
# Fail-open: parse error / missing fields → ALLOW.

set -euo pipefail

INPUT=$(cat)

deny() {
  jq -nc '{hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"deny",permissionDecisionReason:"⛔ Code edit from main thread blocked. Spawn Agent(subagent_type=\"sonnet-editor\") to apply it; for many files, give each editor a disjoint file slice. If you are ALREADY a subagent seeing this, it is a guard false-positive — report it in your return, do NOT spawn another editor."}}'
  exit 0
}

# Global override (human escape hatch)
[[ "${ALLOW_MAIN_EDIT:-}" == "1" ]] && exit 0

TOOL=$(printf '%s' "$INPUT" | jq -r '.tool_name // ""' 2>/dev/null || echo "")
[[ "$TOOL" == "Edit" || "$TOOL" == "Write" ]] || exit 0

# Subagent caller → ALLOW. agent_id is only present when a subagent runs the tool.
AGENT_ID=$(printf '%s' "$INPUT" | jq -r '.agent_id // ""' 2>/dev/null || echo "")
[[ -n "$AGENT_ID" ]] && exit 0

FILE=$(printf '%s' "$INPUT" | jq -r '.tool_input.file_path // ""' 2>/dev/null || echo "")
[[ -n "$FILE" ]] || exit 0   # fail-open: no file_path

# Escape hatches — allowed from main thread.
# project-docs/ holds plan/incident/scratch artifacts (incl. *.tasks.json) that the
# main agent and plan-orchestrator OWN and write directly — they are coordination
# state, not product code. The gate guards CODE edits; these must never be blocked.
CLAUDE_HOME="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
if [[ "$FILE" == "$CLAUDE_HOME"* || "$FILE" == *.md || "$FILE" == */.claude/* || "$FILE" == */.planning/* || "$FILE" == */project-docs/* ]]; then
  exit 0
fi

deny
