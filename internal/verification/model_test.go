package verification

import (
	"errors"
	"strings"
	"testing"
)

// validJSON is a well-formed verdict for COR-001.
const validJSON = `{
  "finding_id": "COR-001",
  "verdict": "valid",
  "confidence": 0.96,
  "reason": "The changed branch sends permanent declines into the retry path and no surrounding guard prevents the call.",
  "supporting_evidence": [
    {"type": "code", "source": "internal/payment/retry.go:84-87", "detail": "The permanent-decline branch reaches RetryPayment."}
  ],
  "contradicting_evidence": []
}`

// TestDecodeVerdicts covers the three answers the verifier may give.
func TestDecodeVerdicts(t *testing.T) {
	tests := []struct {
		name           string
		raw            string
		wantVerdict    Verdict
		wantConfidence float64
		wantSupporting int
		wantAgainst    int
	}{
		{
			name:           "valid",
			raw:            validJSON,
			wantVerdict:    VerdictValid,
			wantConfidence: 0.96,
			wantSupporting: 1,
		},
		{
			name: "invalid",
			raw: `{"finding_id":"MAINT-001","verdict":"invalid","confidence":0.93,
			       "reason":"The claimed duplicate parsing does not occur because the parsed value is memoized before both consumers access it.",
			       "supporting_evidence":[],
			       "contradicting_evidence":[{"type":"code","source":"RowSourceDocs.scala:31-44","detail":"Both access paths resolve through the same cached value."}]}`,
			wantVerdict:    VerdictInvalid,
			wantConfidence: 0.93,
			wantAgainst:    1,
		},
		{
			name: "uncertain",
			raw: `{"finding_id":"REQ-002","verdict":"uncertain","confidence":0.62,
			       "reason":"The Jira wording suggests this behavior but does not define the edge case, and the available context does not establish the intended behavior.",
			       "supporting_evidence":[],"contradicting_evidence":[]}`,
			wantVerdict:    VerdictUncertain,
			wantConfidence: 0.62,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Decode(tt.raw)
			if err != nil {
				t.Fatalf("Decode() = %v", err)
			}

			if result.Verdict != tt.wantVerdict {
				t.Errorf("Verdict = %q, want %q", result.Verdict, tt.wantVerdict)
			}
			if result.Confidence != tt.wantConfidence {
				t.Errorf("Confidence = %v, want %v", result.Confidence, tt.wantConfidence)
			}
			if len(result.SupportingEvidence) != tt.wantSupporting {
				t.Errorf("SupportingEvidence = %d items, want %d",
					len(result.SupportingEvidence), tt.wantSupporting)
			}
			if len(result.ContradictingEvidence) != tt.wantAgainst {
				t.Errorf("ContradictingEvidence = %d items, want %d",
					len(result.ContradictingEvidence), tt.wantAgainst)
			}
			if strings.TrimSpace(result.Reason) == "" {
				t.Error("Reason is empty")
			}
		})
	}
}

// TestDecodeRejections checks strict decoding: drift in what the model emits is an error
// rather than a quietly dropped field.
func TestDecodeRejections(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want error
	}{
		{name: "empty", raw: "   ", want: ErrEmptyResponse},
		{name: "prose", raw: "I checked the finding and it looks right.", want: ErrNotJSON},
		{
			name: "markdown fence",
			raw:  "```json\n" + validJSON + "\n```",
			want: ErrNotJSON,
		},
		{name: "not an object", raw: `["valid"]`, want: ErrNotJSON},
		{name: "malformed", raw: `{"finding_id":"COR-001","verdict":`, want: ErrMalformedJSON},
		{
			name: "wrong field type",
			raw:  `{"finding_id":"COR-001","verdict":"valid","confidence":"high","reason":"x"}`,
			want: ErrMalformedJSON,
		},
		{
			name: "unknown top-level field",
			raw:  `{"finding_id":"COR-001","verdict":"valid","confidence":0.9,"reason":"x","severity":"high"}`,
			want: ErrUnknownField,
		},
		{
			name: "unknown evidence field",
			raw: `{"finding_id":"COR-001","verdict":"valid","confidence":0.9,"reason":"x",
			       "supporting_evidence":[{"type":"code","source":"a.go:1","detail":"d","line":4}]}`,
			want: ErrUnknownField,
		},
		{
			name: "trailing prose",
			raw:  validJSON + "\n\nI hope that helps.",
			want: ErrTrailingContent,
		},
		{
			name: "trailing object",
			raw:  validJSON + "\n" + validJSON,
			want: ErrTrailingContent,
		},
		{
			name: "trailing fence",
			raw:  validJSON + "\n```",
			want: ErrTrailingContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Decode(tt.raw); !errors.Is(err, tt.want) {
				t.Errorf("Decode() = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestDecodeAcceptsSurroundingWhitespace checks only whitespace is tolerated around the
// object.
func TestDecodeAcceptsSurroundingWhitespace(t *testing.T) {
	if _, err := Decode("\n\t " + validJSON + "\n\n"); err != nil {
		t.Errorf("Decode() = %v, want whitespace to be tolerated", err)
	}
}

// TestDecodeDoesNotValidate checks the two stages stay separate: decoding is about shape,
// validation is about rules.
func TestDecodeDoesNotValidate(t *testing.T) {
	raw := `{"finding_id":"COR-001","verdict":"probably","confidence":9,"reason":""}`

	result, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode() = %v; decoding checks shape only", err)
	}
	if err := Validate(result, "COR-001"); err == nil {
		t.Error("Validate() = nil for a result that breaks every rule")
	}
}

// TestEnumsAreClosed pins the verdict and evidence sets.
func TestEnumsAreClosed(t *testing.T) {
	for _, verdict := range Verdicts() {
		if !verdict.Valid() {
			t.Errorf("declared verdict %q reports itself invalid", verdict)
		}
	}
	for _, unknown := range []Verdict{"", "probably", "VALID", "true", "confirmed"} {
		if unknown.Valid() {
			t.Errorf("undeclared verdict %q reports itself valid", unknown)
		}
	}

	for _, evidenceType := range EvidenceTypes() {
		if !evidenceType.Valid() {
			t.Errorf("declared evidence type %q reports itself invalid", evidenceType)
		}
	}
	for _, unknown := range []EvidenceType{"", "guess", "CODE", "opinion"} {
		if unknown.Valid() {
			t.Errorf("undeclared evidence type %q reports itself valid", unknown)
		}
	}
}

// TestDisplayHelpers checks the terminal renderings.
func TestDisplayHelpers(t *testing.T) {
	if got := VerdictUncertain.Display(); got != "UNCERTAIN" {
		t.Errorf("Verdict.Display() = %q, want UNCERTAIN", got)
	}
	if got := EvidenceCode.Display(); got != "CODE" {
		t.Errorf("EvidenceType.Display() = %q, want CODE", got)
	}
	if got := StatusNotRequired.Display(); got != "NOT REQUIRED" {
		t.Errorf("Status.Display() = %q, want NOT REQUIRED", got)
	}
	if got := (Result{Confidence: 0.965}).ConfidencePercent(); got != 97 {
		t.Errorf("ConfidencePercent() = %d, want 97", got)
	}
}

// TestOutcomeAccessors checks the helpers reporting on one candidate.
func TestOutcomeAccessors(t *testing.T) {
	verified := Outcome{
		Finding: findingFixture(),
		Status:  StatusVerified,
		Result:  Result{FindingID: "COR-001", Verdict: VerdictValid, Confidence: 0.9, Reason: "checked"},
	}
	if !verified.Verified() || verified.Verdict() != VerdictValid {
		t.Errorf("outcome = %+v, want a valid verdict", verified)
	}
	if vf := verified.VerifiedFinding(); vf.Finding.ID != "COR-001" || vf.Verification.Verdict != VerdictValid {
		t.Errorf("VerifiedFinding() = %+v, want the finding paired with its verdict", vf)
	}

	skipped := Outcome{Finding: findingFixture(), Status: StatusNotRequired}
	if skipped.Verified() || skipped.Verdict() != "" {
		t.Errorf("outcome = %+v, want no verdict", skipped)
	}
}

// TestReportLookup checks a report can be queried by finding ID.
func TestReportLookup(t *testing.T) {
	report := Report{Outcomes: []Outcome{
		{Finding: findingFixture(), Status: StatusVerified},
	}}

	if _, ok := report.Outcome("COR-001"); !ok {
		t.Error("Outcome(\"COR-001\") = not found")
	}
	if _, ok := report.Outcome("NOPE-001"); ok {
		t.Error("Outcome() found a finding that is not in the report")
	}
	if report.Empty() {
		t.Error("Empty() = true for a report with one outcome")
	}
}

// TestFindingIsNeverMutated is the immutability guarantee: a verdict is recorded beside
// the reviewer's output, never folded into it, so what was originally proposed stays
// auditable.
func TestFindingIsNeverMutated(t *testing.T) {
	original := findingFixture()

	outcome := Outcome{
		Finding: original,
		Status:  StatusVerified,
		Result: Result{
			FindingID:  original.ID,
			Verdict:    VerdictInvalid,
			Confidence: 0.95,
			Reason:     "the claimed behavior does not occur",
		},
	}

	pair := outcome.VerifiedFinding()

	if pair.Finding.Severity != original.Severity {
		t.Error("the finding's severity changed")
	}
	if pair.Finding.Confidence != original.Confidence {
		t.Error("the finding's confidence changed")
	}
	if pair.Finding.Title != original.Title {
		t.Error("the finding's title changed")
	}
	if pair.Finding.StartLine != original.StartLine || pair.Finding.EndLine != original.EndLine {
		t.Error("the finding's line range changed")
	}
	if len(pair.Finding.Evidence) != len(original.Evidence) {
		t.Error("the finding's evidence changed")
	}
}
