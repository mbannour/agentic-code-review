package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/your-company/agentic-code-review/internal/analysis"
	"github.com/your-company/agentic-code-review/internal/changerisk"
	"github.com/your-company/agentic-code-review/internal/claude"
	"github.com/your-company/agentic-code-review/internal/contextselect"

	"github.com/your-company/agentic-code-review/internal/findings"
	"github.com/your-company/agentic-code-review/internal/github"
	"github.com/your-company/agentic-code-review/internal/jira"
	"github.com/your-company/agentic-code-review/internal/reporules"
	"github.com/your-company/agentic-code-review/internal/retrieval"
	"github.com/your-company/agentic-code-review/internal/review"
	"github.com/your-company/agentic-code-review/internal/specialist"
)

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() = %v", err)
	}

	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	// Drain concurrently: a pipe holds only a few dozen kilobytes, so reading
	// after fn() returns would deadlock on anything larger.
	done := make(chan string, 1)
	go func() {
		out, err := io.ReadAll(r)
		if err != nil {
			done <- "read error: " + err.Error()
			return
		}
		done <- string(out)
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close write end: %v", err)
	}
	return <-done
}

const testPR = "https://github.com/acme/payments/pull/123"

func TestRun(t *testing.T) {
	// The review flow reads $GITHUB_TOKEN; keep it unset so these tests never
	// reach the network, whatever the developer's environment holds.
	t.Setenv("GITHUB_TOKEN", "")

	tests := []struct {
		name    string
		args    []string
		want    []string // substrings expected on stdout
		wantErr string   // substring of the expected error; empty means no error
	}{
		{
			name: "no args prints usage",
			args: nil,
			want: []string{"Agentic Code Review", "arc review --pr"},
		},
		{
			name: "help prints usage",
			args: []string{"help"},
			want: []string{"Agentic Code Review", "--ticket"},
		},
		{
			// Argument validation succeeds, so the flow reaches the client
			// boundary and stops there because no token is configured.
			name:    "review without a token stops at the client boundary",
			args:    []string{"review", "--pr", testPR},
			wantErr: "missing GitHub token",
		},
		{
			name:    "review with ticket and json format still needs a token",
			args:    []string{"review", "--pr", testPR, "--ticket", "PAY-431", "--format", "json"},
			wantErr: "missing GitHub token",
		},
		{
			name:    "review without pr is an error",
			args:    []string{"review"},
			wantErr: "--pr is required",
		},
		{
			name:    "review with invalid format is an error",
			args:    []string{"review", "--pr", testPR, "--format", "yaml"},
			wantErr: `invalid format "yaml"`,
		},
		{
			name:    "review with a non-GitHub pr URL is an error",
			args:    []string{"review", "--pr", "https://gitlab.com/acme/payments/pull/123"},
			wantErr: "invalid --pr:",
		},
		{
			// Ticket validation runs before the GitHub client is built, so this
			// fails on the ticket rather than on the missing token.
			name:    "invalid explicit ticket is rejected",
			args:    []string{"review", "--pr", testPR, "--ticket", "whatever"},
			wantErr: "invalid --ticket:",
		},
		{
			name:    "valid explicit ticket still needs a token",
			args:    []string{"review", "--pr", testPR, "--ticket", "PAY-431"},
			wantErr: "missing GitHub token",
		},
		{
			name:    "unknown flag is an error",
			args:    []string{"review", "--pr", testPR, "--nope"},
			wantErr: "flag provided but not defined",
		},
		{
			name:    "unknown command is an error",
			args:    []string{"bogus"},
			wantErr: `unknown command "bogus"`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var err error
			out := captureStdout(t, func() { err = Run(tt.args) })

			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Run(%q) = %v, want nil", tt.args, err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("Run(%q) = nil, want error containing %q", tt.args, tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("Run(%q) = %v, want error containing %q", tt.args, err, tt.wantErr)
			}

			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("Run(%q) stdout missing %q; got:\n%s", tt.args, want, out)
				}
			}
		})
	}
}

func TestReviewRejectsUnsafeEvidenceConfigBeforeNetwork(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	path := filepath.Join(t.TempDir(), "evidence.json")
	raw := `{"schema_version":1,"sources":[{"id":"customer","type":"file","kind":"requirement","required":true,"path":"/etc/passwd"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Run([]string{"review", "--pr", testPR, "--evidence-config", path})
	if err == nil || !strings.Contains(err.Error(), "path must stay beneath") {
		t.Fatalf("Run() = %v, want evidence containment error before GitHub client", err)
	}
}

// TestPrintIssueNeverLeaksCredentials makes sure the Jira token cannot reach
// normal output through the issue model.
// TestFetchIssueWithoutConfig checks the configuration error names the ticket and
// the missing variables.
func TestFetchIssueWithoutConfig(t *testing.T) {
	t.Setenv("JIRA_BASE_URL", "")
	t.Setenv("JIRA_EMAIL", "")
	t.Setenv("JIRA_TOKEN", "")

	_, err := fetchIssue(context.Background(), "PAY-431")
	if err == nil {
		t.Fatal("fetchIssue() = nil error, want a configuration error")
	}
	if !errors.Is(err, jira.ErrMissingConfig) {
		t.Errorf("errors.Is(err, jira.ErrMissingConfig) = false; err = %v", err)
	}
	for _, want := range []string{"PAY-431", "JIRA_BASE_URL", "JIRA_EMAIL", "JIRA_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// testRules is the rule set used by the output fixtures.
var testRules = reporules.Rules{Documents: []reporules.RuleDocument{
	{Path: ".ai-review/rules.md", Content: "review rules"},
	{Path: "AGENTS.md", Content: "agent rules"},
	{Path: "CONTRIBUTING.md", Content: "contributing rules"},
}}

// contextFixture builds a review context for output tests.
func contextFixture(issue *jira.Issue) review.Context {
	return review.BuildContext(
		github.PullRequest{Owner: "acme", Repo: "payments", Number: 123},
		github.PullRequestDetails{
			Number: 123, Title: "Add payment retry", State: "open", Draft: false,
			HTMLURL:    "https://github.com/acme/payments/pull/123",
			BaseBranch: "main", HeadBranch: "feature/PAY-431", HeadSHA: "abc123",
			AuthorLogin: "alice", Body: "Improve failed payment retry handling.",
		},
		[]github.ChangedFile{
			{Filename: "internal/payment/retry.go", Status: "modified", Additions: 24, Deletions: 7, Changes: 31, Patch: "@@ a @@"},
			{Filename: "internal/payment/retry_test.go", Status: "modified", Additions: 16, Deletions: 4, Changes: 20, Patch: "@@ b @@"},
			{Filename: "internal/payment/client.go", Status: "modified", Additions: 10, Deletions: 7, Changes: 17, Patch: "@@ c @@"},
			{Filename: "README.md", Status: "modified", Additions: 2, Deletions: 0, Changes: 2, Patch: "@@ d @@"},
		},
		issue,
		testRules,
	)
}

var fixtureIssue = jira.Issue{
	Key: "PAY-431", Summary: "Retry failed card authorizations",
	Description: "Retry failed payments", Status: "In Progress",
	IssueType: "Story", Priority: "High", ParentKey: "PAY-400",
	Labels: []string{"payments", "reliability"},
}

func TestPrintContext(t *testing.T) {
	tests := []struct {
		name        string
		issue       *jira.Issue
		ticketFound bool
		ticketErr   error
		format      string
		want        []string
		notWant     []string
	}{
		{
			name:        "context with a Jira issue",
			issue:       &fixtureIssue,
			ticketFound: true,
			format:      "markdown",
			want: []string{
				"Review Context",
				"Pull Request",
				"  Repository: acme/payments",
				"  Number:     123",
				"  URL:        https://github.com/acme/payments/pull/123",
				"  Title:      Add payment retry",
				"  Author:     alice",
				"  State:      open",
				"  Draft:      false",
				"  Base:       main",
				"  Head:       feature/PAY-431",
				"  SHA:        abc123",
				"Changes",
				"  Files:      4",
				"  Additions:  52",
				"  Deletions:  18",
				"  Changes:    70",
				"  internal/payment/retry.go",
				"    modified  +24 -7",
				"Jira",
				"  Key:        PAY-431",
				"  Summary:    Retry failed card authorizations",
				"  Status:     In Progress",
				"  Type:       Story",
				"  Priority:   High",
				"  Parent:     PAY-400",
				"  Labels:     payments, reliability",
				"Repository Rules",
				"Loaded: 3",
				"  .ai-review/rules.md",
				"  AGENTS.md",
				"  CONTRIBUTING.md",
				"Format:       markdown",
			},
			// Patches and the Jira description are later-stage input, not output.
			// Patches, the Jira description, and rule bodies are all
			// later-stage input, not output.
			notWant: []string{"@@ a @@", "@@ d @@", "Retry failed payments",
				"review rules", "agent rules", "contributing rules"},
		},
		{
			name:   "context without a ticket",
			issue:  nil,
			format: "json",
			want: []string{
				"Review Context",
				"  Repository: acme/payments",
				"Jira",
				"  Ticket:     not detected",
				"Format:       json",
			},
			// The branch name legitimately contains the key; the Jira block
			// must not.
			notWant: []string{"Key:", "Summary:", "Priority:"},
		},
		{
			name:  "ambiguous ticket is reported, not fatal",
			issue: nil,
			ticketErr: &jira.AmbiguousTicketError{
				Source: "pull request title",
				Keys:   []jira.TicketKey{"PAY-431", "PAY-432"},
			},
			format: "markdown",
			want: []string{
				"  Ticket:     ambiguous (PAY-431, PAY-432)",
				"pull request title names several tickets",
				"pass --ticket to choose one",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c := contextFixture(tt.issue)

			out := captureStdout(t, func() {
				printContext(c, tt.ticketFound, tt.ticketErr, tt.format)
			})

			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q; got:\n%s", want, out)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(out, notWant) {
					t.Errorf("output unexpectedly contains %q; got:\n%s", notWant, out)
				}
			}
		})
	}
}

func TestPrintContextEdgeCases(t *testing.T) {
	t.Run("no changed files", func(t *testing.T) {
		c := review.BuildContext(
			github.PullRequest{Owner: "acme", Repo: "payments", Number: 9},
			github.PullRequestDetails{Title: "Empty", HeadBranch: "topic"},
			nil, nil, reporules.Rules{},
		)

		out := captureStdout(t, func() { printContext(c, false, nil, "markdown") })

		for _, want := range []string{"  Files:      0", "  Additions:  0", "  Ticket:     not detected"} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q; got:\n%s", want, out)
			}
		}
	})

	t.Run("file without a patch is flagged", func(t *testing.T) {
		c := review.BuildContext(
			github.PullRequest{Owner: "acme", Repo: "payments", Number: 9},
			github.PullRequestDetails{},
			[]github.ChangedFile{{Filename: "logo.png", Status: "added"}},
			nil,
			reporules.Rules{},
		)

		out := captureStdout(t, func() { printContext(c, false, nil, "markdown") })

		if !strings.Contains(out, "(no patch available)") {
			t.Errorf("output does not flag the missing patch; got:\n%s", out)
		}
	})

	t.Run("ticket without a parent or labels omits those lines", func(t *testing.T) {
		c := contextFixture(&jira.Issue{Key: "PAY-431", Summary: "S", Status: "To Do", IssueType: "Bug", Priority: "Low"})

		out := captureStdout(t, func() { printContext(c, true, nil, "markdown") })

		if strings.Contains(out, "Parent:") {
			t.Errorf("output has a Parent line with no parent; got:\n%s", out)
		}
		if strings.Contains(out, "Labels:") {
			t.Errorf("output has a Labels line with no labels; got:\n%s", out)
		}
	})

	t.Run("credentials never reach the output", func(t *testing.T) {
		const token = "jira_secret_token"
		t.Setenv("JIRA_TOKEN", token)
		t.Setenv("JIRA_EMAIL", "developer@company.com")
		t.Setenv("GITHUB_TOKEN", "ghp_secret")

		out := captureStdout(t, func() { printContext(contextFixture(&fixtureIssue), true, nil, "markdown") })

		for _, secret := range []string{token, "developer@company.com", "ghp_secret"} {
			if strings.Contains(out, secret) {
				t.Errorf("output contains the secret %q", secret)
			}
		}
	})
}

func TestPrintRulesSection(t *testing.T) {
	tests := []struct {
		name    string
		rules   reporules.Rules
		want    []string
		notWant []string
	}{
		{
			name:  "three documents",
			rules: testRules,
			want: []string{
				"Repository Rules",
				"Loaded: 3",
				"  .ai-review/rules.md",
				"  AGENTS.md",
				"  CONTRIBUTING.md",
			},
			// Rule bodies stay out of the output.
			notWant: []string{"review rules", "agent rules", "contributing rules"},
		},
		{
			name:    "no documents",
			rules:   reporules.Rules{},
			want:    []string{"Repository Rules", "Loaded: 0"},
			notWant: []string{"AGENTS.md"},
		},
		{
			name: "single document",
			rules: reporules.Rules{Documents: []reporules.RuleDocument{
				{Path: "CONTRIBUTING.md", Content: "c"},
			}},
			want:    []string{"Loaded: 1", "  CONTRIBUTING.md"},
			notWant: []string{"AGENTS.md", ".ai-review"},
		},
		{
			name: "truncated document is flagged",
			rules: reporules.Rules{Documents: []reporules.RuleDocument{
				{Path: "AGENTS.md", Content: "cut", Truncated: true},
			}},
			want: []string{"  AGENTS.md  (truncated)"},
		},
		{
			name: "complete document is not flagged",
			rules: reporules.Rules{Documents: []reporules.RuleDocument{
				{Path: "AGENTS.md", Content: "complete"},
			}},
			want:    []string{"  AGENTS.md"},
			notWant: []string{"(truncated)"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			ctx := review.BuildContext(
				github.PullRequest{Owner: "acme", Repo: "payments", Number: 1},
				github.PullRequestDetails{},
				nil, nil, tt.rules,
			)

			out := captureStdout(t, func() { printRulesSection(ctx.Rules) })

			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q; got:\n%s", want, out)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(out, notWant) {
					t.Errorf("output unexpectedly contains %q; got:\n%s", notWant, out)
				}
			}
		})
	}
}

func TestPrintAnalysis(t *testing.T) {
	tests := []struct {
		name    string
		result  analysis.Result
		want    []string
		notWant []string
	}{
		{
			name: "both checks pass",
			result: analysis.Result{Checks: []analysis.CheckResult{
				{Name: "go-test", Command: "go test ./...", Passed: true, Duration: 4200 * time.Millisecond},
				{Name: "go-vet", Command: "go vet ./...", Passed: true, Duration: 1300 * time.Millisecond},
			}},
			want: []string{
				"Deterministic Analysis",
				"PASS", "go test ./...", "4.2s",
				"go vet ./...", "1.3s",
			},
			notWant: []string{"FAIL", "SKIP", "TIMEOUT"},
		},
		{
			name: "go test fails",
			result: analysis.Result{Checks: []analysis.CheckResult{
				{Name: "go-test", Command: "go test ./...", ExitCode: 1, Duration: 4800 * time.Millisecond,
					Stdout: "--- FAIL: TestRetry (0.00s)\n    retry_test.go:12: got 2, want 3\nFAIL\n"},
				{Name: "go-vet", Command: "go vet ./...", Passed: true, Duration: 1100 * time.Millisecond},
			}},
			want: []string{
				"FAIL", "go test ./...", "4.8s",
				"PASS", "go vet ./...",
				"go-test output:",
				"--- FAIL: TestRetry (0.00s)",
				"retry_test.go:12: got 2, want 3",
			},
		},
		{
			name: "timeout",
			result: analysis.Result{Checks: []analysis.CheckResult{
				{Name: "go-test", Command: "go test ./...", TimedOut: true, ExitCode: -1, Duration: 2 * time.Minute},
				{Name: "go-vet", Command: "go vet ./...", Passed: true, Duration: 1400 * time.Millisecond},
			}},
			want: []string{"TIMEOUT", "go test ./...", "2m0s", "PASS", "go vet ./..."},
		},
		{
			name: "non-Go repository skips both checks",
			result: analysis.Result{Checks: []analysis.CheckResult{
				{Name: "go-test", Command: "go test ./...", Skipped: true, SkipReason: "go.mod not found"},
				{Name: "go-vet", Command: "go vet ./...", Skipped: true, SkipReason: "go.mod not found"},
			}},
			want:    []string{"SKIP", "go test ./...", "go.mod not found", "go vet ./..."},
			notWant: []string{"PASS", "FAIL"},
		},
		{
			name:   "no checks at all",
			result: analysis.Result{},
			want:   []string{"Deterministic Analysis", "no checks ran"},
		},
		{
			name: "failure output is bounded",
			result: analysis.Result{Checks: []analysis.CheckResult{
				{Name: "go-test", Command: "go test ./...", ExitCode: 1,
					Stdout: "setup line\n" + strings.Repeat("failure line\n", 100) + "final failure summary\n"},
			}},
			want: []string{"lines omitted", "setup line", "final failure summary"},
		},
		{
			name: "stderr is used when stdout is empty",
			result: analysis.Result{Checks: []analysis.CheckResult{
				{Name: "go-vet", Command: "go vet ./...", ExitCode: 1,
					Stderr: "vet: internal/x/y.go:3:2: undefined: z\n"},
			}},
			want: []string{"go-vet output:", "undefined: z"},
		},
		{
			name: "a failed check with no output prints no snippet",
			result: analysis.Result{Checks: []analysis.CheckResult{
				{Name: "go-test", Command: "go test ./...", ExitCode: 1},
			}},
			want:    []string{"FAIL", "go test ./..."},
			notWant: []string{"output:"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() { printAnalysis(tt.result) })

			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q; got:\n%s", want, out)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(out, notWant) {
					t.Errorf("output unexpectedly contains %q; got:\n%s", notWant, out)
				}
			}
		})
	}
}

// TestPrintAnalysisDoesNotDumpHugeOutput checks a flooded check cannot flood the
// terminal.
func TestPrintAnalysisDoesNotDumpHugeOutput(t *testing.T) {
	result := analysis.Result{Checks: []analysis.CheckResult{
		{Name: "go-test", Command: "go test ./...", ExitCode: 1,
			Stdout: strings.Repeat("x", 200*1024)},
	}}

	out := captureStdout(t, func() { printAnalysis(result) })

	if len(out) > 8*1024 {
		t.Errorf("printed %d bytes for one failed check, want a bounded snippet", len(out))
	}
}

func TestPrintAnalysisSkipped(t *testing.T) {
	out := captureStdout(t, func() { printAnalysisSkipped("local repository not provided") })

	for _, want := range []string{"Deterministic Analysis", "skipped: local repository not provided"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

func TestCheckStatus(t *testing.T) {
	tests := []struct {
		name  string
		check analysis.CheckResult
		want  string
	}{
		{name: "passed", check: analysis.CheckResult{Passed: true}, want: "PASS"},
		{name: "failed", check: analysis.CheckResult{ExitCode: 1}, want: "FAIL"},
		{name: "timed out", check: analysis.CheckResult{TimedOut: true}, want: "TIMEOUT"},
		{name: "skipped", check: analysis.CheckResult{Skipped: true, SkipReason: "go.mod not found"}, want: "SKIP"},
		{
			name:  "skipped wins over timed out",
			check: analysis.CheckResult{Skipped: true, TimedOut: true},
			want:  "SKIP",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := checkStatus(tt.check); got != tt.want {
				t.Errorf("checkStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckDetail(t *testing.T) {
	skipped := analysis.CheckResult{Skipped: true, SkipReason: "go.mod not found"}
	if got := checkDetail(skipped); got != "go.mod not found" {
		t.Errorf("checkDetail() = %q, want the skip reason", got)
	}

	ran := analysis.CheckResult{Passed: true, Duration: 4230 * time.Millisecond}
	if got := checkDetail(ran); got != "4.2s" {
		t.Errorf("checkDetail() = %q, want %q", got, "4.2s")
	}
}

// selectionFixture builds a selection for output tests.
func selectionFixture(t *testing.T, budget contextselect.Budget) contextselect.SelectedContext {
	t.Helper()

	rc := review.BuildContext(
		github.PullRequest{Owner: "acme", Repo: "payments", Number: 123},
		github.PullRequestDetails{
			Number: 123, Title: "Add payment retry", HeadSHA: "abc123",
			BaseBranch: "main", HeadBranch: "feature/PAY-431", AuthorLogin: "alice",
		},
		[]github.ChangedFile{
			{Filename: "internal/payment/service.go", Status: "modified", Additions: 24, Deletions: 7,
				Patch: "@@ -1,4 +1,8 @@\n+import \"database/sql\"\n+import \"google.golang.org/grpc\"\n"},
			{Filename: "internal/payment/service_test.go", Status: "modified", Additions: 16, Deletions: 4,
				Patch: "@@ -1,4 +1,8 @@\n+func TestRetry(t *testing.T) {}\n"},
			{Filename: "migrations/0042_retry.sql", Status: "added", Additions: 5,
				Patch: "@@ -0,0 +1,5 @@\n+ALTER TABLE payments ADD COLUMN retries int;\n"},
			{Filename: "go.mod", Status: "modified", Additions: 1,
				Patch: "@@ -5,3 +5,4 @@\n+\tgoogle.golang.org/grpc v1.60.0\n"},
			{Filename: "README.md", Status: "modified", Additions: 2,
				Patch: "@@ -1,2 +1,4 @@\n+Retries are now attempted three times.\n"},
			{Filename: "api/payment.pb.go", Status: "modified", Additions: 100,
				Patch: strings.Repeat("+generated line\n", 200)},
		},
		&jira.Issue{Key: "PAY-431", Summary: "Retry failed card authorizations",
			Description: "Retry failed payments", Status: "In Progress",
			IssueType: "Story", Priority: "High"},
		reporules.Rules{Documents: []reporules.RuleDocument{
			{Path: "AGENTS.md", Content: "Prefer table-driven tests."},
		}},
	)

	analysisResult := analysis.Result{Checks: []analysis.CheckResult{
		{Name: "go-test", Command: "go test ./...", ExitCode: 1,
			Stdout: "--- FAIL: TestRetryDeclined (0.00s)\n    expected 0 retries, got 3\n"},
		{Name: "go-vet", Command: "go vet ./...", Passed: true},
	}}

	selected, err := contextselect.NewSelectorWithBudget(budget).Select(context.Background(), rc, analysisResult)
	if err != nil {
		t.Fatalf("Select() returned error: %v", err)
	}
	return selected
}

func TestPrintSelection(t *testing.T) {
	tests := []struct {
		name    string
		budget  contextselect.Budget
		want    []string
		notWant []string
	}{
		{
			name:   "everything fits",
			budget: contextselect.Budget{},
			want: []string{
				"Context Selection",
				"Language:",
				"  go",
				"Technologies:",
				"  grpc",
				"  sql",
				"Candidate files: 6",
				"Selected files:  6",
				"Dropped files:   0",
				"Context size:",
				"Original:",
				"Selected:",
				"Budget:   200 KB",
				"Selected:",
				"HIGH  internal/payment/service.go",
				"changed source file",
				"HIGH  internal/payment/service_test.go",
				"test related to changed source file",
				"HIGH  migrations/0042_retry.sql",
				"database migration",
				"MED   go.mod",
				"dependency change",
				"LOW   README.md",
				"documentation",
				"LOW   api/payment.pb.go",
				"generated file",
			},
			notWant: []string{
				"Context truncated: yes",
				// Content itself never reaches the terminal.
				"ALTER TABLE",
				"Prefer table-driven tests",
				"expected 0 retries, got 3",
				"generated line",
			},
		},
		{
			name:   "budget pressure truncates the lowest-priority patch",
			budget: contextselect.Budget{Total: 1024},
			want: []string{
				"Context truncated: yes",
				"LOW   api/payment.pb.go  (truncated)",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() { printSelection(selectionFixture(t, tt.budget)) })

			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q; got:\n%s", want, out)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(out, notWant) {
					t.Errorf("output unexpectedly contains %q; got:\n%s", notWant, out)
				}
			}
		})
	}
}

// TestPrintSelectionIsBounded checks a huge change cannot flood the terminal.
func TestPrintSelectionIsBounded(t *testing.T) {
	var files []github.ChangedFile
	for i := 0; i < 40; i++ {
		files = append(files, github.ChangedFile{
			Filename: fmt.Sprintf("internal/f%02d.go", i),
			Status:   "modified",
			Patch:    strings.Repeat("+line\n", 4000),
		})
	}

	rc := review.BuildContext(
		github.PullRequest{Owner: "acme", Repo: "payments", Number: 1},
		github.PullRequestDetails{},
		files, nil, reporules.Rules{},
	)

	selected, err := contextselect.NewSelector().Select(context.Background(), rc, analysis.Result{})
	if err != nil {
		t.Fatalf("Select() returned error: %v", err)
	}

	out := captureStdout(t, func() { printSelection(selected) })

	if len(out) > 16*1024 {
		t.Errorf("printed %d bytes for a 40-file change, want a bounded summary", len(out))
	}
	// No patch content at all.
	if strings.Contains(out, "+line") {
		t.Error("patch content reached the terminal")
	}
}

func TestPrintSelectionNoFiles(t *testing.T) {
	rc := review.BuildContext(
		github.PullRequest{Owner: "acme", Repo: "payments", Number: 1},
		github.PullRequestDetails{}, nil, nil, reporules.Rules{},
	)

	selected, err := contextselect.NewSelector().Select(context.Background(), rc, analysis.Result{})
	if err != nil {
		t.Fatalf("Select() returned error: %v", err)
	}

	out := captureStdout(t, func() { printSelection(selected) })

	for _, want := range []string{"Context Selection", "Language:", "not detected", "Candidate files: 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes int
		want  string
	}{
		{bytes: 0, want: "0 B"},
		{bytes: 512, want: "512 B"},
		{bytes: 1023, want: "1023 B"},
		{bytes: 1024, want: "1 KB"},
		{bytes: 200 * 1024, want: "200 KB"},
		{bytes: 486 * 1024, want: "486 KB"},
		{bytes: 2 * 1024 * 1024, want: "2.0 MB"},
	}

	for _, tt := range tests {
		if got := formatBytes(tt.bytes); got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

// TestPrintSelectionDroppedFiles covers the drop path, where a file cannot even be
// truncated into the remaining budget.
func TestPrintSelectionDroppedFiles(t *testing.T) {
	var files []github.ChangedFile
	for i := 0; i < 6; i++ {
		files = append(files, github.ChangedFile{
			Filename: fmt.Sprintf("docs/guide%02d.md", i),
			Status:   "modified",
			Patch:    strings.Repeat("+documentation line\n", 200),
		})
	}
	files = append(files, github.ChangedFile{
		Filename: "internal/payment/service.go", Status: "modified",
		Patch: strings.Repeat("+source line\n", 200),
	})

	rc := review.BuildContext(
		github.PullRequest{Owner: "acme", Repo: "payments", Number: 1},
		github.PullRequestDetails{}, files, nil, reporules.Rules{},
	)

	selected, err := contextselect.NewSelectorWithBudget(contextselect.Budget{Total: 4 * 1024}).
		Select(context.Background(), rc, analysis.Result{})
	if err != nil {
		t.Fatalf("Select() returned error: %v", err)
	}

	if selected.Stats.DroppedFiles == 0 {
		t.Fatalf("no files were dropped; stats = %+v", selected.Stats)
	}

	out := captureStdout(t, func() { printSelection(selected) })

	for _, want := range []string{"Dropped:", "context budget exhausted", "Context truncated: yes"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
	// The source file must be the one that survived.
	if !strings.Contains(out, "HIGH  internal/payment/service.go") {
		t.Errorf("the high-priority file was not kept; got:\n%s", out)
	}
}

// TestPrintClaudeOutcome checks the terminal report: the validated findings are
// rendered, and the transport envelope never is.
func TestPrintClaudeOutcome(t *testing.T) {
	finding := findings.Finding{
		ID: "COR-001", Category: findings.CategoryCorrectness, Severity: findings.SeverityHigh,
		Confidence: 0.96,
		File:       "internal/payment/retry.go", StartLine: 84, EndLine: 87,
		Title:      "Permanent declines enter the retry path",
		Problem:    "The new branch treats a permanent decline as retryable.",
		Impact:     "Declined authorizations can be attempted repeatedly.",
		Suggestion: "Return before entering RetryPayment for permanent declines.",
		Evidence: []findings.Evidence{{
			Type: findings.EvidenceCode, Source: "internal/payment/retry.go:84-87",
			Detail: "The decline branch reaches RetryPayment.",
		}},
	}

	tests := []struct {
		name    string
		outcome claude.Outcome
		want    []string
		notWant []string
	}{
		{
			name: "completed review with a finding",
			outcome: claude.Outcome{
				Result: findings.ReviewResult{
					Summary:  "Found one actionable correctness issue.",
					Findings: []findings.Finding{finding},
				},
				Transport: claude.Result{
					Output:    `{"summary":"...","findings":[]}`,
					RawOutput: `{"type":"result","result":"...","session_id":"abc"}`,
					Duration:  18400 * time.Millisecond,
				},
			},
			want: []string{
				"Claude Review",
				"Status: completed",
				"Duration: 18.4s",
				"Findings: 1",
				"Agentic Review",
				"1 actionable finding",
				"HIGH · COR-001",
				"internal/payment/retry.go:84-87",
				"Permanent declines enter the retry path",
				"Evidence strength: HIGH",
			},
			// The transport envelope is never printed.
			notWant: []string{`{"type":"result"`, "session_id"},
		},
		{
			name: "truncated output is flagged",
			outcome: claude.Outcome{
				Result:    findings.ReviewResult{Summary: "No actionable issues found."},
				Transport: claude.Result{Truncated: true, Duration: time.Second},
			},
			want: []string{"Output truncated: yes", "No actionable findings found."},
		},
		{
			name: "zero findings is not an error",
			outcome: claude.Outcome{
				Result:    findings.ReviewResult{Summary: "No actionable issues found."},
				Transport: claude.Result{Duration: 4 * time.Second},
			},
			want:    []string{"Status: completed", "Findings: 0", "No actionable findings found."},
			notWant: []string{"error", "failed"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() { printClaudeOutcome(tt.outcome) })

			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q; got:\n%s", want, out)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(out, notWant) {
					t.Errorf("output unexpectedly contains %q; got:\n%s", notWant, out)
				}
			}
		})
	}
}

func TestPrintClaudeSkipped(t *testing.T) {
	out := captureStdout(t, func() { printClaudeSkipped("--claude not provided") })

	for _, want := range []string{"Claude Review", "Skipped: --claude not provided"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

// TestClaudeIsOptIn checks the flag defaults to off, so a review never consumes
// Claude usage unasked.
func TestClaudeIsOptIn(t *testing.T) {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.String("pr", "", "")
	useClaude := fs.Bool("claude", false, "")

	if err := fs.Parse([]string{"--pr", testPR}); err != nil {
		t.Fatalf("Parse() returned error: %v", err)
	}
	if *useClaude {
		t.Error("--claude defaults to true; Claude execution must be opt-in")
	}

	if err := fs.Parse([]string{"--claude"}); err != nil {
		t.Fatalf("Parse() returned error: %v", err)
	}
	if !*useClaude {
		t.Error("--claude did not enable Claude execution")
	}
}

// TestRunReviewSkipsClaudeWithoutTheFlag proves the review completes without ever
// reaching the Claude adapter. The run stops at the GitHub client, well before
// Claude would be invoked, and a missing Claude binary must not matter.
func TestRunReviewSkipsClaudeWithoutTheFlag(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("ARC_CLAUDE_BINARY", "definitely-not-a-real-executable-arc-test")

	err := Run([]string{"review", "--pr", testPR})

	// Fails on the missing GitHub token, never on Claude.
	if err == nil {
		t.Fatal("Run() = nil error, want the missing-token error")
	}
	if strings.Contains(err.Error(), "Claude") {
		t.Errorf("error = %q, want it unrelated to Claude when --claude is absent", err)
	}
}

// TestRunReviewClaudeUnavailableIsActionable checks the missing-binary error is
// useful and mentions no credentials.
func TestRunReviewClaudeUnavailableIsActionable(t *testing.T) {
	t.Setenv("ARC_CLAUDE_BINARY", "definitely-not-a-real-executable-arc-test")

	selected := selectionFixture(t, contextselect.Budget{})

	err := runClaudeReview(context.Background(), claudeStage{selected: selected})
	if err == nil {
		t.Fatal("runClaudeReview() = nil error, want a missing-executable error")
	}
	if !errors.Is(err, claude.ErrExecutableNotFound) {
		t.Errorf("errors.Is(err, claude.ErrExecutableNotFound) = false; err = %v", err)
	}
	for _, want := range []string{"Claude Code", "ARC_CLAUDE_BINARY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	for _, unwanted := range []string{"API key", "API_KEY", "ANTHROPIC"} {
		if strings.Contains(err.Error(), unwanted) {
			t.Errorf("error %q mentions %q; authentication is not this tool's concern", err, unwanted)
		}
	}
}

// TestCaptureRequiresClaude checks the flag combination is refused before any
// network call. Capture records what the reviewer proposed, so without a reviewer
// run there is nothing to record — and a flag that quietly produced no snapshot
// would be indistinguishable from a case that found nothing.
func TestCaptureRequiresClaude(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	path := filepath.Join(t.TempDir(), "run.json")

	err := Run([]string{"review", "--pr", testPR, "--capture-predictions", path, "--capture-run", "run-1"})
	if err == nil || !strings.Contains(err.Error(), "--capture-predictions requires --claude") {
		t.Fatalf("Run() = %v, want the capture/--claude refusal", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("a snapshot was created despite the refusal")
	}
}

// A snapshot with no run name cannot be scored, so the omission is refused up
// front rather than after a full review has been paid for.
func TestCaptureRequiresARunName(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	path := filepath.Join(t.TempDir(), "run.json")

	err := Run([]string{"review", "--pr", testPR, "--claude", "--capture-predictions", path})
	if err == nil || !strings.Contains(err.Error(), "--capture-run is required") {
		t.Fatalf("Run() = %v, want the missing run-name refusal", err)
	}
}

// The capture metadata flags do nothing on their own; silently ignoring them
// would let a mislabelled suite look like it was captured correctly.
func TestCaptureMetadataFlagsRequireCapture(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")

	for _, flagName := range []string{"--capture-case", "--capture-run"} {
		err := Run([]string{"review", "--pr", testPR, "--claude", flagName, "value"})
		if err == nil || !strings.Contains(err.Error(), "require --capture-predictions") {
			t.Errorf("Run(%s) = %v, want the missing --capture-predictions refusal", flagName, err)
		}
	}
}

// The default case ID identifies the pull request under review, so a suite's case
// IDs are stable without a lookup table.
func TestCaptureCaseIDDefaultsToThePullRequest(t *testing.T) {
	pr := github.PullRequest{Owner: "acme", Repo: "payments", Number: 123}

	if got := captureCaseID("", pr); got != "acme/payments#123" {
		t.Errorf("captureCaseID(\"\") = %q, want acme/payments#123", got)
	}
	if got := captureCaseID("  custom-case  ", pr); got != "custom-case" {
		t.Errorf("captureCaseID(explicit) = %q, want custom-case", got)
	}
}

// Capture is opt-in: nothing is written unless a path is given.
func TestCaptureIsOptIn(t *testing.T) {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.String("pr", "", "")
	path := fs.String("capture-predictions", "", "")

	if err := fs.Parse([]string{"--pr", testPR}); err != nil {
		t.Fatalf("Parse() returned error: %v", err)
	}
	if *path != "" {
		t.Error("--capture-predictions has a default path; capture must be opt-in")
	}
}

// TestRetrieveRequiresARepositoryDirectory checks the refusal happens before any
// network call. Retrieval reads the local checkout, and a silent skip is worse
// than an error when the run was meant to measure retrieval's effect.
func TestRetrieveRequiresARepositoryDirectory(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")

	err := Run([]string{"review", "--pr", testPR, "--retrieve"})
	if err == nil || !strings.Contains(err.Error(), "--retrieve requires --repo-dir") {
		t.Fatalf("Run() = %v, want the retrieve/--repo-dir refusal", err)
	}
}

// Retrieval is opt-in, so a baseline run and a retrieval run differ only in the
// flag — which is what makes the two comparable.
func TestRetrievalIsOptIn(t *testing.T) {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.String("pr", "", "")
	retrieve := fs.Bool("retrieve", false, "")

	if err := fs.Parse([]string{"--pr", testPR}); err != nil {
		t.Fatalf("Parse() returned error: %v", err)
	}
	if *retrieve {
		t.Error("--retrieve defaults to true; retrieval must be opt-in")
	}
}

func TestPrintRetrievalStatesTheSkipReason(t *testing.T) {
	output := captureStdout(t, func() {
		printRetrieval(retrieval.Result{Skipped: true, Reason: "no indexable source files found in the checkout"})
	})

	if !strings.Contains(output, "skipped: no indexable source files") {
		t.Errorf("output does not explain the skip:\n%s", output)
	}
}

func TestPrintRetrievalListsRegions(t *testing.T) {
	output := captureStdout(t, func() {
		printRetrieval(retrieval.Result{
			Symbols: []string{"RetryPayment"},
			Snippets: []retrieval.Snippet{{
				Symbol: "RetryPayment", Relation: retrieval.RelationDefinition,
				Path: "internal/payment/retry.go", StartLine: 84, EndLine: 97,
				Content: "func RetryPayment() error {\n\treturn nil\n}",
			}},
			Stats: retrieval.Stats{
				FilesIndexed: 120, Definitions: 900,
				TouchedSymbols: 10, ResolvedSymbols: 1, Bytes: 42,
			},
		})
	})

	for _, want := range []string{"DEF", "RetryPayment", "internal/payment/retry.go:84-97", "1 of 10 resolved"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

// A change with no perspective to review it has nothing for a model to look for,
// and paying for that call was the cheapest waste in the pipeline. The skip must
// be stated, not silent.
func TestPrintClaudeSkippedStatesNoApplicablePerspective(t *testing.T) {
	out := captureStdout(t, func() {
		printClaudeSkipped("no review perspective applies to this change")
	})

	if !strings.Contains(out, "no review perspective applies") {
		t.Errorf("output does not explain the skip:\n%s", out)
	}
}

// The saving must never apply to a change containing production code: the router
// selects the correctness perspective for any such change, so the skip cannot fire.
func TestEveryProductionChangeSelectsAPerspective(t *testing.T) {
	cases := map[string][]changerisk.Change{
		"go source": {{Path: "internal/app/app.go", Patch: "@@ -1 +1,2 @@\n+\tcall()\n", Additions: 1}},
		"scala source": {{Path: "src/main/scala/app/App.scala",
			Patch: "@@ -1 +1,2 @@\n+  def run = 1\n", Additions: 1}},
		"tests only": {{Path: "internal/app/app_test.go",
			Patch: "@@ -1 +1,2 @@\n+func TestThing(t *testing.T) {}\n", Additions: 1}},
		"migration only": {{Path: "db/migrations/0007.sql",
			Patch: "@@ -1 +1,2 @@\n+ALTER TABLE payments ADD COLUMN status text;\n", Additions: 1}},
	}

	for name, changes := range cases {
		t.Run(name, func(t *testing.T) {
			profile := changerisk.NewAnalyzer().Analyze(changes)
			plan := specialist.NewRouter().Route(profile)

			if len(plan.Selected) == 0 {
				t.Errorf("no perspective selected for %s; the review would be skipped", name)
			}
		})
	}
}

// Only changes with nothing reviewable in them skip the model.
func TestNonCodeChangesSelectNoPerspective(t *testing.T) {
	profile := changerisk.NewAnalyzer().Analyze([]changerisk.Change{
		{Path: "README.md", Patch: "@@ -1 +1,2 @@\n+prose\n", Additions: 1},
		{Path: "LICENSE", Patch: "@@ -1 +1,2 @@\n+text\n", Additions: 1},
	})

	if plan := specialist.NewRouter().Route(profile); len(plan.Selected) != 0 {
		t.Errorf("selected %v for a documentation-only change", plan.IDs())
	}
}

// The context ceiling follows the risk band, and a high-risk change keeps the full
// allowance: the optimization must not reduce context where it matters most.
func TestContextBudgetFollowsRisk(t *testing.T) {
	high := changerisk.NewAnalyzer().Analyze([]changerisk.Change{
		{Path: "internal/payments/capture.go", Patch: "@@ -1 +1,2 @@\n+\tcapture(amount)\n", Additions: 400},
		{Path: "internal/auth/token.go", Patch: "@@ -1 +1,2 @@\n+\tverify(token)\n", Additions: 400},
	})
	low := changerisk.NewAnalyzer().Analyze([]changerisk.Change{
		{Path: "internal/format/format.go", Patch: "@@ -1 +1,2 @@\n+\treturn s\n", Additions: 2},
	})

	highBudget := contextselect.BudgetForRiskLevel(string(high.Level))
	lowBudget := contextselect.BudgetForRiskLevel(string(low.Level))

	if highBudget.Total != contextselect.DefaultContextBudgetBytes {
		t.Errorf("high-risk budget = %d, want the full %d",
			highBudget.Total, contextselect.DefaultContextBudgetBytes)
	}
	if lowBudget.Total >= highBudget.Total {
		t.Errorf("low-risk budget %d is not below the high-risk %d", lowBudget.Total, highBudget.Total)
	}
}
