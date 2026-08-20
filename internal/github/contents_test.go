package github

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// contentsBody builds a contents-endpoint payload carrying content as base64.
func contentsBody(path, content string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))

	// GitHub wraps base64 at 60 characters, so reproduce that here.
	var wrapped strings.Builder
	for i := 0; i < len(encoded); i += 60 {
		end := i + 60
		if end > len(encoded) {
			end = len(encoded)
		}
		wrapped.WriteString(encoded[i:end])
		wrapped.WriteString("\n")
	}

	return fmt.Sprintf(`{"name":"x","path":%q,"type":"file","size":%d,"encoding":"base64","content":%q}`,
		path, len(content), wrapped.String())
}

const testRef = "abc123"

func TestGetRepositoryFile(t *testing.T) {
	const want = "# Review rules\n\nPrefer table-driven tests.\n"

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, contentsBody("AGENTS.md", want))
	})

	got, err := client.GetRepositoryFile(context.Background(), "acme", "payments", "AGENTS.md", testRef)
	if err != nil {
		t.Fatalf("GetRepositoryFile() returned error: %v", err)
	}
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

// TestGetRepositoryFileRequest covers the endpoint, owner, repo, ref, and headers.
func TestGetRepositoryFileRequest(t *testing.T) {
	var got *http.Request

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		fmt.Fprint(w, contentsBody("AGENTS.md", "rules"))
	})

	if _, err := client.GetRepositoryFile(context.Background(), "acme", "payments", "AGENTS.md", testRef); err != nil {
		t.Fatalf("GetRepositoryFile() returned error: %v", err)
	}
	if got == nil {
		t.Fatal("handler never ran")
	}

	if got.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", got.Method)
	}
	if want := "/repos/acme/payments/contents/AGENTS.md"; got.URL.Path != want {
		t.Errorf("path = %q, want %q", got.URL.Path, want)
	}
	if ref := got.URL.Query().Get("ref"); ref != testRef {
		t.Errorf("ref = %q, want %q", ref, testRef)
	}

	headers := []struct{ name, want string }{
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

func TestGetRepositoryFilePathEscaping(t *testing.T) {
	tests := []struct {
		name     string
		owner    string
		repo     string
		path     string
		wantPath string
	}{
		{
			name:  "nested path keeps its separators",
			owner: "acme", repo: "payments", path: ".ai-review/rules.md",
			wantPath: "/repos/acme/payments/contents/.ai-review/rules.md",
		},
		{
			name:  "spaces in the path are escaped",
			owner: "acme", repo: "payments", path: "docs/review rules.md",
			wantPath: "/repos/acme/payments/contents/docs/review%20rules.md",
		},
		{
			name:  "owner and repo are escaped",
			owner: "acme corp", repo: "pay ments", path: "AGENTS.md",
			wantPath: "/repos/acme%20corp/pay%20ments/contents/AGENTS.md",
		},
		{
			name:  "leading slash is dropped",
			owner: "acme", repo: "payments", path: "/AGENTS.md",
			wantPath: "/repos/acme/payments/contents/AGENTS.md",
		},
		{
			name:  "hash in a file name is escaped",
			owner: "acme", repo: "payments", path: "docs/notes#1.md",
			wantPath: "/repos/acme/payments/contents/docs/notes%231.md",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var path string

			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.EscapedPath()
				fmt.Fprint(w, contentsBody(tt.path, "rules"))
			})

			if _, err := client.GetRepositoryFile(context.Background(), tt.owner, tt.repo, tt.path, testRef); err != nil {
				t.Fatalf("GetRepositoryFile() returned error: %v", err)
			}
			if path != tt.wantPath {
				t.Errorf("escaped path = %q, want %q", path, tt.wantPath)
			}
		})
	}
}

// TestGetRepositoryFileRefIsTheHeadSHA checks the exact snapshot is requested.
func TestGetRepositoryFileRefIsTheHeadSHA(t *testing.T) {
	const headSHA = "0f1e2d3c4b5a69788796a5b4c3d2e1f0aabbccdd"

	var query string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		fmt.Fprint(w, contentsBody("AGENTS.md", "rules"))
	})

	if _, err := client.GetRepositoryFile(context.Background(), "acme", "payments", "AGENTS.md", headSHA); err != nil {
		t.Fatalf("GetRepositoryFile() returned error: %v", err)
	}

	if want := "ref=" + headSHA; query != want {
		t.Errorf("query = %q, want %q", query, want)
	}
}

// TestGetRepositoryFileWithoutRef omits the ref entirely when none is given.
func TestGetRepositoryFileWithoutRef(t *testing.T) {
	var query string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		fmt.Fprint(w, contentsBody("AGENTS.md", "rules"))
	})

	if _, err := client.GetRepositoryFile(context.Background(), "acme", "payments", "AGENTS.md", ""); err != nil {
		t.Fatalf("GetRepositoryFile() returned error: %v", err)
	}
	if query != "" {
		t.Errorf("query = %q, want it to be empty", query)
	}
}

func TestGetRepositoryFileContentDecoding(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "wrapped base64",
			body: contentsBody("AGENTS.md", "# Rules\n\nUse table-driven tests everywhere in this repository.\n"),
			want: "# Rules\n\nUse table-driven tests everywhere in this repository.\n",
		},
		{
			name: "unwrapped base64",
			body: fmt.Sprintf(`{"type":"file","encoding":"base64","content":%q}`,
				base64.StdEncoding.EncodeToString([]byte("plain"))),
			want: "plain",
		},
		{
			name: "empty file",
			body: `{"type":"file","size":0,"encoding":"base64","content":""}`,
			want: "",
		},
		{
			name: "base64 with carriage returns",
			body: fmt.Sprintf(`{"type":"file","encoding":"base64","content":%q}`,
				"IyBS\r\ndWxl\r\ncw=="),
			want: "# Rules",
		},
		{
			name: "utf-8 encoding is taken as plain text",
			body: `{"type":"file","encoding":"utf-8","content":"# Rules"}`,
			want: "# Rules",
		},
		{
			name: "no encoding field with inline content",
			body: `{"type":"file","content":"# Rules"}`,
			want: "# Rules",
		},
		{
			name: "multi-byte content survives the round trip",
			body: contentsBody("AGENTS.md", "# Règles — ✅ tests\n"),
			want: "# Règles — ✅ tests\n",
		},
		{
			name: "unknown fields are ignored",
			body: fmt.Sprintf(`{"name":"AGENTS.md","path":"AGENTS.md","sha":"deadbeef","size":5,
			        "url":"https://api.example.com","html_url":"https://example.com",
			        "git_url":"https://api.example.com/git","download_url":"https://raw.example.com",
			        "type":"file","encoding":"base64","content":%q,"_links":{"self":"x"}}`,
				base64.StdEncoding.EncodeToString([]byte("rules"))),
			want: "rules",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tt.body)
			})

			got, err := client.GetRepositoryFile(context.Background(), "acme", "payments", "AGENTS.md", testRef)
			if err != nil {
				t.Fatalf("GetRepositoryFile() returned error: %v", err)
			}
			if got != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetRepositoryFileStatusErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantIs  error
		wantMsg string
	}{
		{
			name:   "404 not found",
			status: http.StatusNotFound,
			body:   `{"message":"Not Found"}`,
			wantIs: ErrNotFound,
		},
		{
			name:   "401 unauthorized",
			status: http.StatusUnauthorized,
			body:   `{"message":"Bad credentials"}`,
			wantIs: ErrUnauthorized,
		},
		{
			name:   "403 forbidden",
			status: http.StatusForbidden,
			body:   `{"message":"API rate limit exceeded"}`,
			wantIs: ErrForbidden,
		},
		{
			name:    "429 too many requests",
			status:  http.StatusTooManyRequests,
			body:    `{"message":"Too many requests"}`,
			wantMsg: "status 429",
		},
		{
			name:    "500 server error",
			status:  http.StatusInternalServerError,
			body:    `{"message":"Server Error"}`,
			wantMsg: "status 500",
		},
		{
			name:    "502 bad gateway with a non-JSON body",
			status:  http.StatusBadGateway,
			body:    `<html>proxy exploded</html>`,
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

			got, err := client.GetRepositoryFile(context.Background(), "acme", "payments", "AGENTS.md", testRef)
			if err == nil {
				t.Fatalf("GetRepositoryFile() = %q, want error", got)
			}
			if got != "" {
				t.Errorf("GetRepositoryFile() = %q on error, want empty", got)
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Errorf("errors.Is(err, %v) = false; err = %v", tt.wantIs, err)
			}
			if tt.wantMsg != "" && !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantMsg)
			}
			if strings.Contains(err.Error(), testToken) {
				t.Error("error contains the token")
			}
			if strings.Contains(err.Error(), "proxy exploded") {
				t.Error("error contains the raw response body")
			}
		})
	}
}

func TestGetRepositoryFileMalformedJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "truncated object", body: `{"type":"file","content":`},
		{name: "not json at all", body: `<html>hello</html>`},
		{name: "wrong type for content", body: `{"type":"file","content":{"nested":true}}`},
		{name: "wrong type for size", body: `{"type":"file","size":"big"}`},
		{name: "empty body", body: ``},
		{name: "directory listing is an array", body: `[{"name":"a.md","type":"file"}]`},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tt.body)
			})

			got, err := client.GetRepositoryFile(context.Background(), "acme", "payments", "AGENTS.md", testRef)
			if err == nil {
				t.Fatalf("GetRepositoryFile() = %q, want decode error", got)
			}
			if !strings.Contains(err.Error(), "decode repository file") {
				t.Errorf("error = %q, want it to mention decoding", err)
			}
		})
	}
}

func TestGetRepositoryFileInvalidBase64(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "not base64 at all", content: "this is not base64!!!"},
		{name: "bad padding", content: "aGVsbG8"},
		{name: "illegal characters", content: "aGVs*bG8="},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"type":"file","encoding":"base64","content":%q}`, tt.content)
			})

			got, err := client.GetRepositoryFile(context.Background(), "acme", "payments", "AGENTS.md", testRef)
			if err == nil {
				t.Fatalf("GetRepositoryFile() = %q, want a base64 error", got)
			}
			if !strings.Contains(err.Error(), "decode base64 content") {
				t.Errorf("error = %q, want it to mention base64", err)
			}
		})
	}
}

func TestGetRepositoryFileUnsupportedContent(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "directory", body: `{"type":"dir","path":"docs"}`},
		{name: "submodule", body: `{"type":"submodule","path":"vendor/lib"}`},
		{name: "symlink", body: `{"type":"symlink","path":"link.md"}`},
		{name: "too large to inline", body: `{"type":"file","encoding":"none","content":"","size":5000000}`},
		{name: "unknown encoding", body: `{"type":"file","encoding":"quoted-printable","content":"x"}`},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tt.body)
			})

			_, err := client.GetRepositoryFile(context.Background(), "acme", "payments", "AGENTS.md", testRef)
			if !errors.Is(err, ErrUnsupportedContent) {
				t.Errorf("errors.Is(err, ErrUnsupportedContent) = false; err = %v", err)
			}
		})
	}
}

func TestGetRepositoryFileContextCanceled(t *testing.T) {
	release := make(chan struct{})
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
	})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.GetRepositoryFile(ctx, "acme", "payments", "AGENTS.md", testRef)
	if err == nil {
		t.Fatal("GetRepositoryFile() = nil error, want context cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; err = %v", err)
	}
	if !strings.Contains(err.Error(), "AGENTS.md") {
		t.Errorf("error = %q, want it to name the file", err)
	}
}

func TestGetRepositoryFileNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := NewClientWithBaseURL(testToken, srv.URL, srv.Client())
	srv.Close()

	_, err := client.GetRepositoryFile(context.Background(), "acme", "payments", "AGENTS.md", testRef)
	if err == nil {
		t.Fatal("GetRepositoryFile() = nil error, want network error")
	}
	if !strings.Contains(err.Error(), "request repository file") {
		t.Errorf("error = %q, want a wrapped request error", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Error("error contains the token")
	}
}

func TestGetRepositoryFileEmptyPath(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made for an empty path")
	})

	for _, path := range []string{"", "   "} {
		if _, err := client.GetRepositoryFile(context.Background(), "acme", "payments", path, testRef); err == nil {
			t.Errorf("GetRepositoryFile(%q) = nil error, want an error", path)
		}
	}
}

func TestGetRepositoryFileMissingToken(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be made without a token")
	})
	client.token = ""

	if _, err := client.GetRepositoryFile(context.Background(), "acme", "payments", "AGENTS.md", testRef); !errors.Is(err, ErrMissingToken) {
		t.Errorf("errors.Is(err, ErrMissingToken) = false; err = %v", err)
	}
}
