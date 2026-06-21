package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-explorer/internal/config"
	"agent-explorer/internal/explorer"
	"agent-explorer/internal/format"
	"agent-explorer/internal/learning"
	"agent-explorer/internal/llm"
	"agent-explorer/internal/planner"
	"agent-explorer/internal/tools"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "ask":
		if err := runAsk(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println("agent-explorer 0.1.0")
	case "install":
		if err := runInstall(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "trace":
		if err := runTrace(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "feedback":
		if err := runFeedback(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "memory":
		if err := runMemory(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "memory-compact":
		if err := runMemoryCompact(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "memory-maintain":
		if err := runMemoryMaintain(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "eval":
		if err := runEval(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "init-eval":
		if err := runInitEval(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "init-profile":
		if err := runInitProfile(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(1)
	}
}

func runFeedback(args []string) error {
	fs := flag.NewFlagSet("feedback", flag.ExitOnError)
	repo := fs.String("repo", "", "repo path")
	query := fs.String("query", "", "query text")
	acceptPaths := fs.String("accept-paths", "", "comma-separated accepted paths")
	rejectPaths := fs.String("reject-paths", "", "comma-separated rejected paths")
	acceptSymbols := fs.String("accept-symbols", "", "comma-separated accepted symbols")
	rejectSymbols := fs.String("reject-symbols", "", "comma-separated rejected symbols")
	notes := fs.String("notes", "", "optional note")
	configPath := fs.String("config", config.DefaultPath(), "config path")
	fs.Parse(args)
	if *repo == "" {
		return fmt.Errorf("--repo required")
	}
	if *query == "" {
		return fmt.Errorf("--query required")
	}
	runtime, err := config.LoadRuntime(*configPath, *repo)
	if err != nil {
		return err
	}
	entry := learning.FeedbackEntry{
		Query:           *query,
		AcceptedPaths:   csvItems(*acceptPaths),
		RejectedPaths:   csvItems(*rejectPaths),
		AcceptedSymbols: csvItems(*acceptSymbols),
		RejectedSymbols: csvItems(*rejectSymbols),
		Notes:           strings.TrimSpace(*notes),
	}
	if err := learning.AppendFeedback(runtime.Config.MemoryDir, *repo, entry); err != nil {
		return err
	}
	fmt.Println("ok")
	return nil
}

func csvItems(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	raw := strings.Split(v, ",")
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func runMemory(args []string) error {
	fs := flag.NewFlagSet("memory", flag.ExitOnError)
	repo := fs.String("repo", "", "repo path")
	configPath := fs.String("config", config.DefaultPath(), "config path")
	jsonMode := fs.Bool("json", false, "output json")
	limit := fs.Int("limit", 5, "top path count")
	fs.Parse(args)
	if *repo == "" {
		return fmt.Errorf("--repo required")
	}
	runtime, err := config.LoadRuntime(*configPath, *repo)
	if err != nil {
		return err
	}
	stats, err := learning.LoadStats(runtime.Config.MemoryDir, *repo, *limit, learning.ValidationOptions{
		CBMBinary:      runtime.Config.CBMBinary,
		CBMCacheDir:    runtime.Config.CBMCacheDir,
		TimeoutSeconds: runtime.Config.ToolTimeoutSeconds,
	})
	if err != nil {
		return err
	}
	if *jsonMode {
		body, err := format.JSON(stats)
		if err != nil {
			return err
		}
		fmt.Println(body)
		return nil
	}
	fmt.Printf("Entries: %d\n", stats.Entries)
	fmt.Printf("Stale Entries: %d\n", stats.StaleEntries)
	fmt.Printf("Accepted Paths: %d unique=%d\n", stats.AcceptedPaths, stats.UniqueAcceptedPaths)
	fmt.Printf("Rejected Paths: %d unique=%d\n", stats.RejectedPaths, stats.UniqueRejectedPaths)
	fmt.Printf("Accepted Symbols: %d unique=%d\n", stats.AcceptedSymbols, stats.UniqueAcceptedSymbols)
	fmt.Printf("Rejected Symbols: %d unique=%d\n", stats.RejectedSymbols, stats.UniqueRejectedSymbols)
	if len(stats.TopAcceptedPaths) != 0 {
		fmt.Println("Top Accepted Paths:")
		for _, line := range memoryCountLines(stats.TopAcceptedPaths) {
			fmt.Println(line)
		}
	}
	if len(stats.TopRejectedPaths) != 0 {
		fmt.Println("Top Rejected Paths:")
		for _, line := range memoryCountLines(stats.TopRejectedPaths) {
			fmt.Println(line)
		}
	}
	if len(stats.TopStalePaths) != 0 {
		fmt.Println("Top Stale Paths:")
		for _, line := range memoryCountLines(stats.TopStalePaths) {
			fmt.Println(line)
		}
	}
	return nil
}

func runMemoryCompact(args []string) error {
	fs := flag.NewFlagSet("memory-compact", flag.ExitOnError)
	repo := fs.String("repo", "", "repo path")
	configPath := fs.String("config", config.DefaultPath(), "config path")
	keepRecent := fs.Int("keep-recent", 3, "keep this many newest duplicates per key")
	dropStale := fs.Bool("drop-stale", false, "drop stale entries during compaction")
	fs.Parse(args)
	if *repo == "" {
		return fmt.Errorf("--repo required")
	}
	runtime, err := config.LoadRuntime(*configPath, *repo)
	if err != nil {
		return err
	}
	count, err := learning.CompactFeedback(runtime.Config.MemoryDir, *repo, *keepRecent, *dropStale, learning.ValidationOptions{
		CBMBinary:      runtime.Config.CBMBinary,
		CBMCacheDir:    runtime.Config.CBMCacheDir,
		TimeoutSeconds: runtime.Config.ToolTimeoutSeconds,
	})
	if err != nil {
		return err
	}
	fmt.Printf("compacted_entries=%d\n", count)
	return nil
}

func runMemoryMaintain(args []string) error {
	fs := flag.NewFlagSet("memory-maintain", flag.ExitOnError)
	repo := fs.String("repo", "", "repo path")
	configPath := fs.String("config", config.DefaultPath(), "config path")
	maxEntries := fs.Int("max-entries", 0, "target upper bound for memory entries; default uses repo profile")
	keepRecent := fs.Int("keep-recent", 3, "keep this many newest duplicates per key")
	dropStale := fs.Bool("drop-stale", true, "drop stale entries during maintenance")
	apply := fs.Bool("apply", false, "rewrite memory store")
	fs.Parse(args)
	if *repo == "" {
		return fmt.Errorf("--repo required")
	}
	runtime, err := config.LoadRuntime(*configPath, *repo)
	if err != nil {
		return err
	}
	stats, err := learning.LoadStats(runtime.Config.MemoryDir, *repo, 5, learning.ValidationOptions{
		CBMBinary:      runtime.Config.CBMBinary,
		CBMCacheDir:    runtime.Config.CBMCacheDir,
		TimeoutSeconds: runtime.Config.ToolTimeoutSeconds,
	})
	if err != nil {
		return err
	}
	resolvedMaxEntries := *maxEntries
	if resolvedMaxEntries <= 0 {
		resolvedMaxEntries = runtime.Profile.MemoryEntryBudget
	}
	shouldCompact := memoryMaintenanceNeeded(stats, resolvedMaxEntries, *dropStale)
	fmt.Printf("entries=%d stale=%d max_entries=%d action_needed=%t\n", stats.Entries, stats.StaleEntries, resolvedMaxEntries, shouldCompact)
	if !shouldCompact || !*apply {
		return nil
	}
	count, err := learning.CompactFeedback(runtime.Config.MemoryDir, *repo, *keepRecent, *dropStale, learning.ValidationOptions{
		CBMBinary:      runtime.Config.CBMBinary,
		CBMCacheDir:    runtime.Config.CBMCacheDir,
		TimeoutSeconds: runtime.Config.ToolTimeoutSeconds,
	})
	if err != nil {
		return err
	}
	fmt.Printf("compacted_entries=%d\n", count)
	return nil
}

func memoryMaintenanceNeeded(stats learning.Stats, maxEntries int, dropStale bool) bool {
	if maxEntries > 0 && stats.Entries > maxEntries {
		return true
	}
	return dropStale && stats.StaleEntries > 0
}

func memoryMaintenanceSummary(stats learning.Stats, maxEntries int, dropStale bool) string {
	needs := memoryMaintenanceNeeded(stats, maxEntries, dropStale)
	reasons := []string{}
	if maxEntries > 0 && stats.Entries > maxEntries {
		reasons = append(reasons, fmt.Sprintf("entries>%d", maxEntries))
	}
	if dropStale && stats.StaleEntries > 0 {
		reasons = append(reasons, fmt.Sprintf("stale=%d", stats.StaleEntries))
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "healthy")
	}
	return fmt.Sprintf("action_needed=%t reason=%s", needs, strings.Join(reasons, ","))
}

func memoryCountLines(items map[string]int) []string {
	type pair struct {
		key   string
		score int
	}
	pairs := make([]pair, 0, len(items))
	for k, v := range items {
		pairs = append(pairs, pair{key: k, score: v})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].score != pairs[j].score {
			return pairs[i].score > pairs[j].score
		}
		return pairs[i].key < pairs[j].key
	})
	lines := make([]string, 0, len(pairs))
	for _, item := range pairs {
		lines = append(lines, fmt.Sprintf("- %d %s", item.score, item.key))
	}
	return lines
}

func runInstall() error {
	target := "/usr/local/bin/agent-explorer"
	if err := os.MkdirAll("/usr/local/bin", 0o755); err != nil {
		return err
	}

	cmd := exec.Command("go", "build", "-o", target, "./cmd/agent-explorer")
	cmd.Dir = "/var/pile/agent-explorer"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install build failed: %w", err)
	}
	fmt.Println(target)
	return nil
}

func runAsk(args []string) error {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	repo := fs.String("repo", "", "repo path to explore")
	query := fs.String("query", "", "natural language question")
	configPath := fs.String("config", config.DefaultPath(), "config path")
	citationOnly := fs.Bool("citation-only", false, "only output final_answer block")
	jsonMode := fs.Bool("json", false, "output json result")
	agentMode := fs.Bool("agent-mode", false, "ultra-compact output for main agent")
	explainMode := fs.Bool("explain", false, "add short LLM summary on top of retrieval pack")
	debugRetrieval := fs.Bool("debug-retrieval", false, "show suppressed retrieval hits")
	parallelRetrieval := fs.Bool("parallel-retrieval", false, "force semantic+graph_text dual-lane when possible")
	timeoutSeconds := fs.Int("timeout", 90, "overall timeout seconds")
	fs.Parse(args)

	if *repo == "" {
		return fmt.Errorf("--repo required")
	}
	if *query == "" {
		return fmt.Errorf("--query required")
	}

	runtime, err := config.LoadRuntime(*configPath, *repo)
	if err != nil {
		return err
	}
	if *parallelRetrieval {
		runtime.Config.ParallelRetrieval = true
	}

	client := llm.New(runtime.Config)
	plan := planner.New(client, runtime.Profile)
	exp := explorer.New(runtime.Config, runtime.Profile, plan, client)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSeconds)*time.Second)
	defer cancel()

	result, err := exp.Run(ctx, *repo, *query, *explainMode)
	if err != nil {
		return err
	}

	if *jsonMode {
		body, err := format.JSON(result)
		if err != nil {
			return err
		}
		fmt.Println(body)
		return nil
	}

	fmt.Println(format.FinalAnswer(result, *citationOnly, *agentMode, *debugRetrieval))
	return nil
}

func runTrace(args []string) error {
	fs := flag.NewFlagSet("trace", flag.ExitOnError)
	repo := fs.String("repo", "", "repo path to explore")
	symbol := fs.String("symbol", "", "symbol/function name")
	query := fs.String("query", "", "query used to resolve symbol")
	direction := fs.String("direction", "both", "inbound|outbound|both")
	depth := fs.Int("depth", 2, "trace depth")
	configPath := fs.String("config", config.DefaultPath(), "config path")
	jsonMode := fs.Bool("json", false, "output json result")
	timeoutSeconds := fs.Int("timeout", 90, "overall timeout seconds")
	fs.Parse(args)

	if *repo == "" {
		return fmt.Errorf("--repo required")
	}
	if *symbol == "" && *query == "" {
		return fmt.Errorf("--symbol or --query required")
	}

	runtime, err := config.LoadRuntime(*configPath, *repo)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSeconds)*time.Second)
	defer cancel()

	target := *symbol
	if target == "" {
		hit, err := tools.ResolveSymbol(ctx, *repo, runtime.Config.CBMBinary, runtime.Config.CBMCacheDir, *query, runtime.Config.ToolTimeoutSeconds)
		if err != nil {
			return err
		}
		target = nonEmpty(hit.Symbol, strings.TrimSuffix(filepathBase(hit.File), ".go"), *query)
	}

	result, err := tools.TracePath(ctx, *repo, runtime.Config.CBMBinary, runtime.Config.CBMCacheDir, target, *direction, *depth, "calls", runtime.Config.ToolTimeoutSeconds)
	if err != nil {
		return err
	}
	if *jsonMode {
		body, err := format.JSON(result)
		if err != nil {
			return err
		}
		fmt.Println(body)
		return nil
	}
	fmt.Println(format.Trace(result))
	return nil
}

type evalCase struct {
	Query           string   `json:"query"`
	ExpectAnyPath   []string `json:"expect_any_path"`
	ExpectAnySymbol []string `json:"expect_any_symbol"`
	ExpectTopPath   []string `json:"expect_top_path"`
	ExpectTopSymbol []string `json:"expect_top_symbol"`
	RejectTopPath   []string `json:"reject_top_path"`
}

type evalSuite struct {
	Name  string     `json:"name"`
	Cases []evalCase `json:"cases"`
}

type evalTaxonomy struct {
	NoHits               int
	WeakTop1             int
	WrongTop1            int
	RejectedTop1         int
	MatchedOnlyBelowTop1 int
}

func runEval(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	repo := fs.String("repo", "", "repo path to explore")
	suitePath := fs.String("suite", "", "eval suite path; default looks inside target repo first")
	configPath := fs.String("config", config.DefaultPath(), "config path")
	autoLearn := fs.Bool("auto-learn", true, "append eval-derived feedback memory")
	limit := fs.Int("limit", 0, "max cases to run")
	workers := fs.Int("workers", 4, "parallel eval workers")
	timeoutSeconds := fs.Int("timeout", 90, "overall timeout seconds per case")
	fs.Parse(args)

	if *repo == "" {
		return fmt.Errorf("--repo required")
	}
	resolvedSuitePath := *suitePath
	if strings.TrimSpace(resolvedSuitePath) == "" {
		resolvedSuitePath = defaultSuitePath(*repo)
	}
	data, err := os.ReadFile(resolvedSuitePath)
	if err != nil {
		return err
	}
	var suite evalSuite
	if err := json.Unmarshal(data, &suite); err != nil {
		return err
	}
	runtime, err := config.LoadRuntime(*configPath, *repo)
	if err != nil {
		return err
	}
	client := llm.New(runtime.Config)
	plan := planner.New(client, runtime.Profile)
	exp := explorer.New(runtime.Config, runtime.Profile, plan, client)

	total := len(suite.Cases)
	if *limit > 0 && *limit < total {
		total = *limit
	}
	if *workers <= 0 {
		*workers = 1
	}
	if *workers > total && total > 0 {
		*workers = total
	}
	if *workers > goruntime.NumCPU()*2 && goruntime.NumCPU() > 0 {
		*workers = goruntime.NumCPU() * 2
	}
	type evalJob struct {
		index int
		item  evalCase
	}
	type evalResult struct {
		index   int
		line    string
		pass    bool
		result  explorer.Result
		item    evalCase
		note    string
		elapsed time.Duration
	}
	jobs := make(chan evalJob, total)
	results := make(chan evalResult, total)
	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutSeconds)*time.Second)
				start := time.Now()
				result, err := exp.Run(ctx, *repo, job.item.Query, false)
				elapsed := time.Since(start)
				cancel()
				if err != nil {
					results <- evalResult{index: job.index, elapsed: elapsed, line: fmt.Sprintf("%02d FAIL %s error=%v", job.index+1, job.item.Query, err)}
					continue
				}
				ok, note := evalMatch(result, job.item)
				if ok {
					if note != "" {
						results <- evalResult{index: job.index, pass: true, item: job.item, result: result, note: note, elapsed: elapsed, line: fmt.Sprintf("%02d PASS %s %s", job.index+1, job.item.Query, note)}
					} else {
						results <- evalResult{index: job.index, pass: true, item: job.item, result: result, note: note, elapsed: elapsed, line: fmt.Sprintf("%02d PASS %s", job.index+1, job.item.Query)}
					}
					continue
				}
				first := ""
				if len(result.Hits) != 0 {
					first = result.Hits[0].File
				}
				if note != "" {
					results <- evalResult{index: job.index, elapsed: elapsed, line: fmt.Sprintf("%02d FAIL %s top=%s note=%s", job.index+1, job.item.Query, first, note)}
				} else {
					results <- evalResult{index: job.index, elapsed: elapsed, line: fmt.Sprintf("%02d FAIL %s top=%s", job.index+1, job.item.Query, first)}
				}
			}
		}()
	}
	for i := 0; i < total; i++ {
		jobs <- evalJob{index: i, item: suite.Cases[i]}
	}
	close(jobs)
	go func() {
		wg.Wait()
		close(results)
	}()

	ordered := make([]string, total)
	pass := 0
	top1 := 0
	top5 := 0
	weak := 0
	statusCounts := map[string]int{}
	confidenceCounts := map[string]int{}
	taxonomy := evalTaxonomy{}
	mrr := 0.0
	recall5 := 0
	latencies := make([]time.Duration, 0, total)
	for item := range results {
		ordered[item.index] = item.line
		latencies = append(latencies, item.elapsed)
		if item.pass {
			pass++
		}
		if len(item.result.Hits) != 0 {
			statusCounts[format.RetrievalStatus(item.result.Hits)]++
			confidenceCounts[strings.TrimSpace(item.result.Hits[0].Confidence)]++
			rank := evalRank(item.result.Hits, item.item)
			if rank == 1 {
				top1++
			}
			if rank > 0 && rank <= 5 {
				top5++
				recall5++
				mrr += 1.0 / float64(rank)
			}
			if item.result.Hits[0].Confidence == "low" {
				weak++
			}
			classifyEvalOutcome(&taxonomy, item.result.Hits, item.item)
		} else {
			statusCounts["abstain"]++
			confidenceCounts["none"]++
			classifyEvalOutcome(&taxonomy, item.result.Hits, item.item)
		}
		if *autoLearn {
			if err := learnFromEval(runtime.Config.MemoryDir, *repo, item.item, item.result, item.pass, item.note); err == nil {
				if item.pass {
					ordered[item.index] += " learned=1"
				}
			}
		}
	}
	for _, line := range ordered {
		if strings.TrimSpace(line) != "" {
			fmt.Println(line)
		}
	}
	fmt.Printf("Summary: %d/%d passed suite=%s\n", pass, total, resolvedSuitePath)
	fmt.Printf("Metrics: top1=%d/%d top5=%d/%d weak_top1=%d/%d\n", top1, total, top5, total, weak, total)
	if total > 0 {
		fmt.Printf("Ranking: mrr=%.3f recall_at_5=%d/%d\n", mrr/float64(total), recall5, total)
	}
	fmt.Printf("Status: grounded=%d weak_evidence=%d abstain=%d\n", statusCounts["grounded"], statusCounts["weak_evidence"], statusCounts["abstain"])
	fmt.Printf("Top1 Confidence: high=%d medium=%d low=%d none=%d\n", confidenceCounts["high"], confidenceCounts["medium"], confidenceCounts["low"], confidenceCounts["none"])
	fmt.Printf("Failure Taxonomy: no_hits=%d weak_top1=%d wrong_top1=%d rejected_top1=%d below_top1_match=%d\n", taxonomy.NoHits, taxonomy.WeakTop1, taxonomy.WrongTop1, taxonomy.RejectedTop1, taxonomy.MatchedOnlyBelowTop1)
	if len(latencies) != 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		fmt.Printf("Latency: p50=%s p95=%s max=%s\n", percentileDuration(latencies, 0.50), percentileDuration(latencies, 0.95), latencies[len(latencies)-1])
	}
	if stats, err := learning.LoadStats(runtime.Config.MemoryDir, *repo, 3, learning.ValidationOptions{
		CBMBinary:      runtime.Config.CBMBinary,
		CBMCacheDir:    runtime.Config.CBMCacheDir,
		TimeoutSeconds: runtime.Config.ToolTimeoutSeconds,
	}); err == nil {
		fmt.Printf("Memory Maintenance: %s\n", memoryMaintenanceSummary(stats, runtime.Profile.MemoryEntryBudget, true))
	}
	return nil
}

func evalRank(hits []tools.Hit, item evalCase) int {
	for i, hit := range hits {
		if i >= 5 {
			break
		}
		if evalHitMatches(hit, item) {
			return i + 1
		}
	}
	return 0
}

func classifyEvalOutcome(t *evalTaxonomy, hits []tools.Hit, item evalCase) {
	if len(hits) == 0 {
		t.NoHits++
		return
	}
	top := hits[0]
	for _, bad := range item.RejectTopPath {
		if strings.Contains(strings.ToLower(top.File), strings.ToLower(bad)) {
			t.RejectedTop1++
			return
		}
	}
	rank := evalRank(hits, item)
	if rank == 1 {
		if strings.TrimSpace(top.Confidence) == "low" {
			t.WeakTop1++
		}
		return
	}
	if rank > 1 {
		t.MatchedOnlyBelowTop1++
		return
	}
	t.WrongTop1++
}

func percentileDuration(items []time.Duration, q float64) time.Duration {
	if len(items) == 0 {
		return 0
	}
	if q <= 0 {
		return items[0]
	}
	if q >= 1 {
		return items[len(items)-1]
	}
	idx := int(float64(len(items)-1) * q)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(items) {
		idx = len(items) - 1
	}
	return items[idx]
}

func learnFromEval(memoryDir string, repo string, item evalCase, result explorer.Result, pass bool, note string) error {
	if strings.TrimSpace(item.Query) == "" || len(result.Hits) == 0 {
		return nil
	}
	if !shouldAutoLearn(result, pass) {
		return nil
	}
	topPath := result.Hits[0].File
	accepts := []string{}
	rejects := []string{}
	if pass {
		switch note {
		case "top_path", "top_symbol":
			accepts = append(accepts, topPath)
		case "path", "symbol":
			for _, hit := range result.Hits {
				if evalHitMatches(hit, item) {
					accepts = append(accepts, hit.File)
					break
				}
			}
			if len(accepts) == 0 {
				return nil
			}
			if !evalPathMatches(topPath, item) {
				rejects = append(rejects, topPath)
			}
		default:
			return nil
		}
	} else {
		for _, bad := range item.RejectTopPath {
			if strings.Contains(strings.ToLower(topPath), strings.ToLower(bad)) {
				rejects = append(rejects, topPath)
				break
			}
		}
	}
	if len(accepts) == 0 && len(rejects) == 0 {
		return nil
	}
	acceptedEvidence := evidenceRefsForPaths(result.Hits, accepts)
	rejectedEvidence := evidenceRefsForPaths(result.Hits, rejects)
	return learning.AppendObservation(memoryDir, repo, learning.Observation{
		Query:            item.Query,
		Accepts:          accepts,
		Rejects:          rejects,
		AcceptedEvidence: acceptedEvidence,
		RejectedEvidence: rejectedEvidence,
		Notes:            "auto-learn eval",
	})
}

func evidenceRefsForPaths(hits []tools.Hit, paths []string) []learning.EvidenceRef {
	if len(paths) == 0 || len(hits) == 0 {
		return nil
	}
	need := map[string]bool{}
	for _, path := range paths {
		need[strings.ToLower(path)] = true
	}
	out := make([]learning.EvidenceRef, 0, len(paths))
	for _, hit := range hits {
		if !need[strings.ToLower(hit.File)] {
			continue
		}
		out = append(out, learning.EvidenceRef{
			Path:         hit.File,
			Symbol:       hit.Symbol,
			LineStart:    hit.LineStart,
			SnippetProbe: hit.Snippet,
		})
	}
	return out
}

func shouldAutoLearn(result explorer.Result, pass bool) bool {
	if len(result.Hits) == 0 {
		return false
	}
	status := format.RetrievalStatus(result.Hits)
	top := strings.TrimSpace(result.Hits[0].Confidence)
	if pass {
		return status == "grounded" && (top == "high" || top == "medium")
	}
	return status == "grounded" && top == "high"
}

func evalHitMatches(hit tools.Hit, item evalCase) bool {
	pathLower := strings.ToLower(hit.File)
	symbolLower := strings.ToLower(hit.Symbol)
	for _, expected := range item.ExpectAnyPath {
		if strings.Contains(pathLower, strings.ToLower(expected)) {
			return true
		}
	}
	for _, expected := range item.ExpectAnySymbol {
		if strings.Contains(symbolLower, strings.ToLower(expected)) {
			return true
		}
	}
	for _, expected := range item.ExpectTopPath {
		if strings.Contains(pathLower, strings.ToLower(expected)) {
			return true
		}
	}
	for _, expected := range item.ExpectTopSymbol {
		if strings.Contains(symbolLower, strings.ToLower(expected)) {
			return true
		}
	}
	return false
}

func evalPathMatches(path string, item evalCase) bool {
	path = strings.ToLower(path)
	for _, expected := range item.ExpectAnyPath {
		if strings.Contains(path, strings.ToLower(expected)) {
			return true
		}
	}
	for _, expected := range item.ExpectTopPath {
		if strings.Contains(path, strings.ToLower(expected)) {
			return true
		}
	}
	return false
}

func runInitEval(args []string) error {
	fs := flag.NewFlagSet("init-eval", flag.ExitOnError)
	repo := fs.String("repo", "", "repo path")
	configPath := fs.String("config", config.DefaultPath(), "config path")
	force := fs.Bool("force", false, "overwrite existing suite")
	fs.Parse(args)

	if *repo == "" {
		return fmt.Errorf("--repo required")
	}
	runtime, err := config.LoadRuntime(*configPath, *repo)
	if err != nil {
		return err
	}
	suite := buildEvalSuite(*repo, runtime)
	data, err := json.MarshalIndent(suite, "", "  ")
	if err != nil {
		return err
	}
	targetDir := *repo + "/.agent-explorer"
	targetPath := targetDir + "/eval.json"
	if !*force {
		if _, err := os.Stat(targetPath); err == nil {
			return fmt.Errorf("suite already exists: %s", targetPath)
		}
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(targetPath, data, 0o644); err != nil {
		return err
	}
	fmt.Println(targetPath)
	return nil
}

func runInitProfile(args []string) error {
	fs := flag.NewFlagSet("init-profile", flag.ExitOnError)
	repo := fs.String("repo", "", "repo path")
	force := fs.Bool("force", false, "overwrite existing profile")
	fs.Parse(args)

	if *repo == "" {
		return fmt.Errorf("--repo required")
	}
	profile := config.RepoProfile{
		Repo:              *repo,
		Name:              filepathBase(*repo),
		Stack:             config.DetectStack(*repo),
		PrecisionFirst:    true,
		MaxToolFamilies:   2,
		MemoryEntryBudget: 1000,
	}
	switch {
	case strings.Contains(profile.Stack, "go"):
		profile.PreferredPrimary = []string{"graph", "graph_text", "semantic"}
		profile.QueryHints = []string{"prefer symbol/call graph for exported funcs and methods", "use semantic for conceptual behavior when exact symbol unknown"}
		profile.NegativeHints = []string{"avoid rg-first for symbol lookup"}
	case strings.Contains(profile.Stack, "python"):
		profile.PreferredPrimary = []string{"graph_text", "rg", "semantic"}
		profile.QueryHints = []string{"framework behavior may hide behind decorators and routers", "use rg for exact error/config literals"}
		profile.NegativeHints = []string{"avoid semantic-first for exact decorator names"}
	case strings.Contains(profile.Stack, "node"):
		profile.PreferredPrimary = []string{"graph_text", "semantic", "rg"}
		profile.QueryHints = []string{"components/providers/hooks may be better by graph_text than symbol graph", "use rg for env/config literals"}
		profile.NegativeHints = []string{"avoid astgrep unless syntax shape requested"}
	default:
		profile.PreferredPrimary = []string{"graph", "graph_text", "semantic"}
	}
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return err
	}
	targetDir := *repo + "/.agent-explorer"
	targetPath := targetDir + "/profile.json"
	if !*force {
		if _, err := os.Stat(targetPath); err == nil {
			return fmt.Errorf("profile already exists: %s", targetPath)
		}
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(targetPath, data, 0o644); err != nil {
		return err
	}
	fmt.Println(targetPath)
	return nil
}

func evalMatch(result explorer.Result, item evalCase) (bool, string) {
	if len(result.Hits) == 0 {
		return false, "no_hits"
	}
	topPath := strings.ToLower(result.Hits[0].File)
	topSymbol := strings.ToLower(result.Hits[0].Symbol)
	for _, rejected := range item.RejectTopPath {
		if strings.Contains(topPath, strings.ToLower(rejected)) {
			return false, "rejected_top_path"
		}
	}
	if len(item.ExpectTopPath) != 0 || len(item.ExpectTopSymbol) != 0 {
		for _, expected := range item.ExpectTopPath {
			if strings.Contains(topPath, strings.ToLower(expected)) {
				return true, "top_path"
			}
		}
		for _, expected := range item.ExpectTopSymbol {
			if strings.Contains(topSymbol, strings.ToLower(expected)) {
				return true, "top_symbol"
			}
		}
		return false, "top_miss"
	}
	for _, hit := range result.Hits {
		pathLower := strings.ToLower(hit.File)
		symbolLower := strings.ToLower(hit.Symbol)
		for _, expected := range item.ExpectAnyPath {
			if strings.Contains(pathLower, strings.ToLower(expected)) {
				return true, "path"
			}
		}
		for _, expected := range item.ExpectAnySymbol {
			if strings.Contains(symbolLower, strings.ToLower(expected)) {
				return true, "symbol"
			}
		}
	}
	return false, "miss"
}

func filepathBase(path string) string {
	parts := strings.Split(strings.ReplaceAll(path, "\\", "/"), "/")
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}

func defaultSuitePath(repo string) string {
	candidates := []string{
		repo + "/.agent-explorer-eval.json",
		repo + "/.agent-explorer/eval.json",
		"/var/pile/agent-explorer/evals/template.generic.json",
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return candidates[0]
}

func buildEvalSuite(repo string, runtime config.Runtime) evalSuite {
	cases := []evalCase{}
	seenQueries := map[string]bool{}
	addCase := func(query string, paths []string, symbols []string) {
		if len(cases) >= 30 {
			return
		}
		key := strings.ToLower(strings.TrimSpace(query))
		if seenQueries[key] {
			return
		}
		seenQueries[key] = true
		cases = append(cases, evalCase{Query: query, ExpectAnyPath: paths, ExpectAnySymbol: symbols})
	}

	switch {
	case strings.Contains(runtime.Profile.Stack, "go"):
		addCase("where auth middleware defined", []string{"middleware/auth", "auth.go"}, []string{"AuthMiddleware", "Authenticate", "RequireRole"})
		addCase("which funcs call ClaimsFromContext", []string{"middleware/auth", "handler/auth"}, []string{"ClaimsFromContext"})
		addCase("find missing authorization header error", []string{"middleware/auth"}, nil)
	case strings.Contains(runtime.Profile.Stack, "python"):
		addCase("where auth decorator defined", []string{"auth", "security"}, []string{"require_auth", "auth_required"})
		addCase("find unauthorized error", []string{"auth", "security"}, nil)
	case strings.Contains(runtime.Profile.Stack, "node"):
		addCase("where auth provider defined", []string{"auth-provider", "provider", "auth"}, []string{"useAuth", "AuthProvider"})
		addCase("find missing authorization header error", []string{"auth", "middleware"}, nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	hits, err := tools.SearchGraph(ctx, repo, runtime.Config.CBMBinary, runtime.Config.CBMCacheDir, "", runtime.Config.ToolTimeoutSeconds, 60)
	if err == nil {
		seenFiles := map[string]bool{}
		for _, hit := range hits {
			if len(cases) >= 30 {
				break
			}
			if shouldSkipSeedHit(hit) || seenFiles[hit.File] {
				continue
			}
			seenFiles[hit.File] = true
			base := filepathBase(hit.File)
			dir := strings.TrimSuffix(hit.File, "/"+base)
			if hit.Symbol != "" {
				addCase(fmt.Sprintf("where is %s defined", tailSymbol(hit.Symbol)), []string{base, dir}, []string{tailSymbol(hit.Symbol)})
				addCase(fmt.Sprintf("how does %s work", tailSymbol(hit.Symbol)), []string{base, dir}, []string{tailSymbol(hit.Symbol)})
			}
		}
	}

	templateData, err := os.ReadFile("/var/pile/agent-explorer/evals/template.generic.json")
	if err == nil && len(cases) < 10 {
		var generic evalSuite
		if json.Unmarshal(templateData, &generic) == nil {
			for _, item := range generic.Cases {
				if len(cases) >= 30 {
					break
				}
				addCase(item.Query, item.ExpectAnyPath, item.ExpectAnySymbol)
			}
		}
	}
	if len(cases) > 30 {
		cases = cases[:30]
	}
	return evalSuite{
		Name:  fmt.Sprintf("%s explore suite", filepathBase(repo)),
		Cases: cases,
	}
}

func shouldSkipSeedHit(hit tools.Hit) bool {
	path := strings.ToLower(hit.File)
	symbol := strings.ToLower(hit.Symbol)
	if strings.Contains(path, "/test") || strings.Contains(path, ".pb.") || strings.Contains(path, "/proto/") || strings.HasSuffix(path, "makefile") {
		return true
	}
	if strings.Contains(symbol, ".phony") || strings.TrimSpace(symbol) == "" {
		return true
	}
	return false
}

func tailSymbol(symbol string) string {
	parts := strings.Split(symbol, ".")
	if len(parts) == 0 {
		return symbol
	}
	return parts[len(parts)-1]
}

func nonEmpty(items ...string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return ""
}

func usage() {
	fmt.Println(`agent-explorer

Commands:
  ask       explore repo and emit compact citations
  trace     show compact caller/callee trace
  feedback  store accepted/rejected evidence for future ranking
  memory    inspect retrieval memory for repo
  memory-compact compact retrieval memory for repo
  memory-maintain audit/apply memory maintenance policy
  eval      run eval suite
  init-eval scaffold repo-local eval suite
  init-profile scaffold repo-local profile
  install   install binary to /usr/local/bin
  version   print version

Example:
  agent-explorer ask --repo /www/wwwroot/gass/be --query "where retry logic lives"`)
}
