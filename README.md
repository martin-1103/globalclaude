# globalclaude

<!-- ───────────────────────────────────────────────────────────────────────
     AGENT INSTRUCTIONS — if this file was pasted to you (an AI coding agent),
     execute the numbered steps below top-to-bottom, then stop and report.
     Do not skip steps. Do not ask for confirmation on the clone/install.
     ─────────────────────────────────────────────────────────────────────── -->

## ▶ For the AI agent: run these steps

You are setting up this Claude Code global config on the server you are running
on. Execute in order:

**1. Clone + install (config, templates, external CLI tools — auto-detects user/home):**

```bash
git clone https://github.com/martin-1103/globalclaude.git ~/globalclaude \
  && cd ~/globalclaude && bash install.sh
```

**2. Install the plugins** (their files come from GitHub marketplaces, not this repo):

```bash
claude plugin marketplace add JuliusBrussee/caveman
claude plugin marketplace add mksglu/context-mode
claude plugin marketplace add openai/codex-plugin-cc
claude plugin install caveman@caveman
claude plugin install context-mode@context-mode
claude plugin install codex@openai-codex
```

**3. Re-run install** so `settings.json` pins the real installed context-mode version:

```bash
cd ~/globalclaude && bash install.sh
```

**4. Cache-fix proxy** (prevents up to 20x cost on resumed sessions) — if step 1
warned it was inactive, follow [`cache-fix/README.md`](cache-fix/README.md).

**5. Report to the user** what `install.sh`'s final "tool status" / JSON-valid
lines showed, and list anything still missing (✗). Then tell them the two things
this script CANNOT do for them:
  - **Secrets**: `~/.claude/.credentials.json` (Claude auth) must be copied from
    an existing machine over a secure channel — no secret is in this repo.
  - **Restart Claude Code** for the new config/plugins/statusline to load.

Everything below is reference detail for those steps.

---

Portable Claude Code config — agents, skills, hooks, `settings.json`, `CLAUDE.md`.
Bootstraps a fresh server **for any user**. `install.sh` auto-detects the current
user, home directory, node binary, npm root, and the installed context-mode
plugin version, then renders the config from templates — no hardcoded `/root`.

> ⚠️ This repo intentionally contains **NO secrets**. Credentials
> (`.credentials.json`, `.claude.json`, `auth.json`) are gitignored and must be
> provided separately on each machine. Works as root (`~` = `/root`) or any
> other user (`~` = `/home/<user>`).

---

## What gets installed where

All `~` below resolve to the installing user's home (`/root`, `/home/deploy`, …).

| Repo path           | Installs to                | Purpose                                  |
|---------------------|----------------------------|------------------------------------------|
| `agents/`           | `~/.claude/agents/`        | Subagents (haiku-*, sonnet-editor, etc.) |
| `skills/`           | `~/.claude/skills/`        | Skills (investigate, fix-plan, tavily-*) |
| `hooks/`            | `~/.claude/hooks/`         | Session/tool hooks (cbm-*, cache-heal)   |
| `cproxy-hooks/`     | `~/.cproxy/hooks/`         | Caveman mode hooks (referenced by settings) |
| `CLAUDE.md`         | `~/.claude/CLAUDE.md`      | Global instructions (Bahasa Indonesia, ops rules) |
| `RTK.md`            | `~/.claude/RTK.md`         | RTK token-killer reference               |
| `settings.json.template` | `~/.claude/settings.json` | Rendered with detected paths/version |
| `.mcp.json.template`| `~/.claude/.mcp.json`      | Rendered: codebase-memory-mcp path       |
| `statusline.sh`     | `~/.claude/statusline.sh`  | Custom statusline                        |
| `cache-fix/`        | systemd + npm global       | Prompt-cache proxy (see below)           |

### Templates & placeholders

`settings.json` and `.mcp.json` ship as `.template` files (the literal versions
are intentionally NOT committed — one source of truth). `install.sh` substitutes:

| Placeholder       | Resolved from                                  |
|-------------------|------------------------------------------------|
| `__CLAUDE__`      | `$HOME/.claude`                                |
| `__CPROXY__`      | `$HOME/.cproxy`                                |
| `__LOCALBIN__`    | `$HOME/.local/bin`                             |
| `__NODE_BIN__`    | `command -v node`                              |
| `__NPM_ROOT__`    | `npm root -g` (cache-fix service only)         |
| `__CTX_VER__`     | installed context-mode version (auto-detected) |

The installer validates the rendered `settings.json` as JSON before trusting it.

---

## External dependencies (NOT in this repo)

`settings.json` references tools that live outside `~/.claude`. Install these
or the referenced hooks will error:

`install.sh` runs [`deps.sh`](deps.sh) automatically. Set `SKIP_DEPS=1` to skip.

| Tool                  | Used by                        | Source / install                              |
|-----------------------|--------------------------------|-----------------------------------------------|
| **node**              | all hooks + cache-fix service  | Any Node.js on PATH                           |
| **ripgrep** (`rg`)    | haiku-explorer, statusline     | `apt install ripgrep`                         |
| **fd-find** (`fdfind`)| haiku-explorer                 | `apt install fd-find`                         |
| **jq**                | statusline                     | `apt install jq`                             |
| **ast-grep**          | haiku-explorer (structural search) | `npm i -g @ast-grep/cli` / `cargo install ast-grep` — [ast-grep.github.io](https://ast-grep.github.io) |
| **tokei**             | haiku-explorer (LoC)           | `cargo install tokei`                         |
| **aid** (AI Distiller)| haiku-explorer (code distill)  | [github.com/janreges/ai-distiller](https://github.com/janreges/ai-distiller) (binary) |
| **tavily**            | tavily-* skills                | `uv tool install tavily-cli`                  |
| **rtk**               | `PreToolUse` Bash hook         | [github.com/rtk-ai/rtk](https://github.com/rtk-ai/rtk) — `brew install rtk` then `rtk init -g` |
| **codebase-memory-mcp** | `.mcp.json`                  | [github.com/DeusData/codebase-memory-mcp](https://github.com/DeusData/codebase-memory-mcp) — `npm i -g codebase-memory-mcp` |
| **claude-code-cache-fix** | prompt-cache proxy (port 9801) | `npm i -g claude-code-cache-fix` + systemd — see [`cache-fix/`](cache-fix/) |
| **Plugins**           | `enabledPlugins` in settings   | Install via marketplace (see below)           |

> ⚠️ **rtk name collision**: `cargo install rtk` / `npm i rtk` installs a
> *different* tool (Rust Type Kit). Use the official rtk-ai installer or brew.
> `deps.sh` handles this correctly.

### Prompt-cache proxy (important — saves cost)

CC v2.1.113+ invalidates the prompt cache on resumed sessions, causing **up to
20x cost increase**. The `cache-fix/` directory sets up a local proxy that fixes
this. **Run this on every server** — see [`cache-fix/README.md`](cache-fix/README.md).

Quick version:

```bash
npm install -g claude-code-cache-fix
# render unit with detected node/npm paths, then enable
sed -e "s#__NODE_BIN__#$(command -v node)#g" -e "s#__NPM_ROOT__#$(npm root -g)#g" \
    cache-fix/cache-fix-proxy.service.template > /etc/systemd/system/cache-fix-proxy.service
systemctl daemon-reload && systemctl enable --now cache-fix-proxy
echo 'export ANTHROPIC_BASE_URL="http://127.0.0.1:9801"' >> ~/.bashrc
```

(`install.sh` does this rendering for you — manual steps shown for reference.)

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
~/.claude/plugins/cache/context-mode/context-mode/1.0.162/hooks/sessionstart.mjs
```

After installing context-mode, the actual installed version may differ. If
hooks fail with "file not found", update the version number in `settings.json`
to match the directory under `plugins/cache/context-mode/context-mode/`.

`install.sh` warns you if the pinned version is missing.

---

## Secrets — supply separately

These are **gitignored** and must be placed on each machine by hand:

- `~/.claude/.credentials.json` — Claude auth token
- `~/.claude/.claude.json` — account/session state

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
ls ~/.claude/{agents,skills,hooks} ~/.claude/settings.json

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
cd ~/globalclaude
cp -r ~/.claude/{agents,skills,hooks} .
cp -r ~/.cproxy/hooks/. cproxy-hooks/
cp ~/.claude/{CLAUDE.md,RTK.md,statusline.sh} .
# settings.json / .mcp.json: edit the .template files by hand if their
# structure changed — do NOT copy the rendered (path-substituted) versions back.
git add -A && git commit -m "sync config" && git push
```

Re-run the secret scan before pushing:

```bash
grep -rInE 'sk-[A-Za-z0-9]{20}|ghp_|BEGIN.*PRIVATE KEY' . --exclude-dir=.git
```
