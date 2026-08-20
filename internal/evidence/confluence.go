package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	confluenceBaseURLEnv = "CONFLUENCE_BASE_URL"
	confluenceEmailEnv   = "CONFLUENCE_EMAIL"
	confluenceTokenEnv   = "CONFLUENCE_TOKEN"
)

// ConfluenceConnector reads one current Confluence Cloud page through REST v2.
// The site URL and credentials come only from the operator environment; the
// evidence configuration can select a numeric page id but cannot redirect the
// credential to another host.
type ConfluenceConnector struct {
	config SourceConfig
	client *http.Client
}

func NewConfluenceConnector(config SourceConfig, client *http.Client) *ConfluenceConnector {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	copyClient := *client
	// A redirect is unnecessary for the documented endpoint and creates an
	// avoidable authentication-boundary ambiguity.
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &ConfluenceConnector{config: config, client: &copyClient}
}

func (c *ConfluenceConnector) ID() string       { return c.config.ID }
func (c *ConfluenceConnector) Type() SourceType { return SourceConfluence }
func (c *ConfluenceConnector) Kind() Kind       { return c.config.Kind }
func (c *ConfluenceConnector) Required() bool   { return c.config.Required }

func (c *ConfluenceConnector) Collect(ctx context.Context) (Document, error) {
	base := strings.TrimSpace(os.Getenv(confluenceBaseURLEnv))
	email := strings.TrimSpace(os.Getenv(confluenceEmailEnv))
	token := os.Getenv(confluenceTokenEnv)
	var missing []string
	if base == "" {
		missing = append(missing, confluenceBaseURLEnv)
	}
	if email == "" {
		missing = append(missing, confluenceEmailEnv)
	}
	if token == "" {
		missing = append(missing, confluenceTokenEnv)
	}
	if len(missing) > 0 {
		return Document{}, fmt.Errorf("Confluence connector is not configured: missing %s", strings.Join(missing, ", "))
	}

	endpoint, err := confluencePageURL(base, c.config.PageID)
	if err != nil {
		return Document{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Document{}, fmt.Errorf("build Confluence request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(email, token)

	response, err := c.client.Do(req)
	if err != nil {
		return Document{}, fmt.Errorf("fetch Confluence page %s: %w", c.config.PageID, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Document{}, fmt.Errorf("fetch Confluence page %s: HTTP %d", c.config.PageID, response.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxRawSourceBytes+1))
	if err != nil {
		return Document{}, fmt.Errorf("read Confluence page %s: %w", c.config.PageID, err)
	}
	if len(raw) > MaxRawSourceBytes {
		return Document{}, fmt.Errorf("Confluence page %s exceeds the %d byte source limit", c.config.PageID, MaxRawSourceBytes)
	}

	var page struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Status  string `json:"status"`
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
		Body struct {
			Storage struct {
				Representation string `json:"representation"`
				Value          string `json:"value"`
			} `json:"storage"`
		} `json:"body"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(&page); err != nil {
		return Document{}, fmt.Errorf("decode Confluence page %s: %w", c.config.PageID, err)
	}
	if page.ID != "" && page.ID != c.config.PageID {
		return Document{}, fmt.Errorf("Confluence returned page id %s for requested page %s", page.ID, c.config.PageID)
	}

	original := confluenceStorageText(page.Body.Storage.Value)
	content, truncated := normalizeDocumentContent(original)
	return Document{
		ID: c.ID(), Kind: c.Kind(), SourceType: c.Type(),
		Locator: "confluence:page/" + c.config.PageID,
		Title:   page.Title, Revision: strconv.Itoa(page.Version.Number),
		Content: content, Digest: digest(original), Truncated: truncated,
	}, nil
}

func confluencePageURL(base, pageID string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "", fmt.Errorf("invalid %s: %w", confluenceBaseURLEnv, err)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid %s: expected a site URL without credentials, query, or fragment", confluenceBaseURLEnv)
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return "", fmt.Errorf("invalid %s: HTTPS is required", confluenceBaseURLEnv)
	}

	basePath := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(basePath, "/wiki") {
		basePath += "/wiki"
	}
	parsed.Path = basePath + "/api/v2/pages/" + pageID
	parsed.RawQuery = "body-format=storage"
	return parsed.String(), nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

var (
	unsafeMarkup = regexp.MustCompile(`(?is)<(?:script|style)\b[^>]*>.*?</(?:script|style)>`)
	blockMarkup  = regexp.MustCompile(`(?i)</?(?:p|div|br|li|tr|h[1-6]|table|ul|ol|pre|blockquote)\b[^>]*>`)
	anyMarkup    = regexp.MustCompile(`(?s)<[^>]*>`)
	repeatedGap  = regexp.MustCompile(`\n[ \t]*\n(?:[ \t]*\n)+`)
)

// confluenceStorageText converts the storage XHTML representation into bounded
// plain evidence. It is intentionally not a renderer; it removes markup and
// preserves block boundaries so requirements remain readable.
func confluenceStorageText(storage string) string {
	withoutUnsafe := unsafeMarkup.ReplaceAllString(storage, "")
	withBreaks := blockMarkup.ReplaceAllString(withoutUnsafe, "\n")
	plain := anyMarkup.ReplaceAllString(withBreaks, "")
	plain = html.UnescapeString(plain)
	plain = strings.ReplaceAll(plain, "\r\n", "\n")
	plain = repeatedGap.ReplaceAllString(plain, "\n\n")
	return strings.TrimSpace(plain)
}
