package retrieval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRepo materializes a fake checkout from a path→content map.
func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}

func snippetFor(result Result, symbol string, relation Relation) (Snippet, bool) {
	for _, snippet := range result.Snippets {
		if snippet.Symbol == symbol && snippet.Relation == relation {
			return snippet, true
		}
	}
	return Snippet{}, false
}

// The central case: the change calls into a function it did not touch, and the
// diff alone cannot say whether the call is correct.
func TestRetrievesTheDefinitionOfACalledSymbol(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"internal/payment/retry.go": `package payment

func Charge(id string) error {
	return submitAuthorization(id)
}
`,
		"internal/payment/gateway.go": `package payment

// submitAuthorization sends the authorization to the gateway.
func submitAuthorization(id string) error {
	if id == "" {
		return errBadRequest
	}
	return nil
}

func unrelatedHelper() {}
`,
	})

	result, err := NewRetriever().Retrieve(context.Background(), root, []Change{{
		Path:  "internal/payment/retry.go",
		Patch: "@@ -1,3 +1,4 @@\n+	return submitAuthorization(id)\n",
	}})
	if err != nil {
		t.Fatalf("Retrieve() = %v", err)
	}
	if result.Skipped {
		t.Fatalf("retrieval skipped: %s", result.Reason)
	}

	snippet, ok := snippetFor(result, "submitAuthorization", RelationDefinition)
	if !ok {
		t.Fatalf("no definition retrieved; snippets = %+v", result.Snippets)
	}
	if snippet.Path != "internal/payment/gateway.go" {
		t.Errorf("path = %q, want internal/payment/gateway.go", snippet.Path)
	}
	if !strings.Contains(snippet.Content, "func submitAuthorization") {
		t.Errorf("content does not contain the definition:\n%s", snippet.Content)
	}
	// The next definition ends the snippet: a function body, not the rest of the file.
	if strings.Contains(snippet.Content, "unrelatedHelper") {
		t.Errorf("snippet ran past the definition it was for:\n%s", snippet.Content)
	}
}

// The direction a diff cannot show: the change rewrote a function, and whether
// that is safe depends on code the pull request never opened.
func TestRetrievesUnchangedCallersOfAChangedSymbol(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"internal/payment/retry.go": `package payment

func RetryPayment(id string) error {
	return nil
}
`,
		"internal/api/handler.go": `package api

func handleRetry(id string) error {
	// The caller assumes a nil error means the retry was accepted.
	return payment.RetryPayment(id)
}
`,
	})

	result, err := NewRetriever().Retrieve(context.Background(), root, []Change{{
		Path:  "internal/payment/retry.go",
		Patch: "@@ -1,4 +1,4 @@\n-func RetryPayment(id string) error {\n+func RetryPayment(id string) (bool, error) {\n",
	}})
	if err != nil {
		t.Fatalf("Retrieve() = %v", err)
	}
	if result.Skipped {
		t.Fatalf("retrieval skipped: %s", result.Reason)
	}

	snippet, ok := snippetFor(result, "RetryPayment", RelationCaller)
	if !ok {
		t.Fatalf("no caller retrieved; snippets = %+v", result.Snippets)
	}
	if snippet.Path != "internal/api/handler.go" {
		t.Errorf("path = %q, want internal/api/handler.go", snippet.Path)
	}
	if !strings.Contains(snippet.Content, "payment.RetryPayment(id)") {
		t.Errorf("content does not contain the use site:\n%s", snippet.Content)
	}
}

// Scala is the shape of the repository ARC is smoke-tested against, and def /
// trait / object definitions have to resolve there too.
func TestRetrievesScalaDefinitions(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"src/main/scala/campaign/Query.scala": `package campaign

object Query {
  def run(request: Request) = repository.findCampaigns(request)
}
`,
		"src/main/scala/campaign/Repository.scala": `package campaign

trait Repository {
  def findCampaigns(request: Request): ZIO[Any, Throwable, List[Campaign]]
}
`,
	})

	result, err := NewRetriever().Retrieve(context.Background(), root, []Change{{
		Path:  "src/main/scala/campaign/Query.scala",
		Patch: "@@ -1,3 +1,4 @@\n+  def run(request: Request) = repository.findCampaigns(request)\n",
	}})
	if err != nil {
		t.Fatalf("Retrieve() = %v", err)
	}
	if result.Skipped {
		t.Fatalf("retrieval skipped: %s", result.Reason)
	}

	snippet, ok := snippetFor(result, "findCampaigns", RelationDefinition)
	if !ok {
		t.Fatalf("no Scala definition retrieved; snippets = %+v", result.Snippets)
	}
	if snippet.Path != "src/main/scala/campaign/Repository.scala" {
		t.Errorf("path = %q", snippet.Path)
	}
}

// Retrieval exists to add what the diff lacks. Re-showing a changed file would
// spend the budget on context the reviewer already has.
func TestNeverRetrievesAChangedFile(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"internal/payment/retry.go": `package payment

func helperFunction() error { return nil }

func Charge() error {
	return helperFunction()
}
`,
	})

	result, err := NewRetriever().Retrieve(context.Background(), root, []Change{{
		Path:  "internal/payment/retry.go",
		Patch: "@@ -1,5 +1,6 @@\n+	return helperFunction()\n",
	}})
	if err != nil {
		t.Fatalf("Retrieve() = %v", err)
	}

	for _, snippet := range result.Snippets {
		if snippet.Path == "internal/payment/retry.go" {
			t.Errorf("retrieved a changed file: %s", snippet.Location())
		}
	}
}

// Vendored trees and build output would dominate a symbol index while telling a
// reviewer nothing about the change.
func TestExcludesVendoredAndGeneratedTrees(t *testing.T) {
	definition := `package vendored

func specialHelper() error { return nil }
`
	root := writeRepo(t, map[string]string{
		"node_modules/pkg/index.js":   "function specialHelper() {}\n",
		"vendor/lib/lib.go":           definition,
		"target/scala-2.13/Gen.scala": "object Gen { def specialHelper = 1 }\n",
		"internal/app/app.go":         "package app\n\nfunc Run() error { return specialHelper() }\n",
	})

	result, err := NewRetriever().Retrieve(context.Background(), root, []Change{{
		Path:  "internal/app/app.go",
		Patch: "@@ -1,2 +1,3 @@\n+func Run() error { return specialHelper() }\n",
	}})
	if err != nil {
		t.Fatalf("Retrieve() = %v", err)
	}

	for _, snippet := range result.Snippets {
		for _, excluded := range []string{"node_modules/", "vendor/", "target/"} {
			if strings.HasPrefix(snippet.Path, excluded) {
				t.Errorf("retrieved from an excluded tree: %s", snippet.Path)
			}
		}
	}
}

func TestSkipReasonsAreExplicit(t *testing.T) {
	cases := []struct {
		name    string
		root    string
		changes []Change
		want    string
	}{
		{"no checkout", "", []Change{{Path: "a.go", Patch: "+x"}}, "not provided"},
		{"unreadable checkout", filepath.Join(t.TempDir(), "absent"), []Change{{Path: "a.go", Patch: "+x"}}, "not readable"},
		{"no changes", t.TempDir(), nil, "no changed files"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := NewRetriever().Retrieve(context.Background(), tc.root, tc.changes)
			if err != nil {
				t.Fatalf("Retrieve() = %v", err)
			}
			if !result.Skipped {
				t.Fatal("Retrieve() did not skip")
			}
			if !strings.Contains(result.Reason, tc.want) {
				t.Errorf("reason = %q, want it to mention %q", result.Reason, tc.want)
			}
		})
	}
}

// A skip is never an error: absence degrades the review instead of ending it.
func TestUnresolvableChangeSkipsRatherThanFails(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"README.md": "no code here\n",
	})

	result, err := NewRetriever().Retrieve(context.Background(), root, []Change{{
		Path:  "README.md",
		Patch: "@@ -1 +1,2 @@\n+some documentation prose\n",
	}})
	if err != nil {
		t.Fatalf("Retrieve() = %v", err)
	}
	if !result.Skipped {
		t.Errorf("Retrieve() = %+v, want a skip", result)
	}
}

func TestRetrievalIsBounded(t *testing.T) {
	files := map[string]string{}
	var patch strings.Builder
	patch.WriteString("@@ -1,1 +1,80 @@\n")
	for i := 0; i < 80; i++ {
		name := "symbolNumber" + string(rune('A'+i%26)) + strings.Repeat("x", i%5)
		files["pkg/def"+strings.Repeat("y", i%7)+string(rune('a'+i%26))+".go"] =
			"package pkg\n\nfunc " + name + "() error {\n" + strings.Repeat("\t// filler line to make this long\n", 200) + "\treturn nil\n}\n"
		patch.WriteString("+	" + name + "()\n")
	}
	files["pkg/changed.go"] = "package pkg\n\nfunc Changed() {}\n"
	root := writeRepo(t, files)

	result, err := NewRetriever().Retrieve(context.Background(), root, []Change{{
		Path: "pkg/changed.go", Patch: patch.String(),
	}})
	if err != nil {
		t.Fatalf("Retrieve() = %v", err)
	}

	if len(result.Snippets) > MaxSnippets {
		t.Errorf("snippets = %d, want at most %d", len(result.Snippets), MaxSnippets)
	}
	if result.Stats.Bytes > MaxTotalBytes {
		t.Errorf("bytes = %d, want at most %d", result.Stats.Bytes, MaxTotalBytes)
	}
	for _, snippet := range result.Snippets {
		if snippet.Bytes() > MaxSnippetBytes+64 {
			t.Errorf("snippet %s = %d bytes, want at most %d", snippet.Location(), snippet.Bytes(), MaxSnippetBytes)
		}
	}
}

// Determinism is what lets two runs be compared: the same checkout and patches
// must produce the same snippets in the same order.
func TestRetrievalIsDeterministic(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"a/one.go":     "package a\n\nfunc sharedHelper() error { return nil }\n",
		"b/two.go":     "package b\n\nfunc sharedHelper() error { return nil }\n",
		"c/three.go":   "package c\n\nfunc otherHelper() error { return nil }\n",
		"d/changed.go": "package d\n\nfunc Changed() error { return nil }\n",
	})
	change := []Change{{
		Path:  "d/changed.go",
		Patch: "@@ -1,3 +1,5 @@\n+	sharedHelper()\n+	otherHelper()\n",
	}}

	first, err := NewRetriever().Retrieve(context.Background(), root, change)
	if err != nil {
		t.Fatalf("Retrieve() = %v", err)
	}
	for i := 0; i < 3; i++ {
		again, err := NewRetriever().Retrieve(context.Background(), root, change)
		if err != nil {
			t.Fatalf("Retrieve() = %v", err)
		}
		if len(again.Snippets) != len(first.Snippets) {
			t.Fatalf("snippet count changed between runs: %d then %d", len(first.Snippets), len(again.Snippets))
		}
		for j := range again.Snippets {
			if again.Snippets[j].Location() != first.Snippets[j].Location() {
				t.Errorf("snippet %d moved: %s then %s", j,
					first.Snippets[j].Location(), again.Snippets[j].Location())
			}
		}
	}
}

func TestRetrieveHonorsCancellation(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"a/one.go":     "package a\n\nfunc someHelper() error { return nil }\n",
		"b/changed.go": "package b\n\nfunc Changed() {}\n",
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewRetriever().Retrieve(ctx, root, []Change{{
		Path: "b/changed.go", Patch: "@@ -1,2 +1,3 @@\n+	someHelper()\n",
	}})
	if err == nil {
		t.Fatal("Retrieve() = nil error, want the cancellation")
	}
}

func TestTouchedSymbolsIgnoresContextAndImports(t *testing.T) {
	patch := `@@ -1,6 +1,8 @@
 	untouchedIdentifier()
+import somePackage.somethingImported
+	deliberateCall()
-	removedCall()
 	anotherUntouched()
`
	var names []string
	for _, symbol := range touchedSymbols(patch) {
		names = append(names, symbol.Name)
	}
	joined := strings.Join(names, ",")

	for _, want := range []string{"deliberateCall", "removedCall"} {
		if !strings.Contains(joined, want) {
			t.Errorf("touched symbols %v missing %q", names, want)
		}
	}
	for _, unwanted := range []string{"untouchedIdentifier", "anotherUntouched", "somethingImported"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("touched symbols %v should not contain %q", names, unwanted)
		}
	}
}

func TestDefinitionsInIgnoresComments(t *testing.T) {
	goLang, _ := languageFor("x.go")
	if got := definitionsIn(goLang, "// func commentedOut() {}"); len(got) != 0 {
		t.Errorf("definitionsIn(comment) = %v, want none", got)
	}
	if got := definitionsIn(goLang, "func realFunction() {}"); len(got) != 1 || got[0] != "realFunction" {
		t.Errorf("definitionsIn(func) = %v, want [realFunction]", got)
	}
}
