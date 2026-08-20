package verification

import (
	"errors"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/contextselect"
	"github.com/your-company/agentic-code-review/internal/findings"
)

// TestBuildIncludesTheFindingAndItsPatch checks the two sections that must always be
// present: the claim, and the diff it is about.
func TestBuildIncludesTheFindingAndItsPatch(t *testing.T) {
	vctx, err := NewContextBuilder().Build(findingFixture(), selectionFixture())
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if vctx.Finding.ID != "COR-001" {
		t.Errorf("Finding.ID = %q, want COR-001", vctx.Finding.ID)
	}
	if !vctx.HasPatch() {
		t.Fatal("HasPatch() = false; the changed diff is the primary evidence")
	}
	if vctx.RelevantPatch.Label != testFile {
		t.Errorf("patch label = %q, want %q", vctx.RelevantPatch.Label, testFile)
	}
	if !strings.Contains(vctx.RelevantPatch.Content, "p.Declined && !p.Permanent") {
		t.Errorf("patch does not contain the changed line:\n%s", vctx.RelevantPatch.Content)
	}
	if !strings.Contains(vctx.RelevantCode.Content, "claimed lines (new file): 81-83") {
		t.Errorf("location description missing the claimed lines:\n%s", vctx.RelevantCode.Content)
	}
	if !strings.Contains(vctx.RelevantCode.Content, "the diff above covers these lines") {
		t.Error("location description does not state that the diff covers the claim")
	}
	if vctx.Bytes <= 0 {
		t.Error("Bytes was not accounted")
	}
}

// TestBuildDoesNotResendTheWholePullRequest is the efficiency guarantee: only the files the
// finding is actually about travel with it. Resending the pull request per finding would
// double the cost of the review and bury the claim.
func TestBuildDoesNotResendTheWholePullRequest(t *testing.T) {
	selected := selectionFixture()
	for i := 0; i < 25; i++ {
		selected.Files = append(selected.Files, contextselect.SelectedFile{
			Path:   "internal/unrelated/file" + string(rune('a'+i%26)) + ".go",
			Status: "modified",
			Patch:  "@@ -1,3 +1,4 @@\n+// unrelated change " + strings.Repeat("x", 2000) + "\n",
			Kind:   contextselect.FileKindSource,
		})
	}

	vctx, err := NewContextBuilder().Build(findingFixture(), selected)
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if strings.Contains(vctx.RelevantPatch.Content, "unrelated change") {
		t.Error("an unrelated file's patch reached the verification context")
	}
	for _, related := range vctx.RelatedPatches {
		if strings.Contains(related.Label, "unrelated") {
			t.Errorf("unrelated file %q reached the context without being cited", related.Label)
		}
	}
	if vctx.Bytes > MaxVerificationContextBytes {
		t.Errorf("context is %d bytes, over the %d limit", vctx.Bytes, MaxVerificationContextBytes)
	}
}

// TestBuildFollowsCitedFilesOnly checks surrounding code arrives when the finding cited it,
// and not otherwise. Following citations is how "another file may invalidate the claim"
// becomes checkable without turning verification into a second review.
func TestBuildFollowsCitedFilesOnly(t *testing.T) {
	uncited, err := NewContextBuilder().Build(findingFixture(), selectionFixture())
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	if len(uncited.RelatedPatches) != 0 {
		t.Errorf("RelatedPatches = %+v, want none for a finding that cites only its own file",
			uncited.RelatedPatches)
	}

	cited := findingFixture()
	cited.Evidence = append(cited.Evidence, findings.Evidence{
		Type:   findings.EvidenceCode,
		Source: testOtherFile + ":11",
		Detail: "IsPermanent decides the decline kind.",
	})

	vctx, err := NewContextBuilder().Build(cited, selectionFixture())
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	if len(vctx.RelatedPatches) != 1 || vctx.RelatedPatches[0].Label != testOtherFile {
		t.Fatalf("RelatedPatches = %+v, want the cited file", vctx.RelatedPatches)
	}
	if !strings.Contains(vctx.RelatedPatches[0].Content, "IsPermanent") {
		t.Error("the cited file's patch was not included")
	}
}

// TestBuildJiraRelevance checks the ticket travels only when the finding rests on it. The
// ticket cannot help verify a null dereference, and sending it would only add cost.
func TestBuildJiraRelevance(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*findings.Finding)
		wantJira bool
	}{
		{
			name:     "correctness finding citing only code",
			mutate:   func(*findings.Finding) {},
			wantJira: false,
		},
		{
			name: "finding citing Jira evidence",
			mutate: func(f *findings.Finding) {
				f.Evidence = append(f.Evidence, findings.Evidence{
					Type: findings.EvidenceJira, Source: "PAY-431",
					Detail: "Permanent declines must not be retried.",
				})
			},
			wantJira: true,
		},
		{
			name: "requirement finding",
			mutate: func(f *findings.Finding) {
				f.Category = findings.CategoryRequirement
			},
			wantJira: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := findingFixture()
			tt.mutate(&f)

			vctx, err := NewContextBuilder().Build(f, selectionFixture())
			if err != nil {
				t.Fatalf("Build() = %v", err)
			}

			hasJira := vctx.RelevantJira.Content != ""
			if hasJira != tt.wantJira {
				t.Fatalf("Jira included = %t, want %t", hasJira, tt.wantJira)
			}
			if !tt.wantJira {
				return
			}
			for _, want := range []string{"PAY-431", "Permanent declines must not be retried"} {
				if !strings.Contains(vctx.RelevantJira.Content, want) {
					t.Errorf("Jira excerpt missing %q", want)
				}
			}
		})
	}
}

// TestBuildRuleRelevance checks a cited rule document arrives and an uncited one does not.
func TestBuildRuleRelevance(t *testing.T) {
	f := findingFixture()
	f.Evidence = append(f.Evidence, findings.Evidence{
		Type: findings.EvidenceRule, Source: "AGENTS.md",
		Detail: "Never retry a permanently declined authorization.",
	})

	vctx, err := NewContextBuilder().Build(f, selectionFixture())
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if len(vctx.RelevantRules) != 1 {
		t.Fatalf("RelevantRules = %+v, want just the cited document", vctx.RelevantRules)
	}
	if vctx.RelevantRules[0].Label != "AGENTS.md" {
		t.Errorf("rule label = %q, want AGENTS.md", vctx.RelevantRules[0].Label)
	}
	if !strings.Contains(vctx.RelevantRules[0].Content, "Never retry") {
		t.Error("the rule's content was not included")
	}

	uncited, err := NewContextBuilder().Build(findingFixture(), selectionFixture())
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	if len(uncited.RelevantRules) != 0 {
		t.Errorf("RelevantRules = %+v, want none for a finding citing no rule", uncited.RelevantRules)
	}
}

// TestBuildIncludesDeterministicEvidence checks failing checks arrive with their output and
// passing ones arrive as outcomes.
//
// The passing outcome matters as much as the failure: a passing suite does not disprove a
// finding unless it covers the claimed behavior, and the verifier can only weigh that if it
// knows the suite passed.
func TestBuildIncludesDeterministicEvidence(t *testing.T) {
	vctx, err := NewContextBuilder().Build(findingFixture(), selectionFixture())
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if len(vctx.RelevantAnalysis) != 2 {
		t.Fatalf("RelevantAnalysis = %d excerpts, want both checks", len(vctx.RelevantAnalysis))
	}

	joined := ""
	for _, excerpt := range vctx.RelevantAnalysis {
		joined += excerpt.Content
	}

	for _, want := range []string{
		"check: go-test", "status: passed",
		"check: go-vet", "status: failed", "unreachable code",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("analysis evidence missing %q:\n%s", want, joined)
		}
	}
}

// TestBuildIncludesTechnologyProfile checks the stack travels with the claim, so the verifier
// reasons in the right language.
func TestBuildIncludesTechnologyProfile(t *testing.T) {
	selected := selectionFixture()
	selected.Profile = contextselect.TechnologyProfile{
		Languages:    []string{contextselect.LanguageScala},
		BuildSystems: []string{contextselect.BuildSystemSBT},
		Frameworks:   []string{"play"},
	}

	vctx, err := NewContextBuilder().Build(findingFixture(), selected)
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if !vctx.Profile.HasLanguage(contextselect.LanguageScala) {
		t.Errorf("Profile = %+v, want Scala carried through", vctx.Profile)
	}
	if !vctx.Profile.HasBuildSystem(contextselect.BuildSystemSBT) {
		t.Errorf("Profile = %+v, want sbt carried through", vctx.Profile)
	}
}

// TestBuildIsBounded checks a very large pull request cannot produce an oversized context,
// and that the patch is reduced by hunk rather than cut mid-diff.
func TestBuildIsBounded(t *testing.T) {
	var huge strings.Builder
	// Twelve hunks of about 4 KB each, the last of which covers the claim.
	for i := 0; i < 12; i++ {
		start := 100 + i*100
		huge.WriteString("@@ -" + itoa(start) + ",3 +" + itoa(start) + ",4 @@\n")
		huge.WriteString(" context line\n")
		huge.WriteString("+" + strings.Repeat("x", 4000) + "\n")
		huge.WriteString(" trailing context\n")
	}

	selected := selectionFixture()
	selected.Files[0].Patch = huge.String()

	f := findingFixture()
	f.StartLine, f.EndLine = 1101, 1101

	vctx, err := NewContextBuilder().Build(f, selected)
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if vctx.Bytes > MaxVerificationContextBytes {
		t.Errorf("context is %d bytes, over the %d limit", vctx.Bytes, MaxVerificationContextBytes)
	}
	if len(vctx.RelevantPatch.Content) > maxPatchBytes {
		t.Errorf("patch is %d bytes, over its %d allowance", len(vctx.RelevantPatch.Content), maxPatchBytes)
	}
	if !vctx.RelevantPatch.Truncated {
		t.Error("the reduced patch is not marked as such")
	}
	// Reduction is by hunk, so whatever survived still starts at a hunk header.
	if !strings.HasPrefix(vctx.RelevantPatch.Content, "@@") {
		t.Errorf("the reduced patch does not begin at a hunk boundary:\n%.120s", vctx.RelevantPatch.Content)
	}
	if !strings.Contains(vctx.RelevantPatch.Content, "@@ -1100,3 +1100,4 @@") {
		t.Error("the reduced patch dropped the hunk covering the claim")
	}
}

// TestBuildPriorityUnderPressure checks what survives a tight budget: the claim and its
// patch, in that order, and lower-priority sections are dropped by name rather than cut at
// random.
func TestBuildPriorityUnderPressure(t *testing.T) {
	f := findingFixture()
	f.Category = findings.CategoryRequirement
	f.Evidence = append(f.Evidence,
		findings.Evidence{Type: findings.EvidenceRule, Source: "AGENTS.md", Detail: "rule"},
		findings.Evidence{Type: findings.EvidenceJira, Source: "PAY-431", Detail: "ticket"},
	)

	// Enough for the finding and its patch, and nothing more.
	builder := &ContextBuilder{MaxBytes: findingBytes(f) + len(retryPatch) + 120}

	vctx, err := builder.Build(f, selectionFixture())
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if !vctx.HasPatch() {
		t.Fatal("the patch was dropped; it is the highest-priority evidence after the finding")
	}
	if !vctx.Trimmed {
		t.Fatal("Trimmed = false although sections were dropped")
	}
	if vctx.RelevantJira.Content != "" {
		t.Error("the Jira excerpt survived a budget that could not fit it")
	}
	if len(vctx.RelevantRules) != 0 {
		t.Error("a rule excerpt survived a budget that could not fit it")
	}
	if len(vctx.DroppedSections) == 0 {
		t.Error("DroppedSections is empty although sections were dropped")
	}
	if vctx.Bytes > builder.MaxBytes {
		t.Errorf("context is %d bytes, over its own %d budget", vctx.Bytes, builder.MaxBytes)
	}
}

// TestBuildWithoutPatch checks a finding whose file has no diff is reported rather than
// silently verified against nothing.
func TestBuildWithoutPatch(t *testing.T) {
	tests := []struct {
		name     string
		selected func() contextselect.SelectedContext
	}{
		{
			name: "file not in the selection",
			selected: func() contextselect.SelectedContext {
				s := selectionFixture()
				s.Files = s.Files[1:]
				return s
			},
		},
		{
			name: "file present with no patch",
			selected: func() contextselect.SelectedContext {
				s := selectionFixture()
				s.Files[0].Patch = ""
				return s
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vctx, err := NewContextBuilder().Build(findingFixture(), tt.selected())

			if !errors.Is(err, ErrNoPatch) {
				t.Fatalf("Build() = %v, want ErrNoPatch", err)
			}
			if vctx.HasPatch() {
				t.Error("HasPatch() = true with no patch available")
			}
			if vctx.Finding.ID != "COR-001" {
				t.Error("the finding was not carried through")
			}
		})
	}
}

// TestBuildLocationOutsideTheDiff checks a claim pointing outside the changed hunks is
// flagged as such, which is one of the concrete ways a finding turns out to be wrong.
func TestBuildLocationOutsideTheDiff(t *testing.T) {
	f := findingFixture()
	f.StartLine, f.EndLine = 900, 900

	vctx, err := NewContextBuilder().Build(f, selectionFixture())
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}

	if !strings.Contains(vctx.RelevantCode.Content, "does not cover these lines") {
		t.Errorf("location description does not flag the mismatch:\n%s", vctx.RelevantCode.Content)
	}
}

// TestBuildCarriesNoSecrets checks nothing sensitive can reach a verification request. The
// context type has nowhere to put a credential, and this pins that.
func TestBuildCarriesNoSecrets(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_should_never_appear")
	t.Setenv("JIRA_TOKEN", "jira_should_never_appear")

	f := findingFixture()
	f.Category = findings.CategoryRequirement
	f.Evidence = append(f.Evidence,
		findings.Evidence{Type: findings.EvidenceRule, Source: "AGENTS.md", Detail: "rule"},
		findings.Evidence{Type: findings.EvidenceJira, Source: "PAY-431", Detail: "ticket"},
		findings.Evidence{Type: findings.EvidenceVet, Source: "go vet ./...", Detail: "vet output"},
	)

	vctx, err := NewContextBuilder().Build(f, selectionFixture())
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}

	whole := vctx.RelevantPatch.Content + vctx.RelevantCode.Content + vctx.RelevantJira.Content
	for _, excerpt := range append(append([]Excerpt{}, vctx.RelevantRules...), vctx.RelevantAnalysis...) {
		whole += excerpt.Content
	}
	for _, excerpt := range vctx.RelatedPatches {
		whole += excerpt.Content
	}

	for _, forbidden := range []string{
		"ghp_", "jira_should_never_appear", "GITHUB_TOKEN", "JIRA_TOKEN",
		"Authorization", "Bearer", "ANTHROPIC",
	} {
		if strings.Contains(whole, forbidden) {
			t.Errorf("verification context contains %q", forbidden)
		}
	}
}

// TestBuildIsDeterministic checks the same finding and selection always produce the same
// context.
func TestBuildIsDeterministic(t *testing.T) {
	first, err := NewContextBuilder().Build(findingFixture(), selectionFixture())
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}

	for i := 0; i < 10; i++ {
		again, err := NewContextBuilder().Build(findingFixture(), selectionFixture())
		if err != nil {
			t.Fatalf("Build() = %v", err)
		}
		if again.Bytes != first.Bytes || again.RelevantPatch.Content != first.RelevantPatch.Content {
			t.Fatalf("run %d produced a different context", i)
		}
	}
}

// itoa is a small helper so the patch fixture above stays readable.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
