package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	// userAgent identifies this tool to Jira.
	userAgent = "arc-agentic-code-review"

	// issueFields is the exact set of fields requested, so Jira does not return
	// its entire issue representation.
	issueFields = "summary,description,status,issuetype,priority,labels,parent"

	// defaultTimeout bounds a single API call when the caller's context has no
	// deadline of its own.
	defaultTimeout = 30 * time.Second

	// maxErrorBodyBytes caps how much of a non-2xx body is read. Only the
	// errorMessages field is ever surfaced.
	maxErrorBodyBytes = 1 << 16

	// maxErrorMessageLen truncates the message included in errors.
	maxErrorMessageLen = 200
)

// Environment variables holding the Jira configuration.
const (
	BaseURLEnvVar = "JIRA_BASE_URL"
	EmailEnvVar   = "JIRA_EMAIL"
	TokenEnvVar   = "JIRA_TOKEN"
)

// Sentinel errors callers can match with errors.Is.
var (
	// ErrMissingConfig is returned when Jira configuration is incomplete.
	ErrMissingConfig = errors.New("incomplete Jira configuration")

	// ErrUnauthorized indicates the credentials were rejected (HTTP 401).
	ErrUnauthorized = errors.New("Jira authentication failed")

	// ErrForbidden indicates the account may not view the issue (HTTP 403).
	ErrForbidden = errors.New("Jira access denied")

	// ErrIssueNotFound indicates the issue does not exist or is not visible
	// (HTTP 404).
	ErrIssueNotFound = errors.New("Jira issue not found")

	// ErrRateLimited indicates Jira is throttling requests (HTTP 429).
	ErrRateLimited = errors.New("Jira rate limited")

	// ErrServer indicates a Jira-side failure (HTTP 5xx).
	ErrServer = errors.New("Jira server error")
)

// APIError describes a non-2xx response from Jira. Message is a summary of the
// response's errorMessages field, truncated; the raw body is never retained.
//
// The matching sentinel error is derived from StatusCode, so an APIError built by
// a caller — in a test, say — still satisfies errors.Is.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	var b strings.Builder
	b.WriteString("jira api: ")
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
// errors.Is(err, ErrIssueNotFound) works.
func (e *APIError) Unwrap() error {
	switch {
	case e.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case e.StatusCode == http.StatusForbidden:
		return ErrForbidden
	case e.StatusCode == http.StatusNotFound:
		return ErrIssueNotFound
	case e.StatusCode == http.StatusTooManyRequests:
		return ErrRateLimited
	case e.StatusCode >= 500:
		return ErrServer
	default:
		return nil
	}
}

// Issue is the subset of a Jira issue that this tool needs.
type Issue struct {
	Key         TicketKey
	Summary     string
	Description string
	Status      string
	IssueType   string
	Priority    string
	Labels      []string
	ParentKey   string
}

// Client is a read-only Jira Cloud REST API client.
//
// The email and token live only in this struct and are written only to the
// request's Basic Auth header. They are never logged, printed, or included in an
// error.
type Client struct {
	baseURL    string
	email      string
	token      string
	httpClient *http.Client
}

// NewClient returns a Client for the Jira site at baseURL. A trailing slash on
// baseURL is accepted.
func NewClient(baseURL string, email string, token string) *Client {
	return NewClientWithHTTPClient(baseURL, email, token, nil)
}

// NewClientWithHTTPClient returns a Client using the supplied http.Client, which
// lets tests point it at an httptest server. A nil httpClient falls back to one
// with a sane timeout.
func NewClientWithHTTPClient(baseURL string, email string, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		email:      strings.TrimSpace(email),
		token:      token,
		httpClient: httpClient,
	}
}

// NewClientFromEnv builds a Client from $JIRA_BASE_URL, $JIRA_EMAIL, and
// $JIRA_TOKEN. It returns an error wrapping ErrMissingConfig naming every
// variable that is unset, so the user can fix them in one go.
func NewClientFromEnv() (*Client, error) {
	baseURL := strings.TrimSpace(os.Getenv(BaseURLEnvVar))
	email := strings.TrimSpace(os.Getenv(EmailEnvVar))
	token := strings.TrimSpace(os.Getenv(TokenEnvVar))

	var missing []string
	if baseURL == "" {
		missing = append(missing, BaseURLEnvVar)
	}
	if email == "" {
		missing = append(missing, EmailEnvVar)
	}
	if token == "" {
		missing = append(missing, TokenEnvVar)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: set %s", ErrMissingConfig, strings.Join(missing, ", "))
	}

	return NewClient(baseURL, email, token), nil
}

// issueResponse mirrors the Jira wire format. It exists so the nested "fields"
// shape never escapes this package.
type issueResponse struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string      `json:"summary"`
		Description adfDocument `json:"description"`
		Status      *struct {
			Name string `json:"name"`
		} `json:"status"`
		IssueType *struct {
			Name string `json:"name"`
		} `json:"issuetype"`
		Priority *struct {
			Name string `json:"name"`
		} `json:"priority"`
		Labels []string `json:"labels"`
		Parent *struct {
			Key string `json:"key"`
		} `json:"parent"`
	} `json:"fields"`
}

// toIssue normalizes the wire response into the domain model. Absent nested
// objects become empty strings rather than panicking.
func (r issueResponse) toIssue(requested TicketKey) Issue {
	issue := Issue{
		Key:         TicketKey(r.Key),
		Summary:     strings.TrimSpace(r.Fields.Summary),
		Description: r.Fields.Description.PlainText(),
		Labels:      r.Fields.Labels,
	}

	// Jira echoes the key back; fall back to what was asked for.
	if issue.Key == "" {
		issue.Key = requested
	}
	if r.Fields.Status != nil {
		issue.Status = r.Fields.Status.Name
	}
	if r.Fields.IssueType != nil {
		issue.IssueType = r.Fields.IssueType.Name
	}
	if r.Fields.Priority != nil {
		issue.Priority = r.Fields.Priority.Name
	}
	if r.Fields.Parent != nil {
		issue.ParentKey = r.Fields.Parent.Key
	}

	return issue
}

// GetIssue fetches the issue identified by key via
// GET /rest/api/3/issue/{issueKey}, requesting only the fields this tool uses.
func (c *Client) GetIssue(ctx context.Context, key TicketKey) (Issue, error) {
	if err := c.validate(); err != nil {
		return Issue{}, err
	}
	if strings.TrimSpace(key.String()) == "" {
		return Issue{}, errors.New("jira: empty issue key")
	}

	endpoint := fmt.Sprintf("%s/rest/api/3/issue/%s?%s",
		c.baseURL,
		url.PathEscape(key.String()),
		url.Values{"fields": {issueFields}}.Encode(),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Issue{}, fmt.Errorf("build Jira issue request for %s: %w", key, err)
	}

	req.SetBasicAuth(c.email, c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// url.Error stringifies the request URL, which holds no secret: the
		// credentials travel in a header.
		return Issue{}, fmt.Errorf("request Jira issue %s: %w", key, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Issue{}, c.statusError(resp)
	}

	var body issueResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Issue{}, fmt.Errorf("decode Jira issue %s response: %w", key, err)
	}

	return body.toIssue(key), nil
}

// validate reports incomplete configuration before any request is attempted.
func (c *Client) validate() error {
	var missing []string
	if c.baseURL == "" {
		missing = append(missing, BaseURLEnvVar)
	}
	if c.email == "" {
		missing = append(missing, EmailEnvVar)
	}
	if c.token == "" {
		missing = append(missing, TokenEnvVar)
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: set %s", ErrMissingConfig, strings.Join(missing, ", "))
	}
	return nil
}

// statusError converts a non-2xx response into an *APIError.
func (c *Client) statusError(resp *http.Response) error {
	return &APIError{
		StatusCode: resp.StatusCode,
		Message:    c.redact(apiMessage(resp.Body)),
	}
}

// redact strips the credentials from s. Jira error text is server-controlled, so
// an echoed credential must never survive into an error we surface.
func (c *Client) redact(s string) string {
	if c.token != "" {
		s = strings.ReplaceAll(s, c.token, "[REDACTED]")
	}
	if c.email != "" {
		s = strings.ReplaceAll(s, c.email, "[REDACTED]")
	}
	return s
}

// apiMessage summarizes a Jira error body, reading only the errorMessages and
// errors fields. It returns "" when the body is unreadable or not JSON.
func apiMessage(r io.Reader) string {
	var body struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
		Message       string            `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(r, maxErrorBodyBytes)).Decode(&body); err != nil {
		return ""
	}

	parts := make([]string, 0, len(body.ErrorMessages)+1)
	for _, m := range body.ErrorMessages {
		if m = strings.TrimSpace(m); m != "" {
			parts = append(parts, m)
		}
	}
	if len(parts) == 0 && len(body.Errors) > 0 {
		// Field-level errors: report the field names only, not their values.
		fields := make([]string, 0, len(body.Errors))
		for name := range body.Errors {
			fields = append(fields, name)
		}
		sort.Strings(fields)
		parts = append(parts, "invalid fields: "+strings.Join(fields, ", "))
	}
	if len(parts) == 0 && strings.TrimSpace(body.Message) != "" {
		parts = append(parts, strings.TrimSpace(body.Message))
	}

	msg := strings.Join(parts, "; ")
	if len(msg) > maxErrorMessageLen {
		msg = msg[:maxErrorMessageLen] + "…"
	}
	return msg
}
