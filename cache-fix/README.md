# claude-code-cache-fix setup

Stabilizes Claude Code's prompt cache. Without it, **resumed sessions can cost up
to 20x** more (cache prefix invalidated on resume). Upstream:
https://github.com/cnighswonger/claude-code-cache-fix

## Why proxy mode (not preload)

CC **v2.1.113+** ships as a Bun binary — the preload interceptor
(`NODE_OPTIONS="--import"`) does **not** work. This server runs CC v2.1.174, so
**proxy mode is mandatory**.

Architecture:

```
claude  --(ANTHROPIC_BASE_URL=http://127.0.0.1:9801)-->  cache-fix-proxy  -->  api.anthropic.com
```

The proxy runs as a systemd service; `.bashrc` exports `ANTHROPIC_BASE_URL` so
every `claude` invocation routes through it.

## Install (target server, root)

```bash
# 1. Install the npm package globally (provides cache-fix-proxy binary + proxy/server.mjs)
npm install -g claude-code-cache-fix

# 2. Render the systemd unit with this machine's node + npm paths
NODE_BIN="$(command -v node)"
NPM_ROOT="$(npm root -g)"
sed -e "s#__NODE_BIN__#$NODE_BIN#g" -e "s#__NPM_ROOT__#$NPM_ROOT#g" \
    cache-fix-proxy.service.template > /etc/systemd/system/cache-fix-proxy.service

# 3. Enable + start
systemctl daemon-reload
systemctl enable --now cache-fix-proxy

# 4. Route claude through the proxy (append to ~/.bashrc)
echo 'export ANTHROPIC_BASE_URL="http://127.0.0.1:9801"' >> ~/.bashrc
```

> The unit ships as `.service.template` with `__NODE_BIN__` / `__NPM_ROOT__`
> placeholders so it works regardless of where node/npm live. `install.sh`
> renders it automatically; the steps above are the manual path. Service runs as
> root (systemd system unit). For a non-root user, use a systemd **user** unit
> (`systemctl --user`) and drop the `User=root` line.

## Verify

```bash
systemctl is-active cache-fix-proxy        # -> active
curl -s http://127.0.0.1:9801/health       # -> {"status":"ok"}
echo "$ANTHROPIC_BASE_URL"                  # -> http://127.0.0.1:9801 (after sourcing bashrc)
```

A `degraded` health (503) means a process restart is needed:
`systemctl restart cache-fix-proxy`.

## Recommended env (optional, in ~/.claude/settings.json)

Pins models so the cache prefix hash stays stable across CC version bumps:

```json
{
  "env": {
    "CLAUDE_CODE_DISABLE_LEGACY_MODEL_REMAP": "1"
  }
}
```

`CLAUDE_CODE_DISABLE_LEGACY_MODEL_REMAP=1` is the single most impactful flag —
stops CC silently remapping your pinned model after updates.
