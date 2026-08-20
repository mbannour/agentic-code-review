package analysis

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runCall records one invocation of the fake runner.
type runCall struct {
	Dir   string
	Check Check
}

// fakeRunner returns canned results per check name and records every call. It
// never executes anything.
type fakeRunner struct {
	// results maps a check name to the result to return.
	results map[string]CheckResult

	// defaultResult is returned for check names absent from results.
	defaultResult CheckResult

	calls []runCall

	// onRun, when set, runs before producing a result. Used to simulate a caller
	// cancelling mid-analysis.
	onRun func(call runCall)
}

func newFakeRunner(results map[string]CheckResult) *fakeRunner {
	return &fakeRunner{
		results:       results,
		defaultResult: CheckResult{Passed: true},
	}
}

func (f *fakeRunner) Run(ctx context.Context, dir string, check Check) CheckResult {
	call := runCall{Dir: dir, Check: check}
	f.calls = append(f.calls, call)

	if f.onRun != nil {
		f.onRun(call)
	}

	result, ok := f.results[check.Name]
	if !ok {
		result = f.defaultResult
	}

	// Fill in the identity fields a real runner would set.
	result.Name = check.Name
	if result.Command == "" {
		result.Command = check.DisplayCommand()
	}
	return result
}

// names returns the check names the runner was asked to run, in order.
func (f *fakeRunner) names() []string {
	names := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		names = append(names, c.Check.Name)
	}
	return names
}

// goRepo creates a temporary directory containing a go.mod.
func goRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.19\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	return dir
}

// pass and fail build canned results.
func pass(exitCode int) CheckResult   { return CheckResult{Passed: true, ExitCode: exitCode} }
func fail(exitCode int) CheckResult   { return CheckResult{Passed: false, ExitCode: exitCode} }
func timedOut() CheckResult           { return CheckResult{Passed: false, ExitCode: -1, TimedOut: true} }
func withStdout(s string) CheckResult { return CheckResult{Passed: true, Stdout: s} }

func TestAnalyze(t *testing.T) {
	tests := []struct {
		name         string
		results      map[string]CheckResult
		wantPassed   bool
		wantFailures []string
	}{
		{
			name: "both checks pass",
			results: map[string]CheckResult{
				"go-test": pass(0),
				"go-vet":  pass(0),
			},
			wantPassed: true,
		},
		{
			name: "go-test fails",
			results: map[string]CheckResult{
				"go-test": fail(1),
				"go-vet":  pass(0),
			},
			wantFailures: []string{"go-test"},
		},
		{
			name: "go-vet fails",
			results: map[string]CheckResult{
				"go-test": pass(0),
				"go-vet":  fail(1),
			},
			wantFailures: []string{"go-vet"},
		},
		{
			name: "both checks fail",
			results: map[string]CheckResult{
				"go-test": fail(1),
				"go-vet":  fail(2),
			},
			wantFailures: []string{"go-test", "go-vet"},
		},
		{
			name: "go-test times out",
			results: map[string]CheckResult{
				"go-test": timedOut(),
				"go-vet":  pass(0),
			},
			wantFailures: []string{"go-test"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			runner := newFakeRunner(tt.results)

			result, err := NewAnalyzer(runner).Analyze(context.Background(), goRepo(t))

			// A failing check is evidence, never an application error.
			if err != nil {
				t.Fatalf("Analyze() returned error: %v", err)
			}

			if len(result.Checks) != 2 {
				t.Fatalf("got %d check results, want 2", len(result.Checks))
			}
			if result.Passed() != tt.wantPassed {
				t.Errorf("Passed() = %t, want %t", result.Passed(), tt.wantPassed)
			}
			if result.HasFailures() == tt.wantPassed {
				t.Errorf("HasFailures() = %t, want %t", result.HasFailures(), !tt.wantPassed)
			}

			var failed []string
			for _, c := range result.FailedChecks() {
				failed = append(failed, c.Name)
			}
			if strings.Join(failed, ",") != strings.Join(tt.wantFailures, ",") {
				t.Errorf("FailedChecks() = %v, want %v", failed, tt.wantFailures)
			}
		})
	}
}

// TestAnalyzeRunsEveryCheckAfterAFailure is the continue-on-failure guarantee.
func TestAnalyzeRunsEveryCheckAfterAFailure(t *testing.T) {
	runner := newFakeRunner(map[string]CheckResult{
		"go-test": fail(1),
		"go-vet":  pass(0),
	})

	result, err := NewAnalyzer(runner).Analyze(context.Background(), goRepo(t))
	if err != nil {
		t.Fatalf("Analyze() returned error: %v", err)
	}

	if want := "go-test,go-vet"; strings.Join(runner.names(), ",") != want {
		t.Errorf("ran %v, want %v", runner.names(), want)
	}
	if len(result.Checks) != 2 {
		t.Errorf("got %d results, want 2; execution stopped at the failure", len(result.Checks))
	}
	if !result.Checks[1].Passed {
		t.Error("the check after the failure did not run to a passing result")
	}
}

func TestAnalyzeExitCodes(t *testing.T) {
	tests := []struct {
		name     string
		result   CheckResult
		wantCode int
		wantPass bool
	}{
		{name: "success is zero", result: pass(0), wantCode: 0, wantPass: true},
		{name: "go test failure is one", result: fail(1), wantCode: 1},
		{name: "go vet failure is one", result: fail(1), wantCode: 1},
		{name: "build failure is two", result: fail(2), wantCode: 2},
		{name: "signal has no exit code", result: timedOut(), wantCode: -1},
		{name: "large exit code is preserved", result: fail(127), wantCode: 127},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			runner := newFakeRunner(map[string]CheckResult{"go-test": tt.result})
			runner.defaultResult = pass(0)

			result, err := NewAnalyzer(runner).Analyze(context.Background(), goRepo(t))
			if err != nil {
				t.Fatalf("Analyze() returned error: %v", err)
			}

			got := result.Checks[0]
			if got.ExitCode != tt.wantCode {
				t.Errorf("ExitCode = %d, want %d", got.ExitCode, tt.wantCode)
			}
			if got.Passed != tt.wantPass {
				t.Errorf("Passed = %t, want %t", got.Passed, tt.wantPass)
			}
		})
	}
}

func TestAnalyzeTimeout(t *testing.T) {
	runner := newFakeRunner(map[string]CheckResult{
		"go-test": {Passed: false, ExitCode: -1, TimedOut: true, Duration: 2 * time.Minute},
		"go-vet":  {Passed: true, Duration: 1400 * time.Millisecond},
	})

	result, err := NewAnalyzer(runner).Analyze(context.Background(), goRepo(t))
	if err != nil {
		t.Fatalf("Analyze() returned error: %v", err)
	}

	if !result.Checks[0].TimedOut {
		t.Error("TimedOut = false for the timed-out check")
	}
	if result.Checks[0].Passed {
		t.Error("Passed = true for a timed-out check")
	}
	if !result.Checks[0].Failed() {
		t.Error("Failed() = false for a timed-out check")
	}
	// A timeout must not abort the run.
	if !result.Checks[1].Passed {
		t.Error("the check after the timeout did not run")
	}
	if result.Checks[0].Duration != 2*time.Minute {
		t.Errorf("Duration = %v, want 2m0s", result.Checks[0].Duration)
	}
}

func TestAnalyzeOutputCapture(t *testing.T) {
	runner := newFakeRunner(map[string]CheckResult{
		"go-test": {Passed: false, ExitCode: 1,
			Stdout: "--- FAIL: TestRetry (0.00s)\n",
			Stderr: "exit status 1\n"},
		"go-vet": withStdout("nothing to report\n"),
	})

	result, err := NewAnalyzer(runner).Analyze(context.Background(), goRepo(t))
	if err != nil {
		t.Fatalf("Analyze() returned error: %v", err)
	}

	if want := "--- FAIL: TestRetry (0.00s)\n"; result.Checks[0].Stdout != want {
		t.Errorf("Stdout = %q, want %q", result.Checks[0].Stdout, want)
	}
	if want := "exit status 1\n"; result.Checks[0].Stderr != want {
		t.Errorf("Stderr = %q, want %q", result.Checks[0].Stderr, want)
	}
	if want := "nothing to report\n"; result.Checks[1].Stdout != want {
		t.Errorf("Stdout = %q, want %q", result.Checks[1].Stdout, want)
	}
	if result.Checks[1].Stderr != "" {
		t.Errorf("Stderr = %q, want empty", result.Checks[1].Stderr)
	}
}

func TestAnalyzeRepositoryValidation(t *testing.T) {
	existing := goRepo(t)

	filePath := filepath.Join(t.TempDir(), "not-a-dir.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tests := []struct {
		name    string
		dir     string
		wantErr string
	}{
		{name: "existing directory", dir: existing},
		{name: "missing directory", dir: filepath.Join(existing, "no", "such", "dir"), wantErr: "does not exist"},
		{name: "path is a file", dir: filePath, wantErr: "is not a directory"},
		{name: "empty path", dir: "", wantErr: "empty repository directory"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			runner := newFakeRunner(nil)

			_, err := NewAnalyzer(runner).Analyze(context.Background(), tt.dir)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Analyze() returned error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("Analyze(%q) = nil error, want %q", tt.dir, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
			// A bad path must not cause any command to be attempted.
			if len(runner.calls) != 0 {
				t.Errorf("runner was called %d times for an invalid path", len(runner.calls))
			}
		})
	}
}

func TestAnalyzeWithoutGoModule(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{
			name:  "empty directory",
			setup: func(t *testing.T) string { return t.TempDir() },
		},
		{
			name: "directory with other files",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
					t.Fatalf("write file: %v", err)
				}
				return dir
			},
		},
		{
			name: "go.mod is a directory",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.Mkdir(filepath.Join(dir, "go.mod"), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				return dir
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			runner := newFakeRunner(nil)

			result, err := NewAnalyzer(runner).Analyze(context.Background(), tt.setup(t))

			// A non-Go repository is not an error.
			if err != nil {
				t.Fatalf("Analyze() returned error: %v", err)
			}
			if len(result.Checks) != 2 {
				t.Fatalf("got %d results, want 2 skipped checks", len(result.Checks))
			}

			for _, c := range result.Checks {
				if !c.Skipped {
					t.Errorf("check %s ran; want it skipped", c.Name)
				}
				if c.SkipReason != SkipReasonNoGoModule {
					t.Errorf("SkipReason = %q, want %q", c.SkipReason, SkipReasonNoGoModule)
				}
				if c.Passed {
					t.Errorf("check %s reports Passed for a skipped check", c.Name)
				}
				if c.Failed() {
					t.Errorf("check %s reports Failed for a skipped check", c.Name)
				}
				if c.Command == "" {
					t.Errorf("check %s has no display command", c.Name)
				}
			}

			// Nothing should have been executed.
			if len(runner.calls) != 0 {
				t.Errorf("runner was called %d times without a go.mod", len(runner.calls))
			}
			// Skipped checks do not count as failures.
			if !result.Passed() {
				t.Error("Passed() = false when every check was skipped")
			}
			if len(result.SkippedChecks()) != 2 {
				t.Errorf("SkippedChecks() returned %d, want 2", len(result.SkippedChecks()))
			}
		})
	}
}

// TestAnalyzePassesRepoDirAndChecksToRunner pins what the runner receives.
func TestAnalyzePassesRepoDirAndChecksToRunner(t *testing.T) {
	dir := goRepo(t)
	runner := newFakeRunner(nil)

	if _, err := NewAnalyzer(runner).Analyze(context.Background(), dir); err != nil {
		t.Fatalf("Analyze() returned error: %v", err)
	}

	if len(runner.calls) != 2 {
		t.Fatalf("made %d calls, want 2", len(runner.calls))
	}

	for _, call := range runner.calls {
		if call.Dir != dir {
			t.Errorf("check %s ran in %q, want %q", call.Check.Name, call.Dir, dir)
		}
	}

	tests := []struct {
		index       int
		wantName    string
		wantCommand string
		wantArgs    string
		wantDisplay string
	}{
		{index: 0, wantName: "go-test", wantCommand: "go", wantArgs: "test,./...", wantDisplay: "go test ./..."},
		{index: 1, wantName: "go-vet", wantCommand: "go", wantArgs: "vet,./...", wantDisplay: "go vet ./..."},
	}

	for _, tt := range tests {
		check := runner.calls[tt.index].Check

		if check.Name != tt.wantName {
			t.Errorf("check %d name = %q, want %q", tt.index, check.Name, tt.wantName)
		}
		if check.Command != tt.wantCommand {
			t.Errorf("check %d command = %q, want %q", tt.index, check.Command, tt.wantCommand)
		}
		if strings.Join(check.Args, ",") != tt.wantArgs {
			t.Errorf("check %d args = %v, want %v", tt.index, check.Args, tt.wantArgs)
		}
		if check.DisplayCommand() != tt.wantDisplay {
			t.Errorf("check %d display = %q, want %q", tt.index, check.DisplayCommand(), tt.wantDisplay)
		}
		if check.EffectiveTimeout() != 2*time.Minute {
			t.Errorf("check %d timeout = %v, want 2m0s", tt.index, check.EffectiveTimeout())
		}
	}
}

// TestAnalyzeNeverInvokesAShell guards the security requirement at the check
// level: no check may name a shell or rely on shell syntax.
func TestAnalyzeNeverInvokesAShell(t *testing.T) {
	shells := map[string]bool{"sh": true, "bash": true, "zsh": true, "cmd": true, "cmd.exe": true, "powershell": true}

	for _, check := range DefaultChecks() {
		if shells[check.Command] {
			t.Errorf("check %s runs a shell (%q)", check.Name, check.Command)
		}
		for _, arg := range check.Args {
			if arg == "-c" {
				t.Errorf("check %s passes -c, suggesting a shell command string", check.Name)
			}
			if strings.ContainsAny(arg, "|;&$`><") {
				t.Errorf("check %s argument %q contains shell metacharacters", check.Name, arg)
			}
		}
	}
}

func TestAnalyzeContextCancellation(t *testing.T) {
	t.Run("cancelled before the first check", func(t *testing.T) {
		runner := newFakeRunner(nil)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := NewAnalyzer(runner).Analyze(ctx, goRepo(t))
		if err == nil {
			t.Fatal("Analyze() = nil error, want cancellation")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("errors.Is(err, context.Canceled) = false; err = %v", err)
		}
		if len(runner.calls) != 0 {
			t.Errorf("runner was called %d times after cancellation", len(runner.calls))
		}
	})

	t.Run("cancelled between checks", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		runner := newFakeRunner(nil)
		runner.onRun = func(call runCall) {
			if call.Check.Name == "go-test" {
				cancel()
			}
		}

		_, err := NewAnalyzer(runner).Analyze(ctx, goRepo(t))
		if !errors.Is(err, context.Canceled) {
			t.Errorf("errors.Is(err, context.Canceled) = false; err = %v", err)
		}
		if len(runner.calls) != 1 {
			t.Errorf("ran %d checks after cancellation, want 1", len(runner.calls))
		}
	})

	t.Run("a deadline exceeded parent context is reported", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		time.Sleep(time.Millisecond)

		_, err := NewAnalyzer(newFakeRunner(nil)).Analyze(ctx, goRepo(t))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("errors.Is(err, context.DeadlineExceeded) = false; err = %v", err)
		}
	})
}

// TestAnalyzeExecErrorIsAnApplicationError separates "the tool reported problems"
// from "the tool could not be started".
func TestAnalyzeExecErrorIsAnApplicationError(t *testing.T) {
	runner := newFakeRunner(map[string]CheckResult{
		"go-test": {ExecError: `exec: "go": executable file not found in $PATH`},
	})

	result, err := NewAnalyzer(runner).Analyze(context.Background(), goRepo(t))
	if err == nil {
		t.Fatalf("Analyze() = %+v, want an application error", result)
	}
	for _, want := range []string{"go-test", "go test ./...", "executable file not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestAnalyzeWithCustomChecks(t *testing.T) {
	checks := []Check{
		{Name: "alpha", Command: "tool", Args: []string{"--alpha"}, Timeout: time.Second},
		{Name: "beta", Command: "tool", Args: []string{"--beta"}, Timeout: time.Second},
		{Name: "gamma", Command: "tool", Timeout: time.Second},
	}

	runner := newFakeRunner(map[string]CheckResult{"beta": fail(3)})
	runner.defaultResult = pass(0)

	result, err := NewAnalyzerWithChecks(runner, checks).Analyze(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Analyze() returned error: %v", err)
	}

	if want := "alpha,beta,gamma"; strings.Join(runner.names(), ",") != want {
		t.Errorf("ran %v, want %v", runner.names(), want)
	}
	if len(result.FailedChecks()) != 1 || result.FailedChecks()[0].Name != "beta" {
		t.Errorf("FailedChecks() = %v, want [beta]", result.FailedChecks())
	}
	// These checks do not require a Go module, so an empty directory is fine.
	if len(result.SkippedChecks()) != 0 {
		t.Errorf("SkippedChecks() = %v, want none", result.SkippedChecks())
	}
}

func TestAnalyzeNoChecks(t *testing.T) {
	result, err := NewAnalyzerWithChecks(newFakeRunner(nil), nil).Analyze(context.Background(), goRepo(t))
	if err != nil {
		t.Fatalf("Analyze() returned error: %v", err)
	}
	if !result.Empty() {
		t.Errorf("Checks = %v, want none", result.Checks)
	}
	if !result.Passed() {
		t.Error("Passed() = false with no checks")
	}
}

func TestAnalyzeWithoutRunner(t *testing.T) {
	analyzer := &Analyzer{checks: DefaultChecks()}

	if _, err := analyzer.Analyze(context.Background(), goRepo(t)); err == nil {
		t.Error("Analyze() = nil error with no runner")
	}
}

// TestAnalyzeRecordsRepoDir checks the result carries the resolved checkout path.
func TestAnalyzeRecordsRepoDir(t *testing.T) {
	dir := goRepo(t)

	result, err := NewAnalyzer(newFakeRunner(nil)).Analyze(context.Background(), dir)
	if err != nil {
		t.Fatalf("Analyze() returned error: %v", err)
	}
	if result.RepoDir != dir {
		t.Errorf("RepoDir = %q, want %q", result.RepoDir, dir)
	}
}

func TestResultHelpers(t *testing.T) {
	tests := []struct {
		name         string
		checks       []CheckResult
		wantPassed   bool
		wantFailures int
		wantSkipped  int
	}{
		{
			name:       "no checks",
			checks:     nil,
			wantPassed: true,
		},
		{
			name: "all passing",
			checks: []CheckResult{
				{Name: "go-test", Passed: true},
				{Name: "go-vet", Passed: true},
			},
			wantPassed: true,
		},
		{
			name: "one failure",
			checks: []CheckResult{
				{Name: "go-test", Passed: false, ExitCode: 1},
				{Name: "go-vet", Passed: true},
			},
			wantFailures: 1,
		},
		{
			name: "all failing",
			checks: []CheckResult{
				{Name: "go-test", Passed: false, ExitCode: 1},
				{Name: "go-vet", Passed: false, ExitCode: 1},
			},
			wantFailures: 2,
		},
		{
			name: "skipped checks are not failures",
			checks: []CheckResult{
				{Name: "go-test", Skipped: true, SkipReason: SkipReasonNoGoModule},
				{Name: "go-vet", Skipped: true, SkipReason: SkipReasonNoGoModule},
			},
			wantPassed:  true,
			wantSkipped: 2,
		},
		{
			name: "a skip alongside a failure",
			checks: []CheckResult{
				{Name: "go-test", Passed: false, ExitCode: 1},
				{Name: "go-vet", Skipped: true, SkipReason: SkipReasonNoGoModule},
			},
			wantFailures: 1,
			wantSkipped:  1,
		},
		{
			name:         "a timeout is a failure",
			checks:       []CheckResult{{Name: "go-test", TimedOut: true}},
			wantFailures: 1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			result := Result{Checks: tt.checks}

			if result.Passed() != tt.wantPassed {
				t.Errorf("Passed() = %t, want %t", result.Passed(), tt.wantPassed)
			}
			if result.HasFailures() == tt.wantPassed {
				t.Errorf("HasFailures() = %t, want %t", result.HasFailures(), !tt.wantPassed)
			}
			if len(result.FailedChecks()) != tt.wantFailures {
				t.Errorf("FailedChecks() returned %d, want %d", len(result.FailedChecks()), tt.wantFailures)
			}
			if len(result.SkippedChecks()) != tt.wantSkipped {
				t.Errorf("SkippedChecks() returned %d, want %d", len(result.SkippedChecks()), tt.wantSkipped)
			}
			if result.Empty() != (len(tt.checks) == 0) {
				t.Errorf("Empty() = %t, want %t", result.Empty(), len(tt.checks) == 0)
			}
		})
	}
}

func TestCheckDisplayCommand(t *testing.T) {
	tests := []struct {
		name  string
		check Check
		want  string
	}{
		{name: "go test", check: Check{Command: "go", Args: []string{"test", "./..."}}, want: "go test ./..."},
		{name: "go vet", check: Check{Command: "go", Args: []string{"vet", "./..."}}, want: "go vet ./..."},
		{name: "no arguments", check: Check{Command: "staticcheck"}, want: "staticcheck"},
		{name: "several arguments", check: Check{Command: "go", Args: []string{"test", "-race", "./..."}}, want: "go test -race ./..."},
		{name: "empty check", check: Check{}, want: ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.check.DisplayCommand(); got != tt.want {
				t.Errorf("DisplayCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckEffectiveTimeout(t *testing.T) {
	tests := []struct {
		name  string
		given time.Duration
		want  time.Duration
	}{
		{name: "explicit timeout", given: 30 * time.Second, want: 30 * time.Second},
		{name: "zero falls back to the default", given: 0, want: DefaultCheckTimeout},
		{name: "negative falls back to the default", given: -time.Second, want: DefaultCheckTimeout},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			check := Check{Command: "go", Timeout: tt.given}
			if got := check.EffectiveTimeout(); got != tt.want {
				t.Errorf("EffectiveTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDefaultChecksAreImmutable checks a caller cannot corrupt the built-in set.
func TestDefaultChecksAreImmutable(t *testing.T) {
	checks := DefaultChecks()
	checks[0].Name = "MUTATED"
	checks[0].Command = "rm"
	checks[0].Args[0] = "-rf"

	fresh := DefaultChecks()
	if fresh[0].Name != "go-test" {
		t.Errorf("DefaultChecks()[0].Name = %q, want go-test", fresh[0].Name)
	}
	if fresh[0].Command != "go" {
		t.Errorf("DefaultChecks()[0].Command = %q, want go", fresh[0].Command)
	}
	if fresh[0].Args[0] != "test" {
		t.Errorf("DefaultChecks()[0].Args[0] = %q, want test", fresh[0].Args[0])
	}
}

func TestDefaultChecks(t *testing.T) {
	checks := DefaultChecks()

	if len(checks) != 2 {
		t.Fatalf("got %d default checks, want 2", len(checks))
	}
	if got := checks[0].DisplayCommand(); got != "go test ./..." {
		t.Errorf("first check = %q, want %q", got, "go test ./...")
	}
	if got := checks[1].DisplayCommand(); got != "go vet ./..." {
		t.Errorf("second check = %q, want %q", got, "go vet ./...")
	}
	for _, c := range checks {
		if len(c.RequiresFiles) == 0 || c.RequiresFiles[0] != "go.mod" {
			t.Errorf("check %s does not require a Go module: %v", c.Name, c.RequiresFiles)
		}
	}
}
