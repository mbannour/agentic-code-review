package contextselect

import (
	"fmt"
	"strings"
)

// Default byte budgets. The total is the ceiling on everything the selector
// emits; the rest are caps on individual sections so no single kind of content
// can crowd out the changed code.
const (
	DefaultContextBudgetBytes  = 200 * 1024
	DefaultRulesBudgetBytes    = 30 * 1024
	DefaultEvidenceBudgetBytes = 40 * 1024
	DefaultAnalysisBudgetBytes = 30 * 1024

	// DefaultDiscussionBudgetBytes caps the pull request description and the human
	// comments together. The conversation is context about intent, and a long
	// argument is not more intent than a short one.
	DefaultDiscussionBudgetBytes = 16 * 1024

	// DefaultRetrievalBudgetBytes caps the unchanged code retrieved for context.
	// It is spent only after the changed patches have taken what they need:
	// context about the change must never crowd out the change itself.
	DefaultRetrievalBudgetBytes = 32 * 1024

	// DefaultTicketBudgetBytes caps the Jira description. The key and summary are
	// never subject to it.
	DefaultTicketBudgetBytes = 16 * 1024

	// DefaultPerCheckOutputBytes caps one failing check's snippet.
	DefaultPerCheckOutputBytes = 8 * 1024

	// minimumPatchBytes is the smallest useful patch fragment. If less than this
	// remains, a file is dropped rather than reduced to a stub.
	minimumPatchBytes = 512
)

// Truncation markers. Each states plainly what was cut, so no consumer has to
// guess whether content is complete.
const (
	MarkerPatch             = "[TRUNCATED: patch exceeded context budget]"
	MarkerRule              = "[TRUNCATED: rule document exceeded context budget]"
	MarkerEvidence          = "[TRUNCATED: external evidence exceeded context budget]"
	MarkerAnalysis          = "[TRUNCATED: check output exceeded context budget]"
	MarkerRetrieval         = "[TRUNCATED: retrieved code exceeded context budget]"
	MarkerDescription       = "[TRUNCATED: pull request description exceeded context budget]"
	MarkerDiscussion        = "[TRUNCATED: discussion exceeded context budget]"
	MarkerTicketDescription = "[TRUNCATED: Jira description exceeded context limit]"
)

// Risk-tiered context ceilings.
//
// Context is the dominant cost of a review: everything selected is sent to a model,
// and a token spent on a low-risk change is a token not available for a high-risk
// one. So the ceiling follows the assessed risk rather than being constant.
//
// The tiers are generous rather than tight. Cutting context is the one optimization
// that can silently make a review worse — a dropped patch is a defect nobody looks
// for — so the low tier is still large enough to hold an ordinary change whole, and
// the high tier is the full allowance.
const (
	LowRiskContextBudgetBytes    = 96 * 1024
	MediumRiskContextBudgetBytes = 144 * 1024
	HighRiskContextBudgetBytes   = DefaultContextBudgetBytes
)

// BudgetForRiskLevel returns the allowances for an assessed risk band.
//
// The band arrives as the plain string the risk stage produced; an unrecognized
// value gets the full budget, because failing to recognize a risk band is not a
// reason to review a change with less context.
func BudgetForRiskLevel(level string) Budget {
	switch level {
	case "minimal", "low":
		return DefaultBudget().ScaledTo(LowRiskContextBudgetBytes)
	case "medium":
		return DefaultBudget().ScaledTo(MediumRiskContextBudgetBytes)
	default:
		return DefaultBudget()
	}
}

// ScaledTo returns the budget with every allowance scaled to a new total.
//
// Sub-allowances are scaled proportionally rather than clamped, because clamping
// would leave the fixed sections at full size and take the whole reduction out of
// the patches — starving the change itself, which is the one thing a review cannot
// do without. Each floor keeps a section useful rather than vestigial.
func (b Budget) ScaledTo(total int) Budget {
	base := b.normalized()
	if total <= 0 || total >= base.Total {
		return base
	}

	scale := func(value, floor int) int {
		scaled := value * total / base.Total
		if scaled < floor {
			return floor
		}
		return scaled
	}

	return Budget{
		Total:             total,
		Rules:             scale(base.Rules, 8*1024),
		Analysis:          scale(base.Analysis, 8*1024),
		Evidence:          scale(base.Evidence, 8*1024),
		Retrieval:         scale(base.Retrieval, 4*1024),
		Discussion:        scale(base.Discussion, 4*1024),
		TicketDescription: scale(base.TicketDescription, 4*1024),
		PerCheckOutput:    scale(base.PerCheckOutput, 2*1024),
	}.normalized()
}

// EstimatedTokens is a rough token count for a byte size.
//
// Four bytes per token is the usual approximation for source code and English
// prose. It is stated as an estimate and used only for reporting: nothing decides
// anything from it, so being approximate costs nothing.
func EstimatedTokens(bytes int) int { return bytes / 4 }

// Budget holds the byte allowances for one selection.
type Budget struct {
	// Total is the ceiling on all selected content.
	Total int

	// Rules caps the combined repository rule documents.
	Rules int

	// Analysis caps the combined check output.
	Analysis int

	// Evidence caps the combined external evidence documents.
	Evidence int

	// Retrieval caps the combined unchanged-code snippets.
	Retrieval int

	// Discussion caps the description and comments together.
	Discussion int

	// TicketDescription caps the Jira description.
	TicketDescription int

	// PerCheckOutput caps one check's snippet.
	PerCheckOutput int
}

// DefaultBudget returns the standard allowances.
func DefaultBudget() Budget {
	return Budget{
		Total:             DefaultContextBudgetBytes,
		Rules:             DefaultRulesBudgetBytes,
		Evidence:          DefaultEvidenceBudgetBytes,
		Retrieval:         DefaultRetrievalBudgetBytes,
		Discussion:        DefaultDiscussionBudgetBytes,
		Analysis:          DefaultAnalysisBudgetBytes,
		TicketDescription: DefaultTicketBudgetBytes,
		PerCheckOutput:    DefaultPerCheckOutputBytes,
	}
}

// normalized fills in any unset allowance from the defaults and keeps the section
// caps inside the total.
func (b Budget) normalized() Budget {
	defaults := DefaultBudget()

	if b.Total <= 0 {
		b.Total = defaults.Total
	}
	if b.Rules <= 0 {
		b.Rules = defaults.Rules
	}
	if b.Analysis <= 0 {
		b.Analysis = defaults.Analysis
	}
	if b.Evidence <= 0 {
		b.Evidence = defaults.Evidence
	}
	if b.Retrieval <= 0 {
		b.Retrieval = defaults.Retrieval
	}
	if b.Discussion <= 0 {
		b.Discussion = defaults.Discussion
	}
	if b.TicketDescription <= 0 {
		b.TicketDescription = defaults.TicketDescription
	}
	if b.PerCheckOutput <= 0 {
		b.PerCheckOutput = defaults.PerCheckOutput
	}

	// A section may never claim more than the whole budget.
	if b.Rules > b.Total {
		b.Rules = b.Total
	}
	if b.Analysis > b.Total {
		b.Analysis = b.Total
	}
	if b.Evidence > b.Total {
		b.Evidence = b.Total
	}
	if b.Retrieval > b.Total {
		b.Retrieval = b.Total
	}
	if b.Discussion > b.Total {
		b.Discussion = b.Total
	}
	if b.TicketDescription > b.Total {
		b.TicketDescription = b.Total
	}
	if b.PerCheckOutput > b.Analysis {
		b.PerCheckOutput = b.Analysis
	}

	return b
}

// truncateTo cuts content to limit bytes and appends marker. It reports whether
// it had to cut. The cut is positional, so the same input always yields the same
// output.
func truncateTo(content string, limit int, marker string) (string, bool) {
	if limit <= 0 {
		if content == "" {
			return "", false
		}
		return marker, true
	}
	if len(content) <= limit {
		return content, false
	}

	cut := trimToValidUTF8(content[:limit])
	return cut + "\n" + marker, true
}

// trimToValidUTF8 backs off the tail until the string is valid UTF-8, so a cut
// never splits a rune.
func trimToValidUTF8(s string) string {
	for len(s) > 0 && !validUTF8(s) {
		s = s[:len(s)-1]
	}
	return s
}

func validUTF8(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}

// truncateLines keeps lines from both ends of content, noting how many were
// dropped. Check output often has the first failure near the start and the test
// summary at the end.
func truncateLines(content string, maxLines int) (string, bool) {
	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines {
		return content, false
	}

	headLines := (maxLines + 1) / 2
	tailLines := maxLines - headLines
	omitted := len(lines) - maxLines
	kept := append([]string{}, lines[:headLines]...)
	kept = append(kept, fmt.Sprintf("... %d lines omitted", omitted))
	if tailLines > 0 {
		kept = append(kept, lines[len(lines)-tailLines:]...)
	}
	return strings.Join(kept, "\n"), true
}
