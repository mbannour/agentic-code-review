package evaluation

import (
	"strings"
	"testing"
)

const validLabelsJSON = `{
  "schema_version": 1,
  "name": "labels",
  "cases": [{
    "id": "case-1",
    "findings": [{
      "id": "COR-001",
      "category": "correctness",
      "file": "src/a.go",
      "start_line": 10,
      "end_line": 12,
      "title_contains": ["error"]
    }]
  }]
}`

const validPredictionsJSON = `{
  "schema_version": 1,
  "run_name": "run",
  "cases": [{
    "id": "case-1",
    "findings": [{
      "id": "COR-101",
      "category": "correctness",
      "file": "src/a.go",
      "start_line": 11,
      "end_line": 11,
      "title": "Error is swallowed"
    }]
  }]
}`

func TestDecodeValidInputs(t *testing.T) {
	labels, err := DecodeLabels(strings.NewReader(validLabelsJSON))
	if err != nil {
		t.Fatalf("DecodeLabels() = %v", err)
	}
	predictions, err := DecodePredictions(strings.NewReader(validPredictionsJSON))
	if err != nil {
		t.Fatalf("DecodePredictions() = %v", err)
	}
	if labels.Name != "labels" || predictions.RunName != "run" {
		t.Errorf("decoded labels/predictions = %+v / %+v", labels, predictions)
	}
}

func TestDecodeIsStrictAndValidatesSchema(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"unknown field", strings.Replace(validLabelsJSON, `"name": "labels"`, `"name": "labels", "surprise": true`, 1), "unknown field"},
		{"trailing object", validLabelsJSON + `{}`, "unexpected content"},
		{"wrong schema", strings.Replace(validLabelsJSON, `"schema_version": 1`, `"schema_version": 2`, 1), "schema_version is 2"},
		{"duplicate case", `{"schema_version":1,"name":"labels","cases":[{"id":"case-1","findings":[]},{"id":"case-1","findings":[]}]}`, "duplicated"},
		{"invalid traversal path", strings.Replace(validLabelsJSON, `src/a.go`, `../a.go`, 1), "relative repository path"},
		{"invalid line range", strings.Replace(validLabelsJSON, `"end_line": 12`, `"end_line": 9`, 1), "greater than or equal"},
		{"empty title term", strings.Replace(validLabelsJSON, `"error"`, `" "`, 1), "must not be empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeLabels(strings.NewReader(tt.input))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DecodeLabels() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestDecodePredictionsRequiresTraceableFindings(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"missing run name", strings.Replace(validPredictionsJSON, `"run_name": "run"`, `"run_name": ""`, 1), "run_name"},
		{"empty prediction id", strings.Replace(validPredictionsJSON, `"id": "COR-101"`, `"id": ""`, 1), ".id must not be empty"},
		{"empty title", strings.Replace(validPredictionsJSON, `"title": "Error is swallowed"`, `"title": ""`, 1), ".title must not be empty"},
		{"unknown category", strings.Replace(validPredictionsJSON, `"category": "correctness"`, `"category": "style"`, 1), "category is not recognized"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodePredictions(strings.NewReader(tt.input))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DecodePredictions() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}
