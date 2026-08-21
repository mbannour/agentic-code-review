package publish

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/your-company/agentic-code-review/internal/findings"
	"github.com/your-company/agentic-code-review/internal/github"
)

// DismissalKind is a human verdict on a published finding.
//
// The vocabulary is small and closed on purpose. A free-text reply is an
// argument, and weighing arguments is the reviewer's job; a dismissal is an
// instruction, and an instruction has to be unambiguous before deterministic code
// acts on it.
type DismissalKind string

const (
	// DismissalFalsePositive means the finding is wrong.
	DismissalFalsePositive DismissalKind = "false-positive"

	// DismissalWontFix means the finding is right and the team is not acting on
	// it. It stops the comment recurring without pretending the problem is gone.
	DismissalWontFix DismissalKind = "wont-fix"
)

// DismissalKinds lists the recognized verdicts in a stable order.
func DismissalKinds() []DismissalKind {
	return []DismissalKind{DismissalFalsePositive, DismissalWontFix}
}

// Display renders the verdict for output.
func (k DismissalKind) Display() string { return strings.ToUpper(string(k)) }

// Dismissal is one human verdict, with who gave it.
type Dismissal struct {
	Kind   DismissalKind
	Author string

	// Location is where the dismissal was written, for the audit line.
	Location string
}

// fingerprintMarkerPattern matches the hidden marker ARC puts on every inline
// comment it publishes. It is what lets a human reply be matched back to the
// finding it answers: finding IDs are assigned per run and cannot be used.
var fingerprintMarkerPattern = regexp.MustCompile(`<!--\s*arc-finding:v1\s+([0-9a-f]{12})\s*-->`)

// dismissalPattern matches an explicit verdict addressed to ARC.
//
// The prefix is required. A reply saying "this is a false positive" is a person
// talking to another person about the finding; "arc: false-positive" is a person
// talking to this tool, and only the second form suppresses anything.
var dismissalPattern = regexp.MustCompile(`(?im)^\s*(?:@?arc)\s*[:,]?\s*(false[-\s]?positive|wont[-\s]?fix|won't[-\s]?fix)\b`)

// RenderFingerprintMarker returns the hidden marker for a finding.
func RenderFingerprintMarker(finding findings.Finding) string {
	return fmt.Sprintf("<!-- arc-finding:v1 %s -->", Fingerprint(finding))
}

// ParseFingerprintMarker extracts the finding fingerprint from a comment body.
func ParseFingerprintMarker(body string) (string, bool) {
	match := fingerprintMarkerPattern.FindStringSubmatch(body)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// parseDismissal extracts a verdict from a comment body.
func parseDismissal(body string) (DismissalKind, bool) {
	match := dismissalPattern.FindStringSubmatch(body)
	if match == nil {
		return "", false
	}

	normalized := strings.NewReplacer(" ", "-", "'", "").Replace(strings.ToLower(match[1]))
	switch normalized {
	case "false-positive":
		return DismissalFalsePositive, true
	case "wont-fix", "wontfix":
		return DismissalWontFix, true
	default:
		return "", false
	}
}

// DismissalsFrom reads human verdicts out of a pull request's comments.
//
// The matching is positional, and it is worth being explicit about why. A finding
// ID cannot be used, because IDs are assigned per run. So ARC's own inline comment
// carries a hidden fingerprint, and a human verdict written on the same file and
// line is taken to answer that finding. A verdict written anywhere else has nothing
// to attach to and is ignored rather than guessed at.
//
// Only ARC's own comments supply fingerprints, so a person cannot invent one: the
// worst a fabricated marker can do is name a finding that was never published.
func DismissalsFrom(comments []github.Comment) map[string]Dismissal {
	// First pass: where did ARC publish which finding?
	atLocation := map[string]string{}
	for _, comment := range comments {
		fingerprint, ok := ParseFingerprintMarker(comment.Body)
		if !ok || comment.Path == "" {
			continue
		}
		atLocation[locationKey(comment.Path, comment.Line)] = fingerprint
	}
	if len(atLocation) == 0 {
		return nil
	}

	// Second pass: who answered, and where.
	dismissals := map[string]Dismissal{}
	for _, comment := range comments {
		if _, isARC := ParseFingerprintMarker(comment.Body); isARC {
			continue
		}
		kind, ok := parseDismissal(comment.Body)
		if !ok || comment.Path == "" {
			continue
		}
		fingerprint, ok := atLocation[locationKey(comment.Path, comment.Line)]
		if !ok {
			continue
		}

		// A finding dismissed twice keeps the first verdict: re-running a review
		// must not let ordering change the outcome.
		if _, already := dismissals[fingerprint]; already {
			continue
		}
		dismissals[fingerprint] = Dismissal{
			Kind:     kind,
			Author:   comment.Author,
			Location: comment.Location(),
		}
	}
	return dismissals
}

func locationKey(path string, line int) string {
	return fmt.Sprintf("%s:%d", strings.ReplaceAll(path, "\\", "/"), line)
}

// applyDismissal adjusts a decision for a human verdict.
//
// Two rules, and the split between them is the whole design. A dismissed finding
// never returns to the diff: commenting again on a line someone explicitly closed
// is how a tool gets muted. But a dismissal does not delete a blocker or a security
// finding — it moves those to the body with the dismissal recorded, because the one
// thing worse than a noisy reviewer is a silent one that was told to stay quiet
// about something serious. Everything else is suppressed outright.
func applyDismissal(decision Decision, dismissals map[string]Dismissal) Decision {
	if len(dismissals) == 0 || decision.Disposition == DispositionSuppress {
		return decision
	}

	dismissal, ok := dismissals[Fingerprint(decision.Finding)]
	if !ok {
		return decision
	}

	reason := reason(ReasonHumanDismissed, "%s by %s at %s",
		dismissal.Kind, dismissal.Author, dismissal.Location)

	if serious(decision.Finding) {
		return summarize(decision, reason)
	}
	return suppress(decision, reason)
}

// serious reports whether a finding is too consequential to be removed from the
// review by a comment.
func serious(finding findings.Finding) bool {
	return finding.Severity == findings.SeverityBlocker ||
		finding.Category == findings.CategorySecurity
}
