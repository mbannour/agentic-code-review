package contextselect

import (
	"context"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/analysis"
	"github.com/your-company/agentic-code-review/internal/review"
	"github.com/your-company/agentic-code-review/internal/technology"
)

func TestBudgetForRiskLevel(t *testing.T) {
	cases := map[string]int{
		"minimal":  LowRiskContextBudgetBytes,
		"low":      LowRiskContextBudgetBytes,
		"medium":   MediumRiskContextBudgetBytes,
		"high":     HighRiskContextBudgetBytes,
		"":         HighRiskContextBudgetBytes,
		"nonsense": HighRiskContextBudgetBytes,
	}

	for level, want := range cases {
		t.Run(level, func(t *testing.T) {
			if got := BudgetForRiskLevel(level).Total; got != want {
				t.Errorf("total = %d, want %d", got, want)
			}
		})
	}
}

// Failing to recognize a risk band is not a reason to review with less context.
func TestUnknownRiskLevelGetsTheFullBudget(t *testing.T) {
	if BudgetForRiskLevel("catastrophic").Total != DefaultContextBudgetBytes {
		t.Error("an unrecognized band was given a reduced budget")
	}
}

// Clamping instead of scaling would leave the fixed sections at full size and take
// the whole reduction out of the patches — starving the change itself.
//
// The sub-allowances are ceilings rather than reservations: whatever the rules,
// evidence, and retrieval sections do not use flows to the patches. So the property
// worth pinning is proportional, not absolute — a reduced budget must not leave the
// patches a *smaller share* than the default does.
func TestScalingDoesNotStarveThePatches(t *testing.T) {
	guaranteedShare := func(b Budget) float64 {
		fixed := b.Rules + b.Analysis + b.Evidence + b.Retrieval + b.Discussion + b.TicketDescription
		if fixed >= b.Total {
			return 0
		}
		return float64(b.Total-fixed) / float64(b.Total)
	}

	base := guaranteedShare(DefaultBudget())
	if base <= 0 {
		t.Fatal("the default budget guarantees the patches nothing")
	}

	for _, level := range []string{"low", "medium"} {
		t.Run(level, func(t *testing.T) {
			scaled := BudgetForRiskLevel(level)
			if got := guaranteedShare(scaled); got < base {
				t.Errorf("patch share = %.3f at %s, want at least the default %.3f",
					got, level, base)
			}
		})
	}
}

// Every section stays useful rather than vestigial.
func TestScalingKeepsFloors(t *testing.T) {
	scaled := DefaultBudget().ScaledTo(16 * 1024)

	for name, value := range map[string]int{
		"rules": scaled.Rules, "analysis": scaled.Analysis, "evidence": scaled.Evidence,
		"retrieval": scaled.Retrieval, "discussion": scaled.Discussion,
		"ticket": scaled.TicketDescription,
	} {
		if value <= 0 {
			t.Errorf("%s allowance = %d, want a usable floor", name, value)
		}
	}
}

func TestScalingUpIsRefused(t *testing.T) {
	scaled := DefaultBudget().ScaledTo(10 * 1024 * 1024)
	if scaled.Total != DefaultContextBudgetBytes {
		t.Errorf("total = %d, want the default ceiling", scaled.Total)
	}
}

// The point of the tiering: a smaller ceiling really does select fewer bytes.
func TestLowerTierSelectsFewerBytes(t *testing.T) {
	files := make([]review.FileChange, 0, 30)
	for i := 0; i < 30; i++ {
		files = append(files, review.FileChange{
			Filename: "internal/pkg/file" + string(rune('a'+i)) + ".go", Status: "modified",
			Patch: "@@ -1,50 +1,80 @@\n" + strings.Repeat("+\tcall(argument)\n", 200),
		})
	}
	reviewCtx := review.Context{
		PullRequest: review.PullRequestContext{Owner: "acme", Repository: "payments", Number: 1},
		Changes:     review.ChangeContext{Files: files},
	}

	full, err := NewSelectorWithBudget(BudgetForRiskLevel("high")).
		SelectWithTechnology(context.Background(), reviewCtx, analysis.Result{}, technology.Profile{})
	if err != nil {
		t.Fatalf("select(high) = %v", err)
	}
	low, err := NewSelectorWithBudget(BudgetForRiskLevel("low")).
		SelectWithTechnology(context.Background(), reviewCtx, analysis.Result{}, technology.Profile{})
	if err != nil {
		t.Fatalf("select(low) = %v", err)
	}

	if low.Stats.SelectedBytes >= full.Stats.SelectedBytes {
		t.Errorf("low tier selected %d bytes, high tier %d; the tier had no effect",
			low.Stats.SelectedBytes, full.Stats.SelectedBytes)
	}
	if low.Stats.SelectedBytes > LowRiskContextBudgetBytes {
		t.Errorf("low tier selected %d bytes, above its %d ceiling",
			low.Stats.SelectedBytes, LowRiskContextBudgetBytes)
	}
	// Robustness: a reduced ceiling must still carry the change, and must still say
	// what it dropped.
	if len(low.Stats.Dropped) > 0 && !low.Stats.Truncated {
		t.Error("content was dropped without the selection reporting truncation")
	}
	if len(low.Files) == 0 {
		t.Error("the low tier selected no changed files at all")
	}
}

func TestEstimatedTokens(t *testing.T) {
	if got := EstimatedTokens(200 * 1024); got < 40_000 || got > 60_000 {
		t.Errorf("EstimatedTokens(200 KB) = %d, want roughly 50k", got)
	}
	if EstimatedTokens(0) != 0 {
		t.Error("EstimatedTokens(0) is not zero")
	}
}
