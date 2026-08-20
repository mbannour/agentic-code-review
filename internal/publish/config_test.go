package publish

import (
	"errors"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/findings"
)

// TestDefaultConfigIsValid checks the shipped policy passes its own validation, and that its
// numbers are the ones the policy documents.
func TestDefaultConfigIsValid(t *testing.T) {
	config := DefaultConfig()

	if err := config.Validate(); err != nil {
		t.Fatalf("DefaultConfig().Validate() = %v", err)
	}

	tests := []struct {
		name string
		got  float64
		want float64
	}{
		{"blocker reviewer", config.BlockerReviewerConfidence, 0.80},
		{"blocker verifier", config.BlockerVerifierConfidence, 0.80},
		{"high reviewer", config.HighReviewerConfidence, 0.80},
		{"high verifier", config.HighVerifierConfidence, 0.80},
		{"medium reviewer", config.MediumReviewerConfidence, 0.85},
		{"medium verifier", config.MediumVerifierConfidence, 0.85},
		{"low summary", config.LowSummaryConfidence, 0.80},
		{"security verifier", config.SecurityVerifierConfidence, 0.90},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s threshold = %v, want %v", tt.name, tt.got, tt.want)
		}
	}

	if config.MaxInlineComments != 10 || config.MaxSummaryFindings != 10 || config.MaxPublishedFindings != 20 {
		t.Errorf("limits = %d/%d/%d, want 10/10/20",
			config.MaxInlineComments, config.MaxSummaryFindings, config.MaxPublishedFindings)
	}
	if config.ArchitectureMediumInline {
		t.Error("ArchitectureMediumInline = true by default; medium architecture belongs in the body")
	}
}

// TestConfigValidationRejectsNonsense checks a malformed policy is refused rather than repaired.
// A policy that silently fell back to defaults would publish under rules nobody chose.
func TestConfigValidationRejectsNonsense(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantMsg string
	}{
		{
			name:    "negative reviewer confidence",
			mutate:  func(c *Config) { c.HighReviewerConfidence = -0.1 },
			wantMsg: "HighReviewerConfidence",
		},
		{
			name:    "reviewer confidence above one",
			mutate:  func(c *Config) { c.BlockerReviewerConfidence = 1.5 },
			wantMsg: "BlockerReviewerConfidence",
		},
		{
			name:    "negative verifier confidence",
			mutate:  func(c *Config) { c.MediumVerifierConfidence = -1 },
			wantMsg: "MediumVerifierConfidence",
		},
		{
			name:    "verifier confidence above one",
			mutate:  func(c *Config) { c.SecurityVerifierConfidence = 2 },
			wantMsg: "SecurityVerifierConfidence",
		},
		{
			name:    "low summary confidence out of range",
			mutate:  func(c *Config) { c.LowSummaryConfidence = 1.01 },
			wantMsg: "LowSummaryConfidence",
		},
		{
			name:    "zero inline limit",
			mutate:  func(c *Config) { c.MaxInlineComments = 0 },
			wantMsg: "MaxInlineComments",
		},
		{
			name:    "negative inline limit",
			mutate:  func(c *Config) { c.MaxInlineComments = -3 },
			wantMsg: "MaxInlineComments",
		},
		{
			name:    "zero summary limit",
			mutate:  func(c *Config) { c.MaxSummaryFindings = 0 },
			wantMsg: "MaxSummaryFindings",
		},
		{
			name:    "zero total limit",
			mutate:  func(c *Config) { c.MaxPublishedFindings = 0 },
			wantMsg: "MaxPublishedFindings",
		},
		{
			name: "total below the inline cap is a contradiction",
			mutate: func(c *Config) {
				c.MaxInlineComments = 10
				c.MaxPublishedFindings = 5
			},
			wantMsg: "is below MaxInlineComments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := DefaultConfig()
			tt.mutate(&config)

			err := config.Validate()
			if err == nil {
				t.Fatal("Validate() = nil, want a rejection")
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("errors.Is(err, ErrInvalidConfig) = false; err = %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tt.wantMsg)
			}
		})
	}
}

// TestConfigValidationReportsEveryProblem checks one pass surfaces the whole configuration
// fault rather than stopping at the first.
func TestConfigValidationReportsEveryProblem(t *testing.T) {
	config := Config{
		HighReviewerConfidence:   -1,
		MediumVerifierConfidence: 3,
	}

	err := config.Validate()
	if err == nil {
		t.Fatal("Validate() = nil")
	}
	// Two bad thresholds and three unset limits.
	if got := strings.Count(err.Error(), "\n  - "); got < 5 {
		t.Errorf("reported %d problems, want every one:\n%v", got, err)
	}
}

// TestInvalidConfigFailsConstruction checks a policy cannot be built from a bad configuration.
func TestInvalidConfigFailsConstruction(t *testing.T) {
	config := DefaultConfig()
	config.MaxInlineComments = 0

	if _, err := NewPolicyWithConfig(config); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("NewPolicyWithConfig() = %v, want ErrInvalidConfig", err)
	}

	valid, err := NewPolicyWithConfig(DefaultConfig())
	if err != nil {
		t.Fatalf("NewPolicyWithConfig(DefaultConfig()) = %v", err)
	}
	if valid.Config() != DefaultConfig() {
		t.Error("the constructed policy does not carry the configuration it was given")
	}
}

// TestZeroPolicyFallsBackToDefaults checks a Policy built with its zero value behaves as the
// default policy rather than as a policy with every threshold at zero, which would publish
// everything.
func TestZeroPolicyFallsBackToDefaults(t *testing.T) {
	var zero Policy

	if zero.Config() != DefaultConfig() {
		t.Fatalf("zero Policy config = %+v, want the defaults", zero.Config())
	}

	f := codeFinding("COR-001", findings.CategoryCorrectness, findings.SeverityHigh, 0.10)
	decision := zero.Evaluate(verifiedCandidate(f, "valid", 0.10), mapperFixture())

	if decision.Disposition != DispositionSuppress {
		t.Errorf("disposition = %s, want suppress; a zero policy must not publish everything",
			decision.Disposition)
	}
}

// TestThresholdLookupsBySeverity checks the per-severity accessors.
func TestThresholdLookupsBySeverity(t *testing.T) {
	config := DefaultConfig()

	tests := []struct {
		severity     findings.Severity
		wantReviewer float64
		wantVerifier float64
	}{
		{findings.SeverityBlocker, 0.80, 0.80},
		{findings.SeverityHigh, 0.80, 0.80},
		{findings.SeverityMedium, 0.85, 0.85},
	}

	for _, tt := range tests {
		t.Run(string(tt.severity), func(t *testing.T) {
			if got := config.ReviewerThreshold(tt.severity); got != tt.wantReviewer {
				t.Errorf("ReviewerThreshold(%s) = %v, want %v", tt.severity, got, tt.wantReviewer)
			}
			if got := config.VerifierThreshold(tt.severity); got != tt.wantVerifier {
				t.Errorf("VerifierThreshold(%s) = %v, want %v", tt.severity, got, tt.wantVerifier)
			}
		})
	}
}

// TestReasonRendering checks the reason model's output, which appears in terminal reports and in
// the plan's suppression records.
func TestReasonRendering(t *testing.T) {
	if got := (Reason{Code: ReasonLowSeverity}).String(); got != "low_severity" {
		t.Errorf("Reason.String() = %q, want the bare code", got)
	}
	if got := (Reason{Code: ReasonCommentLimit, Detail: "limit of 10"}).String(); got != "comment_limit: limit of 10" {
		t.Errorf("Reason.String() = %q, want code and detail", got)
	}

	reasons := Reasons{
		{Code: ReasonVerifiedValid, Detail: "verifier confidence 97%"},
		{Code: ReasonCommentLimit},
	}
	if got := reasons.Primary().Code; got != ReasonVerifiedValid {
		t.Errorf("Primary() = %q, want the first reason", got)
	}
	if !reasons.Has(ReasonCommentLimit) {
		t.Error("Has(ReasonCommentLimit) = false")
	}
	if reasons.Has(ReasonLowSeverity) {
		t.Error("Has(ReasonLowSeverity) = true for a reason that is not present")
	}
	if got := reasons.String(); !strings.Contains(got, "; ") {
		t.Errorf("Reasons.String() = %q, want the reasons joined", got)
	}
	if got := (Reasons{}).Primary().Code; got != "" {
		t.Errorf("Primary() on an empty list = %q, want empty", got)
	}
}

// TestDispositionDisplay checks the terminal renderings.
func TestDispositionDisplay(t *testing.T) {
	tests := map[Disposition]string{
		DispositionInline:   "INLINE",
		DispositionSummary:  "SUMMARY",
		DispositionSuppress: "SUPPRESS",
		Disposition("x"):    "?",
	}

	for disposition, want := range tests {
		if got := disposition.Display(); got != want {
			t.Errorf("%q.Display() = %q, want %q", disposition, got, want)
		}
	}
}
