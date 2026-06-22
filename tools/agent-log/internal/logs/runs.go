package logs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Root is the base directory where per-run log directories are stored.
const Root = "/var/pile/agent-log/data/runs"

// Request records the inputs for one agent run.
type Request struct {
	Query          string `json:"query"`
	StartedAt      string `json:"started_at,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Verbose        bool   `json:"verbose,omitempty"`
	JSONMode       bool   `json:"json_mode,omitempty"`
}

// IndexRecord is one entry in the per-run index.json.
type IndexRecord struct {
	RecordedAt    string `json:"recorded_at"`
	RunDir        string `json:"run_dir"`
	Status        string `json:"status"`
	StartedAt     string `json:"started_at,omitempty"`
	FinishedAt    string `json:"finished_at,omitempty"`
	DurationMs    int64  `json:"duration_ms,omitempty"`
	Query         string `json:"query,omitempty"`
	Turns         int    `json:"turns,omitempty"`
	MaxTurns      bool   `json:"max_turns,omitempty"`
	AnswerPreview string `json:"answer_preview,omitempty"`
	Error         string `json:"error,omitempty"`
	RequestPath   string `json:"request_path,omitempty"`
	ResultPath    string `json:"result_path,omitempty"`
	FailurePath   string `json:"failure_path,omitempty"`
}

// EnsureRunDir creates and returns a timestamped directory under Root.
func EnsureRunDir(query string) (string, error) {
	dir := filepath.Join(Root, time.Now().UTC().Format("20060102-150405")+"-"+label(query))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir run dir: %w", err)
	}
	return dir, nil
}

// SaveStarted writes request.json and records "started" in the index.
func SaveStarted(runDir string, req Request) error {
	if req.StartedAt == "" {
		req.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := writeJSON(filepath.Join(runDir, "request.json"), req); err != nil {
		return err
	}
	return updateIndex(IndexRecord{
		RecordedAt:  req.StartedAt,
		RunDir:      runDir,
		Status:      "started",
		StartedAt:   req.StartedAt,
		Query:       req.Query,
		RequestPath: filepath.Join(runDir, "request.json"),
	})
}

// SaveSuccess writes result.json and records "ok" in the index.
func SaveSuccess(runDir string, req Request, answer string, turns int, maxTurns bool, startedAt, finishedAt time.Time) error {
	payload := map[string]any{
		"answer":    answer,
		"turns":     turns,
		"max_turns": maxTurns,
	}
	if err := writeJSON(filepath.Join(runDir, "result.json"), payload); err != nil {
		return err
	}
	var durMs int64
	if !startedAt.IsZero() && !finishedAt.IsZero() && finishedAt.After(startedAt) {
		durMs = finishedAt.Sub(startedAt).Milliseconds()
	}
	return updateIndex(IndexRecord{
		RecordedAt:    finishedAt.UTC().Format(time.RFC3339),
		RunDir:        runDir,
		Status:        "ok",
		StartedAt:     req.StartedAt,
		FinishedAt:    finishedAt.UTC().Format(time.RFC3339),
		DurationMs:    durMs,
		Query:         req.Query,
		Turns:         turns,
		MaxTurns:      maxTurns,
		AnswerPreview: squash(answer, 240),
		RequestPath:   filepath.Join(runDir, "request.json"),
		ResultPath:    filepath.Join(runDir, "result.json"),
	})
}

// SaveFailure writes failure.json and records "failed" in the index.
func SaveFailure(runDir string, req Request, runErr error, turns int, maxTurns bool, startedAt, finishedAt time.Time) error {
	if err := writeJSON(filepath.Join(runDir, "failure.json"), map[string]string{"error": runErr.Error()}); err != nil {
		return err
	}
	var durMs int64
	if !startedAt.IsZero() && !finishedAt.IsZero() && finishedAt.After(startedAt) {
		durMs = finishedAt.Sub(startedAt).Milliseconds()
	}
	return updateIndex(IndexRecord{
		RecordedAt:  finishedAt.UTC().Format(time.RFC3339),
		RunDir:      runDir,
		Status:      "failed",
		StartedAt:   req.StartedAt,
		FinishedAt:  finishedAt.UTC().Format(time.RFC3339),
		DurationMs:  durMs,
		Query:       req.Query,
		Turns:       turns,
		MaxTurns:    maxTurns,
		Error:       squash(runErr.Error(), 240),
		RequestPath: filepath.Join(runDir, "request.json"),
		FailurePath: filepath.Join(runDir, "failure.json"),
	})
}

// Prune deletes old run directories: any older than maxAge, then the oldest
// beyond keep. Keep defaults to 50 when <= 0.
func Prune(keep int, maxAge time.Duration) error {
	if keep <= 0 {
		keep = 50
	}
	entries, err := os.ReadDir(Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	type item struct {
		name string
		when time.Time
	}
	var runs []item
	now := time.Now()
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(Root, entry.Name())
		if maxAge > 0 && now.Sub(info.ModTime()) > maxAge {
			_ = os.RemoveAll(path)
			continue
		}
		runs = append(runs, item{name: entry.Name(), when: info.ModTime()})
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].when.After(runs[j].when) })
	for i := keep; i < len(runs); i++ {
		_ = os.RemoveAll(filepath.Join(Root, runs[i].name))
	}
	return nil
}

func updateIndex(record IndexRecord) error {
	if err := os.MkdirAll(Root, 0o755); err != nil {
		return fmt.Errorf("mkdir runs root: %w", err)
	}
	path := filepath.Join(Root, "index.json")
	var items []IndexRecord
	if raw, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(raw))) != 0 {
		_ = json.Unmarshal(raw, &items)
	}
	replaced := false
	for i := range items {
		if items[i].RunDir == record.RunDir {
			items[i] = record
			replaced = true
			break
		}
	}
	if !replaced {
		items = append([]IndexRecord{record}, items...)
	}
	if len(items) > 200 {
		items = items[:200]
	}
	raw, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func writeJSON(path string, payload any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

func label(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "run"
	}
	var b strings.Builder
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
		if b.Len() >= 48 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "run"
	}
	return out
}

func squash(v string, limit int) string {
	v = strings.Join(strings.Fields(strings.TrimSpace(v)), " ")
	if limit > 0 && len(v) > limit {
		return v[:limit] + "...(truncated)"
	}
	return v
}
