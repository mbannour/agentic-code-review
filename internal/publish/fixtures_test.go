package publish

import (
	"github.com/your-company/agentic-code-review/internal/contextselect"
	"github.com/your-company/agentic-code-review/internal/findings"
	"github.com/your-company/agentic-code-review/internal/github"
	"github.com/your-company/agentic-code-review/internal/verification"
)

// retryPatch is the reference diff used throughout these tests.
//
// Its line numbering is worth stating explicitly, because every location assertion
// depends on it. The hunk header declares old lines 80-84 and new lines 80-86:
//
//	new (RIGHT): 80 context, 81-83 added, 84-86 context
//	old (LEFT):  80 context, 81 deleted, 82-84 context
const retryPatch = `@@ -80,5 +80,7 @@ func RetryPayment(p Payment) error {
 func RetryPayment(p Payment) error {
-	if p.Declined {
+	if p.Declined && !p.Permanent {
+		return nil
+	}
 	return retry(p)
 }
 `

// twoHunkPatch has a second hunk far from the first, so a test can tell "in the
// diff" from "in the file".
const twoHunkPatch = `@@ -10,3 +10,4 @@
 package payment
+import "errors"


@@ -200,3 +201,4 @@
 func audit() {
+	log()
 }
 `

// deletedFilePatch is a file removed entirely: every line exists on the left only.
const deletedFilePatch = `@@ -1,4 +0,0 @@
-package legacy
-
-func old() {}
-`

const (
	testFile     = "internal/payment/retry.go"
	testFileTest = "internal/payment/retry_test.go"
	testHeadSHA  = "abc123def4567890abc123def4567890abc123de"
)

// changedFilesFixture is the changed-file listing the mapper is normally built from.
func changedFilesFixture() []github.ChangedFile {
	return []github.ChangedFile{
		{Filename: testFile, Status: "modified", Patch: retryPatch},
		{Filename: testFileTest, Status: "added", Patch: twoHunkPatch},
		{Filename: "internal/payment/ledger.pdf", Status: "modified", Patch: ""},
		{Filename: "internal/legacy/old.go", Status: "removed", Patch: deletedFilePatch},
	}
}

// mapperFixture is the mapper over changedFilesFixture.
func mapperFixture() Mapper { return NewMapperFromChangedFiles(changedFilesFixture()) }

// findingFixture is a valid, inline-eligible finding on an added line.
func findingFixture() findings.Finding {
	return findings.Finding{
		ID:         "COR-001",
		Category:   findings.CategoryCorrectness,
		Severity:   findings.SeverityHigh,
		Confidence: 0.96,
		File:       testFile,
		StartLine:  81,
		EndLine:    81,
		Title:      "Permanent declines enter the retry path",
		Problem:    "The new retry branch treats permanent declines as retryable failures.",
		Impact:     "Declined authorizations can be submitted repeatedly.",
		Suggestion: "Return before entering the retry path for permanent declines.",
		Evidence: []findings.Evidence{
			{Type: findings.EvidenceCode, Source: testFile + ":81-83", Detail: "The decline branch reaches RetryPayment."},
			{Type: findings.EvidenceJira, Source: "PAY-431", Detail: "Permanent declines must not be retried."},
		},
	}
}

// finding builds a variant of the fixture finding.
func finding(id string, severity findings.Severity, confidence float64, file string, start, end int) findings.Finding {
	f := findingFixture()
	f.ID = id
	f.Severity = severity
	f.Confidence = confidence
	f.File = file
	f.StartLine = start
	f.EndLine = end
	f.Title = "Problem in " + id
	return f
}

// resultFixture wraps findings in a review result.
func resultFixture(fs ...findings.Finding) findings.ReviewResult {
	return findings.ReviewResult{Summary: "Found actionable issues.", Findings: fs}
}

// selectionFixture is the selected context the review ran against, used for the
// selection-based mapper and for check reporting.
func selectionFixture() contextselect.SelectedContext {
	return contextselect.SelectedContext{
		PullRequest: contextselect.PullRequestSummary{
			Owner:      "acme",
			Repository: "payments",
			Number:     123,
			HeadSHA:    testHeadSHA,
		},
		Files: []contextselect.SelectedFile{
			{Path: testFile, Status: "modified", Patch: retryPatch, Kind: contextselect.FileKindSource},
			{Path: testFileTest, Status: "added", Patch: twoHunkPatch, Kind: contextselect.FileKindTest},
		},
		Analysis: []contextselect.SelectedAnalysis{
			{Name: "go-test", Command: "go test ./...", Passed: true},
			{Name: "go-vet", Command: "go vet ./...", Passed: true},
		},
	}
}

// verifiedCandidates turns findings into policy candidates with a strong valid verdict.
//
// It is the "everything checked out" baseline: tests that are about severity, confidence,
// mappability, or limits use it so the verification gate is out of their way, and tests that
// are about verification supply their own outcomes.
func verifiedCandidates(fs ...findings.Finding) []Candidate {
	candidates := make([]Candidate, 0, len(fs))
	for _, f := range fs {
		candidates = append(candidates, verifiedCandidate(f, verification.VerdictValid, 0.95))
	}
	return candidates
}

// verifiedCandidate builds one candidate with an explicit verdict. A low-severity finding is
// marked not-required, matching what the verification stage actually does.
func verifiedCandidate(f findings.Finding, verdict verification.Verdict, confidence float64) Candidate {
	if !verification.RequiresVerification(f) {
		return Candidate{Finding: f, Verification: verification.Outcome{
			Finding:    f,
			Status:     verification.StatusNotRequired,
			SkipReason: verification.ReasonNotRequiredLowSeverity,
		}}
	}

	return Candidate{Finding: f, Verification: verification.Outcome{
		Finding: f,
		Status:  verification.StatusVerified,
		Result: verification.Result{
			FindingID:  f.ID,
			Verdict:    verdict,
			Confidence: confidence,
			Reason:     "checked the cited code and the surrounding guards",
		},
	}}
}

// failedCandidate builds a candidate whose verification did not complete.
func failedCandidate(f findings.Finding, why string) Candidate {
	return Candidate{Finding: f, Verification: verification.Outcome{
		Finding:       f,
		Status:        verification.StatusFailed,
		FailureReason: why,
	}}
}

// planFor builds a plan for findings that all verified valid, which is what the tests about
// rendering, publishing, and limits want.
func planFor(fs ...findings.Finding) Plan {
	return NewPolicy().BuildPlan(verifiedCandidates(fs...), mapperFixture(), testHeadSHA,
		"Found actionable issues.")
}
