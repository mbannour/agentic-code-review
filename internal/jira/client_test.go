package jira

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testEmail = "developer@company.com"
	testToken = "jira_api_token_value"
	testKey   = TicketKey("PAY-431")
)

// issueJSON is a trimmed copy of a real Jira Cloud issue payload.
const issueJSON = `{
  "id": "10042",
  "key": "PAY-431",
  "fields": {
    "summary": "Retry failed card authorizations",
    "description": {
      "type": "doc",
      "version": 1,
      "content": [
        {"type": "paragraph", "content": [{"type": "text", "text": "Retry failed payments"}]}
      ]
    },
    "status": {"name": "In Progress", "id": "3"},
    "issuetype": {"name": "Story", "id": "10001"},
    "priority": {"name": "High", "id": "2"},
    "labels": ["payments", "reliability"],
    "parent": {"key": "PAY-400", "id": "10000"}
  }
}`

// newTestClient starts an httptest server with handler and returns a Client
// pointed at it.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return NewClientWithHTTPClient(srv.URL, testEmail, testToken, srv.Client())
}

func TestGetIssue(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, issueJSON)
	})

	got, err := client.GetIssue(context.Background(), testKey)
	if err != nil {
		t.Fatalf("GetIssue() returned error: %v", err)
	}

	if got.Key != "PAY-431" {
		t.Errorf("Key = %q, want %q", got.Key, "PAY-431")
	}
	if want := "Retry failed card authorizations"; got.Summary != want {
		t.Errorf("Summary = %q, want %q", got.Summary, want)
	}
	if want := "Retry failed payments"; got.Description != want {
		t.Errorf("Description = %q, want %q", got.Description, want)
	}
	if got.Status != "In Progress" {
		t.Errorf("Status = %q, want %q", got.Status, "In Progress")
	}
	if got.IssueType != "Story" {
		t.Errorf("IssueType = %q, want %q", got.IssueType, "Story")
	}
	if got.Priority != "High" {
		t.Errorf("Priority = %q, want %q", got.Priority, "High")
	}
	if got.ParentKey != "PAY-400" {
		t.Errorf("ParentKey = %q, want %q", got.ParentKey, "PAY-400")
	}
	if strings.Join(got.Labels, ",") != "payments,reliability" {
		t.Errorf("Labels = %v, want [payments reliability]", got.Labels)
	}
}

// TestGetIssueRequest covers the path, the fields query parameter, the method,
// Basic Auth credentials, and the fixed headers.
func TestGetIssueRequest(t *testing.T) {
	var got *http.Request

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		fmt.Fprint(w, issueJSON)
	})

	if _, err := client.GetIssue(context.Background(), testKey); err != nil {
		t.Fatalf("GetIssue() returned error: %v", err)
	}
	if got == nil {
		t.Fatal("handler never ran")
	}

	if got.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", got.Method)
	}
	if want := "/rest/api/3/issue/PAY-431"; got.URL.Path != want {
		t.Errorf("path = %q, want %q", got.URL.Path, want)
	}

	wantFields := "summary,description,status,issuetype,priority,labels,parent"
	if fields := got.URL.Query().Get("fields"); fields != wantFields {
		t.Errorf("fields = %q, want %q", fields, wantFields)
	}

	user, pass, ok := got.BasicAuth()
	if !ok {
		t.Fatal("request carries no Basic Auth credentials")
	}
	if user != testEmail {
		t.Errorf("basic auth username = %q, want %q", user, testEmail)
	}
	if pass != testToken {
		t.Errorf("basic auth password = %q, want the token", pass)
	}

	if want := "application/json"; got.Header.Get("Accept") != want {
		t.Errorf("Accept = %q, want %q", got.Header.Get("Accept"), want)
	}
	if got.Header.Get("User-Agent") != userAgent {
		t.Errorf("User-Agent = %q, want %q", got.Header.Get("User-Agent"), userAgent)
	}
}

// TestGetIssueOnlyRequestsNeededFields guards against fetching every Jira field.
func TestGetIssueOnlyRequestsNeededFields(t *testing.T) {
	var query string

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		fmt.Fprint(w, issueJSON)
	})

	if _, err := client.GetIssue(context.Background(), testKey); err != nil {
		t.Fatalf("GetIssue() returned error: %v", err)
	}

	if !strings.Contains(query, "fields=") {
		t.Errorf("query %q does not restrict fields", query)
	}
	for _, unwanted := range []string{"*all", "comment", "worklog", "attachment", "changelog"} {
		if strings.Contains(query, unwanted) {
			t.Errorf("query %q requests %q, which this step does not need", query, unwanted)
		}
	}
}

func TestGetIssueFieldMapping(t *testing.T) {
	tests := []struct {
		name string
		body string
		want Issue
	}{
		{
			name: "all fields present",
			body: issueJSON,
			want: Issue{
				Key: "PAY-431", Summary: "Retry failed card authorizations",
				Description: "Retry failed payments", Status: "In Progress",
				IssueType: "Story", Priority: "High", ParentKey: "PAY-400",
				Labels: []string{"payments", "reliability"},
			},
		},
		{
			name: "parent is null",
			body: `{"key":"PAY-431","fields":{"summary":"S","status":{"name":"To Do"},
			        "issuetype":{"name":"Bug"},"priority":{"name":"Low"},"labels":[],"parent":null}}`,
			want: Issue{Key: "PAY-431", Summary: "S", Status: "To Do", IssueType: "Bug", Priority: "Low", Labels: []string{}},
		},
		{
			name: "parent key absent",
			body: `{"key":"PAY-431","fields":{"summary":"S","status":{"name":"To Do"},"issuetype":{"name":"Bug"}}}`,
			want: Issue{Key: "PAY-431", Summary: "S", Status: "To Do", IssueType: "Bug"},
		},
		{
			name: "description is null",
			body: `{"key":"PAY-431","fields":{"summary":"S","description":null,"status":{"name":"Done"}}}`,
			want: Issue{Key: "PAY-431", Summary: "S", Status: "Done"},
		},
		{
			name: "description is a plain string",
			body: `{"key":"PAY-431","fields":{"summary":"S","description":"Legacy plain text"}}`,
			want: Issue{Key: "PAY-431", Summary: "S", Description: "Legacy plain text"},
		},
		{
			name: "priority is null",
			body: `{"key":"PAY-431","fields":{"summary":"S","priority":null}}`,
			want: Issue{Key: "PAY-431", Summary: "S"},
		},
		{
			name: "status is null",
			body: `{"key":"PAY-431","fields":{"summary":"S","status":null}}`,
			want: Issue{Key: "PAY-431", Summary: "S"},
		},
		{
			name: "issue type is null",
			body: `{"key":"PAY-431","fields":{"summary":"S","issuetype":null}}`,
			want: Issue{Key: "PAY-431", Summary: "S"},
		},
		{
			name: "labels absent",
			body: `{"key":"PAY-431","fields":{"summary":"S"}}`,
			want: Issue{Key: "PAY-431", Summary: "S"},
		},
		{
			name: "labels null",
			body: `{"key":"PAY-431","fields":{"summary":"S","labels":null}}`,
			want: Issue{Key: "PAY-431", Summary: "S"},
		},
		{
			name: "many labels keep their order",
			body: `{"key":"PAY-431","fields":{"summary":"S","labels":["c","a","b"]}}`,
			want: Issue{Key: "PAY-431", Summary: "S", Labels: []string{"c", "a", "b"}},
		},
		{
			name: "summary is trimmed",
			body: `{"key":"PAY-431","fields":{"summary":"  Retry failed payments  "}}`,
			want: Issue{Key: "PAY-431", Summary: "Retry failed payments"},
		},
		{
			name: "empty fields object",
			body: `{"key":"PAY-431","fields":{}}`,
			want: Issue{Key: "PAY-431"},
		},
		{
			name: "no fields key at all",
			body: `{"key":"PAY-431"}`,
			want: Issue{Key: "PAY-431"},
		},
		{
			name: "response key missing falls back to the requested key",
			body: `{"fields":{"summary":"S"}}`,
			want: Issue{Key: "PAY-431", Summary: "S"},
		},
		{
			name: "unknown fields are ignored",
			body: `{"key":"PAY-431","id":"10042","self":"https://x/rest/api/3/issue/10042",
			        "fields":{"summary":"S","customfield_10001":{"deep":{"nested":true}},
			        "comment":{"comments":[]},"votes":{"votes":3}}}`,
			want: Issue{Key: "PAY-431", Summary: "S"},
		},
		{
			name: "adf description with a list",
			body: `{"key":"PAY-431","fields":{"summary":"S","description":{"type":"doc","version":1,"content":[
			        {"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Acceptance criteria"}]},
			        {"type":"bulletList","content":[
			          {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Retry twice"}]}]},
			          {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Log failures"}]}]}]}]}}}`,
			want: Issue{
				Key: "PAY-431", Summary: "S",
				Description: "Acceptance criteria\n\n- Retry twice\n- Log failures",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tt.body)
			})

			got, err := client.GetIssue(context.Background(), testKey)
			if err != nil {
				t.Fatalf("GetIssue() returned error: %v", err)
			}

			if got.Key != tt.want.Key {
				t.Errorf("Key = %q, want %q", got.Key, tt.want.Key)
			}
			if got.Summary != tt.want.Summary {
				t.Errorf("Summary = %q, want %q", got.Summary, tt.want.Summary)
			}
			if got.Description != tt.want.Description {
				t.Errorf("Description = %q, want %q", got.Description, tt.want.Description)
			}
			if got.Status != tt.want.Status {
				t.Errorf("Status = %q, want %q", got.Status, tt.want.Status)
			}
			if got.IssueType != tt.want.IssueType {
				t.Errorf("IssueType = %q, want %q", got.IssueType, tt.want.IssueType)
			}
			if got.Priority != tt.want.Priority {
				t.Errorf("Priority = %q, want %q", got.Priority, tt.want.Priority)
			}
			if got.ParentKey != tt.want.ParentKey {
				t.Errorf("ParentKey = %q, want %q", got.ParentKey, tt.want.ParentKey)
			}
			if strings.Join(got.Labels, ",") != strings.Join(tt.want.Labels, ",") {
				t.Errorf("Labels = %v, want %v", got.Labels, tt.want.Labels)
			}
		})
	}
}

func TestGetIssueKeyEscaping(t *testing.T) {
	var path string

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		fmt.Fprint(w, issueJSON)
	})

	if _, err := client.GetIssue(context.Background(), TicketKey("PAY 431/x")); err != nil {
		t.Fatalf("GetIssue() returned error: %v", err)
	}

	if want := "/rest/api/3/issue/PAY%20431%2Fx"; path != want {
		t.Errorf("escaped path = %q, want %q", path, want)
	}
}

func TestGetIssueStatusErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantIs     error
		wantMsg    string
		wantAPIMsg string
	}{
		{
			name:       "401 unauthorized",
			status:     http.StatusUnauthorized,
			body:       `{"errorMessages":["Client must be authenticated to access this resource."]}`,
			wantIs:     ErrUnauthorized,
			wantMsg:    "authentication failed",
			wantAPIMsg: "Client must be authenticated to access this resource.",
		},
		{
			name:       "403 forbidden",
			status:     http.StatusForbidden,
			body:       `{"errorMessages":["You do not have permission to view this issue."]}`,
			wantIs:     ErrForbidden,
			wantMsg:    "access denied",
			wantAPIMsg: "You do not have permission to view this issue.",
		},
		{
			name:       "404 not found",
			status:     http.StatusNotFound,
			body:       `{"errorMessages":["Issue does not exist"],"errors":{}}`,
			wantIs:     ErrIssueNotFound,
			wantMsg:    "issue not found",
			wantAPIMsg: "Issue does not exist",
		},
		{
			name:    "429 rate limited",
			status:  http.StatusTooManyRequests,
			body:    `{"errorMessages":["Rate limit exceeded"]}`,
			wantIs:  ErrRateLimited,
			wantMsg: "rate limited",
		},
		{
			name:    "500 server error",
			status:  http.StatusInternalServerError,
			body:    `{"errorMessages":["Internal server error"]}`,
			wantIs:  ErrServer,
			wantMsg: "server error",
		},
		{
			name:    "503 service unavailable",
			status:  http.StatusServiceUnavailable,
			body:    `{"errorMessages":["Temporarily unavailable"]}`,
			wantIs:  ErrServer,
			wantMsg: "status 503",
		},
		{
			name:    "400 bad request with field errors",
			status:  http.StatusBadRequest,
			body:    `{"errorMessages":[],"errors":{"fields":"bad","project":"missing"}}`,
			wantMsg: "invalid fields: fields, project",
		},
		{
			name:    "error body is not JSON",
			status:  http.StatusBadGateway,
			body:    `<html>proxy exploded</html>`,
			wantIs:  ErrServer,
			wantMsg: "status 502",
		},
		{
			name:    "error body is empty",
			status:  http.StatusNotFound,
			body:    ``,
			wantIs:  ErrIssueNotFound,
			wantMsg: "issue not found",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			})

			got, err := client.GetIssue(context.Background(), testKey)
			if err == nil {
				t.Fatalf("GetIssue() = %+v, want error", got)
			}
			if got.Key != "" || got.Summary != "" {
				t.Errorf("GetIssue() = %+v on error, want zero value", got)
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
			if strings.Contains(err.Error(), "proxy exploded") {
				t.Error("error text contains the raw response body")
			}
		})
	}
}

// TestErrorsNeverContainCredentials asserts neither the token nor the email
// survives into an error, even when the server echoes them back.
func TestErrorsNeverContainCredentials(t *testing.T) {
	statuses := []int{
		http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusTooManyRequests, http.StatusInternalServerError,
	}

	for _, status := range statuses {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				user, pass, _ := r.BasicAuth()
				fmt.Fprintf(w, `{"errorMessages":["rejected %s / %s / %s"]}`,
					user, pass, r.Header.Get("Authorization"))
			})

			_, err := client.GetIssue(context.Background(), testKey)
			if err == nil {
				t.Fatal("want error")
			}
			if strings.Contains(err.Error(), testToken) {
				t.Errorf("error %q contains the token", err)
			}
			if strings.Contains(err.Error(), testEmail) {
				t.Errorf("error %q contains the email", err)
			}
		})
	}
}

func TestGetIssueMalformedJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "truncated object", body: `{"key":"PAY-431","fields":`},
		{name: "not json at all", body: `<html>hello</html>`},
		{name: "array instead of object", body: `[{"key":"PAY-431"}]`},
		{name: "wrong type for summary", body: `{"key":"PAY-431","fields":{"summary":{"nested":true}}}`},
		{name: "wrong type for labels", body: `{"key":"PAY-431","fields":{"labels":"payments"}}`},
		{name: "wrong type for status", body: `{"key":"PAY-431","fields":{"status":"In Progress"}}`},
		{name: "description is a number", body: `{"key":"PAY-431","fields":{"description":42}}`},
		{name: "empty body", body: ``},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tt.body)
			})

			got, err := client.GetIssue(context.Background(), testKey)
			if err == nil {
				t.Fatalf("GetIssue() = %+v, want decode error", got)
			}
			if !strings.Contains(err.Error(), "decode Jira issue") {
				t.Errorf("error = %q, want it to mention decoding", err)
			}
		})
	}
}

func TestGetIssueContextCanceled(t *testing.T) {
	release := make(chan struct{})
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
	})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.GetIssue(ctx, testKey)
	if err == nil {
		t.Fatal("GetIssue() = nil error, want context cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; err = %v", err)
	}
	if !strings.Contains(err.Error(), "request Jira issue PAY-431") {
		t.Errorf("error = %q, want it to name the issue", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Error("error contains the token")
	}
}

func TestGetIssueNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := NewClientWithHTTPClient(srv.URL, testEmail, testToken, srv.Client())
	srv.Close()

	_, err := client.GetIssue(context.Background(), testKey)
	if err == nil {
		t.Fatal("GetIssue() = nil error, want network error")
	}
	if !strings.Contains(err.Error(), "request Jira issue") {
		t.Errorf("error = %q, want a wrapped request error", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Error("error contains the token")
	}
}

func TestGetIssueMissingConfig(t *testing.T) {
	tests := []struct {
		name        string
		baseURL     string
		email       string
		token       string
		wantMissing []string
	}{
		{name: "no base URL", email: testEmail, token: testToken, wantMissing: []string{BaseURLEnvVar}},
		{name: "no email", baseURL: "https://x.atlassian.net", token: testToken, wantMissing: []string{EmailEnvVar}},
		{name: "no token", baseURL: "https://x.atlassian.net", email: testEmail, wantMissing: []string{TokenEnvVar}},
		{
			name:        "nothing configured",
			wantMissing: []string{BaseURLEnvVar, EmailEnvVar, TokenEnvVar},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.baseURL, tt.email, tt.token)

			_, err := client.GetIssue(context.Background(), testKey)
			if !errors.Is(err, ErrMissingConfig) {
				t.Fatalf("errors.Is(err, ErrMissingConfig) = false; err = %v", err)
			}
			for _, name := range tt.wantMissing {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("error %q does not name the missing variable %s", err, name)
				}
			}
		})
	}
}

func TestGetIssueEmptyKey(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made for an empty key")
	})

	if _, err := client.GetIssue(context.Background(), ""); err == nil {
		t.Error("GetIssue(\"\") = nil error, want an error")
	}
}

func TestNewClientBaseURLNormalization(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "no trailing slash", in: "https://company.atlassian.net", want: "https://company.atlassian.net"},
		{name: "trailing slash", in: "https://company.atlassian.net/", want: "https://company.atlassian.net"},
		{name: "several trailing slashes", in: "https://company.atlassian.net///", want: "https://company.atlassian.net"},
		{name: "surrounding whitespace", in: "  https://company.atlassian.net/  ", want: "https://company.atlassian.net"},
		{name: "path prefix is preserved", in: "https://proxy.example.com/jira/", want: "https://proxy.example.com/jira"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := NewClient(tt.in, testEmail, testToken).baseURL; got != tt.want {
				t.Errorf("baseURL = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBaseURLTrailingSlashHitsSamePath proves both base URL forms produce an
// identical request path.
func TestBaseURLTrailingSlashHitsSamePath(t *testing.T) {
	var paths []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		fmt.Fprint(w, issueJSON)
	}))
	defer srv.Close()

	for _, base := range []string{srv.URL, srv.URL + "/"} {
		client := NewClientWithHTTPClient(base, testEmail, testToken, srv.Client())
		if _, err := client.GetIssue(context.Background(), testKey); err != nil {
			t.Fatalf("GetIssue() with base %q returned error: %v", base, err)
		}
	}

	if len(paths) != 2 || paths[0] != paths[1] {
		t.Errorf("paths = %v, want two identical paths", paths)
	}
}

func TestNewClientFromEnv(t *testing.T) {
	t.Run("all variables set", func(t *testing.T) {
		t.Setenv(BaseURLEnvVar, "https://company.atlassian.net/")
		t.Setenv(EmailEnvVar, testEmail)
		t.Setenv(TokenEnvVar, testToken)

		client, err := NewClientFromEnv()
		if err != nil {
			t.Fatalf("NewClientFromEnv() returned error: %v", err)
		}
		if want := "https://company.atlassian.net"; client.baseURL != want {
			t.Errorf("baseURL = %q, want %q", client.baseURL, want)
		}
		if client.email != testEmail || client.token != testToken {
			t.Error("credentials were not read from the environment")
		}
		if client.httpClient == nil || client.httpClient.Timeout != defaultTimeout {
			t.Errorf("http client timeout = %v, want %v", client.httpClient.Timeout, defaultTimeout)
		}
	})

	tests := []struct {
		name        string
		baseURL     string
		email       string
		token       string
		wantMissing []string
	}{
		{name: "base URL unset", email: testEmail, token: testToken, wantMissing: []string{BaseURLEnvVar}},
		{name: "email unset", baseURL: "https://x.atlassian.net", token: testToken, wantMissing: []string{EmailEnvVar}},
		{name: "token unset", baseURL: "https://x.atlassian.net", email: testEmail, wantMissing: []string{TokenEnvVar}},
		{name: "token is whitespace", baseURL: "https://x.atlassian.net", email: testEmail, token: "   ", wantMissing: []string{TokenEnvVar}},
		{
			name:        "nothing set names every variable",
			wantMissing: []string{BaseURLEnvVar, EmailEnvVar, TokenEnvVar},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(BaseURLEnvVar, tt.baseURL)
			t.Setenv(EmailEnvVar, tt.email)
			t.Setenv(TokenEnvVar, tt.token)

			_, err := NewClientFromEnv()
			if !errors.Is(err, ErrMissingConfig) {
				t.Fatalf("errors.Is(err, ErrMissingConfig) = false; err = %v", err)
			}
			for _, name := range tt.wantMissing {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("error %q does not name %s", err, name)
				}
			}
			if strings.Contains(err.Error(), testToken) {
				t.Error("error contains the token")
			}
		})
	}
}
