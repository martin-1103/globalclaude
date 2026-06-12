#!/usr/bin/env bash
#
# globalclaude installer — portable across users (no hardcoded /root).
# Detects current user/home, runtime paths, and plugin versions, then renders
# config templates accordingly. Idempotent.
#
# Run from the cloned repo root:  bash install.sh
#
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

log()  { printf '\033[1;32m[install]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[warn]\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m[fail]\033[0m %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# 0. Detect environment — NO assumption of root / /root
# ---------------------------------------------------------------------------
USER_NAME="$(id -un)"
HOME_DIR="${HOME:-$(getent passwd "$USER_NAME" | cut -d: -f6)}"
[ -n "$HOME_DIR" ] && [ -d "$HOME_DIR" ] || die "Cannot resolve home dir for $USER_NAME"

CLAUDE_DIR="$HOME_DIR/.claude"
CPROXY_DIR="$HOME_DIR/.cproxy"
LOCALBIN="$HOME_DIR/.local/bin"

# node binary (runs all hooks + cache-fix service)
NODE_BIN="$(command -v node || true)"
[ -n "$NODE_BIN" ] || warn "node not on PATH — hooks/service may fail"

# npm global module root (for cache-fix service ExecStart)
NPM_ROOT="$(npm root -g 2>/dev/null || echo "")"

log "Detected: user=$USER_NAME  home=$HOME_DIR"
log "          node=$NODE_BIN  npm_root=${NPM_ROOT:-<none>}"

mkdir -p "$CLAUDE_DIR" "$CPROXY_DIR/hooks" "$LOCALBIN"

# ---------------------------------------------------------------------------
# 1. Detect installed context-mode plugin version (settings.json pins it)
# ---------------------------------------------------------------------------
CTX_DIR="$CLAUDE_DIR/plugins/cache/context-mode/context-mode"
CTX_VER=""
if [ -d "$CTX_DIR" ]; then
  CTX_VER="$(ls -1 "$CTX_DIR" 2>/dev/null | sort -V | tail -1)"
fi
if [ -z "$CTX_VER" ]; then
  CTX_VER="1.0.162"
  warn "context-mode not installed yet — pinning placeholder $CTX_VER in settings.json."
  warn "Install the plugin, then re-run this script (or fix the version by hand)."
else
  log "context-mode version detected: $CTX_VER"
fi

# ---------------------------------------------------------------------------
# 2. Backup existing settings.json
# ---------------------------------------------------------------------------
if [ -f "$CLAUDE_DIR/settings.json" ]; then
  cp "$CLAUDE_DIR/settings.json" "$CLAUDE_DIR/settings.json.pre-globalclaude"
  log "Backed up existing settings.json -> settings.json.pre-globalclaude"
fi

# ---------------------------------------------------------------------------
# 3. Copy plain config (no path substitution needed)
# ---------------------------------------------------------------------------
log "Copying agents/ skills/ hooks/"
cp -r "$REPO_DIR/agents"  "$CLAUDE_DIR/"
cp -r "$REPO_DIR/skills"  "$CLAUDE_DIR/"
cp -r "$REPO_DIR/hooks"   "$CLAUDE_DIR/"

log "Copying root files"
cp "$REPO_DIR/CLAUDE.md"     "$CLAUDE_DIR/"
cp "$REPO_DIR/RTK.md"        "$CLAUDE_DIR/"
cp "$REPO_DIR/statusline.sh" "$CLAUDE_DIR/"
chmod +x "$CLAUDE_DIR/statusline.sh" "$CLAUDE_DIR/hooks/"* 2>/dev/null || true

log "Copying caveman hooks -> $CPROXY_DIR/hooks"
cp -r "$REPO_DIR/cproxy-hooks/." "$CPROXY_DIR/hooks/"

# ---------------------------------------------------------------------------
# 4. Render templated config (substitute detected paths)
# ---------------------------------------------------------------------------
render() {  # render <template> <output>
  sed -e "s#__CLAUDE__#$CLAUDE_DIR#g" \
      -e "s#__CPROXY__#$CPROXY_DIR#g" \
      -e "s#__LOCALBIN__#$LOCALBIN#g" \
      -e "s#__NODE_BIN__#${NODE_BIN:-node}#g" \
      -e "s#__NPM_ROOT__#${NPM_ROOT:-/usr/lib/node_modules}#g" \
      -e "s#__CTX_VER__#$CTX_VER#g" \
      "$1" > "$2"
}

log "Rendering settings.json"
render "$REPO_DIR/settings.json.template" "$CLAUDE_DIR/settings.json"
# Validate JSON before trusting it
if command -v node >/dev/null; then
  node -e "JSON.parse(require('fs').readFileSync('$CLAUDE_DIR/settings.json','utf8'))" \
    && log "settings.json is valid JSON" \
    || die "Rendered settings.json is INVALID JSON — check template placeholders"
fi

log "Rendering .mcp.json"
render "$REPO_DIR/.mcp.json.template" "$CLAUDE_DIR/.mcp.json"

# ---------------------------------------------------------------------------
# 5. External CLI tools (rg, ast-grep, aid, tavily, rtk, codebase-memory-mcp...)
# ---------------------------------------------------------------------------
if [ "${SKIP_DEPS:-0}" = "1" ]; then
  warn "SKIP_DEPS=1 — skipping external tool install (deps.sh)"
else
  log "Installing external CLI tools (deps.sh)"
  bash "$REPO_DIR/deps.sh" || warn "deps.sh reported failures — review output above"
fi

# ---------------------------------------------------------------------------
# 6. Cache-fix proxy (prompt-cache stabilizer) — render service with detected paths
# ---------------------------------------------------------------------------
if ! (command -v systemctl >/dev/null && systemctl is-active --quiet cache-fix-proxy 2>/dev/null); then
  warn "cache-fix-proxy not active. Strongly recommended (prevents up to 20x cost on resumed sessions)."
  if [ -n "$NPM_ROOT" ] && [ "$USER_NAME" = "root" ]; then
    render "$REPO_DIR/cache-fix/cache-fix-proxy.service.template" "/tmp/cache-fix-proxy.service"
    warn "Rendered service -> /tmp/cache-fix-proxy.service. To install:"
    warn "  npm i -g claude-code-cache-fix"
    warn "  cp /tmp/cache-fix-proxy.service /etc/systemd/system/"
    warn "  systemctl daemon-reload && systemctl enable --now cache-fix-proxy"
  else
    warn "  Non-root or no npm root — see cache-fix/README.md for user-unit setup."
  fi
  warn "  Then: echo 'export ANTHROPIC_BASE_URL=\"http://127.0.0.1:9801\"' >> ~/.bashrc"
fi

# ---------------------------------------------------------------------------
# 7. Plugins
# ---------------------------------------------------------------------------
log "Plugins to install (via Claude Code /plugin or CLI):"
cat <<'EOF'
    claude plugin marketplace add JuliusBrussee/caveman
    claude plugin marketplace add mksglu/context-mode
    claude plugin marketplace add openai/codex-plugin-cc
    claude plugin install caveman@caveman
    claude plugin install context-mode@context-mode
    claude plugin install codex@openai-codex
EOF
warn "After installing context-mode, RE-RUN this script so settings.json pins the real version."

log "Done. Restart Claude Code. Verify: caveman banner, statusline, /plugin list."
