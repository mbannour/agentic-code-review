package evaluation

import (
	"fmt"
	"strings"
)

// Render returns a terminal-readable evaluation report.
func Render(report Report) string {
	var out strings.Builder
	out.WriteString("Labelled Evaluation\n\n")
	fmt.Fprintf(&out, "Label set:   %s\n", report.LabelSet)
	fmt.Fprintf(&out, "Run:         %s\n", report.RunName)
	if report.Model != "" {
		fmt.Fprintf(&out, "Model:       %s\n", report.Model)
	}
	if report.PromptVersion != "" {
		fmt.Fprintf(&out, "Prompt:      %s\n", report.PromptVersion)
	}
	fmt.Fprintf(&out, "Cases:       %d\n", report.CaseCount)
	fmt.Fprintf(&out, "Labels:      %d\n", report.LabelCount)
	fmt.Fprintf(&out, "Predictions: %d\n", report.PredictionCount)

	out.WriteString("\nAggregate (micro)\n\n")
	out.WriteString("| TP | FP | FN | Precision | Recall | F1 |\n")
	out.WriteString("| ---: | ---: | ---: | ---: | ---: | ---: |\n")
	fmt.Fprintf(&out, "| %d | %d | %d | %.1f%% | %.1f%% | %.1f%% |\n",
		report.Metrics.TruePositives,
		report.Metrics.FalsePositives,
		report.Metrics.FalseNegatives,
		report.Metrics.Precision*100,
		report.Metrics.Recall*100,
		report.Metrics.F1*100,
	)

	if len(report.ByCategory) > 0 {
		out.WriteString("\nBy category\n\n")
		out.WriteString("| Category | TP | FP | FN | Precision | Recall | F1 |\n")
		out.WriteString("| --- | ---: | ---: | ---: | ---: | ---: | ---: |\n")
		for _, category := range report.ByCategory {
			metrics := category.Metrics
			fmt.Fprintf(&out, "| %s | %d | %d | %d | %.1f%% | %.1f%% | %.1f%% |\n",
				category.Category,
				metrics.TruePositives,
				metrics.FalsePositives,
				metrics.FalseNegatives,
				metrics.Precision*100,
				metrics.Recall*100,
				metrics.F1*100,
			)
		}
	}

	if report.CleanCases.Total > 0 {
		fmt.Fprintf(&out, "\nClean cases: %d/%d correctly produced no findings.\n",
			report.CleanCases.Correct, report.CleanCases.Total)
	}

	out.WriteString("\nCases\n")
	for _, c := range report.Cases {
		status := "PASS"
		if !c.Passed {
			status = "FAIL"
		}
		fmt.Fprintf(&out, "\n%-4s  %-24s TP %d  FP %d  FN %d\n",
			status, c.ID, c.Metrics.TruePositives, c.Metrics.FalsePositives, c.Metrics.FalseNegatives)
		for _, missed := range c.MissedLabels {
			fmt.Fprintf(&out, "      missed %-16s %s  %s\n",
				missed.ID, missed.Category, referenceLocation(missed))
		}
		for _, falsePositive := range c.FalsePositives {
			fmt.Fprintf(&out, "      false  %-16s %s  %s\n",
				falsePositive.ID, falsePositive.Category, referenceLocation(falsePositive))
		}
	}

	return out.String()
}

func referenceLocation(reference Reference) string {
	if reference.EndLine <= reference.StartLine {
		return fmt.Sprintf("%s:%d", reference.File, reference.StartLine)
	}
	return fmt.Sprintf("%s:%d-%d", reference.File, reference.StartLine, reference.EndLine)
}
