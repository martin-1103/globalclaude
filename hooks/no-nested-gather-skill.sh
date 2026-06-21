#!/usr/bin/env bash
# no-nested-gather-skill.sh — PreToolUse(Skill)
#
# Goal: stop DOUBLE-SPAWN. The haiku-* gather skills are `context: fork` — every
# call spawns a fresh agent (~20s boot: load prompt + `!cat *-context.md` +
# haiku cold start). That cost is justified ONCE, from the main thread, to keep
# main context clean. But when a SUBAGENT calls them, the caller's context is
# already disposable — forking again buys nothing and just pays the 20s twice.
#
# Rule (mirror image of main-agent-gather-guard.sh):
#   haiku-* gather skill from MAIN      → ALLOW (1 fork, by design)
#   haiku-* gather skill from SUBAGENT  → DENY  (already disposable; query direct)
#
# Detection (verified live 2026-06-12 via payload probe): subagent payloads
#   carry `agent_id`; main-thread payloads do not. Deterministic — replaces
#   the old transcript-poll heuristic (200ms poll + 3s mtime gate).
#   agent_id present → caller IS a subagent → nested → DENY.
#   agent_id absent  → caller is main → ALLOW (1 fork, by design).
#
# Override: ALLOW_NESTED_SKILL=1 bypasses entirely.
# Fail-open: parse error / non-target skill → ALLOW.

set -euo pipefail

INPUT=$(cat)

# Global override
[[ "${ALLOW_NESTED_SKILL:-}" == "1" ]] && exit 0

TOOL=$(printf '%s' "$INPUT" | jq -r '.tool_name // ""' 2>/dev/null || echo "")
[[ "$TOOL" == "Skill" ]] || exit 0

SKILL=$(printf '%s' "$INPUT" | jq -r '.tool_input.skill // ""' 2>/dev/null || echo "")
[[ -n "$SKILL" ]] || exit 0

# Only guard the fork-based gather skills. Others (investigate, fix-plan, etc.)
# are orchestrators meant to run from anywhere — leave them alone.
case "$SKILL" in
  haiku-bash) ;;
  *) exit 0 ;;
esac

# ── Is the caller a subagent? agent_id present only on subagent payloads. ─────
AGENT_ID=$(printf '%s' "$INPUT" | jq -r '.agent_id // ""' 2>/dev/null || echo "")

if [[ -n "$AGENT_ID" ]]; then
  jq -nc --arg s "$SKILL" '{hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"deny",permissionDecisionReason:("⛔ nested " + $s + " blocked. You ARE a subagent — your context is already disposable, forking again wastes ~20s. Do the work directly with the tools you have (Bash: docker exec -i <container> mysql/clickhouse-client ...).")}}'
  exit 0
fi

# caller is main → allow (1 fork by design)
exit 0
