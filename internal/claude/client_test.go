package claude

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeRunner records the request and returns a canned result. It never executes
// anything, so the unit tests do not require Claude Code to be installed.
type fakeRunner struct {
	result CommandResult
	err    error

	requests []CommandRequest

	// onRun, when set, runs before returning.
	onRun func(request CommandRequest)
}

func (f *fakeRunner) Run(ctx context.Context, request CommandRequest) (CommandResult, error) {
	f.requests = append(f.requests, request)
	if f.onRun != nil {
		f.onRun(request)
	}
	return f.result, f.err
}

// last returns the most recent request.
func (f *fakeRunner) last(t *testing.T) CommandRequest {
	t.Helper()
	if len(f.requests) == 0 {
		t.Fatal("the runner was never called")
	}
	return f.requests[len(f.requests)-1]
}

// envelopeJSON builds a transport envelope carrying text.
func envelopeJSON(text string) string {
	return fmt.Sprintf(`{"type":"result","subtype":"success","is_error":false,
		"duration_ms":18400,"num_turns":1,"result":%q,
		"session_id":"11111111-2222-3333-4444-555555555555","total_cost_usd":0.0421}`, text)
}

func okRunner(text string) *fakeRunner {
	return &fakeRunner{result: CommandResult{
		Stdout: envelopeJSON(text), ExitCode: 0, Duration: 18400 * time.Millisecond,
	}}
}

const testInput = "ROLE\nYou are a disciplined code review agent.\n"

func TestNewClientDefaults(t *testing.T) {
	t.Setenv(BinaryEnvVar, "")

	client := NewClient()

	if client.Binary() != DefaultBinary {
		t.Errorf("Binary() = %q, want %q", client.Binary(), DefaultBinary)
	}
	if client.Binary() != "claude" {
		t.Errorf("default binary = %q, want %q", client.Binary(), "claude")
	}
	if client.Timeout() != DefaultTimeout {
		t.Errorf("Timeout() = %v, want %v", client.Timeout(), DefaultTimeout)
	}
	if client.maxOutputBytes != MaxOutputBytes {
		t.Errorf("maxOutputBytes = %d, want %d", client.maxOutputBytes, MaxOutputBytes)
	}
	if client.runner == nil {
		t.Error("runner is nil")
	}
}

func TestNewClientBinaryConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		envVar string
		option string
		want   string
	}{
		{name: "default", want: DefaultBinary},
		{name: "environment override", envVar: "/usr/local/bin/claude", want: "/usr/local/bin/claude"},
		{name: "option override", option: "/opt/claude/bin/claude", want: "/opt/claude/bin/claude"},
		{
			name:   "option wins over the environment",
			envVar: "/usr/local/bin/claude",
			option: "/opt/claude/bin/claude",
			want:   "/opt/claude/bin/claude",
		},
		{name: "blank environment value falls back", envVar: "   ", want: DefaultBinary},
		{name: "blank option is ignored", option: "  ", want: DefaultBinary},
		{name: "environment value is trimmed", envVar: "  /usr/bin/claude  ", want: "/usr/bin/claude"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(BinaryEnvVar, tt.envVar)

			var opts []Option
			if tt.option != "" {
				opts = append(opts, WithBinary(tt.option))
			}

			if got := NewClient(opts...).Binary(); got != tt.want {
				t.Errorf("Binary() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestClientReadsNoCredentials is the authentication boundary: the client must
// not look at any API key variable.
func TestClientReadsNoCredentials(t *testing.T) {
	secrets := map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-secret",
		"CLAUDE_API_KEY":    "claude-secret",
		"OPENAI_API_KEY":    "openai-secret",
		"GITHUB_TOKEN":      "ghp_secret",
		"JIRA_TOKEN":        "jira-secret",
	}
	for name, value := range secrets {
		t.Setenv(name, value)
	}
	t.Setenv(BinaryEnvVar, "/usr/local/bin/claude")

	runner := okRunner("no issues found")
	client := NewClient(WithRunner(runner))

	result, err := client.Review(context.Background(), Request{Input: testInput})
	if err != nil {
		t.Fatalf("Review() returned error: %v", err)
	}

	// Nothing about the invocation or the result may carry a credential.
	rendered := fmt.Sprintf("%+v %+v", runner.last(t), result)
	for name, value := range secrets {
		if strings.Contains(rendered, value) {
			t.Errorf("the invocation carries the value of %s", name)
		}
	}
}

func TestReviewInvocation(t *testing.T) {
	runner := okRunner("review text")
	client := NewClient(WithRunner(runner), WithBinary("/usr/local/bin/claude"))

	if _, err := client.Review(context.Background(), Request{
		Input:            testInput,
		WorkingDirectory: "/home/user/work/payments",
	}); err != nil {
		t.Fatalf("Review() returned error: %v", err)
	}

	got := runner.last(t)

	if got.Binary != "/usr/local/bin/claude" {
		t.Errorf("Binary = %q, want the configured path", got.Binary)
	}
	if strings.Join(got.Args, " ") != "-p --output-format json" {
		t.Errorf("Args = %v, want [-p --output-format json]", got.Args)
	}
	if got.Stdin != testInput {
		t.Errorf("Stdin = %q, want the review input", got.Stdin)
	}
	if got.WorkingDirectory != "/home/user/work/payments" {
		t.Errorf("WorkingDirectory = %q, want the repository directory", got.WorkingDirectory)
	}
	if got.MaxOutputBytes != MaxOutputBytes {
		t.Errorf("MaxOutputBytes = %d, want %d", got.MaxOutputBytes, MaxOutputBytes)
	}
}

// TestReviewArgumentsAreExactlyTheNonInteractiveFlags pins the invocation shape.
func TestReviewArgumentsAreExactlyTheNonInteractiveFlags(t *testing.T) {
	args := reviewArgs()

	want := []string{"-p", "--output-format", "json"}
	if strings.Join(args, ",") != strings.Join(want, ",") {
		t.Fatalf("reviewArgs() = %v, want %v", args, want)
	}

	// Print mode and a JSON envelope, and nothing invented beyond them.
	if args[0] != "-p" {
		t.Errorf("first argument = %q, want -p for non-interactive print mode", args[0])
	}
	if args[1] != "--output-format" || args[2] != "json" {
		t.Errorf("args = %v, want a JSON output format", args)
	}
}

// TestReviewSendsInputOnStdinNotArguments guards against putting a large context
// into the argument list.
func TestReviewSendsInputOnStdinNotArguments(t *testing.T) {
	input := strings.Repeat("CHANGED FILES\nfile: internal/x.go\n", 5000)

	runner := okRunner("ok")
	if _, err := NewClient(WithRunner(runner)).Review(context.Background(), Request{Input: input}); err != nil {
		t.Fatalf("Review() returned error: %v", err)
	}

	got := runner.last(t)
	if got.Stdin != input {
		t.Error("the input did not travel on stdin")
	}
	for _, arg := range got.Args {
		if len(arg) > 64 {
			t.Errorf("argument %q looks like embedded context", arg[:64])
		}
		if strings.Contains(arg, "CHANGED FILES") {
			t.Error("review context leaked into the argument list")
		}
	}
}

// TestReviewNeverInvokesAShell is the security guarantee at the client level.
func TestReviewNeverInvokesAShell(t *testing.T) {
	shells := map[string]bool{"sh": true, "bash": true, "zsh": true, "cmd": true, "cmd.exe": true, "powershell": true}

	runner := okRunner("ok")
	client := NewClient(WithRunner(runner), WithBinary("claude"))

	if _, err := client.Review(context.Background(), Request{Input: testInput}); err != nil {
		t.Fatalf("Review() returned error: %v", err)
	}

	got := runner.last(t)
	if shells[got.Binary] {
		t.Errorf("Binary = %q, which is a shell", got.Binary)
	}
	for _, arg := range got.Args {
		if arg == "-c" {
			t.Error("the arguments include -c, suggesting a shell command string")
		}
		if strings.ContainsAny(arg, "|;&$`><") {
			t.Errorf("argument %q contains shell metacharacters", arg)
		}
	}
}

func TestReviewSuccess(t *testing.T) {
	const text = "Potential correctness issue in internal/payment/retry.go: the retry counter is never reset."

	runner := okRunner(text)
	got, err := NewClient(WithRunner(runner)).Review(context.Background(), Request{Input: testInput})
	if err != nil {
		t.Fatalf("Review() returned error: %v", err)
	}

	if got.Output != text {
		t.Errorf("Output = %q, want %q", got.Output, text)
	}
	if got.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", got.ExitCode)
	}
	if got.TimedOut {
		t.Error("TimedOut = true for a successful invocation")
	}
	if got.Truncated {
		t.Error("Truncated = true for a small output")
	}
	if got.Duration != 18400*time.Millisecond {
		t.Errorf("Duration = %v, want 18.4s", got.Duration)
	}
	if got.SessionID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("SessionID = %q, want it carried through", got.SessionID)
	}
	if got.CostUSD != 0.0421 {
		t.Errorf("CostUSD = %v, want 0.0421", got.CostUSD)
	}
	if got.NumTurns != 1 {
		t.Errorf("NumTurns = %d, want 1", got.NumTurns)
	}
	if got.RawOutput == "" {
		t.Error("RawOutput is empty; it is kept for debugging")
	}
}

func TestReviewParsesTransportEnvelope(t *testing.T) {
	tests := []struct {
		name    string
		stdout  string
		want    string
		wantErr error
	}{
		{
			name:   "standard result envelope",
			stdout: envelopeJSON("review text"),
			want:   "review text",
		},
		{
			name:   "text field instead of result",
			stdout: `{"type":"result","text":"review text"}`,
			want:   "review text",
		},
		{
			name:   "unknown fields are ignored",
			stdout: `{"type":"result","result":"review text","future_field":{"nested":[1,2]},"permission_denials":[]}`,
			want:   "review text",
		},
		{
			name:   "surrounding whitespace",
			stdout: "\n\n  " + envelopeJSON("review text") + "  \n",
			want:   "review text",
		},
		{
			name:   "array envelope takes the last text",
			stdout: `[{"type":"system"},{"type":"assistant","text":"partial"},{"type":"result","result":"final review"}]`,
			want:   "final review",
		},
		{
			name:   "multi-line review text",
			stdout: envelopeJSON("line one\nline two\nline three"),
			want:   "line one\nline two\nline three",
		},
		{
			name:    "invalid json",
			stdout:  `{"type":"result","result":`,
			wantErr: ErrInvalidOutput,
		},
		{
			name:    "not json at all",
			stdout:  "Claude Code is not authenticated",
			wantErr: ErrInvalidOutput,
		},
		{
			name:    "empty stdout",
			stdout:  "",
			wantErr: ErrEmptyOutput,
		},
		{
			name:    "whitespace stdout",
			stdout:  "   \n  ",
			wantErr: ErrEmptyOutput,
		},
		{
			name:    "empty json object",
			stdout:  `{}`,
			wantErr: ErrEmptyOutput,
		},
		{
			name:    "empty result string",
			stdout:  envelopeJSON(""),
			wantErr: ErrEmptyOutput,
		},
		{
			name:    "empty array",
			stdout:  `[]`,
			wantErr: ErrEmptyOutput,
		},
		{
			name:    "json array of the wrong shape",
			stdout:  `[1,2,3]`,
			wantErr: ErrInvalidOutput,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{result: CommandResult{Stdout: tt.stdout, ExitCode: 0}}

			got, err := NewClient(WithRunner(runner)).Review(context.Background(), Request{Input: testInput})

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("errors.Is(err, %v) = false; err = %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("Review() returned error: %v", err)
			}
			if got.Output != tt.want {
				t.Errorf("Output = %q, want %q", got.Output, tt.want)
			}
		})
	}
}

func TestReviewIsErrorEnvelope(t *testing.T) {
	runner := &fakeRunner{result: CommandResult{
		Stdout:   `{"type":"result","is_error":true,"result":"Execution error: rate limited"}`,
		ExitCode: 0,
	}}

	got, err := NewClient(WithRunner(runner)).Review(context.Background(), Request{Input: testInput})
	if err == nil {
		t.Fatalf("Review() = %+v, want an error", got)
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error = %q, want it to carry the reported detail", err)
	}
}

func TestReviewNonZeroExit(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		stderr   string
		wantMsg  string
	}{
		{name: "exit 1", exitCode: 1, stderr: "Error: not logged in\n", wantMsg: "status 1"},
		{name: "stderr detail is included", exitCode: 1, stderr: "Error: not logged in\n", wantMsg: "not logged in"},
		{name: "exit 2 with no stderr", exitCode: 2, wantMsg: "status 2"},
		{name: "exit 127", exitCode: 127, wantMsg: "status 127"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{result: CommandResult{ExitCode: tt.exitCode, Stderr: tt.stderr}}

			got, err := NewClient(WithRunner(runner)).Review(context.Background(), Request{Input: testInput})
			if err == nil {
				t.Fatalf("Review() = %+v, want an error", got)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantMsg)
			}
			// The result still describes what happened.
			if got.ExitCode != tt.exitCode {
				t.Errorf("ExitCode = %d, want %d", got.ExitCode, tt.exitCode)
			}
		})
	}
}

// TestReviewNonZeroExitStderrIsBounded keeps a flood of stderr out of the error.
func TestReviewNonZeroExitStderrIsBounded(t *testing.T) {
	runner := &fakeRunner{result: CommandResult{
		ExitCode: 1,
		Stderr:   strings.Repeat("noise ", 100000),
	}}

	_, err := NewClient(WithRunner(runner)).Review(context.Background(), Request{Input: testInput})
	if err == nil {
		t.Fatal("want an error")
	}
	if len(err.Error()) > 400 {
		t.Errorf("error message is %d bytes, want it bounded", len(err.Error()))
	}
}

func TestReviewMissingExecutable(t *testing.T) {
	runner := &fakeRunner{err: fmt.Errorf("%w: claude", ErrExecutableNotFound)}

	_, err := NewClient(WithRunner(runner), WithBinary("claude")).Review(
		context.Background(), Request{Input: testInput})

	if !errors.Is(err, ErrExecutableNotFound) {
		t.Fatalf("errors.Is(err, ErrExecutableNotFound) = false; err = %v", err)
	}

	// The message must be actionable, and must not mention API keys.
	message := err.Error()
	for _, want := range []string{"Claude Code", BinaryEnvVar} {
		if !strings.Contains(message, want) {
			t.Errorf("error %q does not mention %q", message, want)
		}
	}
	for _, unwanted := range []string{"API key", "API_KEY", "ANTHROPIC"} {
		if strings.Contains(message, unwanted) {
			t.Errorf("error %q mentions %q; authentication is not this tool's concern", message, unwanted)
		}
	}
}

func TestReviewStartFailure(t *testing.T) {
	runner := &fakeRunner{err: fmt.Errorf("%w: permission denied", ErrStartFailed)}

	_, err := NewClient(WithRunner(runner)).Review(context.Background(), Request{Input: testInput})
	if !errors.Is(err, ErrStartFailed) {
		t.Errorf("errors.Is(err, ErrStartFailed) = false; err = %v", err)
	}
}

func TestReviewTimeout(t *testing.T) {
	runner := &fakeRunner{result: CommandResult{TimedOut: true, ExitCode: -1, Duration: time.Second}}

	got, err := NewClient(WithRunner(runner), WithTimeout(time.Second)).Review(
		context.Background(), Request{Input: testInput})

	if !errors.Is(err, ErrTimedOut) {
		t.Fatalf("errors.Is(err, ErrTimedOut) = false; err = %v", err)
	}
	if !got.TimedOut {
		t.Error("Result.TimedOut = false for a timed-out invocation")
	}
	if !strings.Contains(err.Error(), "1s") {
		t.Errorf("error = %q, want it to name the timeout", err)
	}
}

// TestReviewAppliesItsOwnTimeout checks the client bounds the runner's context.
func TestReviewAppliesItsOwnTimeout(t *testing.T) {
	var deadlineSet bool

	runner := &fakeRunner{result: CommandResult{Stdout: envelopeJSON("ok")}}
	runner.onRun = func(CommandRequest) {}

	client := NewClient(WithTimeout(30*time.Second), WithRunner(&deadlineRunner{
		inner: runner,
		check: func(ctx context.Context) {
			_, deadlineSet = ctx.Deadline()
		},
	}))

	if _, err := client.Review(context.Background(), Request{Input: testInput}); err != nil {
		t.Fatalf("Review() returned error: %v", err)
	}
	if !deadlineSet {
		t.Error("the runner's context carries no deadline; the client did not bound the invocation")
	}
}

// deadlineRunner inspects the context it is given, then delegates.
type deadlineRunner struct {
	inner *fakeRunner
	check func(ctx context.Context)
}

func (d *deadlineRunner) Run(ctx context.Context, request CommandRequest) (CommandResult, error) {
	d.check(ctx)
	return d.inner.Run(ctx, request)
}

func TestReviewContextCancellation(t *testing.T) {
	runner := &fakeRunner{err: context.Canceled}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewClient(WithRunner(runner)).Review(ctx, Request{Input: testInput})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; err = %v", err)
	}
}

func TestReviewOutputTruncation(t *testing.T) {
	t.Run("truncated but still parseable", func(t *testing.T) {
		runner := &fakeRunner{result: CommandResult{
			Stdout:          envelopeJSON("review text"),
			StdoutTruncated: true,
		}}

		got, err := NewClient(WithRunner(runner)).Review(context.Background(), Request{Input: testInput})
		if err != nil {
			t.Fatalf("Review() returned error: %v", err)
		}
		if !got.Truncated {
			t.Error("Truncated = false although the runner reported truncation")
		}
		if got.Output != "review text" {
			t.Errorf("Output = %q, want the parsed text", got.Output)
		}
	})

	t.Run("truncated beyond parsing", func(t *testing.T) {
		runner := &fakeRunner{result: CommandResult{
			Stdout:          `{"type":"result","result":"a very long review that got cut`,
			StdoutTruncated: true,
		}}

		got, err := NewClient(WithRunner(runner), WithMaxOutputBytes(1024)).Review(
			context.Background(), Request{Input: testInput})

		if !errors.Is(err, ErrOutputTooLarge) {
			t.Fatalf("errors.Is(err, ErrOutputTooLarge) = false; err = %v", err)
		}
		if !strings.Contains(err.Error(), "1024") {
			t.Errorf("error = %q, want it to name the limit", err)
		}
		if !got.Truncated {
			t.Error("Truncated = false on an over-long output")
		}
	})
}

func TestReviewEmptyInput(t *testing.T) {
	runner := okRunner("ok")

	for _, input := range []string{"", "   ", "\n\t "} {
		_, err := NewClient(WithRunner(runner)).Review(context.Background(), Request{Input: input})
		if !errors.Is(err, ErrEmptyInput) {
			t.Errorf("Review(%q) error = %v, want ErrEmptyInput", input, err)
		}
	}

	if len(runner.requests) != 0 {
		t.Errorf("the runner was called %d times for empty input", len(runner.requests))
	}
}

func TestClientAvailable(t *testing.T) {
	t.Run("a missing executable is reported clearly", func(t *testing.T) {
		client := NewClient(WithBinary("definitely-not-a-real-executable-arc-test"))

		err := client.Available(context.Background())
		if !errors.Is(err, ErrExecutableNotFound) {
			t.Fatalf("errors.Is(err, ErrExecutableNotFound) = false; err = %v", err)
		}
		if !strings.Contains(err.Error(), BinaryEnvVar) {
			t.Errorf("error %q does not suggest %s", err, BinaryEnvVar)
		}
	})

	t.Run("an executable on PATH is found", func(t *testing.T) {
		// "go" is certainly present while these tests run.
		if err := NewClient(WithBinary("go")).Available(context.Background()); err != nil {
			t.Errorf("Available() returned error for an executable on PATH: %v", err)
		}
	})

	t.Run("availability does not run a review", func(t *testing.T) {
		runner := okRunner("ok")
		client := NewClient(WithBinary("go"), WithRunner(runner))

		if err := client.Available(context.Background()); err != nil {
			t.Fatalf("Available() returned error: %v", err)
		}
		if len(runner.requests) != 0 {
			t.Errorf("Available() ran %d invocations, want none", len(runner.requests))
		}
	})

	t.Run("a cancelled context is reported", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if err := NewClient(WithBinary("go")).Available(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("errors.Is(err, context.Canceled) = false; err = %v", err)
		}
	})
}

func TestClientOptions(t *testing.T) {
	t.Setenv(BinaryEnvVar, "")

	client := NewClient(
		WithBinary("/opt/claude"),
		WithTimeout(90*time.Second),
		WithMaxOutputBytes(4096),
		WithRunner(okRunner("ok")),
	)

	if client.Binary() != "/opt/claude" {
		t.Errorf("Binary() = %q", client.Binary())
	}
	if client.Timeout() != 90*time.Second {
		t.Errorf("Timeout() = %v", client.Timeout())
	}
	if client.maxOutputBytes != 4096 {
		t.Errorf("maxOutputBytes = %d", client.maxOutputBytes)
	}

	// Invalid values are ignored rather than producing a broken client.
	fallback := NewClient(WithTimeout(0), WithTimeout(-time.Second), WithMaxOutputBytes(0), WithRunner(nil))
	if fallback.Timeout() != DefaultTimeout {
		t.Errorf("Timeout() = %v, want the default", fallback.Timeout())
	}
	if fallback.maxOutputBytes != MaxOutputBytes {
		t.Errorf("maxOutputBytes = %d, want the default", fallback.maxOutputBytes)
	}
	if fallback.runner == nil {
		t.Error("a nil runner replaced the default")
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "single line", in: "one line", want: "one line"},
		{name: "several lines", in: "first\nsecond\nthird", want: "first"},
		{name: "leading whitespace", in: "  padded  \nnext", want: "padded"},
		{name: "empty", in: "", want: ""},
		{name: "very long line is bounded", in: strings.Repeat("x", 500), want: strings.Repeat("x", 200) + "…"},
	}

	for _, tt := range tests {
		if got := firstLine(tt.in); got != tt.want {
			t.Errorf("firstLine(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}
