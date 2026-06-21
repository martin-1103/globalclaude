#!/usr/bin/env bash
#
# gasslog — deterministic docker-log helper for the haiku-logs subagent.
#
# Two stages, by design:
#   1. list  — discover containers (live `docker ps`, no hardcoded names)
#   2. logs  — pull one container's logs, keep only error/warn, dedup + count
#
# Usage:
#   gasslog.sh list [pattern]                  # list running containers (optional name filter)
#   gasslog.sh logs <pattern> [window] [mode]  # filtered+deduped logs for ONE container
#
#   mode (3rd arg): SIGNAL (default) = errors/warns · ALL = every level ·
#                   any other value = treated as a grep -iE regex and the logs
#                   are searched for THAT content (level ignored). Use this to
#                   hunt non-error signals like "report=0" or "projection_gap".
#
# Examples:
#   gasslog.sh list                      # all running containers
#   gasslog.sh list sync                 # containers whose name matches "sync"
#   gasslog.sh logs sync-service         # last 5m of errors/warns, deduped
#   gasslog.sh logs sync 30m             # widen window to 30 minutes
#   gasslog.sh logs report 1h ALL        # include all levels, not only error/warn
#   gasslog.sh logs report 1h 'report=0|projection_gap'   # content search, deduped
#
# Env:
#   GASSLOG_TIMEOUT  per docker-call timeout in seconds (default 30). Guards
#                    against a wedged `docker logs` (see docker-log-truncate-hang).
#   GASSLOG_TAIL     read at most N tail lines (default 5000). Bounds the
#                    full-file scan json-file driver does on --since.
#
# Exit codes: 0 ok · 2 usage error / invalid search regex · 3 no container matched
#             · 4 ambiguous match · 5 docker unavailable / call timed out
set -euo pipefail

# Match structured slog levels (level=ERROR/WARN/...) — NOT bare "error", which
# false-positives on field names like error_cursors=0. Plus Go runtime crash
# markers that have no level= prefix.
LEVEL_RE='level=(ERROR|WARN|FATAL|PANIC)|panic:|fatal error:'
TIMEOUT="${GASSLOG_TIMEOUT:-30}"        # seconds per docker call
TAIL="${GASSLOG_TAIL:-5000}"            # read at most N tail lines (bounds full-file scan)

die() { printf '%s\n' "$1" >&2; exit "${2:-2}"; }

usage() {
  sed -n '3,26p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-2}"
}

# `docker ps` with a hard timeout; fails LOUD (never returns an empty list that
# would masquerade as "no containers" when the daemon is actually down).
docker_ps() {
  local out rc
  out="$(timeout "$TIMEOUT" docker ps --format "$1" 2>/dev/null)" || {
    rc=$?
    [ "$rc" -eq 124 ] && die "docker ps timed out after ${TIMEOUT}s — daemon wedged?" 5
    die "docker ps failed (exit $rc) — is the docker daemon up?" 5
  }
  printf '%s\n' "$out"
}

# Resolve a name pattern to exactly one running container.
# Prints the container name on success; errors out on zero or many matches.
resolve_one() {
  local pat="$1" matches
  matches="$(docker_ps '{{.Names}}' | grep -iE "$pat" || true)"
  [ -n "$matches" ] || die "no running container matches: $pat" 3
  if [ "$(printf '%s\n' "$matches" | wc -l)" -gt 1 ]; then
    {
      echo "ambiguous — \"$pat\" matches multiple containers:"
      printf '  %s\n' $matches
      echo "be more specific."
    } >&2
    exit 4
  fi
  printf '%s\n' "$matches"
}

cmd_list() {
  local pat="${1:-.}"
  printf '%-32s %s\n' "CONTAINER" "STATUS"
  docker_ps '{{.Names}}\t{{.Status}}' \
    | grep -iE "$pat" \
    | sort \
    | awk -F'\t' '{printf "%-32s %s\n", $1, $2}'
}

cmd_logs() {
  [ $# -ge 1 ] || die "logs needs a container pattern"
  local pat="$1" window="${2:-5m}" mode="${3:-SIGNAL}" name
  name="$(resolve_one "$pat")"

  # 3rd arg picks the filter:
  #   SIGNAL → error/warn levels · ALL → everything ·
  #   anything else → that value is a content-search regex (level ignored).
  local filter kind
  case "$mode" in
    SIGNAL) filter="$LEVEL_RE"; kind=signal ;;
    ALL)    filter='.';         kind=all ;;
    *)      filter="$mode";     kind=search
            # Validate the regex up front so a bad pattern fails LOUD (exit 2)
            # instead of being swallowed by the `|| true` on the grep below.
            # grep exits 1 on no-match (fine), 2 on a malformed regex (reject).
            # `|| rc=$?` keeps set -e from aborting on the expected exit 1.
            local rc=0
            printf '' | grep -iE "$filter" >/dev/null 2>&1 || rc=$?
            [ "$rc" -gt 1 ] && die "invalid search regex: $mode" 2 ;;
  esac

  # Fetch with a hard timeout, captured separately so a docker failure/timeout
  # surfaces LOUD instead of looking like an empty (healthy) log.
  local raw rc
  raw="$(timeout "$TIMEOUT" docker logs "$name" --tail "$TAIL" --since "$window" --timestamps 2>&1)" || {
    rc=$?
    [ "$rc" -eq 124 ] && \
      die "docker logs timed out after ${TIMEOUT}s for $name — log may be huge or wedged (see docker-log-truncate-hang)" 5
    die "docker logs failed (exit $rc) for $name" 5
  }

  # Filter on the captured buffer; grep no-match (exit 1) must NOT abort the script.
  local filtered
  filtered="$(printf '%s\n' "$raw" | grep -iE "$filter" || true)"

  # Dedup in awk: key = message with the timestamp prefix stripped and digit-runs
  # normalized to # so "took 41ms" and "took 9ms" collapse together. First line of
  # each key kept verbatim; track first/last ts + count. Sort by count desc.
  # Guard the empty case — feeding awk a lone blank line would forge a phantom group.
  local out=""
  if [ -n "$filtered" ]; then
    out="$(
      printf '%s\n' "$filtered" \
        | awk '
          {
            ts = $1                          # docker --timestamps prefixes RFC3339
            msg = $0
            sub(/^[^ ]+ /, "", msg)          # drop the ts prefix from the message
            key = msg
            gsub(/[0-9]+/, "#", key)         # normalize numbers for grouping
            if (!(key in seen)) {
              seen[key] = 1
              first[key] = ts
              sample[key] = msg
              order[++n] = key
            }
            last[key] = ts
            count[key]++
          }
          END {
            for (i = 1; i <= n; i++) {
              k = order[i]
              printf "%d\t%s\t%s\t%s\n", count[k], first[k], last[k], sample[k]
            }
          }
        ' \
        | sort -t$'\t' -k1,1 -rn
    )"
  fi

  echo "[container]: $name"
  echo "[window]:    last $window"
  [ "$kind" = search ] && echo "[search]:    $mode"
  if [ -z "$out" ]; then
    case "$kind" in
      all)    echo "[found]:     0 lines"
              echo "No log lines in last $window." ;;
      search) echo "[found]:     0 matches"
              echo "No lines matching /$mode/ in last $window." ;;
      *)      echo "[found]:     0 errors/warnings"
              echo "No errors/warnings in last $window. Service appears healthy." ;;
    esac
    return 0
  fi

  local groups label
  groups="$(printf '%s\n' "$out" | wc -l)"
  case "$kind" in
    search) label="match group(s)" ;;
    all)    label="line group(s)" ;;
    *)      label="error/warn group(s)" ;;
  esac
  echo "[found]:     $groups distinct $label"
  echo
  echo "RESULTS (most frequent first):"
  printf '%s\n' "$out" | awk -F'\t' '
    {
      n = $1; first = $2; last = $3; msg = $4
      printf "%d. [x%d] %s\n", NR, n, msg
      if (n > 1) printf "      first: %s  last: %s\n", first, last
      else       printf "      at: %s\n", first
    }'
}

command -v docker >/dev/null 2>&1 || die "docker not found in PATH" 5
command -v timeout >/dev/null 2>&1 || die "timeout (coreutils) not found in PATH" 5

[ $# -ge 1 ] || usage
sub="$1"; shift || true
case "$sub" in
  list)        cmd_list "$@" ;;
  logs)        cmd_logs "$@" ;;
  -h|--help|help) usage 0 ;;
  *)           die "unknown subcommand: $sub (use: list | logs)" ;;
esac
