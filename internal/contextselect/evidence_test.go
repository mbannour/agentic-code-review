package contextselect

import (
	"context"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/analysis"
	"github.com/your-company/agentic-code-review/internal/evidence"
	"github.com/your-company/agentic-code-review/internal/review"
)

func TestExternalEvidenceIsBoundedBeforeReview(t *testing.T) {
	rc := review.Context{
		PullRequest: review.PullRequestContext{Owner: "acme", Repository: "orders", Number: 7},
		Changes: review.ChangeContext{FileCount: 1, Files: []review.FileChange{
			{Filename: "orders.go", Status: "modified", Patch: "@@ -1 +1 @@\n-old\n+new\n"},
		}},
		Evidence: review.EvidenceContext{Documents: []review.EvidenceDocument{
			{ID: "customer", Kind: evidence.KindRequirement, SourceType: evidence.SourceFile,
				Locator: "customer.md", Content: "explicit requirement"},
			{ID: "architecture", Kind: evidence.KindArchitecture, SourceType: evidence.SourceConfluence,
				Locator: "confluence:page/1", Content: strings.Repeat("design ", 200)},
		}},
	}

	selected, err := NewSelectorWithBudget(Budget{Total: 4096, Evidence: 80}).
		Select(context.Background(), rc, analysis.Result{})
	if err != nil {
		t.Fatalf("Select() = %v", err)
	}
	if selected.Stats.CandidateEvidence != 2 || selected.Stats.SelectedEvidence != 2 {
		t.Fatalf("stats = %+v", selected.Stats)
	}
	if selected.Stats.SelectedBytes > selected.Stats.BudgetBytes {
		t.Fatalf("selected %d bytes exceed budget %d", selected.Stats.SelectedBytes, selected.Stats.BudgetBytes)
	}
	if !selected.Evidence[1].Truncated || !strings.Contains(selected.Evidence[1].Content, MarkerEvidence) {
		t.Fatalf("second evidence was not honestly truncated: %+v", selected.Evidence[1])
	}
}
