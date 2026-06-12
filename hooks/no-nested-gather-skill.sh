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
# Detection: identical to gather-guard. CC flushes tool_use_id into
#   <transcript>/subagents/agent-<id>.jsonl ~100ms after PreToolUse. If a
#   subagent file owns this TID, the caller IS a subagent → nested → DENY.
#   No subagents dir / not owned / stale → caller is main → ALLOW.
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
  haiku-db|haiku-logs|haiku-explorer|haiku-bash|haiku-codebase-memory) ;;
  *) exit 0 ;;
esac

# ── Is the caller a subagent? (own TID flushed into subagents/*.jsonl) ────────
TP=$(printf '%s' "$INPUT" | jq -r '.transcript_path // ""' 2>/dev/null || echo "")
TID=$(printf '%s' "$INPUT" | jq -r '.tool_use_id // ""' 2>/dev/null || echo "")
# fail-open: can't detect → allow (never wedge a real flow on parse gaps)
[[ -n "$TP" && -n "$TID" ]] || exit 0

SUBDIR="${TP%.jsonl}/subagents"
# no subagents dir → caller is main → allow
[[ -d "$SUBDIR" ]] || exit 0

NEWEST=$(ls -t "$SUBDIR"/*.jsonl 2>/dev/null | head -1 || true)
[[ -n "$NEWEST" ]] || exit 0      # empty dir → main → allow

NOW=$(date +%s 2>/dev/null || echo 0)
MTIME=$(stat -c %Y "$NEWEST" 2>/dev/null || echo 0)
(( NOW - MTIME < 3 )) || exit 0   # stale subagents → main → allow

MF=""
for _ in $(seq 1 8); do
  MF=$(grep -lF -- "$TID" "$SUBDIR"/*.jsonl 2>/dev/null | head -1 || true)
  [[ -n "$MF" ]] && break
  sleep 0.025
done

# TID owned by a subagent → nested call → DENY
if [[ -n "$MF" ]]; then
  jq -nc --arg s "$SKILL" '{hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"deny",permissionDecisionReason:("⛔ nested " + $s + " blocked. You ARE a subagent — your context is already disposable, forking again wastes ~20s. Do the work directly with the tools you have (Bash: docker exec -i <container> mysql/clickhouse-client ...).")}}'
  exit 0
fi

# not owned by any subagent → caller is main → allow (1 fork by design)
exit 0
