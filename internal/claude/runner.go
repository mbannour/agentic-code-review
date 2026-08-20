package claude

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner executes one Claude Code invocation.
//
// It returns an error only when the process could not be run at all. A non-zero
// exit status is reported in CommandResult, since that is an outcome the client
// interprets rather than a failure of the runner.
type Runner interface {
	Run(ctx context.Context, request CommandRequest) (CommandResult, error)
}

// CommandRunner executes Claude Code as a real process.
//
// It uses exec.CommandContext with an explicit binary and argument slice. No
// shell is involved — there is no "sh -c" anywhere — so no part of the request can
// be interpreted as shell syntax.
type CommandRunner struct{}

// NewCommandRunner returns a runner that executes the real Claude Code CLI.
func NewCommandRunner() *CommandRunner { return &CommandRunner{} }

// Run executes the request, capturing bounded stdout and stderr.
func (r *CommandRunner) Run(ctx context.Context, request CommandRequest) (CommandResult, error) {
	if strings.TrimSpace(request.Binary) == "" {
		return CommandResult{}, fmt.Errorf("%w: no executable configured", ErrExecutableNotFound)
	}

	// Resolve the binary first so a missing installation is a clear, actionable
	// error rather than a generic exec failure.
	if _, err := exec.LookPath(request.Binary); err != nil {
		return CommandResult{}, fmt.Errorf("%w: %s", ErrExecutableNotFound, request.Binary)
	}

	cmd := exec.CommandContext(ctx, request.Binary, request.Args...)
	cmd.Dir = request.WorkingDirectory

	// The whole review input travels on stdin, never in the argument list.
	cmd.Stdin = strings.NewReader(request.Stdin)

	// The process environment is inherited untouched, so the user's existing
	// Claude Code session applies. It is never inspected, serialized, or logged.
	limit := request.MaxOutputBytes
	if limit <= 0 {
		limit = MaxOutputBytes
	}

	stdout := &boundedBuffer{limit: limit}
	stderr := &boundedBuffer{limit: limit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	start := time.Now()
	err := cmd.Run()

	result := CommandResult{
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		Duration:        time.Since(start),
		ExitCode:        -1,
		StdoutTruncated: stdout.Truncated(),
	}

	switch {
	case err == nil:
		result.ExitCode = 0
		return result, nil

	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.TimedOut = true
		return result, nil

	case errors.Is(ctx.Err(), context.Canceled):
		return result, ctx.Err()

	default:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, fmt.Errorf("%w: %v", ErrStartFailed, err)
	}
}

// boundedBuffer accumulates output up to a byte limit and discards the rest, so a
// runaway subprocess cannot exhaust memory. Discarding rather than erroring keeps
// whatever prefix arrived usable.
type boundedBuffer struct {
	limit     int
	buf       []byte
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - len(b.buf)
	if remaining <= 0 {
		b.truncated = true
		// Report the full write so the process is not stopped by a short write.
		return len(p), nil
	}

	if len(p) > remaining {
		b.buf = append(b.buf, p[:remaining]...)
		b.truncated = true
		return len(p), nil
	}

	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *boundedBuffer) String() string { return string(b.buf) }

// Truncated reports whether output was discarded.
func (b *boundedBuffer) Truncated() bool { return b.truncated }
