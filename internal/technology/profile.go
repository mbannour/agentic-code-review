// Package technology decides, from repository signals alone, what a project is
// built with.
//
// It exists so that exactly one component answers the question "what language and
// toolchain is this?" — deterministically, from files the repository actually
// contains, and never by asking a model. Its answer becomes normalized application
// data that two later stages consume: the deterministic checks choose what to run
// from it, and the review input tells the model which language semantics are worth
// weighing. Neither of those stages inspects repository files itself.
//
// Adding a language means adding its signals and its toolchain here. Nothing in the
// review pipeline changes.
package technology

import "sort"

// Language is a programming language this tool recognizes.
//
// The set is closed: an unrecognized language is reported as no language rather
// than as a guess, so a later stage never receives a label it has no guidance for.
type Language string

const (
	LanguageGo         Language = "go"
	LanguageScala      Language = "scala"
	LanguageJavaScript Language = "javascript"
	LanguageTypeScript Language = "typescript"
)

// Languages lists every recognized language, in a stable order.
func Languages() []Language {
	return []Language{LanguageGo, LanguageScala, LanguageJavaScript, LanguageTypeScript}
}

// Valid reports whether l is a recognized language.
func (l Language) Valid() bool {
	for _, known := range Languages() {
		if l == known {
			return true
		}
	}
	return false
}

// Display renders the language for terminal and review output.
func (l Language) Display() string {
	switch l {
	case LanguageGo:
		return "Go"
	case LanguageScala:
		return "Scala"
	case LanguageJavaScript:
		return "JavaScript"
	case LanguageTypeScript:
		return "TypeScript"
	default:
		return string(l)
	}
}

// BuildSystem is a toolchain this tool knows how to invoke.
//
// A build system is tracked separately from its language because the two do not
// map one to one: Scala can be built by sbt, Maven, or Gradle, and which one it is
// decides what commands exist.
type BuildSystem string

const (
	BuildSystemGo  BuildSystem = "go"
	BuildSystemSBT BuildSystem = "sbt"
	BuildSystemNPM BuildSystem = "npm"
)

// BuildSystems lists every recognized build system, in a stable order.
func BuildSystems() []BuildSystem {
	return []BuildSystem{BuildSystemGo, BuildSystemSBT, BuildSystemNPM}
}

// Valid reports whether b is a recognized build system.
func (b BuildSystem) Valid() bool {
	for _, known := range BuildSystems() {
		if b == known {
			return true
		}
	}
	return false
}

// Display renders the build system for terminal and review output.
func (b BuildSystem) Display() string {
	switch b {
	case BuildSystemGo:
		return "go"
	case BuildSystemSBT:
		return "sbt"
	case BuildSystemNPM:
		return "npm"
	default:
		return string(b)
	}
}

// Profile is what a repository is built with.
//
// Every field is sorted and free of duplicates, so the same repository always
// produces the same profile whatever order its signals arrived in. More than one
// language and more than one build system are expected: a repository with both a
// go.mod and a build.sbt is two projects, and pretending otherwise would silently
// drop half its checks.
//
// Frameworks and Libraries are free-form labels rather than enums. They are hints
// for review guidance, not decisions, and a short readable list is worth more here
// than an exhaustive package database.
type Profile struct {
	Languages    []Language
	BuildSystems []BuildSystem
	Frameworks   []string
	Libraries    []string
}

// Empty reports whether nothing was detected.
func (p Profile) Empty() bool {
	return len(p.Languages) == 0 && len(p.BuildSystems) == 0 &&
		len(p.Frameworks) == 0 && len(p.Libraries) == 0
}

// HasLanguage reports whether the language was detected.
func (p Profile) HasLanguage(language Language) bool {
	for _, l := range p.Languages {
		if l == language {
			return true
		}
	}
	return false
}

// HasBuildSystem reports whether the build system was detected.
func (p Profile) HasBuildSystem(buildSystem BuildSystem) bool {
	for _, b := range p.BuildSystems {
		if b == buildSystem {
			return true
		}
	}
	return false
}

// HasTechnology reports whether the label was detected as a framework or a library.
func (p Profile) HasTechnology(label string) bool {
	for _, name := range append(append([]string{}, p.Frameworks...), p.Libraries...) {
		if name == label {
			return true
		}
	}
	return false
}

// Technologies returns the frameworks and libraries as one sorted list. It is what
// review guidance keys off when the distinction between the two does not matter.
func (p Profile) Technologies() []string {
	set := map[string]bool{}
	for _, name := range p.Frameworks {
		set[name] = true
	}
	for _, name := range p.Libraries {
		set[name] = true
	}
	return sortedStrings(set)
}

// LanguageLabels renders the languages for display, in profile order.
func (p Profile) LanguageLabels() []string {
	labels := make([]string, 0, len(p.Languages))
	for _, l := range p.Languages {
		labels = append(labels, l.Display())
	}
	return labels
}

// BuildSystemLabels renders the build systems for display, in profile order.
func (p Profile) BuildSystemLabels() []string {
	labels := make([]string, 0, len(p.BuildSystems))
	for _, b := range p.BuildSystems {
		labels = append(labels, b.Display())
	}
	return labels
}

// Merge combines two profiles into one.
//
// It is how a profile built from repository manifests is joined with one built from
// the changed files: each source sees only part of the picture, and neither should
// be able to hide what the other found. The result is normalized, so merging is
// commutative and repeatable.
func Merge(profiles ...Profile) Profile {
	languages := map[Language]bool{}
	buildSystems := map[BuildSystem]bool{}
	frameworks := map[string]bool{}
	libraries := map[string]bool{}

	for _, p := range profiles {
		for _, l := range p.Languages {
			languages[l] = true
		}
		for _, b := range p.BuildSystems {
			buildSystems[b] = true
		}
		for _, f := range p.Frameworks {
			frameworks[f] = true
		}
		for _, l := range p.Libraries {
			libraries[l] = true
		}
	}

	return Profile{
		Languages:    sortedLanguages(languages),
		BuildSystems: sortedBuildSystems(buildSystems),
		Frameworks:   sortedStrings(frameworks),
		Libraries:    sortedStrings(libraries),
	}
}

// sortedLanguages returns the set's languages in recognized order.
func sortedLanguages(set map[Language]bool) []Language {
	if len(set) == 0 {
		return nil
	}
	out := make([]Language, 0, len(set))
	for _, known := range Languages() {
		if set[known] {
			out = append(out, known)
		}
	}
	// An unrecognized value cannot reach a profile through this package, but if one
	// ever did it would be reported rather than silently dropped.
	for l := range set {
		if !l.Valid() {
			out = append(out, l)
		}
	}
	return out
}

// sortedBuildSystems returns the set's build systems in recognized order.
func sortedBuildSystems(set map[BuildSystem]bool) []BuildSystem {
	if len(set) == 0 {
		return nil
	}
	out := make([]BuildSystem, 0, len(set))
	for _, known := range BuildSystems() {
		if set[known] {
			out = append(out, known)
		}
	}
	for b := range set {
		if !b.Valid() {
			out = append(out, b)
		}
	}
	return out
}

// sortedStrings returns a set's members in a stable order.
func sortedStrings(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
