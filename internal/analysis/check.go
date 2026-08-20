// Package analysis runs trusted, code-owned tooling against a local repository
// checkout and returns structured evidence.
//
// Everything here is deterministic. Commands come only from the check
// definitions in this file — never from a model, a Jira ticket, a pull request
// description, or repository rule text — and are executed directly, never through
// a shell.
package analysis

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/your-company/agentic-code-review/internal/technology"
)

const (
	// MaxOutputBytes caps the captured stdout and stderr of one check, applied
	// to each stream separately. Tool output eventually becomes review context,
	// so the bound is enforced here rather than downstream.
	MaxOutputBytes = 64 * 1024
)

// truncationMarkerFormat is appended to output that hit MaxOutputBytes.
const truncationMarkerFormat = "\n[TRUNCATED: command output exceeded %d bytes]\n"

// Check is one deterministic command to run. Checks are defined in Go code; no
// configuration file or user input can introduce one.
type Check struct {
	// Name identifies the check, e.g. "go-test".
	Name string

	// Command is the executable to run. It is passed to exec directly, so it is
	// never interpreted by a shell.
	Command string

	// Args are the arguments passed to Command, already split. They are never
	// concatenated into a command line.
	Args []string

	// Timeout bounds this check alone. Zero means DefaultCheckTimeout.
	Timeout time.Duration

	// BuildSystem is the toolchain this check belongs to, for reporting.
	BuildSystem technology.BuildSystem

	// RequiresFiles lists the marker files that make this check meaningful, any
	// one of which is enough. A checkout with none of them gets a skipped result
	// rather than a failure: "there is no build.sbt here" is not a defect in the
	// pull request.
	RequiresFiles []string

	// RequiresDirectory is a local dependency directory needed before the command
	// is meaningful. It is used for build systems such as npm, where running a
	// project script without installed dependencies produces environment noise rather
	// than evidence about the pull request.
	RequiresDirectory string
}

// DefaultChecks returns the checks for a repository whose toolchain is unknown.
//
// It is the Go set, which is what this tool itself is written in, and it exists only
// for callers that have no profile to offer. The review pipeline does not use it:
// there, checks are chosen by ChecksForProfile from detected technology, so a Scala
// repository never has Go commands run against it.
func DefaultChecks() []Check {
	return ChecksForProfile(technology.Profile{
		Languages:    []technology.Language{technology.LanguageGo},
		BuildSystems: []technology.BuildSystem{technology.BuildSystemGo},
	})
}

// DisplayCommand renders the check as readable text, e.g. "go test ./...".
//
// It is for display and for embedding in review evidence only. It is never
// parsed, and never executed as a shell string.
func (c Check) DisplayCommand() string {
	if len(c.Args) == 0 {
		return c.Command
	}
	return c.Command + " " + strings.Join(c.Args, " ")
}

// EffectiveTimeout returns the check's timeout, falling back to the default.
func (c Check) EffectiveTimeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultCheckTimeout
	}
	return c.Timeout
}

// CheckResult is the structured outcome of one check.
type CheckResult struct {
	Name    string
	Command string

	// Passed is true only when the check ran to completion with exit code 0.
	Passed bool

	// ExitCode is the process exit status, or -1 when no status was obtained
	// (timeout, signal, or a check that never ran).
	ExitCode int

	Stdout string
	Stderr string

	// StdoutTruncated and StderrTruncated report whether MaxOutputBytes was hit.
	StdoutTruncated bool
	StderrTruncated bool

	Duration time.Duration

	// TimedOut reports that the check exceeded its own timeout.
	TimedOut bool

	// Skipped reports that the check was deliberately not run, with SkipReason
	// explaining why. A skipped check is not a failure.
	Skipped    bool
	SkipReason string

	// ExecError describes a failure to execute the command at all — the binary
	// was missing, the directory was unusable. This is an application-level
	// problem, distinct from a check that ran and reported problems.
	ExecError string
}

// Failed reports whether the check ran and did not pass. A skipped check has not
// failed.
func (r CheckResult) Failed() bool { return !r.Passed && !r.Skipped }

// Result is the outcome of an analysis run.
type Result struct {
	Checks []CheckResult

	// RepoDir is the checkout the checks ran against.
	RepoDir string
}

// Passed reports whether no check failed. Skipped checks do not count against
// it, so an analysis that could run nothing has passed vacuously — callers that
// care should consult Skipped or FailedChecks.
func (r Result) Passed() bool { return len(r.FailedChecks()) == 0 }

// HasFailures reports whether any check failed.
func (r Result) HasFailures() bool { return len(r.FailedChecks()) > 0 }

// FailedChecks returns the checks that ran and did not pass, in order.
func (r Result) FailedChecks() []CheckResult {
	var failed []CheckResult
	for _, c := range r.Checks {
		if c.Failed() {
			failed = append(failed, c)
		}
	}
	return failed
}

// SkippedChecks returns the checks that were not run, in order.
func (r Result) SkippedChecks() []CheckResult {
	var skipped []CheckResult
	for _, c := range r.Checks {
		if c.Skipped {
			skipped = append(skipped, c)
		}
	}
	return skipped
}

// Empty reports whether no checks were recorded at all.
func (r Result) Empty() bool { return len(r.Checks) == 0 }

// truncateOutput bounds s to MaxOutputBytes, retaining both the beginning and
// end. Build tools commonly print setup noise first and the actionable failure
// summary last, so prefix-only truncation loses the most useful evidence.
func truncateOutput(s string) (string, bool) {
	if len(s) <= MaxOutputBytes {
		return s, false
	}

	headBytes := MaxOutputBytes / 2
	tailBytes := MaxOutputBytes - headBytes
	head := trimInvalidUTF8Suffix(s[:headBytes])
	tail := trimInvalidUTF8Prefix(s[len(s)-tailBytes:])
	return head + fmt.Sprintf(truncationMarkerFormat, MaxOutputBytes) + tail, true
}

func trimInvalidUTF8Suffix(s string) string {
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func trimInvalidUTF8Prefix(s string) string {
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[1:]
	}
	return s
}

// TruncationMarker returns the marker appended to truncated output. Exposed so
// tests and later stages can recognise it.
func TruncationMarker() string {
	return strings.TrimSpace(fmt.Sprintf(truncationMarkerFormat, MaxOutputBytes))
}
