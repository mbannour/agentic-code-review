package publish

import (
	"fmt"
	"strings"
)

// ReasonCode is the closed set of grounds for a publication decision.
//
// Every decision carries at least one. The point is not bookkeeping: a tool that comments
// on people's work has to be able to say exactly why it said something, and — more often
// — why it stayed quiet about something it found. A suppressed finding with no reason is
// indistinguishable from a bug.
type ReasonCode string

const (
	// ReasonVerifiedValid means adversarial verification upheld the finding.
	ReasonVerifiedValid ReasonCode = "verified_valid"

	// ReasonVerifierInvalid means verification contradicted the finding.
	ReasonVerifierInvalid ReasonCode = "verifier_invalid"

	// ReasonVerifierUncertain means verification could establish neither way.
	ReasonVerifierUncertain ReasonCode = "verifier_uncertain"

	// ReasonVerificationFailed means verification did not complete.
	ReasonVerificationFailed ReasonCode = "verification_failed"

	// ReasonVerificationMissing means verification was required and never performed.
	ReasonVerificationMissing ReasonCode = "verification_missing"

	// ReasonVerificationNotRequired means policy did not call for verification, which is
	// the case for low-severity findings.
	ReasonVerificationNotRequired ReasonCode = "verification_not_required"

	// ReasonLowEvidenceStrength means the reviewer's evidence band is below the gate.
	ReasonLowEvidenceStrength ReasonCode = "low_evidence_strength"

	// ReasonLowVerifierEvidenceStrength means the verifier's evidence band is below the
	// gate. It is separate from ReasonLowEvidenceStrength because the two assessments
	// measure different things.
	ReasonLowVerifierEvidenceStrength ReasonCode = "low_verifier_evidence_strength"

	// ReasonLowSeverity means the finding is low severity, which is never inline.
	ReasonLowSeverity ReasonCode = "low_severity"

	// ReasonNotDiffMappable means no valid GitHub diff location exists for the finding.
	ReasonNotDiffMappable ReasonCode = "not_diff_mappable"

	// ReasonCommentLimit means the inline quota was already full.
	ReasonCommentLimit ReasonCode = "comment_limit"

	// ReasonSummaryLimit means the review body's quota was already full.
	ReasonSummaryLimit ReasonCode = "summary_limit"

	// ReasonTotalLimit means the overall published-finding bound was reached.
	ReasonTotalLimit ReasonCode = "total_limit"

	// ReasonCategoryPolicy means this finding's category places it in the body rather than
	// on a line.
	ReasonCategoryPolicy ReasonCode = "category_policy"

	// ReasonEvidenceMissing means the finding lacks the evidence its category requires.
	ReasonEvidenceMissing ReasonCode = "evidence_missing"

	// ReasonRequirementEvidenceMissing means a requirement finding cites neither the ticket
	// nor a repository rule, so there is nothing to check the claimed requirement against.
	ReasonRequirementEvidenceMissing ReasonCode = "requirement_evidence_missing"

	// ReasonWithinPolicy means the finding met every gate that applies to it.
	ReasonWithinPolicy ReasonCode = "within_policy"
)

// Reason is one ground for a decision, with the specifics that make it checkable.
type Reason struct {
	Code   ReasonCode
	Detail string
}

// String renders the reason as "code: detail", or just the code when there is no detail.
func (r Reason) String() string {
	if r.Detail == "" {
		return string(r.Code)
	}
	return string(r.Code) + ": " + r.Detail
}

// reason builds a Reason with a formatted detail.
func reason(code ReasonCode, format string, args ...any) Reason {
	if format == "" {
		return Reason{Code: code}
	}
	return Reason{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// Reasons is an ordered list of grounds for one decision.
type Reasons []Reason

// Has reports whether the list contains the code.
func (rs Reasons) Has(code ReasonCode) bool {
	for _, r := range rs {
		if r.Code == code {
			return true
		}
	}
	return false
}

// Primary returns the first reason, which is the decisive one. Later reasons are context.
func (rs Reasons) Primary() Reason {
	if len(rs) == 0 {
		return Reason{}
	}
	return rs[0]
}

// String renders every reason on one line, for terminal output.
func (rs Reasons) String() string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		parts = append(parts, r.String())
	}
	return strings.Join(parts, "; ")
}
