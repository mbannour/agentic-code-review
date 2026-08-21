package publish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/your-company/agentic-code-review/internal/findings"
	"github.com/your-company/agentic-code-review/internal/github"
)

// FindingsMarkerVersion is the schema version of the findings marker. It is part
// of the marker text, so a future format change cannot be misread as this one.
const FindingsMarkerVersion = "v1"

// MaxMarkedFingerprints bounds how many fingerprints a marker carries. The bound
// exists because the marker travels in every review body; the total published
// findings are bounded well below it.
const MaxMarkedFingerprints = 40

// fingerprintLength is how much of the digest is kept. Twelve hex characters is
// far more than enough to separate at most twenty findings on one pull request,
// and short enough that the marker stays readable.
const fingerprintLength = 12

var (
	findingsMarkerPattern = regexp.MustCompile(`<!--\s*arc-findings:v1\s+([0-9a-f ]*)\s*-->`)
	fingerprintPattern    = regexp.MustCompile(`^[0-9a-f]{12}$`)
	whitespaceRun         = regexp.MustCompile(`\s+`)
)

// Fingerprint identifies a finding across reviews of the same pull request.
//
// It is deliberately built from the category, the file, and the title — and
// deliberately not from line numbers or the model's finding ID. Line numbers move
// when the author edits the file, and IDs are assigned per run, so either would
// make every re-review look entirely new. The title is normalized for case and
// whitespace but not otherwise interpreted: a genuinely reworded title reads as a
// new finding, which is the safe direction to be wrong in, because it re-reports
// rather than silently withholds.
func Fingerprint(finding findings.Finding) string {
	material := strings.Join([]string{
		strings.ToLower(strings.TrimSpace(string(finding.Category))),
		strings.ToLower(strings.TrimSpace(strings.ReplaceAll(finding.File, "\\", "/"))),
		normalizeTitle(finding.Title),
	}, "\x00")

	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])[:fingerprintLength]
}

func normalizeTitle(title string) string {
	return whitespaceRun.ReplaceAllString(strings.ToLower(strings.TrimSpace(title)), " ")
}

// FindingsMarker is the hidden record of what a published review reported.
//
// It exists so a later review can tell what it already said without reading its
// own comments back: the fingerprints travel in the body ARC itself wrote, and
// no additional GitHub endpoint is needed to recover them.
type FindingsMarker struct {
	Fingerprints []string
}

// Render produces the hidden marker text.
func (m FindingsMarker) Render() string {
	if len(m.Fingerprints) == 0 {
		return fmt.Sprintf("<!-- arc-findings:%s -->", FindingsMarkerVersion)
	}

	kept := make([]string, 0, len(m.Fingerprints))
	seen := map[string]bool{}
	for _, fingerprint := range m.Fingerprints {
		if !fingerprintPattern.MatchString(fingerprint) || seen[fingerprint] {
			continue
		}
		seen[fingerprint] = true
		kept = append(kept, fingerprint)
		if len(kept) >= MaxMarkedFingerprints {
			break
		}
	}
	// Sorted, so the same set of findings always renders the same marker.
	sort.Strings(kept)

	return fmt.Sprintf("<!-- arc-findings:%s %s -->", FindingsMarkerVersion, strings.Join(kept, " "))
}

// ParseFindingsMarker extracts the fingerprints a review body records.
//
// A body without a well-formed marker yields ok=false, which is treated as "no
// previous review is known" rather than as "the previous review found nothing" —
// the two are different, and only one of them justifies staying quiet.
func ParseFindingsMarker(body string) (FindingsMarker, bool) {
	match := findingsMarkerPattern.FindStringSubmatch(body)
	if match == nil {
		return FindingsMarker{}, false
	}

	var fingerprints []string
	for _, field := range strings.Fields(match[1]) {
		if fingerprintPattern.MatchString(field) {
			fingerprints = append(fingerprints, field)
		}
	}
	return FindingsMarker{Fingerprints: fingerprints}, true
}

// MarkerFor renders the findings marker for a plan's published findings.
//
// Only published findings are recorded. A suppressed finding was never shown to
// anyone, so treating it as "already reported" next time would suppress it twice
// for a reason nobody ever saw.
func MarkerFor(plan Plan) FindingsMarker {
	var fingerprints []string
	for _, inline := range plan.Inline {
		fingerprints = append(fingerprints, Fingerprint(inline.Finding))
	}
	for _, finding := range plan.Summary {
		fingerprints = append(fingerprints, Fingerprint(finding))
	}
	return FindingsMarker{Fingerprints: fingerprints}
}

// History is what ARC already said about this pull request.
type History struct {
	// Known reports that a previous ARC review was found and understood. Without
	// it, every finding is treated as new.
	Known bool

	// HeadSHA is the commit the previous review was published for.
	HeadSHA string

	// Reported are the fingerprints that review published.
	Reported []string

	// Dismissals are human verdicts on published findings, keyed by fingerprint.
	// They are honoured only when the caller opts in; see WithDismissals.
	Dismissals map[string]Dismissal
}

// WithDismissals returns the history with human verdicts attached.
//
// It is a separate step from loading the history because honouring a dismissal is
// a decision an operator makes, not a default: anyone able to comment on a pull
// request can write one.
func (h History) WithDismissals(dismissals map[string]Dismissal) History {
	h.Dismissals = dismissals
	return h
}

// DismissalCount is how many findings carry a human verdict.
func (h History) DismissalCount() int { return len(h.Dismissals) }

// Reports whether the previous review published this finding.
func (h History) Reports(finding findings.Finding) bool {
	if !h.Known {
		return false
	}
	fingerprint := Fingerprint(finding)
	for _, reported := range h.Reported {
		if reported == fingerprint {
			return true
		}
	}
	return false
}

// Count is how many findings the previous review published.
func (h History) Count() int { return len(h.Reported) }

// HistoryFrom reads the most recent ARC review of a pull request.
//
// Reviews are scanned newest first, and only a review carrying both markers
// counts: the head marker proves it was an ARC review of a specific commit, and
// the findings marker is what makes the comparison possible. A review published
// before this feature existed has no findings marker, so it is treated as unknown
// rather than as an empty result.
func HistoryFrom(reviews []github.ExistingReview, currentHeadSHA string, expectedAuthor string) History {
	author := strings.ToLower(strings.TrimSpace(expectedAuthor))
	current := strings.ToLower(strings.TrimSpace(currentHeadSHA))

	for i := len(reviews) - 1; i >= 0; i-- {
		review := reviews[i]

		head, ok := ParseMarker(review.Body)
		if !ok {
			continue
		}
		if author != "" && strings.ToLower(strings.TrimSpace(review.AuthorLogin)) != author {
			continue
		}
		// A review of the commit under review is this same run's work, not a
		// previous one: comparing against it would call every finding a repeat.
		if current != "" && head.HeadSHA == current {
			continue
		}

		marker, ok := ParseFindingsMarker(review.Body)
		if !ok {
			continue
		}
		return History{Known: true, HeadSHA: head.HeadSHA, Reported: marker.Fingerprints}
	}

	return History{}
}

// LoadHistory reads the pull request's existing reviews and returns what ARC
// already reported.
//
// It is a read, and a failure to perform it is not a reason to abandon a review:
// the caller receives an empty history and every finding is treated as new, which
// is the same behaviour as the first review of a pull request.
func (p Publisher) LoadHistory(ctx context.Context, pr github.PullRequest, headSHA string) (History, error) {
	reviews, err := p.api.ListPullRequestReviews(ctx, pr)
	if err != nil {
		return History{}, err
	}
	return HistoryFrom(reviews, headSHA, ""), nil
}

// Delta is how this review compares with the previous one.
type Delta struct {
	// Known reports that a previous ARC review was found. Without it the counts
	// below are meaningless and must not be rendered.
	Known bool

	// PreviousHeadSHA is the commit the previous review was published for.
	PreviousHeadSHA string

	// Resolved is how many findings the previous review published that this one
	// does not. It is deliberately called resolved rather than fixed: ARC knows
	// only that it is no longer reporting them.
	Resolved int

	// Persisting is how many are reported again.
	Persisting int

	// New is how many were not in the previous review.
	New int
}

// DeltaFor compares a plan's published findings with what was published before.
func DeltaFor(plan Plan, history History) Delta {
	if !history.Known {
		return Delta{}
	}

	delta := Delta{Known: true, PreviousHeadSHA: history.HeadSHA}

	current := map[string]bool{}
	for _, fingerprint := range MarkerFor(plan).Fingerprints {
		current[fingerprint] = true
	}
	previous := map[string]bool{}
	for _, fingerprint := range history.Reported {
		previous[fingerprint] = true
	}

	for fingerprint := range current {
		if previous[fingerprint] {
			delta.Persisting++
		} else {
			delta.New++
		}
	}
	for fingerprint := range previous {
		if !current[fingerprint] {
			delta.Resolved++
		}
	}
	return delta
}
