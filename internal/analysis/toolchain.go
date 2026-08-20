package analysis

import (
	"time"

	"github.com/your-company/agentic-code-review/internal/technology"
)

// Check timeouts, centralized so the cost of each toolchain is visible in one place.
//
// They differ because an sbt compile can take substantially longer than a Go test or
// vet pass. Per-check bounds keep either toolchain from holding the review open
// indefinitely.
const (
	GoCheckTimeout      = 2 * time.Minute
	SBTCompileTimeout   = 5 * time.Minute
	NPMBuildTimeout     = 10 * time.Minute
	DefaultCheckTimeout = 2 * time.Minute
)

// Toolchain is one build system's deterministic checks.
//
// Implementations answer two questions and nothing else: does this profile involve
// me, and what do I run. They do not detect languages — that already happened in
// internal/technology — and they never derive a command from repository content, a
// model, or anything else outside this package.
type Toolchain interface {
	// Name identifies the toolchain in output.
	Name() string

	// BuildSystem is the profile value this toolchain answers for.
	BuildSystem() technology.BuildSystem

	// Detect reports whether the profile calls for this toolchain.
	Detect(profile technology.Profile) bool

	// Checks returns the commands to run, in order.
	Checks() []Check
}

// Toolchains returns every registered toolchain, in a stable order.
//
// This is the extension point: a new language's checks are added by appending its
// toolchain here. Nothing downstream changes, because everything downstream consumes
// Check values.
func Toolchains() []Toolchain {
	return []Toolchain{
		goToolchain{}, sbtToolchain{}, npmToolchain{},
		gosecToolchain{}, semgrepToolchain{},
	}
}

// npmToolchain runs the production build for npm-managed Next.js applications.
// Tests are intentionally left to CI, matching the compile-only policy used for sbt.
// Requiring the detected Next.js framework avoids assuming every npm package defines
// a build script.
type npmToolchain struct{}

func (npmToolchain) Name() string { return "npm" }

func (npmToolchain) BuildSystem() technology.BuildSystem { return technology.BuildSystemNPM }

func (npmToolchain) Detect(profile technology.Profile) bool {
	return profile.HasBuildSystem(technology.BuildSystemNPM) && profile.HasTechnology("next.js")
}

func (npmToolchain) Checks() []Check {
	return []Check{
		{
			Name:              "npm-build",
			Command:           "npm",
			Args:              []string{"run", "build"},
			Timeout:           NPMBuildTimeout,
			BuildSystem:       technology.BuildSystemNPM,
			RequiresFiles:     []string{"package.json"},
			RequiresDirectory: "node_modules",
		},
	}
}

// ChecksForProfile returns the checks that apply to profile, in toolchain order.
//
// A profile with no recognized build system yields no checks. That is the honest
// answer: running a toolchain a repository does not use would produce failures that
// say nothing about the pull request.
func ChecksForProfile(profile technology.Profile) []Check {
	var checks []Check

	for _, toolchain := range Toolchains() {
		if !toolchain.Detect(profile) {
			continue
		}
		checks = append(checks, toolchain.Checks()...)
	}

	return copyChecks(checks)
}

// ToolchainsForProfile returns the toolchains that apply to profile.
func ToolchainsForProfile(profile technology.Profile) []Toolchain {
	var selected []Toolchain
	for _, toolchain := range Toolchains() {
		if toolchain.Detect(profile) {
			selected = append(selected, toolchain)
		}
	}
	return selected
}

// goToolchain runs the Go tool's own checks.
type goToolchain struct{}

func (goToolchain) Name() string { return "go" }

func (goToolchain) BuildSystem() technology.BuildSystem { return technology.BuildSystemGo }

// Detect accepts either signal: a Go build system, or Go sources in a repository
// whose build system was not identified.
func (goToolchain) Detect(profile technology.Profile) bool {
	return profile.HasBuildSystem(technology.BuildSystemGo) ||
		profile.HasLanguage(technology.LanguageGo)
}

func (goToolchain) Checks() []Check {
	return []Check{
		{
			Name:          "go-test",
			Command:       "go",
			Args:          []string{"test", "./..."},
			Timeout:       GoCheckTimeout,
			BuildSystem:   technology.BuildSystemGo,
			RequiresFiles: []string{"go.mod", "go.work"},
		},
		{
			Name:          "go-vet",
			Command:       "go",
			Args:          []string{"vet", "./..."},
			Timeout:       GoCheckTimeout,
			BuildSystem:   technology.BuildSystemGo,
			RequiresFiles: []string{"go.mod", "go.work"},
		},
	}
}

// sbtToolchain runs sbt's compile task. Tests are deliberately excluded because they
// can make PR review latency unpredictable; the project's CI remains responsible for
// the full test suite.
type sbtToolchain struct{}

func (sbtToolchain) Name() string { return "sbt" }

func (sbtToolchain) BuildSystem() technology.BuildSystem { return technology.BuildSystemSBT }

// Detect requires the build system itself. Scala sources alone are not enough: a
// Scala repository built by Maven or Gradle has no sbt to invoke, and inventing one
// would fail for a reason that has nothing to do with the code.
func (sbtToolchain) Detect(profile technology.Profile) bool {
	return profile.HasBuildSystem(technology.BuildSystemSBT)
}

func (sbtToolchain) Checks() []Check {
	return []Check{
		{
			Name:          "sbt-compile",
			Command:       "sbt",
			Args:          []string{"-batch", "compile"},
			Timeout:       SBTCompileTimeout,
			BuildSystem:   technology.BuildSystemSBT,
			RequiresFiles: []string{"build.sbt", "project/build.properties"},
		},
	}
}

// copyChecks returns a deep copy so a caller cannot mutate a toolchain's definition
// through the slice it was handed.
func copyChecks(checks []Check) []Check {
	out := make([]Check, len(checks))
	copy(out, checks)

	for i := range out {
		if len(checks[i].Args) > 0 {
			out[i].Args = make([]string, len(checks[i].Args))
			copy(out[i].Args, checks[i].Args)
		}
		if len(checks[i].RequiresFiles) > 0 {
			out[i].RequiresFiles = make([]string, len(checks[i].RequiresFiles))
			copy(out[i].RequiresFiles, checks[i].RequiresFiles)
		}
	}

	return out
}
