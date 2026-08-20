package publish

import (
	"testing"

	"github.com/your-company/agentic-code-review/internal/findings"
)

func TestConfiguredDocumentCanSubstantiateRequirementOrArchitecture(t *testing.T) {
	document := findings.Evidence{Type: findings.EvidenceDocument, Source: "customer-contract", Detail: "explicit requirement"}
	for _, category := range []findings.Category{findings.CategoryRequirement, findings.CategoryArchitecture} {
		finding := findings.Finding{Category: category, Evidence: []findings.Evidence{document}}
		if reason, ok := evidenceShortfall(finding); !ok {
			t.Errorf("%s document evidence rejected: %s", category, reason)
		}
	}
}

func TestSchemaAloneNeverSubstitutesForChangedCodeEvidence(t *testing.T) {
	schema := findings.Evidence{Type: findings.EvidenceSchema, Source: "stage-schema", Detail: "column is text"}
	for _, category := range []findings.Category{
		findings.CategoryCorrectness, findings.CategorySecurity, findings.CategoryMaintainability,
	} {
		finding := findings.Finding{Category: category, Evidence: []findings.Evidence{schema}}
		if _, ok := evidenceShortfall(finding); ok {
			t.Errorf("%s accepted schema without code evidence", category)
		}
	}
}
