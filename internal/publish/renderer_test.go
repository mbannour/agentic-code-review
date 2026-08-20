package publish

import (
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/contextselect"
	"github.com/your-company/agentic-code-review/internal/findings"
)

// TestInlineCommentContainsEveryField checks a rendered comment is actionable on its
// own: it names the finding, states the problem, the impact, the evidence, the
// remediation, and the evidence-strength band.
func TestInlineCommentContainsEveryField(t *testing.T) {
	f := findingFixture()
	body := NewRenderer().InlineComment(f)

	for _, want := range []string{
		"COR-001",
		"HIGH",
		"Permanent declines enter the retry path",
		"The new retry branch treats permanent declines as retryable failures.",
		"**Impact:** Declined authorizations can be submitted repeatedly.",
		"**Evidence**",
		"`PAY-431` — Permanent declines must not be retried.",
		"`" + testFile + ":81-83`",
		"**Suggestion:** Return before entering the retry path for permanent declines.",
		"Evidence strength: HIGH",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("comment does not contain %q\n---\n%s", want, body)
		}
	}
}

// TestInlineCommentEvidenceStrength checks raw scores are rendered only as ordinal bands.
func TestInlineCommentEvidenceStrength(t *testing.T) {
	tests := []struct {
		confidence float64
		want       string
	}{
		{0.79, "Evidence strength: LOW"},
		{0.80, "Evidence strength: MEDIUM"},
		{0.899, "Evidence strength: MEDIUM"},
		{0.90, "Evidence strength: HIGH"},
	}

	for _, tt := range tests {
		f := findingFixture()
		f.Confidence = tt.confidence
		if body := NewRenderer().InlineComment(f); !strings.Contains(body, tt.want) {
			t.Errorf("raw score %v: comment does not contain %q", tt.confidence, tt.want)
		}
	}
}

// TestInlineCommentIsBounded checks the size limit holds even for a finding at every
// field's maximum length, and that shortening never removes the parts that make the
// comment usable.
func TestInlineCommentIsBounded(t *testing.T) {
	f := findingFixture()
	f.Problem = strings.Repeat("p", findings.MaxProblemChars)
	f.Impact = strings.Repeat("i", findings.MaxImpactChars)
	f.Suggestion = strings.Repeat("s", findings.MaxSuggestionChars)
	f.Evidence = nil
	for i := 0; i < findings.MaxEvidencePerFinding; i++ {
		f.Evidence = append(f.Evidence, findings.Evidence{
			Type:   findings.EvidenceCode,
			Source: strings.Repeat("q", 200),
			Detail: strings.Repeat("d", findings.MaxEvidenceDetailChars),
		})
	}

	body := NewRenderer().InlineComment(f)

	if len(body) > MaxInlineCommentBytes {
		t.Fatalf("comment is %d bytes, over the %d limit", len(body), MaxInlineCommentBytes)
	}
	for _, want := range []string{"COR-001", "HIGH", f.Title, "**Impact:**", "**Suggestion:**", "Evidence strength: HIGH"} {
		if !strings.Contains(body, want) {
			t.Errorf("shortened comment lost %q", want)
		}
	}
	if !strings.Contains(body, strings.Repeat("p", findings.MaxProblemChars)) {
		t.Error("shortened comment truncated the problem statement, which must be preserved")
	}
}

// TestInlineCommentShorteningIsDeterministic checks the same oversized finding always
// shortens the same way.
func TestInlineCommentShorteningIsDeterministic(t *testing.T) {
	f := findingFixture()
	for i := 0; i < findings.MaxEvidencePerFinding; i++ {
		f.Evidence = append(f.Evidence, findings.Evidence{
			Type:   findings.EvidenceCode,
			Source: "src",
			Detail: strings.Repeat("d", findings.MaxEvidenceDetailChars),
		})
	}

	first := NewRenderer().InlineComment(f)
	for i := 0; i < 3; i++ {
		if again := NewRenderer().InlineComment(f); again != first {
			t.Fatal("InlineComment() is not deterministic")
		}
	}
}

// TestInlineCommentCollapsesTitleWhitespace checks a multi-line title cannot break
// the Markdown structure it sits inside.
func TestInlineCommentCollapsesTitleWhitespace(t *testing.T) {
	f := findingFixture()
	f.Title = "Broken\ntitle\twith   whitespace"

	body := NewRenderer().InlineComment(f)
	if !strings.Contains(body, "**HIGH · COR-001 — Broken title with whitespace**") {
		t.Errorf("title whitespace was not collapsed\n---\n%s", body)
	}
}

// TestReviewBodySections checks the review body reports the commit, the ticket, the
// result, the checks, the inline count, and the findings that had no line.
func TestReviewBodySections(t *testing.T) {
	plan := planFor(finding("COR-001", findings.SeverityHigh, 0.96, testFile, 82, 82),
		finding("SEC-001", findings.SeverityMedium, 0.90, testFile, 83, 83),
		finding("TEST-001", findings.SeverityMedium, 0.90, testFile, 900, 900))

	body := NewRenderer().ReviewBody(ReviewInput{
		Plan:    plan,
		HeadSHA: testHeadSHA,
		JiraKey: "PAY-431",
		Checks:  selectionFixture().Analysis,
	})

	for _, want := range []string{
		"## ARC Agentic Code Review",
		"Reviewed commit: `" + testHeadSHA + "`",
		"Jira: `PAY-431`",
		"### Result",
		"3 findings",
		"- 1 high",
		"- 2 medium",
		"### Deterministic analysis",
		"✅ `go test ./...`",
		"✅ `go vet ./...`",
		"### Inline findings",
		"2 findings were attached to changed lines.",
		"### Additional findings",
		"**MEDIUM · TEST-001",
		"Evidence strength: HIGH",
		"Generated by ARC.",
		"<!-- arc-review:v1 head=" + testHeadSHA + " -->",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("review body does not contain %q\n---\n%s", want, body)
		}
	}
}

// TestReviewBodyFailedChecks checks a failing check is reported as failing, and that
// the check's output is not dumped into the review.
func TestReviewBodyFailedChecks(t *testing.T) {
	body := NewRenderer().ReviewBody(ReviewInput{
		Plan:    planFor(findingFixture()),
		HeadSHA: testHeadSHA,
		Checks: []contextselect.SelectedAnalysis{
			{Name: "go-test", Command: "go test ./...", Passed: false, Output: "FAIL secret-looking output"},
			{Name: "go-vet", Command: "go vet ./...", Skipped: true},
		},
	})

	if !strings.Contains(body, "❌ `go test ./...`") {
		t.Errorf("failing check not reported as failing\n---\n%s", body)
	}
	if !strings.Contains(body, "(skipped)") {
		t.Errorf("skipped check not reported as skipped\n---\n%s", body)
	}
	if strings.Contains(body, "secret-looking output") {
		t.Error("review body includes raw check output; only outcomes belong in a review")
	}
}

// TestReviewBodySingleFinding checks the singular grammar.
func TestReviewBodySingleFinding(t *testing.T) {
	plan := planFor(findingFixture())

	body := NewRenderer().ReviewBody(ReviewInput{Plan: plan, HeadSHA: testHeadSHA})

	for _, want := range []string{"1 finding", "1 finding was attached to a changed line."} {
		if !strings.Contains(body, want) {
			t.Errorf("review body does not contain %q\n---\n%s", want, body)
		}
	}
}

// TestReviewBodyWithoutJira checks a pull request with no ticket produces no empty
// Jira line.
func TestReviewBodyWithoutJira(t *testing.T) {
	body := NewRenderer().ReviewBody(ReviewInput{
		Plan:    planFor(findingFixture()),
		HeadSHA: testHeadSHA,
	})

	if strings.Contains(body, "Jira:") {
		t.Errorf("review body mentions Jira when no ticket was resolved\n---\n%s", body)
	}
}

// TestReviewBodyIsBounded checks the body limit holds with a full plan of
// maximum-length findings.
func TestReviewBodyIsBounded(t *testing.T) {
	var fs []findings.Finding
	for i := 0; i < findings.MaxFindings; i++ {
		f := finding("LOW-"+string(rune('A'+i)), findings.SeverityLow, 0.99, testFile, 82, 82)
		f.Title = strings.Repeat("t", findings.MaxTitleChars)
		f.Problem = strings.Repeat("p", findings.MaxProblemChars)
		f.Impact = strings.Repeat("i", findings.MaxImpactChars)
		f.Suggestion = strings.Repeat("s", findings.MaxSuggestionChars)
		fs = append(fs, f)
	}

	plan := planFor(fs...)
	body := NewRenderer().ReviewBody(ReviewInput{Plan: plan, HeadSHA: testHeadSHA})

	if len(body) > MaxReviewBodyBytes {
		t.Errorf("review body is %d bytes, over the %d limit", len(body), MaxReviewBodyBytes)
	}
}

// TestReviewBodyIsDeterministic checks repeated rendering of one plan is identical.
func TestReviewBodyIsDeterministic(t *testing.T) {
	input := ReviewInput{
		Plan: planFor(finding("COR-001", findings.SeverityHigh, 0.96, testFile, 82, 82),
			finding("TEST-001", findings.SeverityMedium, 0.90, testFile, 900, 900)),
		HeadSHA: testHeadSHA,
		JiraKey: "PAY-431",
		Checks:  selectionFixture().Analysis,
	}

	first := NewRenderer().ReviewBody(input)
	for i := 0; i < 5; i++ {
		if again := NewRenderer().ReviewBody(input); again != first {
			t.Fatal("ReviewBody() is not deterministic")
		}
	}
}

// TestRenderedOutputCarriesNoSecrets checks nothing sensitive can reach GitHub
// through the renderer. The input type simply has nowhere to put a credential, a
// prompt, or a patch; this pins that in a test.
func TestRenderedOutputCarriesNoSecrets(t *testing.T) {
	f := findingFixture()
	plan := planFor(f)

	rendered := NewRenderer().ReviewBody(ReviewInput{
		Plan:    plan,
		HeadSHA: testHeadSHA,
		JiraKey: "PAY-431",
		Checks:  selectionFixture().Analysis,
	}) + NewRenderer().InlineComment(f)

	for _, forbidden := range []string{
		"GITHUB_TOKEN", "JIRA_API_TOKEN", "ANTHROPIC", "Authorization", "Bearer",
		"ghp_", "<repository_data>", "RESPONSE FORMAT", "You are a disciplined senior code reviewer",
		"@@ -80,5 +80,7 @@",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("published text contains %q", forbidden)
		}
	}
}

// TestClamp checks the byte bound is respected and cuts on a rune boundary, so a
// truncated body is never invalid UTF-8.
func TestClamp(t *testing.T) {
	if got := clamp("short", 100); got != "short" {
		t.Errorf("clamp() = %q, want the input unchanged", got)
	}

	long := strings.Repeat("é", 100) // two bytes per rune
	got := clamp(long, 51)
	if len(got) > 51 {
		t.Errorf("clamp() returned %d bytes, want at most 51", len(got))
	}
	if !strings.HasSuffix(got, truncationNote) {
		t.Errorf("clamp() = %q, want it to end with the truncation note", got)
	}
	if !strings.HasPrefix(long, strings.TrimSuffix(got, truncationNote)) {
		t.Error("clamp() cut mid-rune")
	}
}
