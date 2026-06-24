package schema

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TableSchema describes one table's columns.
type TableSchema struct {
	Columns   []string `json:"columns"`
	UpdatedAt string   `json:"updated_at"`
}

// DBSchema describes all discovered tables in one database.
type DBSchema struct {
	DBType string                  `json:"db_type"` // clickhouse|mysql|postgres
	Tables map[string]TableSchema  `json:"tables"`
}

// Store manages per-database JSON schema files.
type Store struct {
	dir string // e.g. /var/pile/agent-db/projects/<slug>/schema
}

// NewStore creates or opens a schema store directory.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("schema dir is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create schema dir %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// DBSchemaPath returns the JSON file path for a database.
func (s *Store) DBSchemaPath(dbName string) string {
	return filepath.Join(s.dir, safeFilename(dbName)+".json")
}

// Load reads one database's schema from disk. Returns nil if file doesn't exist.
func (s *Store) Load(dbName string) (*DBSchema, error) {
	data, err := os.ReadFile(s.DBSchemaPath(dbName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read schema %s: %w", dbName, err)
	}
	var schema DBSchema
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("parse schema %s: %w", dbName, err)
	}
	if schema.Tables == nil {
		schema.Tables = map[string]TableSchema{}
	}
	return &schema, nil
}

// Save writes one database's schema to disk.
func (s *Store) Save(dbName string, schema *DBSchema) error {
	if schema == nil {
		return fmt.Errorf("schema is nil")
	}
	if schema.Tables == nil {
		schema.Tables = map[string]TableSchema{}
	}
	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal schema %s: %w", dbName, err)
	}
	if err := os.WriteFile(s.DBSchemaPath(dbName), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write schema %s: %w", dbName, err)
	}
	return nil
}

// ListDBs returns all discovered database names (sorted).
func (s *Store) ListDBs() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read schema dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		db := strings.TrimSuffix(name, ".json")
		names = append(names, db)
	}
	sort.Strings(names)
	return names, nil
}

// IsSystemTable checks if a table name is a system/internal table that
// shouldn't be stored in the schema index.
func IsSystemTable(table string) bool {
	// Postgres OLAP tables (v1_*, v2_*, etc — internal Hatchet tables)
	if strings.HasPrefix(table, "v") && len(table) > 2 && table[1] >= '0' && table[1] <= '9' {
		// v1_cel_evaluation_failures_olap etc — always internal
		return true
	}
	// Postgres OLAP partitions: table_20260527
	parts := strings.Split(table, "_")
	for _, p := range parts {
		if len(p) == 8 {
			if _, err := time.Parse("20060102", p); err == nil {
				return true
			}
		}
	}
	// Migration tables
	if strings.Contains(table, "schema_migration") || strings.Contains(table, "migration") {
		return true
	}
	// Named system tables
	if strings.Contains(table, "_sys_") || strings.Contains(table, "cron_jobs") {
		return true
	}
	// Hatchet internal OLAP tables
	if strings.Contains(table, "_olap") {
		return true
	}
	return false
}

// IsGarbageName checks if a table name is a parse artifact (e.g. "1", "2").
func IsGarbageName(name string) bool {
	if name == "" {
		return true
	}
	// Pure number = parse artifact from row count or numbered line
	allDigits := true
	for _, r := range name {
		if r < '0' || r > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		return true
	}
	// Starts with a digit = likely garbage
	if name[0] >= '0' && name[0] <= '9' {
		return true
	}
	return false
}

// containsDigit reports whether s contains any digit.
func containsDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

// InjectRelevant builds a compact schema block for databases mentioned in
// the user query. If no specific databases are mentioned, returns a compact
// summary of ALL discovered databases.
func (s *Store) InjectRelevant(query string) (string, error) {
	allDBs, err := s.ListDBs()
	if err != nil {
		return "", err
	}
	if len(allDBs) == 0 {
		return "", nil // no discovered schema yet
	}

	queryLower := strings.ToLower(query)
	matched := map[string]bool{}

	// Helper to check if a word from query matches a DB name part
	// Match "source" -> "source_mirror", "gv3" -> "gv3", not "gv3" -> "gv3_master"
	words := strings.FieldsFunc(queryLower, func(r rune) bool {
		return r == ' ' || r == '.' || r == ',' || r == ';' || r == '(' || r == ')' || r == '"'
	})

	for _, dbName := range allDBs {
		lo := strings.ToLower(dbName)

		// Exact match: "source_mirror"
		for _, w := range words {
			if w == lo || strings.HasPrefix(w, lo) {
				matched[lo] = true
				break
			}
		}
		// Also check if DB appears as a substring of query — but only if the
		// DB name is not a prefix of another DB name (avoid "gv3" matching "gv3_master").
		if !matched[lo] && strings.Contains(queryLower, lo) {
			// Verify it's not a substring of a longer DB name
			isSubstr := false
			for _, other := range allDBs {
				otherLo := strings.ToLower(other)
				if otherLo != lo && strings.HasPrefix(otherLo, lo) {
					isSubstr = true
					break
				}
			}
			if !isSubstr {
				matched[lo] = true
			}
		}
	}

	// Handle db type keywords: "clickhouse", "mysql", "postgres"
	// When user types "clickhouse", match ALL clickhouse databases
	dbTypeKeywords := map[string]string{
		"clickhouse": "clickhouse", "ch": "clickhouse",
		"mysql": "mysql", "maria": "mysql", "mariadb": "mysql",
		"postgres": "postgres", "pg": "postgres", "postgresql": "postgres",
		"hatchet": "postgres", // hatchet is postgres
	}
	for _, w := range words {
		if targetType, ok := dbTypeKeywords[w]; ok {
			// Find all databases with this DB type
			for _, dbName := range allDBs {
				s, err := s.Load(dbName)
				if err != nil || s == nil {
					continue
				}
				if strings.ToLower(s.DBType) == targetType {
					matched[strings.ToLower(dbName)] = true
				}
			}
		}
	}

	var schemaBlock strings.Builder

	// If no specific DB matched, include all databases (compact summary)
	if len(matched) == 0 {
		schemaBlock.WriteString("\n\nPROJECT SCHEMA (discovered databases — use show_tables for details):\n")
		for _, name := range allDBs {
			s, err := s.Load(name)
			if err != nil || s == nil {
				schemaBlock.WriteString(fmt.Sprintf("- %s\n", name))
				continue
			}
			tableNames := make([]string, 0, len(s.Tables))
			for t := range s.Tables {
				tableNames = append(tableNames, t)
			}
			sort.Strings(tableNames)
			dbType := s.DBType
			line := fmt.Sprintf("- %s (%s, %d tables)", name, dbType, len(tableNames))
			schemaBlock.WriteString(line + "\n")
			// Show first 10 table names as examples
			if len(tableNames) > 0 {
				show := tableNames
				if len(show) > 10 {
					show = show[:10]
				}
				schemaBlock.WriteString(fmt.Sprintf("  tables: %s\n", strings.Join(show, ", ")))
			}
		}
		return schemaBlock.String(), nil
	}

	// Specific databases matched — inject full schema only for matched ones
	// For databases with many tables, cap detailed output at 30 tables.
	const maxDetailedTables = 30
	schemaBlock.WriteString("\n\nPROJECT SCHEMA (relevant tables):\n")
	for _, dbName := range allDBs {
		name := strings.ToLower(dbName)
		if !matched[name] {
			continue
		}
		s, err := s.Load(dbName)
		if err != nil || s == nil {
			schemaBlock.WriteString(fmt.Sprintf("- %s\n", dbName))
			continue
		}
		tableNames := make([]string, 0, len(s.Tables))
		for t := range s.Tables {
			tableNames = append(tableNames, t)
		}
		sort.Strings(tableNames)

		total := len(tableNames)
		for i, table := range tableNames {
			if i >= maxDetailedTables {
				remaining := total - i
				schemaBlock.WriteString(fmt.Sprintf("- %s ... (%d more tables — use describe_table to explore)\n",
					dbName, remaining))
				break
			}
			ts := s.Tables[table]
			if len(ts.Columns) > 0 {
				schemaBlock.WriteString(fmt.Sprintf("- %s.%s(%s)\n",
					dbName, table, strings.Join(ts.Columns, ", ")))
			} else {
				schemaBlock.WriteString(fmt.Sprintf("- %s.%s\n", dbName, table))
			}
		}
	}
	return schemaBlock.String(), nil
}

// UpdateTable stores or updates a table's columns in the schema index.
func (s *Store) UpdateTable(dbName, dbType, table string, columns []string) error {
	// Strip schema prefix (e.g. "public.v1_table" -> "v1_table")
	if dotIdx := strings.Index(table, "."); dotIdx >= 0 {
		table = table[dotIdx+1:]
	}
	// Validate: skip garbage and system tables
	if IsGarbageName(table) {
		return nil
	}
	if IsSystemTable(table) {
		return nil
	}
	// Validate column names — skip if all columns are garbage
	validColumns := make([]string, 0, len(columns))
	for _, c := range columns {
		if !IsGarbageName(c) {
			validColumns = append(validColumns, c)
		}
	}
	if len(validColumns) == 0 && len(columns) > 0 {
		return nil // all columns were garbage, skip entirely
	}
	if len(validColumns) == 0 {
		validColumns = columns
	}

	schema, err := s.Load(dbName)
	if err != nil || schema == nil {
		schema = &DBSchema{
			DBType: dbType,
			Tables: map[string]TableSchema{},
		}
	}
	// Ensure DBType is set
	if schema.DBType == "" {
		schema.DBType = dbType
	}

	now := time.Now().UTC().Format(time.RFC3339)
	existing, exists := schema.Tables[table]
	if exists && len(validColumns) <= len(existing.Columns) {
		// Not growing — update timestamp only (skip if same columns)
		same := len(validColumns) == len(existing.Columns)
		if same {
			for i, c := range validColumns {
				if i >= len(existing.Columns) || existing.Columns[i] != c {
					same = false
					break
				}
			}
		}
		if same {
			// Columns unchanged: no-op to avoid unnecessary writes
			return nil
		}
	}

	schema.Tables[table] = TableSchema{
		Columns:   validColumns,
		UpdatedAt: now,
	}

	return s.Save(dbName, schema)
}

// DiscoverDB creates a database entry with just table names (no columns yet),
// called after SHOW TABLES.
func (s *Store) DiscoverDB(dbName, dbType string, tableNames []string) error {
	schema, err := s.Load(dbName)
	if err != nil || schema == nil {
		schema = &DBSchema{
			DBType: dbType,
			Tables: map[string]TableSchema{},
		}
	}
	if schema.DBType == "" {
		schema.DBType = dbType
	}
	now := time.Now().UTC().Format(time.RFC3339)

	for _, table := range tableNames {
		if IsGarbageName(table) || IsSystemTable(table) {
			continue
		}
		if _, exists := schema.Tables[table]; !exists {
			schema.Tables[table] = TableSchema{
				Columns:   []string{},
				UpdatedAt: now,
			}
		}
	}

	return s.Save(dbName, schema)
}

// safeFilename replaces characters unsafe for filenames.
func safeFilename(name string) string {
	s := strings.ReplaceAll(name, ".", "_")
	s = strings.ReplaceAll(s, "/", "_")
	return s
}

// MigrateFromMarkdown migrates existing context.md content into JSON schema
// files. Returns the number of entries migrated.
func (s *Store) MigrateFromMarkdown(markdown string) (int, error) {
	if strings.TrimSpace(markdown) == "" {
		return 0, nil
	}

	type entry struct {
		db     string
		table  string
		cols   []string
		dbType string
	}

	var entries []entry
	lines := strings.Split(markdown, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- `") {
			continue
		}
		// Parse: - `db.table` — <columns: col1, col2> (discovered ...)
		// Or:    - `db.table` (discovered ...)
		rest := line
		// Extract `db.table`
		idx := strings.Index(rest, "`")
		if idx < 0 {
			continue
		}
		rest = rest[idx+1:]
		idx = strings.Index(rest, "`")
		if idx < 0 {
			continue
		}
		name := rest[:idx]
		rest = rest[idx+1:]

		parts := strings.SplitN(name, ".", 2)
		if len(parts) < 2 {
			continue
		}
		db := parts[0]
		table := parts[1]

		// Strip schema prefix from table name (e.g. "public.v1_table" -> "v1_table")
		bareTable := table
		if dotIdx := strings.Index(table, "."); dotIdx >= 0 {
			bareTable = table[dotIdx+1:]
		}

		// Skip garbage
		if IsGarbageName(bareTable) || IsSystemTable(bareTable) {
			continue
		}

		// Check for columns: <columns: ...>
		var cols []string
		if colIdx := strings.Index(rest, "<columns:"); colIdx >= 0 {
			colPart := rest[colIdx+9:]
			if endIdx := strings.Index(colPart, ">"); endIdx >= 0 {
				colPart = colPart[:endIdx]
			}
			// Parse: 1.(project_id), 2.(mode), ...
			colParts := strings.Split(colPart, ",")
			for _, c := range colParts {
				c = strings.TrimSpace(c)
				if parenIdx := strings.Index(c, "("); parenIdx >= 0 {
					c = c[parenIdx+1:]
					if endParen := strings.Index(c, ")"); endParen >= 0 {
						c = c[:endParen]
					}
				}
				c = strings.TrimSpace(c)
				if c != "" {
					cols = append(cols, c)
				}
			}
		}

		entries = append(entries, entry{db: db, table: table, cols: cols})
	}

	for _, e := range entries {
		if err := s.UpdateTable(e.db, "clickhouse", e.table, e.cols); err != nil {
			return 0, fmt.Errorf("migrate %s.%s: %w", e.db, e.table, err)
		}
	}

	return len(entries), nil
}
