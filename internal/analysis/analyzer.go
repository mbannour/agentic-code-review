package analysis

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/your-company/agentic-code-review/internal/technology"
)

// Reasons a check was not run. They are stable strings, since they appear in
// terminal output and become review evidence.
const (
	// SkipReasonNoGoModule is recorded for Go checks in a checkout with no go.mod.
	SkipReasonNoGoModule = "go.mod not found"

	// skipReasonMissingFilesFormat is recorded when none of a check's marker files
	// exist in the checkout.
	skipReasonMissingFilesFormat = "%s not found"

	// SkipReasonNoScannablePaths is recorded for a scoped scanner when the pull
	// request changed nothing it scans. Scanning everything instead would report
	// findings about code this pull request never touched.
	SkipReasonNoScannablePaths = "no changed files of a scannable type"

	// skipReasonMissingToolFormat is recorded when the tool itself is not installed.
	// A missing toolchain degrades the review's evidence; it is not a failure of the
	// pull request, and it must not end the run.
	skipReasonMissingToolFormat = "%s executable not found"
)

// Analyzer orchestrates the deterministic checks: it validates the checkout, decides
// which of its checks can meaningfully run, and delegates process execution to a
// Runner.
//
// It does not detect languages. The check set is chosen before it is constructed,
// from a technology.Profile, so this type never inspects repository content to decide
// what to run.
type Analyzer struct {
	runner Runner
	checks []Check

	// changedPaths are the pull request's changed files, used only by checks that
	// declare ScopeToChangedPaths. They come from the GitHub change list, never
	// from repository content.
	changedPaths []string
}

// NewAnalyzer returns an Analyzer running the default check set through runner.
func NewAnalyzer(runner Runner) *Analyzer {
	return NewAnalyzerWithChecks(runner, DefaultChecks())
}

// NewAnalyzerForProfile returns an Analyzer running the checks that the detected
// technology calls for. It is how the review pipeline builds one.
func NewAnalyzerForProfile(runner Runner, profile technology.Profile) *Analyzer {
	return NewAnalyzerWithChecks(runner, ChecksForProfile(profile))
}

// NewAnalyzerWithChecks returns an Analyzer running an explicit check set. The checks
// still come from calling code, never from external input.
func NewAnalyzerWithChecks(runner Runner, checks []Check) *Analyzer {
	return &Analyzer{runner: runner, checks: copyChecks(checks)}
}

// NewAnalyzerForProfileWithChanges returns an Analyzer that can also run checks
// scoped to the pull request's changed files.
//
// The paths are filtered before they reach an argument list: only file types a
// scanner reads, only plain relative paths, and only up to a bound.
func NewAnalyzerForProfileWithChanges(runner Runner, profile technology.Profile, changedPaths []string) *Analyzer {
	analyzer := NewAnalyzerWithChecks(runner, ChecksForProfile(profile))
	analyzer.changedPaths = scopedPaths(changedPaths)
	return analyzer
}

// ScopedPaths returns the changed paths this analyzer would pass to a scoped
// check, after filtering.
func (a *Analyzer) ScopedPaths() []string {
	out := make([]string, len(a.changedPaths))
	copy(out, a.changedPaths)
	return out
}

// Checks returns the checks this analyzer will run, in order.
func (a *Analyzer) Checks() []Check { return copyChecks(a.checks) }

// Analyze runs every applicable check against the checkout at repoDir.
//
// It returns an error only for application-level problems: an unusable repository
// path, a command that could not be started for an unexpected reason, or a cancelled
// context. Everything else is evidence. A check that runs and reports problems is
// recorded with Passed false; a check whose tool is not installed, or whose build
// file is absent, is recorded as skipped with the reason. All checks run even when an
// earlier one fails, because one failing suite does not make the others uninteresting.
func (a *Analyzer) Analyze(ctx context.Context, repoDir string) (Result, error) {
	if a.runner == nil {
		return Result{}, errors.New("analysis: no runner configured")
	}

	dir, err := validateRepoDir(repoDir)
	if err != nil {
		return Result{}, err
	}

	result := Result{RepoDir: dir}

	for _, check := range a.checks {
		// Stop early only if the caller has given up.
		if err := ctx.Err(); err != nil {
			return Result{}, fmt.Errorf("analysis of %s interrupted: %w", dir, err)
		}

		if reason, skip := a.skipReason(dir, check); skip {
			result.Checks = append(result.Checks, CheckResult{
				Name:       check.Name,
				Command:    check.DisplayCommand(),
				ExitCode:   -1,
				Skipped:    true,
				SkipReason: reason,
			})
			continue
		}

		checkResult := a.runner.Run(ctx, dir, a.resolve(check))
		if checkResult.ExecError != "" {
			return Result{}, fmt.Errorf("run check %s (%s): %s",
				check.Name, check.DisplayCommand(), checkResult.ExecError)
		}

		result.Checks = append(result.Checks, checkResult)
	}

	return result, nil
}

// resolve returns the check as it will actually be executed, with the changed
// paths appended for a scoped scanner.
//
// The paths are appended last, after every code-owned flag, so no configured value
// can be read as a flag by the tool.
func (a *Analyzer) resolve(check Check) Check {
	if !check.ScopeToChangedPaths || len(a.changedPaths) == 0 {
		return check
	}

	resolved := check
	resolved.Args = make([]string, 0, len(check.Args)+len(a.changedPaths)+1)
	resolved.Args = append(resolved.Args, check.Args...)
	// An explicit end-of-flags marker, so a path can never be taken for an option.
	resolved.Args = append(resolved.Args, "--")
	resolved.Args = append(resolved.Args, a.changedPaths...)
	return resolved
}

// skipReason decides whether a check can meaningfully run in dir.
//
// Missing build files, installed dependencies, or executables make a check pointless
// rather than failing. They are reported as skips so the review continues with less
// evidence rather than stopping — a developer without sbt or node_modules on their
// machine should still get a review.
func (a *Analyzer) skipReason(dir string, check Check) (string, bool) {
	if len(check.RequiresFiles) > 0 && !anyFileExists(dir, check.RequiresFiles) {
		// The historical wording for Go is preserved, since it appears in output
		// and in tests.
		if len(check.RequiresFiles) > 0 && check.RequiresFiles[0] == "go.mod" {
			return SkipReasonNoGoModule, true
		}
		return fmt.Sprintf(skipReasonMissingFilesFormat, strings.Join(check.RequiresFiles, " or ")), true
	}
	if check.ScopeToChangedPaths && len(a.changedPaths) == 0 {
		return SkipReasonNoScannablePaths, true
	}
	if check.RequiresDirectory != "" && !directoryExists(dir, check.RequiresDirectory) {
		return fmt.Sprintf("%s directory not found", check.RequiresDirectory), true
	}

	// Only a runner that actually starts processes is asked whether the tool exists.
	if locator, ok := a.runner.(ToolLocator); ok && check.Command != "" {
		if err := locator.LookupTool(check.Command); err != nil {
			return fmt.Sprintf(skipReasonMissingToolFormat, check.Command), true
		}
	}

	return "", false
}

func directoryExists(dir, name string) bool {
	info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name)))
	return err == nil && info.IsDir()
}

// anyFileExists reports whether at least one of names exists as a file under dir.
func anyFileExists(dir string, names []string) bool {
	for _, name := range names {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name)))
		if err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// validateRepoDir checks that repoDir names an existing directory and returns its
// cleaned absolute path.
func validateRepoDir(repoDir string) (string, error) {
	if repoDir == "" {
		return "", errors.New("analysis: empty repository directory")
	}

	abs, err := filepath.Abs(repoDir)
	if err != nil {
		return "", fmt.Errorf("resolve repository directory %q: %w", repoDir, err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("repository directory %q does not exist", repoDir)
		}
		return "", fmt.Errorf("inspect repository directory %q: %w", repoDir, err)
	}

	if !info.IsDir() {
		return "", fmt.Errorf("repository path %q is not a directory", repoDir)
	}

	return abs, nil
}
