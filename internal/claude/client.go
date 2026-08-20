package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Client invokes the locally installed Claude Code CLI.
//
// It owns the invocation shape — the binary, the arguments, the timeout, and the
// output bound — so nothing else in the application executes Claude directly.
type Client struct {
	binary  string
	timeout time.Duration
	runner  Runner

	maxOutputBytes int
}

// Option configures a Client.
type Option func(*Client)

// WithBinary sets the executable path. This is a path only, never a command line.
func WithBinary(binary string) Option {
	return func(c *Client) {
		if strings.TrimSpace(binary) != "" {
			c.binary = strings.TrimSpace(binary)
		}
	}
}

// WithTimeout sets the invocation timeout.
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		if timeout > 0 {
			c.timeout = timeout
		}
	}
}

// WithRunner replaces the process runner, which is how tests avoid executing
// anything.
func WithRunner(runner Runner) Option {
	return func(c *Client) {
		if runner != nil {
			c.runner = runner
		}
	}
}

// WithMaxOutputBytes sets the output bound.
func WithMaxOutputBytes(limit int) Option {
	return func(c *Client) {
		if limit > 0 {
			c.maxOutputBytes = limit
		}
	}
}

// NewClient returns a Client using the default binary, timeout, and runner.
//
// The binary defaults to "claude" and may be overridden by $ARC_CLAUDE_BINARY.
// No credential is read: authentication belongs entirely to the user's local
// Claude Code installation.
func NewClient(opts ...Option) *Client {
	client := &Client{
		binary:         binaryFromEnv(),
		timeout:        DefaultTimeout,
		runner:         NewCommandRunner(),
		maxOutputBytes: MaxOutputBytes,
	}

	for _, opt := range opts {
		opt(client)
	}
	return client
}

// binaryFromEnv reads the executable path override, falling back to the default.
func binaryFromEnv() string {
	if binary := strings.TrimSpace(os.Getenv(BinaryEnvVar)); binary != "" {
		return binary
	}
	return DefaultBinary
}

// Binary returns the configured executable.
func (c *Client) Binary() string { return c.binary }

// Timeout returns the configured invocation timeout.
func (c *Client) Timeout() time.Duration { return c.timeout }

// Available reports whether the Claude Code executable can be found, without
// running a review.
func (c *Client) Available(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if _, err := exec.LookPath(c.binary); err != nil {
		return fmt.Errorf("%w: %s: install or configure Claude Code, or set %s to its path",
			ErrExecutableNotFound, c.binary, BinaryEnvVar)
	}
	return nil
}

// reviewArgs is the non-interactive invocation: print mode with a JSON transport
// envelope. The review input travels on stdin, never in these arguments.
func reviewArgs() []string {
	return []string{"-p", "--output-format", "json"}
}

// Review runs one review and returns the normalized result.
func (c *Client) Review(ctx context.Context, request Request) (Result, error) {
	return c.Invoke(ctx, request)
}

// Verify runs one verification and returns the normalized result.
//
// The transport is identical to a review's — the same executable, the same print mode,
// the same bounds — because what distinguishes the two is entirely the input. Keeping it
// a separate method means a caller states which stage it is in, and neither stage can
// quietly acquire capabilities meant for the other.
func (c *Client) Verify(ctx context.Context, request Request) (Result, error) {
	return c.Invoke(ctx, request)
}

// Invoke runs Claude Code once with the given input and returns the normalized result.
//
// The invocation is bounded by the client's timeout and the output limit. A
// non-zero exit or a timeout produces a Result describing what happened alongside
// an error, so callers can report the detail rather than just a failure.
func (c *Client) Invoke(ctx context.Context, request Request) (Result, error) {
	if strings.TrimSpace(request.Input) == "" {
		return Result{}, ErrEmptyInput
	}

	runCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	commandResult, err := c.runner.Run(runCtx, CommandRequest{
		Binary:           c.binary,
		Args:             reviewArgs(),
		Stdin:            request.Input,
		WorkingDirectory: request.WorkingDirectory,
		MaxOutputBytes:   c.maxOutputBytes,
	})

	result := Result{
		RawOutput: commandResult.Stdout,
		ExitCode:  commandResult.ExitCode,
		Duration:  commandResult.Duration,
		TimedOut:  commandResult.TimedOut,
		Truncated: commandResult.StdoutTruncated,
	}

	if err != nil {
		if errors.Is(err, ErrExecutableNotFound) {
			return result, fmt.Errorf("%w: %s: install or configure Claude Code, or set %s to its path",
				ErrExecutableNotFound, c.binary, BinaryEnvVar)
		}
		return result, err
	}

	if commandResult.TimedOut {
		return result, fmt.Errorf("%w after %s", ErrTimedOut, c.timeout)
	}

	if commandResult.ExitCode != 0 {
		return result, fmt.Errorf("Claude Code exited with status %d%s",
			commandResult.ExitCode, stderrDetail(commandResult.Stderr))
	}

	envelope, err := parseEnvelope(commandResult.Stdout)
	if err != nil {
		if commandResult.StdoutTruncated {
			// Truncation is the more useful diagnosis: the JSON is unparseable
			// because it was cut, not because Claude produced something odd.
			return result, fmt.Errorf("%w: output was truncated at %d bytes",
				ErrOutputTooLarge, c.maxOutputBytes)
		}
		return result, err
	}

	result.Output = envelope.text
	result.SessionID = envelope.sessionID
	result.CostUSD = envelope.costUSD
	result.NumTurns = envelope.numTurns

	if strings.TrimSpace(result.Output) == "" {
		return result, ErrEmptyOutput
	}
	if envelope.isError {
		return result, fmt.Errorf("Claude Code reported an error: %s", firstLine(result.Output))
	}

	return result, nil
}

// envelope is the part of the Claude Code JSON transport this step needs. The
// final review schema arrives in a later step; here we only recover the
// assistant's text.
type envelope struct {
	text      string
	sessionID string
	costUSD   float64
	numTurns  int
	isError   bool
}

// transportEnvelope mirrors the CLI's JSON output. Parsing is deliberately
// tolerant: field names are matched loosely so a CLI update that renames or adds
// fields degrades to a clear error rather than a silent empty review.
type transportEnvelope struct {
	Type      string  `json:"type"`
	Subtype   string  `json:"subtype"`
	IsError   bool    `json:"is_error"`
	Result    string  `json:"result"`
	Text      string  `json:"text"`
	SessionID string  `json:"session_id"`
	CostUSD   float64 `json:"total_cost_usd"`
	NumTurns  int     `json:"num_turns"`
}

// parseEnvelope extracts the review text from the transport JSON. It accepts a
// single object or an array of them, taking the last result-bearing entry.
func parseEnvelope(raw string) (envelope, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return envelope{}, ErrEmptyOutput
	}

	switch trimmed[0] {
	case '{':
		var t transportEnvelope
		if err := json.Unmarshal([]byte(trimmed), &t); err != nil {
			return envelope{}, fmt.Errorf("%w: %v", ErrInvalidOutput, err)
		}
		return fromTransport(t), nil

	case '[':
		var entries []transportEnvelope
		if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
			return envelope{}, fmt.Errorf("%w: %v", ErrInvalidOutput, err)
		}
		if len(entries) == 0 {
			return envelope{}, ErrEmptyOutput
		}

		// Prefer the last entry carrying text, which is where the CLI puts the
		// final result.
		for i := len(entries) - 1; i >= 0; i-- {
			if e := fromTransport(entries[i]); e.text != "" {
				return e, nil
			}
		}
		return fromTransport(entries[len(entries)-1]), nil

	default:
		return envelope{}, fmt.Errorf("%w: output is not JSON", ErrInvalidOutput)
	}
}

// fromTransport normalizes one transport object.
func fromTransport(t transportEnvelope) envelope {
	text := t.Result
	if text == "" {
		text = t.Text
	}

	return envelope{
		text:      text,
		sessionID: t.SessionID,
		costUSD:   t.CostUSD,
		numTurns:  t.NumTurns,
		isError:   t.IsError,
	}
}

// stderrDetail renders a short, bounded stderr fragment for an error message.
func stderrDetail(stderr string) string {
	trimmed := strings.TrimSpace(stderr)
	if trimmed == "" {
		return ""
	}
	return ": " + firstLine(trimmed)
}

// firstLine returns the first line of s, bounded in length.
func firstLine(s string) string {
	const maxLen = 200

	line := s
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	line = strings.TrimSpace(line)
	if len(line) > maxLen {
		line = line[:maxLen] + "…"
	}
	return line
}
