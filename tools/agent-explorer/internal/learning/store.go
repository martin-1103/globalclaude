package learning

import (
	"bufio"
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

	"agent-explorer/internal/tools"
)

type FeedbackEntry struct {
	Query            string        `json:"query"`
	AcceptedPaths    []string      `json:"accepted_paths,omitempty"`
	RejectedPaths    []string      `json:"rejected_paths,omitempty"`
	AcceptedSymbols  []string      `json:"accepted_symbols,omitempty"`
	RejectedSymbols  []string      `json:"rejected_symbols,omitempty"`
	AcceptedEvidence []EvidenceRef `json:"accepted_evidence,omitempty"`
	RejectedEvidence []EvidenceRef `json:"rejected_evidence,omitempty"`
	Notes            string        `json:"notes,omitempty"`
	CreatedAt        string        `json:"created_at"`
}

type EvidenceRef struct {
	Path         string `json:"path,omitempty"`
	Symbol       string `json:"symbol,omitempty"`
	LineStart    int    `json:"line_start,omitempty"`
	LineEnd      int    `json:"line_end,omitempty"`
	BodyHash     string `json:"body_hash,omitempty"`
	SnippetHash  string `json:"snippet_hash,omitempty"`
	SnippetProbe string `json:"snippet_probe,omitempty"`
}

type Summary struct {
	AcceptedPathWeight   map[string]int
	RejectedPathWeight   map[string]int
	AcceptedSymbolWeight map[string]int
	RejectedSymbolWeight map[string]int
	TopicAcceptedPaths   map[string]map[string]int
}

type Observation struct {
	Query            string
	Accepts          []string
	Rejects          []string
	AcceptedEvidence []EvidenceRef
	RejectedEvidence []EvidenceRef
	Notes            string
}

type ValidationOptions struct {
	CBMBinary      string
	CBMCacheDir    string
	TimeoutSeconds int
	LineDistance   int
}

type Stats struct {
	Entries               int            `json:"entries"`
	StaleEntries          int            `json:"stale_entries"`
	AcceptedPaths         int            `json:"accepted_paths"`
	RejectedPaths         int            `json:"rejected_paths"`
	AcceptedSymbols       int            `json:"accepted_symbols"`
	RejectedSymbols       int            `json:"rejected_symbols"`
	UniqueAcceptedPaths   int            `json:"unique_accepted_paths"`
	UniqueRejectedPaths   int            `json:"unique_rejected_paths"`
	UniqueAcceptedSymbols int            `json:"unique_accepted_symbols"`
	UniqueRejectedSymbols int            `json:"unique_rejected_symbols"`
	TopAcceptedPaths      map[string]int `json:"top_accepted_paths,omitempty"`
	TopRejectedPaths      map[string]int `json:"top_rejected_paths,omitempty"`
	TopStalePaths         map[string]int `json:"top_stale_paths,omitempty"`
}

var summaryNow = func() time.Time {
	return time.Now().UTC()
}

var validationNow = func() time.Time {
	return time.Now().UTC()
}

var validationCacheTTL = 5 * time.Minute

var validationCache sync.Map

type validationCacheEntry struct {
	Fresh     bool
	ExpiresAt time.Time
}

func AppendFeedback(memoryDir string, repo string, entry FeedbackEntry) error {
	if strings.TrimSpace(memoryDir) == "" {
		return fmt.Errorf("memory dir empty")
	}
	if strings.TrimSpace(repo) == "" {
		return fmt.Errorf("repo empty")
	}
	entry.Query = strings.TrimSpace(entry.Query)
	if entry.Query == "" {
		return fmt.Errorf("query empty")
	}
	if strings.TrimSpace(entry.CreatedAt) == "" {
		entry.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	path := feedbackPath(memoryDir, repo)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	body, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = f.WriteString(string(body) + "\n")
	return err
}

func LoadSummary(memoryDir string, repo string, opts ValidationOptions) (Summary, error) {
	s := Summary{
		AcceptedPathWeight:   map[string]int{},
		RejectedPathWeight:   map[string]int{},
		AcceptedSymbolWeight: map[string]int{},
		RejectedSymbolWeight: map[string]int{},
		TopicAcceptedPaths:   map[string]map[string]int{},
	}
	path := feedbackPath(memoryDir, repo)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	now := summaryNow()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry FeedbackEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		weight := recencyWeight(now, entry.CreatedAt)
		if staleEntry(repo, entry, opts) {
			weight = max(1, weight/2)
		}
		topics := topicTerms(entry.Query)
		for _, path := range entry.AcceptedPaths {
			path = cleanKey(path)
			if path == "" {
				continue
			}
			s.AcceptedPathWeight[path] += weight
			for _, topic := range topics {
				if s.TopicAcceptedPaths[topic] == nil {
					s.TopicAcceptedPaths[topic] = map[string]int{}
				}
				s.TopicAcceptedPaths[topic][path] += weight
			}
		}
		for _, path := range entry.RejectedPaths {
			path = cleanKey(path)
			if path != "" {
				s.RejectedPathWeight[path] += weight
			}
		}
		for _, symbol := range entry.AcceptedSymbols {
			symbol = cleanKey(symbol)
			if symbol != "" {
				s.AcceptedSymbolWeight[symbol] += weight
			}
		}
		for _, symbol := range entry.RejectedSymbols {
			symbol = cleanKey(symbol)
			if symbol != "" {
				s.RejectedSymbolWeight[symbol] += weight
			}
		}
	}
	return s, scanner.Err()
}

func LoadStats(memoryDir string, repo string, limit int, opts ValidationOptions) (Stats, error) {
	if limit <= 0 {
		limit = 5
	}
	stats := Stats{
		TopAcceptedPaths: map[string]int{},
		TopRejectedPaths: map[string]int{},
		TopStalePaths:    map[string]int{},
	}
	path := feedbackPath(memoryDir, repo)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return stats, nil
		}
		return stats, err
	}
	defer f.Close()
	accepted := map[string]int{}
	rejected := map[string]int{}
	stalePaths := map[string]int{}
	acceptedSymbols := map[string]bool{}
	rejectedSymbols := map[string]bool{}
	scanner := bufio.NewScanner(f)
	now := summaryNow()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry FeedbackEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		weight := recencyWeight(now, entry.CreatedAt)
		stale := staleEntry(repo, entry, opts)
		if stale {
			stats.StaleEntries++
			weight = max(1, weight/2)
		}
		stats.Entries++
		for _, item := range entry.AcceptedPaths {
			key := cleanKey(item)
			if key == "" {
				continue
			}
			stats.AcceptedPaths++
			accepted[key] += weight
			if stale {
				stalePaths[key] += weight
			}
		}
		for _, item := range entry.RejectedPaths {
			key := cleanKey(item)
			if key == "" {
				continue
			}
			stats.RejectedPaths++
			rejected[key] += weight
			if stale {
				stalePaths[key] += weight
			}
		}
		for _, item := range entry.AcceptedSymbols {
			key := cleanKey(item)
			if key == "" {
				continue
			}
			stats.AcceptedSymbols++
			acceptedSymbols[key] = true
		}
		for _, item := range entry.RejectedSymbols {
			key := cleanKey(item)
			if key == "" {
				continue
			}
			stats.RejectedSymbols++
			rejectedSymbols[key] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return stats, err
	}
	stats.UniqueAcceptedPaths = len(accepted)
	stats.UniqueRejectedPaths = len(rejected)
	stats.UniqueAcceptedSymbols = len(acceptedSymbols)
	stats.UniqueRejectedSymbols = len(rejectedSymbols)
	stats.TopAcceptedPaths = topCounts(accepted, limit)
	stats.TopRejectedPaths = topCounts(rejected, limit)
	stats.TopStalePaths = topCounts(stalePaths, limit)
	return stats, nil
}

func TopTopicPaths(summary Summary, query string, limit int) []string {
	if limit <= 0 {
		limit = 3
	}
	topics := topicTerms(query)
	scoreByPath := map[string]int{}
	for _, topic := range topics {
		for path, score := range summary.TopicAcceptedPaths[topic] {
			scoreByPath[path] += score
		}
	}
	type item struct {
		path  string
		score int
	}
	items := make([]item, 0, len(scoreByPath))
	for path, score := range scoreByPath {
		items = append(items, item{path: path, score: score})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		return items[i].path < items[j].path
	})
	out := make([]string, 0, min(limit, len(items)))
	for i, it := range items {
		if i >= limit {
			break
		}
		out = append(out, it.path)
	}
	return out
}

func AppendObservation(memoryDir string, repo string, obs Observation) error {
	entry := FeedbackEntry{
		Query:            strings.TrimSpace(obs.Query),
		AcceptedPaths:    normalizePaths(obs.Accepts),
		RejectedPaths:    normalizePaths(obs.Rejects),
		AcceptedEvidence: normalizeEvidence(obs.AcceptedEvidence),
		RejectedEvidence: normalizeEvidence(obs.RejectedEvidence),
		Notes:            strings.TrimSpace(obs.Notes),
	}
	if entry.Query == "" {
		return fmt.Errorf("query empty")
	}
	if len(entry.AcceptedPaths) == 0 && len(entry.RejectedPaths) == 0 {
		return fmt.Errorf("empty observation")
	}
	return AppendFeedback(memoryDir, repo, entry)
}

func CompactFeedback(memoryDir string, repo string, keepRecent int, dropStale bool, opts ValidationOptions) (int, error) {
	if keepRecent <= 0 {
		keepRecent = 3
	}
	path := feedbackPath(memoryDir, repo)
	entries, err := readFeedbackEntries(path)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}
	compacted := compactEntries(repo, entries, keepRecent, dropStale, opts)
	body := strings.Builder{}
	for _, entry := range compacted {
		line, err := json.Marshal(entry)
		if err != nil {
			return 0, err
		}
		body.Write(line)
		body.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		return 0, err
	}
	return len(compacted), nil
}

func feedbackPath(memoryDir string, repo string) string {
	slug := strings.ReplaceAll(strings.Trim(filepath.Clean(repo), "/"), "/", "__")
	if slug == "" {
		slug = "root"
	}
	return filepath.Join(memoryDir, slug, "feedback.jsonl")
}

func cleanKey(v string) string {
	return strings.ToLower(strings.TrimSpace(filepath.ToSlash(v)))
}

func normalizePaths(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		key := cleanKey(item)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

func topicTerms(query string) []string {
	stop := map[string]bool{
		"how": true, "where": true, "what": true, "when": true, "which": true, "why": true,
		"and": true, "the": true, "are": true, "was": true, "were": true, "with": true,
		"into": true, "from": true, "this": true, "that": true, "logic": true, "path": true,
		"involved": true, "handled": true, "handle": true, "find": true, "show": true, "trace": true,
	}
	seen := map[string]bool{}
	var out []string
	for _, token := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return r == ' ' || r == ',' || r == '.' || r == ';' || r == ':' || r == '(' || r == ')' || r == '/' || r == '-'
	}) {
		if len(token) < 4 || stop[token] || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
		if len(out) >= 4 {
			break
		}
	}
	return out
}

func recencyWeight(now time.Time, createdAt string) int {
	ts := strings.TrimSpace(createdAt)
	if ts == "" {
		return 1
	}
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return 1
	}
	age := now.Sub(parsed.UTC())
	switch {
	case age <= 14*24*time.Hour:
		return 4
	case age <= 60*24*time.Hour:
		return 3
	case age <= 180*24*time.Hour:
		return 2
	default:
		return 1
	}
}

func staleEntry(repo string, entry FeedbackEntry, opts ValidationOptions) bool {
	refs := append([]EvidenceRef{}, entry.AcceptedEvidence...)
	refs = append(refs, entry.RejectedEvidence...)
	if len(refs) == 0 {
		for _, path := range entry.AcceptedPaths {
			refs = append(refs, EvidenceRef{Path: path})
		}
		for _, path := range entry.RejectedPaths {
			refs = append(refs, EvidenceRef{Path: path})
		}
	}
	if len(refs) == 0 {
		return false
	}
	for _, ref := range refs {
		if evidenceFresh(repo, ref, opts) {
			return false
		}
	}
	return true
}

func evidenceFresh(repo string, ref EvidenceRef, opts ValidationOptions) bool {
	if fresh, ok := cachedEvidenceFresh(repo, ref, opts); ok {
		return fresh
	}
	path := strings.TrimSpace(filepath.ToSlash(ref.Path))
	if path == "" {
		storeEvidenceFresh(repo, ref, opts, false)
		return false
	}
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(repo, path)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		storeEvidenceFresh(repo, ref, opts, false)
		return false
	}
	text := normalizeSnippet(string(data))
	lines := strings.Split(string(data), "\n")
	if hash := strings.TrimSpace(ref.SnippetHash); hash != "" && lineWindowHash(lines, ref.LineStart) == hash {
		storeEvidenceFresh(repo, ref, opts, true)
		return true
	}
	if body := strings.TrimSpace(ref.BodyHash); body != "" && bodyWindowHash(lines, ref.LineStart, ref.LineEnd) == body {
		storeEvidenceFresh(repo, ref, opts, true)
		return true
	}
	if probe := normalizeSnippet(ref.SnippetProbe); probe != "" && lineWindowContains(lines, ref.LineStart, probe) {
		storeEvidenceFresh(repo, ref, opts, true)
		return true
	}
	if probe := normalizeSnippet(ref.SnippetProbe); probe != "" && strings.Contains(text, probe) {
		storeEvidenceFresh(repo, ref, opts, true)
		return true
	}
	if tail := tailSymbol(ref.Symbol); tail != "" && containsSymbolToken(text, tail) {
		storeEvidenceFresh(repo, ref, opts, true)
		return true
	}
	if ref.Symbol != "" && symbolResolves(repo, ref, opts) {
		storeEvidenceFresh(repo, ref, opts, true)
		return true
	}
	fresh := ref.Symbol == "" && normalizeSnippet(ref.SnippetProbe) == ""
	storeEvidenceFresh(repo, ref, opts, fresh)
	return fresh
}

func lineWindowContains(lines []string, lineStart int, probe string) bool {
	if lineStart <= 0 || probe == "" || len(lines) == 0 {
		return false
	}
	start := max(0, lineStart-3)
	end := min(len(lines), lineStart+2)
	window := normalizeSnippet(strings.Join(lines[start:end], "\n"))
	return strings.Contains(window, probe)
}

func lineWindowHash(lines []string, lineStart int) string {
	if lineStart <= 0 || len(lines) == 0 {
		return ""
	}
	start := max(0, lineStart-3)
	end := min(len(lines), lineStart+2)
	return snippetHash(strings.Join(lines[start:end], "\n"))
}

func bodyWindowHash(lines []string, lineStart int, lineEnd int) string {
	if lineStart <= 0 || len(lines) == 0 {
		return ""
	}
	start := max(0, lineStart-1)
	end := lineEnd
	if end <= 0 || end < lineStart {
		end = lineStart
	}
	end = min(len(lines), end)
	if end <= start {
		return ""
	}
	return snippetHash(strings.Join(lines[start:end], "\n"))
}

func containsSymbolToken(text string, symbol string) bool {
	symbol = strings.ToLower(strings.TrimSpace(symbol))
	if symbol == "" {
		return false
	}
	for _, token := range strings.FieldsFunc(text, func(r rune) bool {
		return !(r == '_' || r == '.' || r == ':' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z'))
	}) {
		if token == symbol {
			return true
		}
	}
	return false
}

func symbolResolves(repo string, ref EvidenceRef, opts ValidationOptions) bool {
	if strings.TrimSpace(opts.CBMBinary) == "" || strings.TrimSpace(ref.Symbol) == "" {
		return false
	}
	timeout := opts.TimeoutSeconds
	if timeout <= 0 {
		timeout = 10
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()
	hit, err := tools.ResolveSymbol(ctx, repo, opts.CBMBinary, opts.CBMCacheDir, ref.Symbol, timeout)
	if err != nil {
		return false
	}
	refPath := strings.TrimSpace(filepath.ToSlash(ref.Path))
	hitPath := strings.TrimSpace(filepath.ToSlash(hit.File))
	if refPath == "" || hitPath == "" {
		return false
	}
	lineDistance := opts.LineDistance
	if lineDistance <= 0 {
		lineDistance = 40
	}
	lineClose := ref.LineStart <= 0 || hit.LineStart <= 0 || abs(hit.LineStart-ref.LineStart) <= lineDistance
	if filepath.IsAbs(refPath) && filepath.IsAbs(hitPath) {
		return strings.EqualFold(hitPath, refPath) && lineClose
	}
	samePath := strings.EqualFold(filepath.Base(hitPath), filepath.Base(refPath)) || strings.HasSuffix(hitPath, refPath)
	return samePath && lineClose
}

func cachedEvidenceFresh(repo string, ref EvidenceRef, opts ValidationOptions) (bool, bool) {
	key := validationCacheKey(repo, ref, opts)
	if key == "" {
		return false, false
	}
	raw, ok := validationCache.Load(key)
	if !ok {
		return false, false
	}
	entry, ok := raw.(validationCacheEntry)
	if !ok {
		validationCache.Delete(key)
		return false, false
	}
	if validationNow().After(entry.ExpiresAt) {
		validationCache.Delete(key)
		return false, false
	}
	return entry.Fresh, true
}

func storeEvidenceFresh(repo string, ref EvidenceRef, opts ValidationOptions, fresh bool) {
	key := validationCacheKey(repo, ref, opts)
	if key == "" {
		return
	}
	ttl := validationCacheTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	validationCache.Store(key, validationCacheEntry{
		Fresh:     fresh,
		ExpiresAt: validationNow().Add(ttl),
	})
}

func validationCacheKey(repo string, ref EvidenceRef, opts ValidationOptions) string {
	path := strings.TrimSpace(filepath.ToSlash(ref.Path))
	if path == "" {
		return ""
	}
	return strings.Join([]string{
		filepath.Clean(repo),
		path,
		cleanKey(ref.Symbol),
		fmt.Sprintf("%d", ref.LineStart),
		fmt.Sprintf("%d", ref.LineEnd),
		strings.TrimSpace(ref.BodyHash),
		strings.TrimSpace(ref.SnippetHash),
		normalizeSnippet(ref.SnippetProbe),
		strings.TrimSpace(opts.CBMBinary),
		strings.TrimSpace(opts.CBMCacheDir),
		fmt.Sprintf("%d", opts.LineDistance),
	}, "|")
}

func readFeedbackEntries(path string) ([]FeedbackEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var entries []FeedbackEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry FeedbackEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
}

func compactEntries(repo string, entries []FeedbackEntry, keepRecent int, dropStale bool, opts ValidationOptions) []FeedbackEntry {
	sort.SliceStable(entries, func(i, j int) bool {
		return entryTime(entries[i]).After(entryTime(entries[j]))
	})
	seen := map[string]int{}
	out := make([]FeedbackEntry, 0, len(entries))
	for _, entry := range entries {
		if dropStale && staleEntry(repo, entry, opts) {
			continue
		}
		key := compactKey(entry)
		if seen[key] >= keepRecent {
			continue
		}
		seen[key]++
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return entryTime(out[i]).Before(entryTime(out[j]))
	})
	return out
}

func compactKey(entry FeedbackEntry) string {
	return strings.Join([]string{
		strings.TrimSpace(strings.ToLower(entry.Query)),
		strings.Join(normalizePaths(entry.AcceptedPaths), ","),
		strings.Join(normalizePaths(entry.RejectedPaths), ","),
		strings.Join(normalizePaths(entry.AcceptedSymbols), ","),
		strings.Join(normalizePaths(entry.RejectedSymbols), ","),
	}, "|")
}

func entryTime(entry FeedbackEntry) time.Time {
	ts := strings.TrimSpace(entry.CreatedAt)
	if ts == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func topCounts(input map[string]int, limit int) map[string]int {
	type pair struct {
		key   string
		score int
	}
	items := make([]pair, 0, len(input))
	for k, v := range input {
		items = append(items, pair{key: k, score: v})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		return items[i].key < items[j].key
	})
	out := map[string]int{}
	for i, item := range items {
		if i >= limit {
			break
		}
		out[item.key] = item.score
	}
	return out
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func normalizeEvidence(items []EvidenceRef) []EvidenceRef {
	if len(items) == 0 {
		return nil
	}
	out := make([]EvidenceRef, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		item.Path = cleanKey(item.Path)
		item.Symbol = cleanKey(item.Symbol)
		item.SnippetProbe = normalizeSnippet(item.SnippetProbe)
		if item.SnippetHash == "" && item.SnippetProbe != "" {
			item.SnippetHash = snippetHash(item.SnippetProbe)
		}
		key := item.Path + "|" + item.Symbol + "|" + item.SnippetHash
		if item.Path == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

func normalizeSnippet(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return ""
	}
	fields := strings.Fields(v)
	if len(fields) == 0 {
		return ""
	}
	joined := strings.Join(fields, " ")
	if len(joined) > 160 {
		joined = joined[:160]
	}
	return joined
}

func snippetHash(v string) string {
	v = normalizeSnippet(v)
	if v == "" {
		return ""
	}
	sum := sha1.Sum([]byte(v))
	return fmt.Sprintf("%x", sum[:8])
}

func tailSymbol(symbol string) string {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return ""
	}
	parts := strings.Split(symbol, ".")
	return parts[len(parts)-1]
}
