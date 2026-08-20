package publish

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/your-company/agentic-code-review/internal/contextselect"
	"github.com/your-company/agentic-code-review/internal/github"
)

// Sentinel errors callers can match with errors.Is.
var (
	// ErrStaleHead means the pull request moved while the review was running, so
	// the findings describe a commit that is no longer current.
	ErrStaleHead = errors.New("PR head changed during review; rerun ARC before publishing")

	// ErrMissingHeadSHA means the reviewed commit is unknown, which makes pinning
	// the review impossible.
	ErrMissingHeadSHA = errors.New("reviewed head SHA is unknown")

	// ErrInvalidLocation means GitHub rejected a comment location. It is never
	// retried against a different line.
	ErrInvalidLocation = errors.New("GitHub rejected an inline comment location")
)

// StaleHeadError reports the mismatch between what was reviewed and what is now
// current. Both SHAs are safe to display.
type StaleHeadError struct {
	ReviewedHeadSHA string
	CurrentHeadSHA  string
}

func (e *StaleHeadError) Error() string {
	return fmt.Sprintf("%s (reviewed %s, current %s)",
		ErrStaleHead.Error(), e.ReviewedHeadSHA, e.CurrentHeadSHA)
}

// Unwrap lets callers match ErrStaleHead.
func (e *StaleHeadError) Unwrap() error { return ErrStaleHead }

// LocationError reports a location GitHub refused.
//
// It carries only the finding's identity and the line that was requested. No patch,
// no comment body, and never a credential: a publication failure has to be
// diagnosable from a terminal without leaking what was being reviewed.
type LocationError struct {
	// Requested lists every location the rejected review asked for, as
	// "FINDING-ID file:line SIDE". GitHub does not say which one it disliked, so
	// all of them are reported rather than one guessed at.
	Requested []string

	Status  int
	Message string
}

func (e *LocationError) Error() string {
	var b strings.Builder
	b.WriteString(ErrInvalidLocation.Error())
	if e.Status != 0 {
		fmt.Fprintf(&b, " (status %d)", e.Status)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if len(e.Requested) > 0 {
		fmt.Fprintf(&b, "; requested: %s", strings.Join(e.Requested, ", "))
	}
	b.WriteString("; ARC does not retry other lines — rerun the review")
	return b.String()
}

// Unwrap lets callers match ErrInvalidLocation and the underlying API error kind.
func (e *LocationError) Unwrap() error { return ErrInvalidLocation }

// Status is the outcome of a publication attempt.
type Status string

const (
	// StatusPublished means a review was created.
	StatusPublished Status = "published"

	// StatusSkippedNoFindings means there was nothing worth a review.
	StatusSkippedNoFindings Status = "skipped: no findings"

	// StatusSkippedDuplicate means ARC had already reviewed this commit.
	StatusSkippedDuplicate Status = "skipped: already published"

	// StatusAbortedStaleHead means the pull request moved and nothing was posted.
	StatusAbortedStaleHead Status = "aborted: stale head"
)

// Outcome describes what happened, including in the cases where nothing was
// posted. The two SHAs are always populated when they are known, so the caller can
// show a reader exactly what was compared.
type Outcome struct {
	Status Status

	ReviewedHeadSHA string
	CurrentHeadSHA  string

	// InlineComments is how many line comments the review carried.
	InlineComments int

	// SummaryFindings is how many findings were reported in the body instead.
	SummaryFindings int

	// Review is set only when a review was actually created.
	Review github.PublishedReview

	// ExistingReview is set when publication was skipped as a duplicate.
	ExistingReview github.ExistingReview
}

// Published reports whether a review was created.
func (o Outcome) Published() bool { return o.Status == StatusPublished }

// API is the GitHub surface publication needs.
//
// It is an interface for one reason beyond testing: it states, in one place, the
// entire set of GitHub operations this tool can perform. Two reads and one write —
// there is no method here that could modify a branch, a file, or a merge state.
type API interface {
	GetPullRequest(ctx context.Context, pr github.PullRequest) (github.PullRequestDetails, error)
	ListPullRequestReviews(ctx context.Context, pr github.PullRequest) ([]github.ExistingReview, error)
	CreatePullRequestReview(
		ctx context.Context,
		owner string,
		repo string,
		pullNumber int,
		req github.CreateReviewRequest,
	) (github.PublishedReview, error)
}

// Request is one publication attempt.
type Request struct {
	// PullRequest names the base repository and pull request number. A review is
	// always published here, never to a contributor's fork.
	PullRequest github.PullRequest

	// Plan is the decision already made about the findings.
	Plan Plan

	// ReviewedHeadSHA is the commit the findings were derived from, and the commit
	// the review will be pinned to.
	ReviewedHeadSHA string

	// JiraKey is the ticket key, for the review body. The ticket's contents are
	// deliberately not carried.
	JiraKey string

	// Checks are the deterministic check outcomes to report.
	Checks []contextselect.SelectedAnalysis

	// ExpectedAuthor is the login an earlier ARC review would have been posted under,
	// when that is known. Empty means unknown, in which case duplicate detection
	// relies on the marker and the commit alone.
	ExpectedAuthor string
}

// Publisher performs the single write this tool is allowed to make.
//
// Everything it does before that write is a reason not to make it: an empty plan, a
// review ARC already posted, or a pull request that moved. Only when all three
// checks pass does one review get created, pinned to the commit that was actually
// analyzed.
type Publisher struct {
	api      API
	renderer Renderer
}

// NewPublisher returns a Publisher backed by api.
func NewPublisher(api API) Publisher {
	return Publisher{api: api, renderer: NewRenderer()}
}

// Publish creates at most one pull request review.
//
// The order of the guards matters. Nothing is published for an empty plan; nothing
// is published without a reviewed commit; nothing is published if the head moved,
// and in that case the new commit is not reviewed and publication is not retried
// against it; and nothing is published twice for the same commit.
func (p Publisher) Publish(ctx context.Context, req Request) (Outcome, error) {
	reviewed := strings.TrimSpace(req.ReviewedHeadSHA)

	outcome := Outcome{
		ReviewedHeadSHA: reviewed,
		InlineComments:  len(req.Plan.Inline),
		SummaryFindings: len(req.Plan.Summary),
	}

	if req.Plan.NothingToPublish() {
		outcome.Status = StatusSkippedNoFindings
		return outcome, nil
	}
	if reviewed == "" {
		return outcome, ErrMissingHeadSHA
	}

	// Re-read the pull request: the findings describe one exact commit, and a
	// review pinned to a commit the branch has moved past is worse than no review.
	current, err := p.api.GetPullRequest(ctx, req.PullRequest)
	if err != nil {
		return outcome, fmt.Errorf("verify pull request head: %w", err)
	}
	outcome.CurrentHeadSHA = current.HeadSHA

	if !strings.EqualFold(strings.TrimSpace(current.HeadSHA), reviewed) {
		outcome.Status = StatusAbortedStaleHead
		return outcome, &StaleHeadError{ReviewedHeadSHA: reviewed, CurrentHeadSHA: current.HeadSHA}
	}

	existing, err := p.api.ListPullRequestReviews(ctx, req.PullRequest)
	if err != nil {
		return outcome, fmt.Errorf("check for an existing ARC review: %w", err)
	}
	if previous, found := AlreadyPublished(existing, reviewed, req.ExpectedAuthor); found {
		outcome.Status = StatusSkippedDuplicate
		outcome.ExistingReview = previous
		return outcome, nil
	}

	body := p.renderer.ReviewBody(ReviewInput{
		Plan:    req.Plan,
		HeadSHA: reviewed,
		JiraKey: req.JiraKey,
		Checks:  req.Checks,
	})

	comments := make([]github.ReviewComment, 0, len(req.Plan.Inline))
	for _, item := range req.Plan.Inline {
		comments = append(comments, item.Location.Comment(p.renderer.InlineComment(item.Finding)))
	}

	published, err := p.api.CreatePullRequestReview(ctx,
		req.PullRequest.Owner, req.PullRequest.Repo, req.PullRequest.Number,
		github.CreateReviewRequest{
			CommitID: reviewed,
			Body:     body,
			Event:    github.EventComment,
			Comments: comments,
		})
	if err != nil {
		return outcome, p.publicationError(err, req.Plan)
	}

	outcome.Status = StatusPublished
	outcome.Review = published
	return outcome, nil
}

// publicationError translates a failed write into something safe and actionable.
//
// A 422 almost always means a comment location GitHub would not accept. The
// response is to say which finding and which line were rejected and stop. Trying a
// neighbouring line would attach a real finding to code it does not describe, which
// is a worse outcome than a failed publication.
func (p Publisher) publicationError(err error, plan Plan) error {
	var apiErr *github.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 422 {
		return fmt.Errorf("publish review: %w", err)
	}

	locationErr := &LocationError{Status: apiErr.StatusCode, Message: apiErr.Message}
	for _, item := range plan.Inline {
		locationErr.Requested = append(locationErr.Requested,
			item.Finding.ID+" "+item.Location.Describe())
	}

	return fmt.Errorf("publish review: %w", locationErr)
}
