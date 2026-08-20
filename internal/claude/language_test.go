package claude

import (
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/contextselect"
)

// contextWithProfile builds the minimum selected context carrying a profile.
func contextWithProfile(profile contextselect.TechnologyProfile) contextselect.SelectedContext {
	return contextselect.SelectedContext{
		PullRequest: contextselect.PullRequestSummary{
			Owner: "acme", Repository: "payments", Number: 123, HeadSHA: "abc123",
		},
		Profile: profile,
		Files: []contextselect.SelectedFile{{
			Path:       "src/main/scala/net/veact/Export.scala",
			Status:     "modified",
			Patch:      "@@ -1,2 +1,3 @@\n+  val x = opt.get\n",
			Kind:       contextselect.FileKindSource,
			Importance: contextselect.ImportanceHigh,
			Reason:     "changed source file",
		}},
	}
}

// goOnlyProfile, scalaOnlyProfile, and bothProfile are the three profiles guidance has
// to distinguish.
func goOnlyProfile() contextselect.TechnologyProfile {
	return contextselect.TechnologyProfile{
		Languages:    []string{contextselect.LanguageGo},
		BuildSystems: []string{contextselect.BuildSystemGo},
	}
}

func scalaOnlyProfile() contextselect.TechnologyProfile {
	return contextselect.TechnologyProfile{
		Languages:    []string{contextselect.LanguageScala},
		BuildSystems: []string{contextselect.BuildSystemSBT},
		Frameworks:   []string{"play"},
		Libraries:    []string{"cats", "circe"},
	}
}

func bothProfile() contextselect.TechnologyProfile {
	return contextselect.TechnologyProfile{
		Languages:    []string{contextselect.LanguageGo, contextselect.LanguageScala},
		BuildSystems: []string{contextselect.BuildSystemGo, contextselect.BuildSystemSBT},
	}
}

// buildFor renders the review input for a profile.
func buildFor(t *testing.T, profile contextselect.TechnologyProfile) string {
	t.Helper()

	input, err := NewInputBuilder().Build(contextWithProfile(profile))
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	return input.Content
}

// TestLanguageGuidanceIsScopedToWhatWasDetected is the core of this step: each language's
// guidance appears when that language is present, and not otherwise. A Scala reviewer
// reading Go advice, or the reverse, is noise dressed as expertise.
func TestLanguageGuidanceIsScopedToWhatWasDetected(t *testing.T) {
	goMarkers := []string{
		"Go semantics:",
		"error propagation",
		"errors.Is / errors.As",
		"%w wrapping",
		"context.Context propagation",
		"goroutine lifecycle",
		"resource cleanup",
		"HTTP response body handling",
		"database transaction lifecycle",
		"exported API compatibility",
		"test coverage",
	}
	scalaMarkers := []string{
		"Scala semantics:",
		"Option, Either, and Try semantics",
		"pattern matches that are not exhaustive",
		"Future and ExecutionContext usage",
		"blocking calls",
		"collection allocation",
		"immutability",
		"null crossing the boundary",
		"exception handling",
		"resource lifecycle",
		"implicit and given resolution",
		"type-safety regressions",
		"case-class evolution",
		"serialization",
		"test coverage",
	}

	tests := []struct {
		name    string
		profile contextselect.TechnologyProfile
		want    []string
		absent  []string
	}{
		{
			name:    "Go repository",
			profile: goOnlyProfile(),
			want:    goMarkers,
			absent:  []string{"Scala semantics:", "sbt build:", "Option, Either, and Try"},
		},
		{
			name:    "Scala repository",
			profile: scalaOnlyProfile(),
			want:    scalaMarkers,
			absent:  []string{"Go semantics:", "goroutine lifecycle", "errors.Is / errors.As"},
		},
		{
			name:    "mixed repository",
			profile: bothProfile(),
			want:    append(append([]string{}, goMarkers...), scalaMarkers...),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := buildFor(t, tt.profile)

			for _, want := range tt.want {
				if !strings.Contains(content, want) {
					t.Errorf("input missing %q", want)
				}
			}
			for _, unwanted := range tt.absent {
				if strings.Contains(content, unwanted) {
					t.Errorf("input contains %q, which this profile does not call for", unwanted)
				}
			}
		})
	}
}

// TestScalaGuidanceRefusesToBeAChecklist checks the warning travels with the guidance.
// Almost any Scala file can be argued into a purer form, and a review full of such
// arguments is worth nothing to the author.
func TestScalaGuidanceRefusesToBeAChecklist(t *testing.T) {
	content := buildFor(t, scalaOnlyProfile())

	for _, want := range []string{
		"Do not report a finding merely because a Scala best practice exists.",
		"There must be a concrete defect, regression risk, or material",
		"maintainability issue introduced or exposed by this pull request.",
		"Weigh these criteria only where the changed code actually involves them.",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("input missing the discipline statement %q", want)
		}
	}
}

// TestSBTBuildContextAppears checks the build-level guidance is present for sbt, and is
// scoped to pull requests that actually change the build.
func TestSBTBuildContextAppears(t *testing.T) {
	content := buildFor(t, scalaOnlyProfile())

	for _, want := range []string{
		"sbt build:",
		"build.sbt changes",
		"plugin changes in project/plugins.sbt",
		"dependency updates",
		"test configuration",
		"cross-version settings",
		"compiler options",
		"Weigh these only when this pull request changes the build.",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("input missing the sbt build context %q", want)
		}
	}

	if goContent := buildFor(t, goOnlyProfile()); strings.Contains(goContent, "sbt build:") {
		t.Error("a Go repository was given sbt build guidance")
	}
}

// TestProfileSectionReportsEveryDimension checks the profile itself reaches the review:
// languages, build systems, frameworks, and libraries.
func TestProfileSectionReportsEveryDimension(t *testing.T) {
	content := buildFor(t, scalaOnlyProfile())

	for _, want := range []string{
		"TECHNOLOGY PROFILE",
		"languages: scala",
		"build systems: sbt",
		"frameworks: play",
		"libraries: cats, circe",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("input missing the profile line %q", want)
		}
	}
}

// TestScalaFailuresAppearAsEvidence checks a failing sbt run reaches the review as
// evidence, with the reminder that not every failure belongs to this pull request.
func TestScalaFailuresAppearAsEvidence(t *testing.T) {
	selected := contextWithProfile(scalaOnlyProfile())
	selected.Analysis = []contextselect.SelectedAnalysis{
		{
			Name:     "sbt-compile",
			Command:  "sbt -batch compile",
			Passed:   false,
			ExitCode: 1,
			Output:   "[error] Export.scala:42:7: type mismatch;\n[error]  found: Option[String]\n",
		},
	}

	input, err := NewInputBuilder().Build(selected)
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}

	for _, want := range []string{
		"DETERMINISTIC ANALYSIS",
		"check: sbt-compile",
		"command: sbt -batch compile",
		"status: failed",
		"type mismatch",
		"Do not convert every failing check into a finding.",
	} {
		if !strings.Contains(input.Content, want) {
			t.Errorf("input missing %q", want)
		}
	}
}

// TestGuidanceNeverInstructsToRunCommands checks the review input never tells the model
// to run a build or a test. Which commands run is decided in Go, and the model receives
// only their results.
func TestGuidanceNeverInstructsToRunCommands(t *testing.T) {
	for name, profile := range map[string]contextselect.TechnologyProfile{
		"go":    goOnlyProfile(),
		"scala": scalaOnlyProfile(),
		"mixed": bothProfile(),
	} {
		content := buildFor(t, profile)

		for _, forbidden := range []string{
			"run sbt", "run go test", "execute sbt", "sh -c",
			"you may run", "run the tests yourself",
		} {
			if strings.Contains(strings.ToLower(content), strings.ToLower(forbidden)) {
				t.Errorf("%s input contains %q; command selection is not the model's decision",
					name, forbidden)
			}
		}
		if !strings.Contains(content, "Do not run commands that change state.") {
			t.Errorf("%s input lost the review-only instruction", name)
		}
	}
}

// TestUndetectedProfileFallsBackCleanly checks a repository whose stack was not
// recognized gets no language guidance at all rather than a default one.
func TestUndetectedProfileFallsBackCleanly(t *testing.T) {
	content := buildFor(t, contextselect.TechnologyProfile{})

	if !strings.Contains(content, "no technology detected; review the changed code on its own terms") {
		t.Error("input does not state that nothing was detected")
	}
	for _, unwanted := range []string{"Go semantics:", "Scala semantics:", "sbt build:"} {
		if strings.Contains(content, unwanted) {
			t.Errorf("input contains %q for an unrecognized repository", unwanted)
		}
	}
}
