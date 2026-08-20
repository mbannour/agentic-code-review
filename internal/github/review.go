package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// EventComment is the only review event this tool will ever send.
//
// A pull request review can also approve or request changes. This tool is not an
// approval authority: it observes and comments, and a human decides. There is
// deliberately no constant for the other two events, so no future caller can
// reach for one by accident.
const EventComment = "COMMENT"

// SideRight marks a location in the new version of a file; SideLeft marks one in
// the old version. GitHub requires the exact spelling.
const (
	SideRight = "RIGHT"
	SideLeft  = "LEFT"
)

const (
	// reviewsPerPage is the page size used when listing existing reviews.
	reviewsPerPage = 100

	// maxReviewPages bounds pagination on the reviews listing.
	maxReviewPages = 10
)

// ReviewComment is one inline comment in a pull request review.
//
// Line and Side address a single line. StartLine and StartSide extend that to a
// range and are pointers precisely so "unset" is distinguishable from line zero:
// GitHub rejects a half-specified range, so a caller supplies both or neither, and
// omitempty keeps them off the wire entirely for a single-line comment.
type ReviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`

	StartLine *int    `json:"start_line,omitempty"`
	StartSide *string `json:"start_side,omitempty"`

	Body string `json:"body"`
}

// Multiline reports whether this comment spans a range of lines.
func (c ReviewComment) Multiline() bool {
	return c.StartLine != nil && *c.StartLine < c.Line
}

// CreateReviewRequest is the body of one pull request review.
//
// CommitID pins the review to the exact commit that was analyzed. The repository and
// pull request are passed separately, and always name the base repository: a review
// lives on the pull request, never on a contributor's fork, and nothing here touches
// a branch.
type CreateReviewRequest struct {
	CommitID string          `json:"commit_id"`
	Body     string          `json:"body"`
	Event    string          `json:"event"`
	Comments []ReviewComment `json:"comments,omitempty"`
}

// PublishedReview is what GitHub returned for a created review.
type PublishedReview struct {
	ID      int64
	HTMLURL string
	State   string
}

// ExistingReview is one review already present on a pull request. It carries only
// what duplicate detection needs.
type ExistingReview struct {
	ID          int64
	Body        string
	State       string
	CommitID    string
	AuthorLogin string
	HTMLURL     string
}

type reviewResponse struct {
	ID       int64  `json:"id"`
	Body     string `json:"body"`
	State    string `json:"state"`
	CommitID string `json:"commit_id"`
	HTMLURL  string `json:"html_url"`
	User     struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (r reviewResponse) toExisting() ExistingReview {
	return ExistingReview{
		ID:          r.ID,
		Body:        r.Body,
		State:       r.State,
		CommitID:    r.CommitID,
		AuthorLogin: r.User.Login,
		HTMLURL:     r.HTMLURL,
	}
}

// CreatePullRequestReview publishes one review via
// POST /repos/{owner}/{repo}/pulls/{pull_number}/reviews.
//
// This is the only write this tool performs anywhere. Every inline comment travels
// in this single request, so a review either appears whole or not at all, and the
// event is forced to COMMENT regardless of what the caller asked for.
func (c *Client) CreatePullRequestReview(
	ctx context.Context,
	owner string,
	repo string,
	pullNumber int,
	req CreateReviewRequest,
) (PublishedReview, error) {
	pr := PullRequest{Owner: owner, Repo: repo, Number: pullNumber}
	if err := req.validate(pr); err != nil {
		return PublishedReview{}, err
	}

	// The event is set here rather than carried through from the caller: approving
	// or requesting changes is outside this tool's authority, and validate has
	// already refused anything but a comment.
	req.Event = EventComment

	var body reviewResponse
	err := c.doJSON(ctx, http.MethodPost, reviewsPath(pr), nil, req, &body,
		"create review on "+pr.String())
	if err != nil {
		return PublishedReview{}, err
	}

	return PublishedReview{ID: body.ID, HTMLURL: body.HTMLURL, State: body.State}, nil
}

// ListPullRequestReviews lists the reviews already on pr via
// GET /repos/{owner}/{repo}/pulls/{pull_number}/reviews.
//
// It exists so a caller can recognize its own earlier review and decline to post
// a second one.
func (c *Client) ListPullRequestReviews(ctx context.Context, pr PullRequest) ([]ExistingReview, error) {
	var reviews []ExistingReview

	for page := 1; page <= maxReviewPages; page++ {
		query := url.Values{}
		query.Set("per_page", fmt.Sprint(reviewsPerPage))
		query.Set("page", fmt.Sprint(page))

		var body []reviewResponse
		err := c.getJSON(ctx, reviewsPath(pr), query, &body, "reviews for "+pr.String())
		if err != nil {
			return nil, err
		}

		for _, r := range body {
			reviews = append(reviews, r.toExisting())
		}
		if len(body) < reviewsPerPage {
			break
		}
	}

	return reviews, nil
}

// reviewsPath is the API path for a pull request's reviews.
func reviewsPath(pr PullRequest) string { return pullsPath(pr) + "/reviews" }

// validate rejects a request that could not possibly be a valid review, before any
// network call is made.
func (r CreateReviewRequest) validate(pr PullRequest) error {
	switch {
	case strings.TrimSpace(pr.Owner) == "":
		return fmt.Errorf("create review: owner is required")
	case strings.TrimSpace(pr.Repo) == "":
		return fmt.Errorf("create review: repository is required")
	case pr.Number <= 0:
		return fmt.Errorf("create review: pull request number must be greater than zero")
	case strings.TrimSpace(r.CommitID) == "":
		return fmt.Errorf("create review: commit id is required")
	case strings.TrimSpace(r.Body) == "":
		return fmt.Errorf("create review: body is required")
	}

	// An event other than COMMENT is a caller bug, not something to normalize
	// quietly: approving or requesting changes is outside this tool's authority.
	if event := strings.TrimSpace(r.Event); event != "" && event != EventComment {
		return fmt.Errorf("create review: event %q is not permitted; only %s is", r.Event, EventComment)
	}

	for i, comment := range r.Comments {
		switch {
		case strings.TrimSpace(comment.Path) == "":
			return fmt.Errorf("create review: comments[%d]: path is required", i)
		case comment.Line <= 0:
			return fmt.Errorf("create review: comments[%d]: line must be greater than zero", i)
		case comment.Side != SideRight && comment.Side != SideLeft:
			return fmt.Errorf("create review: comments[%d]: side %q must be %s or %s",
				i, comment.Side, SideRight, SideLeft)
		case strings.TrimSpace(comment.Body) == "":
			return fmt.Errorf("create review: comments[%d]: body is required", i)
		}

		// Half a range is worse than no range: GitHub would reject the whole review.
		if (comment.StartLine == nil) != (comment.StartSide == nil) {
			return fmt.Errorf("create review: comments[%d]: start_line and start_side must be set together", i)
		}
		if comment.StartLine == nil {
			continue
		}

		switch {
		case *comment.StartLine <= 0:
			return fmt.Errorf("create review: comments[%d]: start_line must be greater than zero", i)
		case *comment.StartLine >= comment.Line:
			return fmt.Errorf("create review: comments[%d]: start_line %d must be before line %d",
				i, *comment.StartLine, comment.Line)
		case *comment.StartSide != SideRight && *comment.StartSide != SideLeft:
			return fmt.Errorf("create review: comments[%d]: start_side %q must be %s or %s",
				i, *comment.StartSide, SideRight, SideLeft)
		}
	}

	return nil
}
