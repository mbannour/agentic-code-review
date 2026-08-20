package publish

import (
	"testing"

	"github.com/your-company/agentic-code-review/internal/findings"
)

// TestPlanPartitionsEveryFinding is the property the plan exists to guarantee: each
// finding ends up in exactly one list, and none is lost on the way.
func TestPlanPartitionsEveryFinding(t *testing.T) {
	result := resultFixture(
		finding("COR-001", findings.SeverityBlocker, 0.99, testFile, 82, 82), // inline
		finding("SEC-001", findings.SeverityHigh, 0.70, testFile, 83, 83),    // low confidence
		finding("MAINT-001", findings.SeverityLow, 0.99, testFile, 84, 84),   // low severity
		finding("ARCH-001", findings.SeverityHigh, 0.95, testFile, 900, 900), // unmappable
	)

	plan := planFor(result.Findings...)

	if plan.TotalFindings() != len(result.Findings) {
		t.Fatalf("TotalFindings() = %d, want %d", plan.TotalFindings(), len(result.Findings))
	}

	seen := map[string]int{}
	for _, item := range plan.Inline {
		seen[item.Finding.ID]++
	}
	for _, f := range plan.Summary {
		seen[f.ID]++
	}
	for _, item := range plan.Suppressed {
		seen[item.Finding.ID]++
	}

	for _, f := range result.Findings {
		if seen[f.ID] != 1 {
			t.Errorf("%s appears %d times across the plan, want exactly 1", f.ID, seen[f.ID])
		}
	}
}

// TestPlanEmptyOnlyWhenNothingFound checks the distinction publication depends on: a
// plan with summary findings and no inline comments is not empty.
func TestPlanEmptyOnlyWhenNothingFound(t *testing.T) {
	nothing := planFor()
	if !nothing.Empty() {
		t.Error("Empty() = false for a result with no findings")
	}

	summaryOnly := planFor(finding("MAINT-001", findings.SeverityLow, 0.82, testFile, 82, 82))

	if summaryOnly.Empty() {
		t.Error("Empty() = true for a summary-only plan; that is a publishable review")
	}
	if len(summaryOnly.Inline) != 0 || len(summaryOnly.Summary) != 1 {
		t.Errorf("plan = %d inline, %d summary; want 0 and 1",
			len(summaryOnly.Inline), len(summaryOnly.Summary))
	}
}

// TestPlanCountBySeverity checks the severity tally the review body reports.
func TestPlanCountBySeverity(t *testing.T) {
	plan := planFor(finding("B-1", findings.SeverityBlocker, 0.99, testFile, 82, 82),
		finding("H-1", findings.SeverityHigh, 0.95, testFile, 83, 83),
		finding("H-2", findings.SeverityHigh, 0.90, testFile, 84, 84),
		finding("L-1", findings.SeverityLow, 0.99, testFile, 85, 85))

	tests := []struct {
		severity findings.Severity
		want     int
	}{
		{findings.SeverityBlocker, 1},
		{findings.SeverityHigh, 2},
		{findings.SeverityMedium, 0},
		{findings.SeverityLow, 1},
	}

	for _, tt := range tests {
		if got := plan.CountBySeverity(tt.severity); got != tt.want {
			t.Errorf("CountBySeverity(%s) = %d, want %d", tt.severity, got, tt.want)
		}
	}
}

// TestPlanPublishableFindingsOrder checks inline findings come first, which is the order
// the review body reports them in.
func TestPlanPublishableFindingsOrder(t *testing.T) {
	plan := planFor(finding("LOW-1", findings.SeverityLow, 0.99, testFile, 82, 82),
		finding("HIGH-1", findings.SeverityHigh, 0.95, testFile, 83, 83))

	got := plan.PublishableFindings()
	if len(got) != 2 || got[0].ID != "HIGH-1" || got[1].ID != "LOW-1" {
		t.Errorf("PublishableFindings() = %v, want the inline finding first", got)
	}
}

// TestPlanCarriesHeadAndSummary checks the plan records what it was built from, so the
// renderer and publisher never have to guess.
func TestPlanCarriesHeadAndSummary(t *testing.T) {
	result := resultFixture(findingFixture())
	plan := planFor(result.Findings...)

	if plan.HeadSHA != testHeadSHA {
		t.Errorf("HeadSHA = %q, want %q", plan.HeadSHA, testHeadSHA)
	}
	if plan.ResultSummary != result.Summary {
		t.Errorf("ResultSummary = %q, want %q", plan.ResultSummary, result.Summary)
	}
}

// TestPlanAccountsForEveryFinding checks the partition Step 17 guarantees.
//
// Earlier steps suppressed nothing: every validated finding reached the reader somewhere. That
// is no longer true, and deliberately so — a finding below its confidence gate is withheld —
// but the accounting still has to be exact, and every suppression still has to say why.
func TestPlanAccountsForEveryFinding(t *testing.T) {
	fs := []findings.Finding{
		finding("A", findings.SeverityBlocker, 0.99, testFile, 82, 82),
		finding("B", findings.SeverityLow, 0.10, testFile, 900, 900),
		finding("C", findings.SeverityMedium, 0.50, "internal/other/x.go", 5, 5),
	}

	plan := planFor(fs...)

	if plan.TotalFindings() != len(fs) {
		t.Errorf("TotalFindings() = %d, want %d", plan.TotalFindings(), len(fs))
	}
	if len(plan.Decisions) != len(fs) {
		t.Errorf("Decisions = %d, want %d", len(plan.Decisions), len(fs))
	}
	for _, item := range plan.Suppressed {
		if item.Reason == "" {
			t.Errorf("%s was suppressed with no reason", item.Finding.ID)
		}
	}

	// The two weak findings are withheld; the blocker with strong evidence is not.
	if len(plan.Suppressed) != 2 {
		t.Errorf("Suppressed = %d, want the two sub-threshold findings", len(plan.Suppressed))
	}
	if len(plan.Inline) != 1 || plan.Inline[0].Finding.ID != "A" {
		t.Errorf("Inline = %+v, want only the blocker", plan.Inline)
	}
}
