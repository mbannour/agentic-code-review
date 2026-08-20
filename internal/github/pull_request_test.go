package github

import (
	"strings"
	"testing"
)

func TestParsePullRequestURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want PullRequest
	}{
		{
			name: "canonical PR URL",
			url:  "https://github.com/acme/payments/pull/123",
			want: PullRequest{Owner: "acme", Repo: "payments", Number: 123},
		},
		{
			name: "trailing slash",
			url:  "https://github.com/acme/payments/pull/123/",
			want: PullRequest{Owner: "acme", Repo: "payments", Number: 123},
		},
		{
			name: "query string is ignored",
			url:  "https://github.com/acme/payments/pull/123?w=1",
			want: PullRequest{Owner: "acme", Repo: "payments", Number: 123},
		},
		{
			name: "fragment is ignored",
			url:  "https://github.com/acme/payments/pull/123#discussion_r1",
			want: PullRequest{Owner: "acme", Repo: "payments", Number: 123},
		},
		{
			name: "owner and repo with punctuation",
			url:  "https://github.com/acme-corp/payments.api/pull/7",
			want: PullRequest{Owner: "acme-corp", Repo: "payments.api", Number: 7},
		},
		{
			name: "large PR number",
			url:  "https://github.com/acme/payments/pull/999999",
			want: PullRequest{Owner: "acme", Repo: "payments", Number: 999999},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePullRequestURL(tt.url)
			if err != nil {
				t.Fatalf("ParsePullRequestURL(%q) returned error: %v", tt.url, err)
			}
			if got != tt.want {
				t.Errorf("ParsePullRequestURL(%q) = %+v, want %+v", tt.url, got, tt.want)
			}
		})
	}
}

func TestParsePullRequestURLErrors(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr string // substring the error message must contain
	}{
		{
			name:    "gitlab host",
			url:     "https://gitlab.com/acme/payments/pull/123",
			wantErr: `unsupported GitHub host "gitlab.com"`,
		},
		{
			name:    "github issue URL",
			url:     "https://github.com/acme/payments/issues/123",
			wantErr: "not a GitHub pull request",
		},
		{
			name:    "non-numeric PR number",
			url:     "https://github.com/acme/payments/pull/abc",
			wantErr: `invalid pull request number "abc"`,
		},
		{
			name:    "PR number zero",
			url:     "https://github.com/acme/payments/pull/0",
			wantErr: "must be greater than zero",
		},
		{
			name:    "negative PR number",
			url:     "https://github.com/acme/payments/pull/-1",
			wantErr: "must be greater than zero",
		},
		{
			name:    "http scheme",
			url:     "http://github.com/acme/payments/pull/123",
			wantErr: `unsupported URL scheme "http"`,
		},
		{
			name:    "no scheme",
			url:     "github.com/acme/payments/pull/123",
			wantErr: `unsupported URL scheme ""`,
		},
		{
			name:    "path too short",
			url:     "https://github.com/acme/payments/pull",
			wantErr: "invalid GitHub pull request URL path",
		},
		{
			name:    "path too long",
			url:     "https://github.com/acme/payments/pull/123/files",
			wantErr: "invalid GitHub pull request URL path",
		},
		{
			name:    "empty path",
			url:     "https://github.com/",
			wantErr: "invalid GitHub pull request URL path",
		},
		{
			// A doubled slash collapses when the path is trimmed, so this is
			// rejected on path shape rather than by the owner check.
			name:    "missing owner",
			url:     "https://github.com//payments/pull/123",
			wantErr: "invalid GitHub pull request URL path",
		},
		{
			name:    "missing repo",
			url:     "https://github.com/acme//pull/123",
			wantErr: "missing repository name",
		},
		{
			name:    "malformed URL",
			url:     "https://github.com/acme/payments/pull/12 3",
			wantErr: "invalid pull request number",
		},
		{
			name:    "unparseable URL",
			url:     "https://gith ub.com/%zz",
			wantErr: "parse pull request URL",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePullRequestURL(tt.url)
			if err == nil {
				t.Fatalf("ParsePullRequestURL(%q) = %+v, want error", tt.url, got)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ParsePullRequestURL(%q) error = %q, want it to contain %q", tt.url, err, tt.wantErr)
			}
			if got != (PullRequest{}) {
				t.Errorf("ParsePullRequestURL(%q) = %+v on error, want zero value", tt.url, got)
			}
		})
	}
}
