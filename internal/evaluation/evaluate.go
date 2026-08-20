package evaluation

import (
	"sort"
	"strings"

	"github.com/your-company/agentic-code-review/internal/findings"
)

// Evaluate scores predictions against labels. Both arguments must already have
// passed DecodeLabels or DecodePredictions (or be equivalently valid values).
func Evaluate(labels LabelSet, predictions PredictionSet) Report {
	labelCases := make(map[string]LabelCase, len(labels.Cases))
	predictionCases := make(map[string]PredictionCase, len(predictions.Cases))
	caseIDs := make([]string, 0, len(labels.Cases)+len(predictions.Cases))

	for _, c := range labels.Cases {
		labelCases[c.ID] = c
		caseIDs = append(caseIDs, c.ID)
	}
	var extras []string
	for _, c := range predictions.Cases {
		predictionCases[c.ID] = c
		if _, known := labelCases[c.ID]; !known {
			extras = append(extras, c.ID)
		}
	}
	sort.Strings(extras)
	caseIDs = append(caseIDs, extras...)

	report := Report{
		SchemaVersion: SchemaVersion,
		LabelSet:      labels.Name,
		RunName:       predictions.RunName,
		Model:         predictions.Model,
		PromptVersion: predictions.PromptVersion,
		CaseCount:     len(caseIDs),
		ByCategory:    []CategoryMetrics{},
		Cases:         []CaseResult{},
	}

	categoryCounts := map[findings.Category]*Metrics{}
	for _, category := range findings.Categories() {
		categoryCounts[category] = &Metrics{}
	}

	for _, id := range caseIDs {
		labelCase, isLabelledCase := labelCases[id]
		predictionCase := predictionCases[id]
		caseResult := evaluateCase(id, labelCase.Findings, predictionCase.Findings)
		report.Cases = append(report.Cases, caseResult)
		report.LabelCount += len(labelCase.Findings)
		report.PredictionCount += len(predictionCase.Findings)
		addCounts(&report.Metrics, caseResult.Metrics)

		if isLabelledCase && len(labelCase.Findings) == 0 {
			report.CleanCases.Total++
			if len(predictionCase.Findings) == 0 {
				report.CleanCases.Correct++
			}
		}

		labelByID := make(map[string]Label, len(labelCase.Findings))
		for _, label := range labelCase.Findings {
			labelByID[label.ID] = label
		}
		for _, match := range caseResult.Matches {
			categoryCounts[labelByID[match.LabelID].Category].TruePositives++
		}
		for _, missed := range caseResult.MissedLabels {
			categoryCounts[missed.Category].FalseNegatives++
		}
		for _, falsePositive := range caseResult.FalsePositives {
			categoryCounts[falsePositive.Category].FalsePositives++
		}
	}

	report.Metrics = calculateRates(report.Metrics)
	for _, category := range findings.Categories() {
		counts := *categoryCounts[category]
		if counts.TruePositives+counts.FalsePositives+counts.FalseNegatives == 0 {
			continue
		}
		report.ByCategory = append(report.ByCategory, CategoryMetrics{
			Category: category,
			Metrics:  calculateRates(counts),
		})
	}
	return report
}

func evaluateCase(id string, labels []Label, predictions []Prediction) CaseResult {
	pairs := maximumMatching(labels, predictions)
	matchedLabels := make([]bool, len(labels))
	matchedPredictions := make([]bool, len(predictions))

	result := CaseResult{
		ID:             id,
		Matches:        []Match{},
		MissedLabels:   []Reference{},
		FalsePositives: []Reference{},
	}
	for _, pair := range pairs {
		matchedLabels[pair.label] = true
		matchedPredictions[pair.prediction] = true
		result.Matches = append(result.Matches, Match{
			LabelID:      labels[pair.label].ID,
			PredictionID: predictions[pair.prediction].ID,
		})
	}
	for i, label := range labels {
		if !matchedLabels[i] {
			result.MissedLabels = append(result.MissedLabels, labelReference(label))
		}
	}
	for i, prediction := range predictions {
		if !matchedPredictions[i] {
			result.FalsePositives = append(result.FalsePositives, predictionReference(prediction))
		}
	}

	result.Metrics = calculateRates(Metrics{
		TruePositives:  len(result.Matches),
		FalsePositives: len(result.FalsePositives),
		FalseNegatives: len(result.MissedLabels),
	})
	result.Passed = result.Metrics.FalsePositives == 0 && result.Metrics.FalseNegatives == 0
	return result
}

type matchedPair struct {
	label      int
	prediction int
}

// maximumMatching finds the largest one-to-one matching, so input order cannot make
// one broad prediction consume a label and incorrectly turn another valid prediction
// into a false positive.
func maximumMatching(labels []Label, predictions []Prediction) []matchedPair {
	predictionForLabel := make([]int, len(labels))
	for i := range predictionForLabel {
		predictionForLabel[i] = -1
	}

	var augment func(int, []bool) bool
	augment = func(predictionIndex int, seen []bool) bool {
		for labelIndex, label := range labels {
			if seen[labelIndex] || !matches(label, predictions[predictionIndex]) {
				continue
			}
			seen[labelIndex] = true
			if predictionForLabel[labelIndex] == -1 ||
				augment(predictionForLabel[labelIndex], seen) {
				predictionForLabel[labelIndex] = predictionIndex
				return true
			}
		}
		return false
	}

	for predictionIndex := range predictions {
		augment(predictionIndex, make([]bool, len(labels)))
	}

	var pairs []matchedPair
	for labelIndex, predictionIndex := range predictionForLabel {
		if predictionIndex >= 0 {
			pairs = append(pairs, matchedPair{label: labelIndex, prediction: predictionIndex})
		}
	}
	return pairs
}

func matches(label Label, prediction Prediction) bool {
	if label.Category != prediction.Category || normalizedPath(label.File) != normalizedPath(prediction.File) {
		return false
	}
	if label.StartLine > prediction.EndLine || prediction.StartLine > label.EndLine {
		return false
	}
	title := strings.ToLower(prediction.Title)
	for _, term := range label.TitleContains {
		if !strings.Contains(title, strings.ToLower(strings.TrimSpace(term))) {
			return false
		}
	}
	return true
}

func calculateRates(metrics Metrics) Metrics {
	metrics.Precision = divide(metrics.TruePositives, metrics.TruePositives+metrics.FalsePositives)
	metrics.Recall = divide(metrics.TruePositives, metrics.TruePositives+metrics.FalseNegatives)
	if metrics.Precision+metrics.Recall > 0 {
		metrics.F1 = 2 * metrics.Precision * metrics.Recall / (metrics.Precision + metrics.Recall)
	}
	return metrics
}

func divide(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func addCounts(dst *Metrics, src Metrics) {
	dst.TruePositives += src.TruePositives
	dst.FalsePositives += src.FalsePositives
	dst.FalseNegatives += src.FalseNegatives
}

func labelReference(label Label) Reference {
	return Reference{
		ID:        label.ID,
		Category:  label.Category,
		File:      normalizedPath(label.File),
		StartLine: label.StartLine,
		EndLine:   label.EndLine,
	}
}

func predictionReference(prediction Prediction) Reference {
	return Reference{
		ID:        prediction.ID,
		Category:  prediction.Category,
		File:      normalizedPath(prediction.File),
		StartLine: prediction.StartLine,
		EndLine:   prediction.EndLine,
		Title:     prediction.Title,
	}
}
