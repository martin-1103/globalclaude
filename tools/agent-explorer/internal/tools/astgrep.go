package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type astGrepResult struct {
	File  string `json:"file"`
	Range struct {
		Start struct {
			Line int `json:"line"`
		} `json:"start"`
		End struct {
			Line int `json:"line"`
		} `json:"end"`
	} `json:"range"`
	Lines string `json:"lines"`
}

func ASTGrep(ctx context.Context, repo string, pattern string, timeoutSeconds int, limit int) ([]Hit, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, nil
	}
	out, errOut, err := runCommand(ctx, timeoutSeconds, repo, "ast-grep", "scan", "--pattern", pattern, "--json", ".")
	if err != nil && out == "" {
		return nil, fmt.Errorf("ast-grep: %w: %s", err, errOut)
	}

	lines := strings.Split(out, "\n")
	hits := make([]Hit, 0, min(limit, len(lines)))
	for _, line := range lines {
		if len(hits) >= limit || strings.TrimSpace(line) == "" {
			break
		}
		var item astGrepResult
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			continue
		}
		hits = append(hits, Hit{
			Source:    "ast-grep",
			File:      joinRepo(repo, item.File),
			LineStart: item.Range.Start.Line + 1,
			LineEnd:   item.Range.End.Line + 1,
			Snippet:   strings.TrimSpace(item.Lines),
			Why:       "structural pattern match",
			Family:    "astgrep",
		})
	}
	return hits, nil
}
