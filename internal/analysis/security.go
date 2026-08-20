package analysis

import (
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/your-company/agentic-code-review/internal/technology"
)

// Security scanner timeouts. A scanner is worth waiting for but never worth
// holding the review open indefinitely.
const (
	GosecTimeout   = 3 * time.Minute
	SemgrepTimeout = 5 * time.Minute
)

// SemgrepRulesEnv names the operator-supplied Semgrep configuration.
//
// It is deliberately unset by default, and there is deliberately no built-in
// value: `--config=auto` would make ARC fetch rules from a remote registry on
// every review, which is not a decision this tool gets to make for an operator.
// Setting it is an explicit choice to run whatever that configuration names.
const SemgrepRulesEnv = "ARC_SEMGREP_RULES"

// semgrepRulesPattern bounds what may reach the argument list. The value is
// operator configuration, not repository content, but it still becomes a process
// argument: a value able to introduce a second flag would let configuration
// change what the scanner does rather than which rules it uses.
var semgrepRulesPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@:+-]{0,199}$`)

// SemgrepRules returns the configured Semgrep rule set, and whether it is usable.
func SemgrepRules() (string, bool) {
	value := strings.TrimSpace(os.Getenv(SemgrepRulesEnv))
	if value == "" || !semgrepRulesPattern.MatchString(value) {
		return "", false
	}
	return value, true
}

// gosecToolchain runs gosec, Go's static security analyzer.
//
// It is local: gosec ships its rules, so a scan reaches no network and produces
// the same result on a developer machine and in CI. A finding is reported as a
// failing check, which is what makes it evidence a reviewer and a verifier can
// both read — a security claim resting on a tool result rather than on a model's
// assertion is the whole point of running it.
type gosecToolchain struct{}

func (gosecToolchain) Name() string { return "gosec" }

// BuildSystem reports Go: gosec is part of reviewing a Go repository, and the
// field exists for grouping output rather than for deciding what to run.
func (gosecToolchain) BuildSystem() technology.BuildSystem { return technology.BuildSystemGo }

func (gosecToolchain) Detect(profile technology.Profile) bool {
	return profile.HasBuildSystem(technology.BuildSystemGo) ||
		profile.HasLanguage(technology.LanguageGo)
}

func (gosecToolchain) Checks() []Check {
	return []Check{
		{
			Name:    "gosec",
			Command: "gosec",
			// -quiet prints only findings, so a clean scan produces no output and
			// a failing one produces exactly the evidence.
			Args:          []string{"-quiet", "./..."},
			Timeout:       GosecTimeout,
			BuildSystem:   technology.BuildSystemGo,
			RequiresFiles: []string{"go.mod", "go.work"},
		},
	}
}

// semgrepToolchain runs Semgrep against the files the pull request changed.
//
// Scanning the whole repository would report pre-existing findings in untouched
// code, which the changed-file scope rule rejects anyway: the review would spend
// its evidence budget on problems this pull request did not introduce. Scoping the
// scan to the changed paths keeps the evidence about the change.
type semgrepToolchain struct{}

func (semgrepToolchain) Name() string { return "semgrep" }

// BuildSystem reports Go only so the check groups with the rest of the output; the
// scanner itself is language-agnostic and never decides what to run from this.
func (semgrepToolchain) BuildSystem() technology.BuildSystem { return technology.BuildSystemGo }

// Detect requires an operator-configured rule set. Without one there is nothing
// to run, and guessing a rule set would mean choosing what "security" means on
// someone else's behalf.
func (semgrepToolchain) Detect(profile technology.Profile) bool {
	_, configured := SemgrepRules()
	return configured && !profile.Empty()
}

func (semgrepToolchain) Checks() []Check {
	rules, ok := SemgrepRules()
	if !ok {
		return nil
	}
	return []Check{
		{
			Name:    "semgrep",
			Command: "semgrep",
			Args: []string{
				"scan",
				"--config", rules,
				// Findings must set the exit status, or a scan would look like a pass.
				"--error",
				"--quiet",
				"--no-git-ignore",
				// Nothing about a review may modify the repository.
				"--no-autofix",
				"--metrics=off",
			},
			Timeout:             SemgrepTimeout,
			BuildSystem:         technology.BuildSystemGo,
			ScopeToChangedPaths: true,
		},
	}
}

// scannableExtensions are the file types a scoped scan will pass to a scanner.
// Anything else — a lock file, an image, a document — would be noise in the
// argument list.
var scannableExtensions = []string{
	".go", ".scala", ".sc", ".js", ".jsx", ".mjs", ".cjs",
	".ts", ".tsx", ".mts", ".cts", ".py", ".rb", ".java", ".kt",
	".yaml", ".yml", ".json", ".tf", ".sql", ".sh",
}

// MaxScopedPaths bounds how many changed paths are passed to a scoped scanner. A
// very large pull request is scanned in part rather than producing an argument
// list no process would accept.
const MaxScopedPaths = 200

// scopedPathsPattern rejects anything that is not a plain relative path.
//
// The paths come from GitHub's changed-file list rather than from a person, but
// they still become process arguments, and a path starting with a dash would be
// read as a flag. Absolute paths and parent traversal are refused for the same
// reason the evidence connectors refuse them: a scan must stay inside the
// checkout it was pointed at.
var scopedPathsPattern = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9._/@+-]*$`)

// scopedPaths filters a changed-file list down to what a scanner should be given.
func scopedPaths(paths []string) []string {
	var kept []string
	seen := map[string]bool{}

	for _, path := range paths {
		clean := strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
		if clean == "" || seen[clean] || !scopedPathsPattern.MatchString(clean) {
			continue
		}
		if strings.Contains(clean, "/../") || strings.HasSuffix(clean, "/..") {
			continue
		}
		if !hasScannableExtension(clean) {
			continue
		}
		seen[clean] = true
		kept = append(kept, clean)
		if len(kept) >= MaxScopedPaths {
			break
		}
	}
	return kept
}

func hasScannableExtension(path string) bool {
	lower := strings.ToLower(path)
	for _, ext := range scannableExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}
