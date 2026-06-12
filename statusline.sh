#!/usr/bin/env bash
# Claude Code statusline — 2 lines
# Line 1: model | path | branch
# Line 2: context used/max (percentage)

input=$(cat)

model=$(printf '%s' "$input" | jq -r '.model.display_name // "?"')
dir=$(printf '%s' "$input" | jq -r '.workspace.current_dir // .cwd // "?"')
session=$(printf '%s' "$input" | jq -r '.session_id // ""')

# shorten home to ~
dir=${dir/#$HOME/\~}

# git branch (fallback empty)
branch=$(git -C "$(printf '%s' "$input" | jq -r '.workspace.current_dir // .cwd')" rev-parse --abbrev-ref HEAD 2>/dev/null)

# context window
used=$(printf '%s' "$input" | jq -r '.context_window.total_input_tokens // 0')
max=$(printf '%s' "$input" | jq -r '.context_window.context_window_size // 200000')
pct=$(printf '%s' "$input" | jq -r '.context_window.used_percentage // 0')

# format tokens as k
fmt() { awk -v n="$1" 'BEGIN{ if(n>=1000) printf "%.1fk", n/1000; else printf "%d", n }'; }
used_k=$(fmt "$used")
max_k=$(fmt "$max")

# colors
C_RESET=$'\033[0m'
C_DIM=$'\033[2m'
C_CYAN=$'\033[36m'
C_GREEN=$'\033[32m'
C_YELLOW=$'\033[33m'
C_RED=$'\033[31m'

# color by usage
bar_color=$C_GREEN
awk_pct=${pct%.*}
if   [ "${awk_pct:-0}" -ge 80 ]; then bar_color=$C_RED
elif [ "${awk_pct:-0}" -ge 50 ]; then bar_color=$C_YELLOW
fi

line1="${C_CYAN}${model}${C_RESET} ${C_DIM}|${C_RESET} ${dir}"
[ -n "$branch" ] && line1="${line1} ${C_DIM}|${C_RESET} ${C_GREEN}⎇ ${branch}${C_RESET}"

line2="${bar_color}ctx ${used_k}/${max_k} (${pct}%)${C_RESET}"
[ -n "$session" ] && line2="${line2} ${C_DIM}| sid ${session}${C_RESET}"

printf '%s\n%s' "$line1" "$line2"
