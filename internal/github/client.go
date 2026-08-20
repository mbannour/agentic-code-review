package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the public GitHub REST API root.
	DefaultBaseURL = "https://api.github.com"

	// apiVersion is the GitHub REST API version this client is written against.
	apiVersion = "2026-03-10"

	// userAgent identifies this tool to GitHub.
	userAgent = "arc-agentic-code-review"

	// defaultTimeout bounds a single API call when the caller's context has no
	// deadline of its own.
	defaultTimeout = 30 * time.Second

	// maxErrorBodyBytes caps how much of a non-2xx body we read before giving
	// up. We only ever surface the "message" field, never the raw body.
	maxErrorBodyBytes = 1 << 16

	// maxErrorMessageLen truncates the API message included in errors.
	maxErrorMessageLen = 200
)

// TokenEnvVar is the environment variable holding the GitHub API token.
const TokenEnvVar = "GITHUB_TOKEN"

// Sentinel errors callers can match with errors.Is.
var (
	// ErrMissingToken is returned when no GitHub token is configured.
	ErrMissingToken = errors.New("missing GitHub token")

	// ErrUnauthorized indicates the token was rejected (HTTP 401).
	ErrUnauthorized = errors.New("GitHub authentication failed")

	// ErrForbidden indicates access was denied or a rate limit was hit (HTTP 403).
	ErrForbidden = errors.New("GitHub access denied")

	// ErrNotFound indicates the pull request or repository does not exist, or
	// the token cannot see it (HTTP 404).
	ErrNotFound = errors.New("not found")

	// ErrUnprocessable indicates GitHub understood the request but rejected its
	// content (HTTP 422). For a review this usually means a comment location is
	// not part of the pull request diff.
	ErrUnprocessable = errors.New("GitHub rejected the request as invalid")

	// ErrRateLimited indicates the request was refused because a rate limit is
	// exhausted.
	ErrRateLimited = errors.New("GitHub rate limit exceeded")

	// ErrServer indicates GitHub failed on its own side (HTTP 5xx).
	ErrServer = errors.New("GitHub server error")
)

// APIError describes a non-2xx response from the GitHub API. Message is the
// API's own "message" field, truncated; the raw response body is never kept.
//
// The matching sentinel error is derived from StatusCode, so an APIError built by
// a caller — in a test, say — still satisfies errors.Is.
type APIError struct {
	StatusCode int
	Message    string

	// RateLimited reports that the response carried rate-limit exhaustion
	// headers. GitHub signals this as a 403, so the flag is what distinguishes
	// "slow down" from "you may not do this".
	RateLimited bool
}

func (e *APIError) Error() string {
	var b strings.Builder
	b.WriteString("github api: ")
	if kind := e.Unwrap(); kind != nil {
		b.WriteString(kind.Error())
	} else {
		b.WriteString("unexpected response")
	}
	fmt.Fprintf(&b, " (status %d)", e.StatusCode)
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	return b.String()
}

// Unwrap maps the status code onto a sentinel error so
// errors.Is(err, ErrNotFound) works.
func (e *APIError) Unwrap() error {
	switch e.StatusCode {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		if e.RateLimited {
			return ErrRateLimited
		}
		return ErrForbidden
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case http.StatusUnprocessableEntity:
		return ErrUnprocessable
	default:
		if e.StatusCode >= 500 {
			return ErrServer
		}
		return nil
	}
}

// Client is a read-only GitHub REST API client.
//
// The token lives only in this struct and is written only to the Authorization
// header. It is never logged, printed, or included in an error.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient returns a Client for the public GitHub API.
func NewClient(token string) *Client {
	return NewClientWithBaseURL(token, DefaultBaseURL, nil)
}

// NewClientWithBaseURL returns a Client pointed at baseURL, which is useful for
// GitHub Enterprise and for httptest servers in tests. A nil httpClient falls
// back to a client with a sane timeout.
func NewClientWithBaseURL(token string, baseURL string, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: httpClient,
	}
}

// NewClientFromEnv builds a Client using the token in $GITHUB_TOKEN. It returns
// an error wrapping ErrMissingToken when that variable is unset or blank.
func NewClientFromEnv() (*Client, error) {
	token := strings.TrimSpace(os.Getenv(TokenEnvVar))
	if token == "" {
		return nil, fmt.Errorf("%w: set %s to a GitHub token with repository read access", ErrMissingToken, TokenEnvVar)
	}
	return NewClient(token), nil
}

// PullRequestDetails is the subset of GitHub's pull request representation that
// this tool needs.
type PullRequestDetails struct {
	Number      int
	Title       string
	Body        string
	State       string
	Draft       bool
	HTMLURL     string
	BaseBranch  string
	HeadBranch  string
	HeadSHA     string
	AuthorLogin string

	// BaseSHA is the commit the pull request targets. It is the revision whose
	// review guidance is authoritative: rules a pull request proposes cannot be
	// the rules that judge it.
	BaseSHA string
}

// pullRequestResponse mirrors the wire format so the nested shape stays out of
// the domain type.
type pullRequestResponse struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
}

func (r pullRequestResponse) toDetails() PullRequestDetails {
	return PullRequestDetails{
		Number:      r.Number,
		Title:       r.Title,
		Body:        r.Body,
		State:       r.State,
		Draft:       r.Draft,
		HTMLURL:     r.HTMLURL,
		BaseBranch:  r.Base.Ref,
		BaseSHA:     r.Base.SHA,
		HeadBranch:  r.Head.Ref,
		HeadSHA:     r.Head.SHA,
		AuthorLogin: r.User.Login,
	}
}

// GetPullRequest fetches metadata for pr via
// GET /repos/{owner}/{repo}/pulls/{pull_number}.
func (c *Client) GetPullRequest(ctx context.Context, pr PullRequest) (PullRequestDetails, error) {
	var body pullRequestResponse
	if err := c.getJSON(ctx, pullsPath(pr), nil, &body, "pull request "+pr.String()); err != nil {
		return PullRequestDetails{}, err
	}
	return body.toDetails(), nil
}

// pullsPath is the API path for a single pull request.
func pullsPath(pr PullRequest) string {
	return fmt.Sprintf("/repos/%s/%s/pulls/%d",
		url.PathEscape(pr.Owner),
		url.PathEscape(pr.Repo),
		pr.Number,
	)
}

// getJSON performs an authenticated GET against path (with optional query) and
// decodes the JSON response into out. description names the resource for error
// messages. It is the single place that builds GitHub requests, so headers and
// error mapping stay consistent across endpoints.
func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any, description string) error {
	return c.doJSON(ctx, http.MethodGet, path, query, nil, out, description)
}

// doJSON performs one authenticated API call and decodes the JSON response into
// out, which may be nil when the body is not needed. It is the single place that
// builds GitHub requests, so headers, credential handling, and error mapping stay
// identical across every endpoint — including the one write endpoint.
func (c *Client) doJSON(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	payload any,
	out any,
	description string,
) error {
	if c.token == "" {
		return fmt.Errorf("%w: set %s", ErrMissingToken, TokenEnvVar)
	}

	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode %s request: %w", description, err)
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("build %s request: %w", description, err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// url.Error stringifies the request URL, which carries no secret: the
		// token travels in a header, not the URL.
		return fmt.Errorf("request %s: %w", description, c.redactErr(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.statusError(resp)
	}

	if out == nil {
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s response: %w", description, err)
	}

	return nil
}

// statusError converts a non-2xx response into an *APIError, extracting only
// the API's "message" field.
func (c *Client) statusError(resp *http.Response) error {
	return &APIError{
		StatusCode:  resp.StatusCode,
		Message:     c.redact(apiMessage(resp.Body)),
		RateLimited: rateLimited(resp),
	}
}

// redact removes the token from s. The API message is server-controlled, so an
// echoed credential must never survive into an error we surface.
func (c *Client) redact(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "[REDACTED]")
}

// redactErr wraps err so that a token echoed by a transport-level failure cannot
// reach a log or a terminal.
func (c *Client) redactErr(err error) error {
	if err == nil || c.token == "" {
		return err
	}
	if msg := err.Error(); strings.Contains(msg, c.token) {
		return errors.New(c.redact(msg))
	}
	return err
}

// rateLimited reports whether a refusal was a rate limit rather than a
// permission problem. GitHub answers both with 403, so the headers decide.
func rateLimited(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if resp.Header.Get("Retry-After") != "" {
		return true
	}
	return resp.Header.Get("X-RateLimit-Remaining") == "0"
}

// apiMessage reads the "message" field from a GitHub error body, ignoring
// everything else. It returns "" if the body is unreadable or not JSON.
func apiMessage(r io.Reader) string {
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(r, maxErrorBodyBytes)).Decode(&body); err != nil {
		return ""
	}

	msg := strings.TrimSpace(body.Message)
	if len(msg) > maxErrorMessageLen {
		msg = msg[:maxErrorMessageLen] + "…"
	}
	return msg
}
