#!/usr/bin/env bash
#
# globalclaude installer
# Target: identical server (user root, home /root). Idempotent.
# Run from the cloned repo root:  bash install.sh
#
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLAUDE_DIR="/root/.claude"
CPROXY_DIR="/root/.cproxy"

log()  { printf '\033[1;32m[install]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[warn]\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m[fail]\033[0m %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# 0. Preconditions
# ---------------------------------------------------------------------------
[ "$(id -u)" = "0" ] || warn "Not root — paths assume /root. Continue at your own risk."
command -v git  >/dev/null || die "git not found"
command -v node >/dev/null || warn "node not on PATH — hooks may fail (Hermes node expected at /root/.hermes/node/bin/node)"

mkdir -p "$CLAUDE_DIR" "$CPROXY_DIR/hooks"

# ---------------------------------------------------------------------------
# 1. Backup existing config (timestamped)
# ---------------------------------------------------------------------------
if [ -f "$CLAUDE_DIR/settings.json" ]; then
  BK="$CLAUDE_DIR/settings.json.pre-globalclaude"
  cp "$CLAUDE_DIR/settings.json" "$BK"
  log "Backed up existing settings.json -> $BK"
fi

# ---------------------------------------------------------------------------
# 2. Copy config into ~/.claude
# ---------------------------------------------------------------------------
log "Copying agents/ skills/ hooks/"
cp -r "$REPO_DIR/agents"  "$CLAUDE_DIR/"
cp -r "$REPO_DIR/skills"  "$CLAUDE_DIR/"
cp -r "$REPO_DIR/hooks"   "$CLAUDE_DIR/"

log "Copying root files"
cp "$REPO_DIR/CLAUDE.md"     "$CLAUDE_DIR/"
cp "$REPO_DIR/RTK.md"        "$CLAUDE_DIR/"
cp "$REPO_DIR/.mcp.json"     "$CLAUDE_DIR/"
cp "$REPO_DIR/statusline.sh" "$CLAUDE_DIR/"
cp "$REPO_DIR/settings.json" "$CLAUDE_DIR/"
chmod +x "$CLAUDE_DIR/statusline.sh" || true

log "Copying caveman hooks -> $CPROXY_DIR/hooks"
cp -r "$REPO_DIR/cproxy-hooks/." "$CPROXY_DIR/hooks/"

# make hook scripts executable
chmod +x "$CLAUDE_DIR/hooks/"* 2>/dev/null || true

# ---------------------------------------------------------------------------
# 3. External tools — install if missing
# ---------------------------------------------------------------------------
# rtk (token killer) — optional, referenced by PreToolUse hook
if ! command -v rtk >/dev/null; then
  warn "rtk not installed. Install manually (cargo/binary) or remove its PreToolUse hook from settings.json."
fi

# codebase-memory-mcp — referenced by .mcp.json
if ! command -v codebase-memory-mcp >/dev/null; then
  warn "codebase-memory-mcp not found. Install: npm i -g codebase-memory-mcp  (expected at /root/.local/bin or hermes node bin)"
fi

# ---------------------------------------------------------------------------
# 4. Plugins — add marketplaces + install
# ---------------------------------------------------------------------------
# These are pulled from GitHub by Claude Code itself. settings.json already
# lists extraKnownMarketplaces + enabledPlugins, but the plugin *files* must be
# fetched once. Run these inside Claude Code, or via CLI if available:
log "Plugin marketplaces required (add via Claude Code /plugin or CLI):"
cat <<'EOF'
    caveman        -> JuliusBrussee/caveman
    context-mode   -> mksglu/context-mode
    openai-codex   -> openai/codex-plugin-cc
    gopls-lsp      -> anthropics/claude-plugins-official

  If `claude` CLI supports it:
    claude plugin marketplace add JuliusBrussee/caveman
    claude plugin marketplace add mksglu/context-mode
    claude plugin marketplace add openai/codex-plugin-cc
    claude plugin install caveman@caveman
    claude plugin install context-mode@context-mode
    claude plugin install codex@openai-codex
EOF

# ---------------------------------------------------------------------------
# 5. Verify version-pinned paths in settings.json
# ---------------------------------------------------------------------------
PINNED="$(grep -oE 'context-mode/[0-9]+\.[0-9]+\.[0-9]+' "$CLAUDE_DIR/settings.json" | head -1 || true)"
if [ -n "$PINNED" ]; then
  EXPECT="/root/.claude/plugins/cache/$PINNED"
  if [ ! -d "$EXPECT" ]; then
    warn "settings.json pins $PINNED but $EXPECT missing."
    warn "After installing context-mode, update the version number in settings.json to match the installed one."
  fi
fi

log "Done. Restart Claude Code. Verify: caveman mode loads, statusline shows, /plugin lists enabled plugins."
