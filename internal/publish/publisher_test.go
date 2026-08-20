package publish

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/findings"
	"github.com/your-company/agentic-code-review/internal/github"
)

// fakeAPI is a recording stand-in for the GitHub client. Its point is not only to
// avoid the network: the created field is what proves no write happened.
type fakeAPI struct {
	currentSHA string
	reviews    []github.ExistingReview

	getErr    error
	listErr   error
	createErr error

	getCalls     int
	listCalls    int
	created      []github.CreateReviewRequest
	createdOwner string
	createdRepo  string
	createdPull  int
	publishedID  int64
}

func (f *fakeAPI) GetPullRequest(_ context.Context, pr github.PullRequest) (github.PullRequestDetails, error) {
	f.getCalls++
	if f.getErr != nil {
		return github.PullRequestDetails{}, f.getErr
	}
	return github.PullRequestDetails{Number: pr.Number, HeadSHA: f.currentSHA}, nil
}

func (f *fakeAPI) ListPullRequestReviews(_ context.Context, _ github.PullRequest) ([]github.ExistingReview, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.reviews, nil
}

func (f *fakeAPI) CreatePullRequestReview(
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
	f.publishedID++
	return github.PublishedReview{ID: f.publishedID, State: "COMMENTED", HTMLURL: "https://github.test/review"}, nil
}

// testPullRequest is the base repository a review is always published against.
var testPullRequest = github.PullRequest{Owner: "acme", Repo: "payments", Number: 123}

// publishRequest builds a request for the given findings.
func publishRequest(fs ...findings.Finding) Request {
	return Request{
		PullRequest:     testPullRequest,
		Plan:            planFor(fs...),
		ReviewedHeadSHA: testHeadSHA,
		JiraKey:         "PAY-431",
		Checks:          selectionFixture().Analysis,
	}
}

// TestPublishCreatesOneReview checks the happy path: one review, pinned to the
// reviewed commit, carrying every inline comment.
func TestPublishCreatesOneReview(t *testing.T) {
	api := &fakeAPI{currentSHA: testHeadSHA}

	outcome, err := NewPublisher(api).Publish(context.Background(), publishRequest(
		finding("COR-001", findings.SeverityHigh, 0.96, testFile, 82, 82),
		finding("SEC-001", findings.SeverityMedium, 0.90, testFile, 83, 83),
		finding("TEST-001", findings.SeverityMedium, 0.90, testFile, 900, 900),
	))
	if err != nil {
		t.Fatalf("Publish() = %v", err)
	}

	if outcome.Status != StatusPublished || !outcome.Published() {
		t.Errorf("Status = %q, want %q", outcome.Status, StatusPublished)
	}
	if len(api.created) != 1 {
		t.Fatalf("created %d reviews, want exactly 1", len(api.created))
	}

	sent := api.created[0]
	if api.createdOwner != "acme" || api.createdRepo != "payments" || api.createdPull != 123 {
		t.Errorf("review addressed to %s/%s#%d, want acme/payments#123",
			api.createdOwner, api.createdRepo, api.createdPull)
	}
	if sent.CommitID != testHeadSHA {
		t.Errorf("commit_id = %q, want the reviewed head %q", sent.CommitID, testHeadSHA)
	}
	if sent.Event != github.EventComment {
		t.Errorf("event = %q, want %q", sent.Event, github.EventComment)
	}
	if len(sent.Comments) != 2 {
		t.Fatalf("review carries %d comments, want 2 in one request", len(sent.Comments))
	}
	if outcome.InlineComments != 2 || outcome.SummaryFindings != 1 {
		t.Errorf("outcome = %d inline, %d summary; want 2 and 1", outcome.InlineComments, outcome.SummaryFindings)
	}
	if !strings.Contains(sent.Body, "TEST-001") {
		t.Error("the unmappable finding is missing from the review body")
	}
}

// TestPublishSendsMappedLocations checks the comments carry exactly the locations the
// mapper produced, including a multi-line range.
func TestPublishSendsMappedLocations(t *testing.T) {
	api := &fakeAPI{currentSHA: testHeadSHA}

	_, err := NewPublisher(api).Publish(context.Background(), publishRequest(
		finding("COR-001", findings.SeverityBlocker, 0.99, testFile, 81, 83),
		finding("COR-002", findings.SeverityHigh, 0.95, testFile, 85, 85),
	))
	if err != nil {
		t.Fatalf("Publish() = %v", err)
	}

	comments := api.created[0].Comments
	if len(comments) != 2 {
		t.Fatalf("comments = %d, want 2", len(comments))
	}

	multi := comments[0]
	if multi.Path != testFile || multi.StartLine == nil || *multi.StartLine != 81 || multi.Line != 83 {
		t.Errorf("range comment = %+v, want %s lines 81-83", multi, testFile)
	}
	if multi.Side != github.SideRight || multi.StartSide == nil || *multi.StartSide != github.SideRight {
		t.Errorf("range comment sides are not both RIGHT: %+v", multi)
	}

	single := comments[1]
	if single.Line != 85 || single.Side != github.SideRight {
		t.Errorf("single comment = %+v, want line 85 RIGHT", single)
	}
	if single.StartLine != nil || single.StartSide != nil {
		t.Errorf("single comment carries range fields: %+v", single)
	}
	if !strings.Contains(single.Body, "COR-002") {
		t.Error("comment body does not identify its finding")
	}
}

// TestPublishStaleHeadAborts checks the central safety property: if the pull request
// moved, nothing is published, the new commit is not reviewed, and the failure names
// both commits.
func TestPublishStaleHeadAborts(t *testing.T) {
	api := &fakeAPI{currentSHA: "def4560000000000000000000000000000000000"}

	outcome, err := NewPublisher(api).Publish(context.Background(), publishRequest(findingFixture()))

	if err == nil {
		t.Fatal("Publish() = nil error, want a stale-head error")
	}
	if !errors.Is(err, ErrStaleHead) {
		t.Errorf("errors.Is(err, ErrStaleHead) = false; err = %v", err)
	}

	var stale *StaleHeadError
	if !errors.As(err, &stale) {
		t.Fatalf("errors.As(err, *StaleHeadError) = false; err = %v", err)
	}
	if stale.ReviewedHeadSHA != testHeadSHA || stale.CurrentHeadSHA != api.currentSHA {
		t.Errorf("stale error = %+v, want reviewed %s and current %s", stale, testHeadSHA, api.currentSHA)
	}
	if outcome.Status != StatusAbortedStaleHead {
		t.Errorf("Status = %q, want %q", outcome.Status, StatusAbortedStaleHead)
	}

	if len(api.created) != 0 {
		t.Fatal("a review was created after stale-head detection")
	}
	if api.listCalls != 0 {
		t.Error("publication continued past the stale-head check")
	}
	if !strings.Contains(err.Error(), "rerun") {
		t.Errorf("error %q does not tell the user to rerun ARC", err)
	}
}

// TestPublishSameHeadProceeds checks an unchanged head allows publication.
func TestPublishSameHeadProceeds(t *testing.T) {
	api := &fakeAPI{currentSHA: strings.ToUpper(testHeadSHA)}

	outcome, err := NewPublisher(api).Publish(context.Background(), publishRequest(findingFixture()))
	if err != nil {
		t.Fatalf("Publish() = %v", err)
	}
	if outcome.Status != StatusPublished {
		t.Errorf("Status = %q, want %q; SHA comparison must not be case-sensitive", outcome.Status, StatusPublished)
	}
	if api.getCalls != 1 {
		t.Errorf("GetPullRequest called %d times, want 1", api.getCalls)
	}
}

// TestPublishSkipsDuplicate checks a second run for the same commit posts nothing.
func TestPublishSkipsDuplicate(t *testing.T) {
	api := &fakeAPI{
		currentSHA: testHeadSHA,
		reviews: []github.ExistingReview{
			{ID: 7, Body: "## ARC Agentic Code Review\n" + Marker{HeadSHA: testHeadSHA}.Render(),
				CommitID: testHeadSHA, HTMLURL: "https://github.test/earlier"},
		},
	}

	outcome, err := NewPublisher(api).Publish(context.Background(), publishRequest(findingFixture()))
	if err != nil {
		t.Fatalf("Publish() = %v", err)
	}
	if outcome.Status != StatusSkippedDuplicate {
		t.Errorf("Status = %q, want %q", outcome.Status, StatusSkippedDuplicate)
	}
	if outcome.ExistingReview.ID != 7 {
		t.Errorf("ExistingReview.ID = %d, want 7", outcome.ExistingReview.ID)
	}
	if len(api.created) != 0 {
		t.Fatal("a duplicate review was created")
	}
}

// TestPublishAfterNewCommitIsNotADuplicate checks an earlier review for a different
// commit does not suppress the current one.
func TestPublishAfterNewCommitIsNotADuplicate(t *testing.T) {
	api := &fakeAPI{
		currentSHA: testHeadSHA,
		reviews: []github.ExistingReview{
			{ID: 7, Body: Marker{HeadSHA: "0000000000000000000000000000000000000000"}.Render(),
				CommitID: "0000000000000000000000000000000000000000"},
		},
	}

	outcome, err := NewPublisher(api).Publish(context.Background(), publishRequest(findingFixture()))
	if err != nil {
		t.Fatalf("Publish() = %v", err)
	}
	if outcome.Status != StatusPublished || len(api.created) != 1 {
		t.Errorf("Status = %q with %d reviews created, want a published review",
			outcome.Status, len(api.created))
	}
}

// TestPublishZeroFindingsWritesNothing checks a clean review is a success with no
// GitHub write. An empty review is a notification with nothing in it.
func TestPublishZeroFindingsWritesNothing(t *testing.T) {
	api := &fakeAPI{currentSHA: testHeadSHA}

	outcome, err := NewPublisher(api).Publish(context.Background(), Request{
		PullRequest:     testPullRequest,
		Plan:            planFor(),
		ReviewedHeadSHA: testHeadSHA,
	})
	if err != nil {
		t.Fatalf("Publish() = %v; zero findings is a successful review", err)
	}
	if outcome.Status != StatusSkippedNoFindings {
		t.Errorf("Status = %q, want %q", outcome.Status, StatusSkippedNoFindings)
	}
	if len(api.created) != 0 || api.getCalls != 0 || api.listCalls != 0 {
		t.Error("an empty plan reached GitHub")
	}
}

// TestPublishRequiresReviewedSHA checks a review is never published unpinned.
func TestPublishRequiresReviewedSHA(t *testing.T) {
	api := &fakeAPI{currentSHA: testHeadSHA}

	req := publishRequest(findingFixture())
	req.ReviewedHeadSHA = ""

	_, err := NewPublisher(api).Publish(context.Background(), req)
	if !errors.Is(err, ErrMissingHeadSHA) {
		t.Errorf("Publish() = %v, want ErrMissingHeadSHA", err)
	}
	if len(api.created) != 0 {
		t.Error("a review was created without a reviewed commit")
	}
}

// TestPublishAPIErrors checks each GitHub failure mode surfaces intact, with no
// credential and no retry.
func TestPublishAPIErrors(t *testing.T) {
	tests := []struct {
		name    string
		api     *fakeAPI
		wantErr error
		wantMsg string
	}{
		{
			name:    "unauthorized while verifying the head",
			api:     &fakeAPI{currentSHA: testHeadSHA, getErr: &github.APIError{StatusCode: 401, Message: "Bad credentials"}},
			wantErr: github.ErrUnauthorized,
		},
		{
			name:    "forbidden while listing reviews",
			api:     &fakeAPI{currentSHA: testHeadSHA, listErr: &github.APIError{StatusCode: 403, Message: "Resource not accessible"}},
			wantErr: github.ErrForbidden,
		},
		{
			name:    "not found while verifying the head",
			api:     &fakeAPI{currentSHA: testHeadSHA, getErr: &github.APIError{StatusCode: 404, Message: "Not Found"}},
			wantErr: github.ErrNotFound,
		},
		{
			name:    "rate limited",
			api:     &fakeAPI{currentSHA: testHeadSHA, getErr: &github.APIError{StatusCode: 403, Message: "rate limit", RateLimited: true}},
			wantErr: github.ErrRateLimited,
		},
		{
			name:    "server error while publishing",
			api:     &fakeAPI{currentSHA: testHeadSHA, createErr: &github.APIError{StatusCode: 500, Message: "Internal"}},
			wantErr: github.ErrServer,
		},
		{
			name:    "invalid location",
			api:     &fakeAPI{currentSHA: testHeadSHA, createErr: &github.APIError{StatusCode: 422, Message: "line must be part of the diff"}},
			wantErr: ErrInvalidLocation,
			wantMsg: "COR-001",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewPublisher(tt.api).Publish(context.Background(),
				publishRequest(finding("COR-001", findings.SeverityHigh, 0.96, testFile, 82, 82)))

			if err == nil {
				t.Fatal("Publish() = nil error, want a failure")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("errors.Is(err, %v) = false; err = %v", tt.wantErr, err)
			}
			if tt.wantMsg != "" && !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tt.wantMsg)
			}
			for _, forbidden := range []string{"ghp_", "Bearer", "GITHUB_TOKEN=", "@@ -80"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Errorf("error %q leaks %q", err, forbidden)
				}
			}
		})
	}
}

// TestPublishLocationErrorNamesRequestedLines checks a 422 is reported with the
// diagnostic metadata a developer needs — and nothing else. No alternative line is
// attempted: a comment on a guessed line describes code the finding is not about.
func TestPublishLocationErrorDoesNotRetry(t *testing.T) {
	api := &fakeAPI{currentSHA: testHeadSHA, createErr: &github.APIError{StatusCode: 422, Message: "invalid line"}}

	_, err := NewPublisher(api).Publish(context.Background(), publishRequest(
		finding("COR-001", findings.SeverityHigh, 0.96, testFile, 82, 82),
		finding("SEC-001", findings.SeverityHigh, 0.95, testFile, 83, 83),
	))

	var locationErr *LocationError
	if !errors.As(err, &locationErr) {
		t.Fatalf("errors.As(err, *LocationError) = false; err = %v", err)
	}
	if len(locationErr.Requested) != 2 {
		t.Errorf("Requested = %v, want both requested locations", locationErr.Requested)
	}
	for _, want := range []string{"COR-001", "SEC-001", testFile + ":82", testFile + ":83"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if len(api.created) != 1 {
		t.Errorf("CreatePullRequestReview called %d times; a rejected location must not be retried", len(api.created))
	}
}

// TestPublishRespectsCancellation checks a cancelled context stops the attempt at the
// first call rather than publishing anyway.
func TestPublishRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	api := &fakeAPI{currentSHA: testHeadSHA, getErr: context.Canceled}

	_, err := NewPublisher(api).Publish(ctx, publishRequest(findingFixture()))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Publish() = %v, want context.Canceled", err)
	}
	if len(api.created) != 0 {
		t.Error("a review was created despite a cancelled context")
	}
}

// TestPublishTargetsBaseRepository checks the review is addressed to the pull request
// on the base repository. Nothing in the publication path touches a fork or a branch.
func TestPublishTargetsBaseRepository(t *testing.T) {
	api := &fakeAPI{currentSHA: testHeadSHA}

	req := publishRequest(findingFixture())
	req.PullRequest = github.PullRequest{Owner: "acme", Repo: "payments", Number: 456}

	if _, err := NewPublisher(api).Publish(context.Background(), req); err != nil {
		t.Fatalf("Publish() = %v", err)
	}

	if api.createdOwner != "acme" || api.createdRepo != "payments" || api.createdPull != 456 {
		t.Errorf("review addressed to %s/%s#%d, want the base repository acme/payments#456",
			api.createdOwner, api.createdRepo, api.createdPull)
	}
}

// TestPublisherNeverApproves checks that the only event the publisher can send is a
// comment, whatever the plan contains.
func TestPublisherNeverApproves(t *testing.T) {
	api := &fakeAPI{currentSHA: testHeadSHA}

	for _, severity := range findings.Severities() {
		api.created = nil
		req := publishRequest(finding("F-1", severity, 0.99, testFile, 82, 82))
		if req.Plan.Empty() {
			continue
		}
		if _, err := NewPublisher(api).Publish(context.Background(), req); err != nil {
			t.Fatalf("Publish() = %v", err)
		}
		if got := api.created[0].Event; got != github.EventComment {
			t.Errorf("severity %s: event = %q, want %q", severity, got, github.EventComment)
		}
	}
}
