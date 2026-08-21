package contextselect

import (
	"context"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/analysis"
	"github.com/your-company/agentic-code-review/internal/review"
	"github.com/your-company/agentic-code-review/internal/technology"
)

func contextWithDiscussion(description string, comments ...review.Comment) review.Context {
	return review.Context{
		PullRequest: review.PullRequestContext{
			Owner: "acme", Repository: "payments", Number: 1,
			Title: "Change", Body: description,
		},
		Changes: review.ChangeContext{Files: []review.FileChange{{
			Filename: "a.go", Status: "modified", Patch: "@@ -1 +1,2 @@\n+call()\n",
		}}},
		Discussion: review.DiscussionContext{Comments: comments},
	}
}

func selectDiscussionWith(t *testing.T, budget Budget, ctx review.Context) SelectedContext {
	t.Helper()

	selected, err := NewSelectorWithBudget(budget).SelectWithTechnology(
		context.Background(), ctx, analysis.Result{}, technology.Profile{})
	if err != nil {
		t.Fatalf("SelectWithTechnology() = %v", err)
	}
	return selected
}

func TestDescriptionAndCommentsAreSelected(t *testing.T) {
	selected := selectDiscussionWith(t, DefaultBudget(), contextWithDiscussion(
		"Stops retrying permanent declines.",
		review.Comment{Kind: "conversation", Author: "sam", Body: "Add a test."},
		review.Comment{Kind: "inline", Author: "maria", Path: "a.go", Line: 4, Body: "Deliberate."},
	))

	if selected.PullRequest.Description != "Stops retrying permanent declines." {
		t.Errorf("description = %q", selected.PullRequest.Description)
	}
	if len(selected.Discussion) != 2 {
		t.Fatalf("discussion = %d comments, want 2", len(selected.Discussion))
	}
	// Comments on diff lines come first: a remark attached to a line is about the
	// code, while a general one is often about process.
	if selected.Discussion[0].Author != "maria" {
		t.Errorf("first comment = %+v, want the inline one", selected.Discussion[0])
	}
	if selected.Stats.CandidateComments != 2 || selected.Stats.SelectedComments != 2 {
		t.Errorf("stats = %+v", selected.Stats)
	}
}

// The author's own statement of intent outranks remarks about it.
func TestDescriptionTakesTheBudgetBeforeComments(t *testing.T) {
	budget := DefaultBudget()
	budget.Discussion = 200

	selected := selectDiscussionWith(t, budget, contextWithDiscussion(
		strings.Repeat("d", 180),
		review.Comment{Kind: "conversation", Author: "sam", Body: strings.Repeat("c", 100)},
	))

	if len(selected.PullRequest.Description) == 0 {
		t.Error("the description was dropped in favour of a comment")
	}
	if len(selected.Discussion) != 0 {
		t.Errorf("discussion = %+v, want the comment dropped for want of budget", selected.Discussion)
	}
}

// Half a comment is worse than none: an explanation cut before its "but" reverses
// its meaning.
func TestCommentsAreKeptWholeOrDropped(t *testing.T) {
	budget := DefaultBudget()
	budget.Discussion = 120

	selected := selectDiscussionWith(t, budget, contextWithDiscussion(
		"",
		review.Comment{Kind: "conversation", Author: "sam", Body: strings.Repeat("x", 60)},
		review.Comment{Kind: "conversation", Author: "kim", Body: strings.Repeat("y", 300)},
	))

	for _, comment := range selected.Discussion {
		if strings.Contains(comment.Body, MarkerDiscussion) {
			t.Errorf("a comment was truncated mid-text: %+v", comment)
		}
	}
	if len(selected.Discussion) != 1 || selected.Discussion[0].Author != "sam" {
		t.Errorf("discussion = %+v, want only the comment that fits", selected.Discussion)
	}
}

func TestDiscussionRespectsItsAllowance(t *testing.T) {
	budget := DefaultBudget()

	var comments []review.Comment
	for i := 0; i < 200; i++ {
		comments = append(comments, review.Comment{
			Kind: "conversation", Author: "sam", Body: strings.Repeat("z", 1000),
		})
	}

	selected := selectDiscussionWith(t, budget, contextWithDiscussion(strings.Repeat("d", 5000), comments...))

	total := len(selected.PullRequest.Description)
	for _, comment := range selected.Discussion {
		total += len(comment.Body)
	}
	if total > budget.Discussion {
		t.Errorf("discussion bytes = %d, want at most the %d allowance", total, budget.Discussion)
	}
}

func TestDescriptionIsTruncatedWithAMarker(t *testing.T) {
	budget := DefaultBudget()
	budget.Discussion = 300

	selected := selectDiscussionWith(t, budget, contextWithDiscussion(strings.Repeat("d", 5000)))

	if !selected.PullRequest.DescriptionTruncated {
		t.Error("a cut description was not marked truncated")
	}
	if !strings.Contains(selected.PullRequest.Description, MarkerDescription) {
		t.Error("the truncation is not visible in the text")
	}
}
