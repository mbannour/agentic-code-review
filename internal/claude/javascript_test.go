package claude

import (
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/contextselect"
)

func frontendProfile() contextselect.TechnologyProfile {
	return contextselect.TechnologyProfile{
		Languages:    []string{contextselect.LanguageJavaScript, contextselect.LanguageTypeScript},
		BuildSystems: []string{contextselect.BuildSystemNPM},
		Frameworks:   []string{"next.js"},
		Libraries:    []string{"i18next", "playwright", "react", "redux-toolkit", "vitest"},
	}
}

func TestFrontendGuidanceIsTechnologySpecific(t *testing.T) {
	content := buildFor(t, frontendProfile())

	for _, want := range []string{
		"JavaScript semantics:", "Promise and async control flow", "browser-only APIs",
		"TypeScript semantics:", "runtime validation at untyped boundaries", "discriminated-union exhaustiveness",
		"npm build:", "package.json and package-lock.json consistency",
		"next.js:", "server and client component boundaries", "hydration behavior",
		"react:", "Hook dependency arrays", "effect cleanup",
		"redux-toolkit:", "async thunk rejection",
		"vitest:", "async assertions",
		"playwright:", "locators and waits",
		"i18next:", "translation keys",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("frontend review input missing %q", want)
		}
	}
	for _, absent := range []string{"Go semantics:", "Scala semantics:", "sbt build:"} {
		if strings.Contains(content, absent) {
			t.Errorf("frontend review input contains unrelated %q", absent)
		}
	}
}

func TestFrontendProfileIsRenderedIntoReviewInput(t *testing.T) {
	content := buildFor(t, frontendProfile())
	for _, want := range []string{
		"languages: javascript, typescript",
		"build systems: npm",
		"frameworks: next.js",
		"libraries: i18next, playwright, react, redux-toolkit, vitest",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("profile section missing %q", want)
		}
	}
}
