package verification

import (
	"github.com/your-company/agentic-code-review/internal/contextselect"
	"github.com/your-company/agentic-code-review/internal/findings"
)

const (
	testFile      = "internal/payment/retry.go"
	testOtherFile = "internal/payment/decline.go"
)

// retryPatch has new lines 80-86, with 81-83 added.
const retryPatch = `@@ -80,5 +80,7 @@ func RetryPayment(p Payment) error {
 func RetryPayment(p Payment) error {
-	if p.Declined {
+	if p.Declined && !p.Permanent {
+		return nil
+	}
 	return retry(p)
 }
 `

// declinePatch is a second changed file a finding's evidence may cite.
const declinePatch = `@@ -10,3 +10,5 @@
 func IsPermanent(code string) bool {
+	return code == "51"
+}
 `

// findingFixture is a valid, inline-eligible correctness finding.
func findingFixture() findings.Finding {
	return findings.Finding{
		ID:         "COR-001",
		Category:   findings.CategoryCorrectness,
		Severity:   findings.SeverityHigh,
		Confidence: 0.94,
		File:       testFile,
		StartLine:  81,
		EndLine:    83,
		Title:      "Permanent declines enter the retry path",
		Problem:    "The new branch treats a permanent decline as a retryable failure.",
		Impact:     "A declined card can be submitted repeatedly.",
		Suggestion: "Return before entering the retry path for permanent declines.",
		Evidence: []findings.Evidence{
			{Type: findings.EvidenceCode, Source: testFile + ":81-83", Detail: "The decline branch reaches RetryPayment."},
		},
	}
}

// finding builds a variant of the fixture.
func finding(id string, severity findings.Severity, confidence float64) findings.Finding {
	f := findingFixture()
	f.ID = id
	f.Severity = severity
	f.Confidence = confidence
	return f
}

// selectionFixture is the selected context a verification draws its evidence from.
func selectionFixture() contextselect.SelectedContext {
	return contextselect.SelectedContext{
		PullRequest: contextselect.PullRequestSummary{
			Owner: "acme", Repository: "payments", Number: 123, HeadSHA: "abc123",
		},
		Ticket: &contextselect.TicketSummary{
			Key:         "PAY-431",
			Summary:     "Stop retrying permanently declined cards",
			Description: "Permanent declines must not be retried. Soft declines may be retried up to three times.",
			Status:      "In Progress",
		},
		Files: []contextselect.SelectedFile{
			{
				Path: testFile, Status: "modified", Patch: retryPatch,
				Kind: contextselect.FileKindSource, Importance: contextselect.ImportanceHigh,
			},
			{
				Path: testOtherFile, Status: "modified", Patch: declinePatch,
				Kind: contextselect.FileKindSource, Importance: contextselect.ImportanceHigh,
			},
		},
		Rules: []contextselect.SelectedRule{
			{Path: "AGENTS.md", Content: "Never retry a payment authorization that was permanently declined."},
			{Path: "CONTRIBUTING.md", Content: "Run gofmt before pushing."},
		},
		Analysis: []contextselect.SelectedAnalysis{
			{Name: "go-test", Command: "go test ./...", Passed: true},
			{
				Name: "go-vet", Command: "go vet ./...", Passed: false, ExitCode: 1,
				Output: "internal/payment/retry.go:84:2: unreachable code",
			},
		},
		Profile: contextselect.TechnologyProfile{
			Languages:    []string{contextselect.LanguageGo},
			BuildSystems: []string{contextselect.BuildSystemGo},
			Libraries:    []string{"sql"},
		},
	}
}

// resultFor wraps findings in a review result.
func resultFor(fs ...findings.Finding) findings.ReviewResult {
	return findings.ReviewResult{Summary: "Found actionable issues.", Findings: fs}
}

// verifiedOutcome builds a verified outcome with the given verdict.
func verifiedOutcome(f findings.Finding, verdict Verdict, confidence float64) Outcome {
	return Outcome{
		Finding: f,
		Status:  StatusVerified,
		Result: Result{
			FindingID:  f.ID,
			Verdict:    verdict,
			Confidence: confidence,
			Reason:     "checked the evidence",
		},
	}
}
