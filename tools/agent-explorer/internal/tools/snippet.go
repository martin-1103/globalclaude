package tools

import (
	"bufio"
	"fmt"
	"os"
)

func ReadSnippet(path string, startLine int, maxLines int) (string, int, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, 0, fmt.Errorf("open snippet: %w", err)
	}
	defer file.Close()

	if startLine <= 0 {
		startLine = 1
	}
	if maxLines <= 0 {
		maxLines = 20
	}

	scanner := bufio.NewScanner(file)
	current := 0
	endLine := startLine - 1
	snippet := ""
	for scanner.Scan() {
		current++
		if current < startLine {
			continue
		}
		if current >= startLine+maxLines {
			break
		}
		snippet += scanner.Text() + "\n"
		endLine = current
	}
	if err := scanner.Err(); err != nil {
		return "", 0, 0, fmt.Errorf("scan snippet: %w", err)
	}
	return snippet, startLine, endLine, nil
}
