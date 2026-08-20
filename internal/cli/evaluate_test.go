package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/evaluation"
)

func TestRunEvaluateMarkdownAndJSON(t *testing.T) {
	dir := t.TempDir()
	labelsPath := filepath.Join(dir, "labels.json")
	predictionsPath := filepath.Join(dir, "predictions.json")
	writeEvaluationJSON(t, labelsPath, evaluation.LabelSet{
		SchemaVersion: evaluation.SchemaVersion,
		Name:          "cli-labels",
		Cases: []evaluation.LabelCase{{
			ID:       "clean",
			Findings: []evaluation.Label{},
		}},
	})
	writeEvaluationJSON(t, predictionsPath, evaluation.PredictionSet{
		SchemaVersion: evaluation.SchemaVersion,
		RunName:       "cli-run",
		Cases: []evaluation.PredictionCase{{
			ID:       "clean",
			Findings: []evaluation.Prediction{},
		}},
	})

	markdown := captureStdout(t, func() {
		if err := Run([]string{"evaluate", "--labels", labelsPath, "--predictions", predictionsPath}); err != nil {
			t.Fatalf("Run(evaluate) = %v", err)
		}
	})
	for _, want := range []string{"Labelled Evaluation", "cli-labels", "Precision", "Clean cases: 1/1"} {
		if !strings.Contains(markdown, want) {
			t.Errorf("markdown does not contain %q\n---\n%s", want, markdown)
		}
	}

	jsonOutput := captureStdout(t, func() {
		if err := Run([]string{"eval", "--labels", labelsPath, "--predictions", predictionsPath, "--format", "json"}); err != nil {
			t.Fatalf("Run(eval --format json) = %v", err)
		}
	})
	var report evaluation.Report
	if err := json.Unmarshal([]byte(jsonOutput), &report); err != nil {
		t.Fatalf("JSON output = %v\n---\n%s", err, jsonOutput)
	}
	if strings.Contains(jsonOutput, `"matches": null`) || strings.Contains(jsonOutput, `"by_category": null`) {
		t.Errorf("JSON report uses null for collection fields\n---\n%s", jsonOutput)
	}
	if report.LabelSet != "cli-labels" || report.RunName != "cli-run" || report.CleanCases.Correct != 1 {
		t.Errorf("report = %+v", report)
	}
}

func TestRunEvaluateValidatesArgumentsAndFiles(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"labels required", []string{"evaluate"}, "--labels is required"},
		{"predictions required", []string{"evaluate", "--labels", "labels.json"}, "--predictions is required"},
		{"invalid format", []string{"evaluate", "--labels", "labels.json", "--predictions", "predictions.json", "--format", "yaml"}, "invalid format"},
		{"missing labels file", []string{"evaluate", "--labels", "/does/not/exist", "--predictions", "predictions.json"}, "open labels"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Run(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run(%q) error = %v, want containing %q", tt.args, err, tt.want)
			}
		})
	}
}

func writeEvaluationJSON(t *testing.T, file string, value interface{}) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatalf("WriteFile(%s) = %v", file, err)
	}
}
