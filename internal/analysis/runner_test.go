package analysis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The command runner is tested against this test binary re-executed as a helper
// process, which keeps the tests portable and cheap: no repository-wide test
// suite is ever run, and no external tool is required.
const helperEnvVar = "ARC_ANALYSIS_HELPER"

// TestHelperProcess is not a real test. When ARC_ANALYSIS_HELPER is set, the
// process acts as a small controlled command described by the arguments after
// "--", then exits.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnvVar) != "1" {
		t.Skip("not running as a helper process")
	}

	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "helper: no mode given")
		os.Exit(2)
	}

	switch args[0] {
	case "succeed":
		fmt.Fprint(os.Stdout, "ok\n")
		os.Exit(0)

	case "exit":
		code, err := strconv.Atoi(args[1])
		if err != nil {
			os.Exit(2)
		}
		os.Exit(code)

	case "streams":
		fmt.Fprint(os.Stdout, "this went to stdout\n")
		fmt.Fprint(os.Stderr, "this went to stderr\n")
		os.Exit(0)

	case "fail-with-output":
		fmt.Fprint(os.Stdout, "--- FAIL: TestRetry (0.00s)\n")
		fmt.Fprint(os.Stderr, "FAIL\texample.com/x\t0.01s\n")
		os.Exit(1)

	case "flood":
		// Write well past MaxOutputBytes to both streams.
		stream, count := args[1], MaxOutputBytes/8+512
		line := strings.Repeat("y", 7) + "\n"
		for i := 0; i < count; i++ {
			if stream == "stderr" || stream == "both" {
				fmt.Fprint(os.Stderr, line)
			}
			if stream == "stdout" || stream == "both" {
				fmt.Fprint(os.Stdout, line)
			}
		}
		os.Exit(0)

	case "sleep":
		d, err := time.ParseDuration(args[1])
		if err != nil {
			os.Exit(2)
		}
		time.Sleep(d)
		os.Exit(0)

	case "print-cwd":
		dir, err := os.Getwd()
		if err != nil {
			os.Exit(2)
		}
		fmt.Fprint(os.Stdout, dir)
		os.Exit(0)

	default:
		fmt.Fprintf(os.Stderr, "helper: unknown mode %q\n", args[0])
		os.Exit(2)
	}
}

// helperCheck builds a Check that re-executes this test binary as the helper.
func helperCheck(t *testing.T, timeout time.Duration, mode ...string) Check {
	t.Helper()
	t.Setenv(helperEnvVar, "1")

	args := append([]string{"-test.run=TestHelperProcess", "--"}, mode...)
	return Check{
		Name:    "helper-" + strings.Join(mode, "-"),
		Command: os.Args[0],
		Args:    args,
		Timeout: timeout,
	}
}

func TestCommandRunnerSuccess(t *testing.T) {
	check := helperCheck(t, 30*time.Second, "succeed")

	got := NewCommandRunner().Run(context.Background(), t.TempDir(), check)

	if !got.Passed {
		t.Errorf("Passed = false; stderr = %q, execError = %q", got.Stderr, got.ExecError)
	}
	if got.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", got.ExitCode)
	}
	if got.TimedOut {
		t.Error("TimedOut = true for a successful command")
	}
	if got.Skipped {
		t.Error("Skipped = true for a command that ran")
	}
	if got.ExecError != "" {
		t.Errorf("ExecError = %q, want empty", got.ExecError)
	}
	if !strings.Contains(got.Stdout, "ok") {
		t.Errorf("Stdout = %q, want it to contain %q", got.Stdout, "ok")
	}
	if got.Duration <= 0 {
		t.Errorf("Duration = %v, want a positive duration", got.Duration)
	}
	if got.Name != check.Name {
		t.Errorf("Name = %q, want %q", got.Name, check.Name)
	}
	if got.Command != check.DisplayCommand() {
		t.Errorf("Command = %q, want %q", got.Command, check.DisplayCommand())
	}
}

func TestCommandRunnerExitCodes(t *testing.T) {
	tests := []struct {
		name string
		code int
	}{
		{name: "exit 1 like a failing test suite", code: 1},
		{name: "exit 2 like a build failure", code: 2},
		{name: "exit 3", code: 3},
		{name: "exit 42", code: 42},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			check := helperCheck(t, 30*time.Second, "exit", strconv.Itoa(tt.code))

			got := NewCommandRunner().Run(context.Background(), t.TempDir(), check)

			if got.Passed {
				t.Error("Passed = true for a non-zero exit")
			}
			if got.ExitCode != tt.code {
				t.Errorf("ExitCode = %d, want %d", got.ExitCode, tt.code)
			}
			// A tool reporting problems is evidence, not an execution failure.
			if got.ExecError != "" {
				t.Errorf("ExecError = %q, want empty for a clean non-zero exit", got.ExecError)
			}
			if !got.Failed() {
				t.Error("Failed() = false for a non-zero exit")
			}
		})
	}
}

func TestCommandRunnerCapturesStreamsSeparately(t *testing.T) {
	check := helperCheck(t, 30*time.Second, "streams")

	got := NewCommandRunner().Run(context.Background(), t.TempDir(), check)

	if !strings.Contains(got.Stdout, "this went to stdout") {
		t.Errorf("Stdout = %q, want the stdout line", got.Stdout)
	}
	if strings.Contains(got.Stdout, "this went to stderr") {
		t.Errorf("Stdout = %q, want it free of stderr content", got.Stdout)
	}
	if !strings.Contains(got.Stderr, "this went to stderr") {
		t.Errorf("Stderr = %q, want the stderr line", got.Stderr)
	}
	if strings.Contains(got.Stderr, "this went to stdout") {
		t.Errorf("Stderr = %q, want it free of stdout content", got.Stderr)
	}
}

func TestCommandRunnerCapturesOutputOfAFailingCheck(t *testing.T) {
	check := helperCheck(t, 30*time.Second, "fail-with-output")

	got := NewCommandRunner().Run(context.Background(), t.TempDir(), check)

	if got.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", got.ExitCode)
	}
	if !strings.Contains(got.Stdout, "--- FAIL: TestRetry") {
		t.Errorf("Stdout = %q, want the failure detail", got.Stdout)
	}
	if !strings.Contains(got.Stderr, "FAIL\texample.com/x") {
		t.Errorf("Stderr = %q, want the summary line", got.Stderr)
	}
}

func TestCommandRunnerTruncatesOutput(t *testing.T) {
	tests := []struct {
		name       string
		stream     string
		wantStdout bool
		wantStderr bool
	}{
		{name: "stdout is truncated", stream: "stdout", wantStdout: true},
		{name: "stderr is truncated", stream: "stderr", wantStderr: true},
		{name: "both streams are truncated", stream: "both", wantStdout: true, wantStderr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			check := helperCheck(t, 60*time.Second, "flood", tt.stream)

			got := NewCommandRunner().Run(context.Background(), t.TempDir(), check)

			if got.ExecError != "" {
				t.Fatalf("ExecError = %q", got.ExecError)
			}

			marker := TruncationMarker()

			if got.StdoutTruncated != tt.wantStdout {
				t.Errorf("StdoutTruncated = %t, want %t (stdout is %d bytes)",
					got.StdoutTruncated, tt.wantStdout, len(got.Stdout))
			}
			if got.StderrTruncated != tt.wantStderr {
				t.Errorf("StderrTruncated = %t, want %t (stderr is %d bytes)",
					got.StderrTruncated, tt.wantStderr, len(got.Stderr))
			}

			if tt.wantStdout {
				if !strings.Contains(got.Stdout, marker) {
					t.Errorf("Stdout lacks the truncation marker %q", marker)
				}
				if len(got.Stdout) > MaxOutputBytes+len(marker)+8 {
					t.Errorf("Stdout is %d bytes, want about %d", len(got.Stdout), MaxOutputBytes)
				}
			}
			if tt.wantStderr {
				if !strings.Contains(got.Stderr, marker) {
					t.Errorf("Stderr lacks the truncation marker %q", marker)
				}
				if len(got.Stderr) > MaxOutputBytes+len(marker)+8 {
					t.Errorf("Stderr is %d bytes, want about %d", len(got.Stderr), MaxOutputBytes)
				}
			}
		})
	}
}

func TestCommandRunnerTimeout(t *testing.T) {
	check := helperCheck(t, 100*time.Millisecond, "sleep", "30s")

	start := time.Now()
	got := NewCommandRunner().Run(context.Background(), t.TempDir(), check)
	elapsed := time.Since(start)

	if !got.TimedOut {
		t.Errorf("TimedOut = false; exitCode = %d, execError = %q", got.ExitCode, got.ExecError)
	}
	if got.Passed {
		t.Error("Passed = true for a timed-out command")
	}
	if got.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1", got.ExitCode)
	}
	if got.ExecError != "" {
		t.Errorf("ExecError = %q, want empty: a timeout is a check outcome", got.ExecError)
	}
	// The timeout must actually cut the command short.
	if elapsed > 5*time.Second {
		t.Errorf("took %v, want the 100ms timeout to have applied", elapsed)
	}
}

// TestCommandRunnerTimeoutIsPerCheck proves each check gets its own budget rather
// than sharing one across the analysis.
func TestCommandRunnerTimeoutIsPerCheck(t *testing.T) {
	runner := NewCommandRunner()
	dir := t.TempDir()

	slow := helperCheck(t, 100*time.Millisecond, "sleep", "30s")
	fast := helperCheck(t, 30*time.Second, "succeed")

	if got := runner.Run(context.Background(), dir, slow); !got.TimedOut {
		t.Fatal("the slow check did not time out")
	}
	// The next check must still have its full budget.
	if got := runner.Run(context.Background(), dir, fast); !got.Passed {
		t.Errorf("the following check failed after a timeout: execError = %q", got.ExecError)
	}
}

func TestCommandRunnerUsesTheGivenDirectory(t *testing.T) {
	dir := t.TempDir()

	// macOS reports /private/var... for /var..., so compare resolved paths.
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	got := NewCommandRunner().Run(context.Background(), dir, helperCheck(t, 30*time.Second, "print-cwd"))
	if got.ExecError != "" {
		t.Fatalf("ExecError = %q", got.ExecError)
	}

	reported, err := filepath.EvalSymlinks(strings.TrimSpace(got.Stdout))
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", got.Stdout, err)
	}
	if reported != want {
		t.Errorf("command ran in %q, want %q", reported, want)
	}
}

func TestCommandRunnerMissingExecutable(t *testing.T) {
	check := Check{
		Name:    "missing",
		Command: filepath.Join(t.TempDir(), "definitely-not-a-real-binary"),
		Timeout: 5 * time.Second,
	}

	got := NewCommandRunner().Run(context.Background(), t.TempDir(), check)

	if got.Passed {
		t.Error("Passed = true for a missing executable")
	}
	// This is an execution failure, not a check outcome.
	if got.ExecError == "" {
		t.Error("ExecError is empty for a missing executable")
	}
	if got.TimedOut {
		t.Error("TimedOut = true for a missing executable")
	}
}

func TestCommandRunnerMissingDirectory(t *testing.T) {
	check := helperCheck(t, 5*time.Second, "succeed")
	missing := filepath.Join(t.TempDir(), "no", "such", "dir")

	got := NewCommandRunner().Run(context.Background(), missing, check)

	if got.Passed {
		t.Error("Passed = true with an unusable working directory")
	}
	if got.ExecError == "" {
		t.Error("ExecError is empty with an unusable working directory")
	}
}

func TestCommandRunnerContextCancellation(t *testing.T) {
	check := helperCheck(t, 30*time.Second, "sleep", "30s")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	got := NewCommandRunner().Run(ctx, t.TempDir(), check)
	elapsed := time.Since(start)

	if got.Passed {
		t.Error("Passed = true for a cancelled command")
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %v, want cancellation to have cut the command short", elapsed)
	}
	if got.ExecError == "" {
		t.Errorf("ExecError is empty for a cancelled command; result = %+v", got)
	}
}

func TestCommandRunnerAlreadyCancelledContext(t *testing.T) {
	check := helperCheck(t, 30*time.Second, "succeed")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := NewCommandRunner().Run(ctx, t.TempDir(), check)

	if got.Passed {
		t.Error("Passed = true with an already-cancelled context")
	}
}

// TestCommandRunnerZeroTimeoutUsesTheDefault checks a check with no timeout still
// runs under a bound.
func TestCommandRunnerZeroTimeoutUsesTheDefault(t *testing.T) {
	check := helperCheck(t, 0, "succeed")

	if got := NewCommandRunner().Run(context.Background(), t.TempDir(), check); !got.Passed {
		t.Errorf("Passed = false; execError = %q", got.ExecError)
	}
}

// TestCommandRunnerDoesNotInterpretShellSyntax proves no shell is involved: the
// metacharacters reach the process as a literal argument.
func TestCommandRunnerDoesNotInterpretShellSyntax(t *testing.T) {
	t.Setenv(helperEnvVar, "1")

	// If a shell were involved, "; echo pwned" would run as a second command.
	check := Check{
		Name:    "literal-args",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--", "echo-nothing; echo pwned"},
		Timeout: 10 * time.Second,
	}

	got := NewCommandRunner().Run(context.Background(), t.TempDir(), check)

	if strings.Contains(got.Stdout, "pwned") {
		t.Errorf("Stdout = %q; the argument was interpreted by a shell", got.Stdout)
	}
	// The helper rejects the unknown mode, quoting it back verbatim.
	if !strings.Contains(got.Stderr, "unknown mode") {
		t.Errorf("Stderr = %q, want the helper's unknown-mode message", got.Stderr)
	}
}

func TestTruncateOutput(t *testing.T) {
	marker := TruncationMarker()

	tests := []struct {
		name          string
		size          int
		wantTruncated bool
	}{
		{name: "empty", size: 0},
		{name: "small", size: 128},
		{name: "one byte under the limit", size: MaxOutputBytes - 1},
		{name: "exactly at the limit", size: MaxOutputBytes},
		{name: "one byte over the limit", size: MaxOutputBytes + 1, wantTruncated: true},
		{name: "far over the limit", size: 4 * MaxOutputBytes, wantTruncated: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			in := strings.Repeat("z", tt.size)

			got, truncated := truncateOutput(in)

			if truncated != tt.wantTruncated {
				t.Errorf("truncated = %t, want %t", truncated, tt.wantTruncated)
			}

			if !tt.wantTruncated {
				if got != in {
					t.Error("output was altered although it fit within the limit")
				}
				if strings.Contains(got, marker) {
					t.Error("a marker was added to output that was not truncated")
				}
				return
			}

			if !strings.Contains(got, marker) {
				t.Errorf("output lacks the marker %q", marker)
			}
			if !strings.HasPrefix(got, in[:1024]) {
				t.Error("truncated output does not start with the original content")
			}
			if !strings.HasSuffix(got, in[len(in)-1024:]) {
				t.Error("truncated output does not end with the original content")
			}
		})
	}
}

func TestTruncateOutputPreservesFailureSummaryAndUTF8(t *testing.T) {
	in := "setup\n" + strings.Repeat("é noisy output\n", MaxOutputBytes) + "[error] Tests unsuccessful\n"

	got, truncated := truncateOutput(in)

	if !truncated {
		t.Fatal("truncateOutput() did not report truncation")
	}
	if !strings.Contains(got, "setup") || !strings.Contains(got, "[error] Tests unsuccessful") {
		t.Errorf("truncateOutput() did not retain both ends")
	}
	if !utf8.ValidString(got) {
		t.Error("truncateOutput() produced invalid UTF-8")
	}
}

// TestTruncateOutputIsDeterministic pins that truncation is pure Go, repeatable
// byte for byte.
func TestTruncateOutputIsDeterministic(t *testing.T) {
	in := strings.Repeat("output line\n", MaxOutputBytes/6)

	first, _ := truncateOutput(in)
	for i := 0; i < 5; i++ {
		got, _ := truncateOutput(in)
		if got != first {
			t.Fatalf("run %d differed from the first", i)
		}
	}
}

func TestTruncationMarker(t *testing.T) {
	want := fmt.Sprintf("[TRUNCATED: command output exceeded %d bytes]", MaxOutputBytes)

	if got := TruncationMarker(); got != want {
		t.Errorf("TruncationMarker() = %q, want %q", got, want)
	}
	if MaxOutputBytes != 64*1024 {
		t.Errorf("MaxOutputBytes = %d, want %d", MaxOutputBytes, 64*1024)
	}
}

// TestCommandRunnerSatisfiesRunner is a compile-time assertion.
func TestCommandRunnerSatisfiesRunner(t *testing.T) {
	var _ Runner = NewCommandRunner()
	var _ Runner = (*CommandRunner)(nil)
}
