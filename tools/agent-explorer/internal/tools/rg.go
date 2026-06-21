package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

func RG(ctx context.Context, repo string, term string, timeoutSeconds int, limit int) ([]Hit, error) {
	return rgWithArgs(ctx, repo, term, timeoutSeconds, limit)
}

func RGCode(ctx context.Context, repo string, term string, timeoutSeconds int, limit int) ([]Hit, error) {
	args := []string{
		"-n", "--no-heading", "-S", "-F", "-i",
		"-g", "*.go",
		"-g", "*.py",
		"-g", "*.ts",
		"-g", "*.tsx",
		"-g", "*.js",
		"-g", "*.jsx",
		"-g", "*.rs",
		"-g", "*.java",
		"-g", "*.php",
		"-g", "*.rb",
		"-g", "!*.md",
		"-g", "!*.yml",
		"-g", "!*.yaml",
		"-g", "!*.json",
		"-g", "!*.sum",
		"-g", "!*.lock",
		"-g", "!*.old",
		term,
		".",
	}
	return rgWithArgs(ctx, repo, term, timeoutSeconds, limit, args...)
}

func rgWithArgs(ctx context.Context, repo string, term string, timeoutSeconds int, limit int, args ...string) ([]Hit, error) {
	if len(args) == 0 {
		args = []string{"-n", "--no-heading", "-S", "-F", "-i", term, "."}
	}
	out, errOut, err := runCommand(ctx, timeoutSeconds, repo, "rg", args...)
	if err != nil && strings.TrimSpace(out) == "" {
		if strings.Contains(err.Error(), "exit status 1") && strings.TrimSpace(errOut) == "" {
			return nil, nil
		}
	}
	if err != nil && out == "" {
		return nil, fmt.Errorf("rg: %w: %s", err, errOut)
	}

	lines := strings.Split(out, "\n")
	hits := make([]Hit, 0, min(limit, len(lines)))
	for _, line := range lines {
		if len(hits) >= limit || strings.TrimSpace(line) == "" {
			break
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}
		ln, convErr := strconv.Atoi(parts[1])
		if convErr != nil {
			continue
		}
		hits = append(hits, Hit{
			Source:    "rg",
			File:      joinRepo(repo, parts[0]),
			LineStart: ln,
			LineEnd:   ln,
			Snippet:   parts[2],
			Why:       "literal search match",
			Family:    "rg",
		})
	}
	return hits, nil
}
