package findings

import (
	"fmt"
	"sort"
	"strings"
)

// Header is the title of the rendered report.
const Header = "Agentic Review"

// NoFindingsMessage is what a clean review prints. Zero findings is a valid,
// expected outcome, so it reads as a result rather than as a failure.
const NoFindingsMessage = "No actionable findings found."

// Render returns the local terminal report for a validated result.
//
// Rendering is presentation only: it makes no judgement about which findings
// matter, drops nothing, and knows nothing about how a later step might publish
// them. Ordering is deterministic — most serious first, then by location — so the
// same result always prints the same text.
func Render(result ReviewResult) string {
	var out strings.Builder

	out.WriteString(Header + "\n\n")

	if !result.HasFindings() {
		out.WriteString(NoFindingsMessage + "\n")
		if summary := strings.TrimSpace(result.Summary); summary != "" {
			out.WriteString("\n" + summary + "\n")
		}
		return out.String()
	}

	fmt.Fprintf(&out, "%s\n", countLine(result.Count()))

	if summary := strings.TrimSpace(result.Summary); summary != "" {
		out.WriteString("\n" + summary + "\n")
	}

	for _, finding := range sortedFindings(result.Findings) {
		out.WriteString("\n")
		out.WriteString(renderFinding(finding))
	}

	return out.String()
}

// countLine renders the finding count with correct pluralization.
func countLine(count int) string {
	if count == 1 {
		return "1 actionable finding"
	}
	return fmt.Sprintf("%d actionable findings", count)
}

// renderFinding renders one finding block.
func renderFinding(finding Finding) string {
	var out strings.Builder

	fmt.Fprintf(&out, "%s · %s\n", finding.Severity.Display(), finding.ID)
	fmt.Fprintf(&out, "%s\n", finding.Location())

	fmt.Fprintf(&out, "\n%s\n", strings.TrimSpace(finding.Title))

	writeSection(&out, "Problem", finding.Problem)
	writeSection(&out, "Impact", finding.Impact)

	out.WriteString("\nEvidence\n")
	for _, evidence := range finding.Evidence {
		fmt.Fprintf(&out, "- %s: %s\n", evidence.Type.Display(), strings.TrimSpace(evidence.Source))
	}

	writeSection(&out, "Suggestion", finding.Suggestion)

	fmt.Fprintf(&out, "\nConfidence: %d%%\n", finding.ConfidencePercent())
	return out.String()
}

// writeSection writes a labelled prose block.
func writeSection(out *strings.Builder, label, body string) {
	fmt.Fprintf(out, "\n%s\n%s\n", label, strings.TrimSpace(body))
}

// sortedFindings returns the findings in report order without mutating the input:
// most serious first, then by file, line, and ID so ties never reorder between
// runs.
func sortedFindings(findings []Finding) []Finding {
	ordered := make([]Finding, len(findings))
	copy(ordered, findings)

	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Severity.Rank() != b.Severity.Rank() {
			return a.Severity.Rank() < b.Severity.Rank()
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.StartLine != b.StartLine {
			return a.StartLine < b.StartLine
		}
		return a.ID < b.ID
	})

	return ordered
}
