// Package evaluation scores review findings against human-labelled issues.
//
// Labels and predictions are deliberately separate inputs: labels should remain
// stable while model, prompt, and context-selection experiments produce new
// prediction snapshots. Matching and metric calculation are deterministic and do
// not call a model.
package evaluation

import "github.com/your-company/agentic-code-review/internal/findings"

const SchemaVersion = 1

// LabelSet is the human-authored ground truth for a collection of review cases.
type LabelSet struct {
	SchemaVersion int         `json:"schema_version"`
	Name          string      `json:"name"`
	Cases         []LabelCase `json:"cases"`
}

// LabelCase groups the known issues for one pull request or synthetic change.
// Source is an optional human-readable provenance value, such as a PR URL or
// fixture path; it is never opened or executed by the evaluator.
type LabelCase struct {
	ID          string  `json:"id"`
	Description string  `json:"description,omitempty"`
	Source      string  `json:"source,omitempty"`
	Findings    []Label `json:"findings"`
}

// Label is one known issue. TitleContains is an optional list of case-insensitive
// terms a prediction title must contain. It disambiguates multiple issues of the
// same category whose labelled line ranges overlap.
type Label struct {
	ID            string            `json:"id"`
	Category      findings.Category `json:"category"`
	File          string            `json:"file"`
	StartLine     int               `json:"start_line"`
	EndLine       int               `json:"end_line"`
	TitleContains []string          `json:"title_contains,omitempty"`
}

// PredictionSet is one captured reviewer run against the labelled cases.
type PredictionSet struct {
	SchemaVersion int              `json:"schema_version"`
	RunName       string           `json:"run_name"`
	Model         string           `json:"model,omitempty"`
	PromptVersion string           `json:"prompt_version,omitempty"`
	Cases         []PredictionCase `json:"cases"`
}

// PredictionCase groups the findings emitted for one labelled case. Cases with no
// findings are represented explicitly with an empty findings array.
type PredictionCase struct {
	ID       string       `json:"id"`
	Findings []Prediction `json:"findings"`
}

// Prediction is the subset of a review finding required for scoring.
type Prediction struct {
	ID        string            `json:"id"`
	Category  findings.Category `json:"category"`
	File      string            `json:"file"`
	StartLine int               `json:"start_line"`
	EndLine   int               `json:"end_line"`
	Title     string            `json:"title"`
}

// Metrics is a binary-classification scorecard. Counts are micro-aggregated before
// rates are calculated.
type Metrics struct {
	TruePositives  int     `json:"true_positives"`
	FalsePositives int     `json:"false_positives"`
	FalseNegatives int     `json:"false_negatives"`
	Precision      float64 `json:"precision"`
	Recall         float64 `json:"recall"`
	F1             float64 `json:"f1"`
}

// CategoryMetrics carries the scorecard for one finding category.
type CategoryMetrics struct {
	Category findings.Category `json:"category"`
	Metrics  Metrics           `json:"metrics"`
}

// Match records which prediction satisfied which human label.
type Match struct {
	LabelID      string `json:"label_id"`
	PredictionID string `json:"prediction_id"`
}

// Reference is a compact unmatched label or prediction for diagnostics.
type Reference struct {
	ID        string            `json:"id"`
	Category  findings.Category `json:"category"`
	File      string            `json:"file"`
	StartLine int               `json:"start_line"`
	EndLine   int               `json:"end_line"`
	Title     string            `json:"title,omitempty"`
}

// CaseResult is the score and diagnostics for one evaluation case.
type CaseResult struct {
	ID             string      `json:"id"`
	Passed         bool        `json:"passed"`
	Metrics        Metrics     `json:"metrics"`
	Matches        []Match     `json:"matches"`
	MissedLabels   []Reference `json:"missed_labels"`
	FalsePositives []Reference `json:"false_positives"`
}

// CleanCaseStats makes all-negative cases visible; they do not otherwise affect
// precision, recall, or F1 denominators.
type CleanCaseStats struct {
	Total   int `json:"total"`
	Correct int `json:"correct"`
}

// Report is the complete deterministic evaluation result.
type Report struct {
	SchemaVersion   int               `json:"schema_version"`
	LabelSet        string            `json:"label_set"`
	RunName         string            `json:"run_name"`
	Model           string            `json:"model,omitempty"`
	PromptVersion   string            `json:"prompt_version,omitempty"`
	CaseCount       int               `json:"case_count"`
	LabelCount      int               `json:"label_count"`
	PredictionCount int               `json:"prediction_count"`
	Metrics         Metrics           `json:"metrics"`
	ByCategory      []CategoryMetrics `json:"by_category"`
	CleanCases      CleanCaseStats    `json:"clean_cases"`
	Cases           []CaseResult      `json:"cases"`
}
