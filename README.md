# globalclaude

Portable Claude Code config — agents, skills, hooks, `settings.json`, `CLAUDE.md`.
Designed to bootstrap a **fresh server with an identical layout** (user `root`, home `/root`).

> ⚠️ This repo intentionally contains **NO secrets**. Credentials
> (`.credentials.json`, `.claude.json`, `auth.json`) are gitignored and must be
> provided separately on each machine.

---

## Quick start (for an AI agent on the target server)

```bash
git clone https://github.com/martin-1103/globalclaude.git /root/globalclaude
cd /root/globalclaude
bash install.sh
```

Then restart Claude Code and run the verification steps below.

---

## What gets installed where

| Repo path           | Installs to                | Purpose                                  |
|---------------------|----------------------------|------------------------------------------|
| `agents/`           | `/root/.claude/agents/`    | Subagents (haiku-*, sonnet-editor, etc.) |
| `skills/`           | `/root/.claude/skills/`    | Skills (investigate, fix-plan, tavily-*) |
| `hooks/`            | `/root/.claude/hooks/`     | Session/tool hooks (cbm-*, cache-heal)   |
| `cproxy-hooks/`     | `/root/.cproxy/hooks/`     | Caveman mode hooks (referenced by settings) |
| `CLAUDE.md`         | `/root/.claude/CLAUDE.md`  | Global instructions (Bahasa Indonesia, ops rules) |
| `RTK.md`            | `/root/.claude/RTK.md`     | RTK token-killer reference               |
| `settings.json`     | `/root/.claude/settings.json` | Hooks, plugins, statusline, model     |
| `.mcp.json`         | `/root/.claude/.mcp.json`  | MCP servers (codebase-memory-mcp)        |
| `statusline.sh`     | `/root/.claude/statusline.sh` | Custom statusline                     |
| `cache-fix/`        | systemd + npm global       | Prompt-cache proxy (see below)           |

---

## External dependencies (NOT in this repo)

`settings.json` references tools that live outside `~/.claude`. Install these
or the referenced hooks will error:

| Tool                  | Used by                        | Install hint                                  |
|-----------------------|--------------------------------|-----------------------------------------------|
| **Hermes node**       | caveman hooks (`/root/.hermes/node/bin/node`) | Provided by the Hermes runtime. Must exist at that path. |
| **rtk**               | `PreToolUse` Bash hook (`rtk hook claude`) | Rust binary -> `/root/.local/bin/rtk`     |
| **codebase-memory-mcp** | `.mcp.json`                  | `npm i -g codebase-memory-mcp` -> `/root/.local/bin/` |
| **claude-code-cache-fix** | prompt-cache proxy (port 9801) | `npm i -g claude-code-cache-fix` + systemd service — see [`cache-fix/`](cache-fix/) |
| **Plugins**           | `enabledPlugins` in settings   | Install via marketplace (see below)           |

### Prompt-cache proxy (important — saves cost)

CC v2.1.113+ invalidates the prompt cache on resumed sessions, causing **up to
20x cost increase**. The `cache-fix/` directory sets up a local proxy that fixes
this. **Run this on every server** — see [`cache-fix/README.md`](cache-fix/README.md).

Quick version:

```bash
npm install -g claude-code-cache-fix
cp cache-fix/cache-fix-proxy.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now cache-fix-proxy
echo 'export ANTHROPIC_BASE_URL="http://127.0.0.1:9801"' >> ~/.bashrc
```

### Plugins

`settings.json` enables these plugins but their **files are fetched from GitHub
by Claude Code**, not stored here:

- `caveman@caveman` — `JuliusBrussee/caveman`
- `context-mode@context-mode` — `mksglu/context-mode`
- `codex@openai-codex` — `openai/codex-plugin-cc`
- `gopls-lsp@claude-plugins-official` — `anthropics/claude-plugins-official`

Add + install (if the `claude` CLI supports plugin commands):

```bash
claude plugin marketplace add JuliusBrussee/caveman
claude plugin marketplace add mksglu/context-mode
claude plugin marketplace add openai/codex-plugin-cc
claude plugin install caveman@caveman
claude plugin install context-mode@context-mode
claude plugin install codex@openai-codex
```

Otherwise add them from inside Claude Code with `/plugin`.

---

## ⚠️ Version-pinned paths — read this

`settings.json` hardcodes the **context-mode plugin version** in several hook
paths, e.g.:

```
/root/.claude/plugins/cache/context-mode/context-mode/1.0.162/hooks/sessionstart.mjs
```

After installing context-mode, the actual installed version may differ. If
hooks fail with "file not found", update the version number in `settings.json`
to match the directory under `plugins/cache/context-mode/context-mode/`.

`install.sh` warns you if the pinned version is missing.

---

## Secrets — supply separately

These are **gitignored** and must be placed on each machine by hand:

- `/root/.claude/.credentials.json` — Claude auth token
- `/root/.claude/.claude.json` — account/session state
- `/root/.hermes/auth.json`, `/root/.hermes/.env` — Hermes credentials

Copy them over a secure channel (scp/secrets manager). Never commit them.

> 🔑 **`.bashrc` warning**: the source server's `~/.bashrc` holds **raw API keys**
> in shell aliases (`gpt`, `ds`, etc.) plus the `ANTHROPIC_BASE_URL` proxy export.
> **Never commit `.bashrc`.** Only the single `export ANTHROPIC_BASE_URL` line is
> needed for the cache-fix proxy — add it by hand (see cache-fix setup), do not
> copy the whole file.

---

## Verify after install

```bash
# 1. Config in place
ls /root/.claude/{agents,skills,hooks} /root/.claude/settings.json

# 2. External tools
which rtk codebase-memory-mcp node

# 3. Cache-fix proxy
systemctl is-active cache-fix-proxy            # -> active
curl -s http://127.0.0.1:9801/health           # -> {"status":"ok"}

# 4. Start Claude Code, then inside it:
#    - caveman mode banner appears on session start
#    - statusline renders
#    - /plugin shows caveman, context-mode, codex enabled
#    - a skill triggers (e.g. type /investigate)
```

---

## Updating the repo from a live machine

When you tweak config on the source machine and want to push changes:

```bash
cd /root/globalclaude
cp -r /root/.claude/{agents,skills,hooks} .
cp -r /root/.cproxy/hooks/. cproxy-hooks/
cp /root/.claude/{CLAUDE.md,RTK.md,settings.json,.mcp.json,statusline.sh} .
git add -A && git commit -m "sync config" && git push
```

Re-run the secret scan before pushing:

```bash
grep -rInE 'sk-[A-Za-z0-9]{20}|ghp_|BEGIN.*PRIVATE KEY' . --exclude-dir=.git
```
