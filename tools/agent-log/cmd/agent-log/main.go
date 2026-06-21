package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"agent-log/internal/agent"
	"agent-log/internal/config"
	"agent-log/internal/format"
	"agent-log/internal/llm"
	"agent-log/internal/logs"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			usage()
			return
		}
	}

	fs := flag.NewFlagSet("agent-log", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.json (default: /var/pile/agent-log/config.json)")
	vlogsURL := fs.String("vlogs", "", "override vlogs_url from config")
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

	if err := run(*configPath, *vlogsURL, query, *jsonMode, *verbose, *timeoutSeconds); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(configPath, vlogsURL, query string, jsonMode bool, verbose bool, timeoutSeconds int) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	if vlogsURL != "" {
		cfg.VLogsURL = vlogsURL
	}

	client := llm.New(cfg)
	tools := logs.New(cfg)
	ag := agent.New(cfg, client, tools)

	if timeoutSeconds <= 0 {
		timeoutSeconds = cfg.MaxTurns * cfg.LLMTimeoutSeconds
		if timeoutSeconds <= 0 {
			timeoutSeconds = 300
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	res, err := ag.Run(ctx, query)
	if err != nil {
		return err
	}

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

func usage() {
	fmt.Println(`agent-log — natural-language log query agent

Usage:
  agent-log [flags] "your question"

Flags:
  --config PATH   path to config.json (default: /var/pile/agent-log/config.json)
  --vlogs URL     override VictoriaLogs URL
  --json          output raw JSON result
  --timeout N     overall timeout seconds`)
}
