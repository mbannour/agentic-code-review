package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "ghp_test_token_value"

// prJSON is a trimmed copy of a real GitHub pull request payload.
const prJSON = `{
  "number": 123,
  "title": "Add payment retry",
  "body": "Implements PAY-431",
  "state": "open",
  "draft": false,
  "html_url": "https://github.com/acme/payments/pull/123",
  "user": { "login": "alice" },
  "base": { "ref": "main" },
  "head": { "ref": "feature/PAY-431", "sha": "abc123" },
  "mergeable": true,
  "additions": 42
}`

// newTestClient starts an httptest server with handler and returns a Client
// pointed at it.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return NewClientWithBaseURL(testToken, srv.URL, srv.Client())
}

var testPRRef = PullRequest{Owner: "acme", Repo: "payments", Number: 123}

func TestGetPullRequest(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, prJSON)
	})

	got, err := client.GetPullRequest(context.Background(), testPRRef)
	if err != nil {
		t.Fatalf("GetPullRequest() returned error: %v", err)
	}

	want := PullRequestDetails{
		Number:      123,
		Title:       "Add payment retry",
		Body:        "Implements PAY-431",
		State:       "open",
		Draft:       false,
		HTMLURL:     "https://github.com/acme/payments/pull/123",
		BaseBranch:  "main",
		HeadBranch:  "feature/PAY-431",
		HeadSHA:     "abc123",
		AuthorLogin: "alice",
	}

	if got != want {
		t.Errorf("GetPullRequest() = %+v\nwant %+v", got, want)
	}
}

// TestGetPullRequestRequest covers the request path, method, and every required
// header, including that the token is sent as a bearer credential.
func TestGetPullRequestRequest(t *testing.T) {
	var got *http.Request

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		fmt.Fprint(w, prJSON)
	})

	if _, err := client.GetPullRequest(context.Background(), testPRRef); err != nil {
		t.Fatalf("GetPullRequest() returned error: %v", err)
	}
	if got == nil {
		t.Fatal("handler never ran")
	}

	if got.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", got.Method)
	}
	if want := "/repos/acme/payments/pulls/123"; got.URL.Path != want {
		t.Errorf("path = %q, want %q", got.URL.Path, want)
	}

	headers := []struct {
		name string
		want string
	}{
		{"Accept", "application/vnd.github+json"},
		{"Authorization", "Bearer " + testToken},
		{"X-GitHub-Api-Version", apiVersion},
		{"User-Agent", userAgent},
	}
	for _, h := range headers {
		if got := got.Header.Get(h.name); got != h.want {
			t.Errorf("header %s = %q, want %q", h.name, got, h.want)
		}
	}
}

// TestGetPullRequestPathEscaping checks owner/repo values are escaped into the
// path rather than concatenated blindly.
func TestGetPullRequestPathEscaping(t *testing.T) {
	var path string

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		fmt.Fprint(w, prJSON)
	})

	pr := PullRequest{Owner: "acme corp", Repo: "pay/ments", Number: 7}
	if _, err := client.GetPullRequest(context.Background(), pr); err != nil {
		t.Fatalf("GetPullRequest() returned error: %v", err)
	}

	if want := "/repos/acme%20corp/pay%2Fments/pulls/7"; path != want {
		t.Errorf("escaped path = %q, want %q", path, want)
	}
}

// TestGetPullRequestNestedFields verifies the nested base/head/user objects map
// onto the flat domain type.
func TestGetPullRequestNestedFields(t *testing.T) {
	tests := []struct {
		name string
		body string
		want PullRequestDetails
	}{
		{
			name: "all nested fields present",
			body: prJSON,
			want: PullRequestDetails{
				Number: 123, Title: "Add payment retry", Body: "Implements PAY-431",
				State: "open", HTMLURL: "https://github.com/acme/payments/pull/123",
				BaseBranch: "main", HeadBranch: "feature/PAY-431", HeadSHA: "abc123",
				AuthorLogin: "alice",
			},
		},
		{
			name: "draft pull request against a release branch",
			body: `{"number":9,"title":"WIP","state":"open","draft":true,
			        "user":{"login":"bob"},
			        "base":{"ref":"release/2.0"},
			        "head":{"ref":"wip","sha":"deadbeef"}}`,
			want: PullRequestDetails{
				Number: 9, Title: "WIP", State: "open", Draft: true,
				BaseBranch: "release/2.0", HeadBranch: "wip", HeadSHA: "deadbeef",
				AuthorLogin: "bob",
			},
		},
		{
			name: "nested objects absent leaves fields empty",
			body: `{"number":5,"title":"No nesting","state":"closed"}`,
			want: PullRequestDetails{Number: 5, Title: "No nesting", State: "closed"},
		},
		{
			name: "nested objects explicitly null",
			body: `{"number":6,"title":"Nulls","state":"open",
			        "user":null,"base":null,"head":null}`,
			want: PullRequestDetails{Number: 6, Title: "Nulls", State: "open"},
		},
		{
			name: "unknown fields are ignored",
			body: `{"number":8,"title":"Extra","state":"open",
			        "user":{"login":"carol","id":42,"site_admin":false},
			        "base":{"ref":"main","repo":{"full_name":"acme/payments"}},
			        "head":{"ref":"topic","sha":"cafe","label":"acme:topic"},
			        "some_future_field":{"nested":[1,2,3]}}`,
			want: PullRequestDetails{
				Number: 8, Title: "Extra", State: "open",
				BaseBranch: "main", HeadBranch: "topic", HeadSHA: "cafe",
				AuthorLogin: "carol",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tt.body)
			})

			got, err := client.GetPullRequest(context.Background(), testPRRef)
			if err != nil {
				t.Fatalf("GetPullRequest() returned error: %v", err)
			}
			if got != tt.want {
				t.Errorf("GetPullRequest() = %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestGetPullRequestStatusErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantIs     error  // sentinel the error must match, nil if none
		wantMsg    string // substring expected in the error text
		wantAPIMsg string // expected APIError.Message
	}{
		{
			name:       "401 unauthorized",
			status:     http.StatusUnauthorized,
			body:       `{"message":"Bad credentials"}`,
			wantIs:     ErrUnauthorized,
			wantMsg:    "authentication failed",
			wantAPIMsg: "Bad credentials",
		},
		{
			name:       "403 forbidden",
			status:     http.StatusForbidden,
			body:       `{"message":"API rate limit exceeded"}`,
			wantIs:     ErrForbidden,
			wantMsg:    "access denied",
			wantAPIMsg: "API rate limit exceeded",
		},
		{
			name:       "404 not found",
			status:     http.StatusNotFound,
			body:       `{"message":"Not Found"}`,
			wantIs:     ErrNotFound,
			wantMsg:    "not found",
			wantAPIMsg: "Not Found",
		},
		{
			name:    "500 server error",
			status:  http.StatusInternalServerError,
			body:    `{"message":"Server Error"}`,
			wantMsg: "status 500",
		},
		{
			name:    "422 unprocessable",
			status:  http.StatusUnprocessableEntity,
			body:    `{"message":"Validation Failed"}`,
			wantMsg: "status 422",
		},
		{
			name:    "error body is not JSON",
			status:  http.StatusBadGateway,
			body:    `<html>gateway blew up</html>`,
			wantMsg: "status 502",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			})

			got, err := client.GetPullRequest(context.Background(), testPRRef)
			if err == nil {
				t.Fatalf("GetPullRequest() = %+v, want error", got)
			}
			if got != (PullRequestDetails{}) {
				t.Errorf("GetPullRequest() = %+v on error, want zero value", got)
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Errorf("errors.Is(err, %v) = false; err = %v", tt.wantIs, err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantMsg)
			}

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error %v is not an *APIError", err)
			}
			if apiErr.StatusCode != tt.status {
				t.Errorf("APIError.StatusCode = %d, want %d", apiErr.StatusCode, tt.status)
			}
			if tt.wantAPIMsg != "" && apiErr.Message != tt.wantAPIMsg {
				t.Errorf("APIError.Message = %q, want %q", apiErr.Message, tt.wantAPIMsg)
			}

			// The response body must not leak wholesale into the error.
			if strings.Contains(err.Error(), "gateway blew up") {
				t.Error("error text contains the raw response body")
			}
		})
	}
}

func TestGetPullRequestInvalidJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "truncated object", body: `{"number": 123, "title":`},
		{name: "not json at all", body: `<html>hello</html>`},
		{name: "wrong type for number", body: `{"number": "one-two-three"}`},
		{name: "top level array", body: `[{"number":123}]`},
		{name: "empty body", body: ``},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tt.body)
			})

			got, err := client.GetPullRequest(context.Background(), testPRRef)
			if err == nil {
				t.Fatalf("GetPullRequest() = %+v, want decode error", got)
			}
			if !strings.Contains(err.Error(), "decode pull request acme/payments#123 response") {
				t.Errorf("error = %q, want it to mention decoding", err)
			}
		})
	}
}

// TestGetPullRequestContextCanceled checks that cancelling the context aborts
// the in-flight request and the error is wrapped.
func TestGetPullRequestContextCanceled(t *testing.T) {
	release := make(chan struct{})
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-release // hold the response open until the test cancels
	})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the call

	_, err := client.GetPullRequest(ctx, testPRRef)
	if err == nil {
		t.Fatal("GetPullRequest() = nil error, want context cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; err = %v", err)
	}
	if !strings.Contains(err.Error(), "request pull request acme/payments#123") {
		t.Errorf("error = %q, want it to name the pull request", err)
	}
}

// TestGetPullRequestNetworkError covers an unreachable server.
func TestGetPullRequestNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := NewClientWithBaseURL(testToken, srv.URL, srv.Client())
	srv.Close() // nothing is listening any more

	_, err := client.GetPullRequest(context.Background(), testPRRef)
	if err == nil {
		t.Fatal("GetPullRequest() = nil error, want network error")
	}
	if !strings.Contains(err.Error(), "request pull request") {
		t.Errorf("error = %q, want it to be a wrapped request error", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Error("error text contains the token")
	}
}

// TestErrorsNeverContainToken asserts the token stays out of every error path.
func TestErrorsNeverContainToken(t *testing.T) {
	statuses := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError}

	for _, status := range statuses {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				// A hostile server echoing the credential back must not make
				// it into our error.
				fmt.Fprintf(w, `{"message":"failed for token %s"}`, r.Header.Get("Authorization"))
			})

			_, err := client.GetPullRequest(context.Background(), testPRRef)
			if err == nil {
				t.Fatal("want error")
			}
			if strings.Contains(err.Error(), testToken) {
				t.Errorf("error %q contains the token", err)
			}
		})
	}
}

func TestGetPullRequestMissingToken(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made without a token")
	})
	client.token = ""

	_, err := client.GetPullRequest(context.Background(), testPRRef)
	if !errors.Is(err, ErrMissingToken) {
		t.Errorf("errors.Is(err, ErrMissingToken) = false; err = %v", err)
	}
}

func TestNewClientFromEnv(t *testing.T) {
	t.Run("token present", func(t *testing.T) {
		t.Setenv(TokenEnvVar, testToken)

		client, err := NewClientFromEnv()
		if err != nil {
			t.Fatalf("NewClientFromEnv() returned error: %v", err)
		}
		if client.token != testToken {
			t.Error("client did not pick up the token from the environment")
		}
		if client.baseURL != DefaultBaseURL {
			t.Errorf("baseURL = %q, want %q", client.baseURL, DefaultBaseURL)
		}
	})

	for _, value := range []string{"", "   "} {
		t.Run(fmt.Sprintf("token %q is rejected", value), func(t *testing.T) {
			t.Setenv(TokenEnvVar, value)

			_, err := NewClientFromEnv()
			if !errors.Is(err, ErrMissingToken) {
				t.Errorf("errors.Is(err, ErrMissingToken) = false; err = %v", err)
			}
			if !strings.Contains(err.Error(), TokenEnvVar) {
				t.Errorf("error = %q, want it to name %s", err, TokenEnvVar)
			}
		})
	}
}

func TestNewClientDefaults(t *testing.T) {
	client := NewClient(testToken)

	if client.baseURL != DefaultBaseURL {
		t.Errorf("baseURL = %q, want %q", client.baseURL, DefaultBaseURL)
	}
	if client.httpClient == nil {
		t.Fatal("httpClient is nil")
	}
	if client.httpClient.Timeout != defaultTimeout {
		t.Errorf("timeout = %v, want %v", client.httpClient.Timeout, defaultTimeout)
	}

	t.Run("trailing slash in base URL is trimmed", func(t *testing.T) {
		c := NewClientWithBaseURL(testToken, "https://github.example.com/api/v3/", nil)
		if want := "https://github.example.com/api/v3"; c.baseURL != want {
			t.Errorf("baseURL = %q, want %q", c.baseURL, want)
		}
	})

	t.Run("empty base URL falls back to the default", func(t *testing.T) {
		c := NewClientWithBaseURL(testToken, "", nil)
		if c.baseURL != DefaultBaseURL {
			t.Errorf("baseURL = %q, want %q", c.baseURL, DefaultBaseURL)
		}
	})
}
