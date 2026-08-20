package evaluation

import (
	"math"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/findings"
)

func label(id string, category findings.Category, file string, start, end int, terms ...string) Label {
	return Label{
		ID: id, Category: category, File: file, StartLine: start, EndLine: end, TitleContains: terms,
	}
}

func prediction(id string, category findings.Category, file string, start, end int, title string) Prediction {
	return Prediction{
		ID: id, Category: category, File: file, StartLine: start, EndLine: end, Title: title,
	}
}

func testLabels(cases ...LabelCase) LabelSet {
	return LabelSet{SchemaVersion: SchemaVersion, Name: "test-labels", Cases: cases}
}

func testPredictions(cases ...PredictionCase) PredictionSet {
	return PredictionSet{SchemaVersion: SchemaVersion, RunName: "test-run", Cases: cases}
}

func TestEvaluatePerfectRun(t *testing.T) {
	labels := testLabels(
		LabelCase{ID: "bug", Findings: []Label{
			label("SEC-001", findings.CategorySecurity, "src/auth.go", 10, 14, "authorization"),
		}},
		LabelCase{ID: "clean", Findings: []Label{}},
	)
	predictions := testPredictions(
		PredictionCase{ID: "bug", Findings: []Prediction{
			prediction("SEC-101", findings.CategorySecurity, "src/auth.go", 12, 12, "Missing authorization guard"),
		}},
		PredictionCase{ID: "clean", Findings: []Prediction{}},
	)

	report := Evaluate(labels, predictions)

	if report.CaseCount != 2 || report.LabelCount != 1 || report.PredictionCount != 1 {
		t.Fatalf("counts = cases %d labels %d predictions %d", report.CaseCount, report.LabelCount, report.PredictionCount)
	}
	if report.Metrics.TruePositives != 1 || report.Metrics.FalsePositives != 0 || report.Metrics.FalseNegatives != 0 {
		t.Fatalf("metrics = %+v", report.Metrics)
	}
	if report.Metrics.Precision != 1 || report.Metrics.Recall != 1 || report.Metrics.F1 != 1 {
		t.Errorf("rates = %+v, want all 1", report.Metrics)
	}
	if report.CleanCases != (CleanCaseStats{Total: 1, Correct: 1}) {
		t.Errorf("clean cases = %+v", report.CleanCases)
	}
	for _, c := range report.Cases {
		if !c.Passed {
			t.Errorf("case %s did not pass: %+v", c.ID, c)
		}
	}
}

func TestEvaluateCountsFalsePositivesFalseNegativesAndExtraCases(t *testing.T) {
	labels := testLabels(
		LabelCase{ID: "missed", Findings: []Label{
			label("COR-001", findings.CategoryCorrectness, "src/a.go", 20, 22),
		}},
		LabelCase{ID: "matched", Findings: []Label{
			label("REQ-001", findings.CategoryRequirement, "src/b.go", 30, 35),
		}},
	)
	predictions := testPredictions(
		PredictionCase{ID: "matched", Findings: []Prediction{
			prediction("REQ-101", findings.CategoryRequirement, "src/b.go", 32, 32, "Requirement gap"),
			prediction("SEC-101", findings.CategorySecurity, "src/b.go", 32, 32, "Different category"),
		}},
		PredictionCase{ID: "extra-case", Findings: []Prediction{
			prediction("MNT-101", findings.CategoryMaintainability, "src/c.go", 1, 1, "Unlabelled case finding"),
		}},
	)

	report := Evaluate(labels, predictions)

	if report.CaseCount != 3 {
		t.Fatalf("CaseCount = %d, want union of three cases", report.CaseCount)
	}
	if report.CleanCases.Total != 0 {
		t.Errorf("extra prediction-only case counted as a labelled clean case: %+v", report.CleanCases)
	}
	metrics := report.Metrics
	if metrics.TruePositives != 1 || metrics.FalsePositives != 2 || metrics.FalseNegatives != 1 {
		t.Fatalf("metrics = %+v", metrics)
	}
	assertNear(t, metrics.Precision, 1.0/3.0)
	assertNear(t, metrics.Recall, 0.5)
	assertNear(t, metrics.F1, 0.4)

	if report.Cases[0].ID != "missed" || report.Cases[1].ID != "matched" || report.Cases[2].ID != "extra-case" {
		t.Errorf("case order = %v, want label order then sorted extras", caseResultIDs(report.Cases))
	}
}

func TestMaximumMatchingDoesNotDependOnPredictionOrder(t *testing.T) {
	labels := []Label{
		label("COR-BROAD", findings.CategoryCorrectness, "src/a.go", 10, 20),
		label("COR-SPECIFIC", findings.CategoryCorrectness, "src/a.go", 10, 20, "timeout"),
	}
	predictions := []Prediction{
		prediction("P-SPECIFIC", findings.CategoryCorrectness, "src/a.go", 12, 12, "Timeout is swallowed"),
		prediction("P-BROAD", findings.CategoryCorrectness, "src/a.go", 14, 14, "Error is swallowed"),
	}

	result := evaluateCase("ambiguous", labels, predictions)

	if result.Metrics.TruePositives != 2 || result.Metrics.FalsePositives != 0 || result.Metrics.FalseNegatives != 0 {
		t.Fatalf("maximum matching failed: %+v", result)
	}
	if result.Matches[0] != (Match{LabelID: "COR-BROAD", PredictionID: "P-BROAD"}) ||
		result.Matches[1] != (Match{LabelID: "COR-SPECIFIC", PredictionID: "P-SPECIFIC"}) {
		t.Errorf("matches = %+v", result.Matches)
	}
}

func TestMatchingRequiresEveryDeterministicCondition(t *testing.T) {
	want := label("SEC-001", findings.CategorySecurity, "src/auth.go", 10, 15, "authorization", "guard")
	tests := []struct {
		name       string
		prediction Prediction
		matches    bool
	}{
		{"matching", prediction("P1", findings.CategorySecurity, "src\\auth.go", 12, 12, "Authorization guard missing"), true},
		{"wrong category", prediction("P2", findings.CategoryCorrectness, "src/auth.go", 12, 12, "Authorization guard missing"), false},
		{"wrong file", prediction("P3", findings.CategorySecurity, "src/other.go", 12, 12, "Authorization guard missing"), false},
		{"no line overlap", prediction("P4", findings.CategorySecurity, "src/auth.go", 16, 18, "Authorization guard missing"), false},
		{"missing title term", prediction("P5", findings.CategorySecurity, "src/auth.go", 12, 12, "Authorization missing"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matches(want, tt.prediction); got != tt.matches {
				t.Errorf("matches() = %t, want %t", got, tt.matches)
			}
		})
	}
}

func TestRenderIncludesMetricsAndDiagnostics(t *testing.T) {
	report := Evaluate(
		testLabels(LabelCase{ID: "case", Findings: []Label{
			label("COR-001", findings.CategoryCorrectness, "src/a.go", 4, 8),
		}}),
		testPredictions(PredictionCase{ID: "case", Findings: []Prediction{
			prediction("SEC-001", findings.CategorySecurity, "src/a.go", 5, 5, "Wrong category"),
		}}),
	)

	out := Render(report)
	for _, want := range []string{
		"Labelled Evaluation", "Precision", "Recall", "F1", "correctness", "security",
		"FAIL", "missed COR-001", "false  SEC-001", "src/a.go:4-8",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered report does not contain %q\n---\n%s", want, out)
		}
	}
}

func assertNear(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %f, want %f", got, want)
	}
}

func caseResultIDs(cases []CaseResult) []string {
	ids := make([]string, 0, len(cases))
	for _, c := range cases {
		ids = append(ids, c.ID)
	}
	return ids
}
