package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

type cbmNode struct {
	FilePath      string `json:"file_path"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
	QualifiedName string `json:"qualified_name"`
	Name          string `json:"name"`
	Label         string `json:"label"`
}

type cbmSearchGraphResponse struct {
	Results []cbmNode `json:"results"`
}

type cbmSearchCodeResult struct {
	FilePath      string `json:"file_path"`
	File          string `json:"file"`
	Signature     string `json:"signature"`
	QualifiedName string `json:"qualified_name"`
	Node          string `json:"node"`
	Label         string `json:"label"`
	StartLine     int    `json:"start_line"`
	EndLine       int    `json:"end_line"`
}

type cbmSearchCodeResponse struct {
	Results []cbmSearchCodeResult `json:"results"`
}

type cbmTraceNode struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name"`
	Hop           int    `json:"hop"`
}

type cbmTraceResponse struct {
	Function  string         `json:"function"`
	Direction string         `json:"direction"`
	Mode      string         `json:"mode"`
	Callers   []cbmTraceNode `json:"callers"`
	Callees   []cbmTraceNode `json:"callees"`
}

func SearchGraph(ctx context.Context, repo string, cbmBinary string, cacheDir string, term string, timeoutSeconds int, limit int) ([]Hit, error) {
	return SearchGraphScoped(ctx, repo, cbmBinary, cacheDir, term, "", timeoutSeconds, limit)
}

func SearchGraphScoped(ctx context.Context, repo string, cbmBinary string, cacheDir string, term string, pathFilter string, timeoutSeconds int, limit int) ([]Hit, error) {
	project := projectName(repo)
	payload := fmt.Sprintf(`{"project":"%s","query":%q,"limit":%d}`, project, term, limit)
	out, errOut, err := runCommandWithEnv(ctx, timeoutSeconds, repo, []string{"CBM_CACHE_DIR=" + cacheDir}, cbmBinary, "cli", "search_graph", payload)
	if err != nil && out == "" {
		return nil, fmt.Errorf("cbm search_graph: %w: %s", err, errOut)
	}

	var resp cbmSearchGraphResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, fmt.Errorf("parse cbm search_graph: %w", err)
	}
	hits := make([]Hit, 0, len(resp.Results))
	for _, item := range resp.Results {
		if item.FilePath == "" {
			continue
		}
		hits = append(hits, Hit{
			Source:    "codebase-memory/search_graph",
			File:      item.FilePath,
			LineStart: max(item.StartLine, 1),
			LineEnd:   max(item.EndLine, max(item.StartLine, 1)),
			Symbol:    nonEmpty(item.QualifiedName, item.Name),
			Why:       "graph symbol search",
			Family:    "graph",
			Tags:      []string{item.Label},
		})
	}
	return hits, nil
}

func SearchGraphByName(ctx context.Context, repo string, cbmBinary string, cacheDir string, namePattern string, timeoutSeconds int, limit int) ([]Hit, error) {
	project := projectName(repo)
	payload := fmt.Sprintf(`{"project":"%s","name_pattern":%q,"limit":%d}`, project, namePattern, limit)
	out, errOut, err := runCommandWithEnv(ctx, timeoutSeconds, repo, []string{"CBM_CACHE_DIR=" + cacheDir}, cbmBinary, "cli", "search_graph", payload)
	if err != nil && out == "" {
		return nil, fmt.Errorf("cbm search_graph exact: %w: %s", err, errOut)
	}

	var resp cbmSearchGraphResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, fmt.Errorf("parse cbm search_graph exact: %w", err)
	}
	hits := make([]Hit, 0, len(resp.Results))
	for _, item := range resp.Results {
		if item.FilePath == "" {
			continue
		}
		hits = append(hits, Hit{
			Source:    "codebase-memory/search_graph",
			File:      item.FilePath,
			LineStart: max(item.StartLine, 1),
			LineEnd:   max(item.EndLine, max(item.StartLine, 1)),
			Symbol:    nonEmpty(item.QualifiedName, item.Name),
			Why:       "exact graph name search",
			Family:    "graph",
			Tags:      []string{item.Label},
		})
	}
	return hits, nil
}

func SearchCode(ctx context.Context, repo string, cbmBinary string, cacheDir string, term string, timeoutSeconds int, limit int) ([]Hit, error) {
	return SearchCodeScoped(ctx, repo, cbmBinary, cacheDir, term, "", timeoutSeconds, limit)
}

func SearchCodeScoped(ctx context.Context, repo string, cbmBinary string, cacheDir string, term string, pathFilter string, timeoutSeconds int, limit int) ([]Hit, error) {
	project := projectName(repo)
	payload := fmt.Sprintf(`{"project":"%s","pattern":%q,"mode":"compact","limit":%d`, project, term, limit)
	if strings.TrimSpace(pathFilter) != "" {
		payload += fmt.Sprintf(`,"path_filter":%q`, pathFilter)
	}
	payload += `}`
	out, errOut, err := runCommandWithEnv(ctx, timeoutSeconds, repo, []string{"CBM_CACHE_DIR=" + cacheDir}, cbmBinary, "cli", "search_code", payload)
	if err != nil && out == "" {
		return nil, fmt.Errorf("cbm search_code: %w: %s", err, errOut)
	}

	var resp cbmSearchCodeResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, fmt.Errorf("parse cbm search_code: %w", err)
	}
	hits := make([]Hit, 0, len(resp.Results))
	for _, item := range resp.Results {
		filePath := nonEmpty(item.FilePath, item.File)
		if filePath == "" {
			continue
		}
		hits = append(hits, Hit{
			Source:    "codebase-memory/search_code",
			File:      filePath,
			LineStart: max(item.StartLine, 1),
			LineEnd:   max(item.EndLine, max(item.StartLine, 1)),
			Symbol:    nonEmpty(item.QualifiedName, item.Node),
			Why:       nonEmpty(item.Signature, item.QualifiedName, item.Node, "graph text search"),
			Family:    "graph_text",
			Tags:      []string{item.Label},
		})
	}
	return hits, nil
}

func TracePath(ctx context.Context, repo string, cbmBinary string, cacheDir string, functionName string, direction string, depth int, mode string, timeoutSeconds int) (TraceResult, error) {
	project := projectName(repo)
	if direction == "" {
		direction = "both"
	}
	if depth <= 0 {
		depth = 2
	}
	if mode == "" {
		mode = "calls"
	}
	payload := fmt.Sprintf(`{"project":"%s","function_name":%q,"direction":%q,"depth":%d,"mode":%q}`, project, functionName, direction, depth, mode)
	out, errOut, err := runCommandWithEnv(ctx, timeoutSeconds, repo, []string{"CBM_CACHE_DIR=" + cacheDir}, cbmBinary, "cli", "trace_path", payload)
	if err != nil && out == "" {
		return TraceResult{}, fmt.Errorf("cbm trace_path: %w: %s", err, errOut)
	}

	var resp cbmTraceResponse
	if err := json.Unmarshal([]byte(extractJSON(out)), &resp); err != nil {
		return TraceResult{}, fmt.Errorf("parse cbm trace_path: %w", err)
	}
	result := TraceResult{
		Symbol:    nonEmpty(resp.Function, functionName),
		Direction: resp.Direction,
		Mode:      resp.Mode,
		Callers:   resolveTraceNodes(ctx, repo, cbmBinary, cacheDir, resp.Callers, timeoutSeconds),
		Callees:   resolveTraceNodes(ctx, repo, cbmBinary, cacheDir, resp.Callees, timeoutSeconds),
	}
	return result, nil
}

func ResolveSymbol(ctx context.Context, repo string, cbmBinary string, cacheDir string, query string, timeoutSeconds int) (Hit, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed != "" {
		pattern := ".*" + regexp.QuoteMeta(trimmed) + ".*"
		if exact, err := SearchGraphByName(ctx, repo, cbmBinary, cacheDir, pattern, timeoutSeconds, 5); err == nil && len(exact) != 0 {
			best := exact[0]
			for _, hit := range exact {
				if strings.EqualFold(filepath.Base(hit.File), trimmed) || strings.HasSuffix(strings.ToLower(hit.Symbol), "."+strings.ToLower(trimmed)) {
					best = hit
					break
				}
			}
			return best, nil
		}
	}
	results, err := SearchGraph(ctx, repo, cbmBinary, cacheDir, query, timeoutSeconds, 5)
	if err != nil {
		return Hit{}, err
	}
	if len(results) == 0 {
		return Hit{}, fmt.Errorf("no symbol match for %q", query)
	}
	best := results[0]
	queryLower := strings.ToLower(strings.TrimSpace(query))
	for _, hit := range results {
		symbolLower := strings.ToLower(hit.Symbol)
		baseLower := strings.ToLower(filepath.Base(hit.File))
		if strings.Contains(symbolLower, queryLower) || strings.Contains(baseLower, queryLower) {
			best = hit
			break
		}
	}
	return best, nil
}

func resolveTraceNodes(ctx context.Context, repo string, cbmBinary string, cacheDir string, nodes []cbmTraceNode, timeoutSeconds int) []TraceStep {
	steps := make([]TraceStep, 0, len(nodes))
	if len(nodes) == 0 {
		return steps
	}
	steps = make([]TraceStep, len(nodes))
	var wg sync.WaitGroup
	wg.Add(len(nodes))
	for i, node := range nodes {
		go func(index int, node cbmTraceNode) {
			defer wg.Done()
			step := TraceStep{
				Name:          node.Name,
				QualifiedName: node.QualifiedName,
				Hop:           node.Hop,
			}
			resolveQuery := nonEmpty(node.QualifiedName, node.Name)
			if hit, err := ResolveSymbol(ctx, repo, cbmBinary, cacheDir, resolveQuery, timeoutSeconds); err == nil {
				step.File = hit.File
				step.LineStart = hit.LineStart
				step.LineEnd = hit.LineEnd
				if step.QualifiedName == "" {
					step.QualifiedName = hit.Symbol
				}
			} else if node.QualifiedName != "" && node.Name != "" {
				if hit, fallbackErr := ResolveSymbol(ctx, repo, cbmBinary, cacheDir, node.Name, timeoutSeconds); fallbackErr == nil {
					step.File = hit.File
					step.LineStart = hit.LineStart
					step.LineEnd = hit.LineEnd
					if step.QualifiedName == "" {
						step.QualifiedName = hit.Symbol
					}
				}
			}
			steps[index] = step
		}(i, node)
	}
	wg.Wait()
	return steps
}

func extractJSON(text string) string {
	text = strings.TrimSpace(text)
	re := regexp.MustCompile(`(?s)\{.*\}$`)
	match := re.FindString(text)
	if match != "" {
		return match
	}
	return text
}

func projectName(repo string) string {
	cleaned := strings.Trim(filepath.Clean(repo), "/")
	if cleaned == "" {
		return "root"
	}
	return strings.ReplaceAll(cleaned, "/", "-")
}
