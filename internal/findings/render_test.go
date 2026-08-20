package findings

import (
	"strings"
	"testing"
)

func TestRenderFinding(t *testing.T) {
	got := Render(validResult())

	want := []string{
		"Agentic Review",
		"1 actionable finding",
		"Found one actionable correctness issue.",
		"HIGH · COR-001",
		"internal/payment/retry.go:84-87",
		"Permanent declines enter the retry path",
		"Problem",
		"The new branch treats a permanent decline as a retryable failure.",
		"Impact",
		"A declined card can be submitted repeatedly.",
		"Evidence",
		"- CODE: internal/payment/retry.go:84-87",
		"- JIRA: PAY-431",
		"Suggestion",
		"Return before entering RetryPayment when the decline is permanent.",
		"Confidence: 96%",
	}

	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("rendered report missing %q; got:\n%s", w, got)
		}
	}

	// The sections appear in a fixed order.
	order := []string{"HIGH · COR-001", "internal/payment/retry.go:84-87",
		"Permanent declines enter the retry path", "Problem", "Impact", "Evidence",
		"Suggestion", "Confidence: 96%"}
	last := -1
	for _, section := range order {
		idx := strings.Index(got, section)
		if idx < last {
			t.Errorf("%q appears out of order in:\n%s", section, got)
		}
		last = idx
	}
}

// TestRenderZeroFindings is the clean-review output: a result, not an error.
func TestRenderZeroFindings(t *testing.T) {
	got := Render(ReviewResult{Summary: "No actionable issues found."})

	if !strings.HasPrefix(got, Header+"\n\n") {
		t.Errorf("report does not start with the header; got:\n%s", got)
	}
	if !strings.Contains(got, NoFindingsMessage) {
		t.Errorf("report missing %q; got:\n%s", NoFindingsMessage, got)
	}
	for _, notWant := range []string{"Problem", "Evidence", "Confidence:", "1 actionable finding"} {
		if strings.Contains(got, notWant) {
			t.Errorf("empty report unexpectedly contains %q; got:\n%s", notWant, got)
		}
	}
}

func TestRenderCountLine(t *testing.T) {
	tests := []struct {
		count int
		want  string
	}{
		{count: 1, want: "1 actionable finding\n"},
		{count: 2, want: "2 actionable findings\n"},
		{count: 3, want: "3 actionable findings\n"},
	}

	for _, tt := range tests {
		result := ReviewResult{Summary: "s"}
		for i := 0; i < tt.count; i++ {
			finding := validFinding()
			finding.ID = "COR-00" + string(rune('1'+i))
			finding.StartLine, finding.EndLine = 84+i, 84+i
			result.Findings = append(result.Findings, finding)
		}

		if got := Render(result); !strings.Contains(got, tt.want) {
			t.Errorf("Render() with %d findings missing %q; got:\n%s", tt.count, tt.want, got)
		}
	}
}

// TestRenderOrdersBySeverity checks the report leads with what matters most, and
// that ordering does not depend on the order the findings arrived in.
func TestRenderOrdersBySeverity(t *testing.T) {
	low := validFinding()
	low.ID, low.Severity, low.StartLine, low.EndLine = "MAINT-001", SeverityLow, 90, 90
	low.Title = "Low severity problem"

	blocker := validFinding()
	blocker.ID, blocker.Severity, blocker.StartLine, blocker.EndLine = "SEC-001", SeverityBlocker, 88, 88
	blocker.Title = "Blocker severity problem"

	medium := validFinding()
	medium.ID, medium.Severity, medium.StartLine, medium.EndLine = "TEST-001", SeverityMedium, 89, 89
	medium.Title = "Medium severity problem"

	result := ReviewResult{
		Summary:  "Four issues.",
		Findings: []Finding{low, medium, validFinding(), blocker},
	}

	got := Render(result)
	order := []string{"BLOCKER · SEC-001", "HIGH · COR-001", "MEDIUM · TEST-001", "LOW · MAINT-001"}

	last := -1
	for _, section := range order {
		idx := strings.Index(got, section)
		if idx < 0 {
			t.Fatalf("report missing %q; got:\n%s", section, got)
		}
		if idx < last {
			t.Errorf("%q appears out of severity order in:\n%s", section, got)
		}
		last = idx
	}
}

// TestRenderDoesNotMutate guards against the renderer reordering the caller's
// slice as a side effect of sorting.
func TestRenderDoesNotMutate(t *testing.T) {
	second := validFinding()
	second.ID, second.Severity, second.StartLine, second.EndLine = "SEC-001", SeverityBlocker, 88, 88
	second.Title = "Blocker"

	result := ReviewResult{Summary: "Two.", Findings: []Finding{validFinding(), second}}
	Render(result)

	if result.Findings[0].ID != "COR-001" || result.Findings[1].ID != "SEC-001" {
		t.Errorf("Render() reordered the caller's findings: %s, %s",
			result.Findings[0].ID, result.Findings[1].ID)
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	result := validResult()
	first := Render(result)

	for i := 0; i < 20; i++ {
		if got := Render(result); got != first {
			t.Fatalf("Render() is not deterministic:\n%s\nvs\n%s", first, got)
		}
	}
}

func TestFindingLocationAndConfidence(t *testing.T) {
	tests := []struct {
		name           string
		finding        Finding
		wantLocation   string
		wantConfidence int
	}{
		{
			name:           "line range",
			finding:        Finding{File: "a.go", StartLine: 84, EndLine: 87, Confidence: 0.96},
			wantLocation:   "a.go:84-87",
			wantConfidence: 96,
		},
		{
			name:           "single line",
			finding:        Finding{File: "a.go", StartLine: 12, EndLine: 12, Confidence: 0.815},
			wantLocation:   "a.go:12",
			wantConfidence: 82,
		},
		{
			name:           "certain",
			finding:        Finding{File: "a.go", StartLine: 1, EndLine: 1, Confidence: 1},
			wantLocation:   "a.go:1",
			wantConfidence: 100,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.finding.Location(); got != tt.wantLocation {
				t.Errorf("Location() = %q, want %q", got, tt.wantLocation)
			}
			if got := tt.finding.ConfidencePercent(); got != tt.wantConfidence {
				t.Errorf("ConfidencePercent() = %d, want %d", got, tt.wantConfidence)
			}
		})
	}
}

// TestRenderExposesNoPatch is a structural guarantee: a rendered finding explains,
// and never hands the reader something to apply or run.
func TestRenderExposesNoPatch(t *testing.T) {
	got := Render(validResult())

	for _, forbidden := range []string{"--- a/", "+++ b/", "```", "$ ", "git apply"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("rendered report contains %q; got:\n%s", forbidden, got)
		}
	}
}
