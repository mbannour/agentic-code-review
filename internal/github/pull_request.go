package github

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type PullRequest struct {
	Owner  string
	Repo   string
	Number int
}

func ParsePullRequestURL(rawURL string) (PullRequest, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return PullRequest{}, fmt.Errorf("parse pull request URL: %w", err)
	}

	if parsedURL.Scheme != "https" {
		return PullRequest{}, fmt.Errorf("unsupported URL scheme %q", parsedURL.Scheme)
	}

	if parsedURL.Host != "github.com" {
		return PullRequest{}, fmt.Errorf("unsupported GitHub host %q", parsedURL.Host)
	}

	parts := strings.Split(strings.Trim(parsedURL.Path, "/"), "/")

	if len(parts) != 4 {
		return PullRequest{}, fmt.Errorf("invalid GitHub pull request URL path %q", parsedURL.Path)
	}

	if parts[0] == "" {
		return PullRequest{}, fmt.Errorf("missing repository owner in URL path %q", parsedURL.Path)
	}

	if parts[1] == "" {
		return PullRequest{}, fmt.Errorf("missing repository name in URL path %q", parsedURL.Path)
	}

	if parts[2] != "pull" {
		return PullRequest{}, fmt.Errorf("URL is not a GitHub pull request")
	}

	number, err := strconv.Atoi(parts[3])
	if err != nil {
		return PullRequest{}, fmt.Errorf("invalid pull request number %q", parts[3])
	}

	if number <= 0 {
		return PullRequest{}, fmt.Errorf("pull request number must be greater than zero")
	}

	return PullRequest{
		Owner:  parts[0],
		Repo:   parts[1],
		Number: number,
	}, nil
}

// String renders the pull request in owner/repo#number form. It is safe for
// logs and error messages.
func (pr PullRequest) String() string {
	return fmt.Sprintf("%s/%s#%d", pr.Owner, pr.Repo, pr.Number)
}
