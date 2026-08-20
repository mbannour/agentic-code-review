package analysis

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/your-company/agentic-code-review/internal/technology"
)

// goProfile, scalaProfile, and mixedProfile are the three shapes check selection has to
// handle.
func goProfile() technology.Profile {
	return technology.Profile{
		Languages:    []technology.Language{technology.LanguageGo},
		BuildSystems: []technology.BuildSystem{technology.BuildSystemGo},
	}
}

func scalaProfile() technology.Profile {
	return technology.Profile{
		Languages:    []technology.Language{technology.LanguageScala},
		BuildSystems: []technology.BuildSystem{technology.BuildSystemSBT},
	}
}

func mixedProfile() technology.Profile {
	return technology.Merge(goProfile(), scalaProfile())
}

// commandsFor renders the selected checks as displayable commands.
func commandsFor(profile technology.Profile) []string {
	checks := ChecksForProfile(profile)
	commands := make([]string, 0, len(checks))
	for _, check := range checks {
		commands = append(commands, check.DisplayCommand())
	}
	return commands
}

// TestChecksForProfile covers which toolchain each profile selects.
func TestChecksForProfile(t *testing.T) {
	tests := []struct {
		name    string
		profile technology.Profile
		want    []string
	}{
		{
			name:    "Go profile selects the Go checks",
			profile: goProfile(),
			want:    []string{"go test ./...", "go vet ./..."},
		},
		{
			name:    "Scala profile selects sbt compile",
			profile: scalaProfile(),
			want:    []string{"sbt -batch compile"},
		},
		{
			name:    "a mixed profile selects both toolchains",
			profile: mixedProfile(),
			want:    []string{"go test ./...", "go vet ./...", "sbt -batch compile"},
		},
		{
			// Go sources without a manifest still get the Go tool, which is
			// universal for Go code.
			name:    "Go sources alone still select the Go checks",
			profile: technology.Profile{Languages: []technology.Language{technology.LanguageGo}},
			want:    []string{"go test ./...", "go vet ./..."},
		},
		{
			// Scala without sbt selects nothing: the repository may be built by
			// Maven or Gradle, and inventing sbt would fail for reasons that have
			// nothing to do with the code.
			name:    "Scala without a build system selects nothing",
			profile: technology.Profile{Languages: []technology.Language{technology.LanguageScala}},
			want:    nil,
		},
		{
			name:    "an empty profile selects nothing",
			profile: technology.Profile{},
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := commandsFor(tt.profile)

			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("checks = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCheckTimeoutsDifferByToolchain checks each check carries its own bound, and that
// the sbt bounds are the larger ones. One timeout for the whole stage would either kill
// sbt or let a hung Go test hold the review open.
func TestCheckTimeoutsDifferByToolchain(t *testing.T) {
	timeouts := map[string]time.Duration{}
	for _, check := range ChecksForProfile(mixedProfile()) {
		timeouts[check.Name] = check.EffectiveTimeout()
	}

	tests := []struct {
		name string
		want time.Duration
	}{
		{"go-test", GoCheckTimeout},
		{"go-vet", GoCheckTimeout},
		{"sbt-compile", SBTCompileTimeout},
	}

	for _, tt := range tests {
		if got := timeouts[tt.name]; got != tt.want {
			t.Errorf("%s timeout = %s, want %s", tt.name, got, tt.want)
		}
	}

	if !(timeouts["sbt-compile"] > timeouts["go-test"]) {
		t.Error("sbt compile is not given more time than go test")
	}
}

// TestChecksNeverUseAShell is the property that matters most about these definitions:
// every command is an executable with an argument list, so nothing in a command can be
// interpreted as shell syntax.
func TestChecksNeverUseAShell(t *testing.T) {
	for _, toolchain := range Toolchains() {
		for _, check := range toolchain.Checks() {
			switch check.Command {
			case "sh", "bash", "zsh", "cmd", "cmd.exe", "powershell", "env":
				t.Errorf("%s runs %q, which is a shell", check.Name, check.Command)
			}
			for _, arg := range check.Args {
				if strings.ContainsAny(arg, "|&;><$`") {
					t.Errorf("%s argument %q contains shell metacharacters", check.Name, arg)
				}
			}
			if strings.Contains(check.Command, " ") {
				t.Errorf("%s command %q is a command line rather than an executable", check.Name, check.Command)
			}
		}
	}
}

// TestChecksForProfileIsACopy checks a caller cannot mutate a toolchain's definition
// through the slice it was handed.
func TestChecksForProfileIsACopy(t *testing.T) {
	first := ChecksForProfile(goProfile())
	first[0].Command = "rm"
	first[0].Args[0] = "-rf"

	second := ChecksForProfile(goProfile())
	if second[0].Command != "go" || second[0].Args[0] != "test" {
		t.Errorf("check definition was mutated through a returned slice: %+v", second[0])
	}
}

// TestToolchainsForProfile checks the toolchain listing used for reporting.
func TestToolchainsForProfile(t *testing.T) {
	tests := []struct {
		name    string
		profile technology.Profile
		want    []string
	}{
		{name: "go", profile: goProfile(), want: []string{"go"}},
		{name: "scala", profile: scalaProfile(), want: []string{"sbt"}},
		{name: "mixed", profile: mixedProfile(), want: []string{"go", "sbt"}},
		{name: "none", profile: technology.Profile{}, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, toolchain := range ToolchainsForProfile(tt.profile) {
				got = append(got, toolchain.Name())
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("toolchains = %v, want %v", got, tt.want)
			}
		})
	}
}

// missingToolRunner is a runner that finds no executables, standing in for a machine
// without the toolchain installed.
type missingToolRunner struct {
	ran []string
}

func (r *missingToolRunner) Run(_ context.Context, _ string, check Check) CheckResult {
	r.ran = append(r.ran, check.Name)
	return CheckResult{Name: check.Name, Command: check.DisplayCommand(), Passed: true}
}

func (r *missingToolRunner) LookupTool(name string) error {
	return errors.New("exec: \"" + name + "\": executable file not found in $PATH")
}

// TestMissingToolIsSkippedNotFatal checks that a missing toolchain degrades the review
// instead of ending it. A developer without sbt should still get a review.
func TestMissingToolIsSkippedNotFatal(t *testing.T) {
	tests := []struct {
		name       string
		profile    technology.Profile
		files      []string
		wantSkips  []string
		wantReason string
	}{
		{
			name:       "sbt is not installed",
			profile:    scalaProfile(),
			files:      []string{"build.sbt"},
			wantSkips:  []string{"sbt-compile"},
			wantReason: "sbt executable not found",
		},
		{
			name:       "go is not installed",
			profile:    goProfile(),
			files:      []string{"go.mod"},
			wantSkips:  []string{"go-test", "go-vet"},
			wantReason: "go executable not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := repoWithFiles(t, tt.files...)
			runner := &missingToolRunner{}

			result, err := NewAnalyzerForProfile(runner, tt.profile).Analyze(context.Background(), dir)
			if err != nil {
				t.Fatalf("Analyze() = %v; a missing tool must not end the review", err)
			}

			if len(runner.ran) != 0 {
				t.Errorf("ran %v, want nothing executed when the tool is absent", runner.ran)
			}
			if len(result.SkippedChecks()) != len(tt.wantSkips) {
				t.Fatalf("skipped %d checks, want %d: %+v",
					len(result.SkippedChecks()), len(tt.wantSkips), result.Checks)
			}
			for i, want := range tt.wantSkips {
				got := result.SkippedChecks()[i]
				if got.Name != want {
					t.Errorf("skipped[%d] = %q, want %q", i, got.Name, want)
				}
				if got.SkipReason != tt.wantReason {
					t.Errorf("%s reason = %q, want %q", got.Name, got.SkipReason, tt.wantReason)
				}
				if got.Failed() {
					t.Errorf("%s counts as failed; a skipped check has not failed", got.Name)
				}
			}
			if result.HasFailures() {
				t.Error("HasFailures() = true when every check was skipped")
			}
		})
	}
}

// TestMissingBuildFileIsSkipped checks a check whose build file is absent is skipped
// with that reason, before the tool is even looked for.
func TestMissingBuildFileIsSkipped(t *testing.T) {
	tests := []struct {
		name       string
		profile    technology.Profile
		wantReason string
	}{
		{name: "no go.mod", profile: goProfile(), wantReason: SkipReasonNoGoModule},
		{name: "no build.sbt", profile: scalaProfile(), wantReason: "build.sbt or project/build.properties not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := repoWithFiles(t, "README.md")

			result, err := NewAnalyzerForProfile(&missingToolRunner{}, tt.profile).Analyze(context.Background(), dir)
			if err != nil {
				t.Fatalf("Analyze() = %v", err)
			}

			for _, check := range result.Checks {
				if !check.Skipped {
					t.Errorf("%s ran without its build file", check.Name)
				}
				if check.SkipReason != tt.wantReason {
					t.Errorf("%s reason = %q, want %q", check.Name, check.SkipReason, tt.wantReason)
				}
			}
		})
	}
}

// failingToolchainRunner reports a failure for the named check and success otherwise.
type failingToolchainRunner struct {
	failing string
	ran     []string
}

func (r *failingToolchainRunner) Run(_ context.Context, _ string, check Check) CheckResult {
	r.ran = append(r.ran, check.Name)

	result := CheckResult{Name: check.Name, Command: check.DisplayCommand()}
	if check.Name == r.failing {
		result.ExitCode = 1
		result.Stdout = "[error] /src/main/scala/Export.scala:42:7: type mismatch"
		return result
	}
	result.Passed = true
	return result
}

// TestFailedSbtCheckIsEvidenceNotACrash checks a compile failure is recorded as
// evidence rather than ending the review as an application error.
func TestFailedSbtCheckIsEvidenceNotACrash(t *testing.T) {
	dir := repoWithFiles(t, "build.sbt")
	runner := &failingToolchainRunner{failing: "sbt-compile"}

	result, err := NewAnalyzerWithChecks(runner, ChecksForProfile(scalaProfile())).
		Analyze(context.Background(), dir)
	if err != nil {
		t.Fatalf("Analyze() = %v; a failing check is evidence, not an error", err)
	}

	if len(runner.ran) != 1 || runner.ran[0] != "sbt-compile" {
		t.Errorf("ran %v, want only sbt-compile", runner.ran)
	}
	failed := result.FailedChecks()
	if len(failed) != 1 || failed[0].Name != "sbt-compile" {
		t.Fatalf("FailedChecks() = %+v, want just sbt-compile", failed)
	}
	if !strings.Contains(failed[0].Stdout, "type mismatch") {
		t.Error("the tool's own output was not kept as evidence")
	}
	if result.Passed() {
		t.Error("Passed() = true with a failing check")
	}
}

// timingOutRunner reports every check as having exceeded its own timeout.
type timingOutRunner struct{}

func (timingOutRunner) Run(_ context.Context, _ string, check Check) CheckResult {
	return CheckResult{
		Name:     check.Name,
		Command:  check.DisplayCommand(),
		ExitCode: -1,
		TimedOut: true,
		Duration: check.EffectiveTimeout(),
	}
}

// TestTimeoutIsACheckOutcome checks a timeout is reported as a failed check carrying its
// own bound, rather than as an application error.
func TestTimeoutIsACheckOutcome(t *testing.T) {
	dir := repoWithFiles(t, "build.sbt")

	result, err := NewAnalyzerWithChecks(timingOutRunner{}, ChecksForProfile(scalaProfile())).
		Analyze(context.Background(), dir)
	if err != nil {
		t.Fatalf("Analyze() = %v; a timeout is a check outcome", err)
	}

	for _, check := range result.Checks {
		if !check.TimedOut {
			t.Errorf("%s did not record the timeout", check.Name)
		}
		if !check.Failed() {
			t.Errorf("%s timed out but does not count as failed", check.Name)
		}
	}
	if got := result.Checks[0].Duration; got != SBTCompileTimeout {
		t.Errorf("sbt compile ran for %s, want its own %s bound", got, SBTCompileTimeout)
	}
}

// TestCommandRunnerLooksUpRealTools checks the locator the analyzer relies on. Whether a
// process exists is a question for whatever runs processes.
func TestCommandRunnerLooksUpRealTools(t *testing.T) {
	runner := NewCommandRunner()

	if err := runner.LookupTool("go"); err != nil {
		t.Errorf("LookupTool(\"go\") = %v; the Go tool builds this project", err)
	}
	if err := runner.LookupTool("definitely-not-a-real-tool-arc-test"); err == nil {
		t.Error("LookupTool() found a tool that does not exist")
	}
}

// repoWithFiles creates a temporary checkout containing the named empty files.
func repoWithFiles(t *testing.T, names ...string) string {
	t.Helper()

	dir := t.TempDir()
	for _, name := range names {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) = %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte("\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) = %v", full, err)
		}
	}
	return dir
}
