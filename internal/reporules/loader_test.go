package reporules

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/github"
)

// readRequest records one call into the fake reader.
type readRequest struct {
	Owner string
	Repo  string
	Path  string
	Ref   string
}

// fakeReader serves canned content or errors per path and records every call.
type fakeReader struct {
	// files maps a repository path to its content.
	files map[string]string

	// errs maps a repository path to an error to return instead of content.
	errs map[string]error

	// notFound is the error returned for paths in neither map.
	notFound error

	requests []readRequest
}

func newFakeReader(files map[string]string) *fakeReader {
	return &fakeReader{
		files:    files,
		errs:     map[string]error{},
		notFound: fmt.Errorf("get contents: %w", github.ErrNotFound),
	}
}

func (f *fakeReader) GetRepositoryFile(ctx context.Context, owner, repo, path, ref string) (string, error) {
	f.requests = append(f.requests, readRequest{Owner: owner, Repo: repo, Path: path, Ref: ref})

	if err, ok := f.errs[path]; ok {
		return "", err
	}
	if content, ok := f.files[path]; ok {
		return content, nil
	}
	return "", f.notFound
}

// paths returns the paths requested, in call order.
func (f *fakeReader) paths() []string {
	paths := make([]string, 0, len(f.requests))
	for _, r := range f.requests {
		paths = append(paths, r.Path)
	}
	return paths
}

const (
	agentsPath  = "AGENTS.md"
	contribPath = "CONTRIBUTING.md"
	aiRulesPath = ".ai-review/rules.md"
	headSHA     = "abc123"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name      string
		files     map[string]string
		wantPaths []string
	}{
		{
			name: "all three rule files exist",
			files: map[string]string{
				aiRulesPath: "review rules",
				agentsPath:  "agent rules",
				contribPath: "contributing rules",
			},
			// Priority order, not alphabetical.
			wantPaths: []string{aiRulesPath, agentsPath, contribPath},
		},
		{
			name:      "only AGENTS.md exists",
			files:     map[string]string{agentsPath: "agent rules"},
			wantPaths: []string{agentsPath},
		},
		{
			name:      "only .ai-review/rules.md exists",
			files:     map[string]string{aiRulesPath: "review rules"},
			wantPaths: []string{aiRulesPath},
		},
		{
			name:      "only CONTRIBUTING.md exists",
			files:     map[string]string{contribPath: "contributing rules"},
			wantPaths: []string{contribPath},
		},
		{
			name:      "no rule files exist",
			files:     map[string]string{},
			wantPaths: nil,
		},
		{
			name: "AGENTS.md and CONTRIBUTING.md but no .ai-review",
			files: map[string]string{
				agentsPath:  "agent rules",
				contribPath: "contributing rules",
			},
			wantPaths: []string{agentsPath, contribPath},
		},
		{
			name: ".ai-review and CONTRIBUTING but no AGENTS",
			files: map[string]string{
				aiRulesPath: "review rules",
				contribPath: "contributing rules",
			},
			wantPaths: []string{aiRulesPath, contribPath},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			reader := newFakeReader(tt.files)
			loader := NewLoader(reader)

			rules, err := loader.Load(context.Background(), "acme", "payments", headSHA)
			if err != nil {
				t.Fatalf("Load() returned error: %v", err)
			}

			if strings.Join(rules.Paths(), ",") != strings.Join(tt.wantPaths, ",") {
				t.Errorf("paths = %v, want %v", rules.Paths(), tt.wantPaths)
			}
			if rules.Count() != len(tt.wantPaths) {
				t.Errorf("Count() = %d, want %d", rules.Count(), len(tt.wantPaths))
			}
			if rules.Empty() != (len(tt.wantPaths) == 0) {
				t.Errorf("Empty() = %t, want %t", rules.Empty(), len(tt.wantPaths) == 0)
			}

			// Content must match what the reader served.
			for _, doc := range rules.Documents {
				if want := tt.files[doc.Path]; doc.Content != want {
					t.Errorf("content of %s = %q, want %q", doc.Path, doc.Content, want)
				}
				if doc.Truncated {
					t.Errorf("%s marked truncated unexpectedly", doc.Path)
				}
			}
		})
	}
}

// TestLoadPriorityOrderIsStable checks the order does not depend on which files
// exist or on map iteration.
func TestLoadPriorityOrderIsStable(t *testing.T) {
	files := map[string]string{
		contribPath: "c",
		agentsPath:  "a",
		aiRulesPath: "r",
	}

	// Repeat, since a map-driven implementation would only fail intermittently.
	for i := 0; i < 20; i++ {
		reader := newFakeReader(files)

		rules, err := NewLoader(reader).Load(context.Background(), "acme", "payments", headSHA)
		if err != nil {
			t.Fatalf("Load() returned error: %v", err)
		}

		want := []string{aiRulesPath, agentsPath, contribPath}
		if strings.Join(rules.Paths(), ",") != strings.Join(want, ",") {
			t.Fatalf("run %d: paths = %v, want %v", i, rules.Paths(), want)
		}
		if strings.Join(reader.paths(), ",") != strings.Join(want, ",") {
			t.Fatalf("run %d: requested %v, want %v", i, reader.paths(), want)
		}
	}
}

// TestLoadRequestsEachPathOnce guards against duplicate fetching.
func TestLoadRequestsEachPathOnce(t *testing.T) {
	reader := newFakeReader(map[string]string{
		aiRulesPath: "r",
		agentsPath:  "a",
		contribPath: "c",
	})

	if _, err := NewLoader(reader).Load(context.Background(), "acme", "payments", headSHA); err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if len(reader.requests) != len(RulePaths) {
		t.Errorf("made %d requests, want %d (one per rule path)", len(reader.requests), len(RulePaths))
	}

	seen := map[string]int{}
	for _, r := range reader.requests {
		seen[r.Path]++
	}
	for path, count := range seen {
		if count != 1 {
			t.Errorf("path %s requested %d times, want once", path, count)
		}
	}
}

// TestLoadUsesTheGivenRef checks every read is pinned to the supplied head SHA.
func TestLoadUsesTheGivenRef(t *testing.T) {
	const sha = "0f1e2d3c4b5a69788796a5b4c3d2e1f0aabbccdd"

	reader := newFakeReader(map[string]string{agentsPath: "a"})

	if _, err := NewLoader(reader).Load(context.Background(), "acme", "payments", sha); err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if len(reader.requests) == 0 {
		t.Fatal("no requests were made")
	}
	for _, r := range reader.requests {
		if r.Ref != sha {
			t.Errorf("path %s read at ref %q, want %q", r.Path, r.Ref, sha)
		}
		if r.Owner != "acme" {
			t.Errorf("owner = %q, want acme", r.Owner)
		}
		if r.Repo != "payments" {
			t.Errorf("repo = %q, want payments", r.Repo)
		}
	}
}

// TestLoadOnlyReadsTheAllowList is the security guarantee: no other path is
// touched, and the repository is never scanned.
func TestLoadOnlyReadsTheAllowList(t *testing.T) {
	reader := newFakeReader(map[string]string{
		agentsPath: "a",
		".env":     "SECRET=hunter2",
		"id_rsa":   "-----BEGIN PRIVATE KEY-----",
	})

	rules, err := NewLoader(reader).Load(context.Background(), "acme", "payments", headSHA)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	allowed := map[string]bool{}
	for _, p := range RulePaths {
		allowed[p] = true
	}
	for _, r := range reader.requests {
		if !allowed[r.Path] {
			t.Errorf("loader read %q, which is not on the allow-list", r.Path)
		}
	}

	for _, doc := range rules.Documents {
		if !allowed[doc.Path] {
			t.Errorf("rules contain %q, which is not on the allow-list", doc.Path)
		}
		if strings.Contains(doc.Content, "SECRET") || strings.Contains(doc.Content, "PRIVATE KEY") {
			t.Errorf("rule document %s contains secret material", doc.Path)
		}
	}
}

func TestLoadSkipsEmptyDocuments(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "empty file", content: ""},
		{name: "spaces only", content: "   "},
		{name: "newlines only", content: "\n\n\n"},
		{name: "tabs and newlines", content: "\t\n \t\n"},
		{name: "carriage returns", content: "\r\n\r\n"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			reader := newFakeReader(map[string]string{
				aiRulesPath: tt.content,
				agentsPath:  "real rules",
			})

			rules, err := NewLoader(reader).Load(context.Background(), "acme", "payments", headSHA)
			if err != nil {
				t.Fatalf("Load() returned error: %v", err)
			}

			if strings.Join(rules.Paths(), ",") != agentsPath {
				t.Errorf("paths = %v, want only %s", rules.Paths(), agentsPath)
			}
		})
	}
}

func TestLoadAllDocumentsEmpty(t *testing.T) {
	reader := newFakeReader(map[string]string{
		aiRulesPath: "",
		agentsPath:  "   ",
		contribPath: "\n",
	})

	rules, err := NewLoader(reader).Load(context.Background(), "acme", "payments", headSHA)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if !rules.Empty() {
		t.Errorf("Empty() = false, want true; paths = %v", rules.Paths())
	}
}

// TestLoadIgnoresNotFound checks a 404-equivalent for an optional file is not an
// error, while the remaining files are still read.
func TestLoadIgnoresNotFound(t *testing.T) {
	reader := newFakeReader(map[string]string{contribPath: "contributing rules"})
	reader.errs[aiRulesPath] = fmt.Errorf("get contents: %w", github.ErrNotFound)
	reader.errs[agentsPath] = &github.APIError{StatusCode: 404, Message: "Not Found"}

	rules, err := NewLoader(reader).Load(context.Background(), "acme", "payments", headSHA)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if strings.Join(rules.Paths(), ",") != contribPath {
		t.Errorf("paths = %v, want only %s", rules.Paths(), contribPath)
	}
	// The loader must keep going after a missing file.
	if len(reader.requests) != len(RulePaths) {
		t.Errorf("made %d requests, want %d; it stopped early", len(reader.requests), len(RulePaths))
	}
}

func TestLoadPropagatesRealFailures(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantIs  error
		wantMsg string
	}{
		{
			name:   "401 unauthorized",
			err:    &github.APIError{StatusCode: 401, Message: "Bad credentials"},
			wantIs: github.ErrUnauthorized,
		},
		{
			name:   "403 forbidden",
			err:    &github.APIError{StatusCode: 403, Message: "API rate limit exceeded"},
			wantIs: github.ErrForbidden,
		},
		{
			name:    "429 rate limited",
			err:     &github.APIError{StatusCode: 429, Message: "Too many requests"},
			wantMsg: "status 429",
		},
		{
			name:    "500 server error",
			err:     &github.APIError{StatusCode: 500, Message: "Server Error"},
			wantMsg: "status 500",
		},
		{
			name:    "network failure",
			err:     errors.New("dial tcp 140.82.121.6:443: connect: connection refused"),
			wantMsg: "connection refused",
		},
		{
			name:    "context cancellation",
			err:     context.Canceled,
			wantIs:  context.Canceled,
			wantMsg: "context canceled",
		},
		{
			name:    "unsupported content",
			err:     fmt.Errorf("%w: AGENTS.md is a dir", github.ErrUnsupportedContent),
			wantIs:  github.ErrUnsupportedContent,
			wantMsg: "AGENTS.md",
		},
		{
			name:    "base64 failure",
			err:     errors.New("decode base64 content of AGENTS.md: illegal base64 data"),
			wantMsg: "base64",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			// Fail on the second path, so the first has already succeeded.
			reader := newFakeReader(map[string]string{aiRulesPath: "review rules"})
			reader.errs[agentsPath] = tt.err

			rules, err := NewLoader(reader).Load(context.Background(), "acme", "payments", headSHA)
			if err == nil {
				t.Fatalf("Load() = %+v, want error", rules)
			}
			if !rules.Empty() {
				t.Errorf("Load() returned %v on error, want no documents", rules.Paths())
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Errorf("errors.Is(err, %v) = false; err = %v", tt.wantIs, err)
			}
			if tt.wantMsg != "" && !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantMsg)
			}
			// The error should say which document failed.
			if !strings.Contains(err.Error(), agentsPath) {
				t.Errorf("error = %q, want it to name %s", err, agentsPath)
			}
		})
	}
}

func TestLoadTruncatesOversizedDocuments(t *testing.T) {
	tests := []struct {
		name          string
		size          int
		wantTruncated bool
	}{
		{name: "well under the limit", size: 1024},
		{name: "one byte under the limit", size: MaxRuleDocumentBytes - 1},
		{name: "exactly at the limit", size: MaxRuleDocumentBytes},
		{name: "one byte over the limit", size: MaxRuleDocumentBytes + 1, wantTruncated: true},
		{name: "far over the limit", size: 5 * 1024 * 1024, wantTruncated: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			content := strings.Repeat("a", tt.size)
			reader := newFakeReader(map[string]string{agentsPath: content})

			rules, err := NewLoader(reader).Load(context.Background(), "acme", "payments", headSHA)
			if err != nil {
				t.Fatalf("Load() returned error: %v", err)
			}
			if len(rules.Documents) != 1 {
				t.Fatalf("got %d documents, want 1", len(rules.Documents))
			}

			doc := rules.Documents[0]
			if doc.Truncated != tt.wantTruncated {
				t.Errorf("Truncated = %t, want %t", doc.Truncated, tt.wantTruncated)
			}

			marker := fmt.Sprintf("[TRUNCATED: rule document exceeded %d bytes]", MaxRuleDocumentBytes)

			if !tt.wantTruncated {
				if doc.Content != content {
					t.Errorf("content was altered for a document within the limit")
				}
				if strings.Contains(doc.Content, marker) {
					t.Error("content carries a truncation marker but was not truncated")
				}
				return
			}

			if !strings.Contains(doc.Content, marker) {
				t.Errorf("content lacks the truncation marker; tail = %q", tail(doc.Content, 80))
			}
			if !strings.HasPrefix(doc.Content, content[:1024]) {
				t.Error("truncated content does not start with the original text")
			}
			if len(doc.Content) > MaxRuleDocumentBytes+len(marker)+8 {
				t.Errorf("truncated content is %d bytes, want about %d", len(doc.Content), MaxRuleDocumentBytes)
			}
		})
	}
}

// TestTruncationIsDeterministic checks the same oversized input always produces
// byte-identical output.
func TestTruncationIsDeterministic(t *testing.T) {
	content := strings.Repeat("rule text ", MaxRuleDocumentBytes/5)

	var first string
	for i := 0; i < 5; i++ {
		reader := newFakeReader(map[string]string{agentsPath: content})

		rules, err := NewLoader(reader).Load(context.Background(), "acme", "payments", headSHA)
		if err != nil {
			t.Fatalf("Load() returned error: %v", err)
		}

		got := rules.Documents[0].Content
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("run %d produced different content than run 0", i)
		}
	}
}

// TestTruncationKeepsValidUTF8 makes sure a cut never lands inside a rune.
func TestTruncationKeepsValidUTF8(t *testing.T) {
	// "é" is two bytes, so a cut at the limit lands mid-rune for some offsets.
	for offset := 0; offset < 4; offset++ {
		content := strings.Repeat("x", offset) + strings.Repeat("é", MaxRuleDocumentBytes)

		reader := newFakeReader(map[string]string{agentsPath: content})

		rules, err := NewLoader(reader).Load(context.Background(), "acme", "payments", headSHA)
		if err != nil {
			t.Fatalf("Load() returned error: %v", err)
		}

		got := rules.Documents[0].Content
		if !utf8ValidString(got) {
			t.Errorf("offset %d: truncated content is not valid UTF-8", offset)
		}
		if strings.Contains(got, "�") {
			t.Errorf("offset %d: truncated content contains a replacement character", offset)
		}
	}
}

func TestRulesHelpers(t *testing.T) {
	t.Run("empty rules", func(t *testing.T) {
		var rules Rules

		if !rules.Empty() {
			t.Error("Empty() = false on zero Rules")
		}
		if rules.Count() != 0 {
			t.Errorf("Count() = %d, want 0", rules.Count())
		}
		if got := rules.CombinedText(); got != "" {
			t.Errorf("CombinedText() = %q, want empty", got)
		}
		if got := rules.Paths(); len(got) != 0 {
			t.Errorf("Paths() = %v, want empty", got)
		}
	})

	t.Run("combined text keeps order and labels each document", func(t *testing.T) {
		rules := Rules{Documents: []RuleDocument{
			{Path: aiRulesPath, Content: "review rules"},
			{Path: agentsPath, Content: "agent rules"},
		}}

		got := rules.CombinedText()

		wantOrder := []string{aiRulesPath, "review rules", agentsPath, "agent rules"}
		last := -1
		for _, want := range wantOrder {
			idx := strings.Index(got, want)
			if idx < 0 {
				t.Fatalf("CombinedText() missing %q; got:\n%s", want, got)
			}
			if idx < last {
				t.Errorf("CombinedText() has %q out of order; got:\n%s", want, got)
			}
			last = idx
		}
	})

	t.Run("single document", func(t *testing.T) {
		rules := Rules{Documents: []RuleDocument{{Path: agentsPath, Content: "only"}}}

		if rules.Empty() {
			t.Error("Empty() = true with one document")
		}
		if !strings.Contains(rules.CombinedText(), "only") {
			t.Error("CombinedText() lost the content")
		}
	})
}

// TestRulePathsAllowList pins the supported files and their order.
func TestRulePathsAllowList(t *testing.T) {
	want := []string{".ai-review/rules.md", "AGENTS.md", "CONTRIBUTING.md"}

	if strings.Join(RulePaths, ",") != strings.Join(want, ",") {
		t.Errorf("RulePaths = %v, want %v", RulePaths, want)
	}
}

func TestMaxRuleDocumentBytes(t *testing.T) {
	if MaxRuleDocumentBytes != 100*1024 {
		t.Errorf("MaxRuleDocumentBytes = %d, want %d", MaxRuleDocumentBytes, 100*1024)
	}
}

// Helpers.

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}
