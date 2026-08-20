package evidence

import (
	"context"
	"strings"
	"testing"
)

type recordingRunner struct {
	binary string
	args   []string
	env    []string
	out    string
	errOut string
	err    error
}

func (r *recordingRunner) Run(_ context.Context, binary string, args []string, env []string) (string, string, error) {
	r.binary = binary
	r.args = append([]string(nil), args...)
	r.env = append([]string(nil), env...)
	return r.out, r.errOut, r.err
}

func TestPostgresConnectorRunsFixedReadOnlyMetadataQuery(t *testing.T) {
	t.Setenv(postgresServiceEnv, "arc-stage-readonly")
	t.Setenv(psqlBinaryEnv, "/opt/bin/psql")
	runner := &recordingRunner{out: "TABLE\tpublic\torders\tBASE TABLE\nCOLUMN\tpublic\torders\tid\tuuid\tNO\t"}
	connector := NewPostgresSchemaConnector(SourceConfig{
		ID: "stage", Type: SourcePostgresSchema, Kind: KindDatabaseSchema,
		Schema: "public", Required: true,
	}, runner)

	document, err := connector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}
	if runner.binary != "/opt/bin/psql" || !containsArg(runner.args, "--no-psqlrc") || !containsArg(runner.args, "--no-password") {
		t.Fatalf("command = %q %q", runner.binary, runner.args)
	}
	joinedArgs := strings.Join(runner.args, " ")
	for _, want := range []string{"BEGIN TRANSACTION READ ONLY", "information_schema.columns", "pg_constraint", "pg_indexes"} {
		if !strings.Contains(joinedArgs, want) {
			t.Errorf("query missing %q", want)
		}
	}
	joinedEnv := strings.Join(runner.env, "\n")
	for _, want := range []string{"PGSERVICE=arc-stage-readonly", "default_transaction_read_only=on", "PGAPPNAME=arc-evidence-readonly"} {
		if !strings.Contains(joinedEnv, want) {
			t.Errorf("environment missing %q", want)
		}
	}
	if !strings.Contains(document.Content, "COLUMN\tpublic\torders\tid") {
		t.Fatalf("document = %+v", document)
	}
}

func TestPostgresConnectorRejectsSchemaInjectionBeforeExecution(t *testing.T) {
	t.Setenv(postgresServiceEnv, "arc-stage-readonly")
	runner := &recordingRunner{}
	connector := NewPostgresSchemaConnector(SourceConfig{
		ID: "stage", Type: SourcePostgresSchema, Kind: KindDatabaseSchema,
		Schema: "public; select * from users",
	}, runner)
	if _, err := connector.Collect(context.Background()); err == nil {
		t.Fatal("Collect() accepted arbitrary SQL")
	}
	if runner.binary != "" {
		t.Fatal("runner was invoked for unsafe schema")
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
