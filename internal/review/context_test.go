package review

import (
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/github"
	"github.com/your-company/agentic-code-review/internal/jira"
	"github.com/your-company/agentic-code-review/internal/reporules"
)

// Fixtures shared across the tests.
var (
	testPR = github.PullRequest{Owner: "acme", Repo: "payments", Number: 123}

	testDetails = github.PullRequestDetails{
		Number:      123,
		Title:       "Add payment retry",
		Body:        "Improve failed payment retry handling.",
		State:       "open",
		Draft:       false,
		HTMLURL:     "https://github.com/acme/payments/pull/123",
		BaseBranch:  "main",
		HeadBranch:  "feature/PAY-431",
		HeadSHA:     "abc123",
		AuthorLogin: "alice",
	}

	testFiles = []github.ChangedFile{
		{Filename: "a.go", Status: "modified", Additions: 20, Deletions: 5, Changes: 25, Patch: "@@ a @@"},
		{Filename: "b.go", Status: "added", Additions: 10, Deletions: 2, Changes: 12, Patch: "@@ b @@"},
		{Filename: "c.go", Status: "removed", Additions: 0, Deletions: 6, Changes: 6, Patch: "@@ c @@"},
	}

	testIssue = jira.Issue{
		Key:         "PAY-431",
		Summary:     "Retry failed card authorizations",
		Description: "Retry failed payments",
		Status:      "In Progress",
		IssueType:   "Story",
		Priority:    "High",
		Labels:      []string{"payments", "reliability"},
		ParentKey:   "PAY-400",
	}
)

func TestBuildContextPullRequestMetadata(t *testing.T) {
	ctx := BuildContext(testPR, testDetails, testFiles, nil, testRules)

	tests := []struct {
		field string
		got   string
		want  string
	}{
		{"Owner", ctx.PullRequest.Owner, "acme"},
		{"Repository", ctx.PullRequest.Repository, "payments"},
		{"URL", ctx.PullRequest.URL, "https://github.com/acme/payments/pull/123"},
		{"Title", ctx.PullRequest.Title, "Add payment retry"},
		{"Body", ctx.PullRequest.Body, "Improve failed payment retry handling."},
		{"Author", ctx.PullRequest.Author, "alice"},
		{"BaseBranch", ctx.PullRequest.BaseBranch, "main"},
		{"HeadBranch", ctx.PullRequest.HeadBranch, "feature/PAY-431"},
		{"HeadSHA", ctx.PullRequest.HeadSHA, "abc123"},
		{"State", ctx.PullRequest.State, "open"},
	}

	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("PullRequest.%s = %q, want %q", tt.field, tt.got, tt.want)
		}
	}

	if ctx.PullRequest.Number != 123 {
		t.Errorf("Number = %d, want 123", ctx.PullRequest.Number)
	}
}

// TestBuildContextNumberComesFromTheParsedURL checks the reference wins over the
// API payload, and that a zero reference falls back to the API value.
func TestBuildContextNumberComesFromTheParsedURL(t *testing.T) {
	t.Run("reference number is authoritative", func(t *testing.T) {
		details := testDetails
		details.Number = 999

		if got := BuildContext(testPR, details, nil, nil, reporules.Rules{}).PullRequest.Number; got != 123 {
			t.Errorf("Number = %d, want 123", got)
		}
	})

	t.Run("zero reference falls back to the details", func(t *testing.T) {
		pr := github.PullRequest{Owner: "acme", Repo: "payments"}

		if got := BuildContext(pr, testDetails, nil, nil, reporules.Rules{}).PullRequest.Number; got != 123 {
			t.Errorf("Number = %d, want 123", got)
		}
	})
}

func TestBuildContextDraft(t *testing.T) {
	tests := []struct {
		name  string
		draft bool
	}{
		{name: "draft pull request", draft: true},
		{name: "ready pull request", draft: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			details := testDetails
			details.Draft = tt.draft

			ctx := BuildContext(testPR, details, testFiles, nil, reporules.Rules{})

			if ctx.PullRequest.Draft != tt.draft {
				t.Errorf("Draft = %t, want %t", ctx.PullRequest.Draft, tt.draft)
			}
			if ctx.IsDraft() != tt.draft {
				t.Errorf("IsDraft() = %t, want %t", ctx.IsDraft(), tt.draft)
			}
		})
	}
}

func TestBuildContextChangedFiles(t *testing.T) {
	ctx := BuildContext(testPR, testDetails, testFiles, nil, reporules.Rules{})

	if len(ctx.Changes.Files) != 3 {
		t.Fatalf("got %d files, want 3", len(ctx.Changes.Files))
	}

	want := []FileChange{
		{Filename: "a.go", Status: "modified", Additions: 20, Deletions: 5, Changes: 25, Patch: "@@ a @@"},
		{Filename: "b.go", Status: "added", Additions: 10, Deletions: 2, Changes: 12, Patch: "@@ b @@"},
		{Filename: "c.go", Status: "removed", Additions: 0, Deletions: 6, Changes: 6, Patch: "@@ c @@"},
	}

	for i := range want {
		if ctx.Changes.Files[i] != want[i] {
			t.Errorf("file %d = %+v, want %+v", i, ctx.Changes.Files[i], want[i])
		}
	}
}

func TestBuildContextAggregates(t *testing.T) {
	tests := []struct {
		name  string
		files []github.ChangedFile
		want  ChangeContext
	}{
		{
			name:  "three files",
			files: testFiles,
			want:  ChangeContext{FileCount: 3, Additions: 30, Deletions: 13, Changes: 43},
		},
		{
			name:  "no files",
			files: nil,
			want:  ChangeContext{},
		},
		{
			name:  "empty slice",
			files: []github.ChangedFile{},
			want:  ChangeContext{},
		},
		{
			name:  "single file",
			files: []github.ChangedFile{{Filename: "a.go", Additions: 7, Deletions: 3, Changes: 10}},
			want:  ChangeContext{FileCount: 1, Additions: 7, Deletions: 3, Changes: 10},
		},
		{
			name: "additions only",
			files: []github.ChangedFile{
				{Filename: "a.go", Additions: 5, Changes: 5},
				{Filename: "b.go", Additions: 6, Changes: 6},
			},
			want: ChangeContext{FileCount: 2, Additions: 11, Changes: 11},
		},
		{
			name: "deletions only",
			files: []github.ChangedFile{
				{Filename: "a.go", Deletions: 5, Changes: 5},
				{Filename: "b.go", Deletions: 6, Changes: 6},
			},
			want: ChangeContext{FileCount: 2, Deletions: 11, Changes: 11},
		},
		{
			name: "patchless binary file still counts toward the file count",
			files: []github.ChangedFile{
				{Filename: "logo.png", Status: "added"},
				{Filename: "a.go", Additions: 2, Deletions: 1, Changes: 3},
			},
			want: ChangeContext{FileCount: 2, Additions: 2, Deletions: 1, Changes: 3},
		},
		{
			// Changes must be summed from the source values, never derived as
			// Additions+Deletions.
			name: "reported changes disagree with additions plus deletions",
			files: []github.ChangedFile{
				{Filename: "a.go", Additions: 10, Deletions: 5, Changes: 99},
			},
			want: ChangeContext{FileCount: 1, Additions: 10, Deletions: 5, Changes: 99},
		},
		{
			name: "reported changes are zero",
			files: []github.ChangedFile{
				{Filename: "a.go", Additions: 10, Deletions: 5, Changes: 0},
			},
			want: ChangeContext{FileCount: 1, Additions: 10, Deletions: 5, Changes: 0},
		},
		{
			name: "many files",
			files: func() []github.ChangedFile {
				files := make([]github.ChangedFile, 0, 50)
				for i := 0; i < 50; i++ {
					files = append(files, github.ChangedFile{Filename: "f.go", Additions: 2, Deletions: 1, Changes: 3})
				}
				return files
			}(),
			want: ChangeContext{FileCount: 50, Additions: 100, Deletions: 50, Changes: 150},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := BuildContext(testPR, testDetails, tt.files, nil, reporules.Rules{}).Changes

			if got.FileCount != tt.want.FileCount {
				t.Errorf("FileCount = %d, want %d", got.FileCount, tt.want.FileCount)
			}
			if got.Additions != tt.want.Additions {
				t.Errorf("Additions = %d, want %d", got.Additions, tt.want.Additions)
			}
			if got.Deletions != tt.want.Deletions {
				t.Errorf("Deletions = %d, want %d", got.Deletions, tt.want.Deletions)
			}
			if got.Changes != tt.want.Changes {
				t.Errorf("Changes = %d, want %d", got.Changes, tt.want.Changes)
			}
			if got.FileCount != len(got.Files) {
				t.Errorf("FileCount = %d but Files holds %d entries", got.FileCount, len(got.Files))
			}
		})
	}
}

func TestBuildContextZeroChangedFiles(t *testing.T) {
	ctx := BuildContext(testPR, testDetails, nil, nil, reporules.Rules{})

	if ctx.Changes.FileCount != 0 {
		t.Errorf("FileCount = %d, want 0", ctx.Changes.FileCount)
	}
	if ctx.Changes.Files != nil {
		t.Errorf("Files = %v, want nil", ctx.Changes.Files)
	}
	if ctx.HasChanges() {
		t.Error("HasChanges() = true, want false")
	}
	// A file-less pull request is still a valid context.
	if ctx.PullRequest.Number != 123 {
		t.Error("pull request metadata was lost when there are no files")
	}
}

func TestBuildContextPatchMapping(t *testing.T) {
	files := []github.ChangedFile{
		{Filename: "a.go", Patch: "@@ -1,4 +1,6 @@\n+added line\n-removed line"},
		{Filename: "logo.png", Patch: ""},
	}

	ctx := BuildContext(testPR, testDetails, files, nil, reporules.Rules{})

	if want := "@@ -1,4 +1,6 @@\n+added line\n-removed line"; ctx.Changes.Files[0].Patch != want {
		t.Errorf("Patch = %q, want %q", ctx.Changes.Files[0].Patch, want)
	}
	if !ctx.Changes.Files[0].HasPatch() {
		t.Error("HasPatch() = false for a file with a patch")
	}
	if ctx.Changes.Files[1].Patch != "" {
		t.Errorf("Patch = %q, want empty", ctx.Changes.Files[1].Patch)
	}
	if ctx.Changes.Files[1].HasPatch() {
		t.Error("HasPatch() = true for a file without a patch")
	}
}

func TestBuildContextTicket(t *testing.T) {
	ctx := BuildContext(testPR, testDetails, testFiles, &testIssue, reporules.Rules{})

	if !ctx.HasTicket() {
		t.Fatal("HasTicket() = false, want true")
	}
	if ctx.Ticket == nil {
		t.Fatal("Ticket = nil, want a ticket")
	}

	tests := []struct {
		field string
		got   string
		want  string
	}{
		{"Key", ctx.Ticket.Key, "PAY-431"},
		{"Summary", ctx.Ticket.Summary, "Retry failed card authorizations"},
		{"Description", ctx.Ticket.Description, "Retry failed payments"},
		{"Status", ctx.Ticket.Status, "In Progress"},
		{"IssueType", ctx.Ticket.IssueType, "Story"},
		{"Priority", ctx.Ticket.Priority, "High"},
		{"ParentKey", ctx.Ticket.ParentKey, "PAY-400"},
	}

	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("Ticket.%s = %q, want %q", tt.field, tt.got, tt.want)
		}
	}

	if strings.Join(ctx.Ticket.Labels, ",") != "payments,reliability" {
		t.Errorf("Labels = %v, want [payments reliability]", ctx.Ticket.Labels)
	}
}

func TestBuildContextTicketVariants(t *testing.T) {
	tests := []struct {
		name          string
		issue         jira.Issue
		wantParent    string
		wantLabels    []string
		wantLabelsNil bool
	}{
		{
			name:       "issue with a parent",
			issue:      jira.Issue{Key: "PAY-431", ParentKey: "PAY-400"},
			wantParent: "PAY-400",
		},
		{
			name:  "issue without a parent",
			issue: jira.Issue{Key: "PAY-431"},
		},
		{
			name:       "single label",
			issue:      jira.Issue{Key: "PAY-431", Labels: []string{"payments"}},
			wantLabels: []string{"payments"},
		},
		{
			name:       "several labels keep their order",
			issue:      jira.Issue{Key: "PAY-431", Labels: []string{"c", "a", "b"}},
			wantLabels: []string{"c", "a", "b"},
		},
		{
			name:          "nil labels stay nil",
			issue:         jira.Issue{Key: "PAY-431"},
			wantLabelsNil: true,
		},
		{
			name:       "empty label slice",
			issue:      jira.Issue{Key: "PAY-431", Labels: []string{}},
			wantLabels: []string{},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			ctx := BuildContext(testPR, testDetails, testFiles, &tt.issue, reporules.Rules{})

			if ctx.Ticket.ParentKey != tt.wantParent {
				t.Errorf("ParentKey = %q, want %q", ctx.Ticket.ParentKey, tt.wantParent)
			}
			if tt.wantLabelsNil && ctx.Ticket.Labels != nil {
				t.Errorf("Labels = %v, want nil", ctx.Ticket.Labels)
			}
			if !tt.wantLabelsNil && strings.Join(ctx.Ticket.Labels, ",") != strings.Join(tt.wantLabels, ",") {
				t.Errorf("Labels = %v, want %v", ctx.Ticket.Labels, tt.wantLabels)
			}
		})
	}
}

func TestBuildContextWithoutTicket(t *testing.T) {
	ctx := BuildContext(testPR, testDetails, testFiles, nil, reporules.Rules{})

	if ctx.Ticket != nil {
		t.Errorf("Ticket = %+v, want nil", ctx.Ticket)
	}
	if ctx.HasTicket() {
		t.Error("HasTicket() = true, want false")
	}

	// Everything else must still be populated: a missing ticket is not an error.
	if ctx.PullRequest.Title != "Add payment retry" {
		t.Error("pull request metadata was lost when there is no ticket")
	}
	if ctx.Changes.FileCount != 3 {
		t.Error("change data was lost when there is no ticket")
	}
}

// TestBuildContextDoesNotShareSlices is the snapshot guarantee: mutating the
// context must not reach back into the GitHub or Jira data, and vice versa.
func TestBuildContextDoesNotShareSlices(t *testing.T) {
	files := []github.ChangedFile{
		{Filename: "a.go", Status: "modified", Additions: 20, Deletions: 5, Changes: 25, Patch: "@@ a @@"},
		{Filename: "b.go", Status: "added", Additions: 10, Deletions: 2, Changes: 12, Patch: "@@ b @@"},
	}
	issue := jira.Issue{Key: "PAY-431", Labels: []string{"payments", "reliability"}}

	ctx := BuildContext(testPR, testDetails, files, &issue, reporules.Rules{})

	t.Run("mutating the context does not affect the inputs", func(t *testing.T) {
		ctx.Changes.Files[0].Filename = "MUTATED.go"
		ctx.Changes.Files[0].Additions = 9999
		ctx.Ticket.Labels[0] = "MUTATED"

		if files[0].Filename != "a.go" {
			t.Errorf("input file name changed to %q; the context shares the slice", files[0].Filename)
		}
		if files[0].Additions != 20 {
			t.Errorf("input additions changed to %d; the context shares the slice", files[0].Additions)
		}
		if issue.Labels[0] != "payments" {
			t.Errorf("input label changed to %q; the context shares the slice", issue.Labels[0])
		}
	})

	t.Run("mutating the inputs does not affect the context", func(t *testing.T) {
		fresh := BuildContext(testPR, testDetails, files, &issue, reporules.Rules{})

		files[1].Filename = "CHANGED_LATER.go"
		files[1].Deletions = 777
		issue.Labels[1] = "CHANGED_LATER"
		issue.Summary = "changed later"

		if fresh.Changes.Files[1].Filename != "b.go" {
			t.Errorf("context file name became %q; it is not a snapshot", fresh.Changes.Files[1].Filename)
		}
		if fresh.Changes.Files[1].Deletions != 2 {
			t.Errorf("context deletions became %d; it is not a snapshot", fresh.Changes.Files[1].Deletions)
		}
		if fresh.Ticket.Labels[1] != "reliability" {
			t.Errorf("context label became %q; it is not a snapshot", fresh.Ticket.Labels[1])
		}
		if fresh.Ticket.Summary != "" {
			t.Errorf("context summary became %q; it is not a snapshot", fresh.Ticket.Summary)
		}
	})

	t.Run("appending to the context files does not touch the input", func(t *testing.T) {
		c := BuildContext(testPR, testDetails, files, &issue, reporules.Rules{})
		c.Changes.Files = append(c.Changes.Files, FileChange{Filename: "extra.go"})

		if len(files) != 2 {
			t.Errorf("input slice grew to %d entries", len(files))
		}
	})

	t.Run("two contexts from the same inputs are independent", func(t *testing.T) {
		a := BuildContext(testPR, testDetails, files, &issue, reporules.Rules{})
		b := BuildContext(testPR, testDetails, files, &issue, reporules.Rules{})

		a.Changes.Files[0].Filename = "ONLY_IN_A.go"
		a.Ticket.Labels[0] = "ONLY_IN_A"

		if b.Changes.Files[0].Filename == "ONLY_IN_A.go" {
			t.Error("two contexts share the same file slice")
		}
		if b.Ticket.Labels[0] == "ONLY_IN_A" {
			t.Error("two contexts share the same label slice")
		}
	})
}

func TestContextHelpers(t *testing.T) {
	withTicket := BuildContext(testPR, testDetails, testFiles, &testIssue, reporules.Rules{})
	withoutTicket := BuildContext(testPR, testDetails, nil, nil, reporules.Rules{})

	if !withTicket.HasTicket() {
		t.Error("HasTicket() = false with a ticket")
	}
	if withoutTicket.HasTicket() {
		t.Error("HasTicket() = true without a ticket")
	}
	if !withTicket.HasChanges() {
		t.Error("HasChanges() = false with three files")
	}
	if withoutTicket.HasChanges() {
		t.Error("HasChanges() = true with no files")
	}

	if got := withTicket.PullRequest.Slug(); got != "acme/payments" {
		t.Errorf("Slug() = %q, want %q", got, "acme/payments")
	}
	if got := withTicket.PullRequest.Ref(); got != "acme/payments#123" {
		t.Errorf("Ref() = %q, want %q", got, "acme/payments#123")
	}
}

// TestZeroContextIsUsable checks the zero value does not panic through any
// helper, so later stages can hold a Context before it is built.
func TestZeroContextIsUsable(t *testing.T) {
	var ctx Context

	if ctx.HasTicket() {
		t.Error("HasTicket() = true on the zero context")
	}
	if ctx.IsDraft() {
		t.Error("IsDraft() = true on the zero context")
	}
	if ctx.HasChanges() {
		t.Error("HasChanges() = true on the zero context")
	}
	if got := ctx.PullRequest.Ref(); got != "/#0" {
		t.Errorf("Ref() = %q, want %q", got, "/#0")
	}
}

var testRules = reporules.Rules{Documents: []reporules.RuleDocument{
	{Path: ".ai-review/rules.md", Content: "review rules"},
	{Path: "AGENTS.md", Content: "agent rules"},
	{Path: "CONTRIBUTING.md", Content: "contributing rules"},
}}

func TestBuildContextRules(t *testing.T) {
	tests := []struct {
		name      string
		rules     reporules.Rules
		wantPaths []string
	}{
		{
			name:      "three rule documents keep their order",
			rules:     testRules,
			wantPaths: []string{".ai-review/rules.md", "AGENTS.md", "CONTRIBUTING.md"},
		},
		{
			name:      "no rules",
			rules:     reporules.Rules{},
			wantPaths: nil,
		},
		{
			name:      "nil document slice",
			rules:     reporules.Rules{Documents: nil},
			wantPaths: nil,
		},
		{
			name:      "empty document slice",
			rules:     reporules.Rules{Documents: []reporules.RuleDocument{}},
			wantPaths: nil,
		},
		{
			name: "single document",
			rules: reporules.Rules{Documents: []reporules.RuleDocument{
				{Path: "AGENTS.md", Content: "agent rules"},
			}},
			wantPaths: []string{"AGENTS.md"},
		},
		{
			name: "order is preserved even when it is not the canonical one",
			rules: reporules.Rules{Documents: []reporules.RuleDocument{
				{Path: "CONTRIBUTING.md", Content: "c"},
				{Path: "AGENTS.md", Content: "a"},
			}},
			wantPaths: []string{"CONTRIBUTING.md", "AGENTS.md"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			ctx := BuildContext(testPR, testDetails, testFiles, nil, tt.rules)

			if strings.Join(ctx.Rules.Paths(), ",") != strings.Join(tt.wantPaths, ",") {
				t.Errorf("paths = %v, want %v", ctx.Rules.Paths(), tt.wantPaths)
			}
			if ctx.Rules.Count() != len(tt.wantPaths) {
				t.Errorf("Count() = %d, want %d", ctx.Rules.Count(), len(tt.wantPaths))
			}
			if ctx.HasRules() != (len(tt.wantPaths) > 0) {
				t.Errorf("HasRules() = %t, want %t", ctx.HasRules(), len(tt.wantPaths) > 0)
			}

			// Content must survive the copy intact.
			for i, doc := range ctx.Rules.Documents {
				want := tt.rules.Documents[i]
				if doc.Path != want.Path || doc.Content != want.Content || doc.Truncated != want.Truncated {
					t.Errorf("document %d = %+v, want %+v", i, doc, want)
				}
			}
		})
	}
}

func TestBuildContextRulesTruncationFlag(t *testing.T) {
	rules := reporules.Rules{Documents: []reporules.RuleDocument{
		{Path: "AGENTS.md", Content: "cut short", Truncated: true},
		{Path: "CONTRIBUTING.md", Content: "complete"},
	}}

	ctx := BuildContext(testPR, testDetails, testFiles, nil, rules)

	if !ctx.Rules.Documents[0].Truncated {
		t.Error("Truncated = false, want true for the truncated document")
	}
	if ctx.Rules.Documents[1].Truncated {
		t.Error("Truncated = true, want false for the complete document")
	}
}

// TestBuildContextDoesNotShareRuleSlices extends the snapshot guarantee to rules.
func TestBuildContextDoesNotShareRuleSlices(t *testing.T) {
	rules := reporules.Rules{Documents: []reporules.RuleDocument{
		{Path: ".ai-review/rules.md", Content: "review rules"},
		{Path: "AGENTS.md", Content: "agent rules"},
	}}

	t.Run("mutating the context does not affect the input", func(t *testing.T) {
		ctx := BuildContext(testPR, testDetails, testFiles, nil, rules)

		ctx.Rules.Documents[0].Path = "MUTATED.md"
		ctx.Rules.Documents[0].Content = "MUTATED"
		ctx.Rules.Documents[0].Truncated = true

		if rules.Documents[0].Path != ".ai-review/rules.md" {
			t.Errorf("input path changed to %q; the context shares the slice", rules.Documents[0].Path)
		}
		if rules.Documents[0].Content != "review rules" {
			t.Errorf("input content changed to %q; the context shares the slice", rules.Documents[0].Content)
		}
		if rules.Documents[0].Truncated {
			t.Error("input truncation flag changed; the context shares the slice")
		}
	})

	t.Run("mutating the input does not affect the context", func(t *testing.T) {
		ctx := BuildContext(testPR, testDetails, testFiles, nil, rules)

		rules.Documents[1].Content = "CHANGED_LATER"

		if ctx.Rules.Documents[1].Content != "agent rules" {
			t.Errorf("context content became %q; it is not a snapshot", ctx.Rules.Documents[1].Content)
		}
	})

	t.Run("appending to the context rules does not touch the input", func(t *testing.T) {
		ctx := BuildContext(testPR, testDetails, testFiles, nil, rules)
		ctx.Rules.Documents = append(ctx.Rules.Documents, RuleDocument{Path: "extra.md"})

		if len(rules.Documents) != 2 {
			t.Errorf("input slice grew to %d entries", len(rules.Documents))
		}
	})

	t.Run("two contexts from the same rules are independent", func(t *testing.T) {
		a := BuildContext(testPR, testDetails, testFiles, nil, rules)
		b := BuildContext(testPR, testDetails, testFiles, nil, rules)

		a.Rules.Documents[0].Content = "ONLY_IN_A"

		if b.Rules.Documents[0].Content == "ONLY_IN_A" {
			t.Error("two contexts share the same rule slice")
		}
	})
}

// TestZeroRuleContextIsUsable checks the helpers tolerate the zero value.
func TestZeroRuleContextIsUsable(t *testing.T) {
	var ctx Context

	if ctx.HasRules() {
		t.Error("HasRules() = true on the zero context")
	}
	if ctx.Rules.Count() != 0 {
		t.Errorf("Count() = %d, want 0", ctx.Rules.Count())
	}
	if got := ctx.Rules.Paths(); len(got) != 0 {
		t.Errorf("Paths() = %v, want empty", got)
	}
}
