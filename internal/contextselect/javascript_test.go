package contextselect

import (
	"context"
	"testing"

	"github.com/your-company/agentic-code-review/internal/analysis"
	"github.com/your-company/agentic-code-review/internal/github"
	"github.com/your-company/agentic-code-review/internal/reporules"
	"github.com/your-company/agentic-code-review/internal/review"
	"github.com/your-company/agentic-code-review/internal/technology"
)

func TestFrontendSelectionCarriesTechnologyAndPrioritizesBuildFiles(t *testing.T) {
	files := []github.ChangedFile{
		{Filename: "src/pages/index.tsx", Status: "modified", Patch: "@@ -1 +1,2 @@\n+export default function Page() {}\n"},
		{Filename: "package.json", Status: "modified", Patch: "@@ -1 +1,2 @@\n+  \"next\": \"15.5.21\"\n"},
		{Filename: "README.md", Status: "modified", Patch: "@@ -1 +1,2 @@\n+docs\n"},
	}
	rc := review.BuildContext(
		github.PullRequest{Owner: "acme", Repo: "frontend", Number: 12},
		github.PullRequestDetails{Number: 12, HeadSHA: "abc123", BaseBranch: "main"},
		files, nil, reporules.Rules{},
	)
	profile := technology.Profile{
		Languages:    []technology.Language{technology.LanguageJavaScript, technology.LanguageTypeScript},
		BuildSystems: []technology.BuildSystem{technology.BuildSystemNPM},
		Frameworks:   []string{"next.js"},
		Libraries:    []string{"react"},
	}

	selected, err := NewSelector().SelectWithTechnology(context.Background(), rc, analysis.Result{}, profile)
	if err != nil {
		t.Fatalf("SelectWithTechnology() = %v", err)
	}
	if !selected.Profile.HasLanguage(LanguageJavaScript) || !selected.Profile.HasLanguage(LanguageTypeScript) ||
		!selected.Profile.HasBuildSystem(BuildSystemNPM) || !selected.Profile.HasTechnology("next.js") {
		t.Errorf("Profile = %+v, want the complete frontend stack", selected.Profile)
	}
	build := fileByPath(t, selected, "package.json")
	if build.Importance != ImportanceHigh || build.Reason != "npm/frontend build definition" {
		t.Errorf("package.json = %+v, want promoted frontend build definition", build)
	}
}

func TestChangedTypeScriptSourceCannotFallThroughToUnknown(t *testing.T) {
	profile := technology.DetectFromFiles([]technology.FileSignal{{Path: "src/components/App.tsx"}})
	if !profile.HasLanguage(technology.LanguageTypeScript) {
		t.Errorf("profile = %+v, want TypeScript", profile)
	}
	if got := Classify("src/components/App.test.tsx", ""); got != FileKindTest {
		t.Errorf("test classification = %q, want test", got)
	}
}
