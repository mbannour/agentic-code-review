package analysis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/technology"
)

func TestGosecAppliesWhereverGoDoes(t *testing.T) {
	commands := commandsFor(goProfile())

	var found bool
	for _, command := range commands {
		if strings.HasPrefix(command, "gosec") {
			found = true
		}
	}
	if !found {
		t.Errorf("Go profile selected %v, want a gosec check", commands)
	}
	if commandsHave(commandsFor(scalaProfile()), "gosec") {
		t.Error("a Scala repository selected gosec; it only reads Go")
	}
}

func commandsHave(commands []string, prefix string) bool {
	for _, command := range commands {
		if strings.HasPrefix(command, prefix) {
			return true
		}
	}
	return false
}

// Semgrep runs only when an operator names a rule set. A default would mean ARC
// choosing what "security" means, and `--config=auto` would fetch rules from a
// remote registry on every review.
func TestSemgrepRequiresAnOperatorConfiguredRuleSet(t *testing.T) {
	t.Setenv(SemgrepRulesEnv, "")
	if commandsHave(commandsFor(goProfile()), "semgrep") {
		t.Error("semgrep was selected with no configured rule set")
	}

	t.Setenv(SemgrepRulesEnv, "p/security-audit")
	commands := commandsFor(goProfile())
	if !commandsHave(commands, "semgrep") {
		t.Errorf("commands = %v, want a semgrep check once rules are configured", commands)
	}
}

// The rule set becomes a process argument, so a value able to introduce a second
// flag would change what the scanner does rather than which rules it uses.
func TestSemgrepRejectsUnsafeRuleValues(t *testing.T) {
	for _, unsafe := range []string{
		"--config=/etc/passwd",
		"-e",
		"p/audit --autofix",
		"p/audit;rm -rf /",
		"$(whoami)",
		"rules\nmore",
	} {
		t.Run(unsafe, func(t *testing.T) {
			t.Setenv(SemgrepRulesEnv, unsafe)

			if _, ok := SemgrepRules(); ok {
				t.Errorf("SemgrepRules() accepted %q", unsafe)
			}
			if commandsHave(commandsFor(goProfile()), "semgrep") {
				t.Errorf("semgrep was selected with the unsafe value %q", unsafe)
			}
		})
	}
}

func TestScopedPathsFiltersWhatReachesTheArgumentList(t *testing.T) {
	got := scopedPaths([]string{
		"internal/app/app.go",
		"src/main/scala/Query.scala",
		"web/page.tsx",
		"internal/app/app.go", // duplicate
		"docs/guide.md",       // not scanned
		"assets/logo.png",     // not scanned
		"-rf",                 // would read as a flag
		"--config=evil",       // would read as a flag
		"/etc/passwd",         // absolute
		"../../etc/shadow",    // traversal
		"a/../../b/c.go",      // traversal
		"",
	})

	want := []string{"internal/app/app.go", "src/main/scala/Query.scala", "web/page.tsx"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("scopedPaths() = %v, want %v", got, want)
	}
}

func TestScopedPathsIsBounded(t *testing.T) {
	var paths []string
	for i := 0; i < MaxScopedPaths*3; i++ {
		paths = append(paths, "pkg/file"+strings.Repeat("x", i%5)+itoaTest(i)+".go")
	}

	if got := len(scopedPaths(paths)); got != MaxScopedPaths {
		t.Errorf("scopedPaths() kept %d, want %d", got, MaxScopedPaths)
	}
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

// A scoped scanner receives the changed paths after every code-owned flag, behind
// an end-of-flags marker.
func TestScopedCheckAppendsPathsAfterAnEndOfFlagsMarker(t *testing.T) {
	t.Setenv(SemgrepRulesEnv, "p/security-audit")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatalf("write app.go: %v", err)
	}

	runner := newFakeRunner(map[string]CheckResult{})
	analyzer := NewAnalyzerForProfileWithChanges(runner,
		technology.Profile{Languages: []technology.Language{technology.LanguageGo}},
		[]string{"app.go", "docs/readme.md"})

	if _, err := analyzer.Analyze(context.Background(), dir); err != nil {
		t.Fatalf("Analyze() = %v", err)
	}

	var semgrep *Check
	for i, call := range runner.calls {
		if call.Check.Name == "semgrep" {
			semgrep = &runner.calls[i].Check
		}
	}
	if semgrep == nil {
		t.Fatal("semgrep did not run")
	}

	args := semgrep.Args
	marker := -1
	for i, arg := range args {
		if arg == "--" {
			marker = i
		}
	}
	if marker < 0 {
		t.Fatalf("args = %v, want an end-of-flags marker", args)
	}
	if got := strings.Join(args[marker+1:], ","); got != "app.go" {
		t.Errorf("scanned paths = %q, want app.go", got)
	}
	for _, arg := range args[:marker] {
		if arg == "docs/readme.md" || arg == "app.go" {
			t.Errorf("a path appeared before the marker: %v", args)
		}
	}
}

// A pull request that changed nothing a scanner reads is skipped, rather than
// silently scanning the whole repository and reporting untouched code.
func TestScopedCheckSkipsWithoutScannablePaths(t *testing.T) {
	t.Setenv(SemgrepRulesEnv, "p/security-audit")

	runner := newFakeRunner(map[string]CheckResult{})
	analyzer := NewAnalyzerForProfileWithChanges(runner,
		technology.Profile{Languages: []technology.Language{technology.LanguageGo}},
		[]string{"README.md", "docs/guide.md"})

	result, err := analyzer.Analyze(context.Background(), goRepo(t))
	if err != nil {
		t.Fatalf("Analyze() = %v", err)
	}

	for _, check := range result.Checks {
		if check.Name != "semgrep" {
			continue
		}
		if !check.Skipped {
			t.Error("semgrep ran with no scannable changed paths")
		}
		if check.SkipReason != SkipReasonNoScannablePaths {
			t.Errorf("skip reason = %q, want %q", check.SkipReason, SkipReasonNoScannablePaths)
		}
		return
	}
	t.Fatal("no semgrep result recorded")
}

// Nothing in a scanner's argument list may modify the repository.
func TestSecurityScannersNeverWrite(t *testing.T) {
	t.Setenv(SemgrepRulesEnv, "p/security-audit")

	for _, check := range ChecksForProfile(goProfile()) {
		joined := strings.Join(check.Args, " ")
		for _, forbidden := range []string{"--autofix", "--fix", "-w", "--write"} {
			if strings.Contains(joined, forbidden) {
				t.Errorf("check %s passes %q; a review never modifies the repository", check.Name, forbidden)
			}
		}
	}
}
