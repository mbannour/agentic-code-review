// Package claude is the execution boundary between this application and the
// locally installed Claude Code CLI.
//
// Authentication is deliberately not this package's concern. It never reads,
// stores, prints, or transmits credentials: it invokes the `claude` executable and
// relies entirely on whatever session the user has already configured in their
// Claude Code installation. No API key of any kind is read from the environment.
//
// The executable is invoked directly, never through a shell, and this step is
// review-only: nothing here permits writing files, committing, or publishing.
package claude

import (
	"errors"
	"time"
)

const (
	// DefaultBinary is the Claude Code executable, resolved through PATH.
	DefaultBinary = "claude"

	// BinaryEnvVar overrides the executable path. It is a path only — never a
	// command line, and never a credential.
	BinaryEnvVar = "ARC_CLAUDE_BINARY"

	// DefaultTimeout bounds one review invocation.
	DefaultTimeout = 5 * time.Minute

	// MaxOutputBytes bounds how much of Claude's output is held in memory.
	MaxOutputBytes = 2 * 1024 * 1024

	// TruncationMarker is appended to output that hit MaxOutputBytes.
	TruncationMarker = "\n[TRUNCATED: Claude output exceeded 2097152 bytes]\n"
)

// Sentinel errors callers can match with errors.Is.
var (
	// ErrExecutableNotFound means the Claude Code CLI could not be located. The
	// fix is to install or configure Claude Code, or to set ARC_CLAUDE_BINARY.
	ErrExecutableNotFound = errors.New("Claude Code executable not found")

	// ErrStartFailed means the process could not be started.
	ErrStartFailed = errors.New("could not start Claude Code")

	// ErrTimedOut means the invocation exceeded its timeout.
	ErrTimedOut = errors.New("Claude Code timed out")

	// ErrEmptyOutput means Claude produced no usable result.
	ErrEmptyOutput = errors.New("Claude Code produced no output")

	// ErrInvalidOutput means the JSON transport envelope could not be parsed.
	ErrInvalidOutput = errors.New("could not parse Claude Code output")

	// ErrOutputTooLarge means the output was truncated so severely that parsing
	// is impossible.
	ErrOutputTooLarge = errors.New("Claude Code output exceeded the size limit")

	// ErrEmptyInput means no review input was supplied.
	ErrEmptyInput = errors.New("empty review input")
)

// Request is one review invocation.
type Request struct {
	// Input is the full review input, delivered on stdin.
	Input string

	// WorkingDirectory is the local checkout to run in, if one is available. It
	// lets Claude Code pick up project-local instructions; the selected context
	// remains the primary input either way. Empty means the current directory.
	WorkingDirectory string
}

// Result is the normalized outcome of a review invocation.
type Result struct {
	// Output is the assistant's review text, extracted from the transport
	// envelope.
	Output string

	// RawOutput is the unparsed stdout, kept for debugging only. It is never
	// printed by default.
	RawOutput string

	ExitCode int
	Duration time.Duration
	TimedOut bool

	// Truncated reports whether the output hit MaxOutputBytes.
	Truncated bool

	// SessionID and CostUSD come from the envelope when present, and are useful
	// for diagnostics. They are not credentials.
	SessionID string
	CostUSD   float64
	NumTurns  int
}

// CommandRequest is what a Runner needs to execute: an explicit binary, explicit
// arguments, and stdin. There is no command string, so nothing can be interpreted
// as shell syntax.
type CommandRequest struct {
	Binary           string
	Args             []string
	Stdin            string
	WorkingDirectory string

	// MaxOutputBytes bounds captured stdout and stderr.
	MaxOutputBytes int
}

// CommandResult is the raw outcome of executing a CommandRequest.
type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
	TimedOut bool

	// StdoutTruncated reports whether stdout hit the byte limit.
	StdoutTruncated bool
}
