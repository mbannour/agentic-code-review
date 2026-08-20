package publish

import (
	"errors"
	"fmt"

	"github.com/your-company/agentic-code-review/internal/findings"
)

// Default publication evidence-strength gates and limits.
//
// They live here, together, because they are the whole of this tool's publishing
// authority. Scattered through the decision code they would be impossible to review as a
// policy; gathered here they can be read in one sitting and argued about as bands.
//
// Nothing outside this package's code may change them. Repository rules, the pull request
// body, a Jira ticket, and both model invocations all influence what the review says —
// none of them influences what is allowed to be published.
const (
	// Reviewer evidence-strength gates, by severity. A more serious claim is allowed a
	// slightly weaker case, because the cost of missing a blocker exceeds the cost of a
	// wrong medium.
	DefaultBlockerReviewerStrength = findings.EvidenceStrengthMedium
	DefaultHighReviewerStrength    = findings.EvidenceStrengthMedium
	DefaultMediumReviewerStrength  = findings.EvidenceStrengthHigh

	// Verifier evidence-strength gates, by severity. They mirror the reviewer's and are
	// checked independently: the two ordinal assessments must never be averaged.
	DefaultBlockerVerifierStrength = findings.EvidenceStrengthMedium
	DefaultHighVerifierStrength    = findings.EvidenceStrengthMedium
	DefaultMediumVerifierStrength  = findings.EvidenceStrengthHigh

	// DefaultLowSummaryStrength is what a low-severity finding needs to be worth mentioning
	// at all. Low findings are never attached to a line.
	DefaultLowSummaryStrength = findings.EvidenceStrengthMedium

	// DefaultSecurityVerifierStrength is the stricter bar a security finding must clear
	// to reach a line. A wrong security comment is expensive out of proportion to its
	// severity: it alarms people, and it teaches them to distrust the next one.
	DefaultSecurityVerifierStrength = findings.EvidenceStrengthHigh

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

// Config is the publication policy's evidence-strength gates and limits.
//
// A Config is validated before use, and an invalid one is refused rather than repaired:
// a policy that silently fell back to defaults would publish under rules nobody chose.
type Config struct {
	BlockerReviewerStrength findings.EvidenceStrength
	BlockerVerifierStrength findings.EvidenceStrength

	HighReviewerStrength findings.EvidenceStrength
	HighVerifierStrength findings.EvidenceStrength

	MediumReviewerStrength findings.EvidenceStrength
	MediumVerifierStrength findings.EvidenceStrength

	LowSummaryStrength findings.EvidenceStrength

	// SecurityVerifierStrength is an additional inline-only gate for security findings.
	// A security finding that clears its severity thresholds but not this one is still
	// reported — in the review body, where being wrong costs far less.
	SecurityVerifierStrength findings.EvidenceStrength

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
		BlockerReviewerStrength: DefaultBlockerReviewerStrength,
		BlockerVerifierStrength: DefaultBlockerVerifierStrength,

		HighReviewerStrength: DefaultHighReviewerStrength,
		HighVerifierStrength: DefaultHighVerifierStrength,

		MediumReviewerStrength: DefaultMediumReviewerStrength,
		MediumVerifierStrength: DefaultMediumVerifierStrength,

		LowSummaryStrength: DefaultLowSummaryStrength,

		SecurityVerifierStrength: DefaultSecurityVerifierStrength,

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
// It is strict on purpose. An unknown strength or a limit of zero is a programming mistake,
// and the safe response is to refuse to publish rather than to guess what was meant.
func (c Config) Validate() error {
	var problems []string

	strengths := []struct {
		name  string
		value findings.EvidenceStrength
	}{
		{"BlockerReviewerStrength", c.BlockerReviewerStrength},
		{"BlockerVerifierStrength", c.BlockerVerifierStrength},
		{"HighReviewerStrength", c.HighReviewerStrength},
		{"HighVerifierStrength", c.HighVerifierStrength},
		{"MediumReviewerStrength", c.MediumReviewerStrength},
		{"MediumVerifierStrength", c.MediumVerifierStrength},
		{"LowSummaryStrength", c.LowSummaryStrength},
		{"SecurityVerifierStrength", c.SecurityVerifierStrength},
	}
	for _, strength := range strengths {
		if !strength.value.Valid() {
			problems = append(problems, fmt.Sprintf("%s: %q is not LOW, MEDIUM, or HIGH",
				strength.name, strength.value))
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

// ReviewerStrength returns the minimum reviewer evidence strength for a severity.
func (c Config) ReviewerStrength(severity findings.Severity) findings.EvidenceStrength {
	switch severity {
	case findings.SeverityBlocker:
		return c.BlockerReviewerStrength
	case findings.SeverityHigh:
		return c.HighReviewerStrength
	case findings.SeverityMedium:
		return c.MediumReviewerStrength
	default:
		return c.LowSummaryStrength
	}
}

// VerifierStrength returns the minimum verifier evidence strength for a severity.
func (c Config) VerifierStrength(severity findings.Severity) findings.EvidenceStrength {
	switch severity {
	case findings.SeverityBlocker:
		return c.BlockerVerifierStrength
	case findings.SeverityHigh:
		return c.HighVerifierStrength
	case findings.SeverityMedium:
		return c.MediumVerifierStrength
	default:
		return c.MediumVerifierStrength
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
