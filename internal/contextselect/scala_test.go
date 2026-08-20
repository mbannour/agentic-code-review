package contextselect

import (
	"context"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/analysis"
	"github.com/your-company/agentic-code-review/internal/github"
	"github.com/your-company/agentic-code-review/internal/reporules"
	"github.com/your-company/agentic-code-review/internal/review"
	"github.com/your-company/agentic-code-review/internal/technology"
)

// scalaFile builds a changed Scala file with a small patch.
func scalaFile(path string) github.ChangedFile {
	return github.ChangedFile{
		Filename:  path,
		Status:    "modified",
		Additions: 3,
		Patch:     "@@ -1,2 +1,3 @@\n+  val value = maybe.get\n",
	}
}

// scalaReviewContext wraps changed files in a review context.
func scalaReviewContext(files ...github.ChangedFile) review.Context {
	return review.BuildContext(
		github.PullRequest{Owner: "acme", Repo: "payments", Number: 12},
		github.PullRequestDetails{Number: 12, HeadSHA: "abc123", BaseBranch: "main"},
		files, nil, reporules.Rules{},
	)
}

// selectScala runs the selector with a Scala/sbt profile already detected.
func selectScala(t *testing.T, files ...github.ChangedFile) SelectedContext {
	t.Helper()

	sbtProfile := technology.Profile{
		Languages:    []technology.Language{technology.LanguageScala},
		BuildSystems: []technology.BuildSystem{technology.BuildSystemSBT},
	}

	selected, err := NewSelector().SelectWithTechnology(
		context.Background(), scalaReviewContext(files...), analysis.Result{}, sbtProfile)
	if err != nil {
		t.Fatalf("SelectWithTechnology() = %v", err)
	}
	return selected
}

// fileByPath finds a selected file, failing the test if it was dropped.
func fileByPath(t *testing.T, selected SelectedContext, path string) SelectedFile {
	t.Helper()

	for _, f := range selected.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("%s is not in the selection: %+v", path, selected.Files)
	return SelectedFile{}
}

// TestClassifyScalaFiles checks Scala source and test files are told apart. Scala marks
// tests by naming convention rather than by suffix, so a classifier that only knows
// _test.go would file every Scala spec as source and inflate its priority.
func TestClassifyScalaFiles(t *testing.T) {
	tests := []struct {
		path string
		want FileKind
	}{
		{"src/main/scala/net/veact/Export.scala", FileKindSource},
		{"src/main/scala/net/veact/EmailTemplateKind.scala", FileKindSource},
		{"src/test/scala/net/veact/ExportSpec.scala", FileKindTest},
		{"src/test/scala/net/veact/ExportTest.scala", FileKindTest},
		{"src/test/scala/net/veact/ExportSuite.scala", FileKindTest},
		{"src/test/scala/net/veact/Fixtures.scala", FileKindTest},
		{"src/it/scala/net/veact/IntegrationSpec.scala", FileKindTest},
		{"modules/server/src/main/scala/Handler.scala", FileKindSource},
		{"build.sbt", FileKindConfig},
		{"project/plugins.sbt", FileKindConfig},
		{"project/build.properties", FileKindConfig},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := Classify(tt.path, ""); got != tt.want {
				t.Errorf("Classify(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestScalaTestRelatedToChangedSource checks the source/test pairing by naming
// convention, in both the Spec and Test styles, including across the src/main and
// src/test split. The relation is lexical and claims nothing more.
func TestScalaTestRelatedToChangedSource(t *testing.T) {
	tests := []struct {
		name   string
		source string
		test   string
	}{
		{
			name:   "Spec convention across source roots",
			source: "src/main/scala/net/veact/Export.scala",
			test:   "src/test/scala/net/veact/ExportSpec.scala",
		},
		{
			name:   "Test convention across source roots",
			source: "src/main/scala/net/veact/Export.scala",
			test:   "src/test/scala/net/veact/ExportTest.scala",
		},
		{
			name:   "Spec convention in the same directory",
			source: "app/models/Foo.scala",
			test:   "app/models/FooSpec.scala",
		},
		{
			name:   "integration test source root",
			source: "src/main/scala/net/veact/Export.scala",
			test:   "src/it/scala/net/veact/ExportSpec.scala",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selected := selectScala(t, scalaFile(tt.source), scalaFile(tt.test))

			testFile := fileByPath(t, selected, tt.test)
			if testFile.Kind != FileKindTest {
				t.Errorf("%s classified as %q, want test", tt.test, testFile.Kind)
			}
			if testFile.Reason != "test related to changed source file" {
				t.Errorf("%s reason = %q, want the related-source reason", tt.test, testFile.Reason)
			}
			if testFile.Importance != ImportanceHigh {
				t.Errorf("%s importance = %q, want high", tt.test, testFile.Importance)
			}
		})
	}
}

// TestUnrelatedScalaTestIsNotMarkedRelated checks the pairing does not fire on a test
// whose subject is not part of this pull request.
func TestUnrelatedScalaTestIsNotMarkedRelated(t *testing.T) {
	selected := selectScala(t,
		scalaFile("src/main/scala/net/veact/Export.scala"),
		scalaFile("src/test/scala/net/veact/LedgerSpec.scala"),
	)

	testFile := fileByPath(t, selected, "src/test/scala/net/veact/LedgerSpec.scala")
	if testFile.Reason == "test related to changed source file" {
		t.Error("an unrelated spec was marked as related to a changed source file")
	}
}

// TestSBTBuildFilePriority checks a build definition is promoted when Scala is under
// review: it decides dependency versions, compiler options, and what the test task runs.
func TestSBTBuildFilePriority(t *testing.T) {
	selected := selectScala(t,
		scalaFile("src/main/scala/net/veact/Export.scala"),
		github.ChangedFile{Filename: "build.sbt", Status: "modified",
			Patch: "@@ -1,2 +1,3 @@\n+libraryDependencies += \"io.circe\" %% \"circe-core\" % \"0.14.6\"\n"},
		github.ChangedFile{Filename: "README.md", Status: "modified", Patch: "@@ -1 +1,2 @@\n+prose\n"},
	)

	build := fileByPath(t, selected, "build.sbt")
	if build.Importance != ImportanceHigh {
		t.Errorf("build.sbt importance = %q, want high when Scala is under review", build.Importance)
	}
	if build.Reason != "sbt build definition" {
		t.Errorf("build.sbt reason = %q, want the build-definition reason", build.Reason)
	}

	// It ranks above documentation and ordinary config, and below changed source.
	order := map[string]int{}
	for i, f := range selected.Files {
		order[f.Path] = i
	}
	if order["src/main/scala/net/veact/Export.scala"] > order["build.sbt"] {
		t.Error("build.sbt outranks changed Scala source; the code is what is being reviewed")
	}
	if order["build.sbt"] > order["README.md"] {
		t.Error("documentation outranks the sbt build definition")
	}
}

// TestBuildPromotionIsScopedToScala checks the promotion applies to sbt build files and
// nothing else. Ordinary configuration in a Go repository stays ordinary configuration.
func TestBuildPromotionIsScopedToScala(t *testing.T) {
	goProfile := technology.Profile{
		Languages:    []technology.Language{technology.LanguageGo},
		BuildSystems: []technology.BuildSystem{technology.BuildSystemGo},
	}

	rc := scalaReviewContext(
		github.ChangedFile{Filename: "internal/payment/retry.go", Status: "modified",
			Patch: "@@ -1,2 +1,3 @@\n+func retry() {}\n"},
		github.ChangedFile{Filename: ".golangci.yml", Status: "modified",
			Patch: "@@ -1 +1,2 @@\n+linters:\n"},
	)

	selected, err := NewSelector().SelectWithTechnology(context.Background(), rc, analysis.Result{}, goProfile)
	if err != nil {
		t.Fatalf("SelectWithTechnology() = %v", err)
	}

	if selected.Profile.HasBuildSystem(BuildSystemSBT) {
		t.Errorf("BuildSystems = %v, want no sbt in a Go repository", selected.Profile.BuildSystems)
	}
	config := fileByPath(t, selected, ".golangci.yml")
	if config.Reason == "sbt build definition" {
		t.Error("ordinary configuration was treated as an sbt build definition")
	}
	if config.Importance == ImportanceHigh {
		t.Errorf(".golangci.yml importance = %q, want it left at its own level", config.Importance)
	}
}

// TestChangedSBTFilePromotesItselfAnywhere records a deliberate consequence of the
// detection rules: an .sbt file is build definition, so a pull request that changes one
// proves sbt even in a repository that is mostly something else, and the file is
// promoted accordingly. That is the honest reading of the signal, not an accident.
func TestChangedSBTFilePromotesItselfAnywhere(t *testing.T) {
	rc := scalaReviewContext(
		github.ChangedFile{Filename: "internal/payment/retry.go", Status: "modified",
			Patch: "@@ -1,2 +1,3 @@\n+func retry() {}\n"},
		github.ChangedFile{Filename: "tools/build.sbt", Status: "modified",
			Patch: "@@ -1 +1,2 @@\n+// build definition\n"},
	)

	selected, err := NewSelector().SelectWithTechnology(context.Background(), rc, analysis.Result{},
		technology.Profile{Languages: []technology.Language{technology.LanguageGo}})
	if err != nil {
		t.Fatalf("SelectWithTechnology() = %v", err)
	}

	if !selected.Profile.HasBuildSystem(BuildSystemSBT) {
		t.Errorf("BuildSystems = %v, want sbt proved by the changed .sbt file", selected.Profile.BuildSystems)
	}
	if got := fileByPath(t, selected, "tools/build.sbt").Reason; got != "sbt build definition" {
		t.Errorf("tools/build.sbt reason = %q, want the build-definition reason", got)
	}
}

// TestScalaOnlyPullRequestHasALanguage is the gap this step closes: a Scala pull request
// must not fall through to the unknown-language path.
func TestScalaOnlyPullRequestHasALanguage(t *testing.T) {
	selected := selectScala(t,
		scalaFile("src/main/scala/net/veact/Export.scala"),
		scalaFile("src/test/scala/net/veact/ExportSpec.scala"),
	)

	if selected.Profile.Empty() {
		t.Fatal("profile is empty for a Scala pull request")
	}
	if !selected.Profile.HasLanguage(LanguageScala) {
		t.Errorf("Languages = %v, want scala", selected.Profile.Languages)
	}
	if !selected.Profile.HasBuildSystem(BuildSystemSBT) {
		t.Errorf("BuildSystems = %v, want sbt", selected.Profile.BuildSystems)
	}
	if selected.Profile.HasLanguage(LanguageGo) {
		t.Errorf("Languages = %v, want no Go in a Scala repository", selected.Profile.Languages)
	}
}

// TestDocumentationOnlyScalaPullRequestKeepsItsLanguage checks the repository-level
// profile survives a pull request whose diff reveals nothing.
func TestDocumentationOnlyScalaPullRequestKeepsItsLanguage(t *testing.T) {
	selected := selectScala(t,
		github.ChangedFile{Filename: "README.md", Status: "modified", Patch: "@@ -1 +1,2 @@\n+prose\n"},
	)

	if !selected.Profile.HasLanguage(LanguageScala) {
		t.Errorf("Languages = %v, want scala from the repository profile", selected.Profile.Languages)
	}
	if !selected.Profile.HasBuildSystem(BuildSystemSBT) {
		t.Errorf("BuildSystems = %v, want sbt from the repository profile", selected.Profile.BuildSystems)
	}
}

// TestMergedProfileKeepsBothSources checks the diff cannot hide what the repository
// proved, and the repository cannot hide what the diff revealed.
func TestMergedProfileKeepsBothSources(t *testing.T) {
	sbtOnly := technology.Profile{
		Languages:    []technology.Language{technology.LanguageScala},
		BuildSystems: []technology.BuildSystem{technology.BuildSystemSBT},
	}

	rc := scalaReviewContext(
		github.ChangedFile{Filename: "internal/tool/main.go", Status: "added",
			Patch: "@@ -0,0 +1,2 @@\n+package main\n"},
	)

	selected, err := NewSelector().SelectWithTechnology(context.Background(), rc, analysis.Result{}, sbtOnly)
	if err != nil {
		t.Fatalf("SelectWithTechnology() = %v", err)
	}

	if !selected.Profile.HasLanguage(LanguageScala) || !selected.Profile.HasLanguage(LanguageGo) {
		t.Errorf("Languages = %v, want both scala and go", selected.Profile.Languages)
	}
}

// TestScalaLibrariesReachTheSelection checks build.sbt dependencies become labels the
// review can use.
func TestScalaLibrariesReachTheSelection(t *testing.T) {
	selected := selectScala(t, github.ChangedFile{
		Filename: "build.sbt",
		Status:   "modified",
		Patch: "@@ -1,3 +1,5 @@\n" +
			"+  \"org.typelevel\" %% \"cats-core\" % \"2.10.0\",\n" +
			"+  \"com.typesafe.play\" %% \"play\" % \"2.9.0\",\n",
	})

	if !selected.Profile.HasTechnology("cats") {
		t.Errorf("Libraries = %v, want cats", selected.Profile.Libraries)
	}
	if !selected.Profile.HasTechnology("play") {
		t.Errorf("Frameworks = %v, want play", selected.Profile.Frameworks)
	}
	if got := strings.Join(selected.Profile.Technologies(), ","); !strings.Contains(got, "play") {
		t.Errorf("Technologies() = %q, want it to include play", got)
	}
}

// TestSelectWithTechnologyIsDeterministic checks the merged profile and the ordering do
// not vary between runs.
func TestSelectWithTechnologyIsDeterministic(t *testing.T) {
	files := []github.ChangedFile{
		scalaFile("src/main/scala/net/veact/Export.scala"),
		scalaFile("src/test/scala/net/veact/ExportSpec.scala"),
		github.ChangedFile{Filename: "build.sbt", Status: "modified", Patch: "@@ -1 +1,2 @@\n+// x\n"},
	}

	first := selectScala(t, files...)
	for i := 0; i < 10; i++ {
		again := selectScala(t, files...)

		if strings.Join(again.Profile.Languages, ",") != strings.Join(first.Profile.Languages, ",") {
			t.Fatalf("run %d languages = %v, first = %v", i, again.Profile.Languages, first.Profile.Languages)
		}
		for j := range first.Files {
			if again.Files[j].Path != first.Files[j].Path {
				t.Fatalf("run %d file[%d] = %s, first = %s", i, j, again.Files[j].Path, first.Files[j].Path)
			}
		}
	}
}
