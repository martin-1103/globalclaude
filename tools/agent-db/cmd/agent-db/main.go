package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"agent-db/internal/agent"
	"agent-db/internal/config"
	"agent-db/internal/db"
	"agent-db/internal/format"
	"agent-db/internal/llm"
	"agent-db/internal/logs"
)

func main() {
	// Subcommands are dispatched before the query-mode flag parsing.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			if err := runInit(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			return
		case "projects":
			if err := runProjects(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			return
		case "-h", "--help", "help":
			usage()
			return
		}
	}

	fs := flag.NewFlagSet("agent-db", flag.ExitOnError)
	projectDir := fs.String("project", "", "project path for per-project config/context (default: cwd)")
	configPath := fs.String("config", "", "global config path (default: /var/pile/agent-db/config.json)")
	jsonMode := fs.Bool("json", false, "output raw JSON result")
	verbose := fs.Bool("verbose", false, "show all steps (default: answer only)")
	timeoutSeconds := fs.Int("timeout", 0, "overall timeout seconds (default: max_turns * llm_timeout)")
	fs.Usage = usage
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}

	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		usage()
		os.Exit(1)
	}

	if err := run(*configPath, *projectDir, query, *jsonMode, *verbose, *timeoutSeconds); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(configPath, projectDir, query string, jsonMode bool, verbose bool, timeoutSeconds int) (runErr error) {
	dir := projectDir
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
		dir = cwd
	}
	startedAt := time.Now().UTC()
	runDir, err := logs.EnsureRunDir(dir, query)
	if err != nil {
		return err
	}
	req := logs.Request{
		Command:        "query",
		RunDir:         runDir,
		ProjectDir:     dir,
		ConfigPath:     configPath,
		Query:          query,
		JSONMode:       jsonMode,
		Verbose:        verbose,
		TimeoutSeconds: timeoutSeconds,
		StartedAt:      startedAt.Format(time.RFC3339),
	}
	if err := logs.SaveStarted(runDir, req); err != nil {
		return err
	}
	finalized := false
	var res agent.Result
	defer func() {
		if finalized {
			return
		}
		finishedAt := time.Now().UTC()
		if rec := recover(); rec != nil {
			err := fmt.Errorf("panic: %v", rec)
			summary := logs.BuildSummary("failed", query, startedAt, finishedAt, res.Turns, len(res.Steps), lastIntent(res.Steps), res.Answer, res.MaxTurns, err)
			_ = logs.SaveFailure(runDir, req, err, summary)
			panic(rec)
		}
		if runErr != nil {
			summary := logs.BuildSummary("failed", query, startedAt, finishedAt, res.Turns, len(res.Steps), lastIntent(res.Steps), res.Answer, res.MaxTurns, runErr)
			_ = logs.SaveFailure(runDir, req, runErr, summary)
			return
		}
		err := fmt.Errorf("process exited before success finalize")
		summary := logs.BuildSummary("failed", query, startedAt, finishedAt, res.Turns, len(res.Steps), lastIntent(res.Steps), res.Answer, res.MaxTurns, err)
		_ = logs.SaveFailure(runDir, req, err, summary)
	}()

	cfg, err := config.Load(configPath, dir)
	if err != nil {
		return err
	}

	client := llm.New(cfg)
	tools := db.New(cfg)
	ag := agent.New(cfg, client, tools)

	if timeoutSeconds <= 0 {
		timeoutSeconds = cfg.MaxTurns * cfg.LLMTimeoutSeconds
		if timeoutSeconds <= 0 {
			timeoutSeconds = 300
		}
	}
	baseCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(baseCtx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	ctx = logs.WithLLMLogger(ctx, logs.NewLLMLogger(runDir))

	res, err = ag.Run(ctx, query)
	if err != nil {
		return err
	}
	summary := logs.BuildSummary("ok", query, startedAt, time.Now().UTC(), res.Turns, len(res.Steps), lastIntent(res.Steps), res.Answer, res.MaxTurns, nil)
	if err := logs.SaveSuccess(runDir, req, res, summary); err != nil {
		return err
	}
	finalized = true
	_ = logs.Prune(dir, 50, 14*24*time.Hour)

	if jsonMode {
		body, err := format.JSON(res)
		if err != nil {
			return err
		}
		fmt.Println(body)
		return nil
	}

	fmt.Println(format.Human(res, verbose))
	return nil
}

func lastIntent(steps []agent.Step) string {
	for i := len(steps) - 1; i >= 0; i-- {
		if strings.TrimSpace(steps[i].Intent) != "" {
			return steps[i].Intent
		}
	}
	return ""
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("agent-db init", flag.ExitOnError)
	projectPath := fs.String("project", "", "project path to initialize (default: cwd)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := *projectPath
	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
		path = cwd
	}
	cfgPath, ctxPath, err := config.InitProject(path)
	if err != nil {
		return err
	}
	fmt.Printf("created project config: %s\n", cfgPath)
	fmt.Printf("created project context: %s\n", ctxPath)
	fmt.Println("edit the config above to set containers/credentials, then run agent-db --project", path)
	return nil
}

func runProjects(args []string) error {
	fs := flag.NewFlagSet("agent-db projects", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	slugs, err := config.ListProjects()
	if err != nil {
		return err
	}
	if len(slugs) == 0 {
		fmt.Println("(no projects)")
		return nil
	}
	for _, s := range slugs {
		fmt.Println(s)
	}
	return nil
}

func usage() {
	fmt.Println(`agent-db — natural-language database query agent

Usage:
  agent-db [flags] "your question"
  agent-db init --project /path/to/project
  agent-db projects

Subcommands:
  init       create per-project config.json + context.md under /var/pile/agent-db/projects/<slug>/
  projects   list all known project slugs

Flags:
  --project PATH   project path for per-project config/context (default: cwd)
  --config PATH    global config path (default: /var/pile/agent-db/config.json)
  --json           output raw JSON result
  --timeout N      overall timeout seconds

Examples:
  agent-db "berapa order gagal hari ini"
  agent-db --project /path/to/project "query pertanyaan"
  agent-db --json "query"
  agent-db init --project /www/wwwroot/gass/be
  agent-db projects`)
}
