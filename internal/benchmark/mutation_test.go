package benchmark

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/your-company/agentic-code-review/internal/findings"
)

// repoRoot walks up to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(dir + "/go.mod"); err == nil {
			return dir
		}
		dir = dir[:strings.LastIndex(dir, "/")]
	}
	t.Fatal("module root not found")
	return ""
}

// Every mutant must apply cleanly and produce a diff that names the real file.
// A mutant whose anchor text has drifted would silently measure nothing.
func TestEveryMutantApplies(t *testing.T) {
	root := repoRoot(t)

	for _, mutant := range Mutants() {
		t.Run(mutant.ID, func(t *testing.T) {
			change, err := mutant.Apply(root)
			if err != nil {
				t.Fatalf("Apply() = %v", err)
			}
			if !strings.Contains(change.Patch, "+++ b/"+mutant.Path) {
				t.Errorf("patch does not name %s:\n%s", mutant.Path, change.Patch)
			}
			if change.Additions == 0 && change.Deletions == 0 {
				t.Error("patch changes no lines")
			}
			if !strings.Contains(change.Patch, "@@") {
				t.Error("patch has no hunk header")
			}
		})
	}
}

// Applying a mutant must never modify the repository.
func TestApplyDoesNotTouchTheWorkingTree(t *testing.T) {
	root := repoRoot(t)
	mutant := Mutants()[0]

	before, err := os.ReadFile(root + "/" + mutant.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mutant.Apply(root); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(root + "/" + mutant.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("the mutant modified the real file")
	}
}

// A defect's detection terms must be about the mechanism, not the wording, but they
// must also be specific enough that unrelated prose cannot match.
func TestDetectionTermsAreSpecific(t *testing.T) {
	for _, mutant := range Mutants() {
		if mutant.Clean {
			if len(mutant.WantTerms) != 0 {
				t.Errorf("%s is a clean control but declares detection terms", mutant.ID)
			}
			continue
		}
		if len(mutant.WantTerms) < 3 {
			t.Errorf("%s has %d terms, want several phrasings of the mechanism",
				mutant.ID, len(mutant.WantTerms))
		}
		for _, term := range mutant.WantTerms {
			if len(term) < 4 {
				t.Errorf("%s term %q is too short to be specific", mutant.ID, term)
			}
		}
	}
}

// A clean control must never be counted as a detection, whatever is reported.
func TestCleanControlsCannotBeDetected(t *testing.T) {
	for _, mutant := range Mutants() {
		if !mutant.Clean {
			continue
		}
		finding := findings.Finding{
			File: mutant.Path, Title: "anything at all", Problem: "any problem",
		}
		if mutant.Identifies(finding) {
			t.Errorf("%s counted a finding as a detection", mutant.ID)
		}
	}
}

// TestMutationBenchmark runs the real reviewer against every seeded defect.
//
// It is gated behind ARC_BENCHMARK because it invokes Claude once per mutant plus
// once per candidate finding, which costs real usage and minutes. The gate is the
// difference between a suite anyone can run and a measurement someone chooses to
// pay for.
func TestMutationBenchmark(t *testing.T) {
	if os.Getenv("ARC_BENCHMARK") == "" {
		t.Skip("set ARC_BENCHMARK=1 to run the mutation benchmark (invokes Claude)")
	}

	root := repoRoot(t)
	runner := Runner{
		RepoRoot: root,
		Retrieve: os.Getenv("ARC_BENCHMARK_RETRIEVE") != "",
		Verify:   os.Getenv("ARC_BENCHMARK_NO_VERIFY") == "",
	}

	mutants := Mutants()
	if only := os.Getenv("ARC_BENCHMARK_ONLY"); only != "" {
		var filtered []Mutant
		for _, mutant := range mutants {
			if strings.Contains(mutant.ID, only) {
				filtered = append(filtered, mutant)
			}
		}
		mutants = filtered
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	start := time.Now()
	report := runner.RunAll(ctx, mutants, func(outcome Outcome) {
		status := "MISS"
		switch {
		case outcome.Error != "":
			status = "ERROR: " + outcome.Error
		case outcome.Mutant.Clean && len(outcome.Published) == 0:
			status = "QUIET"
		case outcome.Mutant.Clean:
			status = "NOISE"
		case outcome.Detected:
			status = "FOUND"
		case outcome.DetectedPreVerification:
			status = "SUPPRESSED"
		}
		t.Logf("%-12s %-28s findings=%d published=%d",
			status, outcome.Mutant.ID, len(outcome.Findings), len(outcome.Published))
	})

	t.Logf("\n%s\nelapsed: %s", report.Render(), time.Since(start).Round(time.Second))
}
