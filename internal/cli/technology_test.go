package cli

import (
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/technology"
)

// TestPrintTechnology checks the terminal report of what the repository is built with.
// Stating the toolchain before running it makes a wrong detection obvious rather than
// mysterious.
func TestPrintTechnology(t *testing.T) {
	profile := technology.Profile{
		Languages:    []technology.Language{technology.LanguageScala},
		BuildSystems: []technology.BuildSystem{technology.BuildSystemSBT},
		Frameworks:   []string{"play"},
		Libraries:    []string{"cats"},
	}

	out := captureStdout(t, func() { printTechnology(profile) })

	for _, want := range []string{
		"Technology",
		"Languages:",
		"  Scala",
		"Build systems:",
		"  sbt",
		"Frameworks:",
		"  play",
		"Libraries:",
		"  cats",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q\n---\n%s", want, out)
		}
	}
}

// TestPrintTechnologyMixed checks both stacks are reported for a mixed repository.
func TestPrintTechnologyMixed(t *testing.T) {
	profile := technology.Merge(
		technology.Profile{
			Languages:    []technology.Language{technology.LanguageGo},
			BuildSystems: []technology.BuildSystem{technology.BuildSystemGo},
		},
		technology.Profile{
			Languages:    []technology.Language{technology.LanguageScala},
			BuildSystems: []technology.BuildSystem{technology.BuildSystemSBT},
		},
	)

	out := captureStdout(t, func() { printTechnology(profile) })

	for _, want := range []string{"  Go", "  Scala", "  go", "  sbt"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q\n---\n%s", want, out)
		}
	}
}

// TestPrintTechnologyUndetected checks an unrecognized repository says so.
func TestPrintTechnologyUndetected(t *testing.T) {
	out := captureStdout(t, func() { printTechnology(technology.Profile{}) })

	if !strings.Contains(out, "no language or build system detected") {
		t.Errorf("output does not state that nothing was detected\n---\n%s", out)
	}
}

// TestQualityNoteNamesTheMissingEvidence checks the cost of running without a local
// checkout is stated rather than left to be inferred from an absence.
func TestQualityNoteNamesTheMissingEvidence(t *testing.T) {
	tests := []struct {
		name    string
		profile technology.Profile
		want    []string
	}{
		{
			name: "scala without a checkout",
			profile: technology.Profile{
				Languages:    []technology.Language{technology.LanguageScala},
				BuildSystems: []technology.BuildSystem{technology.BuildSystemSBT},
			},
			want: []string{
				"Review quality note:",
				"Scala-specific reasoning is enabled",
				"sbt -batch compile",
				"evidence is unavailable",
			},
		},
		{
			name: "go without a checkout",
			profile: technology.Profile{
				Languages:    []technology.Language{technology.LanguageGo},
				BuildSystems: []technology.BuildSystem{technology.BuildSystemGo},
			},
			want: []string{"Go-specific reasoning is enabled", "go test ./...", "go vet ./..."},
		},
		{
			name:    "nothing detected",
			profile: technology.Profile{},
			want:    []string{"language-specific build/test checks were not executed."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() { printQualityNote(tt.profile) })

			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("note does not contain %q\n---\n%s", want, out)
				}
			}
		})
	}
}

// TestAnalysisSkippedStatesTheReason checks the skip line itself.
func TestAnalysisSkippedWithoutRepoDir(t *testing.T) {
	out := captureStdout(t, func() { printAnalysisSkipped("local repository not provided") })

	for _, want := range []string{"Deterministic Analysis", "skipped: local repository not provided"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q\n---\n%s", want, out)
		}
	}
}
