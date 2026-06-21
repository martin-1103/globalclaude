package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type claudeContextToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func SemanticSearch(ctx context.Context, repo string, command string, query string, timeoutSeconds int, limit int) ([]Hit, error) {
	result, err := runClaudeContextTool(ctx, command, "search", repo, query, timeoutSeconds, limit, false)
	if err != nil {
		return nil, err
	}
	if result.IsError {
		msg := firstText(result)
		if isNotIndexedText(msg) || isLostCollectionText(msg) {
			force := isLostCollectionText(msg)
			if _, idxErr := runClaudeContextTool(ctx, command, "index-wait", repo, "", timeoutSeconds, limit, force); idxErr != nil {
				return nil, fmt.Errorf("semantic index trigger failed: %w", idxErr)
			}
			if force {
				return nil, fmt.Errorf("semantic reindex started for %s; rerun after indexing", repo)
			}
			return nil, fmt.Errorf("semantic index started for %s; rerun after indexing", repo)
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return parseClaudeContextText(repo, result), nil
}

func runClaudeContextTool(ctx context.Context, command string, op string, repo string, query string, timeoutSeconds int, limit int, force bool) (claudeContextToolResult, error) {
	runner, err := claudeContextRunnerPath()
	if err != nil {
		return claudeContextToolResult{}, err
	}
	shellCommand, err := buildClaudeContextRunnerCommand(command, runner, op, repo, query, limit, force)
	if err != nil {
		return claudeContextToolResult{}, err
	}
	output, stderr, err := runCommand(ctx, timeoutSeconds, "", "bash", "-lc", shellCommand)
	if err != nil {
		return claudeContextToolResult{}, fmt.Errorf("semantic runner failed: %w stderr=%q", err, trimForError(stderr))
	}
	var result claudeContextToolResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &result); err != nil {
		return claudeContextToolResult{}, fmt.Errorf("parse semantic result: %w output=%q", err, trimForError(output))
	}
	return result, nil
}

func claudeContextRunnerPath() (string, error) {
	const runner = "/var/pile/agent-explorer/scripts/claude_context_runner.mjs"
	if _, err := os.Stat(runner); err != nil {
		return "", fmt.Errorf("claude-context runner missing: %s", runner)
	}
	return runner, nil
}

func buildClaudeContextRunnerCommand(command string, runner string, op string, repo string, query string, limit int, force bool) (string, error) {
	prefix := strings.TrimSpace(command)
	if prefix == "" {
		return "", fmt.Errorf("claude-context command empty")
	}
	re := regexp.MustCompile(`\s+\S*claude-context-mcp(?:\s.*)?$`)
	if !re.MatchString(prefix) {
		return "", fmt.Errorf("claude-context command missing claude-context-mcp binary")
	}
	prefix = strings.TrimSpace(re.ReplaceAllString(prefix, ""))
	if prefix == "" {
		return "", fmt.Errorf("claude-context env prefix empty")
	}
	return fmt.Sprintf("%s node %s %s %s %s %d %t",
		prefix,
		shellQuote(runner),
		shellQuote(op),
		shellQuote(repo),
		shellQuote(query),
		limit,
		force,
	), nil
}

func parseClaudeContextText(repo string, result claudeContextToolResult) []Hit {
	text := firstText(result)
	re := regexp.MustCompile(`(?m)^\s*\d+\.\s+Code snippet.*\n\s*Location:\s+(.+):(\d+)-(\d+)`)
	matches := re.FindAllStringSubmatch(text, -1)
	hits := make([]Hit, 0, len(matches))
	for _, m := range matches {
		start, _ := strconv.Atoi(m[2])
		end, _ := strconv.Atoi(m[3])
		hits = append(hits, Hit{
			Source:    "claude-context/search_code",
			File:      joinRepo(repo, m[1]),
			LineStart: start,
			LineEnd:   end,
			Why:       "semantic search",
			Family:    "semantic",
		})
	}
	return hits
}

func firstText(result claudeContextToolResult) string {
	for _, item := range result.Content {
		if item.Text != "" {
			return item.Text
		}
	}
	return ""
}

func isNotIndexedText(text string) bool {
	text = strings.ToLower(text)
	return strings.Contains(text, "not indexed") || strings.Contains(text, "index it first")
}

func isLostCollectionText(text string) bool {
	text = strings.ToLower(text)
	return strings.Contains(text, "collection not found") || (strings.Contains(text, "index data") && strings.Contains(text, "lost"))
}

func trimForError(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= 400 {
		return text
	}
	return text[len(text)-400:]
}

func shellQuote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}
