package publish

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/your-company/agentic-code-review/internal/github"
)

// MarkerVersion is the marker schema version. It is part of the marker text so a
// later format can be recognized as different rather than misread as this one.
const MarkerVersion = "v1"

// markerPattern matches an ARC marker and captures the head SHA it claims.
//
// It is deliberately narrow: a hexadecimal SHA of plausible length and nothing
// else. Loose matching here would let arbitrary pull request text pass itself off
// as ARC's own state.
var markerPattern = regexp.MustCompile(`<!--\s*arc-review:v1\s+head=([0-9a-fA-F]{7,64})\s*-->`)

// Marker records which commit an ARC review was published for.
//
// It exists so a second run does not post a second identical review. This is the
// weakest link in the publication path — the marker lives in text anyone can copy —
// so it is kept here, alone and small, behind one function that decides whether a
// review counts as ARC's own. When the tool gains an app identity, that check
// changes in this file and nowhere else.
type Marker struct {
	HeadSHA string
}

// Render returns the hidden HTML comment to embed in a review body.
func (m Marker) Render() string {
	return fmt.Sprintf("<!-- arc-review:%s head=%s -->", MarkerVersion, m.HeadSHA)
}

// ParseMarker extracts the marker from a review body.
//
// A body without a well-formed marker yields ok=false. A malformed marker — a
// truncated SHA, a different version, a mangled comment — is simply not a marker,
// and is never treated as partial evidence of an earlier publication.
func ParseMarker(body string) (Marker, bool) {
	match := markerPattern.FindStringSubmatch(body)
	if match == nil {
		return Marker{}, false
	}
	return Marker{HeadSHA: strings.ToLower(match[1])}, true
}

// AlreadyPublished reports whether one of reviews is an ARC review for headSHA.
//
// Matching text alone is not enough, because a marker is text anyone can copy into a
// comment. Everything available is required to agree: the marker must name this exact
// commit, the review's own commit_id must name it too when GitHub supplies one, and the
// review's author must be expectedAuthor when the caller knows who that is. An empty
// expectedAuthor means the identity is unknown — a personal access token belongs to a
// human whose other reviews must not be mistaken for ARC's — and the check falls back to
// marker plus commit.
//
// This is the weakest link in the publication path, which is why it lives here alone:
// when the tool gains a GitHub App identity the author becomes reliable, and only this
// function changes.
func AlreadyPublished(
	reviews []github.ExistingReview,
	headSHA string,
	expectedAuthor string,
) (github.ExistingReview, bool) {
	want := strings.ToLower(strings.TrimSpace(headSHA))
	if want == "" {
		return github.ExistingReview{}, false
	}
	author := strings.ToLower(strings.TrimSpace(expectedAuthor))

	for _, review := range reviews {
		marker, ok := ParseMarker(review.Body)
		if !ok || marker.HeadSHA != want {
			continue
		}
		if commit := strings.ToLower(strings.TrimSpace(review.CommitID)); commit != "" && commit != want {
			continue
		}
		if author != "" && strings.ToLower(strings.TrimSpace(review.AuthorLogin)) != author {
			continue
		}
		return review, true
	}

	return github.ExistingReview{}, false
}
