// Package verification is the adversarial second opinion on a candidate finding.
//
// The reviewer looks for defects; this stage tries to disprove what it found. The two
// have deliberately opposite objectives, because a model asked to confirm its own work
// will confirm it. Nothing here searches for new problems, and nothing here may alter a
// finding: a verdict is recorded alongside the original, never folded into it, so the
// reviewer's output stays auditable after the fact.
//
// Everything in this package is deterministic. It decodes a verdict, bounds it,
// validates it, and decides what may proceed — the model invocation itself lives in
// internal/claude, and publication remains entirely Step 14's business.
package verification

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/your-company/agentic-code-review/internal/findings"
)

// Verdict is the closed set of conclusions a verification may reach.
type Verdict string

const (
	// VerdictValid means the evidence supports the claimed failure mode and no
	// concrete reason to reject it was found.
	VerdictValid Verdict = "valid"

	// VerdictInvalid means the evidence contradicts the finding.
	VerdictInvalid Verdict = "invalid"

	// VerdictUncertain means the available evidence establishes neither. It is a
	// first-class answer, not a failure: pretending to certainty is what produces
	// false positives.
	VerdictUncertain Verdict = "uncertain"
)

// Verdicts lists every allowed verdict, in a stable order.
func Verdicts() []Verdict { return []Verdict{VerdictValid, VerdictInvalid, VerdictUncertain} }

// Valid reports whether v is an allowed verdict.
func (v Verdict) Valid() bool {
	for _, allowed := range Verdicts() {
		if v == allowed {
			return true
		}
	}
	return false
}

// Display renders the verdict for terminal output.
func (v Verdict) Display() string { return strings.ToUpper(string(v)) }

// EvidenceType is the closed set of things a verification may cite.
//
// It mirrors the reviewer's set but is its own type on purpose: the two stages cite
// evidence for opposite reasons, and neither should be able to change what the other
// accepts by accident.
type EvidenceType string

const (
	EvidenceCode     EvidenceType = "code"
	EvidenceJira     EvidenceType = "jira"
	EvidenceRule     EvidenceType = "rule"
	EvidenceDocument EvidenceType = "document"
	EvidenceSchema   EvidenceType = "schema"
	EvidenceTest     EvidenceType = "test"
	EvidenceVet      EvidenceType = "vet"
)

// EvidenceTypes lists every allowed evidence type.
func EvidenceTypes() []EvidenceType {
	return []EvidenceType{
		EvidenceCode, EvidenceJira, EvidenceRule, EvidenceTest, EvidenceVet,
		EvidenceDocument, EvidenceSchema,
	}
}

// Valid reports whether t is an allowed evidence type.
func (t EvidenceType) Valid() bool {
	for _, allowed := range EvidenceTypes() {
		if t == allowed {
			return true
		}
	}
	return false
}

// Display renders the evidence type for terminal output.
func (t EvidenceType) Display() string { return strings.ToUpper(string(t)) }

// Evidence is one thing the verification examined, and what it showed.
type Evidence struct {
	Type   EvidenceType `json:"type"`
	Source string       `json:"source"`
	Detail string       `json:"detail"`
}

// Result is one completed verification of one finding.
//
// Confidence is the verifier's raw ordinal input about its verdict, not a probability and
// not confidence in the finding. A strong invalid verdict on a finding the reviewer also
// strongly supported is the case this whole stage exists to catch.
type Result struct {
	FindingID  string  `json:"finding_id"`
	Verdict    Verdict `json:"verdict"`
	Confidence float64 `json:"confidence"`

	Reason string `json:"reason"`

	SupportingEvidence    []Evidence `json:"supporting_evidence"`
	ContradictingEvidence []Evidence `json:"contradicting_evidence"`
}

// EvidenceStrength returns the coarse evidence band used by policy and output.
func (r Result) EvidenceStrength() findings.EvidenceStrength {
	return findings.EvidenceStrengthFromScore(r.Confidence)
}

// Status is what happened to a candidate finding at this stage.
type Status string

const (
	// StatusVerified means the verifier ran and returned a valid result.
	StatusVerified Status = "verified"

	// StatusNotRequired means policy did not call for verification. It is not a
	// failure and must not suppress anything: a low-severity finding was never going
	// to become an inline comment, so spending a model invocation on it buys nothing.
	StatusNotRequired Status = "not_required"

	// StatusFailed means verification could not be completed — a timeout, malformed
	// output, a transport error, a cancelled context. It is deliberately distinct
	// from an invalid verdict, and it is treated as fail-closed for publication.
	StatusFailed Status = "failed"
)

// Display renders the status for terminal output.
func (s Status) Display() string {
	return strings.ToUpper(strings.ReplaceAll(string(s), "_", " "))
}

// VerifiedFinding pairs a finding with its verification.
//
// The finding is a copy of the reviewer's original and is never modified here. If the
// verifier disagrees about severity, the line, or the wording, that disagreement lives
// in the Result — rewriting the finding would destroy the record of what was actually
// proposed.
type VerifiedFinding struct {
	Finding      findings.Finding
	Verification Result
}

// Outcome is the full record of one candidate at this stage: what was proposed, what
// happened, and why.
type Outcome struct {
	Finding findings.Finding

	Status Status

	// Result is populated only when Status is StatusVerified.
	Result Result

	// SkipReason explains a StatusNotRequired outcome.
	SkipReason string

	// FailureReason explains a StatusFailed outcome. It carries no credential and no
	// raw model output.
	FailureReason string

	// ContextBytes is the size of the verification context that was built for this
	// finding, for cost reporting.
	ContextBytes int
}

// Verified reports whether a verdict is available.
func (o Outcome) Verified() bool { return o.Status == StatusVerified }

// Verdict returns the verdict, or the empty verdict when none was reached.
func (o Outcome) Verdict() Verdict {
	if o.Status != StatusVerified {
		return ""
	}
	return o.Result.Verdict
}

// VerifiedFinding pairs the finding with its verification result.
func (o Outcome) VerifiedFinding() VerifiedFinding {
	return VerifiedFinding{Finding: o.Finding, Verification: o.Result}
}

// Stats is the lightweight tally of what this stage did.
type Stats struct {
	Candidates int
	Verified   int
	Valid      int
	Invalid    int
	Uncertain  int
	Skipped    int
	Failed     int

	// ContextBytes is the total verification context built across all findings. It is
	// the honest measure of what this stage cost in input.
	ContextBytes int
}

// Report is the outcome of verifying one review result.
//
// Outcomes are in the same order as the findings they came from, whatever order the
// concurrent invocations happened to finish in.
type Report struct {
	Outcomes []Outcome
	Stats    Stats
}

// Empty reports whether nothing was considered.
func (r Report) Empty() bool { return len(r.Outcomes) == 0 }

// Outcome returns the outcome for a finding ID.
func (r Report) Outcome(findingID string) (Outcome, bool) {
	for _, outcome := range r.Outcomes {
		if outcome.Finding.ID == findingID {
			return outcome, true
		}
	}
	return Outcome{}, false
}

// Sentinel errors callers can match with errors.Is.
var (
	// ErrEmptyResponse means there was nothing to decode.
	ErrEmptyResponse = errors.New("empty verification response")

	// ErrNotJSON means the response did not begin with a JSON object.
	ErrNotJSON = errors.New("verification response is not a JSON object")

	// ErrMalformedJSON means the JSON could not be decoded.
	ErrMalformedJSON = errors.New("malformed verification JSON")

	// ErrUnknownField means the response carried a field this model does not define.
	ErrUnknownField = errors.New("unknown field in verification JSON")

	// ErrTrailingContent means something followed the JSON object.
	ErrTrailingContent = errors.New("unexpected content after verification JSON")
)

// Decode parses a verification response strictly.
//
// The strictness is the same as the reviewer's, and for the same reason: an unknown
// field or a stray sentence means the output drifted from what this code understands,
// and a verdict that is only mostly understood is not a verdict worth acting on.
func Decode(raw string) (Result, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Result{}, ErrEmptyResponse
	}
	if trimmed[0] != '{' {
		return Result{}, fmt.Errorf("%w: expected the response to start with '{'", ErrNotJSON)
	}

	reader := strings.NewReader(trimmed)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()

	var result Result
	if err := decoder.Decode(&result); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return Result{}, fmt.Errorf("%w: %v", ErrUnknownField, err)
		}
		return Result{}, fmt.Errorf("%w: %v", ErrMalformedJSON, err)
	}

	rest, err := io.ReadAll(io.MultiReader(decoder.Buffered(), reader))
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrMalformedJSON, err)
	}
	if leftover := strings.TrimSpace(string(rest)); leftover != "" {
		return Result{}, fmt.Errorf("%w: %s", ErrTrailingContent, firstLine(leftover))
	}

	return result, nil
}

// firstLine returns a short, single-line fragment of s for an error message.
func firstLine(s string) string {
	const maxLen = 120

	line := s
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	line = strings.TrimSpace(line)
	if len(line) > maxLen {
		line = line[:maxLen] + "…"
	}
	return line
}
