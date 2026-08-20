package publish

import (
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/github"
)

// TestMarkerRoundTrip checks a rendered marker parses back to the same commit.
func TestMarkerRoundTrip(t *testing.T) {
	rendered := Marker{HeadSHA: testHeadSHA}.Render()

	if want := "<!-- arc-review:v1 head=" + testHeadSHA + " -->"; rendered != want {
		t.Fatalf("Render() = %q, want %q", rendered, want)
	}

	parsed, ok := ParseMarker("body text\n\n" + rendered + "\n")
	if !ok {
		t.Fatal("ParseMarker() ok = false for a rendered marker")
	}
	if parsed.HeadSHA != testHeadSHA {
		t.Errorf("HeadSHA = %q, want %q", parsed.HeadSHA, testHeadSHA)
	}
}

// TestParseMarkerRejectsMalformed checks that anything not exactly a marker is not a
// marker. A half-recognized marker must never count as evidence of a publication.
func TestParseMarkerRejectsMalformed(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty body", body: ""},
		{name: "no marker", body: "Looks good to me!"},
		{name: "different version", body: "<!-- arc-review:v2 head=" + testHeadSHA + " -->"},
		{name: "no head", body: "<!-- arc-review:v1 -->"},
		{name: "empty head", body: "<!-- arc-review:v1 head= -->"},
		{name: "short sha", body: "<!-- arc-review:v1 head=abc -->"},
		{name: "non-hex sha", body: "<!-- arc-review:v1 head=zzzzzzz -->"},
		{name: "unclosed comment", body: "<!-- arc-review:v1 head=" + testHeadSHA},
		{name: "not a comment", body: "arc-review:v1 head=" + testHeadSHA},
		{name: "wrong tool", body: "<!-- other-bot:v1 head=" + testHeadSHA + " -->"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if marker, ok := ParseMarker(tt.body); ok {
				t.Errorf("ParseMarker() = %+v, ok = true; want no marker", marker)
			}
		})
	}
}

// TestAlreadyPublished covers duplicate detection: the same commit skips, a different
// commit does not, and text in someone else's review does not masquerade as ARC state.
func TestAlreadyPublished(t *testing.T) {
	arcBody := "## ARC Agentic Code Review\n\n" + Marker{HeadSHA: testHeadSHA}.Render()
	otherSHA := "def4560000000000000000000000000000000000"

	tests := []struct {
		name    string
		reviews []github.ExistingReview
		headSHA string
		author  string
		want    bool
	}{
		{
			name: "same marker and same commit skips",
			reviews: []github.ExistingReview{
				{ID: 1, Body: arcBody, CommitID: testHeadSHA, AuthorLogin: "dev"},
			},
			headSHA: testHeadSHA,
			want:    true,
		},
		{
			name: "marker for a different commit does not skip",
			reviews: []github.ExistingReview{
				{ID: 1, Body: arcBody, CommitID: testHeadSHA},
			},
			headSHA: otherSHA,
			want:    false,
		},
		{
			name: "commit id disagreeing with the marker does not skip",
			reviews: []github.ExistingReview{
				{ID: 1, Body: arcBody, CommitID: otherSHA},
			},
			headSHA: testHeadSHA,
			want:    false,
		},
		{
			name: "review without a commit id is credited on the marker alone",
			reviews: []github.ExistingReview{
				{ID: 1, Body: arcBody, CommitID: ""},
			},
			headSHA: testHeadSHA,
			want:    true,
		},
		{
			name: "unrelated human review does not skip",
			reviews: []github.ExistingReview{
				{ID: 1, Body: "Nice work, one nit below.", CommitID: testHeadSHA, AuthorLogin: "reviewer"},
				{ID: 2, Body: "", State: "APPROVED", CommitID: testHeadSHA},
			},
			headSHA: testHeadSHA,
			want:    false,
		},
		{
			name: "malformed marker does not skip",
			reviews: []github.ExistingReview{
				{ID: 1, Body: "<!-- arc-review:v1 head=abc -->", CommitID: testHeadSHA},
			},
			headSHA: testHeadSHA,
			want:    false,
		},
		{
			name:    "no reviews at all",
			reviews: nil,
			headSHA: testHeadSHA,
			want:    false,
		},
		{
			name: "unknown reviewed commit never matches",
			reviews: []github.ExistingReview{
				{ID: 1, Body: arcBody, CommitID: testHeadSHA},
			},
			headSHA: "",
			want:    false,
		},
		{
			name: "case differences in the sha still match",
			reviews: []github.ExistingReview{
				{ID: 1, Body: "<!-- arc-review:v1 head=" + strings.ToUpper(testHeadSHA) + " -->",
					CommitID: strings.ToUpper(testHeadSHA)},
			},
			headSHA: testHeadSHA,
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previous, found := AlreadyPublished(tt.reviews, tt.headSHA, tt.author)
			if found != tt.want {
				t.Fatalf("AlreadyPublished() = %t, want %t", found, tt.want)
			}
			if found && previous.ID == 0 {
				t.Error("AlreadyPublished() returned no review alongside a positive match")
			}
		})
	}
}

// TestMarkerIsInvisibleInRenderedBody checks the marker is an HTML comment, so a human
// reading the review never sees it.
func TestMarkerIsInvisibleInRenderedBody(t *testing.T) {
	body := NewRenderer().ReviewBody(ReviewInput{
		Plan:    planFor(findingFixture()),
		HeadSHA: testHeadSHA,
	})

	marker, ok := ParseMarker(body)
	if !ok {
		t.Fatal("rendered review body carries no marker; a second run would post a duplicate")
	}
	if marker.HeadSHA != testHeadSHA {
		t.Errorf("marker head = %q, want %q", marker.HeadSHA, testHeadSHA)
	}
	if !strings.HasPrefix(strings.TrimSpace(marker.Render()), "<!--") {
		t.Error("marker is not an HTML comment")
	}
}
