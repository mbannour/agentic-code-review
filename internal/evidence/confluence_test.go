package evidence

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestConfluenceConnectorUsesOperatorEndpointAndNormalizesPage(t *testing.T) {
	t.Setenv(confluenceBaseURLEnv, "https://team.atlassian.net")
	t.Setenv(confluenceEmailEnv, "reviewer@example.com")
	t.Setenv(confluenceTokenEnv, "top-secret")

	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.URL.String(); got != "https://team.atlassian.net/wiki/api/v2/pages/123?body-format=storage" {
			t.Fatalf("URL = %s", got)
		}
		wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("reviewer@example.com:top-secret"))
		if got := request.Header.Get("Authorization"); got != wantAuth {
			t.Fatalf("Authorization = %q", got)
		}
		body := `{"id":"123","title":"Order requirements","version":{"number":7},"body":{"storage":{"representation":"storage","value":"<h1>Orders</h1><p>Keep &amp; validate reference.</p>"}}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}

	connector := NewConfluenceConnector(SourceConfig{
		ID: "orders", Type: SourceConfluence, Kind: KindRequirement,
		PageID: "123", Required: true,
	}, client)
	document, err := connector.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}
	if document.Title != "Order requirements" || document.Revision != "7" ||
		!strings.Contains(document.Content, "Keep & validate reference.") || strings.Contains(document.Content, "<p>") {
		t.Fatalf("document = %+v", document)
	}
}

func TestConfluencePageURLRefusesCredentialRedirectTargets(t *testing.T) {
	for _, base := range []string{
		"http://example.com", "https://user:password@example.com", "https://example.com?next=evil",
	} {
		if _, err := confluencePageURL(base, "1"); err == nil {
			t.Errorf("confluencePageURL(%q) accepted unsafe URL", base)
		}
	}
}
