package contextselect

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/analysis"
	"github.com/your-company/agentic-code-review/internal/review"
)

// Test helpers.

func file(path, patch string) review.FileChange {
	return review.FileChange{Filename: path, Status: "modified", Patch: patch,
		Additions: 1, Deletions: 1, Changes: 2}
}

func patchOf(size int) string {
	return "@@ -1,1 +1,1 @@\n" + strings.Repeat("+x\n", size/3)
}

func reviewContextWith(files ...review.FileChange) review.Context {
	return review.Context{
		PullRequest: review.PullRequestContext{
			Owner: "acme", Repository: "payments", Number: 123,
			Title: "Add payment retry", Author: "alice",
			BaseBranch: "main", HeadBranch: "feature/PAY-431", HeadSHA: "abc123",
		},
		Changes: review.ChangeContext{Files: files, FileCount: len(files)},
	}
}

// selectOrFail runs the selector and fails the test on error.
func selectOrFail(t *testing.T, s *Selector, rc review.Context, ar analysis.Result) SelectedContext {
	t.Helper()

	got, err := s.Select(context.Background(), rc, ar)
	if err != nil {
		t.Fatalf("Select() returned error: %v", err)
	}
	return got
}

// paths returns the selected file paths, in order.
func paths(files []SelectedFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func TestSelectPullRequestSummary(t *testing.T) {
	got := selectOrFail(t, NewSelector(), reviewContextWith(file("a.go", "patch")), analysis.Result{})

	pr := got.PullRequest
	if pr.Owner != "acme" || pr.Repository != "payments" || pr.Number != 123 {
		t.Errorf("PullRequest = %+v, want acme/payments#123", pr)
	}
	if pr.Slug() != "acme/payments" {
		t.Errorf("Slug() = %q, want acme/payments", pr.Slug())
	}
	if pr.Title != "Add payment retry" || pr.Author != "alice" || pr.HeadSHA != "abc123" {
		t.Errorf("PullRequest = %+v, want the metadata carried through", pr)
	}
}

func TestSelectClassifiesAndRanksFiles(t *testing.T) {
	rc := reviewContextWith(
		file("README.md", "docs patch"),
		file("api/payment.pb.go", "generated patch"),
		file("go.mod", "dependency patch"),
		file("deploy/values.yaml", "config patch"),
		file("migrations/0042_retry.sql", "migration patch"),
		file("internal/payment/service_test.go", "test patch"),
		file("internal/payment/service.go", "source patch"),
	)

	got := selectOrFail(t, NewSelector(), rc, analysis.Result{})

	// Priority order, regardless of the order the files arrived in.
	want := []string{
		"internal/payment/service.go",
		"internal/payment/service_test.go",
		"migrations/0042_retry.sql",
		"deploy/values.yaml",
		"go.mod",
		"README.md",
		"api/payment.pb.go",
	}
	if strings.Join(paths(got.Files), ",") != strings.Join(want, ",") {
		t.Errorf("order = %v\nwant %v", paths(got.Files), want)
	}

	wantKinds := map[string]FileKind{
		"internal/payment/service.go":      FileKindSource,
		"internal/payment/service_test.go": FileKindTest,
		"migrations/0042_retry.sql":        FileKindMigration,
		"deploy/values.yaml":               FileKindConfig,
		"go.mod":                           FileKindDependency,
		"README.md":                        FileKindDocumentation,
		"api/payment.pb.go":                FileKindGenerated,
	}
	wantImportance := map[string]Importance{
		"internal/payment/service.go":      ImportanceHigh,
		"internal/payment/service_test.go": ImportanceHigh,
		"migrations/0042_retry.sql":        ImportanceHigh,
		"deploy/values.yaml":               ImportanceMedium,
		"go.mod":                           ImportanceMedium,
		"README.md":                        ImportanceLow,
		"api/payment.pb.go":                ImportanceLow,
	}

	for _, f := range got.Files {
		if f.Kind != wantKinds[f.Path] {
			t.Errorf("%s kind = %q, want %q", f.Path, f.Kind, wantKinds[f.Path])
		}
		if f.Importance != wantImportance[f.Path] {
			t.Errorf("%s importance = %q, want %q", f.Path, f.Importance, wantImportance[f.Path])
		}
		if f.Reason == "" {
			t.Errorf("%s has no selection reason", f.Path)
		}
	}
}

// TestSelectOrderIsStable checks equal-priority files keep their input order and
// that repeated runs agree.
func TestSelectOrderIsStable(t *testing.T) {
	rc := reviewContextWith(
		file("internal/c.go", "c"),
		file("internal/a.go", "a"),
		file("internal/b.go", "b"),
		file("docs/z.md", "z"),
		file("docs/y.md", "y"),
	)

	first := selectOrFail(t, NewSelector(), rc, analysis.Result{})

	// Input order preserved within a kind: not sorted alphabetically.
	want := []string{"internal/c.go", "internal/a.go", "internal/b.go", "docs/z.md", "docs/y.md"}
	if strings.Join(paths(first.Files), ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", paths(first.Files), want)
	}

	for i := 0; i < 10; i++ {
		got := selectOrFail(t, NewSelector(), rc, analysis.Result{})
		if strings.Join(paths(got.Files), ",") != strings.Join(paths(first.Files), ",") {
			t.Fatalf("run %d gave a different order: %v", i, paths(got.Files))
		}
	}
}

func TestSelectRelatedTests(t *testing.T) {
	tests := []struct {
		name       string
		files      []review.FileChange
		testPath   string
		wantReason string
	}{
		{
			name: "test paired with a changed source file",
			files: []review.FileChange{
				file("internal/payment/service.go", "source"),
				file("internal/payment/service_test.go", "test"),
			},
			testPath:   "internal/payment/service_test.go",
			wantReason: "test related to changed source file",
		},
		{
			name: "root level pairing",
			files: []review.FileChange{
				file("foo.go", "source"),
				file("foo_test.go", "test"),
			},
			testPath:   "foo_test.go",
			wantReason: "test related to changed source file",
		},
		{
			name: "test without its source file changed",
			files: []review.FileChange{
				file("internal/payment/other.go", "source"),
				file("internal/payment/service_test.go", "test"),
			},
			testPath:   "internal/payment/service_test.go",
			wantReason: "changed test file",
		},
		{
			name: "test alone in the change",
			files: []review.FileChange{
				file("internal/payment/service_test.go", "test"),
			},
			testPath:   "internal/payment/service_test.go",
			wantReason: "changed test file",
		},
		{
			name: "same base name in a different directory is not related",
			files: []review.FileChange{
				file("internal/a/service.go", "source"),
				file("internal/b/service_test.go", "test"),
			},
			testPath:   "internal/b/service_test.go",
			wantReason: "changed test file",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := selectOrFail(t, NewSelector(), reviewContextWith(tt.files...), analysis.Result{})

			for _, f := range got.Files {
				if f.Path != tt.testPath {
					continue
				}
				if f.Reason != tt.wantReason {
					t.Errorf("%s reason = %q, want %q", f.Path, f.Reason, tt.wantReason)
				}
				// Related or not, a changed test stays high priority.
				if f.Importance != ImportanceHigh {
					t.Errorf("%s importance = %q, want high", f.Path, f.Importance)
				}
				return
			}
			t.Fatalf("%s was not selected", tt.testPath)
		})
	}
}

func TestSelectTechnologyProfile(t *testing.T) {
	tests := []struct {
		name             string
		files            []review.FileChange
		wantLanguages    []string
		wantTechnologies []string
	}{
		{
			name:          "go detected from a source file",
			files:         []review.FileChange{file("internal/x.go", "package x")},
			wantLanguages: []string{"go"},
		},
		{
			name:          "go detected from go.mod",
			files:         []review.FileChange{file("go.mod", "module example.com/x")},
			wantLanguages: []string{"go"},
		},
		{
			name:          "go detected from go.sum",
			files:         []review.FileChange{file("go.sum", "h1:abc")},
			wantLanguages: []string{"go"},
		},
		{
			name:             "database/sql",
			files:            []review.FileChange{file("internal/store.go", "+import \"database/sql\"")},
			wantLanguages:    []string{"go"},
			wantTechnologies: []string{"sql"},
		},
		{
			name:             "gorm",
			files:            []review.FileChange{file("internal/store.go", "+import \"gorm.io/gorm\"")},
			wantLanguages:    []string{"go"},
			wantTechnologies: []string{"gorm"},
		},
		{
			name:             "grpc",
			files:            []review.FileChange{file("internal/server.go", "+import \"google.golang.org/grpc\"")},
			wantLanguages:    []string{"go"},
			wantTechnologies: []string{"grpc"},
		},
		{
			name:             "gin",
			files:            []review.FileChange{file("internal/api.go", "+import \"github.com/gin-gonic/gin\"")},
			wantLanguages:    []string{"go"},
			wantTechnologies: []string{"gin"},
		},
		{
			name:             "chi",
			files:            []review.FileChange{file("internal/api.go", "+import \"github.com/go-chi/chi/v5\"")},
			wantLanguages:    []string{"go"},
			wantTechnologies: []string{"chi"},
		},
		{
			name:             "gorilla mux",
			files:            []review.FileChange{file("internal/api.go", "+import \"github.com/gorilla/mux\"")},
			wantLanguages:    []string{"go"},
			wantTechnologies: []string{"http-router"},
		},
		{
			name:             "opentelemetry",
			files:            []review.FileChange{file("internal/trace.go", "+import \"go.opentelemetry.io/otel\"")},
			wantLanguages:    []string{"go"},
			wantTechnologies: []string{"opentelemetry"},
		},
		{
			name:             "kubernetes via k8s.io",
			files:            []review.FileChange{file("internal/k8s.go", "+import \"k8s.io/client-go/kubernetes\"")},
			wantLanguages:    []string{"go"},
			wantTechnologies: []string{"kubernetes"},
		},
		{
			name:             "kubernetes via kubernetes.io",
			files:            []review.FileChange{file("deploy/app.yaml", "+  app.kubernetes.io/name: payments")},
			wantTechnologies: []string{"kubernetes"},
		},
		{
			name: "detected from a go.mod requirement",
			files: []review.FileChange{file("go.mod",
				"+\tgoogle.golang.org/grpc v1.60.0\n+\tgorm.io/gorm v1.25.0")},
			wantLanguages:    []string{"go"},
			wantTechnologies: []string{"gorm", "grpc"},
		},
		{
			name: "several technologies are sorted",
			files: []review.FileChange{file("internal/server.go",
				"+import (\n+\t\"database/sql\"\n+\t\"google.golang.org/grpc\"\n+\t\"go.opentelemetry.io/otel\"\n+)")},
			wantLanguages:    []string{"go"},
			wantTechnologies: []string{"grpc", "opentelemetry", "sql"},
		},
		{
			name:  "nothing detected",
			files: []review.FileChange{file("notes.txt", "hello")},
		},
		{
			name: "no files at all",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := selectOrFail(t, NewSelector(), reviewContextWith(tt.files...), analysis.Result{})

			if strings.Join(got.Profile.Languages, ",") != strings.Join(tt.wantLanguages, ",") {
				t.Errorf("Languages = %v, want %v", got.Profile.Languages, tt.wantLanguages)
			}
			if strings.Join(got.Profile.Technologies(), ",") != strings.Join(tt.wantTechnologies, ",") {
				t.Errorf("Technologies = %v, want %v", got.Profile.Technologies(), tt.wantTechnologies)
			}
			if got.Profile.Empty() != (len(tt.wantLanguages) == 0 && len(tt.wantTechnologies) == 0) {
				t.Errorf("Empty() = %t, unexpected for %+v", got.Profile.Empty(), got.Profile)
			}
			for _, lang := range tt.wantLanguages {
				if !got.Profile.HasLanguage(lang) {
					t.Errorf("HasLanguage(%q) = false", lang)
				}
			}
			for _, tech := range tt.wantTechnologies {
				if !got.Profile.HasTechnology(tech) {
					t.Errorf("HasTechnology(%q) = false", tech)
				}
			}
		})
	}
}

// TestSelectProfileIsDeterministic guards against map-iteration order leaking out.
func TestSelectProfileIsDeterministic(t *testing.T) {
	rc := reviewContextWith(file("internal/server.go",
		"+import (\n+\t\"database/sql\"\n+\t\"google.golang.org/grpc\"\n+\t\"gorm.io/gorm\"\n+\t\"go.opentelemetry.io/otel\"\n+)"))

	first := selectOrFail(t, NewSelector(), rc, analysis.Result{}).Profile

	for i := 0; i < 20; i++ {
		got := selectOrFail(t, NewSelector(), rc, analysis.Result{}).Profile
		if strings.Join(got.Technologies(), ",") != strings.Join(first.Technologies(), ",") {
			t.Fatalf("run %d gave %v, first gave %v", i, got.Technologies(), first.Technologies())
		}
	}
}

func TestSelectBudgetEnforced(t *testing.T) {
	tests := []struct {
		name  string
		total int
		files []review.FileChange
	}{
		{
			name:  "everything fits",
			total: 100 * 1024,
			files: []review.FileChange{file("a.go", patchOf(1024)), file("b.go", patchOf(1024))},
		},
		{
			name:  "a tight budget",
			total: 4 * 1024,
			files: []review.FileChange{file("a.go", patchOf(3000)), file("b.go", patchOf(3000)), file("c.go", patchOf(3000))},
		},
		{
			name:  "one file far larger than the budget",
			total: 4 * 1024,
			files: []review.FileChange{file("a.go", patchOf(200*1024))},
		},
		{
			name:  "many files",
			total: 20 * 1024,
			files: func() []review.FileChange {
				var files []review.FileChange
				for i := 0; i < 50; i++ {
					files = append(files, file(fmt.Sprintf("internal/f%02d.go", i), patchOf(2048)))
				}
				return files
			}(),
		},
		{
			name:  "the default budget with a large change",
			total: 0,
			files: func() []review.FileChange {
				var files []review.FileChange
				for i := 0; i < 40; i++ {
					files = append(files, file(fmt.Sprintf("internal/f%02d.go", i), patchOf(20*1024)))
				}
				return files
			}(),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			selector := NewSelectorWithBudget(Budget{Total: tt.total})
			budget := selector.Budget()

			got := selectOrFail(t, selector, reviewContextWith(tt.files...), analysis.Result{})

			// The headline guarantee: the selection never exceeds its budget.
			if got.Stats.SelectedBytes > budget.Total {
				t.Errorf("SelectedBytes = %d, want at most the budget %d", got.Stats.SelectedBytes, budget.Total)
			}
			if got.Stats.BudgetBytes != budget.Total {
				t.Errorf("BudgetBytes = %d, want %d", got.Stats.BudgetBytes, budget.Total)
			}

			// The recorded total must match what was actually kept.
			var actual int
			for _, f := range got.Files {
				actual += len(f.Patch)
			}
			for _, r := range got.Rules {
				actual += len(r.Content)
			}
			for _, a := range got.Analysis {
				actual += len(a.Output)
			}
			if got.Ticket != nil {
				actual += len(got.Ticket.Description)
			}
			if actual != got.Stats.SelectedBytes {
				t.Errorf("SelectedBytes = %d but the content measures %d", got.Stats.SelectedBytes, actual)
			}

			if got.Stats.CandidateFiles != len(tt.files) {
				t.Errorf("CandidateFiles = %d, want %d", got.Stats.CandidateFiles, len(tt.files))
			}
			if got.Stats.SelectedFiles+got.Stats.DroppedFiles != got.Stats.CandidateFiles {
				t.Errorf("%d selected + %d dropped != %d candidates",
					got.Stats.SelectedFiles, got.Stats.DroppedFiles, got.Stats.CandidateFiles)
			}
		})
	}
}

// TestSelectDropsLowPriorityFirst is the core budget-pressure guarantee.
func TestSelectDropsLowPriorityFirst(t *testing.T) {
	// Each patch is 2 KB; the budget fits roughly two of them.
	rc := reviewContextWith(
		file("README.md", patchOf(2048)),
		file("api/payment.pb.go", patchOf(2048)),
		file("internal/payment/service.go", patchOf(2048)),
		file("internal/payment/service_test.go", patchOf(2048)),
	)

	got := selectOrFail(t, NewSelectorWithBudget(Budget{Total: 5 * 1024}), rc, analysis.Result{})

	kept := map[string]bool{}
	for _, f := range got.Files {
		if f.Patch != "" {
			kept[f.Path] = true
		}
	}

	for _, path := range []string{"internal/payment/service.go", "internal/payment/service_test.go"} {
		if !kept[path] {
			t.Errorf("%s was not kept; high-priority content must survive", path)
		}
	}

	dropped := map[string]bool{}
	for _, d := range got.Stats.Dropped {
		dropped[d.Path] = true
	}
	if !dropped["api/payment.pb.go"] && !dropped["README.md"] {
		t.Errorf("no low-priority file was dropped; dropped = %v", got.Stats.Dropped)
	}
	for _, d := range got.Stats.Dropped {
		if d.Importance == ImportanceHigh {
			t.Errorf("dropped a high-importance file %s while low-priority content remained", d.Path)
		}
		if d.Reason == "" {
			t.Errorf("dropped file %s has no reason", d.Path)
		}
		if d.Bytes == 0 {
			t.Errorf("dropped file %s records no size", d.Path)
		}
	}
}

func TestSelectPatchTruncation(t *testing.T) {
	big := patchOf(50 * 1024)
	rc := reviewContextWith(file("internal/payment/service.go", big))

	got := selectOrFail(t, NewSelectorWithBudget(Budget{Total: 8 * 1024}), rc, analysis.Result{})

	if len(got.Files) != 1 {
		t.Fatalf("got %d files, want 1", len(got.Files))
	}

	f := got.Files[0]
	if !f.Truncated {
		t.Error("Truncated = false for a patch larger than the budget")
	}
	if !strings.Contains(f.Patch, MarkerPatch) {
		t.Errorf("patch lacks the marker %q", MarkerPatch)
	}
	if f.OriginalBytes != len(big) {
		t.Errorf("OriginalBytes = %d, want %d", f.OriginalBytes, len(big))
	}
	if len(f.Patch) >= len(big) {
		t.Errorf("patch is %d bytes, want it reduced from %d", len(f.Patch), len(big))
	}
	// Path and status survive truncation.
	if f.Path != "internal/payment/service.go" || f.Status != "modified" {
		t.Errorf("file identity lost: %+v", f)
	}
	if !got.Stats.Truncated {
		t.Error("Stats.Truncated = false although a patch was cut")
	}
}

func TestSelectFileWithoutPatch(t *testing.T) {
	rc := reviewContextWith(
		review.FileChange{Filename: "assets/logo.png", Status: "added"},
		file("internal/x.go", "patch"),
	)

	got := selectOrFail(t, NewSelector(), rc, analysis.Result{})

	if len(got.Files) != 2 {
		t.Fatalf("got %d files, want 2", len(got.Files))
	}
	for _, f := range got.Files {
		if f.Path != "assets/logo.png" {
			continue
		}
		if f.Patch != "" || f.Truncated || f.OriginalBytes != 0 {
			t.Errorf("patchless file = %+v, want empty patch and no truncation", f)
		}
		return
	}
	t.Error("the patchless file was dropped; it is still a fact about the change")
}

func TestSelectTicket(t *testing.T) {
	tests := []struct {
		name          string
		ticket        *review.TicketContext
		budget        int
		wantNil       bool
		wantTruncated bool
	}{
		{name: "no ticket", ticket: nil, wantNil: true},
		{
			name: "small description",
			ticket: &review.TicketContext{
				Key: "PAY-431", Summary: "Retry failed card authorizations",
				Description: "Retry failed payments", Status: "In Progress",
				IssueType: "Story", Priority: "High", ParentKey: "PAY-400",
				Labels: []string{"payments", "reliability"},
			},
		},
		{
			name: "huge description is truncated",
			ticket: &review.TicketContext{
				Key: "PAY-431", Summary: "Retry failed card authorizations",
				Description: strings.Repeat("acceptance criteria\n", 5000),
			},
			budget:        1024,
			wantTruncated: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			rc := reviewContextWith(file("a.go", "patch"))
			rc.Ticket = tt.ticket

			selector := NewSelectorWithBudget(Budget{TicketDescription: tt.budget})
			got := selectOrFail(t, selector, rc, analysis.Result{})

			if tt.wantNil {
				if got.Ticket != nil {
					t.Fatalf("Ticket = %+v, want nil", got.Ticket)
				}
				if got.HasTicket() {
					t.Error("HasTicket() = true without a ticket")
				}
				return
			}

			if got.Ticket == nil {
				t.Fatal("Ticket = nil, want a summary")
			}

			// The key and summary are never subject to the budget.
			if got.Ticket.Key != tt.ticket.Key {
				t.Errorf("Key = %q, want %q", got.Ticket.Key, tt.ticket.Key)
			}
			if got.Ticket.Summary != tt.ticket.Summary {
				t.Errorf("Summary = %q, want %q", got.Ticket.Summary, tt.ticket.Summary)
			}

			if got.Ticket.DescriptionTruncated != tt.wantTruncated {
				t.Errorf("DescriptionTruncated = %t, want %t", got.Ticket.DescriptionTruncated, tt.wantTruncated)
			}
			if tt.wantTruncated {
				if !strings.Contains(got.Ticket.Description, MarkerTicketDescription) {
					t.Errorf("description lacks the marker %q", MarkerTicketDescription)
				}
				if len(got.Ticket.Description) > tt.budget+len(MarkerTicketDescription)+8 {
					t.Errorf("description is %d bytes, want about %d", len(got.Ticket.Description), tt.budget)
				}
			} else if got.Ticket.Description != tt.ticket.Description {
				t.Errorf("description was altered: %q", got.Ticket.Description)
			}
		})
	}
}

// TestSelectTicketSummaryNeverDropped keeps the key and summary under the most
// extreme budget pressure.
func TestSelectTicketSummaryNeverDropped(t *testing.T) {
	rc := reviewContextWith(file("a.go", patchOf(100*1024)))
	rc.Ticket = &review.TicketContext{
		Key: "PAY-431", Summary: "Retry failed card authorizations",
		Description: strings.Repeat("x", 100*1024), Status: "In Progress",
	}

	got := selectOrFail(t, NewSelectorWithBudget(Budget{Total: 1024, TicketDescription: 64}), rc, analysis.Result{})

	if got.Ticket == nil {
		t.Fatal("Ticket = nil under budget pressure; the key and summary must survive")
	}
	if got.Ticket.Key != "PAY-431" {
		t.Errorf("Key = %q, want PAY-431", got.Ticket.Key)
	}
	if got.Ticket.Summary != "Retry failed card authorizations" {
		t.Errorf("Summary = %q, want it intact", got.Ticket.Summary)
	}
	if got.Ticket.Status != "In Progress" {
		t.Errorf("Status = %q, want it intact", got.Ticket.Status)
	}
}

// TestSelectTicketLabelsAreCopied keeps the selection a snapshot.
func TestSelectTicketLabelsAreCopied(t *testing.T) {
	labels := []string{"payments", "reliability"}
	rc := reviewContextWith(file("a.go", "patch"))
	rc.Ticket = &review.TicketContext{Key: "PAY-431", Summary: "S", Labels: labels}

	got := selectOrFail(t, NewSelector(), rc, analysis.Result{})

	got.Ticket.Labels[0] = "MUTATED"
	if labels[0] != "payments" {
		t.Error("the selection shares its label slice with the review context")
	}
}

func TestSelectRules(t *testing.T) {
	documents := []review.RuleDocument{
		{Path: ".ai-review/rules.md", Content: "review rules"},
		{Path: "AGENTS.md", Content: "agent rules"},
		{Path: "CONTRIBUTING.md", Content: "contributing rules"},
	}

	rc := reviewContextWith(file("a.go", "patch"))
	rc.Rules = review.RuleContext{Documents: documents}

	got := selectOrFail(t, NewSelector(), rc, analysis.Result{})

	// Stable order, matching the loader's priority.
	want := []string{".ai-review/rules.md", "AGENTS.md", "CONTRIBUTING.md"}
	var gotPaths []string
	for _, r := range got.Rules {
		gotPaths = append(gotPaths, r.Path)
	}
	if strings.Join(gotPaths, ",") != strings.Join(want, ",") {
		t.Errorf("rule order = %v, want %v", gotPaths, want)
	}

	for i, r := range got.Rules {
		if r.Content != documents[i].Content {
			t.Errorf("%s content = %q, want %q", r.Path, r.Content, documents[i].Content)
		}
		if r.Truncated {
			t.Errorf("%s was truncated unexpectedly", r.Path)
		}
		if r.OriginalBytes != len(documents[i].Content) {
			t.Errorf("%s OriginalBytes = %d, want %d", r.Path, r.OriginalBytes, len(documents[i].Content))
		}
	}
}

func TestSelectRulesBudget(t *testing.T) {
	rc := reviewContextWith(file("a.go", "patch"))
	rc.Rules = review.RuleContext{Documents: []review.RuleDocument{
		{Path: ".ai-review/rules.md", Content: strings.Repeat("r", 8*1024)},
		{Path: "AGENTS.md", Content: strings.Repeat("a", 8*1024)},
		{Path: "CONTRIBUTING.md", Content: strings.Repeat("c", 8*1024)},
	}}

	const rulesBudget = 10 * 1024
	got := selectOrFail(t, NewSelectorWithBudget(Budget{Rules: rulesBudget}), rc, analysis.Result{})

	var total int
	for _, r := range got.Rules {
		total += len(r.Content)
	}
	if total > rulesBudget+3*(len(MarkerRule)+8) {
		t.Errorf("rules total %d bytes, want about the %d budget", total, rulesBudget)
	}

	// Every document is still listed, even when its content had to go.
	if len(got.Rules) != 3 {
		t.Errorf("got %d rules, want all 3 listed", len(got.Rules))
	}
	// The highest-priority document keeps its content in full.
	if got.Rules[0].Truncated {
		t.Error(".ai-review/rules.md was truncated before the lower-priority documents")
	}
	// Something further down had to be cut.
	if !got.Rules[2].Truncated {
		t.Error("CONTRIBUTING.md was not truncated although the budget was exceeded")
	}
	if !strings.Contains(got.Rules[2].Content, MarkerRule) {
		t.Errorf("truncated rule lacks the marker: %q", got.Rules[2].Content)
	}
	if !got.Stats.Truncated {
		t.Error("Stats.Truncated = false although a rule was cut")
	}
}

func TestSelectNoRules(t *testing.T) {
	got := selectOrFail(t, NewSelector(), reviewContextWith(file("a.go", "patch")), analysis.Result{})

	if got.Rules != nil {
		t.Errorf("Rules = %v, want nil", got.Rules)
	}
}

func TestSelectAnalysis(t *testing.T) {
	result := analysis.Result{Checks: []analysis.CheckResult{
		{
			Name: "go-test", Command: "go test ./...", ExitCode: 1,
			Stdout: "--- FAIL: TestRetryDeclined (0.00s)\n    expected 0 retries, got 3\nFAIL\n",
			Stderr: "exit status 1\n",
		},
		{
			Name: "go-vet", Command: "go vet ./...", Passed: true,
			Stdout: strings.Repeat("noise about nothing\n", 5000),
		},
	}}

	got := selectOrFail(t, NewSelector(), reviewContextWith(file("a.go", "patch")), result)

	if len(got.Analysis) != 2 {
		t.Fatalf("got %d checks, want 2", len(got.Analysis))
	}

	failed := got.Analysis[0]
	if failed.Passed {
		t.Error("the failing check reports Passed")
	}
	if failed.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", failed.ExitCode)
	}
	if !strings.Contains(failed.Output, "TestRetryDeclined") {
		t.Errorf("failing check output lost its detail: %q", failed.Output)
	}
	if !strings.Contains(failed.Output, "expected 0 retries, got 3") {
		t.Errorf("failing check output lost the assertion: %q", failed.Output)
	}

	// A passing check contributes its status only, never its output.
	passed := got.Analysis[1]
	if !passed.Passed {
		t.Error("the passing check does not report Passed")
	}
	if passed.Output != "" {
		t.Errorf("passing check carries %d bytes of output, want none", len(passed.Output))
	}

	if len(got.FailedAnalysis()) != 1 {
		t.Errorf("FailedAnalysis() returned %d, want 1", len(got.FailedAnalysis()))
	}
}

func TestSelectAnalysisTruncation(t *testing.T) {
	result := analysis.Result{Checks: []analysis.CheckResult{{
		Name: "go-test", Command: "go test ./...", ExitCode: 1,
		Stdout: strings.Repeat("--- FAIL: TestSomething\n", 5000),
	}}}

	const perCheck = 2048
	got := selectOrFail(t,
		NewSelectorWithBudget(Budget{Analysis: 4096, PerCheckOutput: perCheck}),
		reviewContextWith(file("a.go", "patch")), result)

	check := got.Analysis[0]
	if !check.Truncated {
		t.Error("Truncated = false for oversized check output")
	}
	if len(check.Output) > perCheck+len(MarkerAnalysis)+40 {
		t.Errorf("output is %d bytes, want about %d", len(check.Output), perCheck)
	}
	if check.OriginalBytes == 0 {
		t.Error("OriginalBytes = 0, want the pre-truncation size")
	}
	// The head of the output, where the diagnosis is, must survive.
	if !strings.Contains(check.Output, "--- FAIL: TestSomething") {
		t.Errorf("output lost the failure line: %q", check.Output[:min(200, len(check.Output))])
	}
	if !got.Stats.Truncated {
		t.Error("Stats.Truncated = false although check output was cut")
	}
}

func TestSelectAnalysisSkippedAndTimedOut(t *testing.T) {
	result := analysis.Result{Checks: []analysis.CheckResult{
		{Name: "go-test", Command: "go test ./...", Skipped: true, SkipReason: "go.mod not found", ExitCode: -1},
		{Name: "go-vet", Command: "go vet ./...", TimedOut: true, ExitCode: -1, Stdout: "partial output\n"},
	}}

	got := selectOrFail(t, NewSelector(), reviewContextWith(file("a.go", "patch")), result)

	if !got.Analysis[0].Skipped {
		t.Error("the skipped check does not report Skipped")
	}
	if got.Analysis[0].Output != "" {
		t.Errorf("skipped check carries output: %q", got.Analysis[0].Output)
	}
	if !got.Analysis[1].TimedOut {
		t.Error("the timed-out check does not report TimedOut")
	}
	// A timeout is a failure, so whatever output exists is evidence.
	if !strings.Contains(got.Analysis[1].Output, "partial output") {
		t.Errorf("timed-out check lost its output: %q", got.Analysis[1].Output)
	}
}

func TestSelectAnalysisUsesStderrWhenStdoutIsEmpty(t *testing.T) {
	result := analysis.Result{Checks: []analysis.CheckResult{{
		Name: "go-vet", Command: "go vet ./...", ExitCode: 1,
		Stderr: "vet: internal/x/y.go:3:2: undefined: z\n",
	}}}

	got := selectOrFail(t, NewSelector(), reviewContextWith(file("a.go", "patch")), result)

	if !strings.Contains(got.Analysis[0].Output, "undefined: z") {
		t.Errorf("output = %q, want the stderr detail", got.Analysis[0].Output)
	}
}

func TestSelectNoAnalysis(t *testing.T) {
	got := selectOrFail(t, NewSelector(), reviewContextWith(file("a.go", "patch")), analysis.Result{})

	if got.Analysis != nil {
		t.Errorf("Analysis = %v, want nil", got.Analysis)
	}
	if len(got.FailedAnalysis()) != 0 {
		t.Errorf("FailedAnalysis() returned %d, want 0", len(got.FailedAnalysis()))
	}
}

func TestSelectEdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		files     []review.FileChange
		wantFiles int
		check     func(t *testing.T, got SelectedContext)
	}{
		{
			name:      "no changed files",
			files:     nil,
			wantFiles: 0,
			check: func(t *testing.T, got SelectedContext) {
				if got.Stats.CandidateFiles != 0 || got.Stats.SelectedFiles != 0 || got.Stats.DroppedFiles != 0 {
					t.Errorf("stats = %+v, want all zero", got.Stats)
				}
				if got.Stats.Truncated {
					t.Error("Truncated = true for an empty change")
				}
				// The pull request metadata still comes through.
				if got.PullRequest.Number != 123 {
					t.Error("pull request metadata lost for an empty change")
				}
			},
		},
		{
			name: "documentation-only pull request",
			files: []review.FileChange{
				file("README.md", "docs patch"),
				file("docs/guide.md", "guide patch"),
			},
			wantFiles: 2,
			check: func(t *testing.T, got SelectedContext) {
				// Documentation is never discarded outright.
				for _, f := range got.Files {
					if f.Patch == "" {
						t.Errorf("%s lost its patch in a documentation-only change", f.Path)
					}
					if f.Importance != ImportanceLow {
						t.Errorf("%s importance = %q, want low", f.Path, f.Importance)
					}
				}
			},
		},
		{
			name: "generated-files-only pull request",
			files: []review.FileChange{
				file("api/payment.pb.go", "generated patch"),
				file("internal/mock/store_generated.go", "mock patch"),
			},
			wantFiles: 2,
			check: func(t *testing.T, got SelectedContext) {
				for _, f := range got.Files {
					if f.Kind != FileKindGenerated {
						t.Errorf("%s kind = %q, want generated", f.Path, f.Kind)
					}
					// The only content there is, so it must be kept.
					if f.Patch == "" {
						t.Errorf("%s lost its patch in a generated-only change", f.Path)
					}
				}
			},
		},
		{
			name:      "single unknown file",
			files:     []review.FileChange{file("data/export.parquet", "binary-ish patch")},
			wantFiles: 1,
			check: func(t *testing.T, got SelectedContext) {
				if got.Files[0].Importance != ImportanceMedium {
					t.Errorf("importance = %q, want medium", got.Files[0].Importance)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := selectOrFail(t, NewSelector(), reviewContextWith(tt.files...), analysis.Result{})

			if len(got.Files) != tt.wantFiles {
				t.Fatalf("got %d files, want %d", len(got.Files), tt.wantFiles)
			}
			tt.check(t, got)
		})
	}
}

func TestSelectStats(t *testing.T) {
	patchA, patchB, patchC := patchOf(600), patchOf(600), patchOf(600)
	rules := []review.RuleDocument{{Path: "AGENTS.md", Content: strings.Repeat("r", 400)}}
	description := strings.Repeat("d", 300)

	rc := reviewContextWith(
		file("internal/a.go", patchA),
		file("internal/b.go", patchB),
		file("README.md", patchC),
	)
	rc.Rules = review.RuleContext{Documents: rules}
	rc.Ticket = &review.TicketContext{Key: "PAY-431", Summary: "S", Description: description}

	result := analysis.Result{Checks: []analysis.CheckResult{
		{Name: "go-test", Command: "go test ./...", ExitCode: 1, Stdout: strings.Repeat("f", 500)},
	}}

	got := selectOrFail(t, NewSelector(), rc, result)

	wantOriginal := len(patchA) + len(patchB) + len(patchC) + 400 + 300 + 500
	if got.Stats.OriginalBytes != wantOriginal {
		t.Errorf("OriginalBytes = %d, want %d", got.Stats.OriginalBytes, wantOriginal)
	}
	// Everything fits in the default budget, so nothing was cut.
	if got.Stats.SelectedBytes != wantOriginal {
		t.Errorf("SelectedBytes = %d, want %d", got.Stats.SelectedBytes, wantOriginal)
	}
	if got.Stats.CandidateFiles != 3 {
		t.Errorf("CandidateFiles = %d, want 3", got.Stats.CandidateFiles)
	}
	if got.Stats.SelectedFiles != 3 {
		t.Errorf("SelectedFiles = %d, want 3", got.Stats.SelectedFiles)
	}
	if got.Stats.DroppedFiles != 0 {
		t.Errorf("DroppedFiles = %d, want 0", got.Stats.DroppedFiles)
	}
	if got.Stats.Truncated {
		t.Error("Truncated = true although everything fit")
	}
	if got.Stats.BudgetBytes != DefaultContextBudgetBytes {
		t.Errorf("BudgetBytes = %d, want %d", got.Stats.BudgetBytes, DefaultContextBudgetBytes)
	}
}

func TestSelectFilesByImportance(t *testing.T) {
	rc := reviewContextWith(
		file("internal/a.go", "source"),
		file("go.mod", "dependency"),
		file("README.md", "docs"),
	)

	got := selectOrFail(t, NewSelector(), rc, analysis.Result{})

	tests := []struct {
		importance Importance
		want       int
	}{
		{importance: ImportanceHigh, want: 1},
		{importance: ImportanceMedium, want: 1},
		{importance: ImportanceLow, want: 1},
	}

	for _, tt := range tests {
		if got := len(got.FilesByImportance(tt.importance)); got != tt.want {
			t.Errorf("FilesByImportance(%q) returned %d, want %d", tt.importance, got, tt.want)
		}
	}
}

func TestSelectContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewSelector().Select(ctx, reviewContextWith(file("a.go", "patch")), analysis.Result{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false; err = %v", err)
	}
}

// TestSelectIsRepeatable runs a full selection many times and compares everything
// that matters.
func TestSelectIsRepeatable(t *testing.T) {
	rc := reviewContextWith(
		file("internal/payment/service.go", patchOf(3000)),
		file("internal/payment/service_test.go", patchOf(3000)),
		file("migrations/0042.sql", patchOf(1000)),
		file("README.md", patchOf(3000)),
		file("api/x.pb.go", patchOf(3000)),
	)
	rc.Rules = review.RuleContext{Documents: []review.RuleDocument{{Path: "AGENTS.md", Content: "rules"}}}
	rc.Ticket = &review.TicketContext{Key: "PAY-431", Summary: "S", Description: strings.Repeat("d", 100)}

	result := analysis.Result{Checks: []analysis.CheckResult{
		{Name: "go-test", Command: "go test ./...", ExitCode: 1, Stdout: strings.Repeat("fail\n", 100)},
	}}

	selector := NewSelectorWithBudget(Budget{Total: 8 * 1024})
	first := selectOrFail(t, selector, rc, result)

	for i := 0; i < 10; i++ {
		got := selectOrFail(t, selector, rc, result)

		if strings.Join(paths(got.Files), ",") != strings.Join(paths(first.Files), ",") {
			t.Fatalf("run %d selected different files: %v", i, paths(got.Files))
		}
		if got.Stats.SelectedBytes != first.Stats.SelectedBytes ||
			got.Stats.OriginalBytes != first.Stats.OriginalBytes ||
			got.Stats.SelectedFiles != first.Stats.SelectedFiles ||
			got.Stats.DroppedFiles != first.Stats.DroppedFiles {
			t.Fatalf("run %d produced different stats: %+v", i, got.Stats)
		}
		for j := range got.Files {
			if got.Files[j].Patch != first.Files[j].Patch {
				t.Fatalf("run %d produced a different patch for %s", i, got.Files[j].Path)
			}
		}
	}
}

// TestSelectCarriesNoCredentials guards the boundary: the selection is built only
// from normalized application data.
func TestSelectCarriesNoCredentials(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_secret_value")
	t.Setenv("JIRA_TOKEN", "jira_secret_value")
	t.Setenv("JIRA_EMAIL", "developer@company.com")

	rc := reviewContextWith(file("internal/a.go", "patch"))
	rc.Ticket = &review.TicketContext{Key: "PAY-431", Summary: "S"}
	rc.Rules = review.RuleContext{Documents: []review.RuleDocument{{Path: "AGENTS.md", Content: "rules"}}}

	got := selectOrFail(t, NewSelector(), rc, analysis.Result{})

	rendered := fmt.Sprintf("%+v", got)
	for _, secret := range []string{"ghp_secret_value", "jira_secret_value", "developer@company.com"} {
		if strings.Contains(rendered, secret) {
			t.Errorf("the selection contains the secret %q", secret)
		}
	}
}

func TestSelectorBudgetAccessor(t *testing.T) {
	if got := NewSelector().Budget(); got != DefaultBudget() {
		t.Errorf("Budget() = %+v, want the defaults", got)
	}
	if got := NewSelectorWithBudget(Budget{Total: 4096}).Budget(); got.Total != 4096 {
		t.Errorf("Budget().Total = %d, want 4096", got.Total)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
