package publish

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/your-company/agentic-code-review/internal/findings"
)

// Default publication thresholds and limits.
//
// They live here, together, because they are the whole of this tool's publishing
// authority. Scattered through the decision code they would be impossible to review as a
// policy; gathered here they can be read in one sitting and argued about as numbers.
//
// Nothing outside this package's code may change them. Repository rules, the pull request
// body, a Jira ticket, and both model invocations all influence what the review says —
// none of them influences what is allowed to be published.
const (
	// Reviewer-confidence gates, by severity. A more serious claim is allowed a slightly
	// weaker case, because the cost of missing a blocker exceeds the cost of a wrong
	// medium.
	DefaultBlockerReviewerConfidence = 0.80
	DefaultHighReviewerConfidence    = 0.80
	DefaultMediumReviewerConfidence  = 0.85

	// Verifier-confidence gates, by severity. They mirror the reviewer's and are checked
	// independently: the two numbers measure different things and must never be averaged.
	DefaultBlockerVerifierConfidence = 0.80
	DefaultHighVerifierConfidence    = 0.80
	DefaultMediumVerifierConfidence  = 0.85

	// DefaultLowSummaryConfidence is what a low-severity finding needs to be worth
	// mentioning at all. Low findings are never attached to a line.
	DefaultLowSummaryConfidence = 0.80

	// DefaultSecurityVerifierConfidence is the stricter bar a security finding must clear
	// to reach a line. A wrong security comment is expensive out of proportion to its
	// severity: it alarms people, and it teaches them to distrust the next one.
	DefaultSecurityVerifierConfidence = 0.90

	// DefaultMaxInlineComments bounds how many lines one review may annotate.
	DefaultMaxInlineComments = 10

	// DefaultMaxSummaryFindings bounds the review body, so a long tail of findings cannot
	// turn it into a document nobody reads.
	DefaultMaxSummaryFindings = 10

	// DefaultMaxPublishedFindings bounds inline and body together. It is the total noise
	// this tool may add to one pull request.
	DefaultMaxPublishedFindings = 20
)

// MaxInlineComments is retained as the documented ceiling on inline comments.
const MaxInlineComments = DefaultMaxInlineComments

// Config is the publication policy's thresholds and limits.
//
// A Config is validated before use, and an invalid one is refused rather than repaired:
// a policy that silently fell back to defaults would publish under rules nobody chose.
type Config struct {
	BlockerReviewerConfidence float64
	BlockerVerifierConfidence float64

	HighReviewerConfidence float64
	HighVerifierConfidence float64

	MediumReviewerConfidence float64
	MediumVerifierConfidence float64

	LowSummaryConfidence float64

	// SecurityVerifierConfidence is an additional inline-only gate for security findings.
	// A security finding that clears its severity thresholds but not this one is still
	// reported — in the review body, where being wrong costs far less.
	SecurityVerifierConfidence float64

	MaxInlineComments    int
	MaxSummaryFindings   int
	MaxPublishedFindings int

	// ArchitectureMediumInline allows medium architecture findings onto a line. It is off
	// by default: an architectural argument rarely belongs on one line of a diff, and it
	// reads as a demand rather than an observation when it lands there.
	ArchitectureMediumInline bool
}

// DefaultConfig returns the policy this tool ships with.
func DefaultConfig() Config {
	return Config{
		BlockerReviewerConfidence: DefaultBlockerReviewerConfidence,
		BlockerVerifierConfidence: DefaultBlockerVerifierConfidence,

		HighReviewerConfidence: DefaultHighReviewerConfidence,
		HighVerifierConfidence: DefaultHighVerifierConfidence,

		MediumReviewerConfidence: DefaultMediumReviewerConfidence,
		MediumVerifierConfidence: DefaultMediumVerifierConfidence,

		LowSummaryConfidence: DefaultLowSummaryConfidence,

		SecurityVerifierConfidence: DefaultSecurityVerifierConfidence,

		MaxInlineComments:    DefaultMaxInlineComments,
		MaxSummaryFindings:   DefaultMaxSummaryFindings,
		MaxPublishedFindings: DefaultMaxPublishedFindings,

		ArchitectureMediumInline: false,
	}
}

// ErrInvalidConfig is the sentinel every configuration failure wraps.
var ErrInvalidConfig = errors.New("invalid publication policy configuration")

// Validate reports every problem with the configuration.
//
// It is strict on purpose. A threshold outside 0..1 or a limit of zero is a programming
// mistake, and the safe response is to refuse to publish rather than to guess what was
// meant.
func (c Config) Validate() error {
	var problems []string

	thresholds := []struct {
		name  string
		value float64
	}{
		{"BlockerReviewerConfidence", c.BlockerReviewerConfidence},
		{"BlockerVerifierConfidence", c.BlockerVerifierConfidence},
		{"HighReviewerConfidence", c.HighReviewerConfidence},
		{"HighVerifierConfidence", c.HighVerifierConfidence},
		{"MediumReviewerConfidence", c.MediumReviewerConfidence},
		{"MediumVerifierConfidence", c.MediumVerifierConfidence},
		{"LowSummaryConfidence", c.LowSummaryConfidence},
		{"SecurityVerifierConfidence", c.SecurityVerifierConfidence},
	}
	for _, threshold := range thresholds {
		if threshold.value < 0 || threshold.value > 1 {
			problems = append(problems, fmt.Sprintf("%s: %s is outside 0.0..1.0",
				threshold.name, strconv.FormatFloat(threshold.value, 'g', -1, 64)))
		}
	}

	limits := []struct {
		name  string
		value int
	}{
		{"MaxInlineComments", c.MaxInlineComments},
		{"MaxSummaryFindings", c.MaxSummaryFindings},
		{"MaxPublishedFindings", c.MaxPublishedFindings},
	}
	for _, limit := range limits {
		if limit.value <= 0 {
			problems = append(problems, fmt.Sprintf("%s: %d must be greater than zero",
				limit.name, limit.value))
		}
	}

	// A total lower than either part would make one of the two limits unreachable, which
	// is a contradiction rather than a stricter policy.
	if c.MaxPublishedFindings > 0 && c.MaxInlineComments > 0 &&
		c.MaxPublishedFindings < c.MaxInlineComments {
		problems = append(problems, fmt.Sprintf(
			"MaxPublishedFindings: %d is below MaxInlineComments %d",
			c.MaxPublishedFindings, c.MaxInlineComments))
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %d problem(s): %s", ErrInvalidConfig, len(problems), joinLines(problems))
}

// ReviewerThreshold returns the reviewer-confidence gate for a severity.
func (c Config) ReviewerThreshold(severity findings.Severity) float64 {
	switch severity {
	case findings.SeverityBlocker:
		return c.BlockerReviewerConfidence
	case findings.SeverityHigh:
		return c.HighReviewerConfidence
	case findings.SeverityMedium:
		return c.MediumReviewerConfidence
	default:
		return c.LowSummaryConfidence
	}
}

// VerifierThreshold returns the verifier-confidence gate for a severity.
func (c Config) VerifierThreshold(severity findings.Severity) float64 {
	switch severity {
	case findings.SeverityBlocker:
		return c.BlockerVerifierConfidence
	case findings.SeverityHigh:
		return c.HighVerifierConfidence
	case findings.SeverityMedium:
		return c.MediumVerifierConfidence
	default:
		return c.MediumVerifierConfidence
	}
}

// joinLines renders problems as one indented list.
func joinLines(problems []string) string {
	out := ""
	for _, problem := range problems {
		out += "\n  - " + problem
	}
	return out
}
