package claude

import (
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/contextselect"
)

func inputWithFocus(t *testing.T, focus contextselect.ReviewFocus) string {
	t.Helper()

	input, err := NewInputBuilder().Build(contextselect.SelectedContext{
		PullRequest: contextselect.PullRequestSummary{
			Owner: "acme", Repository: "payments", Number: 1, Title: "Change",
		},
		Files: []contextselect.SelectedFile{{
			Path: "internal/payments/capture.go", Status: "modified",
			Patch: "@@ -1,2 +1,3 @@\n+	capture(payment)\n",
			Kind:  contextselect.FileKindSource, Importance: contextselect.ImportanceHigh,
		}},
		Focus: focus,
	})
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	return input.Content
}

func TestReviewFocusStatesRiskAreasAsSignalsNotFindings(t *testing.T) {
	content := inputWithFocus(t, contextselect.ReviewFocus{
		RiskLevel:   "high",
		RiskAreas:   []string{"payments", "state_machine"},
		RiskReasons: []string{"payments: path names payments"},
		Specialists: []contextselect.FocusSpecialist{{
			ID: "reliability", Title: "Reliability",
			Purpose: "What happens when this runs twice, slowly, or halfway?",
			Focus:   []string{"a retry path that can duplicate an effect, event, or charge"},
		}},
	})

	for _, want := range []string{
		"REVIEW FOCUS",
		"Assessed change risk: high",
		"payments, state_machine",
		"They are not defects and must not be reported as such",
		"Reliability — What happens when this runs twice",
		"a retry path that can duplicate an effect, event, or charge",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

// Routing allocates attention; it must not tell the reviewer that nothing else
// can be wrong, or the cost decision would become a correctness claim.
func TestReviewFocusDoesNotNarrowHonesty(t *testing.T) {
	content := inputWithFocus(t, contextselect.ReviewFocus{
		RiskLevel: "medium",
		Specialists: []contextselect.FocusSpecialist{{
			ID: "security", Title: "Security", Purpose: "Can this reach what it should not?",
			Focus: []string{"authorization checks removed"},
		}},
	})

	for _, want := range []string{
		"outside these perspectives is still worth reporting",
		"allocates attention",
		"nothing to report contributes zero findings",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

// With no focus computed the section is absent rather than empty, so the prompt
// never implies an assessment that was not made.
func TestReviewFocusIsOmittedWhenAbsent(t *testing.T) {
	content := inputWithFocus(t, contextselect.ReviewFocus{})

	if strings.Contains(content, "REVIEW FOCUS") {
		t.Error("the focus section appears with no focus computed")
	}
}
