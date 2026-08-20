package verification

import (
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/contextselect"
	"github.com/your-company/agentic-code-review/internal/evidence"
	"github.com/your-company/agentic-code-review/internal/findings"
)

func TestVerificationIncludesOnlyExternalEvidenceCitedByFinding(t *testing.T) {
	selected := selectionFixture()
	selected.Evidence = []contextselect.SelectedEvidence{
		{ID: "orders-contract", Kind: evidence.KindRequirement, SourceType: evidence.SourceConfluence,
			Locator: "confluence:page/123", Content: "References must be retained."},
		{ID: "unrelated", Kind: evidence.KindReference, SourceType: evidence.SourceFile,
			Locator: "other.md", Content: "Unrelated content."},
	}
	candidate := finding("REQ-001", findings.SeverityHigh, 0.9)
	candidate.Category = findings.CategoryRequirement
	candidate.Evidence = append(candidate.Evidence, findings.Evidence{
		Type: findings.EvidenceDocument, Source: "orders-contract", Detail: "Requires retaining references.",
	})

	context, err := NewContextBuilder().Build(candidate, selected)
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	if len(context.RelevantEvidence) != 1 || context.RelevantEvidence[0].Label != "orders-contract" {
		t.Fatalf("RelevantEvidence = %+v", context.RelevantEvidence)
	}
	if strings.Contains(context.RelevantEvidence[0].Content, "Unrelated content") {
		t.Fatal("uncited external document entered verification context")
	}
}
