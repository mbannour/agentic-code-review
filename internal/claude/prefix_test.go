package claude

import (
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/contextselect"
)

func promptFor(t *testing.T, focus contextselect.ReviewFocus) string {
	t.Helper()

	input, err := NewInputBuilder().Build(contextselect.SelectedContext{
		PullRequest: contextselect.PullRequestSummary{
			Owner: "acme", Repository: "payments", Number: 1,
			Title: "Change", Description: "why this change exists",
		},
		Discussion: []contextselect.SelectedComment{
			{Kind: "inline", Author: "maria", Path: "a.go", Line: 4, Body: "deliberate"},
		},
		Rules: []contextselect.SelectedRule{{Path: "AGENTS.md", Content: "standards", Revision: "base"}},
		Files: []contextselect.SelectedFile{{
			Path: "a.go", Status: "modified", Patch: "@@ -1 +1,2 @@\n+call()\n",
			Kind: contextselect.FileKindSource, Importance: contextselect.ImportanceHigh,
		}},
		Focus: focus,
	})
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	return input.Content
}

// The shared context must be a stable prefix: everything that depends only on the
// pull request comes before anything that depends on which perspective is reviewing
// it. Once more than one perspective reviews the same change, that boundary is the
// difference between sending this context once and sending it five times.
func TestSharedContextPrecedesPerspectiveSpecificSections(t *testing.T) {
	content := promptFor(t, contextselect.ReviewFocus{
		RiskLevel: "high",
		Specialists: []contextselect.FocusSpecialist{{
			ID: "security", Title: "Security", Purpose: "Can this reach what it should not?",
			Focus: []string{"authorization removed"},
		}},
	})

	shared := []string{
		"PR INTENT", "PULL REQUEST DISCUSSION", "JIRA", "REPOSITORY RULES",
		"EXTERNAL EVIDENCE", "DETERMINISTIC", "CHANGED FILES", "RELATED UNCHANGED CODE",
	}
	specific := []string{"REVIEW FOCUS", "RESPONSE FORMAT"}

	lastShared := -1
	for _, section := range shared {
		index := strings.Index(content, section)
		if index < 0 {
			continue // an absent section states its absence elsewhere
		}
		if index > lastShared {
			lastShared = index
		}
	}
	for _, section := range specific {
		// The last occurrence: the instructions reference the response format by
		// name long before the section itself appears.
		index := strings.LastIndex(content, section)
		if index < 0 {
			t.Fatalf("prompt has no %s section", section)
		}
		if index < lastShared {
			t.Errorf("%s appears before the end of the shared context; the prefix is not stable", section)
		}
	}
}

// Two perspectives reviewing the same change must share a byte-identical prefix, or
// nothing downstream can reuse it.
func TestPromptPrefixIsIdenticalAcrossPerspectives(t *testing.T) {
	security := promptFor(t, contextselect.ReviewFocus{
		RiskLevel: "high",
		Specialists: []contextselect.FocusSpecialist{{
			ID: "security", Title: "Security", Purpose: "Can this reach what it should not?",
			Focus: []string{"authorization removed"},
		}},
	})
	reliability := promptFor(t, contextselect.ReviewFocus{
		RiskLevel: "high",
		Specialists: []contextselect.FocusSpecialist{{
			ID: "reliability", Title: "Reliability", Purpose: "What happens when this runs twice?",
			Focus: []string{"retry duplicates an effect"},
		}},
	})

	divergence := strings.Index(security, "REVIEW FOCUS")
	if divergence <= 0 {
		t.Fatal("no REVIEW FOCUS section to diverge at")
	}
	if security[:divergence] != reliability[:divergence] {
		t.Error("the shared prefix differs between perspectives")
	}

	// And the prefix must be the bulk of the prompt, or sharing it saves little.
	if divergence < len(security)/2 {
		t.Errorf("shared prefix is %d of %d bytes; most of the prompt is perspective-specific",
			divergence, len(security))
	}
}
