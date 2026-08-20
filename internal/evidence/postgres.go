package evidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	postgresServiceEnv = "ARC_POSTGRES_SERVICE"
	psqlBinaryEnv      = "ARC_PSQL_BINARY"
)

// CommandRunner is the execution boundary used by the PostgreSQL metadata
// connector. The command, arguments, and query are code-owned; configuration
// supplies only a validated schema identifier.
type CommandRunner interface {
	Run(ctx context.Context, binary string, args []string, env []string) (stdout, stderr string, err error)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, binary string, args []string, env []string) (string, string, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = env
	stdout := &limitedBuffer{limit: MaxRawSourceBytes + 1}
	stderr := &limitedBuffer{limit: 32 * 1024}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if stdout.overflow {
		return "", stderr.String(), fmt.Errorf("schema output exceeds the %d byte source limit", MaxRawSourceBytes)
	}
	return stdout.String(), stderr.String(), err
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.overflow = true
		return original, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.overflow = true
	}
	_, _ = b.Buffer.Write(p)
	return original, nil
}

// PostgresSchemaConnector inspects PostgreSQL catalogs only. It cannot execute
// configured SQL, read table rows, or mutate the database. ARC requires a
// libpq service name supplied by the operator and additionally forces a
// read-only transaction and session.
type PostgresSchemaConnector struct {
	config SourceConfig
	runner CommandRunner
}

func NewPostgresSchemaConnector(config SourceConfig, runner CommandRunner) *PostgresSchemaConnector {
	if runner == nil {
		runner = execCommandRunner{}
	}
	return &PostgresSchemaConnector{config: config, runner: runner}
}

func (c *PostgresSchemaConnector) ID() string       { return c.config.ID }
func (c *PostgresSchemaConnector) Type() SourceType { return SourcePostgresSchema }
func (c *PostgresSchemaConnector) Kind() Kind       { return KindDatabaseSchema }
func (c *PostgresSchemaConnector) Required() bool   { return c.config.Required }

func (c *PostgresSchemaConnector) Collect(ctx context.Context) (Document, error) {
	if !sqlNamePattern.MatchString(c.config.Schema) {
		return Document{}, fmt.Errorf("unsafe PostgreSQL schema identifier")
	}
	service := strings.TrimSpace(os.Getenv(postgresServiceEnv))
	if service == "" {
		return Document{}, fmt.Errorf("PostgreSQL schema connector is not configured: missing %s", postgresServiceEnv)
	}
	binary := strings.TrimSpace(os.Getenv(psqlBinaryEnv))
	if binary == "" {
		binary = "psql"
	}

	query := postgresMetadataQuery(c.config.Schema)
	args := []string{
		"--no-psqlrc", "--no-password", "--set=ON_ERROR_STOP=1",
		"--quiet", "--tuples-only", "--no-align", "--field-separator=\t",
		"--command", query,
	}
	env := replaceEnvironment(os.Environ(), map[string]string{
		"PGSERVICE": service,
		"PGAPPNAME": "arc-evidence-readonly",
		"PGOPTIONS": "-c default_transaction_read_only=on -c statement_timeout=15000 -c lock_timeout=3000",
	})

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stdout, stderr, err := c.runner.Run(runCtx, binary, args, env)
	if err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return Document{}, fmt.Errorf("inspect PostgreSQL schema %s: timed out", c.config.Schema)
		}
		detail := strings.Join(strings.Fields(stderr), " ")
		if len(detail) > 500 {
			detail = detail[:500] + "…"
		}
		if detail != "" {
			return Document{}, fmt.Errorf("inspect PostgreSQL schema %s: %s", c.config.Schema, detail)
		}
		return Document{}, fmt.Errorf("inspect PostgreSQL schema %s: command failed", c.config.Schema)
	}

	original := strings.TrimSpace(stdout)
	content, truncated := normalizeDocumentContent(original)
	return Document{
		ID: c.ID(), Kind: KindDatabaseSchema, SourceType: c.Type(),
		Locator: "postgres:schema/" + c.config.Schema,
		Title:   "PostgreSQL schema " + c.config.Schema,
		Content: content, Digest: digest(original), Truncated: truncated,
	}, nil
}

// postgresMetadataQuery is fixed except for a validated identifier used only as
// a quoted string literal. It returns schema structure, never application rows.
func postgresMetadataQuery(schema string) string {
	return `BEGIN TRANSACTION READ ONLY;
SELECT 'TABLE' || E'\t' || table_schema || E'\t' || table_name || E'\t' || table_type
FROM information_schema.tables
WHERE table_schema = '` + schema + `'
ORDER BY table_name;

SELECT 'COLUMN' || E'\t' || table_schema || E'\t' || table_name || E'\t' || column_name || E'\t' ||
       data_type || E'\t' || is_nullable || E'\t' || COALESCE(column_default, '')
FROM information_schema.columns
WHERE table_schema = '` + schema + `'
ORDER BY table_name, ordinal_position;

SELECT 'CONSTRAINT' || E'\t' || n.nspname || E'\t' || cls.relname || E'\t' || con.conname || E'\t' ||
       pg_get_constraintdef(con.oid, true)
FROM pg_constraint con
JOIN pg_class cls ON cls.oid = con.conrelid
JOIN pg_namespace n ON n.oid = cls.relnamespace
WHERE n.nspname = '` + schema + `'
ORDER BY cls.relname, con.conname;

SELECT 'INDEX' || E'\t' || schemaname || E'\t' || tablename || E'\t' || indexname || E'\t' || indexdef
FROM pg_indexes
WHERE schemaname = '` + schema + `'
ORDER BY tablename, indexname;
COMMIT;`
}

func replaceEnvironment(current []string, replacements map[string]string) []string {
	result := make([]string, 0, len(current)+len(replacements))
	for _, item := range current {
		key := item
		if index := strings.IndexByte(item, '='); index >= 0 {
			key = item[:index]
		}
		if _, replaced := replacements[key]; replaced {
			continue
		}
		result = append(result, item)
	}
	for _, key := range []string{"PGSERVICE", "PGAPPNAME", "PGOPTIONS"} {
		result = append(result, key+"="+replacements[key])
	}
	return result
}
