package contextselect

import (
	"github.com/your-company/agentic-code-review/internal/technology"
)

// Language and build-system labels, re-exported so callers of this package do not
// have to reach for the detector to name what it found.
const (
	LanguageGo         = string(technology.LanguageGo)
	LanguageScala      = string(technology.LanguageScala)
	LanguageJavaScript = string(technology.LanguageJavaScript)
	LanguageTypeScript = string(technology.LanguageTypeScript)

	BuildSystemGo  = string(technology.BuildSystemGo)
	BuildSystemSBT = string(technology.BuildSystemSBT)
	BuildSystemNPM = string(technology.BuildSystemNPM)
)

// DetectProfile builds a technology profile from the selection candidates alone:
// their paths and the text of their patches.
//
// It is the weaker of the two detection paths and knows it. The repository is never
// read here, so a pull request that touches only documentation reveals nothing about
// the language — that is what the repository-level detector in internal/technology is
// for, and its profile arrives through Select as the stronger signal. Both are merged
// so neither can hide what the other saw.
func DetectProfile(files []candidate) TechnologyProfile {
	signals := make([]technology.FileSignal, 0, len(files))
	for _, f := range files {
		signals = append(signals, technology.FileSignal{Path: f.Path, Content: f.Patch})
	}

	return fromTechnologyProfile(technology.DetectFromFiles(signals))
}

// fromTechnologyProfile converts a detected profile into the string form carried
// through the review.
func fromTechnologyProfile(profile technology.Profile) TechnologyProfile {
	converted := TechnologyProfile{
		Frameworks: profile.Frameworks,
		Libraries:  profile.Libraries,
	}

	for _, language := range profile.Languages {
		converted.Languages = append(converted.Languages, string(language))
	}
	for _, buildSystem := range profile.BuildSystems {
		converted.BuildSystems = append(converted.BuildSystems, string(buildSystem))
	}

	return converted
}
