package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capturedRequest is what the mock server saw.
type capturedRequest struct {
	method string
	path   string
	query  string
	auth   string
	body   map[string]any
}

// newReviewServer returns a test server that records the request and answers with
// status and body. No test in this file talks to a real repository.
func newReviewServer(t *testing.T, status int, response string) (*Client, *capturedRequest) {
	t.Helper()

	captured := &capturedRequest{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.method = r.Method
		captured.path = r.URL.Path
		captured.query = r.URL.RawQuery
		captured.auth = r.Header.Get("Authorization")

		if raw, err := io.ReadAll(r.Body); err == nil && len(raw) > 0 {
			_ = json.Unmarshal(raw, &captured.body)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)

	return NewClientWithBaseURL(testToken, server.URL, server.Client()), captured
}

// intPtr and strPtr build the optional range fields.
func intPtr(v int) *int       { return &v }
func strPtr(v string) *string { return &v }

// testRepo is the base repository every review in this file is published against.
const (
	testOwner = "acme"
	testRepo  = "payments"
	testPull  = 123
)

// reviewFixture is a valid review with one single-line, one multi-line, and one
// old-side comment.
func reviewFixture() CreateReviewRequest {
	return CreateReviewRequest{
		CommitID: "abc123def4567890",
		Body:     "## ARC Agentic Code Review",
		Event:    EventComment,
		Comments: []ReviewComment{
			{Path: "internal/payment/retry.go", Line: 84, Side: SideRight, Body: "single"},
			{Path: "internal/payment/retry.go", Line: 83, Side: SideRight,
				StartLine: intPtr(81), StartSide: strPtr(SideRight), Body: "range"},
			{Path: "internal/legacy/old.go", Line: 3, Side: SideLeft, Body: "deleted"},
		},
	}
}

// createReview calls the endpoint with the fixture repository.
func createReview(client *Client, ctx context.Context, req CreateReviewRequest) (PublishedReview, error) {
	return client.CreatePullRequestReview(ctx, testOwner, testRepo, testPull, req)
}

// TestCreatePullRequestReviewRequest checks the endpoint, the addressing, and every
// field of the payload GitHub receives.
func TestCreatePullRequestReviewRequest(t *testing.T) {
	client, captured := newReviewServer(t, http.StatusOK,
		`{"id": 42, "state": "COMMENTED", "html_url": "https://github.com/acme/payments/pull/123#pullrequestreview-42"}`)

	published, err := createReview(client, context.Background(), reviewFixture())
	if err != nil {
		t.Fatalf("CreatePullRequestReview() = %v", err)
	}

	if captured.method != http.MethodPost {
		t.Errorf("method = %q, want POST", captured.method)
	}
	if want := "/repos/acme/payments/pulls/123/reviews"; captured.path != want {
		t.Errorf("path = %q, want %q", captured.path, want)
	}
	if captured.body["commit_id"] != "abc123def4567890" {
		t.Errorf("commit_id = %v, want the reviewed head", captured.body["commit_id"])
	}
	if captured.body["event"] != EventComment {
		t.Errorf("event = %v, want %q", captured.body["event"], EventComment)
	}

	comments, ok := captured.body["comments"].([]any)
	if !ok || len(comments) != 3 {
		t.Fatalf("comments = %v, want 3 comments in one request", captured.body["comments"])
	}

	single := comments[0].(map[string]any)
	if single["path"] != "internal/payment/retry.go" || single["line"] != float64(84) || single["side"] != SideRight {
		t.Errorf("single-line comment = %v", single)
	}
	if _, present := single["start_line"]; present {
		t.Error("single-line comment carries start_line; GitHub rejects a partial range")
	}

	multi := comments[1].(map[string]any)
	if multi["start_line"] != float64(81) || multi["start_side"] != SideRight || multi["line"] != float64(83) {
		t.Errorf("multiline comment = %v, want lines 81-83 on the right", multi)
	}

	left := comments[2].(map[string]any)
	if left["side"] != SideLeft {
		t.Errorf("deleted-line comment side = %v, want %q", left["side"], SideLeft)
	}

	if published.ID != 42 || published.State != "COMMENTED" || published.HTMLURL == "" {
		t.Errorf("PublishedReview = %+v, want the API's own values", published)
	}
}

// TestCreatePullRequestReviewSendsToken checks the credential travels in the header
// and nowhere else.
func TestCreatePullRequestReviewSendsToken(t *testing.T) {
	client, captured := newReviewServer(t, http.StatusOK, `{"id": 1}`)

	if _, err := createReview(client, context.Background(), reviewFixture()); err != nil {
		t.Fatalf("CreatePullRequestReview() = %v", err)
	}

	if captured.auth != "Bearer "+testToken {
		t.Errorf("Authorization = %q, want the bearer token", captured.auth)
	}
	if strings.Contains(captured.path+captured.query, testToken) {
		t.Error("token appears in the request URL")
	}
}

// TestCreatePullRequestReviewErrors checks every failure GitHub can answer with is
// mapped to a matchable error that carries no credential.
func TestCreatePullRequestReviewErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{name: "401", status: 401, body: `{"message": "Bad credentials"}`, wantErr: ErrUnauthorized},
		{name: "403", status: 403, body: `{"message": "Resource not accessible by personal access token"}`, wantErr: ErrForbidden},
		{name: "404", status: 404, body: `{"message": "Not Found"}`, wantErr: ErrNotFound},
		{name: "422", status: 422, body: `{"message": "line must be part of the diff"}`, wantErr: ErrUnprocessable},
		{name: "429", status: 429, body: `{"message": "too many requests"}`, wantErr: ErrRateLimited},
		{name: "500", status: 500, body: `{"message": "Internal server error"}`, wantErr: ErrServer},
		{name: "502", status: 502, body: `{"message": "Bad gateway"}`, wantErr: ErrServer},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := newReviewServer(t, tt.status, tt.body)

			_, err := createReview(client, context.Background(), reviewFixture())
			if err == nil {
				t.Fatal("CreatePullRequestReview() = nil error, want a failure")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("errors.Is(err, %v) = false; err = %v", tt.wantErr, err)
			}
			if strings.Contains(err.Error(), testToken) {
				t.Errorf("error %q contains the token", err)
			}
		})
	}
}

// TestCreatePullRequestReviewRateLimitHeader checks a 403 carrying an exhausted rate
// limit is distinguished from a permission failure.
func TestCreatePullRequestReviewRateLimitHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message": "API rate limit exceeded"}`)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testToken, server.URL, server.Client())

	_, err := createReview(client, context.Background(), reviewFixture())
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("errors.Is(err, ErrRateLimited) = false; err = %v", err)
	}
}

// TestCreatePullRequestReviewNeverEchoesToken checks a server that reflects the
// credential back cannot get it into an error message.
func TestCreatePullRequestReviewNeverEchoesToken(t *testing.T) {
	client, _ := newReviewServer(t, http.StatusUnauthorized,
		`{"message": "bad token `+testToken+`"}`)

	_, err := createReview(client, context.Background(), reviewFixture())
	if err == nil {
		t.Fatal("CreatePullRequestReview() = nil error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("error %q echoes the token back", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Errorf("error %q does not show the redaction", err)
	}
}

// TestCreatePullRequestReviewCancellation checks a cancelled context is reported and
// nothing is retried.
func TestCreatePullRequestReviewCancellation(t *testing.T) {
	client, _ := newReviewServer(t, http.StatusOK, `{"id": 1}`)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := createReview(client, ctx, reviewFixture()); !errors.Is(err, context.Canceled) {
		t.Errorf("CreatePullRequestReview() = %v, want context.Canceled", err)
	}
}

// TestCreatePullRequestReviewRejectsInvalidRequests checks the client refuses a
// malformed review before making any request. Nothing here reaches the network.
func TestCreatePullRequestReviewRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*CreateReviewRequest)
		wantMsg string
	}{
		{name: "no commit", mutate: func(r *CreateReviewRequest) { r.CommitID = "" }, wantMsg: "commit id"},
		{name: "no body", mutate: func(r *CreateReviewRequest) { r.Body = "" }, wantMsg: "body"},
		{
			name:    "approve event",
			mutate:  func(r *CreateReviewRequest) { r.Event = "APPROVE" },
			wantMsg: "not permitted",
		},
		{
			name:    "request changes event",
			mutate:  func(r *CreateReviewRequest) { r.Event = "REQUEST_CHANGES" },
			wantMsg: "not permitted",
		},
		{
			name:    "comment without a path",
			mutate:  func(r *CreateReviewRequest) { r.Comments[0].Path = "" },
			wantMsg: "path is required",
		},
		{
			name:    "comment without a line",
			mutate:  func(r *CreateReviewRequest) { r.Comments[0].Line = 0 },
			wantMsg: "line must be greater than zero",
		},
		{
			name:    "comment with an invalid side",
			mutate:  func(r *CreateReviewRequest) { r.Comments[0].Side = "MIDDLE" },
			wantMsg: "side",
		},
		{
			name:    "comment without a body",
			mutate:  func(r *CreateReviewRequest) { r.Comments[0].Body = "" },
			wantMsg: "body is required",
		},
		{
			name:    "range starting after the line",
			mutate:  func(r *CreateReviewRequest) { r.Comments[1].StartLine = intPtr(99) },
			wantMsg: "start_line",
		},
		{
			name:    "range with an invalid start side",
			mutate:  func(r *CreateReviewRequest) { r.Comments[1].StartSide = strPtr("MIDDLE") },
			wantMsg: "start_side",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("an invalid review reached the network")
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testToken, server.URL, server.Client())

			req := reviewFixture()
			tt.mutate(&req)

			_, err := createReview(client, context.Background(), req)
			if err == nil {
				t.Fatal("CreatePullRequestReview() = nil error, want a validation failure")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tt.wantMsg)
			}
		})
	}
}

// TestCreatePullRequestReviewForcesCommentEvent checks the event sent is COMMENT even
// when the caller left it unset. This tool never approves and never requests changes.
func TestCreatePullRequestReviewForcesCommentEvent(t *testing.T) {
	client, captured := newReviewServer(t, http.StatusOK, `{"id": 1}`)

	req := reviewFixture()
	req.Event = ""

	if _, err := createReview(client, context.Background(), req); err != nil {
		t.Fatalf("CreatePullRequestReview() = %v", err)
	}
	if captured.body["event"] != EventComment {
		t.Errorf("event = %v, want %q", captured.body["event"], EventComment)
	}
}

// TestCreatePullRequestReviewWithoutComments checks a summary-only review is valid: a
// finding that maps to no line still gets published, in the body.
func TestCreatePullRequestReviewWithoutComments(t *testing.T) {
	client, captured := newReviewServer(t, http.StatusOK, `{"id": 1}`)

	req := reviewFixture()
	req.Comments = nil

	if _, err := createReview(client, context.Background(), req); err != nil {
		t.Fatalf("CreatePullRequestReview() = %v", err)
	}
	if _, present := captured.body["comments"]; present {
		t.Error("an empty comments array was sent; it should be omitted")
	}
}

// TestListPullRequestReviews checks the reviews listing, which duplicate detection
// depends on.
func TestListPullRequestReviews(t *testing.T) {
	client, captured := newReviewServer(t, http.StatusOK,
		`[{"id": 7, "body": "<!-- arc-review:v1 head=abc123 -->", "state": "COMMENTED",
		   "commit_id": "abc123", "html_url": "https://github.test/7", "user": {"login": "dev"}}]`)

	reviews, err := client.ListPullRequestReviews(context.Background(),
		PullRequest{Owner: "acme", Repo: "payments", Number: 123})
	if err != nil {
		t.Fatalf("ListPullRequestReviews() = %v", err)
	}

	if captured.method != http.MethodGet {
		t.Errorf("method = %q, want GET", captured.method)
	}
	if want := "/repos/acme/payments/pulls/123/reviews"; captured.path != want {
		t.Errorf("path = %q, want %q", captured.path, want)
	}
	if len(reviews) != 1 {
		t.Fatalf("reviews = %d, want 1", len(reviews))
	}

	got := reviews[0]
	if got.ID != 7 || got.CommitID != "abc123" || got.AuthorLogin != "dev" || got.State != "COMMENTED" {
		t.Errorf("review = %+v, want the API's own values", got)
	}
	if !strings.Contains(got.Body, "arc-review") {
		t.Errorf("body = %q, want the review body preserved for marker detection", got.Body)
	}
}

// TestListPullRequestReviewsError checks a failed listing is reported rather than
// treated as "no earlier review", which would let a duplicate through.
func TestListPullRequestReviewsError(t *testing.T) {
	client, _ := newReviewServer(t, http.StatusForbidden, `{"message": "no access"}`)

	_, err := client.ListPullRequestReviews(context.Background(),
		PullRequest{Owner: "acme", Repo: "payments", Number: 123})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("errors.Is(err, ErrForbidden) = false; err = %v", err)
	}
}

// TestReviewCommentMultiline checks the helper used to decide whether a comment is a
// range.
func TestReviewCommentMultiline(t *testing.T) {
	tests := []struct {
		name    string
		comment ReviewComment
		want    bool
	}{
		{name: "single line", comment: ReviewComment{Line: 84}, want: false},
		{name: "range", comment: ReviewComment{Line: 84, StartLine: intPtr(81)}, want: true},
		{name: "degenerate range", comment: ReviewComment{Line: 84, StartLine: intPtr(84)}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.comment.Multiline(); got != tt.want {
				t.Errorf("Multiline() = %t, want %t", got, tt.want)
			}
		})
	}
}
