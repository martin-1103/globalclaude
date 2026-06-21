# haiku-db Context — <PROJECT NAME>
<!-- Per-project adapter for the global haiku-db skill/agent. Copy to ./.claude/haiku-db-context.md -->
<!-- The engine (agent/skill) is project-agnostic; THIS file holds everything project-specific. -->
<!-- Last updated: YYYY-MM-DD -->

## Credentials Bootstrap

How the agent loads DB creds into the shell. Default works for most projects; override
if creds live somewhere other than `./.env`.
```bash
set -a; source ./.env 2>/dev/null; set +a
```

## DB Connections

### ClickHouse
| Env Var | Default | Notes |
|---------|---------|-------|
| `CLICKHOUSE_USER` | `default` | username |
| `CLICKHOUSE_PASSWORD` | from `.env` | password (NEVER hardcode here) |

Container name (from `docker ps`): `<clickhouse-container>`
Databases: `<list databases, e.g. source_mirror, report, cs>`

### MySQL
| Env Var | DSN / value | Database |
|---------|-------------|----------|
| `<DSN_VAR>` | `user:pass@tcp(<host>:3306)/<db>` | `<db>` |

Container name: `<mysql-container>` · CLI user/pass: `<user>` / `<pass>`

### Redis
Container: `<redis-container>` · host:port `<redis>:6379` · DB index `0`

## Query Conventions

- CH native port `9000`, HTTP `8123`
- Multi-tenant filter: `WHERE project_id = ?`  (adjust to this project's tenant column)
- <any project-specific gotchas: timezone, ReplacingMergeTree, ToLocalUTC, etc.>

---

## Discovered Tables (auto-updated by haiku-db skill)
<!-- haiku-db appends new schema discoveries below this line -->
<!-- Format: `database.table_name` — description (discovered YYYY-MM-DD) -->
<!-- Last updated: YYYY-MM-DD -->
