package verification

import (
	"errors"
	"strings"
	"testing"
)

// resultFixture is a valid verification result for COR-001.
func resultFixture() Result {
	return Result{
		FindingID:  "COR-001",
		Verdict:    VerdictValid,
		Confidence: 0.96,
		Reason:     "The changed branch reaches RetryPayment and no surrounding guard prevents the call.",
		SupportingEvidence: []Evidence{
			{Type: EvidenceCode, Source: testFile + ":81-83", Detail: "The decline branch reaches RetryPayment."},
		},
	}
}

// TestValidateAcceptsEveryVerdict checks a well-formed result of each kind passes, and
// that neither evidence side is required.
func TestValidateAcceptsEveryVerdict(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Result)
	}{
		{name: "valid with supporting evidence", mutate: func(*Result) {}},
		{
			name: "invalid with contradicting evidence",
			mutate: func(r *Result) {
				r.Verdict = VerdictInvalid
				r.SupportingEvidence = nil
				r.ContradictingEvidence = []Evidence{
					{Type: EvidenceCode, Source: testOtherFile + ":11", Detail: "A guard returns first."},
				}
			},
		},
		{
			name: "uncertain with no evidence at all",
			mutate: func(r *Result) {
				r.Verdict = VerdictUncertain
				r.Confidence = 0.55
				r.SupportingEvidence = nil
				r.ContradictingEvidence = nil
			},
		},
		{
			name:   "confidence at the bounds",
			mutate: func(r *Result) { r.Confidence = 0 },
		},
		{
			name:   "confidence at the upper bound",
			mutate: func(r *Result) { r.Confidence = 1 },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resultFixture()
			tt.mutate(&result)

			if err := Validate(result, "COR-001"); err != nil {
				t.Errorf("Validate() = %v", err)
			}
		})
	}
}

// TestValidateRejections covers every rule a verification result has to satisfy.
func TestValidateRejections(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Result)
		wantMsg string
	}{
		{
			name:    "unknown verdict",
			mutate:  func(r *Result) { r.Verdict = "probably" },
			wantMsg: "verdict",
		},
		{
			name:    "empty verdict",
			mutate:  func(r *Result) { r.Verdict = "" },
			wantMsg: "verdict",
		},
		{
			name:    "confidence below zero",
			mutate:  func(r *Result) { r.Confidence = -0.1 },
			wantMsg: "below 0.0",
		},
		{
			name:    "confidence above one",
			mutate:  func(r *Result) { r.Confidence = 1.2 },
			wantMsg: "above 1.0",
		},
		{
			name:    "missing reason",
			mutate:  func(r *Result) { r.Reason = "  " },
			wantMsg: "reason: must not be empty",
		},
		{
			name:    "oversized reason",
			mutate:  func(r *Result) { r.Reason = strings.Repeat("r", MaxReasonChars+1) },
			wantMsg: "exceed the limit",
		},
		{
			name:    "missing finding id",
			mutate:  func(r *Result) { r.FindingID = "" },
			wantMsg: "finding_id: must not be empty",
		},
		{
			name:    "wrong finding id",
			mutate:  func(r *Result) { r.FindingID = "SEC-009" },
			wantMsg: "does not match the finding under verification",
		},
		{
			name:    "oversized finding id",
			mutate:  func(r *Result) { r.FindingID = strings.Repeat("I", MaxFindingIDChars+1) },
			wantMsg: "exceed the limit",
		},
		{
			name: "invalid evidence type",
			mutate: func(r *Result) {
				r.SupportingEvidence[0].Type = "hunch"
			},
			wantMsg: "supporting_evidence[0].type",
		},
		{
			name: "missing evidence source",
			mutate: func(r *Result) {
				r.SupportingEvidence[0].Source = " "
			},
			wantMsg: "supporting_evidence[0].source: must not be empty",
		},
		{
			name: "missing evidence detail",
			mutate: func(r *Result) {
				r.SupportingEvidence[0].Detail = ""
			},
			wantMsg: "supporting_evidence[0].detail: must not be empty",
		},
		{
			name: "oversized evidence source",
			mutate: func(r *Result) {
				r.SupportingEvidence[0].Source = strings.Repeat("s", MaxEvidenceSourceChars+1)
			},
			wantMsg: "supporting_evidence[0].source",
		},
		{
			name: "oversized evidence detail",
			mutate: func(r *Result) {
				r.SupportingEvidence[0].Detail = strings.Repeat("d", MaxEvidenceDetailChars+1)
			},
			wantMsg: "supporting_evidence[0].detail",
		},
		{
			name: "too much evidence on one side",
			mutate: func(r *Result) {
				r.ContradictingEvidence = nil
				for i := 0; i <= MaxEvidencePerSide; i++ {
					r.ContradictingEvidence = append(r.ContradictingEvidence,
						Evidence{Type: EvidenceCode, Source: "a.go:1", Detail: "d"})
				}
			},
			wantMsg: "contradicting_evidence: 9 items exceed the limit",
		},
		{
			name: "invalid type on the contradicting side",
			mutate: func(r *Result) {
				r.ContradictingEvidence = []Evidence{{Type: "vibes", Source: "a.go:1", Detail: "d"}}
			},
			wantMsg: "contradicting_evidence[0].type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resultFixture()
			tt.mutate(&result)

			err := Validate(result, "COR-001")
			if err == nil {
				t.Fatal("Validate() = nil, want a rejection")
			}
			if !errors.Is(err, ErrInvalidResult) {
				t.Errorf("errors.Is(err, ErrInvalidResult) = false; err = %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tt.wantMsg)
			}
		})
	}
}

// TestValidateWithoutExpectedID checks the identity check is skipped when the caller has
// no particular finding in mind, while the rest of the rules still apply.
func TestValidateWithoutExpectedID(t *testing.T) {
	result := resultFixture()
	result.FindingID = "ANY-001"

	if err := Validate(result, ""); err != nil {
		t.Errorf("Validate() = %v, want the identity check skipped", err)
	}

	result.Reason = ""
	if err := Validate(result, ""); err == nil {
		t.Error("Validate() = nil for an empty reason")
	}
}

// TestValidateReportsEveryProblem checks one pass surfaces all the drift rather than
// stopping at the first fault.
func TestValidateReportsEveryProblem(t *testing.T) {
	result := Result{
		FindingID:          "OTHER-001",
		Verdict:            "maybe",
		Confidence:         3,
		Reason:             "",
		SupportingEvidence: []Evidence{{Type: "guess"}},
	}

	err := Validate(result, "COR-001")
	if err == nil {
		t.Fatal("Validate() = nil")
	}

	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("errors.As(err, *ValidationError) = false; err = %v", err)
	}
	if len(validationErr.Problems) < 6 {
		t.Errorf("reported %d problems, want every one: %v", len(validationErr.Problems), validationErr.Problems)
	}
	if validationErr.FindingID != "OTHER-001" {
		t.Errorf("FindingID = %q, want the id the result claimed", validationErr.FindingID)
	}
}

// TestValidateIsDeterministic checks the same result always produces the same message.
func TestValidateIsDeterministic(t *testing.T) {
	result := Result{FindingID: "COR-001", Verdict: "nope", Confidence: -1}

	first := Validate(result, "COR-001").Error()
	for i := 0; i < 10; i++ {
		if again := Validate(result, "COR-001").Error(); again != first {
			t.Fatalf("run %d gave a different message:\n%s\n%s", i, again, first)
		}
	}
}
