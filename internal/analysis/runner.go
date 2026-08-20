package analysis

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// Runner executes a single check inside dir and reports the outcome.
//
// A Runner never returns an error: a check that runs and reports problems is
// evidence, not a failure of the tool. Genuine execution failures are recorded in
// CheckResult.ExecError for the analyzer to raise.
type Runner interface {
	Run(ctx context.Context, dir string, check Check) CheckResult
}

// ToolLocator reports whether a check's executable can be found.
//
// It is optional: a Runner that implements it lets the analyzer turn a missing tool
// into a skipped check rather than discovering the absence mid-execution. Knowing
// whether a process exists belongs to whatever runs processes, so a fake runner in a
// test is not asked the question at all.
type ToolLocator interface {
	LookupTool(name string) error
}

// CommandRunner executes checks as real processes.
//
// It calls the executable directly through exec.CommandContext. No shell is
// involved — there is no "sh -c" anywhere — so nothing in the command or its
// arguments can be interpreted as shell syntax.
type CommandRunner struct{}

// NewCommandRunner returns a runner that executes real commands.
func NewCommandRunner() *CommandRunner { return &CommandRunner{} }

// LookupTool reports whether name can be found on PATH.
func (r *CommandRunner) LookupTool(name string) error {
	_, err := exec.LookPath(name)
	return err
}

// Run executes check in dir under its own timeout, capturing stdout and stderr
// separately and bounding each.
func (r *CommandRunner) Run(ctx context.Context, dir string, check Check) CheckResult {
	result := CheckResult{
		Name:     check.Name,
		Command:  check.DisplayCommand(),
		ExitCode: -1,
	}

	// Each check gets its own bounded execution rather than sharing one budget
	// across the whole analysis.
	checkCtx, cancel := context.WithTimeout(ctx, check.EffectiveTimeout())
	defer cancel()

	// The executable and every argument come from a code-owned Check.
	cmd := exec.CommandContext(checkCtx, check.Command, check.Args...)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	result.Duration = time.Since(start)

	result.Stdout, result.StdoutTruncated = truncateOutput(stdout.String())
	result.Stderr, result.StderrTruncated = truncateOutput(stderr.String())

	switch {
	case err == nil:
		result.Passed = true
		result.ExitCode = 0

	case errors.Is(checkCtx.Err(), context.DeadlineExceeded):
		// The check outran its own timeout; that is a check outcome, not an
		// application failure.
		result.TimedOut = true

	case errors.Is(checkCtx.Err(), context.Canceled):
		// The caller gave up. Report it as an execution problem so the analyzer
		// surfaces the cancellation rather than reporting a silent failure.
		result.ExecError = context.Canceled.Error()

	default:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// The tool ran and reported problems. This is the evidence we want.
			result.ExitCode = exitErr.ExitCode()
			break
		}
		// The command could not be started at all: missing binary, unusable
		// directory. That is an application-level problem.
		result.ExecError = err.Error()
	}

	return result
}
