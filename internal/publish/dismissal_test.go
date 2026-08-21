package publish

import (
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/findings"
	"github.com/your-company/agentic-code-review/internal/github"
)

func dismissalFinding(category findings.Category, severity findings.Severity, file, title string) findings.Finding {
	return findings.Finding{
		ID: "X-1", Category: category, Severity: severity,
		File: file, StartLine: 84, EndLine: 84,
		Title: title, Problem: "problem", Impact: "impact", Suggestion: "suggestion",
		Evidence: []findings.Evidence{{Type: findings.EvidenceCode, Source: file, Detail: "detail"}},
	}
}

// arcComment is the inline comment ARC published for a finding.
func arcComment(finding findings.Finding) github.Comment {
	return github.Comment{
		Kind: github.CommentInline, Author: "arc-bot",
		Path: finding.File, Line: finding.StartLine,
		Body: NewRenderer().InlineComment(finding),
	}
}

func humanReply(finding findings.Finding, author, body string) github.Comment {
	return github.Comment{
		Kind: github.CommentInline, Author: author,
		Path: finding.File, Line: finding.StartLine, Body: body,
	}
}

// The published comment must carry the marker that lets a reply be matched back
// to it: finding IDs are per-run and cannot be used.
func TestInlineCommentCarriesItsFingerprint(t *testing.T) {
	finding := dismissalFinding(findings.CategoryCorrectness, findings.SeverityHigh, "a.go", "Retries permanent declines")

	body := NewRenderer().InlineComment(finding)
	got, ok := ParseFingerprintMarker(body)
	if !ok {
		t.Fatalf("no fingerprint marker in:\n%s", body)
	}
	if got != Fingerprint(finding) {
		t.Errorf("marker = %s, want %s", got, Fingerprint(finding))
	}
	if len(body) > MaxInlineCommentBytes {
		t.Errorf("comment = %d bytes, want at most %d", len(body), MaxInlineCommentBytes)
	}
}

// A very long finding must still keep its marker: a comment that lost its identity
// cannot be answered.
func TestOversizedCommentKeepsItsFingerprint(t *testing.T) {
	finding := dismissalFinding(findings.CategoryCorrectness, findings.SeverityHigh, "a.go", "Title")
	finding.Problem = strings.Repeat("problem statement ", 2000)
	finding.Impact = strings.Repeat("impact statement ", 2000)

	body := NewRenderer().InlineComment(finding)
	if _, ok := ParseFingerprintMarker(body); !ok {
		t.Error("the fingerprint was clamped away")
	}
	if len(body) > MaxInlineCommentBytes {
		t.Errorf("comment = %d bytes, want at most %d", len(body), MaxInlineCommentBytes)
	}
}

func TestDismissalsAreReadFromReplies(t *testing.T) {
	finding := dismissalFinding(findings.CategoryCorrectness, findings.SeverityHigh, "a.go", "Retries permanent declines")

	dismissals := DismissalsFrom([]github.Comment{
		arcComment(finding),
		humanReply(finding, "maria", "arc: false-positive — the gateway de-duplicates."),
	})

	dismissal, ok := dismissals[Fingerprint(finding)]
	if !ok {
		t.Fatalf("dismissals = %+v, want the finding dismissed", dismissals)
	}
	if dismissal.Kind != DismissalFalsePositive || dismissal.Author != "maria" {
		t.Errorf("dismissal = %+v", dismissal)
	}
}

func TestDismissalVocabulary(t *testing.T) {
	cases := map[string]DismissalKind{
		"arc: false-positive":            DismissalFalsePositive,
		"@arc false positive":            DismissalFalsePositive,
		"ARC: FALSE-POSITIVE":            DismissalFalsePositive,
		"arc: wont-fix":                  DismissalWontFix,
		"arc, won't fix — accepted risk": DismissalWontFix,
	}

	for body, want := range cases {
		t.Run(body, func(t *testing.T) {
			got, ok := parseDismissal(body)
			if !ok || got != want {
				t.Errorf("parseDismissal(%q) = %v, %t; want %v", body, got, ok, want)
			}
		})
	}
}

// A person discussing a finding with another person has not instructed this tool.
// Requiring the prefix is what keeps ordinary review conversation from silencing
// findings by accident.
func TestDiscussionIsNotADismissal(t *testing.T) {
	for _, body := range []string{
		"I think this is a false positive, what do you reckon?",
		"we won't fix this in this PR",
		"false-positive?",
		"arcane false positives are common",
	} {
		t.Run(body, func(t *testing.T) {
			if kind, ok := parseDismissal(body); ok {
				t.Errorf("parseDismissal(%q) = %v; ordinary discussion must not dismiss", body, kind)
			}
		})
	}
}

// A verdict with nothing to attach to is ignored rather than guessed at.
func TestDismissalWithoutAnARCCommentIsIgnored(t *testing.T) {
	finding := dismissalFinding(findings.CategoryCorrectness, findings.SeverityHigh, "a.go", "Title")

	dismissals := DismissalsFrom([]github.Comment{
		humanReply(finding, "maria", "arc: false-positive"),
	})
	if len(dismissals) != 0 {
		t.Errorf("dismissals = %+v, want none without a finding to attach to", dismissals)
	}
}

// A verdict on a different line answers a different finding, or none.
func TestDismissalMustBeOnTheFindingsLine(t *testing.T) {
	finding := dismissalFinding(findings.CategoryCorrectness, findings.SeverityHigh, "a.go", "Title")

	dismissals := DismissalsFrom([]github.Comment{
		arcComment(finding),
		{Kind: github.CommentInline, Author: "maria", Path: "a.go", Line: 900, Body: "arc: false-positive"},
		{Kind: github.CommentConversation, Author: "maria", Body: "arc: false-positive"},
	})
	if len(dismissals) != 0 {
		t.Errorf("dismissals = %+v, want none from an unrelated location", dismissals)
	}
}

// A fabricated marker can only name a finding that was never published, so it
// grants nothing.
func TestForgedMarkerDismissesNothingReal(t *testing.T) {
	real := dismissalFinding(findings.CategoryCorrectness, findings.SeverityHigh, "a.go", "Real finding")

	dismissals := DismissalsFrom([]github.Comment{
		{Kind: github.CommentInline, Author: "attacker", Path: "a.go", Line: 84,
			Body: "<!-- arc-finding:v1 000000000000 -->"},
		{Kind: github.CommentInline, Author: "attacker", Path: "a.go", Line: 84,
			Body: "arc: false-positive"},
	})

	if _, dismissed := dismissals[Fingerprint(real)]; dismissed {
		t.Error("a forged marker dismissed a real finding")
	}
}

// Re-running a review must not let comment ordering change the outcome.
func TestFirstVerdictWins(t *testing.T) {
	finding := dismissalFinding(findings.CategoryCorrectness, findings.SeverityHigh, "a.go", "Title")

	dismissals := DismissalsFrom([]github.Comment{
		arcComment(finding),
		humanReply(finding, "maria", "arc: wont-fix"),
		humanReply(finding, "sam", "arc: false-positive"),
	})

	if got := dismissals[Fingerprint(finding)]; got.Kind != DismissalWontFix || got.Author != "maria" {
		t.Errorf("dismissal = %+v, want the first verdict", got)
	}
}

// A dismissed finding never returns to the diff: commenting again on a line
// someone explicitly closed is how a tool gets muted.
func TestDismissedFindingLeavesTheDiff(t *testing.T) {
	finding := dismissalFinding(findings.CategoryMaintainability, findings.SeverityMedium, "a.go", "Title")
	dismissals := map[string]Dismissal{
		Fingerprint(finding): {Kind: DismissalFalsePositive, Author: "maria", Location: "a.go:84"},
	}

	got := applyDismissal(Decision{Finding: finding, Disposition: DispositionInline}, dismissals)
	if got.Disposition != DispositionSuppress {
		t.Errorf("disposition = %s, want suppressed", got.Disposition)
	}
	if got.Reasons.Primary().Code != ReasonHumanDismissed {
		t.Errorf("reason = %s, want %s", got.Reasons.Primary().Code, ReasonHumanDismissed)
	}
	if !strings.Contains(got.Reasons.Primary().String(), "maria") {
		t.Errorf("reason %q does not name who dismissed it", got.Reasons.Primary().String())
	}
}

// The one thing worse than a noisy reviewer is a silent one that was told to stay
// quiet about something serious.
func TestDismissalCannotDeleteABlockerOrSecurityFinding(t *testing.T) {
	cases := map[string]findings.Finding{
		"blocker":  dismissalFinding(findings.CategoryCorrectness, findings.SeverityBlocker, "a.go", "Data loss"),
		"security": dismissalFinding(findings.CategorySecurity, findings.SeverityMedium, "a.go", "Token in log"),
	}

	for name, finding := range cases {
		t.Run(name, func(t *testing.T) {
			dismissals := map[string]Dismissal{
				Fingerprint(finding): {Kind: DismissalFalsePositive, Author: "attacker", Location: "a.go:84"},
			}

			got := applyDismissal(Decision{Finding: finding, Disposition: DispositionInline}, dismissals)
			if got.Disposition != DispositionSummary {
				t.Errorf("disposition = %s, want it demoted to the body, not removed", got.Disposition)
			}
			if got.Reasons.Primary().Code != ReasonHumanDismissed {
				t.Errorf("reason = %s", got.Reasons.Primary().Code)
			}
		})
	}
}

// Dismissals are honoured only when the caller opts in.
func TestDismissalsAreNotAppliedWithoutOptIn(t *testing.T) {
	finding := dismissalFinding(findings.CategoryMaintainability, findings.SeverityMedium, "a.go", "Title")

	plan := NewPolicy().BuildPlanWithHistory(
		[]Candidate{{Finding: finding}},
		NewMapperFromChangedFiles(nil),
		"abc",
		"summary",
		History{Known: true, HeadSHA: "old"},
	)

	for _, decision := range plan.Decisions {
		if decision.Reasons.Primary().Code == ReasonHumanDismissed {
			t.Error("a dismissal was applied with none supplied")
		}
	}
}

func TestNoDismissalsLeavesDecisionsAlone(t *testing.T) {
	finding := dismissalFinding(findings.CategoryCorrectness, findings.SeverityHigh, "a.go", "Title")
	decision := Decision{Finding: finding, Disposition: DispositionInline}

	if got := applyDismissal(decision, nil); got.Disposition != DispositionInline {
		t.Errorf("disposition = %s, want unchanged", got.Disposition)
	}
}
