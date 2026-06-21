package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"agent-log/internal/config"
	"agent-log/internal/llm"
	"agent-log/internal/logs"
)

type Agent struct {
	cfg    config.Config
	client *llm.Client
	tools  *logs.Tools
}

func New(cfg config.Config, client *llm.Client, tools *logs.Tools) *Agent {
	return &Agent{cfg: cfg, client: client, tools: tools}
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
	// Pre-warm: fetch the live container roster ONCE so the LLM never needs a
	// turn to list_containers just to verify a name. Best-effort — if it errors
	// (docker down), we inject nothing and the LLM can still list_containers.
	roster := a.tools.ListContainers(ctx, "")
	if strings.HasPrefix(strings.TrimSpace(roster), "ERROR") {
		roster = ""
	}

	messages := []llm.Message{
		{Role: "system", Content: a.systemPrompt(roster)},
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
				results[i] = trimLines(out, a.cfg.MaxDisplayLines)
			}(i)
		}
		wg.Wait()

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
func parseToolCalls(reply string) ([]logs.ToolCall, string, bool) {
	for _, block := range extractJSONBlocks(reply) {
		if strings.HasPrefix(block, "[") {
			var calls []logs.ToolCall
			if err := json.Unmarshal([]byte(block), &calls); err != nil {
				continue
			}
			var valid []logs.ToolCall
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
		var call logs.ToolCall
		if err := json.Unmarshal([]byte(block), &call); err != nil {
			continue
		}
		if call.Done || call.Tool != "" {
			return []logs.ToolCall{call}, block, true
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

// firstReasoningLine returns the first non-JSON, non-empty line as the intent.
func firstReasoningLine(reply string) string {
	for _, line := range strings.Split(reply, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "{") || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "```") {
			continue
		}
		return line
	}
	return ""
}

// trimLines caps a tool result at maxLines lines to protect context.
func trimLines(out string, maxLines int) string {
	if maxLines <= 0 {
		return out
	}
	lines := strings.Split(out, "\n")
	if len(lines) <= maxLines {
		return out
	}
	kept := lines[:maxLines]
	return strings.Join(kept, "\n") + fmt.Sprintf("\n... (%d more lines trimmed)", len(lines)-maxLines)
}

func (a *Agent) systemPrompt(roster string) string {
	rosterBlock := ""
	if strings.TrimSpace(roster) != "" {
		rosterBlock = "\n\nLIVE CONTAINERS (docker ps at startup — authoritative, do NOT re-list to verify):\n" + strings.TrimSpace(roster)
	}
	return fmt.Sprintf(`You are a log analysis agent. Query VictoriaLogs (structured app logs) and Docker container logs to answer questions about system behavior, errors, and incidents.

TOOLS — emit ONE JSON object or array [...] for parallel calls:
  {"tool": "query_vlogs", "query": "LogsQL", "start": "1h", "limit": 100}
  {"tool": "list_containers", "pattern": "optional"}
  {"tool": "container_logs", "container": "exact-name", "window": "5m", "mode": ""}

Tool details:
  query_vlogs   — query=LogsQL expression, start=relative (1h/30m/2h) or RFC3339, limit max 500
  list_containers — lists running docker containers, pattern filters by name substring
  container_logs  — mode="" → errors/warns only (SIGNAL), mode="ALL" → all levels, mode=<regex> → content search

LogsQL quick reference:
  service:sync-service                           filter by service
  level:error                                    filter by level
  level:(error OR warn)                          multiple values
  _msg:~"timeout.*"                              regex on message
  service:api-gateway AND level:error            combine filters
  service:api-gateway AND _msg:~"deadline"       service + message pattern

Known services in VictoriaLogs:
  sync-service, message-service, webhook-ingestion, rabbit-bridge-local,
  visitor-ingestion, report-worker-hatchet, event-processor, report-consumer,
  api-gateway, cs-event-consumer, cs-service, report-service, management-service

When finished emit:
  {"done": true, "answer": "..."}

RULES:
- Plan in one line before each tool call.
- The LIVE CONTAINERS roster below is authoritative and current. Use container_logs directly with the exact name — do NOT call list_containers to verify a name that is already in the roster.
- Prefer query_vlogs for app-level logs; use container_logs for crash markers or services not in VictoriaLogs.
- Emit independent queries as one array to run in parallel (e.g. query two services at once).
- Default start="1h" unless user specifies a different window.
- Deduplicate: collapse repeated identical errors into "X occurred N times (first: T, last: T)".
- GROUNDED answers only: mark inferences "likely:". No root cause without a quoted log line. Say "root cause unclear from logs" when unsupported.
- Final answer max 300 words.
- You have at most %d turns. Be efficient.%s`, a.cfg.MaxTurns, rosterBlock)
}
