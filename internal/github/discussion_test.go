package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newDiscussionServer serves the two comment endpoints from fixed bodies.
func newDiscussionServer(t *testing.T, conversation, inline string) (*Client, *[]string) {
	t.Helper()

	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)

		switch {
		case strings.Contains(r.URL.Path, "/issues/"):
			if r.URL.Query().Get("page") != "1" {
				w.Write([]byte("[]"))
				return
			}
			w.Write([]byte(conversation))
		case strings.Contains(r.URL.Path, "/pulls/"):
			if r.URL.Query().Get("page") != "1" {
				w.Write([]byte("[]"))
				return
			}
			w.Write([]byte(inline))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)

	return NewClientWithBaseURL(testToken, srv.URL, srv.Client()), &paths
}

func TestListPullRequestComments(t *testing.T) {
	client, paths := newDiscussionServer(t,
		`[{"body":"Please add a test for the 409 path.","created_at":"2026-08-20T10:00:00Z","user":{"login":"sam"}}]`,
		`[{"body":"Deliberate: the gateway de-duplicates.","created_at":"2026-08-20T11:00:00Z","path":"internal/pay/retry.go","line":84,"user":{"login":"maria"}}]`,
	)

	comments, err := client.ListPullRequestComments(context.Background(),
		PullRequest{Owner: "acme", Repo: "payments", Number: 7})
	if err != nil {
		t.Fatalf("ListPullRequestComments() = %v", err)
	}

	if len(comments) != 2 {
		t.Fatalf("comments = %d, want 2", len(comments))
	}

	// Conversation comments come first, then comments on diff lines.
	if comments[0].Kind != CommentConversation || comments[0].Author != "sam" {
		t.Errorf("first comment = %+v", comments[0])
	}
	if comments[1].Kind != CommentInline || comments[1].Location() != "internal/pay/retry.go:84" {
		t.Errorf("second comment = %+v", comments[1])
	}

	// Both endpoints, and only those two.
	var sawIssues, sawPulls bool
	for _, path := range *paths {
		switch {
		case strings.HasSuffix(path, "/issues/7/comments"):
			sawIssues = true
		case strings.HasSuffix(path, "/pulls/7/comments"):
			sawPulls = true
		default:
			t.Errorf("unexpected endpoint %s", path)
		}
	}
	if !sawIssues || !sawPulls {
		t.Errorf("endpoints read = %v, want both comment endpoints", *paths)
	}
}

func TestListPullRequestCommentsSkipsEmptyBodies(t *testing.T) {
	client, _ := newDiscussionServer(t,
		`[{"body":"   ","user":{"login":"sam"}},{"body":"real","user":{"login":"sam"}}]`,
		`[]`,
	)

	comments, err := client.ListPullRequestComments(context.Background(),
		PullRequest{Owner: "acme", Repo: "payments", Number: 7})
	if err != nil {
		t.Fatalf("ListPullRequestComments() = %v", err)
	}
	if len(comments) != 1 || comments[0].Body != "real" {
		t.Errorf("comments = %+v, want only the non-empty one", comments)
	}
}

// A comment carrying a design document does not belong in the review context.
func TestCommentBodiesAreBounded(t *testing.T) {
	long := strings.Repeat("a very long line of commentary\n", 400)
	client, _ := newDiscussionServer(t,
		`[{"body":"`+strings.TrimSpace(strings.ReplaceAll(long, "\n", "\\n"))+`","user":{"login":"sam"}}]`,
		`[]`,
	)

	comments, err := client.ListPullRequestComments(context.Background(),
		PullRequest{Owner: "acme", Repo: "payments", Number: 7})
	if err != nil {
		t.Fatalf("ListPullRequestComments() = %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("comments = %d, want 1", len(comments))
	}
	if len(comments[0].Body) > MaxCommentBytes+len("\n[TRUNCATED]") {
		t.Errorf("body = %d bytes, want at most %d", len(comments[0].Body), MaxCommentBytes)
	}
	if !comments[0].Truncated {
		t.Error("the comment was cut but not marked truncated")
	}
}

// The token must not surface through a comment-endpoint failure either.
func TestCommentErrorsCarryNoCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Bad credentials: ` + testToken + `"}`))
	}))
	t.Cleanup(srv.Close)

	client := NewClientWithBaseURL(testToken, srv.URL, srv.Client())
	_, err := client.ListPullRequestComments(context.Background(),
		PullRequest{Owner: "acme", Repo: "payments", Number: 7})
	if err == nil {
		t.Fatal("ListPullRequestComments() = nil error, want the 401")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("error leaked the token: %v", err)
	}
}
