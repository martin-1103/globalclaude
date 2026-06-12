#!/usr/bin/env bash
# no-subagent-ctx-recall.sh — PreToolUse(ctx_search|ctx_batch_execute)
#
# Goal: stop SUBAGENTS from using context-mode recall/gather tools.
#
# context-mode injects "use ctx_* as primary research tool" into EVERY session —
# main AND subagents (sessionstart.mjs:166, no gating; core/routing.mjs:817-841
# injects routing into every spawned Agent's prompt). For the MAIN agent that is
# the point: its context is resent every turn, so keeping raw bytes out via the
# sandbox/index saves multiplied tokens. But a SUBAGENT's context is already
# disposable — discarded the moment it returns. Making it pay ctx_search (measured
# 4-6 MINUTES per call on the 996MB FTS5 index, 2026-06-12) buys nothing and wrecks
# latency. Raw `git diff` / `grep` / `Bash` answer the same review question in <1s.
#
# Rule (mirror of no-nested-gather-skill.sh):
#   ctx_search / ctx_batch_execute from MAIN     → ALLOW (context worth protecting)
#   ctx_search / ctx_batch_execute from SUBAGENT → DENY  (disposable; query direct)
#
# Detection (verified live 2026-06-12 via payload probe): subagent payloads carry
#   `agent_id`; main-thread payloads do not. agent_id present → subagent → DENY.
#   Fail-open if CC ever renames the field (absent → treated as main → ALLOW):
#   safe but silent — re-probe the field if subagents start slipping through.
#
# Override: ALLOW_SUBAGENT_CTX=1 bypasses entirely. Fail-open on parse error.

set -euo pipefail

INPUT=$(cat)

[[ "${ALLOW_SUBAGENT_CTX:-}" == "1" ]] && exit 0

TOOL=$(printf '%s' "$INPUT" | jq -r '.tool_name // ""' 2>/dev/null || echo "")
case "$TOOL" in
  *ctx_search|*ctx_batch_execute) ;;
  *) exit 0 ;;
esac

AGENT_ID=$(printf '%s' "$INPUT" | jq -r '.agent_id // ""' 2>/dev/null || echo "")

if [[ -n "$AGENT_ID" ]]; then
  jq -nc --arg t "$TOOL" '{hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"deny",permissionDecisionReason:("⛔ " + $t + " blocked for subagents. You ARE a subagent — your context is disposable, and this tool runs 4-6 min on the bloated FTS5 index. Use raw Bash/Read/Grep (git diff, grep, cat) — sub-second, same answer.")}}'
  exit 0
fi

# caller is main → allow (context worth protecting)
exit 0
