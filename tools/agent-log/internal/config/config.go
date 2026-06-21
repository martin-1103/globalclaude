package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	BaseURL            string `json:"base_url"`
	APIKey             string `json:"api_key"`
	Model              string `json:"model"`
	MaxDisplayLines    int    `json:"max_display_lines"`
	ToolTimeoutSeconds int    `json:"tool_timeout_seconds"`
	LLMTimeoutSeconds  int    `json:"llm_timeout_seconds"`
	MaxTurns           int    `json:"max_turns"`
	VLogsURL           string `json:"vlogs_url"`
	GasslogPath        string `json:"gasslog_path"`
}

const DefaultConfigPath = "/var/pile/agent-log/config.json"

func Load(configPath string) (Config, error) {
	if configPath == "" {
		configPath = DefaultConfigPath
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", configPath, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	applyDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.MaxDisplayLines <= 0 {
		cfg.MaxDisplayLines = 80
	}
	if cfg.ToolTimeoutSeconds <= 0 {
		cfg.ToolTimeoutSeconds = 30
	}
	if cfg.LLMTimeoutSeconds <= 0 {
		cfg.LLMTimeoutSeconds = 60
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 15
	}
	if cfg.VLogsURL == "" {
		cfg.VLogsURL = "http://localhost:9428"
	}
	if cfg.GasslogPath == "" {
		cfg.GasslogPath = "~/.claude/skills/haiku-logs/gasslog.sh"
	}
	// Expand ~ in GasslogPath
	if strings.HasPrefix(cfg.GasslogPath, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			cfg.GasslogPath = filepath.Join(home, cfg.GasslogPath[2:])
		}
	}
}

func (c Config) Validate() error {
	if c.BaseURL == "" {
		return fmt.Errorf("config base_url empty")
	}
	if c.APIKey == "" {
		return fmt.Errorf("config api_key empty")
	}
	if c.Model == "" {
		return fmt.Errorf("config model empty")
	}
	return nil
}
