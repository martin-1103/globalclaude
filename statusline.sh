#!/usr/bin/env bash
# Claude Code statusline — 3 lines
# Line 1: model · path · branch · sid
# Line 2: context-window usage (when to /compact) · quota 5h/7d · per-call cache-hit%
# Line 3: Σ session tokens — cost (= fresh: input+cache_creation, the A/B compare metric)
#         · cached (cache_read, ~free) · out (output)
#
# Compression impact: run same task twice (/clear between), compare Σ cost.
# If cached drops on the compressed run, compression broke prompt cache.

input=$(cat)

model=$(printf '%s' "$input" | jq -r '.model.display_name // "?"')
dir=$(printf '%s' "$input" | jq -r '.workspace.current_dir // .cwd // "?"')
session=$(printf '%s' "$input" | jq -r '.session_id // ""')
transcript=$(printf '%s' "$input" | jq -r '.transcript_path // ""')

# shorten home to ~
dir=${dir/#$HOME/\~}

# git branch (fallback empty)
branch=$(git -C "$(printf '%s' "$input" | jq -r '.workspace.current_dir // .cwd')" rev-parse --abbrev-ref HEAD 2>/dev/null)

# context window usage (tells you when to /compact)
cwused=$(printf '%s' "$input" | jq -r '.context_window.total_input_tokens // 0')
cwmax=$(printf '%s' "$input" | jq -r '.context_window.context_window_size // 200000')
cwpct=$(printf '%s' "$input" | jq -r '.context_window.used_percentage // 0')

# quota (account-wide; absent until first API response on Pro/Max)
q5h=$(printf '%s' "$input" | jq -r '.rate_limits.five_hour.used_percentage  // empty')
q7d=$(printf '%s' "$input" | jq -r '.rate_limits.seven_day.used_percentage  // empty')

# per-call cache-hit (this call only) — drops live if cache breaks
tin=$(printf '%s' "$input" | jq -r '.context_window.current_usage.input_tokens                // 0')
tcc=$(printf '%s' "$input" | jq -r '.context_window.current_usage.cache_creation_input_tokens // 0')
tcr=$(printf '%s' "$input" | jq -r '.context_window.current_usage.cache_read_input_tokens     // 0')
ctot=$((tin + tcc + tcr))
hit=0
[ "$ctot" -gt 0 ] && hit=$((tcr * 100 / ctot))

# cumulative session totals from transcript (mtime-cached to avoid re-scan)
# cost = input + cache_creation (fresh, burns quota) ; cached = cache_read ; out = output
cost=0; cached=0; out=0
if [ -n "$transcript" ] && [ -f "$transcript" ]; then
  cfile="/tmp/ccstatus-cum-${session:-x}"
  mt=$(stat -c %Y "$transcript" 2>/dev/null || stat -f %m "$transcript" 2>/dev/null || echo 0)
  if [ -f "$cfile" ]; then IFS=' ' read -r cmt cost cached out < "$cfile"; else cmt=-1; fi
  if [ "$mt" != "$cmt" ]; then
    read -r cost cached out < <(jq -nr '
      reduce inputs as $l (
        {cost:0,cached:0,out:0};
        ($l.message.usage // {}) as $u |
        .cost   += (($u.input_tokens // 0) + ($u.cache_creation_input_tokens // 0)) |
        .cached += ($u.cache_read_input_tokens // 0) |
        .out    += ($u.output_tokens // 0)
      ) | "\(.cost) \(.cached) \(.out)"' "$transcript" 2>/dev/null)
    cost=${cost:-0}; cached=${cached:-0}; out=${out:-0}
    printf '%s %s %s %s' "$mt" "$cost" "$cached" "$out" > "$cfile" 2>/dev/null
  fi
fi

# format tokens as k
fmt() { awk -v n="$1" 'BEGIN{ if(n>=1000) printf "%.1fk", n/1000; else printf "%d", n }'; }
cwused_k=$(fmt "$cwused")
cwmax_k=$(fmt "$cwmax")
cost_k=$(fmt "$cost")
cached_k=$(fmt "$cached")
out_k=$(fmt "$out")

# colors
C_RESET=$'\033[0m'
C_DIM=$'\033[2m'
C_BOLD=$'\033[1m'
C_CYAN=$'\033[36m'
C_GREEN=$'\033[32m'
C_YELLOW=$'\033[33m'
C_RED=$'\033[31m'

# context bar color: green <70, yellow 70-84, red >=85 (compact zone)
cwp=${cwpct%.*}; cwp=${cwp:-0}
if   [ "$cwp" -ge 85 ]; then ctx_color=$C_RED
elif [ "$cwp" -ge 70 ]; then ctx_color=$C_YELLOW
else ctx_color=$C_GREEN; fi

# build 8-char bar
BARW=8
filled=$((cwp * BARW / 100))
[ "$filled" -gt "$BARW" ] && filled=$BARW
empty=$((BARW - filled))
bar=""
[ "$filled" -gt 0 ] && printf -v f "%${filled}s" && bar="${f// /▓}"
[ "$empty" -gt 0 ]  && printf -v e "%${empty}s"  && bar="${bar}${e// /░}"

# color a quota % : green<70 yellow<90 red
qcol() {
  local v=${1%.*}; v=${v:-0}
  if   [ "$v" -ge 90 ]; then printf '%s%.0f%%%s' "$C_RED" "$1" "$C_RESET"
  elif [ "$v" -ge 70 ]; then printf '%s%.0f%%%s' "$C_YELLOW" "$1" "$C_RESET"
  else printf '%s%.0f%%%s' "$C_GREEN" "$1" "$C_RESET"; fi
}

D="${C_DIM}·${C_RESET}"

line1="${C_CYAN}${model}${C_RESET} ${D} ${dir}"
[ -n "$branch" ] && line1="${line1} ${D} ${C_GREEN}⎇ ${branch}${C_RESET}"
[ -n "$session" ] && line1="${line1} ${D} ${C_DIM}${session}${C_RESET}"

# line2: context usage + quota + per-call hit
ctxseg="${ctx_color}ctx [${bar}] ${cwp}% ${cwused_k}/${cwmax_k}${C_RESET}"
q=""
[ -n "$q5h" ] && q="5h:$(qcol "$q5h")"
[ -n "$q7d" ] && q="${q:+$q }7d:$(qcol "$q7d")"
[ -z "$q" ] && q="${C_DIM}quota:--${C_RESET}"
line2="${ctxseg} ${D} ${q} ${D} hit:${hit}%"

# line3: cumulative — cost highlighted (compare metric)
line3="${C_DIM}Σ${C_RESET} ${C_BOLD}${C_YELLOW}cost ${cost_k}${C_RESET} ${D} ${C_GREEN}cached ${cached_k}${C_RESET} ${D} ${C_DIM}out ${out_k}${C_RESET}"

printf '%s\n%s\n%s' "$line1" "$line2" "$line3"
