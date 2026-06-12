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

# 2. Install + enable the systemd service
cp cache-fix-proxy.service /etc/systemd/system/cache-fix-proxy.service
systemctl daemon-reload
systemctl enable --now cache-fix-proxy

# 3. Route claude through the proxy (append to ~/.bashrc)
echo 'export ANTHROPIC_BASE_URL="http://127.0.0.1:9801"' >> ~/.bashrc
```

> The service file pins the node path
> `/www/server/nodejs/v24.16.0/lib/node_modules/...`. If npm installs elsewhere,
> fix `ExecStart` to match `npm root -g`.

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
