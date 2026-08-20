package publish

import (
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/findings"
	"github.com/your-company/agentic-code-review/internal/github"
)

func historyFinding(category findings.Category, file, title string, line int) findings.Finding {
	return findings.Finding{
		ID: "X-1", Category: category, Severity: findings.SeverityHigh,
		File: file, StartLine: line, EndLine: line,
		Title: title, Problem: "problem", Impact: "impact", Suggestion: "suggestion",
		Evidence: []findings.Evidence{{Type: findings.EvidenceCode, Source: file, Detail: "detail"}},
	}
}

// A fingerprint has to survive the things that legitimately change between
// reviews, or every re-review reports everything again.
func TestFingerprintIgnoresLinesAndIDs(t *testing.T) {
	first := historyFinding(findings.CategoryCorrectness, "internal/pay/retry.go", "Retries permanent declines", 84)
	moved := first
	moved.ID = "COR-007"
	moved.StartLine, moved.EndLine = 120, 128
	moved.Severity = findings.SeverityBlocker
	moved.Problem = "reworded problem statement"

	if Fingerprint(first) != Fingerprint(moved) {
		t.Error("fingerprint changed when only the line, ID, severity, and prose moved")
	}
}

func TestFingerprintNormalizesTitleAndPath(t *testing.T) {
	base := historyFinding(findings.CategoryCorrectness, "internal/pay/retry.go", "Retries permanent declines", 84)

	same := historyFinding(findings.CategoryCorrectness, `internal\pay\retry.go`, "  retries   PERMANENT declines ", 84)
	if Fingerprint(base) != Fingerprint(same) {
		t.Error("fingerprint changed for the same finding written with different case, spacing, or separators")
	}
}

func TestFingerprintSeparatesDifferentFindings(t *testing.T) {
	base := historyFinding(findings.CategoryCorrectness, "a.go", "Retries permanent declines", 10)

	cases := map[string]findings.Finding{
		"different file":     historyFinding(findings.CategoryCorrectness, "b.go", "Retries permanent declines", 10),
		"different category": historyFinding(findings.CategorySecurity, "a.go", "Retries permanent declines", 10),
		"different title":    historyFinding(findings.CategoryCorrectness, "a.go", "Token written to the log", 10),
	}

	for name, other := range cases {
		if Fingerprint(base) == Fingerprint(other) {
			t.Errorf("%s produced the same fingerprint", name)
		}
	}
}

func TestFindingsMarkerRoundTrips(t *testing.T) {
	marker := FindingsMarker{Fingerprints: []string{
		Fingerprint(historyFinding(findings.CategoryCorrectness, "a.go", "One", 1)),
		Fingerprint(historyFinding(findings.CategorySecurity, "b.go", "Two", 2)),
	}}

	parsed, ok := ParseFindingsMarker("body text\n" + marker.Render() + "\nmore text")
	if !ok {
		t.Fatal("ParseFindingsMarker() did not find the marker it rendered")
	}
	if len(parsed.Fingerprints) != 2 {
		t.Fatalf("parsed %d fingerprints, want 2", len(parsed.Fingerprints))
	}
	// Rendering is sorted, so the same findings always produce the same marker.
	if marker.Render() != (FindingsMarker{Fingerprints: []string{
		parsed.Fingerprints[1], parsed.Fingerprints[0],
	}}).Render() {
		t.Error("marker rendering depends on input order")
	}
}

func TestFindingsMarkerRejectsMalformedContent(t *testing.T) {
	for _, body := range []string{
		"no marker here",
		"<!-- arc-findings:v2 abcdef123456 -->",
		"<!-- arc-findings abcdef123456 -->",
	} {
		if _, ok := ParseFindingsMarker(body); ok {
			t.Errorf("ParseFindingsMarker(%q) reported a marker", body)
		}
	}

	// A well-formed marker carrying junk yields the marker with the junk dropped,
	// rather than being mistaken for a review that reported nothing.
	parsed, ok := ParseFindingsMarker("<!-- arc-findings:v1 abcdef123456 -->")
	if !ok || len(parsed.Fingerprints) != 1 {
		t.Errorf("ParseFindingsMarker() = %+v, %t", parsed, ok)
	}
}

func TestFindingsMarkerIsBounded(t *testing.T) {
	var fingerprints []string
	for i := 0; i < MaxMarkedFingerprints*2; i++ {
		fingerprints = append(fingerprints,
			Fingerprint(historyFinding(findings.CategoryCorrectness, "a.go", "Title "+string(rune('a'+i%26))+string(rune('a'+i/26)), i+1)))
	}

	parsed, ok := ParseFindingsMarker(FindingsMarker{Fingerprints: fingerprints}.Render())
	if !ok {
		t.Fatal("marker did not parse")
	}
	if len(parsed.Fingerprints) > MaxMarkedFingerprints {
		t.Errorf("marker carries %d fingerprints, want at most %d", len(parsed.Fingerprints), MaxMarkedFingerprints)
	}
}

func reviewWithMarkers(head string, fingerprints []string, author string) github.ExistingReview {
	body := "## ARC Agentic Code Review\n\n" +
		Marker{HeadSHA: head}.Render() + "\n" +
		FindingsMarker{Fingerprints: fingerprints}.Render()
	return github.ExistingReview{Body: body, CommitID: head, AuthorLogin: author}
}

func TestHistoryFromReadsTheMostRecentPreviousReview(t *testing.T) {
	older := Fingerprint(historyFinding(findings.CategoryCorrectness, "a.go", "Older finding", 1))
	newer := Fingerprint(historyFinding(findings.CategorySecurity, "b.go", "Newer finding", 2))

	history := HistoryFrom([]github.ExistingReview{
		reviewWithMarkers("1111111111111111111111111111111111111111", []string{older}, "arc"),
		reviewWithMarkers("2222222222222222222222222222222222222222", []string{newer}, "arc"),
	}, "3333333333333333333333333333333333333333", "")

	if !history.Known {
		t.Fatal("history was not recognized")
	}
	if history.HeadSHA != "2222222222222222222222222222222222222222" {
		t.Errorf("head = %s, want the most recent review's", history.HeadSHA)
	}
	if history.Count() != 1 || history.Reported[0] != newer {
		t.Errorf("reported = %v, want the newest review's findings", history.Reported)
	}
}

// The review of the commit under review is this run's own work. Comparing against
// it would report every finding as a repeat of itself.
func TestHistoryIgnoresAReviewOfTheCurrentHead(t *testing.T) {
	head := "abcabcabcabcabcabcabcabcabcabcabcabcabca"
	fingerprint := Fingerprint(historyFinding(findings.CategoryCorrectness, "a.go", "Finding", 1))

	history := HistoryFrom([]github.ExistingReview{
		reviewWithMarkers(head, []string{fingerprint}, "arc"),
	}, head, "")

	if history.Known {
		t.Errorf("history = %+v, want it to ignore the current head's own review", history)
	}
}

// A review published before the findings marker existed cannot be compared with.
// Unknown must not be read as "the previous review found nothing", because that
// would make every finding look new — or, worse, look already reported.
func TestHistoryTreatsAMarkerlessReviewAsUnknown(t *testing.T) {
	body := "## ARC Agentic Code Review\n\n" + Marker{HeadSHA: "1111111111111111111111111111111111111111"}.Render()

	history := HistoryFrom([]github.ExistingReview{
		{Body: body, CommitID: "1111111111111111111111111111111111111111"},
	}, "2222222222222222222222222222222222222222", "")

	if history.Known {
		t.Errorf("history = %+v, want unknown", history)
	}
}

func TestHistoryIgnoresOtherAuthorsWhenKnown(t *testing.T) {
	fingerprint := Fingerprint(historyFinding(findings.CategoryCorrectness, "a.go", "Finding", 1))

	reviews := []github.ExistingReview{
		reviewWithMarkers("1111111111111111111111111111111111111111", []string{fingerprint}, "someone-else"),
	}

	if history := HistoryFrom(reviews, "2222222222222222222222222222222222222222", "arc-bot"); history.Known {
		t.Error("a review by another author was treated as ARC's own")
	}
	if history := HistoryFrom(reviews, "2222222222222222222222222222222222222222", ""); !history.Known {
		t.Error("with no expected author, the marked review should be usable")
	}
}

func TestDeltaCountsResolvedPersistingAndNew(t *testing.T) {
	persisting := historyFinding(findings.CategoryCorrectness, "a.go", "Still there", 1)
	fresh := historyFinding(findings.CategorySecurity, "b.go", "Brand new", 2)
	gone := historyFinding(findings.CategoryTesting, "c.go", "No longer reported", 3)

	plan := Plan{
		Inline:  []InlineFinding{{Finding: persisting}},
		Summary: []findings.Finding{fresh},
	}
	history := History{
		Known: true, HeadSHA: "1111111111111111111111111111111111111111",
		Reported: []string{Fingerprint(persisting), Fingerprint(gone)},
	}

	delta := DeltaFor(plan, history)
	if !delta.Known {
		t.Fatal("delta is not known")
	}
	if delta.Persisting != 1 || delta.New != 1 || delta.Resolved != 1 {
		t.Errorf("delta = %+v, want 1 persisting, 1 new, 1 resolved", delta)
	}
}

func TestDeltaIsUnknownWithoutHistory(t *testing.T) {
	plan := Plan{Summary: []findings.Finding{historyFinding(findings.CategoryCorrectness, "a.go", "T", 1)}}

	if delta := DeltaFor(plan, History{}); delta.Known {
		t.Errorf("delta = %+v, want unknown without a previous review", delta)
	}
}

// A suppressed finding was never shown to anyone, so recording it would suppress
// it a second time for a reason nobody ever saw.
func TestMarkerRecordsOnlyPublishedFindings(t *testing.T) {
	published := historyFinding(findings.CategoryCorrectness, "a.go", "Published", 1)
	suppressed := historyFinding(findings.CategorySecurity, "b.go", "Suppressed", 2)

	marker := MarkerFor(Plan{
		Inline:     []InlineFinding{{Finding: published}},
		Suppressed: []SuppressedFinding{{Finding: suppressed, Reason: "verifier_uncertain"}},
	})

	if len(marker.Fingerprints) != 1 || marker.Fingerprints[0] != Fingerprint(published) {
		t.Errorf("marker = %v, want only the published finding", marker.Fingerprints)
	}
}

// The demotion is placement only. A repeat finding still stands: being told once
// is not evidence that a problem was fixed.
func TestAlreadyReportedDemotesInlineToSummary(t *testing.T) {
	finding := historyFinding(findings.CategoryCorrectness, "a.go", "Repeat finding", 10)
	history := History{
		Known: true, HeadSHA: "1111111111111111111111111111111111111111",
		Reported: []string{Fingerprint(finding)},
	}

	inline := Decision{Finding: finding, Disposition: DispositionInline}
	demoted := demoteIfAlreadyReported(inline, history)

	if demoted.Disposition != DispositionSummary {
		t.Errorf("disposition = %s, want summary", demoted.Disposition)
	}
	if demoted.Reasons.Primary().Code != ReasonAlreadyReported {
		t.Errorf("reason = %s, want %s", demoted.Reasons.Primary().Code, ReasonAlreadyReported)
	}
	if !strings.Contains(demoted.Reasons.Primary().String(), "11111111") {
		t.Errorf("reason %q does not name the previous commit", demoted.Reasons.Primary().String())
	}
}

func TestAlreadyReportedNeverSuppresses(t *testing.T) {
	finding := historyFinding(findings.CategoryCorrectness, "a.go", "Repeat finding", 10)
	history := History{Known: true, HeadSHA: "1111", Reported: []string{Fingerprint(finding)}}

	for _, disposition := range []Disposition{DispositionSummary, DispositionSuppress} {
		decision := Decision{Finding: finding, Disposition: disposition}
		if got := demoteIfAlreadyReported(decision, history).Disposition; got != disposition {
			t.Errorf("disposition %s became %s; history only demotes inline", disposition, got)
		}
	}
}

// The markers identify the review. A body long enough to be clamped must not lose
// them, or the next run cannot recognize its own work and republishes.
func TestReviewBodyKeepsItsMarkersWhenClamped(t *testing.T) {
	var summary []findings.Finding
	for i := 0; i < 40; i++ {
		finding := historyFinding(findings.CategoryCorrectness, "a.go", "Finding number "+string(rune('a'+i%26)), i+1)
		finding.Problem = strings.Repeat("long problem statement ", 400)
		finding.Impact = strings.Repeat("long impact statement ", 400)
		summary = append(summary, finding)
	}

	head := "abcabcabcabcabcabcabcabcabcabcabcabcabca"
	body := NewRenderer().ReviewBody(ReviewInput{
		Plan:    Plan{Summary: summary, HeadSHA: head},
		HeadSHA: head,
	})

	if len(body) > MaxReviewBodyBytes {
		t.Errorf("body = %d bytes, want at most %d", len(body), MaxReviewBodyBytes)
	}
	if _, ok := ParseMarker(body); !ok {
		t.Error("the head marker was clamped away")
	}
	if _, ok := ParseFindingsMarker(body); !ok {
		t.Error("the findings marker was clamped away")
	}
}

func TestReviewBodyReportsTheDelta(t *testing.T) {
	head := "abcabcabcabcabcabcabcabcabcabcabcabcabca"
	body := NewRenderer().ReviewBody(ReviewInput{
		HeadSHA: head,
		Plan: Plan{
			HeadSHA: head,
			Summary: []findings.Finding{historyFinding(findings.CategoryCorrectness, "a.go", "Still there", 1)},
			Delta: Delta{
				Known: true, PreviousHeadSHA: "1111111111111111111111111111111111111111",
				Resolved: 3, Persisting: 1, New: 0,
			},
		},
	})

	for _, want := range []string{
		"### Since the previous review",
		"3 no longer reported",
		"1 still reported",
		"does not comment again on lines already discussed",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	// ARC knows what it stopped reporting, not that anything was fixed.
	if strings.Contains(body, "fixed") {
		t.Error("body claims findings were fixed; it can only know they are no longer reported")
	}
}

func TestReviewBodyOmitsTheDeltaWithoutHistory(t *testing.T) {
	body := NewRenderer().ReviewBody(ReviewInput{
		HeadSHA: "abc",
		Plan:    Plan{HeadSHA: "abc", Summary: []findings.Finding{historyFinding(findings.CategoryCorrectness, "a.go", "T", 1)}},
	})

	if strings.Contains(body, "Since the previous review") {
		t.Error("body reports a delta with no previous review")
	}
}
