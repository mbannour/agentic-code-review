package cli

import (
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/findings"
	"github.com/your-company/agentic-code-review/internal/publish"
	"github.com/your-company/agentic-code-review/internal/verification"
)

// verificationReportFixture is a report with one of each outcome.
func verificationReportFixture() verification.Report {
	valid := publishFinding("COR-001", findings.SeverityHigh, 0.94, 82)
	uncertain := publishFinding("TEST-001", findings.SeverityMedium, 0.87, 83)
	invalid := publishFinding("ARCH-001", findings.SeverityMedium, 0.83, 84)
	low := publishFinding("MAINT-001", findings.SeverityLow, 0.82, 85)

	outcomes := []verification.Outcome{
		{
			Finding: valid, Status: verification.StatusVerified, ContextBytes: 7000,
			Result: verification.Result{
				FindingID: "COR-001", Verdict: verification.VerdictValid, Confidence: 0.97,
				Reason: "The changed branch reaches RetryPayment and no surrounding guard prevents the retry.",
			},
		},
		{
			Finding: uncertain, Status: verification.StatusVerified, ContextBytes: 7000,
			Result: verification.Result{
				FindingID: "TEST-001", Verdict: verification.VerdictUncertain, Confidence: 0.71,
				Reason: "Existing integration coverage may exercise the path, but available context is insufficient.",
			},
		},
		{
			Finding: invalid, Status: verification.StatusVerified, ContextBytes: 7000,
			Result: verification.Result{
				FindingID: "ARCH-001", Verdict: verification.VerdictInvalid, Confidence: 0.95,
				Reason: "The alleged duplicate initialization is prevented by the existing lazy value.",
			},
		},
		{
			Finding: low, Status: verification.StatusNotRequired,
			SkipReason: verification.ReasonNotRequiredLowSeverity,
		},
	}

	report := verification.Report{Outcomes: outcomes}
	for _, outcome := range outcomes {
		report.Stats.Candidates++
		report.Stats.ContextBytes += outcome.ContextBytes
		switch outcome.Status {
		case verification.StatusVerified:
			report.Stats.Verified++
			switch outcome.Result.Verdict {
			case verification.VerdictValid:
				report.Stats.Valid++
			case verification.VerdictInvalid:
				report.Stats.Invalid++
			case verification.VerdictUncertain:
				report.Stats.Uncertain++
			}
		case verification.StatusNotRequired:
			report.Stats.Skipped++
		}
	}
	return report
}

// TestPrintVerification checks both sides are shown for each candidate — what the reviewer
// claimed and what the verifier concluded — plus the tally.
func TestPrintVerification(t *testing.T) {
	out := captureStdout(t, func() { printVerification(verificationReportFixture()) })

	// Long reasons are wrapped for the terminal, so they are matched against a
	// whitespace-collapsed copy rather than line by line.
	flat := strings.Join(strings.Fields(out), " ")
	for _, want := range []string{
		"no surrounding guard prevents the retry",
		"prevented by the existing lazy value",
		"low severity is summary-only",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("output does not contain %q\n---\n%s", want, out)
		}
	}

	for _, want := range []string{
		"Verification",
		"COR-001",
		"Reviewer:  HIGH / 94%",
		"Verdict:   VALID / 97%",
		"TEST-001",
		"Reviewer:  MEDIUM / 87%",
		"Verdict:   UNCERTAIN / 71%",
		"ARCH-001",
		"Verdict:   INVALID / 95%",
		"MAINT-001",
		"Verdict:   SKIPPED",
		"Verification summary:",
		"Valid:     1",
		"Invalid:   1",
		"Uncertain: 1",
		"Skipped:   1",
		"Context:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q\n---\n%s", want, out)
		}
	}
}

// TestPrintVerificationFailure checks a fail-closed outcome is reported rather than hidden.
func TestPrintVerificationFailure(t *testing.T) {
	report := verification.Report{
		Outcomes: []verification.Outcome{{
			Finding:       publishFinding("SEC-001", findings.SeverityBlocker, 0.99, 82),
			Status:        verification.StatusFailed,
			FailureReason: "Claude Code timed out after 5m0s",
		}},
		Stats: verification.Stats{Candidates: 1, Failed: 1},
	}

	out := captureStdout(t, func() { printVerification(report) })

	for _, want := range []string{"Verdict:   FAILED", "timed out", "Failed:    1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q\n---\n%s", want, out)
		}
	}
}

// TestPrintVerificationWithoutFindings checks a clean review says so.
func TestPrintVerificationWithoutFindings(t *testing.T) {
	out := captureStdout(t, func() { printVerification(verification.Report{}) })

	if !strings.Contains(out, "no findings to verify") {
		t.Errorf("output does not state there was nothing to verify\n---\n%s", out)
	}
}

// TestVerificationFeedsThePublicationPlan is the integration this step exists for: only what
// survived verification reaches the publication policy, and what did not is recorded as
// suppressed rather than lost.
func TestVerificationFeedsThePublicationPlan(t *testing.T) {
	report := verificationReportFixture()

	stage := publishStage(&fakeGitHub{currentSHA: publishTestSHA})
	mapper := publish.NewMapperFromChangedFiles(stage.changedFiles)

	plan := publish.NewPolicy().BuildPlan(
		publish.CandidatesFrom(report), mapper, stage.reviewedSHA, "s")

	// COR-001 was verified valid and maps to a changed line.
	if len(plan.Inline) != 1 || plan.Inline[0].Finding.ID != "COR-001" {
		t.Fatalf("Inline = %+v, want only the verified finding", plan.Inline)
	}
	// MAINT-001 was never verified and stays summary-only, exactly as before this step.
	if len(plan.Summary) != 1 || plan.Summary[0].ID != "MAINT-001" {
		t.Fatalf("Summary = %+v, want only the low finding", plan.Summary)
	}
	// The invalid and uncertain findings reach GitHub in no form at all.
	if len(plan.Suppressed) != 2 {
		t.Fatalf("Suppressed = %+v, want the invalid and uncertain findings", plan.Suppressed)
	}
	for _, item := range plan.Suppressed {
		if item.Reason == "" {
			t.Errorf("%s was suppressed with no reason", item.Finding.ID)
		}
	}
	if plan.TotalFindings() != len(report.Outcomes) {
		t.Errorf("plan accounts for %d findings, want all %d", plan.TotalFindings(), len(report.Outcomes))
	}

	// And nothing suppressed appears in the published body.
	body := publish.NewRenderer().ReviewBody(publish.ReviewInput{Plan: plan, HeadSHA: stage.reviewedSHA})
	for _, unwanted := range []string{"ARCH-001", "TEST-001"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the review body contains %q, which verification suppressed", unwanted)
		}
	}
	if !strings.Contains(body, "MAINT-001") {
		t.Error("the review body dropped the summary-only finding")
	}
}

// TestVerificationSuppressionKeepsFindingsIntact checks a suppressed finding is preserved for
// local reporting rather than rewritten or discarded, and that its verdict is still available.
func TestVerificationSuppressionKeepsFindingsIntact(t *testing.T) {
	report := verificationReportFixture()
	stage := publishStage(&fakeGitHub{currentSHA: publishTestSHA})

	plan := publish.NewPolicy().BuildPlan(publish.CandidatesFrom(report),
		publish.NewMapperFromChangedFiles(stage.changedFiles), stage.reviewedSHA, "s")

	for _, item := range plan.Suppressed {
		if item.Finding.Title == "" || item.Finding.Problem == "" {
			t.Errorf("%s was suppressed with its content stripped", item.Finding.ID)
		}
		if item.Reason == "" {
			t.Errorf("%s was suppressed with no reason", item.Finding.ID)
		}
	}

	for _, decision := range plan.Decisions {
		if decision.VerificationStatus == verification.StatusVerified && decision.Verification == nil {
			t.Errorf("%s lost its verification result", decision.Finding.ID)
		}
	}
}
