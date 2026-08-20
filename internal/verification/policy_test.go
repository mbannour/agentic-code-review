package verification

import (
	"testing"

	"github.com/your-company/agentic-code-review/internal/findings"
)

// TestRequiresVerification checks which severities are worth an invocation. The rule
// follows publication: a finding that can never reach a line does not need a second
// opinion to stay out of the diff.
func TestRequiresVerification(t *testing.T) {
	tests := []struct {
		severity findings.Severity
		want     bool
	}{
		{findings.SeverityBlocker, true},
		{findings.SeverityHigh, true},
		{findings.SeverityMedium, true},
		{findings.SeverityLow, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.severity), func(t *testing.T) {
			if got := RequiresVerification(finding("F-1", tt.severity, 0.9)); got != tt.want {
				t.Errorf("RequiresVerification(%s) = %t, want %t", tt.severity, got, tt.want)
			}
		})
	}
}

// TestRequiresVerificationIgnoresConfidence checks a low-severity finding is skipped however
// certain the reviewer was — including the 82% maintainability case observed on a real pull
// request, which stays summary-only and costs no invocation.
func TestRequiresVerificationIgnoresConfidence(t *testing.T) {
	for _, confidence := range []float64{0.10, 0.82, 0.99, 1.0} {
		low := finding("MAINT-001", findings.SeverityLow, confidence)
		low.Category = findings.CategoryMaintainability

		if RequiresVerification(low) {
			t.Errorf("a low finding at %.2f confidence was sent for verification", confidence)
		}
	}
}
