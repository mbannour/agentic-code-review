package verification

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Limits bound a verification result. They exist for the same reason the reviewer's do:
// a malfunctioning or manipulated model must not be able to flood the terminal or the
// process, and every stage should agree on the same ceilings.
const (
	MaxReasonChars = 2000

	MaxEvidencePerSide     = 8
	MaxEvidenceSourceChars = 500
	MaxEvidenceDetailChars = 1500

	MaxFindingIDChars = 64
)

// ErrInvalidResult is the sentinel every validation failure wraps.
var ErrInvalidResult = errors.New("invalid verification result")

// ValidationError reports every rule a result broke.
type ValidationError struct {
	FindingID string
	Problems  []string
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 0 {
		return ErrInvalidResult.Error()
	}

	subject := "verification"
	if e.FindingID != "" {
		subject = "verification of " + e.FindingID
	}

	return fmt.Sprintf("%s: %s: %d problem(s):\n  - %s",
		ErrInvalidResult.Error(), subject, len(e.Problems), strings.Join(e.Problems, "\n  - "))
}

// Unwrap lets callers match ErrInvalidResult.
func (e *ValidationError) Unwrap() error { return ErrInvalidResult }

// Validator checks a decoded verification result against the rules.
//
// It is a value with no state: the same result and the same expected finding always
// produce the same verdict on the verdict.
type Validator struct{}

// NewValidator returns a Validator.
func NewValidator() Validator { return Validator{} }

// Validate checks result and confirms it answers about expectedFindingID.
//
// The identity check is not a formality. A result for a different finding would attach
// one finding's verdict to another, which is worse than no verification at all, so it is
// rejected rather than reconciled.
func Validate(result Result, expectedFindingID string) error {
	return NewValidator().Validate(result, expectedFindingID)
}

// Validate reports every rule violation in result.
func (v Validator) Validate(result Result, expectedFindingID string) error {
	var problems []string

	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	id := strings.TrimSpace(result.FindingID)
	expected := strings.TrimSpace(expectedFindingID)

	switch {
	case id == "":
		add("finding_id: must not be empty")
	case utf8.RuneCountInString(id) > MaxFindingIDChars:
		add("finding_id: %d characters exceed the limit of %d",
			utf8.RuneCountInString(id), MaxFindingIDChars)
	case expected != "" && id != expected:
		add("finding_id: %q does not match the finding under verification (%q)", id, expected)
	}

	if !result.Verdict.Valid() {
		add("verdict: %q is not one of %s", string(result.Verdict), joinVerdicts())
	}

	// Confidence here is confidence in the verdict, and is bounded exactly as the
	// reviewer's is.
	switch {
	case result.Confidence < 0:
		add("confidence: %s is below 0.0", formatFloat(result.Confidence))
	case result.Confidence > 1:
		add("confidence: %s is above 1.0", formatFloat(result.Confidence))
	}

	if strings.TrimSpace(result.Reason) == "" {
		add("reason: must not be empty")
	} else if n := utf8.RuneCountInString(result.Reason); n > MaxReasonChars {
		add("reason: %d characters exceed the limit of %d", n, MaxReasonChars)
	}

	problems = append(problems, validateEvidence("supporting_evidence", result.SupportingEvidence)...)
	problems = append(problems, validateEvidence("contradicting_evidence", result.ContradictingEvidence)...)

	if len(problems) == 0 {
		return nil
	}
	return &ValidationError{FindingID: id, Problems: problems}
}

// validateEvidence checks one side's evidence list.
//
// Neither side is required to be populated. An invalid verdict is expected to carry
// something contradicting, and a valid one something supporting, but requiring it would
// only teach the model to invent citations.
func validateEvidence(field string, evidence []Evidence) []string {
	var problems []string

	if len(evidence) > MaxEvidencePerSide {
		problems = append(problems, fmt.Sprintf("%s: %d items exceed the limit of %d",
			field, len(evidence), MaxEvidencePerSide))
	}

	for i, item := range evidence {
		where := field + "[" + strconv.Itoa(i) + "]"

		if !item.Type.Valid() {
			problems = append(problems, fmt.Sprintf("%s.type: %q is not one of %s",
				where, string(item.Type), joinEvidenceTypes()))
		}
		if strings.TrimSpace(item.Source) == "" {
			problems = append(problems, where+".source: must not be empty")
		} else if n := utf8.RuneCountInString(item.Source); n > MaxEvidenceSourceChars {
			problems = append(problems, fmt.Sprintf("%s.source: %d characters exceed the limit of %d",
				where, n, MaxEvidenceSourceChars))
		}
		if strings.TrimSpace(item.Detail) == "" {
			problems = append(problems, where+".detail: must not be empty")
		} else if n := utf8.RuneCountInString(item.Detail); n > MaxEvidenceDetailChars {
			problems = append(problems, fmt.Sprintf("%s.detail: %d characters exceed the limit of %d",
				where, n, MaxEvidenceDetailChars))
		}
	}

	return problems
}

// formatFloat renders a confidence value without exponent noise.
func formatFloat(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func joinVerdicts() string {
	parts := make([]string, 0, len(Verdicts()))
	for _, v := range Verdicts() {
		parts = append(parts, string(v))
	}
	return strings.Join(parts, ", ")
}

func joinEvidenceTypes() string {
	parts := make([]string, 0, len(EvidenceTypes()))
	for _, t := range EvidenceTypes() {
		parts = append(parts, string(t))
	}
	return strings.Join(parts, ", ")
}
