package technology

import (
	"context"
	"errors"
	"path"
	"strings"
)

// FileReader reads one explicitly named file from a repository at a ref.
//
// It is an interface with a single method for a reason: detection may read only the
// files named in ManifestPaths, and there is deliberately no way from here to list a
// directory, walk a tree, or search the repository. The GitHub client satisfies it.
type FileReader interface {
	GetRepositoryFile(
		ctx context.Context,
		owner string,
		repo string,
		path string,
		ref string,
	) (string, error)
}

// ErrNotFound is what a FileReader is expected to return, or wrap, for a file the
// repository does not have. Every manifest is optional, so a missing one is normal.
//
// It is declared here rather than imported so this package stays independent of any
// particular API client.
var ErrNotFound = errors.New("not found")

// notFound reports whether err means "this repository does not have that file".
//
// The reader's own sentinel is matched by message as well as by identity, because
// the client's not-found error is its own value. Anything else is a real failure and
// must not be mistaken for absence.
func notFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotFound) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

// ManifestPaths is the allow-list of repository-level files detection may read, in
// the order they are read.
//
// It is an explicit list, never a scan: nothing outside it can be pulled out of the
// repository by this stage. Adding a language means adding its manifest here.
var ManifestPaths = []string{
	// Go
	"go.mod",
	"go.work",

	// Scala / sbt
	"build.sbt",
	"project/build.properties",
	"project/plugins.sbt",

	// JavaScript / TypeScript / npm
	"package.json",
	"package-lock.json",
	"tsconfig.json",
}

// manifestSignal maps a manifest path onto what its mere existence proves.
//
// Existence is the strongest signal available and is treated as such: a repository
// with a build.sbt is a Scala/sbt repository even when the pull request under review
// touches nothing but a README. File extensions are a weaker, secondary signal, used
// only to catch what no manifest revealed.
var manifestSignals = map[string]struct {
	languages    []Language
	buildSystems []BuildSystem
}{
	"go.mod":                   {languages: []Language{LanguageGo}, buildSystems: []BuildSystem{BuildSystemGo}},
	"go.work":                  {languages: []Language{LanguageGo}, buildSystems: []BuildSystem{BuildSystemGo}},
	"build.sbt":                {languages: []Language{LanguageScala}, buildSystems: []BuildSystem{BuildSystemSBT}},
	"project/build.properties": {buildSystems: []BuildSystem{BuildSystemSBT}},
	// plugins.sbt is itself Scala build code; build.properties only pins the sbt
	// version, so it proves the toolchain without proving the language.
	"project/plugins.sbt": {languages: []Language{LanguageScala}, buildSystems: []BuildSystem{BuildSystemSBT}},

	// package.json proves the JavaScript/Node ecosystem but not the package manager;
	// package-lock.json is specifically npm-owned. tsconfig.json proves TypeScript
	// independently of whether npm, yarn, or pnpm drives the build.
	"package.json":      {languages: []Language{LanguageJavaScript}},
	"package-lock.json": {languages: []Language{LanguageJavaScript}, buildSystems: []BuildSystem{BuildSystemNPM}},
	"tsconfig.json":     {languages: []Language{LanguageTypeScript}},
}

// Detector builds a profile from a repository's manifests.
type Detector struct {
	reader FileReader
	paths  []string
}

// NewDetector returns a Detector reading through reader.
func NewDetector(reader FileReader) *Detector {
	return &Detector{reader: reader, paths: ManifestPaths}
}

// Detect builds the profile for owner/repo at ref, optionally refined by the paths
// the pull request changed.
//
// ref should be the reviewed head SHA, so the profile describes the exact snapshot
// under review. A manifest that does not exist is skipped; any other read failure is
// returned, because reviewing a Scala repository as though it had no language is
// worse than stopping to say why.
func (d *Detector) Detect(
	ctx context.Context,
	owner string,
	repo string,
	ref string,
	changedPaths []string,
) (Profile, error) {
	manifests := map[string]string{}

	for _, manifestPath := range d.paths {
		content, err := d.reader.GetRepositoryFile(ctx, owner, repo, manifestPath, ref)
		if err != nil {
			if notFound(err) {
				continue
			}
			return Profile{}, err
		}
		manifests[manifestPath] = content
	}

	return DetectFromSignals(manifests, changedPaths), nil
}

// DetectFromSignals builds a profile from manifest contents and changed paths.
//
// It is the whole of the detection logic, kept pure so it can be exercised without a
// repository: manifests maps a path from ManifestPaths to its content, and paths are
// the files the pull request touched.
func DetectFromSignals(manifests map[string]string, paths []string) Profile {
	var profiles []Profile

	for manifestPath, content := range manifests {
		profiles = append(profiles, profileFromManifest(manifestPath, content))
	}
	profiles = append(profiles, profileFromPaths(paths))

	return Merge(profiles...)
}

// FileSignal is one file the pull request touched, with as much of its content as a
// caller happens to have. Content is optional: a path alone is a usable signal.
type FileSignal struct {
	Path    string
	Content string
}

// DetectFromFiles builds a profile from changed files and their patches.
//
// It is the weaker half of detection, for callers that have the diff but not the
// repository. Patches are searched for dependency coordinates, which is why a
// changed build.sbt or go.mod contributes its libraries here too.
func DetectFromFiles(files []FileSignal) Profile {
	profiles := make([]Profile, 0, len(files)+1)

	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)

		base := normalizeManifestPath(f.Path)
		if _, known := manifestSignals[base]; known {
			profiles = append(profiles, profileFromManifest(base, f.Content))
			continue
		}

		// A patch of any file may still name a dependency; the coordinate tables
		// are specific enough that a false positive needs the coordinate itself.
		profiles = append(profiles, Profile{
			Libraries:  librariesFromContent(f.Content),
			Frameworks: frameworksFromContent(f.Content),
		})
	}

	profiles = append(profiles, profileFromPaths(paths))
	return Merge(profiles...)
}

// profileFromManifest turns one manifest into a profile: what its existence proves,
// plus whatever its content names.
func profileFromManifest(manifestPath string, content string) Profile {
	profile := Profile{
		Libraries:  librariesFromContent(content),
		Frameworks: frameworksFromContent(content),
	}

	if signal, known := manifestSignals[normalizeManifestPath(manifestPath)]; known {
		profile.Languages = append(profile.Languages, signal.languages...)
		profile.BuildSystems = append(profile.BuildSystems, signal.buildSystems...)
	}

	return Merge(profile)
}

// profileFromPaths applies the extension heuristics.
//
// These are secondary on purpose. A *.scala file proves Scala, but it says nothing
// about which build tool drives it, so no build system is inferred from an extension
// alone — running sbt against a repository that has no sbt would be a guess.
func profileFromPaths(paths []string) Profile {
	var profile Profile

	languages := map[Language]bool{}
	buildSystems := map[BuildSystem]bool{}

	for _, filePath := range paths {
		lower := strings.ToLower(strings.TrimSpace(filePath))
		if lower == "" {
			continue
		}

		switch {
		case strings.HasSuffix(lower, "go.sum"), strings.HasSuffix(lower, "go.work.sum"):
			// A checksum file is written by the Go tool itself, so it proves the
			// toolchain as well as the language.
			languages[LanguageGo] = true
			buildSystems[BuildSystemGo] = true
		case strings.HasSuffix(lower, ".go"):
			languages[LanguageGo] = true
		case strings.HasSuffix(lower, ".scala"):
			languages[LanguageScala] = true
		case strings.HasSuffix(lower, ".sbt"):
			// An .sbt file is build definition, so it does prove the toolchain.
			languages[LanguageScala] = true
			buildSystems[BuildSystemSBT] = true
		case strings.HasSuffix(lower, ".js"), strings.HasSuffix(lower, ".jsx"),
			strings.HasSuffix(lower, ".mjs"), strings.HasSuffix(lower, ".cjs"):
			languages[LanguageJavaScript] = true
		case strings.HasSuffix(lower, ".ts"), strings.HasSuffix(lower, ".tsx"),
			strings.HasSuffix(lower, ".mts"), strings.HasSuffix(lower, ".cts"):
			languages[LanguageTypeScript] = true
		}

		if signal, known := manifestSignals[normalizeManifestPath(filePath)]; known {
			for _, l := range signal.languages {
				languages[l] = true
			}
			for _, b := range signal.buildSystems {
				buildSystems[b] = true
			}
		}
	}

	profile.Languages = sortedLanguages(languages)
	profile.BuildSystems = sortedBuildSystems(buildSystems)
	return profile
}

// normalizeManifestPath reduces a path to the form used as a manifest key, so
// "./go.mod" and "project/plugins.sbt" both match.
func normalizeManifestPath(filePath string) string {
	clean := strings.TrimPrefix(path.Clean(strings.TrimSpace(filePath)), "./")
	clean = strings.TrimPrefix(clean, "/")
	lower := strings.ToLower(clean)

	// Two-segment manifests are matched whole; everything else by base name, so a
	// go.mod in a subdirectory of a multi-module repository still counts.
	if _, known := manifestSignals[lower]; known {
		return lower
	}
	base := strings.ToLower(path.Base(clean))
	if _, known := manifestSignals[base]; known {
		return base
	}
	if strings.HasSuffix(lower, "/project/build.properties") {
		return "project/build.properties"
	}
	if strings.HasSuffix(lower, "/project/plugins.sbt") {
		return "project/plugins.sbt"
	}
	return lower
}

// librariesFromContent collects library labels named in a manifest or patch.
func librariesFromContent(content string) []string {
	if strings.TrimSpace(content) == "" {
		return nil
	}

	set := map[string]bool{}
	for _, signal := range librarySignals() {
		if signal.framework {
			continue
		}
		if strings.Contains(content, signal.coordinate) {
			set[signal.label] = true
		}
	}
	// The Scala major version is a language fact, not a dependency, but it travels
	// as a hint like any other: it decides nothing and steers guidance.
	for _, label := range scalaVersionLabels(content) {
		set[label] = true
	}
	return sortedStrings(set)
}

// frameworksFromContent collects framework labels named in a manifest or patch.
func frameworksFromContent(content string) []string {
	if strings.TrimSpace(content) == "" {
		return nil
	}

	set := map[string]bool{}
	for _, signal := range librarySignals() {
		if !signal.framework {
			continue
		}
		if strings.Contains(content, signal.coordinate) {
			set[signal.label] = true
		}
	}
	return sortedStrings(set)
}

// librarySignals is the complete small coordinate table. Keeping the composition
// here makes adding a language additive without making the two scanners drift.
func librarySignals() []librarySignal {
	signals := append([]librarySignal{}, goLibrarySignals()...)
	signals = append(signals, scalaLibrarySignals()...)
	signals = append(signals, javascriptLibrarySignals()...)
	return signals
}

// librarySignal maps a dependency coordinate or import path onto a short label.
//
// The tables are deliberately small. Their purpose is to tell a reviewer which
// semantics are worth weighing, and a label nobody has guidance for is not worth
// detecting.
type librarySignal struct {
	// coordinate is the substring searched for in a manifest or patch.
	coordinate string

	// label is what the profile reports.
	label string

	// framework marks a label that shapes a whole application, as opposed to a
	// library it merely uses.
	framework bool
}

// goLibrarySignals are the Go dependencies worth reporting.
func goLibrarySignals() []librarySignal {
	return []librarySignal{
		{coordinate: "database/sql", label: "sql"},
		{coordinate: "gorm.io/gorm", label: "gorm"},
		{coordinate: "google.golang.org/grpc", label: "grpc"},
		{coordinate: "github.com/gin-gonic/gin", label: "gin", framework: true},
		{coordinate: "github.com/go-chi/chi", label: "chi", framework: true},
		{coordinate: "github.com/gorilla/mux", label: "http-router", framework: true},
		{coordinate: "go.opentelemetry.io", label: "opentelemetry"},
		{coordinate: "kubernetes.io", label: "kubernetes"},
		{coordinate: "k8s.io/", label: "kubernetes"},
	}
}
