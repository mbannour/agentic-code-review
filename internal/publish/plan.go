package publish

import (
	"github.com/your-company/agentic-code-review/internal/findings"
)

// InlineFinding is one finding that will become an inline review comment, paired
// with the diff location it was mapped to.
type InlineFinding struct {
	Finding  findings.Finding
	Location DiffLocation
}

// SuppressedFinding is one finding that will not be published at all, with the
// reason it was withheld.
//
// The MVP policy suppresses nothing: every validated finding reaches the reader
// either on a line or in the review body. The category exists so a future policy can
// withhold a finding explicitly and visibly, rather than by quietly dropping it
// somewhere in the pipeline.
type SuppressedFinding struct {
	Finding findings.Finding
	Reason  string
}

// Plan is the complete, inspectable decision about one review result.
//
// It is built before any network call, which is what makes a dry run meaningful: the
// plan a developer sees without --publish is exactly the plan that would be sent.
// Findings are partitioned, never duplicated and never dropped — every finding in the
// result appears in exactly one of the three lists.
type Plan struct {
	// Inline findings become line comments on the diff.
	Inline []InlineFinding

	// Summary findings are reported in the review body, either because policy keeps
	// them off the diff or because no valid diff location exists for them.
	Summary []findings.Finding

	// Suppressed findings are published nowhere.
	Suppressed []SuppressedFinding

	// Decisions is the full policy record for every candidate, in input order. It is what
	// makes the plan explainable: each entry says what was decided and why.
	Decisions []Decision

	// Stats tallies the dispositions and the grounds for each suppression.
	Stats Stats

	// ResultSummary is the summary line the model wrote for the whole review.
	ResultSummary string

	// HeadSHA is the commit the findings were derived from, and the commit any
	// published review will be pinned to.
	HeadSHA string

	// Delta compares this plan with the previous ARC review of the same pull
	// request. Its zero value means no previous review was found, which is not the
	// same as a previous review that found nothing.
	Delta Delta
}

// TotalFindings is how many findings the plan accounts for.
func (p Plan) TotalFindings() int {
	return len(p.Inline) + len(p.Summary) + len(p.Suppressed)
}

// Empty reports whether the plan accounts for no findings at all.
func (p Plan) Empty() bool { return p.TotalFindings() == 0 }

// PublishableCount is how many findings will actually reach GitHub.
func (p Plan) PublishableCount() int { return len(p.Inline) + len(p.Summary) }

// NothingToPublish reports whether a review would carry no findings.
//
// This is the question publication asks, and it is deliberately not Empty(): a plan can
// account for several findings and still have nothing to say, because policy suppressed
// every one of them. Publishing then would post a review whose body reads "0 findings",
// which is a notification with nothing in it — worse than silence, since it trains people
// to ignore the next one.
//
// A plan with summary findings and no inline comments is not in this state: those findings
// had nowhere to sit on the diff, not nothing to say.
func (p Plan) NothingToPublish() bool { return p.PublishableCount() == 0 }

// PublishableFindings returns every finding the plan will publish, inline first.
func (p Plan) PublishableFindings() []findings.Finding {
	out := make([]findings.Finding, 0, len(p.Inline)+len(p.Summary))
	for _, item := range p.Inline {
		out = append(out, item.Finding)
	}
	out = append(out, p.Summary...)
	return out
}

// CountBySeverity returns how many publishable findings carry the given severity.
func (p Plan) CountBySeverity(severity findings.Severity) int {
	count := 0
	for _, f := range p.PublishableFindings() {
		if f.Severity == severity {
			count++
		}
	}
	return count
}
