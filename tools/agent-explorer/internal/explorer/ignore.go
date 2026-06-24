package explorer

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"agent-explorer/internal/tools"
)

func filterIgnoredHits(repo string, hits []tools.Hit) []tools.Hit {
	matchers := loadIgnoreMatchers(repo)
	filtered := make([]tools.Hit, 0, len(hits))
	for _, hit := range hits {
		if shouldIgnoreHit(repo, hit.File, matchers) {
			continue
		}
		filtered = append(filtered, hit)
	}
	return filtered
}

func shouldIgnoreHit(repo string, file string, matchers []string) bool {
	rel := file
	if relative, err := filepath.Rel(repo, file); err == nil {
		rel = relative
	}
	rel = filepath.ToSlash(rel)
	base := filepath.Base(rel)
	baseLower := strings.ToLower(base)
	parts := strings.Split(rel, "/")
	if strings.HasPrefix(baseLower, "fix_") || strings.HasPrefix(baseLower, "tmp") || strings.HasPrefix(baseLower, "scratch") || strings.HasPrefix(baseLower, "debug_") {
		return true
	}
	for _, part := range parts {
		switch part {
		case "node_modules", "site-packages", ".venv", "__pycache__", ".git":
			return true
		}
	}
	if strings.HasSuffix(base, "_pb2.py") || strings.HasSuffix(base, "_pb2_grpc.py") {
		return true
	}
	for _, pattern := range matchers {
		if ignorePatternMatch(rel, pattern) {
			return true
		}
	}
	return false
}

func loadIgnoreMatchers(repo string) []string {
	path := filepath.Join(repo, ".cbmignore")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	matchers := []string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		matchers = append(matchers, filepath.ToSlash(line))
	}
	return matchers
}

func ignorePatternMatch(rel string, pattern string) bool {
	pattern = strings.TrimSpace(filepath.ToSlash(pattern))
	if pattern == "" {
		return false
	}

	candidates := []string{pattern}
	if strings.HasPrefix(pattern, "**/") {
		candidates = append(candidates, strings.TrimPrefix(pattern, "**/"))
	}
	if strings.HasSuffix(pattern, "/**") {
		trimmed := strings.TrimSuffix(pattern, "/**")
		if rel == trimmed || strings.HasPrefix(rel, trimmed+"/") {
			return true
		}
	}
	for _, candidate := range candidates {
		if ok, err := filepath.Match(candidate, rel); err == nil && ok {
			return true
		}
		if strings.Contains(candidate, "/") {
			continue
		}
		if ok, err := filepath.Match(candidate, filepath.Base(rel)); err == nil && ok {
			return true
		}
	}
	return false
}
