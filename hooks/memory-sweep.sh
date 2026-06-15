#!/usr/bin/env bash
# memory-sweep.sh — SessionStart hook for auto-memory hygiene.
#
# Two-tier sweep:
#   MECHANICAL (auto-fixed here, free): orphan pointers in MEMORY.md whose target
#     file no longer exists are removed. A backup (MEMORY.md.bak) + append-only log
#     (.sweep.log) are written first so any wrong removal is recoverable.
#   SEMANTIC (flagged, not auto-done): orphan files without a pointer, index over the
#     line threshold (tiering), and entry count over the consolidation nudge. These need
#     the model to read + judge, so they are surfaced as context for THIS session.
#
# Emits stdout ONLY when something needs attention — silent on a clean memory dir.
set -uo pipefail

MEM="/root/.claude/projects/-root--claude/memory"
INDEX="$MEM/MEMORY.md"
LINE_THRESHOLD=120      # MEMORY.md is injected every session; split index past this
ENTRY_NUDGE=20          # soft consolidation nudge once this many memory files exist

[ -f "$INDEX" ] || exit 0

flags=()
removed=()

# --- 1. MECHANICAL: drop pointer lines whose .md target is gone ---
newlines=()
changed=0
while IFS= read -r ln || [ -n "$ln" ]; do
  tgt=$(printf '%s' "$ln" | grep -oE '\]\([^)]+\.md\)' | head -1 | sed -E 's/^\]\(//; s/\)$//')
  if [ -n "$tgt" ] && [ ! -f "$MEM/$tgt" ]; then
    removed+=("$tgt")
    changed=1
    continue
  fi
  newlines+=("$ln")
done < "$INDEX"

if [ "$changed" -eq 1 ]; then
  cp "$INDEX" "$INDEX.bak"
  printf '%s\n' "${newlines[@]}" > "$INDEX"
  ts=$(date '+%Y-%m-%d %H:%M')
  for r in "${removed[@]}"; do
    echo "$ts removed orphan pointer -> $r" >> "$MEM/.sweep.log"
  done
fi

# --- 2. SEMANTIC FLAG: memory files with no pointer in the index ---
orphan_files=()
for f in "$MEM"/*.md; do
  [ -e "$f" ] || continue
  base=$(basename "$f")
  [ "$base" = "MEMORY.md" ] && continue
  grep -qF "($base)" "$INDEX" || orphan_files+=("$base")
done

# --- 3. SEMANTIC FLAG: index size + entry count ---
lc=$(wc -l < "$INDEX" | tr -d ' ')
fc=$(ls -1 "$MEM"/*.md 2>/dev/null | grep -vc '/MEMORY.md$')

[ "$changed" -eq 1 ] && \
  flags+=("auto-removed ${#removed[@]} dead pointer(s): ${removed[*]} (recoverable: MEMORY.md.bak, .sweep.log)")
[ "${#orphan_files[@]}" -gt 0 ] && \
  flags+=("${#orphan_files[@]} memory file(s) with NO index pointer: ${orphan_files[*]} — add a MEMORY.md line or delete the file")
[ "$lc" -gt "$LINE_THRESHOLD" ] && \
  flags+=("MEMORY.md = $lc lines (> $LINE_THRESHOLD) — split into per-category sub-indexes (tiering) to stay under the inject limit")
[ "$fc" -gt "$ENTRY_NUDGE" ] && \
  flags+=("$fc memory files — run a consolidation pass: merge near-duplicates, drop stale facts (verify referenced code/flags still exist), resolve contradictions (newer wins)")

if [ "${#flags[@]}" -gt 0 ]; then
  echo "MEMORY HYGIENE — sweep flags (handle this session, then continue):"
  for fl in "${flags[@]}"; do echo "  - $fl"; done
fi
exit 0
