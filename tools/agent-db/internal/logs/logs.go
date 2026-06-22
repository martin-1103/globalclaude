package logs

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const Root = "/var/pile/agent-db/data/runs"

type llmLogKey struct{}

type LLMLogger struct {
	path string
	mu   sync.Mutex
}

type LLMEntry struct {
	RecordedAt string         `json:"recorded_at"`
	Model      string         `json:"model,omitempty"`
	Request    map[string]any `json:"request,omitempty"`
	Response   map[string]any `json:"response,omitempty"`
	Error      string         `json:"error,omitempty"`
	HTTPStatus int            `json:"http_status,omitempty"`
	DurationMs int64          `json:"duration_ms,omitempty"`
}

type Request struct {
	Command        string `json:"command"`
	RunDir         string `json:"run_dir,omitempty"`
	ProjectDir     string `json:"project_dir"`
	ConfigPath     string `json:"config_path"`
	Query          string `json:"query"`
	JSONMode       bool   `json:"json_mode"`
	Verbose        bool   `json:"verbose"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	StartedAt      string `json:"started_at,omitempty"`
}

type IndexRecord struct {
	RecordedAt        string `json:"recorded_at"`
	ProjectDir        string `json:"project_dir"`
	RunDir            string `json:"run_dir"`
	Status            string `json:"status"`
	StartedAt         string `json:"started_at,omitempty"`
	FinishedAt        string `json:"finished_at,omitempty"`
	DurationMs        int64  `json:"duration_ms,omitempty"`
	Query             string `json:"query,omitempty"`
	Turns             int    `json:"turns,omitempty"`
	StepCount         int    `json:"step_count,omitempty"`
	LastIntent        string `json:"last_intent,omitempty"`
	MaxTurns          bool   `json:"max_turns,omitempty"`
	Confidence        string `json:"confidence,omitempty"`
	Completeness      string `json:"completeness,omitempty"`
	RecommendedAction string `json:"recommended_action,omitempty"`
	AnswerPreview     string `json:"answer_preview,omitempty"`
	Error             string `json:"error,omitempty"`
	RequestPath       string `json:"request_path,omitempty"`
	ResultPath        string `json:"result_path,omitempty"`
	FailurePath       string `json:"failure_path,omitempty"`
	SummaryPath       string `json:"summary_path,omitempty"`
	LLMLogPath        string `json:"llm_log_path,omitempty"`
}

type Summary struct {
	Status            string
	Query             string
	StartedAt         string
	FinishedAt        string
	DurationMs        int64
	Turns             int
	StepCount         int
	LastIntent        string
	MaxTurns          bool
	Confidence        string
	Completeness      string
	RecommendedAction string
	AnswerPreview     string
	Error             string
}

func EnsureRunDir(projectDir, query string) (string, error) {
	dir := filepath.Join(Root, slug(projectDir), time.Now().UTC().Format("20060102-150405")+"-"+label(query))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir run dir: %w", err)
	}
	return dir, nil
}

func NewLLMLogger(runDir string) *LLMLogger {
	return &LLMLogger{path: filepath.Join(runDir, "llm.jsonl")}
}

func WithLLMLogger(ctx context.Context, logger *LLMLogger) context.Context {
	if logger == nil {
		return ctx
	}
	return context.WithValue(ctx, llmLogKey{}, logger)
}

func LLMLoggerFromContext(ctx context.Context) *LLMLogger {
	logger, _ := ctx.Value(llmLogKey{}).(*LLMLogger)
	return logger
}

func (l *LLMLogger) Append(entry LLMEntry) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if entry.RecordedAt == "" {
		entry.RecordedAt = time.Now().UTC().Format(time.RFC3339)
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(raw, '\n'))
	return err
}

func SaveStarted(runDir string, req Request) error {
	if req.StartedAt == "" {
		req.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := writeJSON(filepath.Join(runDir, "request.json"), req); err != nil {
		return err
	}
	return updateIndex(req.ProjectDir, IndexRecord{
		RecordedAt:  req.StartedAt,
		ProjectDir:  req.ProjectDir,
		RunDir:      runDir,
		Status:      "started",
		StartedAt:   req.StartedAt,
		Query:       req.Query,
		RequestPath: filepath.Join(runDir, "request.json"),
		LLMLogPath:  filepath.Join(runDir, "llm.jsonl"),
	})
}

func SaveSuccess(runDir string, req Request, payload any, summary Summary) error {
	if err := writeJSON(filepath.Join(runDir, "result.json"), payload); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(runDir, "summary.txt"), []byte(summary.Text()), 0o644); err != nil {
		return err
	}
	return updateIndex(req.ProjectDir, IndexRecord{
		RecordedAt:        nonEmpty(summary.FinishedAt, time.Now().UTC().Format(time.RFC3339)),
		ProjectDir:        req.ProjectDir,
		RunDir:            runDir,
		Status:            "ok",
		StartedAt:         summary.StartedAt,
		FinishedAt:        summary.FinishedAt,
		DurationMs:        summary.DurationMs,
		Query:             req.Query,
		Turns:             summary.Turns,
		StepCount:         summary.StepCount,
		LastIntent:        summary.LastIntent,
		MaxTurns:          summary.MaxTurns,
		Confidence:        summary.Confidence,
		Completeness:      summary.Completeness,
		RecommendedAction: summary.RecommendedAction,
		AnswerPreview:     summary.AnswerPreview,
		RequestPath:       filepath.Join(runDir, "request.json"),
		ResultPath:        filepath.Join(runDir, "result.json"),
		SummaryPath:       filepath.Join(runDir, "summary.txt"),
		LLMLogPath:        filepath.Join(runDir, "llm.jsonl"),
	})
}

func SaveFailure(runDir string, req Request, runErr error, summary Summary) error {
	if err := writeJSON(filepath.Join(runDir, "failure.json"), map[string]string{"error": runErr.Error()}); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(runDir, "summary.txt"), []byte(summary.Text()), 0o644); err != nil {
		return err
	}
	return updateIndex(req.ProjectDir, IndexRecord{
		RecordedAt:        nonEmpty(summary.FinishedAt, time.Now().UTC().Format(time.RFC3339)),
		ProjectDir:        req.ProjectDir,
		RunDir:            runDir,
		Status:            "failed",
		StartedAt:         summary.StartedAt,
		FinishedAt:        summary.FinishedAt,
		DurationMs:        summary.DurationMs,
		Query:             req.Query,
		Turns:             summary.Turns,
		StepCount:         summary.StepCount,
		LastIntent:        summary.LastIntent,
		MaxTurns:          summary.MaxTurns,
		Confidence:        summary.Confidence,
		Completeness:      summary.Completeness,
		RecommendedAction: summary.RecommendedAction,
		AnswerPreview:     summary.AnswerPreview,
		Error:             squash(runErr.Error(), 240),
		RequestPath:       filepath.Join(runDir, "request.json"),
		FailurePath:       filepath.Join(runDir, "failure.json"),
		SummaryPath:       filepath.Join(runDir, "summary.txt"),
		LLMLogPath:        filepath.Join(runDir, "llm.jsonl"),
	})
}

func BuildSummary(status, query string, startedAt, finishedAt time.Time, turns, stepCount int, lastIntent, answer string, maxTurns bool, runErr error) Summary {
	summary := Summary{
		Status:        nonEmpty(status, "unknown"),
		Query:         strings.TrimSpace(query),
		Turns:         turns,
		StepCount:     stepCount,
		LastIntent:    squash(lastIntent, 160),
		MaxTurns:      maxTurns,
		AnswerPreview: squash(answer, 240),
	}
	if !startedAt.IsZero() {
		summary.StartedAt = startedAt.UTC().Format(time.RFC3339)
	}
	if !finishedAt.IsZero() {
		summary.FinishedAt = finishedAt.UTC().Format(time.RFC3339)
	}
	if !startedAt.IsZero() && !finishedAt.IsZero() && finishedAt.After(startedAt) {
		summary.DurationMs = finishedAt.Sub(startedAt).Milliseconds()
	}
	if runErr != nil {
		summary.Error = squash(runErr.Error(), 240)
	}
	summary.Confidence, summary.Completeness, summary.RecommendedAction = assess(answer, maxTurns, runErr)
	return summary
}

func (s Summary) Text() string {
	var b strings.Builder
	writeSection(&b, "run", []summaryField{
		{key: "status", value: s.Status},
		{key: "query", value: s.Query},
		{key: "started_at", value: s.StartedAt},
		{key: "finished_at", value: s.FinishedAt},
		{key: "duration_ms", value: fmt.Sprintf("%d", s.DurationMs), skip: s.DurationMs == 0},
	})
	writeSection(&b, "execution", []summaryField{
		{key: "turns", value: fmt.Sprintf("%d", s.Turns), skip: s.Turns == 0},
		{key: "steps", value: fmt.Sprintf("%d", s.StepCount), skip: s.StepCount == 0},
		{key: "last_intent", value: s.LastIntent},
		{key: "max_turns", value: fmt.Sprintf("%t", s.MaxTurns), skip: !s.MaxTurns},
	})
	writeSection(&b, "assessment", []summaryField{
		{key: "confidence", value: s.Confidence},
		{key: "completeness", value: s.Completeness},
		{key: "recommended_action", value: s.RecommendedAction},
	})
	writeSection(&b, "outcome", []summaryField{
		{key: "answer_preview", value: s.AnswerPreview},
		{key: "error", value: s.Error},
	})
	return strings.TrimSpace(b.String()) + "\n"
}

type summaryField struct {
	key   string
	value string
	skip  bool
}

func updateIndex(projectDir string, record IndexRecord) error {
	path := filepath.Join(Root, slug(projectDir), "index.json")
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

func Preview(v string, limit int) map[string]any {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	sum := sha1.Sum([]byte(v))
	return map[string]any{
		"preview": squash(v, limit),
		"chars":   len(v),
		"sha1_8":  fmt.Sprintf("%x", sum[:4]),
	}
}

func slug(v string) string {
	v = filepath.Clean(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, string(filepath.Separator), "__")
	v = strings.ReplaceAll(v, ":", "_")
	if v == "" {
		return "unknown"
	}
	return v
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

func Prune(projectDir string, keep int, maxAge time.Duration) error {
	if keep <= 0 {
		keep = 50
	}
	dir := filepath.Join(Root, slug(projectDir))
	entries, err := os.ReadDir(dir)
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
		path := filepath.Join(dir, entry.Name())
		if maxAge > 0 && now.Sub(info.ModTime()) > maxAge {
			_ = os.RemoveAll(path)
			continue
		}
		runs = append(runs, item{name: entry.Name(), when: info.ModTime()})
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].when.After(runs[j].when) })
	for i := keep; i < len(runs); i++ {
		_ = os.RemoveAll(filepath.Join(dir, runs[i].name))
	}
	return nil
}

func assess(answer string, maxTurns bool, runErr error) (confidence, completeness, recommendedAction string) {
	if runErr != nil {
		return "low", "incomplete", "inspect failure.json and llm.jsonl, then rerun after fixing the failing query or connectivity issue"
	}
	if maxTurns {
		return "low", "partial", "rerun with a narrower question or a higher timeout"
	}
	normalized := strings.ToLower(strings.TrimSpace(answer))
	if normalized == "" {
		return "low", "incomplete", "rerun and inspect llm.jsonl because no final answer was recorded"
	}
	if containsAny(normalized, []string{"unknown", "unclear", "unverified", "could not", "cannot", "can't", "unable", "insufficient", "missing", "no data", "error"}) {
		return "low", "partial", "validate the missing portion with one targeted follow-up query"
	}
	if containsAny(normalized, []string{"inferred", "assuming", "assumption", "likely", "probably", "appears", "seems", "maybe", "estimate", "estimated"}) {
		return "medium", "partial", "validate the inferred portion with one targeted follow-up query"
	}
	return "high", "complete", "none"
}

func containsAny(v string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(v, term) {
			return true
		}
	}
	return false
}

func writeSection(b *strings.Builder, title string, fields []summaryField) {
	var lines []string
	for _, field := range fields {
		if field.skip || strings.TrimSpace(field.value) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", field.key, field.value))
	}
	if len(lines) == 0 {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString("[")
	b.WriteString(title)
	b.WriteString("]\n")
	for _, line := range lines {
		b.WriteString(line)
		b.WriteString("\n")
	}
}

func nonEmpty(items ...string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return ""
}
