package logs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"agent-log/internal/config"
)

type ToolCall struct {
	Tool      string `json:"tool"`
	Query     string `json:"query"`
	Start     string `json:"start"`
	Limit     int    `json:"limit"`
	Pattern   string `json:"pattern"`
	Container string `json:"container"`
	Window    string `json:"window"`
	Mode      string `json:"mode"`
	Done      bool   `json:"done"`
	Answer    string `json:"answer"`
}

type Tools struct {
	cfg config.Config
}

func New(cfg config.Config) *Tools {
	return &Tools{cfg: cfg}
}

func (t *Tools) Execute(ctx context.Context, call ToolCall) string {
	switch call.Tool {
	case "query_vlogs":
		return t.QueryVLogs(ctx, call.Query, call.Start, call.Limit)
	case "list_containers":
		return t.ListContainers(ctx, call.Pattern)
	case "container_logs":
		return t.ContainerLogs(ctx, call.Container, call.Window, call.Mode)
	default:
		return fmt.Sprintf("ERROR: unknown tool %q", call.Tool)
	}
}

func (t *Tools) QueryVLogs(ctx context.Context, query, start string, limit int) string {
	if strings.TrimSpace(query) == "" {
		return "ERROR: query_vlogs requires non-empty query"
	}
	if start == "" {
		start = "1h"
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	endpoint := strings.TrimRight(t.cfg.VLogsURL, "/") + "/select/logsql/query"
	form := url.Values{
		"query": {query},
		"start": {start},
		"limit": {fmt.Sprintf("%d", limit)},
	}

	cctx, cancel := context.WithTimeout(ctx, time.Duration(t.cfg.ToolTimeoutSeconds)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Sprintf("ERROR: build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Sprintf("ERROR: vlogs request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Sprintf("ERROR: vlogs status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Sprintf("ERROR: read vlogs response: %v", err)
	}

	return formatVLogsResponse(string(body), t.cfg.MaxDisplayLines)
}

func formatVLogsResponse(raw string, maxLines int) string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return "(no results)"
	}

	var out []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Parse minimal fields from JSON line
		ts := jsonField(line, "_time")
		svc := jsonField(line, "service")
		lvl := jsonField(line, "level")
		msg := jsonField(line, "_msg")
		logField := jsonField(line, "log")

		formatted := fmt.Sprintf("[%s] [%s] [%s] %s", ts, svc, lvl, msg)
		if logField != "" && logField != msg {
			if len(logField) > 200 {
				logField = logField[:200] + "..."
			}
			formatted += "\n  " + logField
		}
		out = append(out, formatted)
	}

	total := len(out)
	if total > maxLines {
		out = out[:maxLines]
	}

	result := strings.Join(out, "\n")
	if total > maxLines {
		result += fmt.Sprintf("\n... (%d more lines)", total-maxLines)
	}
	return result
}

// jsonField extracts a string field from a JSON object using simple string search.
// Handles \", \\, \n, \t escape sequences. Not a full JSON parser.
func jsonField(js, key string) string {
	needle := `"` + key + `":"`
	i := strings.Index(js, needle)
	if i < 0 {
		return ""
	}
	rest := js[i+len(needle):]
	var b strings.Builder
	escaped := false
	for _, r := range rest {
		if escaped {
			switch r {
			case '"':
				b.WriteRune('"')
			case '\\':
				b.WriteRune('\\')
			case 'n':
				b.WriteRune('\n')
			case 't':
				b.WriteRune('\t')
			default:
				b.WriteRune(r)
			}
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (t *Tools) ListContainers(ctx context.Context, pattern string) string {
	args := []string{t.cfg.GasslogPath, "list"}
	if strings.TrimSpace(pattern) != "" {
		args = append(args, pattern)
	}
	return t.runGasslog(ctx, args)
}

func (t *Tools) ContainerLogs(ctx context.Context, container, window, mode string) string {
	if strings.TrimSpace(container) == "" {
		return "ERROR: container_logs requires non-empty container"
	}
	if window == "" {
		window = "5m"
	}
	args := []string{t.cfg.GasslogPath, "logs", container, window}
	if mode != "" {
		args = append(args, mode)
	}
	return t.runGasslog(ctx, args)
}

func (t *Tools) runGasslog(ctx context.Context, args []string) string {
	cctx, cancel := context.WithTimeout(ctx, time.Duration(t.cfg.ToolTimeoutSeconds)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, "bash", args...)
	out, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(out))

	if err != nil {
		if cmd.ProcessState != nil {
			switch cmd.ProcessState.ExitCode() {
			case 3:
				return "no containers matched pattern"
			case 4:
				return result // ambiguous — show options to LLM
			case 5:
				return "ERROR: docker unavailable or timed out"
			}
		}
		if result != "" {
			return result
		}
		return fmt.Sprintf("ERROR: gasslog: %v", err)
	}
	return result
}
