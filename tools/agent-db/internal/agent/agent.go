package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"agent-db/internal/config"
	"agent-db/internal/db"
	"agent-db/internal/llm"
	"agent-db/internal/schema"
)

type Agent struct {
	cfg    config.Config
	client *llm.Client
	tools  *db.Tools
	schema *schema.Store
}

func New(cfg config.Config, client *llm.Client, tools *db.Tools) *Agent {
	a := &Agent{cfg: cfg, client: client, tools: tools}
	if strings.TrimSpace(cfg.SchemaDir) != "" {
		store, err := schema.NewStore(cfg.SchemaDir)
		if err == nil {
			a.schema = store
		}
	}
	return a
}

// SchemaStore returns the agent's schema store, or nil if not available.
func (a *Agent) SchemaStore() *schema.Store {
	return a.schema
}

// Step records one turn of the loop for output formatting.
type Step struct {
	Intent  string
	Command string
	Result  string
}

// Result is the outcome of a full agentic run.
type Result struct {
	Answer   string
	Steps    []Step
	Turns    int
	MaxTurns bool // true if loop stopped at max_turns without {"done": true}
}

func (a *Agent) Run(ctx context.Context, query string) (Result, error) {
	messages := []llm.Message{
		{Role: "system", Content: a.systemPrompt(query)},
		{Role: "user", Content: query},
	}

	var res Result
	for turn := 0; turn < a.cfg.MaxTurns; turn++ {
		res.Turns = turn + 1
		reply, err := a.client.Chat(ctx, messages)
		if err != nil {
			return res, fmt.Errorf("llm chat turn %d: %w", turn+1, err)
		}
		messages = append(messages, llm.Message{Role: "assistant", Content: reply})

		calls, raw, found := parseToolCalls(reply)
		if !found {
			// No tool call and no done marker: treat the reply as the answer.
			res.Answer = strings.TrimSpace(reply)
			return res, nil
		}

		// A done marker anywhere in the batch ends the loop.
		for _, c := range calls {
			if c.Done {
				res.Answer = strings.TrimSpace(c.Answer)
				return res, nil
			}
		}

		intent := firstReasoningLine(reply)

		// Execute all calls in parallel, preserving order.
		results := make([]string, len(calls))
		var wg sync.WaitGroup
		for i := range calls {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				out := a.tools.Execute(ctx, calls[i])
				// Schema discovery tools must not be trimmed — truncating columns
				// prevents learn() from seeing full schema and forces re-discovery.
				if calls[i].Tool == "describe_table" || calls[i].Tool == "show_tables" {
					results[i] = out
				} else {
					results[i] = trimRows(out, a.cfg.MaxDisplayRows)
				}
			}(i)
		}
		wg.Wait()

		// Self-learning runs after execution, sequentially, to avoid concurrent
		// writes to context.md.
		for i := range calls {
			a.learn(calls[i], results[i])
		}

		var combined strings.Builder
		for i := range calls {
			if i > 0 {
				combined.WriteString("\n")
			}
			combined.WriteString(fmt.Sprintf("[%s]\n%s\n", calls[i].Tool, results[i]))
		}
		out := strings.TrimRight(combined.String(), "\n")

		res.Steps = append(res.Steps, Step{
			Intent:  intent,
			Command: raw,
			Result:  out,
		})

		messages = append(messages, llm.Message{
			Role:    "user",
			Content: "TOOL RESULT:\n" + out,
		})
	}

	res.MaxTurns = true
	res.Answer = "Reached max_turns without a final answer. Last steps shown above."
	return res, nil
}

// parseToolCalls extracts tool calls from the LLM reply. It finds every
// balanced JSON value ({...} or [...]) anywhere in the text — single-line,
// multiline, fenced or not — and parses each as a tool call or array of tool
// calls. Returns the parsed calls, the raw matched JSON, and whether any found.
func parseToolCalls(reply string) ([]db.ToolCall, string, bool) {
	for _, block := range extractJSONBlocks(reply) {
		if strings.HasPrefix(block, "[") {
			var calls []db.ToolCall
			if err := json.Unmarshal([]byte(block), &calls); err != nil {
				continue
			}
			var valid []db.ToolCall
			for _, c := range calls {
				if c.Done || c.Tool != "" {
					valid = append(valid, c)
				}
			}
			if len(valid) > 0 {
				return valid, block, true
			}
			continue
		}
		var call db.ToolCall
		if err := json.Unmarshal([]byte(block), &call); err != nil {
			continue
		}
		if call.Done || call.Tool != "" {
			return []db.ToolCall{call}, block, true
		}
	}
	return nil, "", false
}

// extractJSONBlocks returns every top-level balanced { } or [ ] substring in s,
// in order. It tracks string literals (and their escapes) so braces inside
// strings do not affect depth. Handles fenced and multiline JSON uniformly.
func extractJSONBlocks(s string) []string {
	var blocks []string
	runes := []rune(s)
	i := 0
	n := len(runes)
	for i < n {
		c := runes[i]
		if c == '{' || c == '[' {
			openCh := c
			closeCh := '}'
			if openCh == '[' {
				closeCh = ']'
			}
			depth := 0
			inStr := false
			esc := false
			start := i
			j := i
			for j < n {
				ch := runes[j]
				if inStr {
					if esc {
						esc = false
					} else if ch == '\\' {
						esc = true
					} else if ch == '"' {
						inStr = false
					}
					j++
					continue
				}
				switch ch {
				case '"':
					inStr = true
				case openCh:
					depth++
				case closeCh:
					depth--
				}
				j++
				if depth == 0 {
					break
				}
			}
			if depth == 0 {
				blocks = append(blocks, strings.TrimSpace(string(runes[start:j])))
				i = j
				continue
			}
			// unbalanced — stop scanning this candidate
		}
		i++
	}
	return blocks
}

// learn inspects a show_tables / describe_table result and updates the JSON
// schema index. Silent and best-effort: failures are ignored so it never
// disrupts the agent. Results starting with ERROR are skipped.
func (a *Agent) learn(call db.ToolCall, result string) {
	if a.schema == nil {
		return
	}
	if strings.HasPrefix(strings.TrimSpace(result), "ERROR") {
		return
	}

	database := a.tools.ResolveDatabase(call.DBType, call.Database)

	switch call.Tool {
	case "show_tables":
		tables := parseCells(result, 0)
		_ = a.schema.DiscoverDB(database, call.DBType, tables)

	case "describe_table":
		table := strings.TrimSpace(call.Table)
		names := parseCells(result, 0)
		_ = a.schema.UpdateTable(database, call.DBType, table, names)

	default:
		return
	}
}

// parseCells extracts the value at column index col from each data row of a
// ClickHouse PrettyCompact or MySQL table-formatted result. Header, separator,
// and border lines are skipped. Returns the de-boxed, trimmed cell values.
func parseCells(out string, col int) []string {
	var cells []string
	seenHeader := false
	for _, line := range strings.Split(out, "\n") {
		raw := line
		// Skip pure border lines (box-drawing or +---+).
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if isBorderLine(trimmed) || hasBoxCorner(trimmed) {
			continue
		}
		// Skip psql row-count footer, e.g. "(25 rows)" / "(1 row)".
		if isRowCountFooter(trimmed) {
			continue
		}
		// Split on cell separators: │ (PrettyCompact), | (MySQL), or tab (psql --no-align).
		var parts []string
		if strings.ContainsAny(raw, "│|\t") {
			parts = strings.FieldsFunc(raw, func(r rune) bool {
				return r == '│' || r == '|' || r == '\t'
			})
		} else {
			// PrettyCompact single-column rows may have no separators; treat the
			// whole stripped line as one cell.
			parts = []string{raw}
		}
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		// Drop empty leading/trailing fields produced by edge separators.
		var fields []string
		for _, p := range parts {
			if p != "" {
				fields = append(fields, p)
			}
		}
		if len(fields) == 0 {
			continue
		}
		// MySQL repeats a header row (e.g. "Tables_in_db" / "Field"); skip the
		// first content row for describe-style multi-column output.
		if !seenHeader && isHeaderRow(fields) {
			seenHeader = true
			continue
		}
		if col < len(fields) {
			cells = append(cells, fields[col])
		}
	}
	return cells
}

// isBorderLine reports whether a line is only box-drawing / +-=| characters.
func isBorderLine(s string) bool {
	for _, r := range s {
		switch r {
		case '─', '┌', '┐', '└', '┘', '├', '┤', '┬', '┴', '┼', '│',
			'+', '-', '=', '|', ' ':
		default:
			return false
		}
	}
	return true
}

// hasBoxCorner reports whether a line contains any box-drawing corner/junction,
// which marks a ClickHouse PrettyCompact border (the column header is embedded in
// the top border, e.g. "┌─name─┐", so such lines must be skipped as non-data).
func hasBoxCorner(s string) bool {
	return strings.ContainsAny(s, "┌┐└┘├┤┬┴┼")
}

// isRowCountFooter recognizes the psql output footer "(N row)" / "(N rows)".
func isRowCountFooter(s string) bool {
	if !strings.HasPrefix(s, "(") || !strings.HasSuffix(s, ")") {
		return false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(s, "("), ")")
	inner = strings.TrimSpace(inner)
	inner = strings.TrimSuffix(inner, " rows")
	inner = strings.TrimSuffix(inner, " row")
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return false
	}
	for _, r := range inner {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isHeaderRow recognizes common MySQL header rows for show/describe output.
func isHeaderRow(fields []string) bool {
	if len(fields) == 0 {
		return false
	}
	f0 := strings.ToLower(fields[0])
	if strings.HasPrefix(f0, "tables_in_") {
		return true
	}
	// DESCRIBE header: Field Type Null Key Default Extra
	if f0 == "field" {
		return true
	}
	// Postgres describe header: column_name data_type
	if f0 == "column_name" {
		return true
	}
	return false
}

// safeName allows simple identifiers for recorded table names.
func safeName(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

// firstReasoningLine returns the first non-JSON, non-empty line as the intent.
func firstReasoningLine(reply string) string {
	for _, line := range strings.Split(reply, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "{") || strings.HasPrefix(line, "```") {
			continue
		}
		return line
	}
	return ""
}

// trimRows caps a tool result at maxRows data lines to protect context.
func trimRows(out string, maxRows int) string {
	if maxRows <= 0 {
		return out
	}
	lines := strings.Split(out, "\n")
	if len(lines) <= maxRows {
		return out
	}
	kept := lines[:maxRows]
	return strings.Join(kept, "\n") + fmt.Sprintf("\n... (%d more rows trimmed)", len(lines)-maxRows)
}

func (a *Agent) systemPrompt(query string) string {
	notes := ""
	if strings.TrimSpace(a.cfg.Notes) != "" {
		notes = "\n\nPROJECT NOTES:\n" + a.cfg.Notes
	}

	avail := a.availableDBs()

	// Inject schema from JSON index with relevance filtering.
	schemaBlock := ""
	if a.schema != nil {
		if s, err := a.schema.InjectRelevant(query); err == nil && s != "" {
			schemaBlock = s
		}
	}

	return fmt.Sprintf(`You are agent-db, a database query agent. Answer the user's question by querying real databases through tools.

AVAILABLE DATABASES: %s

TOOLS — emit ONE JSON object or an array [...] for parallel calls:
  Single:   {"tool": "query_clickhouse", "sql": "...", "database": "optional"}
  Parallel: [{"tool": "count_rows", ...}, {"tool": "count_rows", "table": "other_table", ...}]

Tool shapes:
  {"tool": "query_clickhouse", "sql": "SELECT ...", "database": "optional"}
  {"tool": "query_mysql", "sql": "SELECT ..."}
  {"tool": "query_postgres", "sql": "SELECT ...", "database": "optional"}
  {"tool": "query_redis", "command": "KEYS pattern*"}
  {"tool": "show_tables", "db_type": "clickhouse|mysql|postgres", "database": "optional"}
  {"tool": "describe_table", "db_type": "clickhouse|mysql|postgres", "table": "name", "database": "optional"}
  {"tool": "count_rows", "db_type": "clickhouse|mysql|postgres", "table": "name", "database": "optional", "where": "optional"}

When finished, emit exactly:
  {"done": true, "answer": "..."}

RULES:
- Plan first. State your intent in one short line BEFORE the tool JSON.
- Discover schema before guessing: use show_tables / describe_table when unsure.
- If PROJECT SCHEMA below already lists the tables/columns you need, SKIP discovery.
- ALWAYS count_rows before any SELECT * — never SELECT * a large table blind.
- Default LIMIT %d on SELECTs. Never return unbounded result sets.
- Emit independent calls as one array to run them in parallel; chain dependent calls across turns.
- You have at most %d turns. Be efficient.

GROUNDING (answer rules):
- Report numbers WITH their window, unit, and scope (e.g. "1,204 failed orders, today UTC, status=failed").
- Compare only same-window data. Do not compare a 7-day count to a 1-day count.
- Mark inference explicitly ("inferred:", "assuming:"). Do not present a guess as observed.
- If a query errors or data is missing, say so loudly — do not fabricate a number.
- Final answer max 200 words.%s%s`,
		avail, a.cfg.QueryLimit, a.cfg.MaxTurns, notes, schemaBlock)
}

func (a *Agent) availableDBs() string {
	var avail []string
	if a.cfg.Containers.ClickHouse != "" {
		avail = append(avail, "clickhouse(container="+a.cfg.Containers.ClickHouse+")")
	}
	if a.cfg.Containers.MySQL != "" {
		avail = append(avail, "mysql(container="+a.cfg.Containers.MySQL+")")
	}
	if a.cfg.Containers.Redis != "" {
		avail = append(avail, "redis(container="+a.cfg.Containers.Redis+")")
	}
	if a.cfg.Containers.Postgres != "" {
		avail = append(avail, "postgres(container="+a.cfg.Containers.Postgres+")")
	}
	if len(avail) == 0 {
		return "NONE DETECTED — no containers configured; tool calls will error"
	}
	return strings.Join(avail, ", ")
}
