package technology

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeReader answers with a fixed set of repository files and records what was asked
// for, which is how the allow-list is verified.
type fakeReader struct {
	files map[string]string
	asked []string
	err   error
}

func (f *fakeReader) GetRepositoryFile(_ context.Context, _ string, _ string, path string, _ string) (string, error) {
	f.asked = append(f.asked, path)
	if f.err != nil {
		return "", f.err
	}
	content, ok := f.files[path]
	if !ok {
		return "", fmt.Errorf("read %s: %w", path, ErrNotFound)
	}
	return content, nil
}

// buildSBT is a small but realistic build definition.
const buildSBT = `
ThisBuild / scalaVersion := "2.13.12"

libraryDependencies ++= Seq(
  "org.typelevel" %% "cats-core" % "2.10.0",
  "com.typesafe.play" %% "play" % "2.9.0",
  "io.circe" %% "circe-generic" % "0.14.6",
  "org.scalatest" %% "scalatest" % "3.2.17" % Test
)
`

// goMod is a small Go module file.
const goMod = `module github.com/acme/payments

go 1.22

require (
	google.golang.org/grpc v1.62.0
	gorm.io/gorm v1.25.0
)
`

// TestDetectFromSignals covers the whole detection matrix: what a manifest proves, what
// an extension proves, and what neither proves.
func TestDetectFromSignals(t *testing.T) {
	tests := []struct {
		name             string
		manifests        map[string]string
		paths            []string
		wantLanguages    []Language
		wantBuildSystems []BuildSystem
	}{
		{
			name:             "go.mod detects Go and the Go toolchain",
			manifests:        map[string]string{"go.mod": goMod},
			wantLanguages:    []Language{LanguageGo},
			wantBuildSystems: []BuildSystem{BuildSystemGo},
		},
		{
			name:          "a changed .go file detects Go",
			paths:         []string{"internal/payment/retry.go"},
			wantLanguages: []Language{LanguageGo},
		},
		{
			name:             "go.sum detects Go and the Go toolchain",
			paths:            []string{"go.sum"},
			wantLanguages:    []Language{LanguageGo},
			wantBuildSystems: []BuildSystem{BuildSystemGo},
		},
		{
			name:             "build.sbt detects Scala and sbt",
			manifests:        map[string]string{"build.sbt": buildSBT},
			wantLanguages:    []Language{LanguageScala},
			wantBuildSystems: []BuildSystem{BuildSystemSBT},
		},
		{
			name:             "project/build.properties detects sbt",
			manifests:        map[string]string{"project/build.properties": "sbt.version=1.9.9\n"},
			wantBuildSystems: []BuildSystem{BuildSystemSBT},
		},
		{
			name:             "project/plugins.sbt detects sbt",
			manifests:        map[string]string{"project/plugins.sbt": `addSbtPlugin("org.scalameta" % "sbt-scalafmt" % "2.5.2")`},
			wantLanguages:    []Language{LanguageScala},
			wantBuildSystems: []BuildSystem{BuildSystemSBT},
		},
		{
			name:          "a changed .scala file detects Scala without a build system",
			paths:         []string{"src/main/scala/net/veact/Export.scala"},
			wantLanguages: []Language{LanguageScala},
		},
		{
			name:             "a changed .sbt file detects Scala and sbt",
			paths:            []string{"modules/server/build.sbt"},
			wantLanguages:    []Language{LanguageScala},
			wantBuildSystems: []BuildSystem{BuildSystemSBT},
		},
		{
			name: "a mixed repository detects both",
			manifests: map[string]string{
				"go.mod":    goMod,
				"build.sbt": buildSBT,
			},
			wantLanguages:    []Language{LanguageGo, LanguageScala},
			wantBuildSystems: []BuildSystem{BuildSystemGo, BuildSystemSBT},
		},
		{
			// The gap this step exists to close: a build.sbt is proof of Scala even
			// when the pull request touches nothing but prose.
			name:             "a documentation-only PR in a Scala repository still detects Scala",
			manifests:        map[string]string{"build.sbt": buildSBT},
			paths:            []string{"README.md", "docs/export.md"},
			wantLanguages:    []Language{LanguageScala},
			wantBuildSystems: []BuildSystem{BuildSystemSBT},
		},
		{
			name:  "no known language",
			paths: []string{"README.md", "Makefile", "src/main.rb"},
		},
		{
			name:      "nothing at all",
			manifests: nil,
			paths:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := DetectFromSignals(tt.manifests, tt.paths)

			if !equalLanguages(profile.Languages, tt.wantLanguages) {
				t.Errorf("Languages = %v, want %v", profile.Languages, tt.wantLanguages)
			}
			if !equalBuildSystems(profile.BuildSystems, tt.wantBuildSystems) {
				t.Errorf("BuildSystems = %v, want %v", profile.BuildSystems, tt.wantBuildSystems)
			}

			wantEmpty := len(tt.wantLanguages) == 0 && len(tt.wantBuildSystems) == 0 &&
				len(profile.Frameworks) == 0 && len(profile.Libraries) == 0
			if profile.Empty() != wantEmpty {
				t.Errorf("Empty() = %t, want %t for %+v", profile.Empty(), wantEmpty, profile)
			}
		})
	}
}

// TestDetectReadsOnlyTheAllowList checks detection cannot reach outside its manifest
// list. Anything else in the repository — an .env file, a certificate — is unreachable
// from here by construction.
func TestDetectReadsOnlyTheAllowList(t *testing.T) {
	reader := &fakeReader{files: map[string]string{"build.sbt": buildSBT}}

	if _, err := NewDetector(reader).Detect(context.Background(), "acme", "payments", "sha", nil); err != nil {
		t.Fatalf("Detect() = %v", err)
	}

	if len(reader.asked) != len(ManifestPaths) {
		t.Fatalf("read %d files, want exactly the %d allow-listed manifests: %v",
			len(reader.asked), len(ManifestPaths), reader.asked)
	}
	for i, want := range ManifestPaths {
		if reader.asked[i] != want {
			t.Errorf("read[%d] = %q, want %q", i, reader.asked[i], want)
		}
	}
}

// TestDetectPropagatesRealFailures checks a missing manifest is normal but a broken
// read is not. Reviewing a Scala repository as though it had no language would be worse
// than stopping to say why.
func TestDetectPropagatesRealFailures(t *testing.T) {
	reader := &fakeReader{err: errors.New("GitHub access denied (status 403)")}

	_, err := NewDetector(reader).Detect(context.Background(), "acme", "payments", "sha", nil)
	if err == nil {
		t.Fatal("Detect() = nil error, want the read failure")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %q, want it to carry the underlying failure", err)
	}
}

// TestDetectMissingManifestsIsNotAnError checks a repository with none of the manifests
// yields an empty profile rather than a failure.
func TestDetectMissingManifestsIsNotAnError(t *testing.T) {
	reader := &fakeReader{files: map[string]string{}}

	profile, err := NewDetector(reader).Detect(context.Background(), "acme", "payments", "sha", nil)
	if err != nil {
		t.Fatalf("Detect() = %v", err)
	}
	if !profile.Empty() {
		t.Errorf("profile = %+v, want empty", profile)
	}
}

// TestDetectUsesChangedPaths checks the changed files refine the profile.
func TestDetectUsesChangedPaths(t *testing.T) {
	reader := &fakeReader{files: map[string]string{"go.mod": goMod}}

	profile, err := NewDetector(reader).Detect(context.Background(), "acme", "payments", "sha",
		[]string{"src/main/scala/Export.scala"})
	if err != nil {
		t.Fatalf("Detect() = %v", err)
	}

	if !profile.HasLanguage(LanguageGo) || !profile.HasLanguage(LanguageScala) {
		t.Errorf("Languages = %v, want both go and scala", profile.Languages)
	}
}

// TestDetectLibrariesAndFrameworks checks the small deterministic label set, and that a
// coordinate nobody named is not reported.
func TestDetectLibrariesAndFrameworks(t *testing.T) {
	profile := DetectFromSignals(map[string]string{"build.sbt": buildSBT}, nil)

	for _, want := range []string{"cats", "circe", "scalatest"} {
		if !contains(profile.Libraries, want) {
			t.Errorf("Libraries = %v, want it to include %q", profile.Libraries, want)
		}
	}
	if !contains(profile.Frameworks, "play") {
		t.Errorf("Frameworks = %v, want it to include play", profile.Frameworks)
	}
	for _, unwanted := range []string{"akka", "pekko", "zio", "doobie", "slick"} {
		if profile.HasTechnology(unwanted) {
			t.Errorf("profile reports %q, which the build file does not name", unwanted)
		}
	}
}

// TestDetectGoLibraries checks the Go coordinates still resolve, from a manifest and
// from a patch alike.
func TestDetectGoLibraries(t *testing.T) {
	profile := DetectFromSignals(map[string]string{"go.mod": goMod}, nil)

	for _, want := range []string{"grpc", "gorm"} {
		if !profile.HasTechnology(want) {
			t.Errorf("profile = %+v, want it to include %q", profile, want)
		}
	}

	fromPatch := DetectFromFiles([]FileSignal{
		{Path: "internal/store/db.go", Content: "+import \"database/sql\"\n"},
	})
	if !fromPatch.HasTechnology("sql") {
		t.Errorf("profile = %+v, want sql detected from the patch", fromPatch)
	}
}

// TestProfileOrderingIsStable checks the profile does not depend on the order signals
// arrived in, which map iteration would otherwise make unpredictable.
func TestProfileOrderingIsStable(t *testing.T) {
	manifests := map[string]string{
		"go.mod":                   goMod,
		"build.sbt":                buildSBT,
		"project/build.properties": "sbt.version=1.9.9",
		"project/plugins.sbt":      `addSbtPlugin("com.typesafe.play" % "sbt-plugin" % "2.9.0")`,
	}
	paths := []string{"a.go", "b.scala", "c.sbt", "go.sum"}

	first := DetectFromSignals(manifests, paths)
	for i := 0; i < 20; i++ {
		again := DetectFromSignals(manifests, paths)
		if fmt.Sprint(first) != fmt.Sprint(again) {
			t.Fatalf("run %d gave %+v, first gave %+v", i, again, first)
		}
	}

	// Languages and build systems come out in the package's declared order, not in
	// discovery order.
	if !equalLanguages(first.Languages, []Language{LanguageGo, LanguageScala}) {
		t.Errorf("Languages = %v, want go then scala", first.Languages)
	}
	if !equalBuildSystems(first.BuildSystems, []BuildSystem{BuildSystemGo, BuildSystemSBT}) {
		t.Errorf("BuildSystems = %v, want go then sbt", first.BuildSystems)
	}
}

// TestProfileRemovesDuplicates checks a signal seen many times is reported once.
func TestProfileRemovesDuplicates(t *testing.T) {
	profile := DetectFromSignals(
		map[string]string{"go.mod": goMod, "go.work": "go 1.22\n"},
		[]string{"a.go", "b.go", "c.go", "go.sum", "internal/x/go.mod"},
	)

	if len(profile.Languages) != 1 || profile.Languages[0] != LanguageGo {
		t.Errorf("Languages = %v, want exactly [go]", profile.Languages)
	}
	if len(profile.BuildSystems) != 1 || profile.BuildSystems[0] != BuildSystemGo {
		t.Errorf("BuildSystems = %v, want exactly [go]", profile.BuildSystems)
	}
	if counted := countOccurrences(profile.Technologies()); counted {
		t.Error("Technologies() contains a duplicate label")
	}
}

// TestMergeIsCommutative checks merge order cannot change the answer, which is what
// lets two detection paths be combined without one winning.
func TestMergeIsCommutative(t *testing.T) {
	a := Profile{Languages: []Language{LanguageGo}, Libraries: []string{"sql"}}
	b := Profile{Languages: []Language{LanguageScala}, BuildSystems: []BuildSystem{BuildSystemSBT}, Frameworks: []string{"play"}}

	if got, want := fmt.Sprint(Merge(a, b)), fmt.Sprint(Merge(b, a)); got != want {
		t.Errorf("Merge(a,b) = %s, Merge(b,a) = %s", got, want)
	}
}

// TestProfileDisplayLabels checks the human-facing names.
func TestProfileDisplayLabels(t *testing.T) {
	profile := Profile{
		Languages:    []Language{LanguageGo, LanguageScala},
		BuildSystems: []BuildSystem{BuildSystemGo, BuildSystemSBT},
	}

	if got := strings.Join(profile.LanguageLabels(), ","); got != "Go,Scala" {
		t.Errorf("LanguageLabels() = %q, want %q", got, "Go,Scala")
	}
	if got := strings.Join(profile.BuildSystemLabels(), ","); got != "go,sbt" {
		t.Errorf("BuildSystemLabels() = %q, want %q", got, "go,sbt")
	}
}

// TestEnumsAreClosed pins the recognized sets.
func TestEnumsAreClosed(t *testing.T) {
	if !LanguageGo.Valid() || !LanguageScala.Valid() {
		t.Error("a declared language reports itself invalid")
	}
	if Language("kotlin").Valid() {
		t.Error("an undeclared language reports itself valid")
	}
	if !BuildSystemGo.Valid() || !BuildSystemSBT.Valid() {
		t.Error("a declared build system reports itself invalid")
	}
	if BuildSystem("maven").Valid() {
		t.Error("an undeclared build system reports itself valid")
	}
}

func equalLanguages(got []Language, want []Language) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func equalBuildSystems(got []BuildSystem, want []BuildSystem) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func countOccurrences(values []string) bool {
	seen := map[string]bool{}
	for _, v := range values {
		if seen[v] {
			return true
		}
		seen[v] = true
	}
	return false
}
