package technology

import (
	"strings"
	"testing"
)

const veactFrontendPackageJSON = `{
  "scripts": {"build": "next build", "test": "vitest", "test:e2e": "playwright test"},
  "dependencies": {
    "@mui/material": "^7.3.7",
    "@reduxjs/toolkit": "^1.9.5",
    "i18next": "^23.0.1",
    "next": "15.5.21",
    "react": "18.3.1"
  },
  "devDependencies": {"vitest": "^4.1.10"}
}`

func TestDetectVeactFrontendTechnology(t *testing.T) {
	profile := DetectFromSignals(map[string]string{
		"package.json":      veactFrontendPackageJSON,
		"package-lock.json": `{"lockfileVersion": 3}`,
		"tsconfig.json":     `{"compilerOptions":{"strict":true}}`,
	}, []string{"src/pages/index.tsx", "next.config.js"})

	if !equalLanguages(profile.Languages, []Language{LanguageJavaScript, LanguageTypeScript}) {
		t.Errorf("Languages = %v, want JavaScript and TypeScript", profile.Languages)
	}
	if !equalBuildSystems(profile.BuildSystems, []BuildSystem{BuildSystemNPM}) {
		t.Errorf("BuildSystems = %v, want npm", profile.BuildSystems)
	}
	if !contains(profile.Frameworks, "next.js") {
		t.Errorf("Frameworks = %v, want next.js", profile.Frameworks)
	}
	for _, want := range []string{"react", "vitest", "playwright", "redux-toolkit", "mui", "i18next"} {
		if !profile.HasTechnology(want) {
			t.Errorf("Technologies = %v, want %q", profile.Technologies(), want)
		}
	}
}

func TestFrontendManifestSignalsRemainDistinct(t *testing.T) {
	tests := []struct {
		name            string
		manifests       map[string]string
		wantLanguage    Language
		wantBuildSystem BuildSystem
		noBuildSystem   bool
	}{
		{"package json proves JavaScript, not npm", map[string]string{"package.json": `{}`}, LanguageJavaScript, "", true},
		{"package lock proves npm", map[string]string{"package-lock.json": `{}`}, LanguageJavaScript, BuildSystemNPM, false},
		{"tsconfig proves TypeScript", map[string]string{"tsconfig.json": `{}`}, LanguageTypeScript, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := DetectFromSignals(tt.manifests, nil)
			if !profile.HasLanguage(tt.wantLanguage) {
				t.Errorf("Languages = %v, want %s", profile.Languages, tt.wantLanguage)
			}
			if tt.noBuildSystem && len(profile.BuildSystems) != 0 {
				t.Errorf("BuildSystems = %v, want none", profile.BuildSystems)
			}
			if tt.wantBuildSystem != "" && !profile.HasBuildSystem(tt.wantBuildSystem) {
				t.Errorf("BuildSystems = %v, want %s", profile.BuildSystems, tt.wantBuildSystem)
			}
		})
	}
}

func TestFrontendExtensionsDetectLanguagesWithoutGuessingPackageManager(t *testing.T) {
	profile := DetectFromSignals(nil, []string{
		"src/a.js", "src/b.jsx", "src/c.mjs", "src/d.cjs",
		"src/e.ts", "src/f.tsx", "src/g.mts", "src/h.cts",
	})

	if !profile.HasLanguage(LanguageJavaScript) || !profile.HasLanguage(LanguageTypeScript) {
		t.Errorf("Languages = %v, want JavaScript and TypeScript", profile.Languages)
	}
	if len(profile.BuildSystems) != 0 {
		t.Errorf("BuildSystems = %v, want none from extensions alone", profile.BuildSystems)
	}
}

func TestDocumentationOnlyFrontendStillDetectsRepositoryTechnology(t *testing.T) {
	profile := DetectFromSignals(map[string]string{
		"package.json":      veactFrontendPackageJSON,
		"package-lock.json": `{}`,
		"tsconfig.json":     `{}`,
	}, []string{"README.md"})

	if profile.Empty() || !profile.HasLanguage(LanguageTypeScript) ||
		!profile.HasBuildSystem(BuildSystemNPM) || !profile.HasTechnology("next.js") {
		t.Errorf("profile = %+v, want the frontend stack on a documentation-only PR", profile)
	}
}

func TestNPMBuildFileRecognition(t *testing.T) {
	for _, file := range []string{
		"package.json", "package-lock.json", "tsconfig.json", "next.config.js", "eslint.config.mjs",
	} {
		if !IsNPMBuildFile(file) {
			t.Errorf("IsNPMBuildFile(%q) = false", file)
		}
	}
	if IsNPMBuildFile("src/pages/index.tsx") {
		t.Error("ordinary source was treated as an npm build file")
	}
}

func TestFrontendProfileDisplayAndEnums(t *testing.T) {
	profile := Profile{
		Languages:    []Language{LanguageJavaScript, LanguageTypeScript},
		BuildSystems: []BuildSystem{BuildSystemNPM},
	}
	if got := strings.Join(profile.LanguageLabels(), ","); got != "JavaScript,TypeScript" {
		t.Errorf("LanguageLabels() = %q", got)
	}
	if got := strings.Join(profile.BuildSystemLabels(), ","); got != "npm" {
		t.Errorf("BuildSystemLabels() = %q", got)
	}
	if !LanguageJavaScript.Valid() || !LanguageTypeScript.Valid() || !BuildSystemNPM.Valid() {
		t.Error("a declared frontend language or build system reports itself invalid")
	}
}
