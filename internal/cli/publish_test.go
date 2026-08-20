package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/contextselect"
	"github.com/your-company/agentic-code-review/internal/findings"
	"github.com/your-company/agentic-code-review/internal/github"
	"github.com/your-company/agentic-code-review/internal/publish"
	"github.com/your-company/agentic-code-review/internal/verification"
)

// fakeGitHub is a stand-in for the GitHub API. Its created slice is what proves
// whether the CLI wrote anything.
type fakeGitHub struct {
	currentSHA string
	reviews    []github.ExistingReview
	createErr  error

	created      []github.CreateReviewRequest
	createdOwner string
	createdRepo  string
	createdPull  int
}

func (f *fakeGitHub) GetPullRequest(_ context.Context, pr github.PullRequest) (github.PullRequestDetails, error) {
	return github.PullRequestDetails{Number: pr.Number, HeadSHA: f.currentSHA}, nil
}

func (f *fakeGitHub) ListPullRequestReviews(_ context.Context, _ github.PullRequest) ([]github.ExistingReview, error) {
	return f.reviews, nil
}

func (f *fakeGitHub) CreatePullRequestReview(
	_ context.Context,
	owner string,
	repo string,
	pullNumber int,
	req github.CreateReviewRequest,
) (github.PublishedReview, error) {
	f.createdOwner = owner
	f.createdRepo = repo
	f.createdPull = pullNumber
	f.created = append(f.created, req)
	if f.createErr != nil {
		return github.PublishedReview{}, f.createErr
	}
	return github.PublishedReview{ID: 42, State: "COMMENTED", HTMLURL: "https://github.test/review"}, nil
}

const (
	publishTestSHA  = "abc123def4567890abc123def4567890abc123de"
	publishTestFile = "internal/payment/retry.go"
)

// publishTestPatch has new lines 80-86, with 81-83 added.
const publishTestPatch = `@@ -80,5 +80,7 @@
 func RetryPayment(p Payment) error {
-	if p.Declined {
+	if p.Declined && !p.Permanent {
+		return nil
+	}
 	return retry(p)
 }
 `

// publishFinding is one inline-eligible finding on an added line.
func publishFinding(id string, severity findings.Severity, confidence float64, line int) findings.Finding {
	return findings.Finding{
		ID:         id,
		Category:   findings.CategoryCorrectness,
		Severity:   severity,
		Confidence: confidence,
		File:       publishTestFile,
		StartLine:  line,
		EndLine:    line,
		Title:      "Permanent declines enter the retry path",
		Problem:    "The new retry branch treats permanent declines as retryable failures.",
		Impact:     "Declined authorizations can be submitted repeatedly.",
		Suggestion: "Return before entering the retry path for permanent declines.",
		Evidence: []findings.Evidence{
			{Type: findings.EvidenceCode, Source: publishTestFile + ":82", Detail: "The decline branch reaches RetryPayment."},
			{Type: findings.EvidenceJira, Source: "PAY-431", Detail: "Permanent declines must not be retried."},
		},
	}
}

// publishStage builds the stage a publication runs from.
func publishStage(api publish.API) claudeStage {
	return claudeStage{
		api:         api,
		pullRequest: github.PullRequest{Owner: "acme", Repo: "payments", Number: 123},
		reviewedSHA: publishTestSHA,
		changedFiles: []github.ChangedFile{
			{Filename: publishTestFile, Status: "modified", Patch: publishTestPatch},
		},
		selected: contextselect.SelectedContext{
			Analysis: []contextselect.SelectedAnalysis{
				{Name: "go-test", Command: "go test ./...", Passed: true},
				{Name: "go-vet", Command: "go vet ./...", Passed: true},
			},
		},
		publish: true,
		jiraKey: "PAY-431",
	}
}

// planFor builds the plan the CLI would build for these findings, with each one verified
// valid — the same shape the real pipeline hands to policy.
func planFor(stage claudeStage, fs ...findings.Finding) publish.Plan {
	candidates := make([]publish.Candidate, 0, len(fs))
	for _, f := range fs {
		candidates = append(candidates, publish.Candidate{
			Finding:      f,
			Verification: verifiedOutcomeFor(f),
		})
	}

	return publish.NewPolicy().BuildPlan(
		candidates,
		publish.NewMapperFromChangedFiles(stage.changedFiles),
		stage.reviewedSHA,
		"Found actionable issues.",
	)
}

// verifiedOutcomeFor builds the verification outcome a finding would have arrived with: a
// confident valid verdict, or not-required for a low-severity finding.
func verifiedOutcomeFor(f findings.Finding) verification.Outcome {
	if !verification.RequiresVerification(f) {
		return verification.Outcome{
			Finding:    f,
			Status:     verification.StatusNotRequired,
			SkipReason: verification.ReasonNotRequiredLowSeverity,
		}
	}

	return verification.Outcome{
		Finding: f,
		Status:  verification.StatusVerified,
		Result: verification.Result{
			FindingID:  f.ID,
			Verdict:    verification.VerdictValid,
			Confidence: 0.95,
			Reason:     "checked the cited code",
		},
	}
}

// TestPublishRequiresClaude checks the flag dependency. Publishing describes a review;
// without --claude there is no review to describe.
func TestPublishRequiresClaude(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")

	err := Run([]string{"review", "--pr", testPR, "--publish"})
	if err == nil {
		t.Fatal("Run() = nil error, want the flag-dependency error")
	}
	if !strings.Contains(err.Error(), "--publish requires --claude") {
		t.Errorf("error = %q, want it to explain that --publish requires --claude", err)
	}
}

// TestPublishFlagCheckHappensBeforeAnyNetworkCall checks the combination is refused
// before a token is even read, so a misuse cannot reach GitHub.
func TestPublishFlagCheckHappensBeforeAnyNetworkCall(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")

	err := Run([]string{"review", "--pr", testPR, "--publish"})
	if err == nil || strings.Contains(err.Error(), github.TokenEnvVar) {
		t.Errorf("error = %v, want the flag error rather than a token error", err)
	}
}

// TestDryRunWritesNothing checks the default: a review without --publish inspects the
// plan and makes no GitHub write at all.
func TestDryRunWritesNothing(t *testing.T) {
	api := &fakeGitHub{currentSHA: publishTestSHA}

	stage := publishStage(api)
	stage.publish = false
	plan := planFor(stage, publishFinding("COR-001", findings.SeverityHigh, 0.96, 82))

	out := captureStdout(t, func() {
		printPlan(plan)
		printPublicationSkipped("--publish not provided")
	})

	if len(api.created) != 0 {
		t.Fatal("a dry run created a review")
	}
	for _, want := range []string{
		"Publication Plan",
		"Inline:      1",
		"Summary:     0",
		"Suppressed:  0",
		"GitHub publication:",
		"SKIPPED (--publish not provided)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output does not contain %q\n---\n%s", want, out)
		}
	}
}

// TestDryRunShowsWhereEveryFindingWent checks the plan output accounts for each
// finding: which line it was attached to, why another went to the body, and why a third was
// withheld. Every finding is accounted for somewhere.
func TestDryRunShowsWhereEveryFindingWent(t *testing.T) {
	stage := publishStage(&fakeGitHub{currentSHA: publishTestSHA})
	stage.publish = false

	// Line 900 is outside the diff, so this one is reported in the body rather than inline.
	unmappable := publishFinding("ARCH-001", findings.SeverityHigh, 0.95, 900)
	unmappable.Category = findings.CategoryArchitecture

	plan := planFor(stage,
		publishFinding("COR-001", findings.SeverityHigh, 0.96, 82),
		unmappable,
		publishFinding("TEST-001", findings.SeverityMedium, 0.70, 83),
	)

	out := captureStdout(t, func() { printPlan(plan) })

	for _, want := range []string{
		"Inline comments:",
		"COR-001", publishTestFile + ":82 RIGHT",
		"Summary findings:",
		"ARCH-001", string(publish.ReasonNotDiffMappable),
		"Suppressed:",
		"TEST-001", string(publish.ReasonLowConfidence),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output does not contain %q\n---\n%s", want, out)
		}
	}
}

// TestPublishCreatesReview checks the publishing path end to end at the CLI level.
func TestPublishCreatesReview(t *testing.T) {
	api := &fakeGitHub{currentSHA: publishTestSHA}
	stage := publishStage(api)
	plan := planFor(stage, publishFinding("COR-001", findings.SeverityHigh, 0.96, 82))

	out := captureStdout(t, func() {
		if err := runPublication(context.Background(), stage, plan); err != nil {
			t.Errorf("runPublication() = %v", err)
		}
	})

	if len(api.created) != 1 {
		t.Fatalf("created %d reviews, want 1", len(api.created))
	}
	if api.createdOwner != "acme" || api.createdRepo != "payments" || api.createdPull != 123 {
		t.Errorf("review addressed to %s/%s#%d, want acme/payments#123",
			api.createdOwner, api.createdRepo, api.createdPull)
	}
	if api.created[0].CommitID != publishTestSHA {
		t.Errorf("commit_id = %q, want the reviewed head", api.created[0].CommitID)
	}
	if api.created[0].Event != github.EventComment {
		t.Errorf("event = %q, want COMMENT", api.created[0].Event)
	}
	for _, want := range []string{
		"GitHub Publication",
		"Reviewed head: " + publishTestSHA,
		"Current head:  " + publishTestSHA,
		"Published successfully.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("publish output does not contain %q\n---\n%s", want, out)
		}
	}
}

// TestPublishZeroFindingsWritesNothing checks a clean review reports success and posts
// nothing.
func TestPublishZeroFindingsWritesNothing(t *testing.T) {
	api := &fakeGitHub{currentSHA: publishTestSHA}
	stage := publishStage(api)
	plan := planFor(stage)

	out := captureStdout(t, func() {
		if err := runPublication(context.Background(), stage, plan); err != nil {
			t.Errorf("runPublication() = %v; zero findings is a successful review", err)
		}
	})

	if len(api.created) != 0 {
		t.Fatal("an empty review was published")
	}
	for _, want := range []string{"No actionable findings.", "GitHub publication skipped."} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q\n---\n%s", want, out)
		}
	}
}

// TestPublishStaleHeadWritesNothing checks the abort path: the output names both
// commits, tells the user to rerun, and nothing is posted.
func TestPublishStaleHeadWritesNothing(t *testing.T) {
	api := &fakeGitHub{currentSHA: "def4560000000000000000000000000000000000"}
	stage := publishStage(api)
	plan := planFor(stage, publishFinding("COR-001", findings.SeverityHigh, 0.96, 82))

	var err error
	out := captureStdout(t, func() {
		err = runPublication(context.Background(), stage, plan)
	})

	if !errors.Is(err, publish.ErrStaleHead) {
		t.Fatalf("runPublication() = %v, want a stale-head error", err)
	}
	if len(api.created) != 0 {
		t.Fatal("a review was published against a changed head")
	}
	for _, want := range []string{
		"ABORTED",
		publishTestSHA,
		"def4560000000000000000000000000000000000",
		"PR changed while ARC was reviewing it.",
		"Rerun the review before publishing.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stale output does not contain %q\n---\n%s", want, out)
		}
	}
}

// TestPublishDuplicateWritesNothing checks a second run for the same commit reports the
// earlier review and posts nothing.
func TestPublishDuplicateWritesNothing(t *testing.T) {
	api := &fakeGitHub{
		currentSHA: publishTestSHA,
		reviews: []github.ExistingReview{{
			ID:       7,
			Body:     "## ARC Agentic Code Review\n" + publish.Marker{HeadSHA: publishTestSHA}.Render(),
			CommitID: publishTestSHA,
			HTMLURL:  "https://github.test/earlier",
		}},
	}
	stage := publishStage(api)
	plan := planFor(stage, publishFinding("COR-001", findings.SeverityHigh, 0.96, 82))

	out := captureStdout(t, func() {
		if err := runPublication(context.Background(), stage, plan); err != nil {
			t.Errorf("runPublication() = %v; a duplicate is not a failure", err)
		}
	})

	if len(api.created) != 0 {
		t.Fatal("a duplicate review was published")
	}
	if !strings.Contains(out, "ARC review already published for head "+publishTestSHA) {
		t.Errorf("output does not report the duplicate\n---\n%s", out)
	}
}

// TestPublishAPIErrorPropagatesSafely checks a GitHub failure surfaces as an error
// carrying no credential.
func TestPublishAPIErrorPropagatesSafely(t *testing.T) {
	api := &fakeGitHub{
		currentSHA: publishTestSHA,
		createErr:  &github.APIError{StatusCode: 403, Message: "Resource not accessible by personal access token"},
	}
	stage := publishStage(api)
	plan := planFor(stage, publishFinding("COR-001", findings.SeverityHigh, 0.96, 82))

	err := runPublication(context.Background(), stage, plan)
	if err == nil {
		t.Fatal("runPublication() = nil error, want the API failure")
	}
	if !errors.Is(err, github.ErrForbidden) {
		t.Errorf("errors.Is(err, github.ErrForbidden) = false; err = %v", err)
	}
	for _, forbidden := range []string{"ghp_", "Bearer", "GITHUB_TOKEN="} {
		if strings.Contains(err.Error(), forbidden) {
			t.Errorf("error %q leaks %q", err, forbidden)
		}
	}
}

// TestPublish422ErrorNamesLocations checks a rejected location is reported with the
// finding and line, and that no other line is attempted.
func TestPublish422ErrorNamesLocations(t *testing.T) {
	api := &fakeGitHub{
		currentSHA: publishTestSHA,
		createErr:  &github.APIError{StatusCode: 422, Message: "line must be part of the diff"},
	}
	stage := publishStage(api)
	plan := planFor(stage, publishFinding("COR-001", findings.SeverityHigh, 0.96, 82))

	err := runPublication(context.Background(), stage, plan)
	if !errors.Is(err, publish.ErrInvalidLocation) {
		t.Fatalf("runPublication() = %v, want an invalid-location error", err)
	}
	for _, want := range []string{"COR-001", publishTestFile + ":82"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if len(api.created) != 1 {
		t.Errorf("CreatePullRequestReview called %d times; a rejected location must not be retried",
			len(api.created))
	}
}

// TestUsageDocumentsPublish checks the flag is discoverable, together with its
// dependency on --claude.
func TestUsageDocumentsPublish(t *testing.T) {
	out := captureStdout(t, printUsage)

	for _, want := range []string{"--publish", "requires --claude"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage does not mention %q\n---\n%s", want, out)
		}
	}
}

// TestPublishOutputCarriesNoSecrets checks the terminal output of a publication shows
// no credential and no review body.
func TestPublishOutputCarriesNoSecrets(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_should_never_be_printed")

	api := &fakeGitHub{currentSHA: publishTestSHA}
	stage := publishStage(api)
	plan := planFor(stage, publishFinding("COR-001", findings.SeverityHigh, 0.96, 82))

	out := captureStdout(t, func() {
		printPlan(plan)
		if err := runPublication(context.Background(), stage, plan); err != nil {
			t.Errorf("runPublication() = %v", err)
		}
	})

	for _, forbidden := range []string{"ghp_", "Bearer", "arc-review:v1", "@@ -80"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("terminal output contains %q\n---\n%s", forbidden, out)
		}
	}
}

// TestPublishSummaryOnlyReview is the case observed on a real pull request: one
// low-severity finding with strong evidence.
//
// Severity policy comes before confidence, so it earns no inline comment — and a review
// with a body and no comments is still a review. Skipping it would silently discard the
// only finding of the run.
func TestPublishSummaryOnlyReview(t *testing.T) {
	api := &fakeGitHub{currentSHA: publishTestSHA}
	stage := publishStage(api)
	stage.selected.Analysis = nil // no local checkout, as in that run

	plan := planFor(stage, publishFinding("MAINT-001", findings.SeverityLow, 0.82, 82))

	if len(plan.Inline) != 0 || len(plan.Summary) != 1 {
		t.Fatalf("plan = %d inline, %d summary; want 0 and 1 for a low-severity finding",
			len(plan.Inline), len(plan.Summary))
	}

	out := captureStdout(t, func() {
		if err := runPublication(context.Background(), stage, plan); err != nil {
			t.Errorf("runPublication() = %v; a summary-only review is publishable", err)
		}
	})

	if len(api.created) != 1 {
		t.Fatalf("created %d reviews, want 1; a summary-only plan must still publish", len(api.created))
	}

	sent := api.created[0]
	if len(sent.Comments) != 0 {
		t.Errorf("review carries %d inline comments, want none", len(sent.Comments))
	}
	if sent.Event != github.EventComment {
		t.Errorf("event = %q, want COMMENT", sent.Event)
	}
	for _, want := range []string{
		"### Inline findings",
		"None.",
		"### Additional findings",
		"MAINT-001",
		"Confidence: 82%",
		"Not available — local repository was not provided.",
	} {
		if !strings.Contains(sent.Body, want) {
			t.Errorf("review body does not contain %q\n---\n%s", want, sent.Body)
		}
	}
	if !strings.Contains(out, "Published successfully.") {
		t.Errorf("output does not report success\n---\n%s", out)
	}
}

// TestDryRunSummaryOnlyOutput checks the dry-run counts for that same case.
func TestDryRunSummaryOnlyOutput(t *testing.T) {
	stage := publishStage(&fakeGitHub{currentSHA: publishTestSHA})
	plan := planFor(stage, publishFinding("MAINT-001", findings.SeverityLow, 0.82, 82))

	out := captureStdout(t, func() {
		printPlan(plan)
		printPublicationSkipped("--publish not provided")
	})

	for _, want := range []string{
		"Inline:      0",
		"Summary:     1",
		"Suppressed:  0",
		string(publish.ReasonLowSeverity),
		"SKIPPED (--publish not provided)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output does not contain %q\n---\n%s", want, out)
		}
	}
}
