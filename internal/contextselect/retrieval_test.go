package contextselect

import (
	"context"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/analysis"
	"github.com/your-company/agentic-code-review/internal/retrieval"
	"github.com/your-company/agentic-code-review/internal/review"
	"github.com/your-company/agentic-code-review/internal/technology"
)

func retrievalContext(files ...review.FileChange) review.Context {
	return review.Context{
		PullRequest: review.PullRequestContext{
			Owner: "acme", Repository: "payments", Number: 1, Title: "Change",
		},
		Changes: review.ChangeContext{Files: files},
	}
}

func retrievedSnippet(symbol string, bytes int) retrieval.Snippet {
	return retrieval.Snippet{
		Symbol: symbol, Relation: retrieval.RelationDefinition,
		Path: "internal/other/" + symbol + ".go", StartLine: 10, EndLine: 20,
		Content: strings.Repeat("x", bytes),
	}
}

func selectWith(t *testing.T, budget Budget, ctxReview review.Context, retrieved retrieval.Result) SelectedContext {
	t.Helper()

	selected, err := NewSelectorWithBudget(budget).SelectWithRetrieval(
		context.Background(), ctxReview, analysis.Result{}, technology.Profile{}, retrieved)
	if err != nil {
		t.Fatalf("SelectWithRetrieval() = %v", err)
	}
	return selected
}

func TestRetrievedCodeIsCarriedIntoTheSelection(t *testing.T) {
	selected := selectWith(t, DefaultBudget(),
		retrievalContext(review.FileChange{
			Filename: "internal/payment/retry.go", Status: "modified",
			Patch: "@@ -1,2 +1,3 @@\n+	submitAuthorization(id)\n",
		}),
		retrieval.Result{Snippets: []retrieval.Snippet{retrievedSnippet("submitAuthorization", 200)}},
	)

	if len(selected.Code) != 1 {
		t.Fatalf("selected code = %d regions, want 1", len(selected.Code))
	}
	region := selected.Code[0]
	if region.Symbol != "submitAuthorization" || region.Relation != retrieval.RelationDefinition {
		t.Errorf("region = %+v", region)
	}
	if region.Location() != "internal/other/submitAuthorization.go:10-20" {
		t.Errorf("location = %q", region.Location())
	}
	if selected.Stats.SelectedCode != 1 || selected.Stats.CandidateCode != 1 {
		t.Errorf("stats = %+v", selected.Stats)
	}
}

// The priority that matters: context about a change is worth less than the change.
// A diff large enough to fill the budget costs retrieval its section, never the
// other way around.
func TestChangedPatchesOutrankRetrievedCode(t *testing.T) {
	budget := DefaultBudget()
	budget.Total = 4096

	bigPatch := "@@ -1,200 +1,400 @@\n" + strings.Repeat("+	changedLineOfRealCode()\n", 200)
	selected := selectWith(t, budget,
		retrievalContext(review.FileChange{
			Filename: "internal/payment/retry.go", Status: "modified", Patch: bigPatch,
		}),
		retrieval.Result{Snippets: []retrieval.Snippet{
			retrievedSnippet("someSymbol", 3000),
			retrievedSnippet("otherSymbol", 3000),
		}},
	)

	if len(selected.Files) == 0 {
		t.Fatal("the changed patch was dropped in favour of retrieved code")
	}
	if len(selected.Code) != 0 {
		t.Errorf("retrieved code = %d regions, want 0 when the diff needs the budget", len(selected.Code))
	}
	if selected.Stats.DroppedCode != 2 {
		t.Errorf("dropped code = %d, want 2", selected.Stats.DroppedCode)
	}
	if !selected.Stats.Truncated {
		t.Error("stats do not report the truncation")
	}
}

func TestRetrievedCodeRespectsItsOwnAllowance(t *testing.T) {
	budget := DefaultBudget()

	var snippets []retrieval.Snippet
	for i := 0; i < 40; i++ {
		snippets = append(snippets, retrievedSnippet("symbolNumber"+string(rune('a'+i%26))+string(rune('a'+i/26)), 4096))
	}

	selected := selectWith(t, budget,
		retrievalContext(review.FileChange{
			Filename: "a.go", Status: "modified", Patch: "@@ -1 +1,2 @@\n+	call()\n",
		}),
		retrieval.Result{Snippets: snippets},
	)

	total := 0
	for _, region := range selected.Code {
		total += len(region.Content)
	}
	if total > budget.Retrieval {
		t.Errorf("retrieved bytes = %d, want at most the %d allowance", total, budget.Retrieval)
	}
	if selected.Stats.DroppedCode == 0 {
		t.Error("nothing was reported as dropped despite exceeding the allowance")
	}
}

// An empty section must say why, so a repository with nothing to retrieve can be
// told apart from a stage that was never asked to run.
func TestSkippedRetrievalIsExplained(t *testing.T) {
	selected := selectWith(t, DefaultBudget(),
		retrievalContext(review.FileChange{
			Filename: "a.go", Status: "modified", Patch: "@@ -1 +1,2 @@\n+	call()\n",
		}),
		retrieval.Result{Skipped: true, Reason: "--retrieve not provided"},
	)

	if len(selected.Code) != 0 {
		t.Errorf("selected code = %d regions, want 0", len(selected.Code))
	}
	if selected.Stats.RetrievalSkipped != "--retrieve not provided" {
		t.Errorf("skip reason = %q", selected.Stats.RetrievalSkipped)
	}
}

// The default path must behave exactly as it did before retrieval existed.
func TestSelectWithTechnologyLeavesCodeEmpty(t *testing.T) {
	selected, err := NewSelector().SelectWithTechnology(
		context.Background(),
		retrievalContext(review.FileChange{
			Filename: "a.go", Status: "modified", Patch: "@@ -1 +1,2 @@\n+	call()\n",
		}),
		analysis.Result{}, technology.Profile{})
	if err != nil {
		t.Fatalf("SelectWithTechnology() = %v", err)
	}

	if len(selected.Code) != 0 {
		t.Errorf("selected code = %d regions, want 0 without retrieval", len(selected.Code))
	}
	if selected.Stats.RetrievalSkipped == "" {
		t.Error("the untouched path does not explain the empty code section")
	}
}
