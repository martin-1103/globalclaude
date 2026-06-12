#!/usr/bin/env bash
#
# globalclaude — external CLI tool installer.
# Installs the command-line tools that agents/skills/hooks invoke at runtime.
# These live OUTSIDE ~/.claude and are not shipped in this repo.
#
# Idempotent — skips anything already on PATH. Run: bash deps.sh
#
set -uo pipefail   # NOT -e: one tool failing shouldn't abort the rest

log()  { printf '\033[1;32m[deps]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[warn]\033[0m %s\n' "$*"; }
have() { command -v "$1" >/dev/null 2>&1; }

ARCH="$(uname -m)"   # x86_64 / aarch64

# --- apt tools (Debian/Ubuntu) --------------------------------------------
APT_PKGS=()
have rg     || APT_PKGS+=(ripgrep)
have fdfind || { have fd || APT_PKGS+=(fd-find); }
have jq     || APT_PKGS+=(jq)
if [ "${#APT_PKGS[@]}" -gt 0 ]; then
  if have apt-get; then
    log "apt install: ${APT_PKGS[*]}"
    apt-get update -qq && apt-get install -y -qq "${APT_PKGS[@]}" || warn "apt install failed for: ${APT_PKGS[*]}"
  else
    warn "apt-get not found. Install manually: ${APT_PKGS[*]}"
  fi
fi

# --- ast-grep (structural search; haiku-explorer) -------------------------
if ! have ast-grep; then
  if have npm;   then log "npm i -g @ast-grep/cli"; npm i -g @ast-grep/cli || warn "ast-grep via npm failed"
  elif have cargo; then log "cargo install ast-grep"; cargo install ast-grep --locked || warn "ast-grep via cargo failed"
  else warn "ast-grep: need npm or cargo. https://ast-grep.github.io/guide/quick-start.html"
  fi
fi

# --- tokei (LoC counter; haiku-explorer) ----------------------------------
if ! have tokei; then
  if have cargo; then log "cargo install tokei"; cargo install tokei || warn "tokei via cargo failed"
  else warn "tokei: need cargo, or apt install tokei on newer distros."
  fi
fi

# --- aid / AI Distiller (code structure extractor; haiku-explorer) --------
# Repo: https://github.com/janreges/ai-distiller  (single Go binary, zero deps)
if ! have aid; then
  warn "aid (AI Distiller) not installed. Binary from GitHub releases:"
  warn "  https://github.com/janreges/ai-distiller/releases  (linux-$ARCH, chmod +x, mv -> /usr/local/bin/aid)"
fi

# --- tavily CLI (web search/extract skills) -------------------------------
if ! have tavily; then
  if have uv;   then log "uv tool install tavily-cli"; uv tool install tavily-cli || warn "tavily via uv failed"
  elif have pip3; then log "pip install tavily-cli"; pip3 install --user tavily-cli || warn "tavily via pip failed"
  else warn "tavily: need uv or pip. uv tool install tavily-cli"
  fi
fi

# --- codebase-memory-mcp (.mcp.json) --------------------------------------
# Repo: https://github.com/DeusData/codebase-memory-mcp  (single static binary)
if ! have codebase-memory-mcp; then
  if have npm; then log "npm i -g codebase-memory-mcp"; npm i -g codebase-memory-mcp || warn "codebase-memory-mcp via npm failed"
  else warn "codebase-memory-mcp: need npm. npm i -g codebase-memory-mcp (or brew/scoop/go — see repo)"
  fi
fi

# --- rtk (Rust Token Killer; PreToolUse Bash hook) ------------------------
# Repo: https://github.com/rtk-ai/rtk
# WARNING: `cargo install rtk` / `npm i rtk` grabs a DIFFERENT tool (name
# collision — Rust Type Kit). Use the official installer below.
if ! have rtk; then
  if have brew; then
    log "brew install rtk"; brew install rtk || warn "rtk via brew failed"
  elif have curl; then
    log "Installing rtk via official installer (-> ~/.local/bin)"
    curl -fsSL https://raw.githubusercontent.com/rtk-ai/rtk/refs/heads/master/install.sh | sh \
      || warn "rtk installer failed — see https://github.com/rtk-ai/rtk"
  else
    warn "rtk not installed. Install: https://github.com/rtk-ai/rtk (brew/curl/cargo)"
  fi
  # Wire the Claude Code PreToolUse hook (non-interactive). Adds its own RTK.md;
  # --hook-only avoids clobbering the RTK.md shipped by globalclaude.
  have rtk && { log "rtk init -g --auto-patch --hook-only"; rtk init -g --auto-patch --hook-only || warn "rtk init failed — run 'rtk init -g' manually"; }
fi

# --- Report ----------------------------------------------------------------
echo ""
log "Final tool status:"
for t in rg fdfind jq ast-grep tokei aid tavily codebase-memory-mcp rtk node npm; do
  if have "$t"; then printf '  \033[32m✓\033[0m %s\n' "$t"; else printf '  \033[31m✗\033[0m %s  (missing)\n' "$t"; fi
done
echo ""
warn "Any ✗ above: the agent/skill/hook that calls it will degrade. rtk & aid are"
warn "optional-but-recommended; rg/jq are needed by haiku-explorer + statusline."
