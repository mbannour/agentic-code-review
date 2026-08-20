package analysis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/technology"
)

func npmNextProfile() technology.Profile {
	return technology.Profile{
		Languages:    []technology.Language{technology.LanguageJavaScript, technology.LanguageTypeScript},
		BuildSystems: []technology.BuildSystem{technology.BuildSystemNPM},
		Frameworks:   []string{"next.js"},
		Libraries:    []string{"react"},
	}
}

func TestNPMNextProfileSelectsBuildWithoutTests(t *testing.T) {
	checks := ChecksForProfile(npmNextProfile())
	if len(checks) != 1 {
		t.Fatalf("checks = %+v, want one", checks)
	}
	check := checks[0]
	if check.Name != "npm-build" || check.DisplayCommand() != "npm run build" {
		t.Errorf("check = %+v, want npm run build", check)
	}
	if check.EffectiveTimeout() != NPMBuildTimeout {
		t.Errorf("timeout = %s, want %s", check.EffectiveTimeout(), NPMBuildTimeout)
	}
	if strings.Contains(check.DisplayCommand(), "test") {
		t.Errorf("npm check unexpectedly runs tests: %s", check.DisplayCommand())
	}
}

func TestNPMWithoutNextDoesNotAssumeABuildScript(t *testing.T) {
	profile := technology.Profile{
		Languages:    []technology.Language{technology.LanguageJavaScript},
		BuildSystems: []technology.BuildSystem{technology.BuildSystemNPM},
	}
	if checks := ChecksForProfile(profile); len(checks) != 0 {
		t.Errorf("checks = %+v, want none without a detected Next.js build", checks)
	}
}

func TestNPMBuildSkipsWhenDependenciesAreNotInstalled(t *testing.T) {
	dir := repoWithFiles(t, "package.json")
	runner := &missingToolRunner{}

	result, err := NewAnalyzerForProfile(runner, npmNextProfile()).Analyze(context.Background(), dir)
	if err != nil {
		t.Fatalf("Analyze() = %v", err)
	}
	if len(result.Checks) != 1 || !result.Checks[0].Skipped {
		t.Fatalf("checks = %+v, want one skipped check", result.Checks)
	}
	if result.Checks[0].SkipReason != "node_modules directory not found" {
		t.Errorf("reason = %q", result.Checks[0].SkipReason)
	}
	if len(runner.ran) != 0 {
		t.Errorf("runner executed %v without installed dependencies", runner.ran)
	}
}

func TestNPMBuildUsesTheToolBoundaryWhenDependenciesExist(t *testing.T) {
	dir := repoWithFiles(t, "package.json")
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatalf("MkdirAll(node_modules) = %v", err)
	}
	runner := &failingToolchainRunner{failing: "npm-build"}

	result, err := NewAnalyzerForProfile(runner, npmNextProfile()).Analyze(context.Background(), dir)
	if err != nil {
		t.Fatalf("Analyze() = %v", err)
	}
	if len(runner.ran) != 1 || runner.ran[0] != "npm-build" {
		t.Errorf("ran = %v, want npm-build", runner.ran)
	}
	if len(result.FailedChecks()) != 1 || result.FailedChecks()[0].Name != "npm-build" {
		t.Errorf("failed checks = %+v", result.FailedChecks())
	}
}
