package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	BaseURL            string `json:"base_url"`
	APIKey             string `json:"api_key"`
	Model              string `json:"model"`
	CBMBinary          string `json:"cbm_binary"`
	CBMCacheDir        string `json:"cbm_cache_dir"`
	ClaudeContextCmd   string `json:"claude_context_command"`
	MemoryDir          string `json:"memory_dir"`
	ParallelRetrieval  bool   `json:"parallel_retrieval"`
	ToolTimeoutSeconds int    `json:"tool_timeout_seconds"`
	MaxSearchResults   int    `json:"max_search_results"`
	MaxSnippetLines    int    `json:"max_snippet_lines"`
	LLMTimeoutSeconds  int    `json:"llm_timeout_seconds"`
}

type RepoProfile struct {
	Repo              string              `json:"repo,omitempty"`
	Name              string              `json:"name,omitempty"`
	Stack             string              `json:"stack,omitempty"`
	PreferredPrimary  []string            `json:"preferred_primary,omitempty"`
	DisableSemantic   bool                `json:"disable_semantic,omitempty"`
	PrecisionFirst    bool                `json:"precision_first,omitempty"`
	MaxToolFamilies   int                 `json:"max_tool_families,omitempty"`
	MemoryEntryBudget int                 `json:"memory_entry_budget,omitempty"`
	StaleLineDistance int                 `json:"stale_line_distance,omitempty"`
	QueryHints        []string            `json:"query_hints,omitempty"`
	NegativeHints     []string            `json:"negative_hints,omitempty"`
	ConceptOverlays   map[string][]string `json:"concept_overlays,omitempty"`
}

type Runtime struct {
	Config  Config
	Profile RepoProfile
}

func DefaultPath() string {
	return "/var/pile/agent-explorer/config.json"
}

func Load(path string) (Config, error) {
	if path == "" {
		path = DefaultPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	applyEnvOverrides(&cfg)
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func LoadRuntime(path string, repo string) (Runtime, error) {
	cfg, err := Load(path)
	if err != nil {
		return Runtime{}, err
	}
	profile, err := LoadRepoProfile(repo)
	if err != nil {
		return Runtime{}, err
	}
	if profile.Repo == "" {
		profile.Repo = repo
	}
	if profile.Stack == "" {
		profile.Stack = DetectStack(repo)
	}
	if !profile.PrecisionFirst {
		profile.PrecisionFirst = true
	}
	if profile.MaxToolFamilies <= 0 {
		profile.MaxToolFamilies = 2
	}
	if profile.MemoryEntryBudget <= 0 {
		profile.MemoryEntryBudget = 1000
	}
	if profile.StaleLineDistance <= 0 {
		profile.StaleLineDistance = 40
	}
	return Runtime{Config: cfg, Profile: profile}, nil
}

func LoadRepoProfile(repo string) (RepoProfile, error) {
	candidates := []string{
		filepath.Join(repo, ".agent-explorer", "profile.json"),
		filepath.Join(repo, ".agent-explorer-profile.json"),
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		var profile RepoProfile
		if err := json.Unmarshal(data, &profile); err != nil {
			return RepoProfile{}, fmt.Errorf("parse repo profile %s: %w", candidate, err)
		}
		return profile, nil
	}
	return RepoProfile{}, nil
}

func DetectStack(repo string) string {
	type marker struct {
		file  string
		stack string
	}
	markers := []marker{
		{"go.mod", "go"},
		{"package.json", "node"},
		{"pnpm-workspace.yaml", "node"},
		{"pyproject.toml", "python"},
		{"requirements.txt", "python"},
		{"Cargo.toml", "rust"},
		{"composer.json", "php"},
		{"pom.xml", "java"},
		{"build.gradle", "java"},
		{"Gemfile", "ruby"},
	}
	detected := []string{}
	for _, item := range markers {
		if _, err := os.Stat(filepath.Join(repo, item.file)); err == nil {
			detected = append(detected, item.stack)
		}
	}
	if len(detected) == 0 {
		return "unknown"
	}
	seen := map[string]bool{}
	ordered := []string{}
	for _, item := range detected {
		if seen[item] {
			continue
		}
		seen[item] = true
		ordered = append(ordered, item)
	}
	return strings.Join(ordered, "+")
}

func (c *Config) applyDefaults() {
	if c.ToolTimeoutSeconds <= 0 {
		c.ToolTimeoutSeconds = 20
	}
	if c.MaxSearchResults <= 0 {
		c.MaxSearchResults = 8
	}
	if c.MaxSnippetLines <= 0 {
		c.MaxSnippetLines = 40
	}
	if c.LLMTimeoutSeconds <= 0 {
		c.LLMTimeoutSeconds = 45
	}
	if strings.TrimSpace(c.MemoryDir) == "" {
		c.MemoryDir = "/var/pile/agent-explorer/data"
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
	if c.CBMBinary == "" {
		return fmt.Errorf("config cbm_binary empty")
	}
	return nil
}

func applyEnvOverrides(cfg *Config) {
	overrideString(&cfg.BaseURL, "AGENT_EXPLORER_BASE_URL")
	overrideString(&cfg.APIKey, "AGENT_EXPLORER_API_KEY")
	overrideString(&cfg.Model, "AGENT_EXPLORER_MODEL")
	overrideString(&cfg.CBMBinary, "AGENT_EXPLORER_CBM_BINARY")
	overrideString(&cfg.CBMCacheDir, "AGENT_EXPLORER_CBM_CACHE_DIR")
	overrideString(&cfg.ClaudeContextCmd, "AGENT_EXPLORER_CLAUDE_CONTEXT_COMMAND")
	overrideString(&cfg.MemoryDir, "AGENT_EXPLORER_MEMORY_DIR")
	overrideBool(&cfg.ParallelRetrieval, "AGENT_EXPLORER_PARALLEL_RETRIEVAL")
	overrideInt(&cfg.ToolTimeoutSeconds, "AGENT_EXPLORER_TOOL_TIMEOUT_SECONDS")
	overrideInt(&cfg.MaxSearchResults, "AGENT_EXPLORER_MAX_SEARCH_RESULTS")
	overrideInt(&cfg.MaxSnippetLines, "AGENT_EXPLORER_MAX_SNIPPET_LINES")
	overrideInt(&cfg.LLMTimeoutSeconds, "AGENT_EXPLORER_LLM_TIMEOUT_SECONDS")
}

func overrideString(target *string, env string) {
	if v := os.Getenv(env); v != "" {
		*target = v
	}
}

func overrideInt(target *int, env string) {
	if v := os.Getenv(env); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			*target = parsed
		}
	}
}

func overrideBool(target *bool, env string) {
	if v := strings.TrimSpace(strings.ToLower(os.Getenv(env))); v != "" {
		*target = v == "1" || v == "true" || v == "yes" || v == "on"
	}
}
