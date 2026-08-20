package evaluation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// DecodeLabels strictly decodes and validates a human label set.
func DecodeLabels(r io.Reader) (LabelSet, error) {
	var labels LabelSet
	if err := decodeStrict(r, &labels); err != nil {
		return LabelSet{}, fmt.Errorf("decode labels: %w", err)
	}
	if err := validateLabels(labels); err != nil {
		return LabelSet{}, fmt.Errorf("validate labels: %w", err)
	}
	return labels, nil
}

// DecodePredictions strictly decodes and validates a prediction snapshot.
func DecodePredictions(r io.Reader) (PredictionSet, error) {
	var predictions PredictionSet
	if err := decodeStrict(r, &predictions); err != nil {
		return PredictionSet{}, fmt.Errorf("decode predictions: %w", err)
	}
	if err := validatePredictions(predictions); err != nil {
		return PredictionSet{}, fmt.Errorf("validate predictions: %w", err)
	}
	return predictions, nil
}

func decodeStrict(r io.Reader, dst interface{}) error {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}

	var trailing interface{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected content after JSON object")
		}
		return err
	}
	return nil
}

func validateLabels(labels LabelSet) error {
	if labels.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version is %d, want %d", labels.SchemaVersion, SchemaVersion)
	}
	if strings.TrimSpace(labels.Name) == "" {
		return errors.New("name must not be empty")
	}
	if labels.Cases == nil {
		return errors.New("cases must be an array")
	}
	if len(labels.Cases) == 0 {
		return errors.New("cases must contain at least one case")
	}

	seenCases := map[string]bool{}
	for i, c := range labels.Cases {
		where := fmt.Sprintf("cases[%d]", i)
		id := strings.TrimSpace(c.ID)
		if id == "" {
			return fmt.Errorf("%s.id must not be empty", where)
		}
		if seenCases[id] {
			return fmt.Errorf("%s.id %q is duplicated", where, id)
		}
		seenCases[id] = true
		if c.Findings == nil {
			return fmt.Errorf("%s.findings must be an array", where)
		}

		seenFindings := map[string]bool{}
		for j, label := range c.Findings {
			findingWhere := fmt.Sprintf("%s.findings[%d]", where, j)
			labelID := strings.TrimSpace(label.ID)
			if labelID == "" {
				return fmt.Errorf("%s.id must not be empty", findingWhere)
			}
			if seenFindings[labelID] {
				return fmt.Errorf("%s.id %q is duplicated in case %q", findingWhere, labelID, id)
			}
			seenFindings[labelID] = true
			if err := validateLocation(label.Category, label.File, label.StartLine, label.EndLine); err != nil {
				return fmt.Errorf("%s: %w", findingWhere, err)
			}
			seenTerms := map[string]bool{}
			for k, term := range label.TitleContains {
				normalized := strings.ToLower(strings.TrimSpace(term))
				if normalized == "" {
					return fmt.Errorf("%s.title_contains[%d] must not be empty", findingWhere, k)
				}
				if seenTerms[normalized] {
					return fmt.Errorf("%s.title_contains term %q is duplicated", findingWhere, term)
				}
				seenTerms[normalized] = true
			}
		}
	}
	return nil
}

func validatePredictions(predictions PredictionSet) error {
	if predictions.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version is %d, want %d", predictions.SchemaVersion, SchemaVersion)
	}
	if strings.TrimSpace(predictions.RunName) == "" {
		return errors.New("run_name must not be empty")
	}
	if predictions.Cases == nil {
		return errors.New("cases must be an array")
	}

	seenCases := map[string]bool{}
	for i, c := range predictions.Cases {
		where := fmt.Sprintf("cases[%d]", i)
		id := strings.TrimSpace(c.ID)
		if id == "" {
			return fmt.Errorf("%s.id must not be empty", where)
		}
		if seenCases[id] {
			return fmt.Errorf("%s.id %q is duplicated", where, id)
		}
		seenCases[id] = true
		if c.Findings == nil {
			return fmt.Errorf("%s.findings must be an array", where)
		}

		seenFindings := map[string]bool{}
		for j, prediction := range c.Findings {
			findingWhere := fmt.Sprintf("%s.findings[%d]", where, j)
			predictionID := strings.TrimSpace(prediction.ID)
			if predictionID == "" {
				return fmt.Errorf("%s.id must not be empty", findingWhere)
			}
			if seenFindings[predictionID] {
				return fmt.Errorf("%s.id %q is duplicated in case %q", findingWhere, predictionID, id)
			}
			seenFindings[predictionID] = true
			if strings.TrimSpace(prediction.Title) == "" {
				return fmt.Errorf("%s.title must not be empty", findingWhere)
			}
			if err := validateLocation(prediction.Category, prediction.File, prediction.StartLine, prediction.EndLine); err != nil {
				return fmt.Errorf("%s: %w", findingWhere, err)
			}
		}
	}
	return nil
}

func validateLocation(category interface{ Valid() bool }, file string, startLine, endLine int) error {
	if !category.Valid() {
		return errors.New("category is not recognized")
	}
	if normalizedPath(file) == "" {
		return errors.New("file must be a relative repository path")
	}
	if startLine <= 0 {
		return errors.New("start_line must be greater than zero")
	}
	if endLine < startLine {
		return errors.New("end_line must be greater than or equal to start_line")
	}
	return nil
}

func normalizedPath(file string) string {
	clean := path.Clean(strings.ReplaceAll(strings.TrimSpace(file), "\\", "/"))
	if clean == "." || clean == "" || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return ""
	}
	return clean
}
