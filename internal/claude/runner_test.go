package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The command runner is tested against this test binary re-executed as a helper
// process. Claude Code does not need to be installed for these tests to run.
const helperEnvVar = "ARC_CLAUDE_HELPER"

// TestHelperProcess is not a real test. With ARC_CLAUDE_HELPER set, the process
// behaves like a small controlled command described by the arguments after "--".
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
		os.Exit(2)
	}

	switch args[0] {
	case "echo-stdin":
		// Prove the input arrived on stdin by wrapping it in an envelope.
		buf := make([]byte, 1<<20)
		n, _ := os.Stdin.Read(buf)
		fmt.Printf(`{"type":"result","result":%q}`, string(buf[:n]))
		os.Exit(0)

	case "echo-args":
		fmt.Printf(`{"type":"result","result":%q}`, strings.Join(args[1:], " "))
		os.Exit(0)

	case "print-cwd":
		dir, _ := os.Getwd()
		fmt.Printf(`{"type":"result","result":%q}`, dir)
		os.Exit(0)

	case "exit":
		code, _ := strconv.Atoi(args[1])
		fmt.Fprint(os.Stderr, "Error: helper failing on purpose\n")
		os.Exit(code)

	case "flood":
		line := strings.Repeat("f", 1023) + "\n"
		for i := 0; i < 2048; i++ {
			fmt.Print(line)
		}
		os.Exit(0)

	case "sleep":
		d, _ := time.ParseDuration(args[1])
		time.Sleep(d)
		os.Exit(0)

	default:
		os.Exit(2)
	}
}

// helperRequest builds a CommandRequest that re-executes this test binary.
func helperRequest(t *testing.T, stdin string, mode ...string) CommandRequest {
	t.Helper()
	t.Setenv(helperEnvVar, "1")

	return CommandRequest{
		Binary: os.Args[0],
		Args:   append([]string{"-test.run=TestHelperProcess", "--"}, mode...),
		Stdin:  stdin,
	}
}

func TestCommandRunnerSuccess(t *testing.T) {
	got, err := NewCommandRunner().Run(context.Background(), helperRequest(t, "", "echo-args", "hello"))
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if got.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0; stderr = %q", got.ExitCode, got.Stderr)
	}
	if !strings.Contains(got.Stdout, "hello") {
		t.Errorf("Stdout = %q, want it to carry the argument", got.Stdout)
	}
	if got.TimedOut {
		t.Error("TimedOut = true for a successful command")
	}
	if got.Duration <= 0 {
		t.Errorf("Duration = %v, want a positive duration", got.Duration)
	}
}

// TestCommandRunnerPassesStdin is the transport guarantee: the review input
// reaches the process on stdin.
func TestCommandRunnerPassesStdin(t *testing.T) {
	const input = "ROLE\nYou are a disciplined code review agent.\nCHANGED FILES\nfile: internal/x.go\n"

	got, err := NewCommandRunner().Run(context.Background(), helperRequest(t, input, "echo-stdin"))
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(got.Stdout, "disciplined code review agent") {
		t.Errorf("Stdout = %q, want it to echo the stdin content", got.Stdout)
	}
	if !strings.Contains(got.Stdout, "internal/x.go") {
		t.Error("the full input did not reach stdin")
	}
}

func TestCommandRunnerWorkingDirectory(t *testing.T) {
	dir := t.TempDir()

	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	request := helperRequest(t, "", "print-cwd")
	request.WorkingDirectory = dir

	got, err := NewCommandRunner().Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(got.Stdout, filepath.Base(want)) {
		t.Errorf("Stdout = %q, want the command to have run in %q", got.Stdout, want)
	}
}

// TestCommandRunnerEmptyWorkingDirectory checks an unset directory is fine.
func TestCommandRunnerEmptyWorkingDirectory(t *testing.T) {
	got, err := NewCommandRunner().Run(context.Background(), helperRequest(t, "", "echo-args", "ok"))
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if got.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", got.ExitCode)
	}
}

func TestCommandRunnerNonZeroExit(t *testing.T) {
	for _, code := range []int{1, 2, 127} {
		code := code
		t.Run(fmt.Sprintf("exit %d", code), func(t *testing.T) {
			got, err := NewCommandRunner().Run(context.Background(),
				helperRequest(t, "", "exit", strconv.Itoa(code)))

			// A non-zero exit is an outcome, not a runner failure.
			if err != nil {
				t.Fatalf("Run() returned error: %v", err)
			}
			if got.ExitCode != code {
				t.Errorf("ExitCode = %d, want %d", got.ExitCode, code)
			}
			if !strings.Contains(got.Stderr, "failing on purpose") {
				t.Errorf("Stderr = %q, want the helper's message", got.Stderr)
			}
		})
	}
}

func TestCommandRunnerMissingExecutable(t *testing.T) {
	tests := []struct {
		name   string
		binary string
	}{
		{name: "not on PATH", binary: "definitely-not-a-real-executable-arc-test"},
		{name: "absolute path that does not exist", binary: filepath.Join(t.TempDir(), "claude")},
		{name: "empty binary", binary: ""},
		{name: "whitespace binary", binary: "   "},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewCommandRunner().Run(context.Background(), CommandRequest{Binary: tt.binary})

			if !errors.Is(err, ErrExecutableNotFound) {
				t.Errorf("errors.Is(err, ErrExecutableNotFound) = false; err = %v", err)
			}
		})
	}
}

func TestCommandRunnerTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	got, err := NewCommandRunner().Run(ctx, helperRequest(t, "", "sleep", "30s"))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if !got.TimedOut {
		t.Errorf("TimedOut = false; exitCode = %d", got.ExitCode)
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %v, want the 100ms deadline to have applied", elapsed)
	}
}

func TestCommandRunnerContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := NewCommandRunner().Run(ctx, helperRequest(t, "", "sleep", "30s"))
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; err = %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %v, want cancellation to have cut the command short", elapsed)
	}
}

func TestCommandRunnerBoundsOutput(t *testing.T) {
	request := helperRequest(t, "", "flood")
	request.MaxOutputBytes = 64 * 1024

	got, err := NewCommandRunner().Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !got.StdoutTruncated {
		t.Error("StdoutTruncated = false for a flooding command")
	}
	if len(got.Stdout) > request.MaxOutputBytes {
		t.Errorf("captured %d bytes, want at most %d", len(got.Stdout), request.MaxOutputBytes)
	}
	// The process must still complete rather than being killed by a short write.
	if got.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0; the flood should not break the command", got.ExitCode)
	}
}

// TestCommandRunnerDefaultOutputBound checks an unset limit falls back.
func TestCommandRunnerDefaultOutputBound(t *testing.T) {
	got, err := NewCommandRunner().Run(context.Background(), helperRequest(t, "", "flood"))
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if len(got.Stdout) > MaxOutputBytes {
		t.Errorf("captured %d bytes, want at most %d", len(got.Stdout), MaxOutputBytes)
	}
	if got.StdoutTruncated {
		t.Error("StdoutTruncated = true although the flood fits the default bound")
	}
}

// TestCommandRunnerDoesNotInterpretShellSyntax proves no shell is involved.
func TestCommandRunnerDoesNotInterpretShellSyntax(t *testing.T) {
	request := helperRequest(t, "", "echo-args", "value; echo pwned")

	got, err := NewCommandRunner().Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if strings.Contains(got.Stdout, "pwned\n") {
		t.Errorf("Stdout = %q; the argument was interpreted by a shell", got.Stdout)
	}
	// The metacharacters arrive as one literal argument.
	if !strings.Contains(got.Stdout, "value; echo pwned") {
		t.Errorf("Stdout = %q, want the argument echoed verbatim", got.Stdout)
	}
}

func TestBoundedBuffer(t *testing.T) {
	tests := []struct {
		name          string
		limit         int
		writes        []string
		wantContent   string
		wantTruncated bool
	}{
		{name: "within the limit", limit: 100, writes: []string{"abc", "def"}, wantContent: "abcdef"},
		{name: "exactly at the limit", limit: 6, writes: []string{"abc", "def"}, wantContent: "abcdef"},
		{name: "one byte over", limit: 5, writes: []string{"abc", "def"}, wantContent: "abcde", wantTruncated: true},
		{name: "first write over the limit", limit: 2, writes: []string{"abcdef"}, wantContent: "ab", wantTruncated: true},
		{name: "writes after the limit are discarded", limit: 3, writes: []string{"abc", "def", "ghi"}, wantContent: "abc", wantTruncated: true},
		{name: "zero limit", limit: 0, writes: []string{"abc"}, wantTruncated: true},
		{name: "no writes", limit: 10},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			b := &boundedBuffer{limit: tt.limit}

			for _, w := range tt.writes {
				n, err := b.Write([]byte(w))
				if err != nil {
					t.Fatalf("Write() returned error: %v", err)
				}
				// A short write would make the child process fail.
				if n != len(w) {
					t.Errorf("Write() = %d, want %d (a short write breaks the pipe)", n, len(w))
				}
			}

			if b.String() != tt.wantContent {
				t.Errorf("String() = %q, want %q", b.String(), tt.wantContent)
			}
			if b.Truncated() != tt.wantTruncated {
				t.Errorf("Truncated() = %t, want %t", b.Truncated(), tt.wantTruncated)
			}
			if len(b.String()) > tt.limit {
				t.Errorf("buffered %d bytes, want at most %d", len(b.String()), tt.limit)
			}
		})
	}
}

// TestCommandRunnerSatisfiesRunner is a compile-time assertion.
func TestCommandRunnerSatisfiesRunner(t *testing.T) {
	var _ Runner = NewCommandRunner()
	var _ Runner = (*CommandRunner)(nil)
}

// TestIntegrationWithLocalClaude runs a real review against the locally installed
// Claude Code CLI. It is skipped unless ARC_CLAUDE_INTEGRATION_TEST=1, so neither
// the normal test run nor CI needs Claude Code installed or authenticated.
func TestIntegrationWithLocalClaude(t *testing.T) {
	if os.Getenv("ARC_CLAUDE_INTEGRATION_TEST") != "1" {
		t.Skip("set ARC_CLAUDE_INTEGRATION_TEST=1 to run against the local Claude Code CLI")
	}

	client := NewClient()
	if err := client.Available(context.Background()); err != nil {
		t.Skipf("Claude Code is not available: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := client.Review(ctx, Request{
		Input: "Reply with exactly the word: ok\n",
	})
	if err != nil {
		t.Fatalf("Review() returned error: %v", err)
	}
	if strings.TrimSpace(result.Output) == "" {
		t.Error("Output is empty")
	}
	t.Logf("output: %q (%.4f USD, %d turns, %v)",
		result.Output, result.CostUSD, result.NumTurns, result.Duration)
}
