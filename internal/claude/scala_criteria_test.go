package claude

import (
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/contextselect"
)

func promptForScalaProfile(t *testing.T, libraries ...string) string {
	t.Helper()

	input, err := NewInputBuilder().Build(contextselect.SelectedContext{
		PullRequest: contextselect.PullRequestSummary{Owner: "a", Repository: "b", Number: 1, Title: "t"},
		Profile: contextselect.TechnologyProfile{
			Languages:    []string{"scala"},
			BuildSystems: []string{"sbt"},
			Libraries:    libraries,
		},
		Files: []contextselect.SelectedFile{{
			Path: "src/main/scala/App.scala", Status: "modified",
			Patch: "@@ -1 +1,2 @@\n+  def run = ZIO.succeed(1)\n",
			Kind:  contextselect.FileKindSource, Importance: contextselect.ImportanceHigh,
		}},
	})
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	return input.Content
}

func TestZIOCriteriaAreOffered(t *testing.T) {
	content := promptForScalaProfile(t, "zio")

	for _, want := range []string{
		"zio:",
		"constructed but never run",
		"fiber lifecycle",
		"interruption safety",
		"ZIO.blocking",
		"ZLayer wiring",
		"TestClock",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("prompt missing ZIO criterion %q", want)
		}
	}
}

func TestCatsAndCatsEffectCriteriaAreSeparate(t *testing.T) {
	content := promptForScalaProfile(t, "cats", "cats-effect")

	for _, want := range []string{
		"Validated versus Either",
		"traverse and sequence over an unbounded collection",
		"Semigroup and Monoid",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("prompt missing cats criterion %q", want)
		}
	}
	for _, want := range []string{
		"Resource acquisition and release pairing",
		"unsafeRunSync",
		"IO.blocking",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("prompt missing cats-effect criterion %q", want)
		}
	}
}

// Idioms correct for one Scala version are wrong for the other, so only the
// detected version's guidance may appear.
func TestScalaVersionCriteriaDoNotMix(t *testing.T) {
	three := promptForScalaProfile(t, "scala-3")
	if !strings.Contains(three, "given and using clauses") {
		t.Error("Scala 3 guidance missing for a Scala 3 project")
	}
	if strings.Contains(three, "procedure syntax") {
		t.Error("Scala 2 guidance offered to a Scala 3 project")
	}

	two := promptForScalaProfile(t, "scala-2")
	if !strings.Contains(two, "implicit resolution and ambiguity") {
		t.Error("Scala 2 guidance missing for a Scala 2 project")
	}
	if strings.Contains(two, "opaque types") {
		t.Error("Scala 3 guidance offered to a Scala 2 project")
	}
}

// A cross-built project compiles under both, so guidance for both is correct.
func TestCrossBuiltProjectGetsBothVersions(t *testing.T) {
	content := promptForScalaProfile(t, "scala-2", "scala-3")

	if !strings.Contains(content, "implicit resolution and ambiguity") ||
		!strings.Contains(content, "given and using clauses") {
		t.Error("a cross-built project did not receive both versions' guidance")
	}
}

// The criteria are a lens, not a checklist. Without this framing a Scala review
// becomes a list of purity arguments the author cannot act on.
func TestScalaCriteriaKeepTheBestPracticeWarning(t *testing.T) {
	content := promptForScalaProfile(t, "zio", "cats", "scala-3")

	for _, want := range []string{
		"only where the changed code actually involves them",
		"Do not create a finding merely because a generic best practice exists",
		"Do not report a finding merely because a Scala best practice exists",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("prompt missing the framing %q", want)
		}
	}
}

// An undetected technology contributes nothing: guidance is never offered for a
// library the project does not use.
func TestUndetectedTechnologiesContributeNothing(t *testing.T) {
	content := promptForScalaProfile(t, "zio")

	for _, unwanted := range []string{"unsafeRunSync", "Validated versus Either", "opaque types"} {
		if strings.Contains(content, unwanted) {
			t.Errorf("prompt contains %q for a project that does not use it", unwanted)
		}
	}
}
