package tools

import (
	"path/filepath"
)

func joinRepo(repo string, file string) string {
	if filepath.IsAbs(file) {
		return file
	}
	return filepath.Join(repo, file)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func nonEmpty(items ...string) string {
	for _, item := range items {
		if item != "" {
			return item
		}
	}
	return ""
}
