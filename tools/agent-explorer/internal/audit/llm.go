package audit

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

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

func NewLLMLogger(runDir string) *LLMLogger {
	return &LLMLogger{path: filepath.Join(runDir, "llm.jsonl")}
}

func WithLLMLogger(ctx context.Context, logger *LLMLogger) context.Context {
	if logger == nil {
		return ctx
	}
	return context.WithValue(ctx, llmLogKey{}, logger)
}

func FromContext(ctx context.Context) *LLMLogger {
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

func Redact(v string, limit int) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	v = strings.Join(strings.Fields(v), " ")
	if limit > 0 && len(v) > limit {
		return v[:limit] + "...(truncated)"
	}
	return v
}

func Preview(v string, limit int) map[string]any {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	sum := sha1.Sum([]byte(v))
	return map[string]any{
		"preview": Redact(v, limit),
		"chars":   len(v),
		"sha1_8":  fmt.Sprintf("%x", sum[:4]),
	}
}

func RunDir(baseDir, repo, label string) string {
	return filepath.Join(baseDir, "runs", repoSlug(repo), time.Now().UTC().Format("20060102-150405")+"-"+runLabel(label))
}

func EnsureRunDir(baseDir, repo, label string) (string, error) {
	dir := RunDir(baseDir, repo, label)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir run dir: %w", err)
	}
	return dir, nil
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
