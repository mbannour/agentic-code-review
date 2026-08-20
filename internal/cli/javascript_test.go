package cli

import (
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/technology"
)

func TestPrintFrontendTechnologyAndMissingBuildEvidence(t *testing.T) {
	profile := technology.Profile{
		Languages:    []technology.Language{technology.LanguageJavaScript, technology.LanguageTypeScript},
		BuildSystems: []technology.BuildSystem{technology.BuildSystemNPM},
		Frameworks:   []string{"next.js"},
		Libraries:    []string{"react", "vitest"},
	}

	out := captureStdout(t, func() {
		printTechnology(profile)
		printQualityNote(profile)
	})
	for _, want := range []string{
		"JavaScript", "TypeScript", "npm", "next.js", "react", "vitest",
		"JavaScript/TypeScript-specific reasoning is enabled",
		"npm run build evidence is unavailable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}
