package publish

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/github"
)

// TestOnlyOneRemoteWriteExists is the architecture check behind the whole tool: the
// single write it can perform is publishing a pull request review.
//
// It reads the source rather than the behavior, because the guarantee is about what
// code exists at all. A future change that adds a mutating HTTP call, a git
// subcommand, or a Jira write will fail here, which is the point: the invariant should
// be hard to lose by accident.
func TestOnlyOneRemoteWriteExists(t *testing.T) {
	root := repoRoot(t)

	// Patterns that must not appear in non-test source. Each is paired with what it
	// would mean if it did.
	forbidden := []struct {
		pattern string
		why     string
	}{
		{`http.MethodPut`, "a PUT would modify a resource; review publication is a POST"},
		{`http.MethodPatch`, "a PATCH would modify a resource"},
		{`http.MethodDelete`, "nothing in this tool may delete a resource"},
		{`"PUT"`, "a PUT would modify a resource"},
		{`"PATCH"`, "a PATCH would modify a resource"},
		{`"DELETE"`, "nothing in this tool may delete a resource"},
		{`/merge`, "merging a pull request is outside this tool's authority"},
		{`"APPROVE"`, "this tool is not an approval authority"},
		{`"REQUEST_CHANGES"`, "this tool does not request changes"},
		{`git commit`, "this tool never commits"},
		{`git push`, "this tool never pushes"},
		{`"commit"`, "this tool never commits"},
		{`"push"`, "this tool never pushes"},
		{`os.WriteFile`, "this tool never writes repository files"},
		{`os.Create`, "this tool never writes repository files"},
		{`os.OpenFile`, "the only file this tool creates is an opt-in prediction snapshot"},
		{`/contents/`, "repository content endpoints must be read-only"},
		{`/transitions`, "this tool never modifies Jira"},
	}

	// The one permitted write, and the read endpoints it depends on.
	allowedPostPaths := map[string]bool{
		"internal/github/review.go": true,
	}

	// The one permitted local write. Capture exists so review quality can be
	// measured, and it is held to the same discipline as publication: a single
	// allow-listed file, named here so a second writer has to be added
	// deliberately rather than appearing as an implementation detail.
	allowedFileWritePaths := map[string]bool{
		"internal/evaluation/capture.go": true,
	}

	for _, path := range goSourceFiles(t, root) {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("filepath.Rel() = %v", err)
		}
		if strings.HasSuffix(relative, "_test.go") {
			continue
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		source := string(raw)

		for _, f := range forbidden {
			if !strings.Contains(source, f.pattern) {
				continue
			}
			if f.pattern == "os.OpenFile" && allowedFileWritePaths[relative] {
				continue
			}
			// A GET of a contents endpoint is how repository rules are read; only
			// a write to one is forbidden.
			if f.pattern == "/contents/" && readOnlyContents(source) {
				continue
			}
			t.Errorf("%s contains %q: %s", relative, f.pattern, f.why)
		}

		if strings.Contains(source, "http.MethodPost") && !allowedPostPaths[relative] {
			t.Errorf("%s issues an HTTP POST; the only permitted write is review publication in %v",
				relative, keys(allowedPostPaths))
		}
	}
}

// TestCaptureNeverOverwrites states the property that makes the one local write
// safe: capture creates a file or fails, so it can never destroy a label set, an
// earlier snapshot, or any other file named by mistake.
func TestCaptureNeverOverwrites(t *testing.T) {
	source := readFile(t, filepath.Join(repoRoot(t), "internal/evaluation/capture.go"))

	for _, required := range []string{"os.O_EXCL", "os.O_CREATE"} {
		if !strings.Contains(source, required) {
			t.Errorf("capture.go does not use %s; capture must never overwrite an existing file", required)
		}
	}
	for _, forbidden := range []string{"os.O_TRUNC", "os.O_APPEND", "os.Remove", "os.Rename"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("capture.go uses %s; capture may only create a new file", forbidden)
		}
	}
}

// readOnlyContents reports whether a file that mentions the contents endpoint only
// ever reads from it.
func readOnlyContents(source string) bool {
	return !strings.Contains(source, "http.MethodPost") &&
		!strings.Contains(source, "http.MethodPut")
}

// TestPublicationAPIIsMinimal states the whole GitHub surface publication may use.
//
// The interface is the boundary: if a method were added here that could modify a
// branch, a file, or a merge state, this test would have to be edited deliberately.
func TestPublicationAPIIsMinimal(t *testing.T) {
	// Compile-time proof that both the real client and the test fake satisfy the
	// same narrow interface.
	var _ API = &fakeAPI{}
	var _ API = github.NewClient("")

	// The assertions below are the readable form of the same statement: two reads
	// and one write.
	reads := []string{"GetPullRequest", "ListPullRequestReviews"}
	writes := []string{"CreatePullRequestReview"}

	source := readFile(t, filepath.Join(repoRoot(t), "internal/publish/publisher.go"))
	for _, method := range append(reads, writes...) {
		if !strings.Contains(source, method+"(") {
			t.Errorf("publisher.go does not declare %s; the API surface changed", method)
		}
	}

	// And nothing beyond those three: the interface body is read directly so an added
	// method has to be added here too, deliberately.
	body := source[strings.Index(source, "type API interface {"):]
	body = body[:strings.Index(body, "}")]
	if got := strings.Count(body, "ctx context.Context"); got != len(reads)+len(writes) {
		t.Errorf("publish.API declares %d methods, want exactly %d", got, len(reads)+len(writes))
	}
}

// TestClaudeNeverReachesGitHub checks the model adapter has no path to the API. Claude
// analyzes and proposes; it does not decide or publish.
func TestClaudeNeverReachesGitHub(t *testing.T) {
	root := repoRoot(t)

	for _, path := range goSourceFiles(t, filepath.Join(root, "internal/claude")) {
		relative, _ := filepath.Rel(root, path)
		if strings.HasSuffix(relative, "_test.go") {
			continue
		}

		source := readFile(t, path)
		for _, forbidden := range []string{
			"internal/github",
			"internal/publish",
			"net/http",
		} {
			if strings.Contains(source, forbidden) {
				t.Errorf("%s imports %q; the model adapter must not reach GitHub", relative, forbidden)
			}
		}
	}
}

// TestPublishDoesNotDependOnClaude checks the dependency runs one way: publication
// consumes a validated result and knows nothing about how it was produced.
func TestPublishDoesNotDependOnClaude(t *testing.T) {
	root := repoRoot(t)

	for _, path := range goSourceFiles(t, filepath.Join(root, "internal/publish")) {
		relative, _ := filepath.Rel(root, path)
		if strings.HasSuffix(relative, "_test.go") {
			continue
		}
		if strings.Contains(readFile(t, path), "internal/claude") {
			t.Errorf("%s imports internal/claude; publication must not depend on the model adapter", relative)
		}
	}
}

// repoRoot walks up from the test's directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() = %v", err)
	}

	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	t.Fatal("could not locate the module root")
	return ""
}

// goSourceFiles lists every .go file under root.
func goSourceFiles(t *testing.T, root string) []string {
	t.Helper()

	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "bin" || info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(files) == 0 {
		t.Fatalf("walk %s found no Go files; the invariant scan would pass vacuously", root)
	}
	return files
}

// readFile reads a source file for inspection.
func readFile(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// keys returns a map's keys, for an error message.
func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
