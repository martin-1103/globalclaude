package db

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"agent-db/internal/config"
)

// Tools executes database queries inside docker containers based on the
// merged config. Every method returns the trimmed combined output (stdout,
// with stderr appended on failure) so the agent always sees what happened.
type Tools struct {
	cfg config.Config
}

func New(cfg config.Config) *Tools {
	return &Tools{cfg: cfg}
}

// ToolCall is the parsed JSON the LLM emits to request a tool.
type ToolCall struct {
	Tool     string `json:"tool"`
	SQL      string `json:"sql"`
	Command  string `json:"command"`
	DBType   string `json:"db_type"`
	Table    string `json:"table"`
	Database string `json:"database"`
	Where    string `json:"where"`

	// Done signals end of the loop.
	Done   bool   `json:"done"`
	Answer string `json:"answer"`
}

// Execute dispatches a parsed tool call. It returns the tool output to feed
// back into the conversation. Errors are returned as text (not Go errors) so
// the agent can see and recover from them, except for unknown tools.
func (t *Tools) Execute(ctx context.Context, call ToolCall) string {
	if !t.cfg.ConfirmDestructive {
		if msg := destructiveGuard(call); msg != "" {
			return msg
		}
	}
	switch call.Tool {
	case "query_clickhouse":
		return t.QueryClickHouse(ctx, call.SQL, call.Database)
	case "query_mysql":
		return t.QueryMySQL(ctx, call.SQL)
	case "query_postgres":
		return t.QueryPostgres(ctx, call.SQL, call.Database)
	case "query_redis":
		return t.QueryRedis(ctx, call.Command)
	case "show_tables":
		return t.ShowTables(ctx, call.DBType, call.Database)
	case "describe_table":
		return t.DescribeTable(ctx, call.DBType, call.Table, call.Database)
	case "count_rows":
		return t.CountRows(ctx, call.DBType, call.Table, call.Database, call.Where)
	default:
		return fmt.Sprintf("ERROR: unknown tool %q", call.Tool)
	}
}

func (t *Tools) QueryClickHouse(ctx context.Context, sql string, database string) string {
	if strings.TrimSpace(sql) == "" {
		return "ERROR: query_clickhouse requires non-empty sql"
	}
	container := t.cfg.Containers.ClickHouse
	if container == "" {
		return "ERROR: no clickhouse container configured/detected"
	}
	cred := t.cfg.Credentials
	db := nonEmpty(database, cred.ClickHouseDB, "default")
	user := nonEmpty(cred.ClickHouseUser, "default")

	args := []string{"exec", "-i", container, "clickhouse-client", "-u", user}
	if cred.ClickHousePassword != "" {
		args = append(args, "--password", cred.ClickHousePassword)
	}
	args = append(args, "-d", db, "--query", sql, "--format", "PrettyCompact")
	return t.run(ctx, "docker", args...)
}

func (t *Tools) QueryMySQL(ctx context.Context, sql string) string {
	if strings.TrimSpace(sql) == "" {
		return "ERROR: query_mysql requires non-empty sql"
	}
	container := t.cfg.Containers.MySQL
	if container == "" {
		return "ERROR: no mysql container configured/detected"
	}
	cred := t.cfg.Credentials
	user := nonEmpty(cred.MySQLUser, "root")

	args := []string{"exec", "-i", container, "mysql", "-u" + user}
	if cred.MySQLPassword != "" {
		args = append(args, "-p"+cred.MySQLPassword)
	}
	if cred.MySQLDB != "" {
		args = append(args, cred.MySQLDB)
	}
	args = append(args, "-e", sql)
	return t.run(ctx, "docker", args...)
}

func (t *Tools) QueryPostgres(ctx context.Context, sql string, database string) string {
	if strings.TrimSpace(sql) == "" {
		return "ERROR: query_postgres requires non-empty sql"
	}
	container := t.cfg.Containers.Postgres
	if container == "" {
		return "ERROR: no postgres container configured/detected"
	}
	cred := t.cfg.Credentials
	user := nonEmpty(cred.PostgresUser, "postgres")
	db := nonEmpty(database, cred.PostgresDB, user)

	args := []string{"exec", "-i"}
	if cred.PostgresPassword != "" {
		args = append(args, "-e", "PGPASSWORD="+cred.PostgresPassword)
	}
	args = append(args, container, "psql", "-U", user, "-d", db,
		"--no-align", "--field-separator=\t", "-c", sql)
	return t.run(ctx, "docker", args...)
}

func (t *Tools) QueryRedis(ctx context.Context, command string) string {
	if strings.TrimSpace(command) == "" {
		return "ERROR: query_redis requires non-empty command"
	}
	container := t.cfg.Containers.Redis
	if container == "" {
		return "ERROR: no redis container configured/detected"
	}
	db := nonEmpty(t.cfg.Credentials.RedisDB, "0")
	args := []string{"exec", "-i", container, "redis-cli", "-n", db}
	args = append(args, strings.Fields(command)...)
	return t.run(ctx, "docker", args...)
}

func (t *Tools) ShowTables(ctx context.Context, dbType string, database string) string {
	switch strings.ToLower(dbType) {
	case "clickhouse":
		return t.QueryClickHouse(ctx, "SHOW TABLES", database)
	case "mysql":
		return t.QueryMySQL(ctx, "SHOW TABLES")
	case "postgres":
		return t.QueryPostgres(ctx, "SELECT table_schema||'.'||table_name FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema') ORDER BY 1", database)
	default:
		return fmt.Sprintf("ERROR: show_tables db_type must be clickhouse|mysql|postgres, got %q", dbType)
	}
}

func (t *Tools) DescribeTable(ctx context.Context, dbType string, table string, database string) string {
	if strings.TrimSpace(table) == "" {
		return "ERROR: describe_table requires table"
	}
	if !safeIdent(table) {
		return fmt.Sprintf("ERROR: unsafe table name %q", table)
	}
	switch strings.ToLower(dbType) {
	case "clickhouse":
		return t.QueryClickHouse(ctx, "DESCRIBE TABLE "+table, database)
	case "mysql":
		return t.QueryMySQL(ctx, "DESCRIBE "+table)
	case "postgres":
		// table may be schema-qualified (schema.name); information_schema.columns
		// matches on the bare table name.
		return t.QueryPostgres(ctx, "SELECT column_name, data_type FROM information_schema.columns WHERE table_name = '"+stripSchema(table)+"' ORDER BY ordinal_position", database)
	default:
		return fmt.Sprintf("ERROR: describe_table db_type must be clickhouse|mysql|postgres, got %q", dbType)
	}
}

func (t *Tools) CountRows(ctx context.Context, dbType string, table string, database string, where string) string {
	if strings.TrimSpace(table) == "" {
		return "ERROR: count_rows requires table"
	}
	if !safeIdent(table) {
		return fmt.Sprintf("ERROR: unsafe table name %q", table)
	}
	sql := "SELECT COUNT(*) FROM " + table
	if strings.TrimSpace(where) != "" {
		sql += " WHERE " + where
	}
	switch strings.ToLower(dbType) {
	case "clickhouse":
		return t.QueryClickHouse(ctx, sql, database)
	case "mysql":
		return t.QueryMySQL(ctx, sql)
	case "postgres":
		return t.QueryPostgres(ctx, sql, database)
	default:
		return fmt.Sprintf("ERROR: count_rows db_type must be clickhouse|mysql|postgres, got %q", dbType)
	}
}

// ResolveDatabase returns the effective database name a call targets, used by
// the agent's self-learning to record fully-qualified table names.
func (t *Tools) ResolveDatabase(dbType, database string) string {
	switch strings.ToLower(dbType) {
	case "clickhouse":
		return nonEmpty(database, t.cfg.Credentials.ClickHouseDB, "default")
	case "mysql":
		return nonEmpty(database, t.cfg.Credentials.MySQLDB, "default")
	case "postgres":
		return nonEmpty(database, t.cfg.Credentials.PostgresDB, "postgres")
	default:
		return nonEmpty(database, "default")
	}
}

func (t *Tools) run(ctx context.Context, name string, args ...string) string {
	cctx, cancel := context.WithTimeout(ctx, time.Duration(t.cfg.ToolTimeoutSeconds)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	errOut := strings.TrimSpace(stderr.String())

	if cctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("ERROR: command timed out after %ds\n%s", t.cfg.ToolTimeoutSeconds, out)
	}
	if err != nil {
		msg := errOut
		if msg == "" {
			msg = err.Error()
		}
		if out != "" {
			return fmt.Sprintf("ERROR: %s\n%s", msg, out)
		}
		return "ERROR: " + msg
	}
	if out == "" {
		if errOut != "" {
			return errOut
		}
		return "(no rows)"
	}
	return out
}

// safeIdent allows only identifiers/qualified names for places where the table
// name is interpolated directly (DESCRIBE / COUNT). Free-form SQL goes through
// query_* unchanged — those run as the configured DB user's privileges.
// sqlDestructiveVerbs are statement-leading SQL verbs blocked in read-only mode.
var sqlDestructiveVerbs = map[string]bool{
	"DELETE": true, "UPDATE": true, "DROP": true, "INSERT": true, "TRUNCATE": true,
	"ALTER": true, "CREATE": true, "GRANT": true, "REVOKE": true, "REPLACE": true,
	"MERGE": true, "RENAME": true, "SHUTDOWN": true, "KILL": true, "OPTIMIZE": true,
	"ATTACH": true, "DETACH": true, "LOAD": true,
}

// redisDestructiveCmds are redis first-word commands blocked in read-only mode
// (writes, deletes, and control ops). Read commands (GET/KEYS/SCAN/HGETALL/etc)
// are allowed by not being listed.
var redisDestructiveCmds = map[string]bool{
	"DEL": true, "UNLINK": true, "FLUSHALL": true, "FLUSHDB": true,
	"RENAME": true, "RENAMENX": true, "MOVE": true, "MIGRATE": true,
	"RESTORE": true, "RESTORE-ASKING": true, "CONFIG": true, "SHUTDOWN": true,
	"SWAPDB": true, "SCRIPT": true, "SET": true, "SETEX": true, "SETNX": true,
	"PSETEX": true, "MSET": true, "MSETNX": true, "GETSET": true, "GETDEL": true,
	"APPEND": true, "SETRANGE": true, "HSET": true, "HMSET": true, "HSETNX": true,
	"HDEL": true, "HINCRBY": true, "HINCRBYFLOAT": true, "LPUSH": true, "RPUSH": true,
	"LPUSHX": true, "RPUSHX": true, "LSET": true, "LREM": true, "LPOP": true, "RPOP": true,
	"LINSERT": true, "LTRIM": true, "RPOPLPUSH": true, "LMOVE": true,
	"SADD": true, "SREM": true, "SMOVE": true, "SPOP": true, "SINTERSTORE": true,
	"SUNIONSTORE": true, "SDIFFSTORE": true, "ZADD": true, "ZREM": true,
	"ZPOPMIN": true, "ZPOPMAX": true, "ZINCRBY": true, "ZREMRANGEBYRANK": true,
	"ZREMRANGEBYSCORE": true, "ZUNIONSTORE": true, "ZINTERSTORE": true,
	"INCR": true, "INCRBY": true, "DECR": true, "DECRBY": true, "INCRBYFLOAT": true,
	"EXPIRE": true, "PEXPIRE": true, "EXPIREAT": true, "PEXPIREAT": true,
	"PERSIST": true, "SETBIT": true, "BITFIELD": true, "GEOADD": true,
	"PFADD": true, "XADD": true, "XDEL": true, "XTRIM": true, "BITOP": true,
}

// destructiveGuard returns a non-empty error message if the call would perform
// a destructive op while in read-only mode. Returns "" if the call is allowed.
func destructiveGuard(call ToolCall) string {
	switch call.Tool {
	case "query_clickhouse", "query_mysql", "query_postgres":
		if sqlHasDestructive(call.SQL) {
			return "ERROR: destructive SQL rejected (read-only mode). To allow DELETE/UPDATE/DROP/INSERT/TRUNCATE/ALTER/CREATE/GRANT/REVOKE/REPLACE/MERGE/RENAME/OPTIMIZE, re-run agent-db with --confirm-destructive."
		}
	case "query_redis":
		first := firstRedisCmd(call.Command)
		if first != "" && redisDestructiveCmds[first] {
			return "ERROR: destructive Redis command " + first + " rejected (read-only mode). To allow writes/deletes, re-run agent-db with --confirm-destructive."
		}
	}
	return ""
}

// sqlHasDestructive reports whether any statement (split on ';') begins with a
// destructive verb, after stripping leading whitespace, '(' and SQL comments.
func sqlHasDestructive(sql string) bool {
	for _, stmt := range strings.Split(sql, ";") {
		s := stripSQLLead(stmt)
		if s == "" {
			continue
		}
		if verb := firstToken(s); verb != "" && sqlDestructiveVerbs[verb] {
			return true
		}
	}
	return false
}

// stripSQLLead trims leading whitespace, '(' and SQL line/block comments.
func stripSQLLead(s string) string {
	s = strings.TrimSpace(s)
	for {
		if strings.HasPrefix(s, "--") {
			if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = strings.TrimSpace(s[i+1:])
			continue
			}
			return ""
		}
		if strings.HasPrefix(s, "/*") {
			if i := strings.Index(s, "*/"); i >= 0 {
			s = strings.TrimSpace(s[i+2:])
			continue
			}
			return ""
		}
		if strings.HasPrefix(s, "(") {
			s = strings.TrimSpace(s[1:])
			continue
		}
		break
	}
	return s
}

// firstToken returns the uppercased first whitespace-delimited token of s.
func firstToken(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToUpper(fields[0])
}

// firstRedisCmd returns the uppercased first token of a redis command string.
func firstRedisCmd(cmd string) string {
	fields := strings.Fields(strings.TrimSpace(cmd))
	if len(fields) == 0 {
		return ""
	}
	return strings.ToUpper(fields[0])
}

func safeIdent(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '.' || r == '`':
		default:
			return false
		}
	}
	return true
}

// stripSchema returns the part after the last "." (the bare table name) when the
// identifier is schema-qualified, else the whole string.
func stripSchema(s string) string {
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return s
}

func nonEmpty(items ...string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return ""
}
