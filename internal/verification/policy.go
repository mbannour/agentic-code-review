package verification

import "github.com/your-company/agentic-code-review/internal/findings"

// ReasonNotRequiredLowSeverity is recorded for a finding policy did not call for verifying.
const ReasonNotRequiredLowSeverity = "not verified: low severity is summary-only"

// RequiresVerification reports whether a finding is worth a verification invocation.
//
// The rule follows publication: a finding that can never become an inline comment does
// not need a second opinion to stay out of the diff. Low severity is summary-only under
// the publication policy, so verifying it would spend a model invocation to reach a
// conclusion nothing acts on. Blocker, high, and medium can all reach a line, so all
// three are checked.
//
// This deliberately mirrors — rather than imports — the publication policy's severity
// rule. The two are separate decisions that happen to agree today, and coupling them
// would make a change to one silently change the other.
func RequiresVerification(finding findings.Finding) bool {
	switch finding.Severity {
	case findings.SeverityBlocker, findings.SeverityHigh, findings.SeverityMedium:
		return true
	default:
		return false
	}
}

// Note on where publication is decided.
//
// This file deliberately stops at "does this finding need a second opinion". Whether a verified
// finding is published, and where, is decided in internal/publish by one policy that weighs the
// verdict together with severity, category, evidence, confidence, and diff mappability. Keeping
// a second, partial version of that decision here would mean two places could disagree about
// what reaches a reader.
