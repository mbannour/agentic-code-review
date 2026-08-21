package github

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

const (
	// commentsPerPage is the page size for listing comments, GitHub's maximum.
	commentsPerPage = 100

	// maxCommentPages bounds pagination. A pull request with more than 500
	// comments is read in part; the review does not need the whole argument.
	maxCommentPages = 5

	// MaxCommentBytes bounds one comment. A comment longer than this is carrying
	// a design document, and the review context is not the place for it.
	MaxCommentBytes = 4 * 1024
)

// CommentKind says where a comment lives.
type CommentKind string

const (
	// CommentConversation is a comment on the pull request as a whole.
	CommentConversation CommentKind = "conversation"

	// CommentInline is a comment attached to a line of the diff.
	CommentInline CommentKind = "inline"
)

// Comment is one human remark on a pull request.
//
// It is evidence about intent — a maintainer explaining that behaviour is
// deliberate, a reviewer asking a question, an author describing a trade-off —
// and it is untrusted text like every other repository-supplied string.
type Comment struct {
	Kind   CommentKind
	Author string
	Body   string

	// Path and Line locate an inline comment. Both are empty or zero for a
	// conversation comment.
	Path string
	Line int

	// CreatedAt is GitHub's timestamp, kept as the raw string: it is used for
	// ordering and display, never parsed into a decision.
	CreatedAt string

	// Truncated reports whether Body was cut to MaxCommentBytes.
	Truncated bool
}

// IsInline reports whether the comment is attached to a diff line.
func (c Comment) IsInline() bool { return c.Kind == CommentInline }

// Location renders an inline comment's position, or an empty string.
func (c Comment) Location() string {
	if c.Path == "" {
		return ""
	}
	if c.Line <= 0 {
		return c.Path
	}
	return fmt.Sprintf("%s:%d", c.Path, c.Line)
}

// commentResponse mirrors both comment endpoints. The inline-only fields are
// simply absent for conversation comments.
type commentResponse struct {
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	User      struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
}

func (r commentResponse) toComment(kind CommentKind) Comment {
	body, truncated := boundComment(r.Body)
	return Comment{
		Kind:      kind,
		Author:    r.User.Login,
		Body:      body,
		Path:      r.Path,
		Line:      r.Line,
		CreatedAt: r.CreatedAt,
		Truncated: truncated,
	}
}

// boundComment caps a comment's length without splitting a line.
func boundComment(body string) (string, bool) {
	trimmed := strings.TrimSpace(body)
	if len(trimmed) <= MaxCommentBytes {
		return trimmed, false
	}

	cut := trimmed[:MaxCommentBytes]
	if index := strings.LastIndexByte(cut, '\n'); index > MaxCommentBytes/2 {
		cut = cut[:index]
	}
	return strings.TrimSpace(cut) + "\n[TRUNCATED]", true
}

// ListPullRequestComments reads the pull request's discussion: conversation
// comments first, then comments attached to diff lines.
//
// Both are reads. Nothing about this changes what may be published — a comment
// can tell the review that behaviour is intentional, and a comment claiming
// authority it does not have is exactly the injection attempt the reviewer is
// told to report.
func (c *Client) ListPullRequestComments(ctx context.Context, pr PullRequest) ([]Comment, error) {
	conversation, err := c.listComments(ctx, pr, issuesPath(pr)+"/comments", CommentConversation)
	if err != nil {
		return nil, err
	}

	inline, err := c.listComments(ctx, pr, pullsPath(pr)+"/comments", CommentInline)
	if err != nil {
		return nil, err
	}

	return append(conversation, inline...), nil
}

func (c *Client) listComments(
	ctx context.Context,
	pr PullRequest,
	path string,
	kind CommentKind,
) ([]Comment, error) {
	var comments []Comment

	for page := 1; page <= maxCommentPages; page++ {
		query := url.Values{
			"per_page": {fmt.Sprint(commentsPerPage)},
			"page":     {fmt.Sprint(page)},
		}

		var body []commentResponse
		description := fmt.Sprintf("pull request %s comments %s (page %d)", kind, pr, page)
		if err := c.getJSON(ctx, path, query, &body, description); err != nil {
			return nil, err
		}

		for _, item := range body {
			comment := item.toComment(kind)
			if strings.TrimSpace(comment.Body) == "" {
				continue
			}
			comments = append(comments, comment)
		}

		if len(body) < commentsPerPage {
			return comments, nil
		}
	}

	// Reading part of a long discussion is better than failing the review over
	// it, so the bound is a stopping point rather than an error.
	return comments, nil
}

// issuesPath is the issues endpoint for a pull request. GitHub exposes
// conversation comments there, since every pull request is also an issue.
func issuesPath(pr PullRequest) string {
	return fmt.Sprintf("/repos/%s/%s/issues/%d",
		url.PathEscape(pr.Owner),
		url.PathEscape(pr.Repo),
		pr.Number,
	)
}
