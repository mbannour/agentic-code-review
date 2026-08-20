package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// recordedRequest captures what the client asked for, so pagination and query
// parameters can be asserted after the fact.
type recordedRequest struct {
	Path    string
	Page    string
	PerPage string
	Headers http.Header
}

// newFilesServer serves pages in order: the first request gets pages[0], the
// second pages[1], and so on. Each element is a raw JSON body.
func newFilesServer(t *testing.T, pages []string) (*Client, *[]recordedRequest) {
	t.Helper()

	var recorded []recordedRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded = append(recorded, recordedRequest{
			Path:    r.URL.Path,
			Page:    r.URL.Query().Get("page"),
			PerPage: r.URL.Query().Get("per_page"),
			Headers: r.Header.Clone(),
		})

		idx := len(recorded) - 1
		if idx >= len(pages) {
			t.Errorf("unexpected request %d for %s; only %d pages are stubbed", idx+1, r.URL, len(pages))
			w.WriteHeader(http.StatusTeapot)
			return
		}
		fmt.Fprint(w, pages[idx])
	}))
	t.Cleanup(srv.Close)

	return NewClientWithBaseURL(testToken, srv.URL, srv.Client()), &recorded
}

// filesPage builds a JSON array of n changed files, numbered from start.
func filesPage(start, n int) string {
	items := make([]string, 0, n)
	for i := start; i < start+n; i++ {
		items = append(items, fmt.Sprintf(
			`{"filename":"file%d.go","status":"modified","additions":1,"deletions":1,"changes":2,"patch":"@@ hunk %d @@"}`,
			i, i,
		))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func TestGetPullRequestFiles(t *testing.T) {
	tests := []struct {
		name string
		page string
		want []ChangedFile
	}{
		{
			name: "one changed file",
			page: `[{"filename":"internal/payment/retry.go","status":"modified",
			        "additions":24,"deletions":7,"changes":31,
			        "patch":"@@ -10,6 +10,12 @@ func retry() {"}]`,
			want: []ChangedFile{{
				Filename: "internal/payment/retry.go", Status: "modified",
				Additions: 24, Deletions: 7, Changes: 31,
				Patch: "@@ -10,6 +10,12 @@ func retry() {",
			}},
		},
		{
			name: "multiple changed files",
			page: `[{"filename":"internal/payment/retry.go","status":"modified","additions":24,"deletions":7,"changes":31,"patch":"@@ a @@"},
			        {"filename":"internal/payment/retry_test.go","status":"added","additions":16,"deletions":4,"changes":20,"patch":"@@ b @@"},
			        {"filename":"README.md","status":"modified","additions":2,"deletions":0,"changes":2,"patch":"@@ c @@"}]`,
			want: []ChangedFile{
				{Filename: "internal/payment/retry.go", Status: "modified", Additions: 24, Deletions: 7, Changes: 31, Patch: "@@ a @@"},
				{Filename: "internal/payment/retry_test.go", Status: "added", Additions: 16, Deletions: 4, Changes: 20, Patch: "@@ b @@"},
				{Filename: "README.md", Status: "modified", Additions: 2, Deletions: 0, Changes: 2, Patch: "@@ c @@"},
			},
		},
		{
			name: "patch field missing",
			page: `[{"filename":"assets/logo.png","status":"modified","additions":0,"deletions":0,"changes":0}]`,
			want: []ChangedFile{{Filename: "assets/logo.png", Status: "modified"}},
		},
		{
			name: "patch field null",
			page: `[{"filename":"assets/logo.png","status":"added","patch":null}]`,
			want: []ChangedFile{{Filename: "assets/logo.png", Status: "added"}},
		},
		{
			name: "patch field empty",
			page: `[{"filename":"vendor/huge.json","status":"modified","additions":9000,"deletions":0,"changes":9000,"patch":""}]`,
			want: []ChangedFile{{Filename: "vendor/huge.json", Status: "modified", Additions: 9000, Changes: 9000}},
		},
		{
			name: "deleted file",
			page: `[{"filename":"internal/legacy/old.go","status":"removed","additions":0,"deletions":88,"changes":88,"patch":"@@ -1,88 +0,0 @@"}]`,
			want: []ChangedFile{{
				Filename: "internal/legacy/old.go", Status: "removed",
				Deletions: 88, Changes: 88, Patch: "@@ -1,88 +0,0 @@",
			}},
		},
		{
			name: "renamed file",
			page: `[{"filename":"internal/payment/retry.go","status":"renamed",
			        "additions":2,"deletions":1,"changes":3,
			        "previous_filename":"internal/payment/retries.go",
			        "patch":"@@ -1,4 +1,5 @@"}]`,
			want: []ChangedFile{{
				Filename: "internal/payment/retry.go", Status: "renamed",
				Additions: 2, Deletions: 1, Changes: 3, Patch: "@@ -1,4 +1,5 @@",
			}},
		},
		{
			name: "mixed statuses including a patchless file",
			page: `[{"filename":"a.go","status":"added","additions":5,"deletions":0,"changes":5,"patch":"@@ a @@"},
			        {"filename":"b.bin","status":"added","additions":0,"deletions":0,"changes":0},
			        {"filename":"c.go","status":"removed","additions":0,"deletions":3,"changes":3,"patch":"@@ c @@"}]`,
			want: []ChangedFile{
				{Filename: "a.go", Status: "added", Additions: 5, Changes: 5, Patch: "@@ a @@"},
				{Filename: "b.bin", Status: "added"},
				{Filename: "c.go", Status: "removed", Deletions: 3, Changes: 3, Patch: "@@ c @@"},
			},
		},
		{
			name: "no changed files",
			page: `[]`,
			want: nil,
		},
		{
			name: "unknown fields are ignored",
			page: `[{"filename":"a.go","status":"modified","additions":1,"deletions":1,"changes":2,
			        "patch":"@@ a @@","sha":"abc","blob_url":"https://example.com",
			        "contents_url":"https://api.example.com","raw_url":"https://raw.example.com"}]`,
			want: []ChangedFile{{Filename: "a.go", Status: "modified", Additions: 1, Deletions: 1, Changes: 2, Patch: "@@ a @@"}},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			client, _ := newFilesServer(t, []string{tt.page})

			got, err := client.GetPullRequestFiles(context.Background(), testPRRef)
			if err != nil {
				t.Fatalf("GetPullRequestFiles() returned error: %v", err)
			}

			if len(got) != len(tt.want) {
				t.Fatalf("got %d files, want %d\ngot:  %+v\nwant: %+v", len(got), len(tt.want), got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("file %d = %+v\nwant %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestGetPullRequestFilesRequest asserts the path, page size, and headers of the
// files request.
func TestGetPullRequestFilesRequest(t *testing.T) {
	client, recorded := newFilesServer(t, []string{`[]`})

	if _, err := client.GetPullRequestFiles(context.Background(), testPRRef); err != nil {
		t.Fatalf("GetPullRequestFiles() returned error: %v", err)
	}

	if len(*recorded) != 1 {
		t.Fatalf("made %d requests, want 1", len(*recorded))
	}
	req := (*recorded)[0]

	if want := "/repos/acme/payments/pulls/123/files"; req.Path != want {
		t.Errorf("path = %q, want %q", req.Path, want)
	}
	if req.PerPage != "100" {
		t.Errorf("per_page = %q, want %q", req.PerPage, "100")
	}
	if req.Page != "1" {
		t.Errorf("page = %q, want %q", req.Page, "1")
	}

	headers := []struct{ name, want string }{
		{"Accept", "application/vnd.github+json"},
		{"Authorization", "Bearer " + testToken},
		{"X-GitHub-Api-Version", apiVersion},
		{"User-Agent", userAgent},
	}
	for _, h := range headers {
		if got := req.Headers.Get(h.name); got != h.want {
			t.Errorf("header %s = %q, want %q", h.name, got, h.want)
		}
	}
}

func TestGetPullRequestFilesPagination(t *testing.T) {
	tests := []struct {
		name       string
		pages      []string
		wantFiles  int
		wantPages  []string // expected page query values, in order
		wantFirst  string
		wantLastFn func(files []ChangedFile) string
	}{
		{
			name:      "single short page stops after one request",
			pages:     []string{filesPage(0, 3)},
			wantFiles: 3,
			wantPages: []string{"1"},
			wantFirst: "file0.go",
		},
		{
			name:      "two pages, second is short",
			pages:     []string{filesPage(0, 100), filesPage(100, 40)},
			wantFiles: 140,
			wantPages: []string{"1", "2"},
			wantFirst: "file0.go",
		},
		{
			name:      "full page followed by an empty final page",
			pages:     []string{filesPage(0, 100), `[]`},
			wantFiles: 100,
			wantPages: []string{"1", "2"},
			wantFirst: "file0.go",
		},
		{
			name:      "three pages",
			pages:     []string{filesPage(0, 100), filesPage(100, 100), filesPage(200, 5)},
			wantFiles: 205,
			wantPages: []string{"1", "2", "3"},
			wantFirst: "file0.go",
		},
		{
			name:      "exactly one full page then empty",
			pages:     []string{filesPage(0, 100), filesPage(100, 0)},
			wantFiles: 100,
			wantPages: []string{"1", "2"},
			wantFirst: "file0.go",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			client, recorded := newFilesServer(t, tt.pages)

			got, err := client.GetPullRequestFiles(context.Background(), testPRRef)
			if err != nil {
				t.Fatalf("GetPullRequestFiles() returned error: %v", err)
			}

			if len(got) != tt.wantFiles {
				t.Errorf("got %d files, want %d", len(got), tt.wantFiles)
			}
			if tt.wantFirst != "" && got[0].Filename != tt.wantFirst {
				t.Errorf("first file = %q, want %q", got[0].Filename, tt.wantFirst)
			}

			var pages []string
			for _, r := range *recorded {
				pages = append(pages, r.Page)
				if r.PerPage != "100" {
					t.Errorf("per_page = %q on page %q, want 100", r.PerPage, r.Page)
				}
			}
			if strings.Join(pages, ",") != strings.Join(tt.wantPages, ",") {
				t.Errorf("requested pages %v, want %v", pages, tt.wantPages)
			}

			// Ordering across pages must be preserved.
			for i, f := range got {
				if want := fmt.Sprintf("file%d.go", i); f.Filename != want {
					t.Errorf("file %d = %q, want %q (page ordering lost)", i, f.Filename, want)
					break
				}
			}
		})
	}
}

// TestGetPullRequestFilesOneRequestPerPage guards the efficiency requirement:
// listing N files must not cost N requests.
func TestGetPullRequestFilesOneRequestPerPage(t *testing.T) {
	client, recorded := newFilesServer(t, []string{filesPage(0, 100), filesPage(100, 50)})

	files, err := client.GetPullRequestFiles(context.Background(), testPRRef)
	if err != nil {
		t.Fatalf("GetPullRequestFiles() returned error: %v", err)
	}

	if len(files) != 150 {
		t.Fatalf("got %d files, want 150", len(files))
	}
	if len(*recorded) != 2 {
		t.Errorf("made %d requests for 150 files, want 2 (one per page)", len(*recorded))
	}
}

// TestGetPullRequestFilesPageCap stops a server that always returns full pages.
func TestGetPullRequestFilesPageCap(t *testing.T) {
	var requests int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		fmt.Fprint(w, filesPage(0, 100)) // never a short page
	}))
	defer srv.Close()

	client := NewClientWithBaseURL(testToken, srv.URL, srv.Client())

	_, err := client.GetPullRequestFiles(context.Background(), testPRRef)
	if err == nil {
		t.Fatal("GetPullRequestFiles() = nil error, want a page-cap error")
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("error = %q, want it to mention the page cap", err)
	}
	if requests != maxFilePages {
		t.Errorf("made %d requests, want the cap of %d", requests, maxFilePages)
	}
}

func TestGetPullRequestFilesStatusErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantIs  error
		wantMsg string
	}{
		{"401 unauthorized", http.StatusUnauthorized, `{"message":"Bad credentials"}`, ErrUnauthorized, "authentication failed"},
		{"403 forbidden", http.StatusForbidden, `{"message":"API rate limit exceeded"}`, ErrForbidden, "access denied"},
		{"404 not found", http.StatusNotFound, `{"message":"Not Found"}`, ErrNotFound, "not found"},
		{"500 server error", http.StatusInternalServerError, `{"message":"Server Error"}`, nil, "status 500"},
		{"502 bad gateway", http.StatusBadGateway, `<html>boom</html>`, nil, "status 502"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()

			client := NewClientWithBaseURL(testToken, srv.URL, srv.Client())

			files, err := client.GetPullRequestFiles(context.Background(), testPRRef)
			if err == nil {
				t.Fatalf("GetPullRequestFiles() = %+v, want error", files)
			}
			if files != nil {
				t.Errorf("GetPullRequestFiles() = %+v on error, want nil", files)
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Errorf("errors.Is(err, %v) = false; err = %v", tt.wantIs, err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantMsg)
			}
			if strings.Contains(err.Error(), testToken) {
				t.Error("error contains the token")
			}
		})
	}
}

// TestGetPullRequestFilesErrorOnLaterPage checks a mid-pagination failure is
// surfaced rather than silently returning a partial list.
func TestGetPullRequestFilesErrorOnLaterPage(t *testing.T) {
	var requests int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			fmt.Fprint(w, filesPage(0, 100))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"API rate limit exceeded"}`)
	}))
	defer srv.Close()

	client := NewClientWithBaseURL(testToken, srv.URL, srv.Client())

	files, err := client.GetPullRequestFiles(context.Background(), testPRRef)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("errors.Is(err, ErrForbidden) = false; err = %v", err)
	}
	if files != nil {
		t.Errorf("GetPullRequestFiles() = %+v on error, want nil (no partial results)", files)
	}
}

func TestGetPullRequestFilesMalformedJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "truncated array", body: `[{"filename":"a.go",`},
		{name: "not json at all", body: `<html>hello</html>`},
		{name: "object instead of array", body: `{"filename":"a.go"}`},
		{name: "wrong type for additions", body: `[{"filename":"a.go","additions":"many"}]`},
		{name: "empty body", body: ``},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			client, _ := newFilesServer(t, []string{tt.body})

			files, err := client.GetPullRequestFiles(context.Background(), testPRRef)
			if err == nil {
				t.Fatalf("GetPullRequestFiles() = %+v, want decode error", files)
			}
			if !strings.Contains(err.Error(), "decode pull request files") {
				t.Errorf("error = %q, want it to mention decoding", err)
			}
		})
	}
}

func TestGetPullRequestFilesContextCanceled(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	client := NewClientWithBaseURL(testToken, srv.URL, srv.Client())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.GetPullRequestFiles(ctx, testPRRef)
	if err == nil {
		t.Fatal("GetPullRequestFiles() = nil error, want context cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; err = %v", err)
	}
}

// TestGetPullRequestFilesCancelBetweenPages cancels once the first page is in
// flight, so the second page is never requested.
func TestGetPullRequestFilesCancelBetweenPages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		cancel() // cancel while serving page 1
		fmt.Fprint(w, filesPage(0, 100))
	}))
	defer srv.Close()

	client := NewClientWithBaseURL(testToken, srv.URL, srv.Client())

	if _, err := client.GetPullRequestFiles(ctx, testPRRef); !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; err = %v", err)
	}
	if requests > 2 {
		t.Errorf("made %d requests after cancellation, want at most 2", requests)
	}
}

func TestGetPullRequestFilesMissingToken(t *testing.T) {
	client, _ := newFilesServer(t, []string{`[]`})
	client.token = ""

	if _, err := client.GetPullRequestFiles(context.Background(), testPRRef); !errors.Is(err, ErrMissingToken) {
		t.Errorf("errors.Is(err, ErrMissingToken) = false; err = %v", err)
	}
}

func TestSummarizeChanges(t *testing.T) {
	tests := []struct {
		name  string
		files []ChangedFile
		want  ChangeSummary
	}{
		{
			name:  "no files",
			files: nil,
			want:  ChangeSummary{},
		},
		{
			name:  "empty slice",
			files: []ChangedFile{},
			want:  ChangeSummary{},
		},
		{
			name: "three files",
			files: []ChangedFile{
				{Filename: "internal/payment/retry.go", Additions: 24, Deletions: 7},
				{Filename: "internal/payment/retry_test.go", Additions: 16, Deletions: 4},
				{Filename: "README.md", Additions: 2, Deletions: 0},
			},
			want: ChangeSummary{Files: 3, Additions: 42, Deletions: 11},
		},
		{
			name:  "deletion only",
			files: []ChangedFile{{Filename: "old.go", Status: "removed", Deletions: 88}},
			want:  ChangeSummary{Files: 1, Deletions: 88},
		},
		{
			name:  "patchless binary file still counts",
			files: []ChangedFile{{Filename: "logo.png", Status: "added"}},
			want:  ChangeSummary{Files: 1},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := SummarizeChanges(tt.files); got != tt.want {
				t.Errorf("SummarizeChanges() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestChangedFileHasPatch(t *testing.T) {
	if (ChangedFile{Patch: "@@ a @@"}).HasPatch() != true {
		t.Error("HasPatch() = false for a file with a patch")
	}
	if (ChangedFile{}).HasPatch() != false {
		t.Error("HasPatch() = true for a file without a patch")
	}
}

// TestChangedFileResponseDecodesMissingPatch documents that an absent patch key
// decodes to the empty string rather than failing.
func TestChangedFileResponseDecodesMissingPatch(t *testing.T) {
	for _, body := range []string{
		`{"filename":"a.bin","status":"added"}`,
		`{"filename":"a.bin","status":"added","patch":null}`,
		`{"filename":"a.bin","status":"added","patch":""}`,
	} {
		var got changedFileResponse
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("Unmarshal(%s) returned error: %v", body, err)
		}
		if got.Patch != "" {
			t.Errorf("Unmarshal(%s) patch = %q, want empty", body, strconv.Quote(got.Patch))
		}
	}
}
