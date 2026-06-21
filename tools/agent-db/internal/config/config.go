package config

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Config is the merged configuration: global config.json with optional
// per-project /var/pile/agent-db/projects/<slug>/config.json overrides layered
// on top.
type Config struct {
	BaseURL            string `json:"base_url"`
	APIKey             string `json:"api_key"`
	Model              string `json:"model"`
	QueryLimit         int    `json:"query_limit"`
	MaxDisplayRows     int    `json:"max_display_rows"`
	ToolTimeoutSeconds int    `json:"tool_timeout_seconds"`
	LLMTimeoutSeconds  int    `json:"llm_timeout_seconds"`
	MaxTurns           int    `json:"max_turns"`

	// Project-level fields (only present in per-project config.json).
	Containers  Containers  `json:"containers"`
	Credentials Credentials `json:"credentials"`
	EnvFile     string      `json:"env_file"`
	Notes       string      `json:"notes"`

	// Runtime-only fields (not serialized): identify the resolved project so the
	// agent can read/append the project's context.md for self-learning.
	Slug        string `json:"-"`
	ContextPath string `json:"-"`
	Context     string `json:"-"`
}

type Containers struct {
	ClickHouse string `json:"clickhouse"`
	MySQL      string `json:"mysql"`
	Redis      string `json:"redis"`
	Postgres   string `json:"postgres"`
}

type Credentials struct {
	ClickHouseUser     string `json:"clickhouse_user"`
	ClickHousePassword string `json:"clickhouse_password"`
	ClickHouseDB       string `json:"clickhouse_db"`
	MySQLUser          string `json:"mysql_user"`
	MySQLPassword      string `json:"mysql_password"`
	MySQLDB            string `json:"mysql_db"`
	RedisDB            string `json:"redis_db"`
	PostgresUser       string `json:"postgres_user"`
	PostgresPassword   string `json:"postgres_password"`
	PostgresDB         string `json:"postgres_db"`
}

// ProjectConfig mirrors only the project-overridable fields so we can detect
// which fields were actually set and let them win over the global config.
type ProjectConfig struct {
	BaseURL            *string      `json:"base_url,omitempty"`
	APIKey             *string      `json:"api_key,omitempty"`
	Model              *string      `json:"model,omitempty"`
	QueryLimit         *int         `json:"query_limit,omitempty"`
	MaxDisplayRows     *int         `json:"max_display_rows,omitempty"`
	ToolTimeoutSeconds *int         `json:"tool_timeout_seconds,omitempty"`
	LLMTimeoutSeconds  *int         `json:"llm_timeout_seconds,omitempty"`
	MaxTurns           *int         `json:"max_turns,omitempty"`
	Containers         *Containers  `json:"containers,omitempty"`
	Credentials        *Credentials `json:"credentials,omitempty"`
	EnvFile            *string      `json:"env_file,omitempty"`
	Notes              *string      `json:"notes,omitempty"`
}

// Root is the base directory holding the global config and per-project dirs.
const Root = "/var/pile/agent-db"

func DefaultPath() string {
	return filepath.Join(Root, "config.json")
}

// ProjectsDir is the parent of all per-project directories.
func ProjectsDir() string {
	return filepath.Join(Root, "projects")
}

// Slug converts an absolute project path into a filesystem-safe slug:
// "/" → "__" (double underscore to avoid collision) and "." → "_".
func Slug(projectPath string) string {
	projectPath = strings.TrimRight(projectPath, "/")
	s := strings.ReplaceAll(projectPath, "/", "__")
	s = strings.ReplaceAll(s, ".", "_")
	s = strings.TrimPrefix(s, "__")
	return s
}

// ProjectDir returns /var/pile/agent-db/projects/<slug>.
func ProjectDir(slug string) string {
	return filepath.Join(ProjectsDir(), slug)
}

// ProjectConfigPath returns the per-project config.json path.
func ProjectConfigPath(slug string) string {
	return filepath.Join(ProjectDir(slug), "config.json")
}

// ContextPath returns the per-project context.md path.
func ContextPath(slug string) string {
	return filepath.Join(ProjectDir(slug), "context.md")
}

// Load builds the merged config:
//  1. read global config (configPath or default)
//  2. compute slug from projectDir and merge the per-project config.json (project wins)
//  3. read the per-project context.md (if any) into cfg.Context
//  4. if env_file set, source it and try to auto-detect containers via docker ps
func Load(configPath string, projectDir string) (Config, error) {
	if configPath == "" {
		configPath = DefaultPath()
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("read global config %s: %w", configPath, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse global config: %w", err)
	}

	if projectDir == "" {
		projectDir = "."
	}
	if abs, err := filepath.Abs(projectDir); err == nil {
		projectDir = abs
	}
	slug := Slug(projectDir)
	cfg.Slug = slug
	cfg.ContextPath = ContextPath(slug)

	projPath := ProjectConfigPath(slug)
	if projData, err := os.ReadFile(projPath); err == nil {
		var proj ProjectConfig
		if err := json.Unmarshal(projData, &proj); err != nil {
			return Config{}, fmt.Errorf("parse project config %s: %w", projPath, err)
		}
		mergeProject(&cfg, proj)
	} else if !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("read project config %s: %w", projPath, err)
	}

	if ctxData, err := os.ReadFile(cfg.ContextPath); err == nil {
		cfg.Context = string(ctxData)
	} else if !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("read project context %s: %w", cfg.ContextPath, err)
	}

	if strings.TrimSpace(cfg.EnvFile) != "" {
		envPath := cfg.EnvFile
		if !filepath.IsAbs(envPath) {
			envPath = filepath.Join(projectDir, envPath)
		}
		applyEnvFile(&cfg, envPath)
	}

	autoDetectContainers(&cfg)
	autoDetectCredentials(&cfg)
	persistDiscovered(&cfg)
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// InitProject creates the per-project directory with a template config.json and
// an empty context.md. It returns the config path so the caller can print it.
// It refuses to overwrite an existing config.json.
func InitProject(projectPath string) (configPath string, contextPath string, err error) {
	if abs, e := filepath.Abs(projectPath); e == nil {
		projectPath = abs
	}
	slug := Slug(projectPath)
	dir := ProjectDir(slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("create project dir %s: %w", dir, err)
	}
	cfgPath := ProjectConfigPath(slug)
	ctxPath := ContextPath(slug)

	if _, err := os.Stat(cfgPath); err == nil {
		return "", "", fmt.Errorf("project config already exists: %s", cfgPath)
	} else if !os.IsNotExist(err) {
		return "", "", fmt.Errorf("stat %s: %w", cfgPath, err)
	}

	tmpl := ProjectConfig{
		Containers:  &Containers{},
		Credentials: &Credentials{},
		EnvFile:     strPtr(""),
		Notes:       strPtr(""),
		QueryLimit:  intPtr(0),
		MaxTurns:    intPtr(0),
	}
	body, err := json.MarshalIndent(tmpl, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("marshal template: %w", err)
	}
	if err := os.WriteFile(cfgPath, append(body, '\n'), 0o644); err != nil {
		return "", "", fmt.Errorf("write %s: %w", cfgPath, err)
	}

	if _, err := os.Stat(ctxPath); os.IsNotExist(err) {
		if err := os.WriteFile(ctxPath, []byte{}, 0o644); err != nil {
			return "", "", fmt.Errorf("write %s: %w", ctxPath, err)
		}
	}
	return cfgPath, ctxPath, nil
}

// ListProjects returns the slugs of all per-project directories.
func ListProjects() ([]string, error) {
	entries, err := os.ReadDir(ProjectsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read projects dir: %w", err)
	}
	var slugs []string
	for _, e := range entries {
		if e.IsDir() {
			slugs = append(slugs, e.Name())
		}
	}
	return slugs, nil
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

func mergeProject(cfg *Config, p ProjectConfig) {
	if p.BaseURL != nil {
		cfg.BaseURL = *p.BaseURL
	}
	if p.APIKey != nil {
		cfg.APIKey = *p.APIKey
	}
	if p.Model != nil {
		cfg.Model = *p.Model
	}
	if p.QueryLimit != nil {
		cfg.QueryLimit = *p.QueryLimit
	}
	if p.MaxDisplayRows != nil {
		cfg.MaxDisplayRows = *p.MaxDisplayRows
	}
	if p.ToolTimeoutSeconds != nil {
		cfg.ToolTimeoutSeconds = *p.ToolTimeoutSeconds
	}
	if p.LLMTimeoutSeconds != nil {
		cfg.LLMTimeoutSeconds = *p.LLMTimeoutSeconds
	}
	if p.MaxTurns != nil {
		cfg.MaxTurns = *p.MaxTurns
	}
	if p.Containers != nil {
		cfg.Containers = *p.Containers
	}
	if p.Credentials != nil {
		cfg.Credentials = *p.Credentials
	}
	if p.EnvFile != nil {
		cfg.EnvFile = *p.EnvFile
	}
	if p.Notes != nil {
		cfg.Notes = *p.Notes
	}
}

// applyEnvFile sources the env file (best-effort) and fills credential fields
// that are still empty from matching env vars. It mirrors:
//
//	set -a; source <env_file>; set +a
//
// by parsing KEY=VALUE lines; values already set in the config are kept.
func applyEnvFile(cfg *Config, envPath string) {
	f, err := os.Open(envPath)
	if err != nil {
		return
	}
	defer f.Close()

	env := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, `"'`)
		env[key] = val
		// Export into the process so docker exec inherits it too.
		os.Setenv(key, val)
	}

	pick := func(target *string, keys ...string) {
		if strings.TrimSpace(*target) != "" {
			return
		}
		for _, k := range keys {
			if v, ok := env[k]; ok && v != "" {
				*target = v
				return
			}
		}
	}

	c := &cfg.Credentials
	pick(&c.ClickHouseUser, "CLICKHOUSE_USER", "CH_USER")
	pick(&c.ClickHousePassword, "CLICKHOUSE_PASSWORD", "CH_PASSWORD")
	pick(&c.ClickHouseDB, "CLICKHOUSE_DB", "CH_DB", "CLICKHOUSE_DATABASE")
	pick(&c.MySQLUser, "MYSQL_USER", "DB_USER", "DB_USERNAME")
	pick(&c.MySQLPassword, "MYSQL_PASSWORD", "DB_PASSWORD")
	pick(&c.MySQLDB, "MYSQL_DB", "MYSQL_DATABASE", "DB_DATABASE", "DB_NAME")
	pick(&c.RedisDB, "REDIS_DB", "REDIS_DATABASE")
	pick(&c.PostgresUser, "POSTGRES_USER", "PG_USER", "PGUSER")
	pick(&c.PostgresPassword, "POSTGRES_PASSWORD", "PG_PASSWORD", "PGPASSWORD")
	pick(&c.PostgresDB, "POSTGRES_DB", "PG_DATABASE", "PGDATABASE")
}

// autoDetectContainers fills empty container names by matching `docker ps`
// container names against known image keywords. Best-effort: failures are
// silently ignored (the agent can still work with explicit container names).
func autoDetectContainers(cfg *Config) {
	if cfg.Containers.ClickHouse != "" && cfg.Containers.MySQL != "" && cfg.Containers.Redis != "" && cfg.Containers.Postgres != "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "ps", "--format", "{{.Names}}\t{{.Image}}")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return
	}
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), "\t", 2)
		if len(parts) != 2 {
			continue
		}
		name := parts[0]
		hay := strings.ToLower(parts[0] + " " + parts[1])
		if cfg.Containers.ClickHouse == "" && strings.Contains(hay, "clickhouse") {
			cfg.Containers.ClickHouse = name
		}
		if cfg.Containers.MySQL == "" && (strings.Contains(hay, "mysql") || strings.Contains(hay, "mariadb")) {
			cfg.Containers.MySQL = name
		}
		if cfg.Containers.Redis == "" && strings.Contains(hay, "redis") {
			cfg.Containers.Redis = name
		}
		// First match wins; projects with multiple postgres containers should set
		// containers.postgres explicitly (explicit config skips auto-detect).
		if cfg.Containers.Postgres == "" && (strings.Contains(hay, "postgres") || strings.Contains(hay, "postgis")) {
			cfg.Containers.Postgres = name
		}
	}
}

// autoDetectCredentials fills empty credential fields by reading env vars from
// the running containers via `docker inspect`. Best-effort: failures are
// silently ignored. Credentials go into Config (used for docker exec args) and
// are persisted to the project config.json — never to context.md, which is
// injected into the LLM prompt.
func autoDetectCredentials(cfg *Config) {
	type target struct {
		container string
		fields    map[string]*string // ENV_KEY -> destination
	}
	c := &cfg.Credentials
	targets := []target{
		{cfg.Containers.ClickHouse, map[string]*string{
			"CLICKHOUSE_USER":     &c.ClickHouseUser,
			"CLICKHOUSE_PASSWORD": &c.ClickHousePassword,
			"CLICKHOUSE_DB":       &c.ClickHouseDB,
		}},
		{cfg.Containers.MySQL, map[string]*string{
			"MYSQL_USER":     &c.MySQLUser,
			"MYSQL_PASSWORD": &c.MySQLPassword,
			"MYSQL_DATABASE": &c.MySQLDB,
		}},
		{cfg.Containers.Postgres, map[string]*string{
			"POSTGRES_USER":     &c.PostgresUser,
			"POSTGRES_PASSWORD": &c.PostgresPassword,
			"POSTGRES_DB":       &c.PostgresDB,
		}},
	}
	for _, t := range targets {
		if strings.TrimSpace(t.container) == "" {
			continue
		}
		// Skip docker inspect entirely if all this container's fields are already set.
		allSet := true
		for _, dest := range t.fields {
			if strings.TrimSpace(*dest) == "" {
				allSet = false
				break
			}
		}
		if allSet {
			continue
		}
		env := dockerInspectEnv(t.container)
		if env == nil {
			continue
		}
		for key, dest := range t.fields {
			if strings.TrimSpace(*dest) == "" {
				if v, ok := env[key]; ok && v != "" {
					*dest = v
				}
			}
		}
	}
}

// dockerInspectEnv returns the container's env as a key->value map via
// `docker inspect`. Returns nil on any failure.
func dockerInspectEnv(container string) map[string]string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "inspect", container,
		"--format", "{{range .Config.Env}}{{println .}}{{end}}")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil
	}
	env := map[string]string{}
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		line := scanner.Text()
		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}
		env[strings.TrimSpace(line[:idx])] = strings.TrimSpace(line[idx+1:])
	}
	return env
}

// persistDiscovered writes the (possibly auto-detected) containers and
// credentials back to the project config.json so later runs skip discovery.
// Best-effort: any error is ignored. Only touches the project config, never
// the global config or context.md.
func persistDiscovered(cfg *Config) {
	if strings.TrimSpace(cfg.Slug) == "" {
		return
	}
	path := ProjectConfigPath(cfg.Slug)
	data, err := os.ReadFile(path)
	if err != nil {
		return // no project config to update
	}
	// Preserve every existing key (notes, env_file, and any DB-specific keys the
	// typed structs don't model) by merging into a raw map rather than
	// re-marshaling a typed struct over the file.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	if b, err := json.Marshal(cfg.Containers); err == nil {
		raw["containers"] = b
	}
	if b, err := json.Marshal(cfg.Credentials); err == nil {
		raw["credentials"] = b
	}
	body, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, append(body, '\n'), 0o644)
}

func (c *Config) applyDefaults() {
	if c.QueryLimit <= 0 {
		c.QueryLimit = 50
	}
	if c.MaxDisplayRows <= 0 {
		c.MaxDisplayRows = 20
	}
	if c.ToolTimeoutSeconds <= 0 {
		c.ToolTimeoutSeconds = 30
	}
	if c.LLMTimeoutSeconds <= 0 {
		c.LLMTimeoutSeconds = 60
	}
	if c.MaxTurns <= 0 {
		c.MaxTurns = 8
	}
	if c.Credentials.RedisDB == "" {
		c.Credentials.RedisDB = "0"
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
