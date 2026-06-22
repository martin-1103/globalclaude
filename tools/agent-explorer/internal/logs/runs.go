package logs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-explorer/internal/audit"
	"agent-explorer/internal/explorer"
	"agent-explorer/internal/format"
	"agent-explorer/internal/tools"
)

type AskRequest struct {
	Command           string `json:"command"`
	RunDir            string `json:"run_dir,omitempty"`
	Repo              string `json:"repo"`
	Query             string `json:"query"`
	ConfigPath        string `json:"config_path"`
	Explain           bool   `json:"explain"`
	JSONMode          bool   `json:"json_mode"`
	AgentMode         bool   `json:"agent_mode"`
	MainAgentMode     bool   `json:"main_agent_mode"`
	DebugRetrieval    bool   `json:"debug_retrieval"`
	ParallelRetrieval bool   `json:"parallel_retrieval"`
	TimeoutSeconds    int    `json:"timeout_seconds"`
}

type TraceRequest struct {
	Command        string `json:"command"`
	RunDir         string `json:"run_dir,omitempty"`
	Repo           string `json:"repo"`
	Symbol         string `json:"symbol,omitempty"`
	Query          string `json:"query,omitempty"`
	Direction      string `json:"direction"`
	Depth          int    `json:"depth"`
	ConfigPath     string `json:"config_path"`
	JSONMode       bool   `json:"json_mode"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func SaveAskRun(baseDir string, req AskRequest, result explorer.Result, runErr error) (string, error) {
	return saveRun(baseDir, req.Repo, req.Query, req, result, runErr, askSummary(result, runErr))
}

func SaveTraceRun(baseDir string, req TraceRequest, result tools.TraceResult, runErr error) (string, error) {
	return saveRun(baseDir, req.Repo, nonEmpty(req.Symbol, req.Query), req, result, runErr, traceSummary(result, runErr))
}

func saveRun(baseDir, repo, label string, request any, result any, runErr error, summary string) (string, error) {
	if strings.TrimSpace(baseDir) == "" {
		baseDir = "/var/pile/agent-explorer/data"
	}
	dir := ""
	switch v := request.(type) {
	case AskRequest:
		dir = strings.TrimSpace(v.RunDir)
	case TraceRequest:
		dir = strings.TrimSpace(v.RunDir)
	}
	if dir == "" {
		var err error
		dir, err = audit.EnsureRunDir(baseDir, repo, label)
		if err != nil {
			return "", err
		}
	}
	if err := writeJSON(filepath.Join(dir, "request.json"), request); err != nil {
		return "", err
	}
	record := indexEntry(repo, dir, result, runErr)
	record.RequestPath = filepath.Join(dir, "request.json")
	record.LLMLogPath = filepath.Join(dir, "llm.jsonl")
	if runErr != nil {
		record.FailurePath = filepath.Join(dir, "failure.json")
		if err := writeJSON(record.FailurePath, map[string]string{"error": runErr.Error()}); err != nil {
			return "", err
		}
	} else {
		record.ResultPath = filepath.Join(dir, "result.json")
		if err := writeJSON(record.ResultPath, result); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.txt"), []byte(summary), 0o644); err != nil {
		return "", fmt.Errorf("write summary: %w", err)
	}
	if err := updateIndex(baseDir, repo, record); err != nil {
		return "", err
	}
	_ = pruneRuns(baseDir, repo, 50, 14*24*time.Hour)
	return dir, nil
}

func writeJSON(path string, payload any) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(path), err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	return nil
}

func askSummary(result explorer.Result, runErr error) string {
	if runErr != nil {
		return "status: failed\nerror: " + runErr.Error() + "\n"
	}
	status := classifyAskStatus(result)
	var b strings.Builder
	b.WriteString("status: " + status + "\n")
	b.WriteString("planner_status: " + format.PlannerStatus(result) + "\n")
	b.WriteString("answerability: " + format.Answerability(result) + "\n")
	b.WriteString("confidence_band: " + format.ConfidenceBand(result) + "\n")
	b.WriteString("top_confidence: " + format.TopConfidence(result) + "\n")
	b.WriteString("recommended_action: " + format.RecommendedAction(result) + "\n")
	b.WriteString("quality_flags: " + strings.Join(format.QualityFlags(result), ",") + "\n")
	b.WriteString("query: " + result.Query + "\n")
	b.WriteString("repo: " + result.Repo + "\n")
	b.WriteString(fmt.Sprintf("hits: total=%d primary=%d supporting=%d trace=%d suppressed=%d\n", len(result.Hits), len(result.PrimaryHits), len(result.SupportingHits), len(result.TraceHits), len(result.Suppressed)))
	if len(result.Hits) != 0 {
		top := result.Hits[0]
		b.WriteString(fmt.Sprintf("top_hit: %s:%d-%d symbol=%s score=%d confidence=%s\n", top.File, top.LineStart, top.LineEnd, top.Symbol, top.Score, top.Confidence))
	}
	if summary := format.TraceSummary(result); summary != "" {
		b.WriteString("trace_summary: " + summary + "\n")
	}
	if len(result.Warnings) != 0 {
		b.WriteString("warnings:\n")
		for _, item := range result.Warnings {
			b.WriteString("- " + item + "\n")
		}
	}
	return b.String()
}

func traceSummary(result tools.TraceResult, runErr error) string {
	if runErr != nil {
		return "status: failed\nerror: " + runErr.Error() + "\n"
	}
	status := "ok"
	if len(result.Callers)+len(result.Callees) == 0 {
		status = "weak"
	}
	return fmt.Sprintf("status: %s\nsymbol: %s\ndirection: %s\ncallers: %d\ncallees: %d\n", status, result.Symbol, result.Direction, len(result.Callers), len(result.Callees))
}

func repoSlug(repo string) string {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "unknown"
	}
	repo = filepath.Clean(repo)
	repo = strings.ReplaceAll(repo, string(filepath.Separator), "__")
	repo = strings.ReplaceAll(repo, ":", "_")
	return repo
}

func runLabel(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "run"
	}
	var b strings.Builder
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		if b.Len() == 0 || strings.HasSuffix(b.String(), "-") {
			continue
		}
		b.WriteByte('-')
		if b.Len() >= 40 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "run"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func nonEmpty(items ...string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return ""
}

type indexRecord struct {
	RecordedAt        string   `json:"recorded_at"`
	Repo              string   `json:"repo"`
	RunDir            string   `json:"run_dir"`
	Status            string   `json:"status"`
	PlannerStatus     string   `json:"planner_status,omitempty"`
	Answerability     string   `json:"answerability,omitempty"`
	ConfidenceBand    string   `json:"confidence_band,omitempty"`
	TopConfidence     string   `json:"top_confidence,omitempty"`
	RecommendedAction string   `json:"recommended_action,omitempty"`
	Query             string   `json:"query,omitempty"`
	Symbol            string   `json:"symbol,omitempty"`
	HitCount          int      `json:"hit_count,omitempty"`
	WarningCount      int      `json:"warning_count,omitempty"`
	QualityFlags      []string `json:"quality_flags,omitempty"`
	Error             string   `json:"error,omitempty"`
	RequestPath       string   `json:"request_path,omitempty"`
	ResultPath        string   `json:"result_path,omitempty"`
	FailurePath       string   `json:"failure_path,omitempty"`
	LLMLogPath        string   `json:"llm_log_path,omitempty"`
}

func indexEntry(repo, runDir string, result any, runErr error) indexRecord {
	rec := indexRecord{
		RecordedAt: time.Now().UTC().Format(time.RFC3339),
		Repo:       repo,
		RunDir:     runDir,
	}
	if runErr != nil {
		rec.Status = "failed"
		rec.Error = runErr.Error()
		return rec
	}
	switch v := result.(type) {
	case explorer.Result:
		rec.Status = classifyAskStatus(v)
		rec.PlannerStatus = format.PlannerStatus(v)
		rec.Answerability = format.Answerability(v)
		rec.ConfidenceBand = format.ConfidenceBand(v)
		rec.TopConfidence = format.TopConfidence(v)
		rec.RecommendedAction = format.RecommendedAction(v)
		rec.Query = v.Query
		rec.HitCount = len(v.Hits)
		rec.WarningCount = len(v.Warnings)
		rec.QualityFlags = format.QualityFlags(v)
	case tools.TraceResult:
		rec.Status = "ok"
		if len(v.Callers)+len(v.Callees) == 0 {
			rec.Status = "weak"
		}
		rec.Symbol = v.Symbol
		rec.HitCount = len(v.Callers) + len(v.Callees)
	default:
		rec.Status = "ok"
	}
	return rec
}

func classifyAskStatus(result explorer.Result) string {
	if len(result.Hits) == 0 {
		return "weak"
	}
	top := result.Hits[0]
	if strings.TrimSpace(top.Confidence) == "low" {
		return "weak"
	}
	if hasFatalWarning(result.Warnings) {
		return "weak"
	}
	if len(result.Hits) == 1 && strings.TrimSpace(top.Confidence) != "high" {
		return "weak"
	}
	return "ok"
}

func hasFatalWarning(items []string) bool {
	for _, item := range items {
		text := strings.ToLower(strings.TrimSpace(item))
		if text == "" {
			continue
		}
		if strings.Contains(text, "timed out") || strings.Contains(text, "timeout") || strings.Contains(text, "context deadline exceeded") || strings.Contains(text, "failed:") {
			return true
		}
	}
	return false
}

func updateIndex(baseDir, repo string, record indexRecord) error {
	path := filepath.Join(baseDir, "runs", repoSlug(repo), "index.json")
	var items []indexRecord
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
		items = append([]indexRecord{record}, items...)
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

func SaveStartedIndex(baseDir, repo, runDir, query, symbol string) error {
	record := indexRecord{
		RecordedAt:  time.Now().UTC().Format(time.RFC3339),
		Repo:        repo,
		RunDir:      runDir,
		Status:      "started",
		Query:       query,
		Symbol:      symbol,
		RequestPath: filepath.Join(runDir, "request.json"),
		LLMLogPath:  filepath.Join(runDir, "llm.jsonl"),
	}
	return updateIndex(baseDir, repo, record)
}

func SaveFailedBootstrap(baseDir, repo, runDir, query, symbol string, request any, runErr error) error {
	if strings.TrimSpace(runDir) == "" {
		return fmt.Errorf("run dir required")
	}
	if err := writeJSON(filepath.Join(runDir, "request.json"), request); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(runDir, "failure.json"), map[string]string{"error": runErr.Error()}); err != nil {
		return err
	}
	summary := "status: failed\nerror: " + runErr.Error() + "\n"
	if err := os.WriteFile(filepath.Join(runDir, "summary.txt"), []byte(summary), 0o644); err != nil {
		return err
	}
	record := indexRecord{
		RecordedAt:  time.Now().UTC().Format(time.RFC3339),
		Repo:        repo,
		RunDir:      runDir,
		Status:      "failed",
		Query:       query,
		Symbol:      symbol,
		Error:       runErr.Error(),
		RequestPath: filepath.Join(runDir, "request.json"),
		FailurePath: filepath.Join(runDir, "failure.json"),
		LLMLogPath:  filepath.Join(runDir, "llm.jsonl"),
	}
	return updateIndex(baseDir, repo, record)
}

func pruneRuns(baseDir, repo string, keep int, maxAge time.Duration) error {
	dir := filepath.Join(baseDir, "runs", repoSlug(repo))
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
