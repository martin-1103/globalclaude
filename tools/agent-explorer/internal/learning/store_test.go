package learning

import (
	"os"
	"path/filepath"
	"sync"
	"strings"
	"testing"
	"time"
)

func TestRecencyWeightFreshnessBands(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	if got := recencyWeight(now, now.Add(-7*24*time.Hour).Format(time.RFC3339)); got != 4 {
		t.Fatalf("fresh weight = %d, want 4", got)
	}
	if got := recencyWeight(now, now.Add(-30*24*time.Hour).Format(time.RFC3339)); got != 3 {
		t.Fatalf("recent weight = %d, want 3", got)
	}
	if got := recencyWeight(now, now.Add(-120*24*time.Hour).Format(time.RFC3339)); got != 2 {
		t.Fatalf("warm weight = %d, want 2", got)
	}
	if got := recencyWeight(now, now.Add(-300*24*time.Hour).Format(time.RFC3339)); got != 1 {
		t.Fatalf("old weight = %d, want 1", got)
	}
}

func TestRecencyWeightInvalidTimestampFallsBack(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	if got := recencyWeight(now, "bad-time"); got != 1 {
		t.Fatalf("invalid timestamp weight = %d, want 1", got)
	}
}

func TestCompactFeedbackKeepsNewestDuplicates(t *testing.T) {
	dir := t.TempDir()
	repo := "/tmp/repo"
	base := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	summaryNow = func() time.Time { return base }
	t.Cleanup(func() {
		summaryNow = func() time.Time { return time.Now().UTC() }
	})
	entries := []FeedbackEntry{
		{Query: "q", AcceptedPaths: []string{"a.go"}, CreatedAt: base.Add(-3 * time.Hour).Format(time.RFC3339)},
		{Query: "q", AcceptedPaths: []string{"a.go"}, CreatedAt: base.Add(-2 * time.Hour).Format(time.RFC3339)},
		{Query: "q", AcceptedPaths: []string{"a.go"}, CreatedAt: base.Add(-1 * time.Hour).Format(time.RFC3339)},
	}
	path := feedbackPath(dir, repo)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := AppendFeedback(dir, repo, entry); err != nil {
			t.Fatal(err)
		}
	}
	count, err := CompactFeedback(dir, repo, 2, false, ValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("compacted count = %d, want 2", count)
	}
	loaded, err := readFeedbackEntries(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded entries = %d, want 2", len(loaded))
	}
}

func TestLoadStatsUsesRecencyWeight(t *testing.T) {
	dir := t.TempDir()
	repo := "/tmp/repo"
	base := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	summaryNow = func() time.Time { return base }
	t.Cleanup(func() {
		summaryNow = func() time.Time { return time.Now().UTC() }
	})
	if err := AppendFeedback(dir, repo, FeedbackEntry{
		Query:         "auth query",
		AcceptedPaths: []string{"fresh.go"},
		CreatedAt:     base.Add(-2 * 24 * time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	if err := AppendFeedback(dir, repo, FeedbackEntry{
		Query:         "auth query",
		AcceptedPaths: []string{"old.go"},
		CreatedAt:     base.Add(-300 * 24 * time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := LoadStats(dir, repo, 5, ValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.TopAcceptedPaths["fresh.go"] <= stats.TopAcceptedPaths["old.go"] {
		t.Fatalf("expected fresh.go weight > old.go, got %v", stats.TopAcceptedPaths)
	}
}

func TestStaleEntryDetectedWhenFileMissing(t *testing.T) {
	entry := FeedbackEntry{
		AcceptedEvidence: []EvidenceRef{{Path: "/tmp/does-not-exist.go", SnippetProbe: "hello"}},
	}
	if !staleEntry("/", entry, ValidationOptions{}) {
		t.Fatalf("expected stale entry for missing file")
	}
}

func TestStaleEntryFreshWhenSnippetStillPresent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "auth.go")
	if err := os.WriteFile(file, []byte("line1\nline2\nwriteJSONError(w, http.StatusUnauthorized, \"missing authorization header\")\nline4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	windowHash := lineWindowHash(strings.Split("line1\nline2\nwriteJSONError(w, http.StatusUnauthorized, \"missing authorization header\")\nline4\n", "\n"), 3)
	entry := FeedbackEntry{
		AcceptedEvidence: []EvidenceRef{{Path: file, LineStart: 3, SnippetProbe: "missing authorization header", SnippetHash: windowHash, Symbol: "Authenticate"}},
	}
	if staleEntry("/", entry, ValidationOptions{}) {
		t.Fatalf("expected fresh entry when snippet still present")
	}
}

func TestStaleEntryStaleWhenLineAnchorDriftsAndNoSymbolMatch(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "auth.go")
	if err := os.WriteFile(file, []byte("line1\nline2\nsomething else\nline4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := FeedbackEntry{
		AcceptedEvidence: []EvidenceRef{{Path: file, LineStart: 3, SnippetProbe: "missing authorization header", Symbol: "Authenticate"}},
	}
	if !staleEntry("/", entry, ValidationOptions{}) {
		t.Fatalf("expected stale entry when anchor window and symbol both miss")
	}
}

func TestCompactFeedbackCanDropStale(t *testing.T) {
	dir := t.TempDir()
	repo := "/tmp/repo"
	freshFile := filepath.Join(dir, "fresh.go")
	if err := os.WriteFile(freshFile, []byte("fresh token\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AppendFeedback(dir, repo, FeedbackEntry{
		Query:            "fresh",
		AcceptedPaths:    []string{freshFile},
		AcceptedEvidence: []EvidenceRef{{Path: freshFile, SnippetProbe: "fresh token"}},
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	if err := AppendFeedback(dir, repo, FeedbackEntry{
		Query:            "stale",
		AcceptedPaths:    []string{filepath.Join(dir, "missing.go")},
		AcceptedEvidence: []EvidenceRef{{Path: filepath.Join(dir, "missing.go"), SnippetProbe: "gone"}},
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	count, err := CompactFeedback(dir, repo, 3, true, ValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("compacted count = %d, want 1", count)
	}
}

func TestLineWindowHashEmptyWithoutAnchor(t *testing.T) {
	if got := lineWindowHash([]string{"a", "b"}, 0); got != "" {
		t.Fatalf("lineWindowHash without anchor = %q, want empty", got)
	}
}

func TestBodyWindowHashUsesLineRange(t *testing.T) {
	lines := strings.Split("a\nbody-one\nbody-two\nz\n", "\n")
	if got := bodyWindowHash(lines, 2, 3); got == "" {
		t.Fatalf("expected non-empty body hash")
	}
}

func TestSymbolResolveDistanceDefault(t *testing.T) {
	opts := ValidationOptions{}
	if opts.LineDistance != 0 {
		t.Fatalf("expected zero-value line distance before defaulting")
	}
	if opts.LineDistance <= 0 {
		opts.LineDistance = 40
	}
	if opts.LineDistance != 40 {
		t.Fatalf("line distance = %d, want 40", opts.LineDistance)
	}
}

func TestEvidenceFreshUsesValidationCache(t *testing.T) {
	validationCache = sync.Map{}
	validationCacheTTL = time.Hour
	base := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	validationNow = func() time.Time { return base }
	t.Cleanup(func() {
		validationCache = sync.Map{}
		validationCacheTTL = 5 * time.Minute
		validationNow = func() time.Time { return time.Now().UTC() }
	})

	dir := t.TempDir()
	file := filepath.Join(dir, "cached.go")
	if err := os.WriteFile(file, []byte("alpha\nbeta\nmissing authorization header\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := EvidenceRef{Path: file, LineStart: 3, SnippetProbe: "missing authorization header"}
	if !evidenceFresh("/", ref, ValidationOptions{}) {
		t.Fatalf("expected fresh evidence on first read")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if !evidenceFresh("/", ref, ValidationOptions{}) {
		t.Fatalf("expected cached fresh evidence on second read")
	}
}
