package evaluation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/your-company/agentic-code-review/internal/findings"
)

// CaptureRequest identifies one captured run. The case ID must match the label
// case this run is scored against; nothing here is inferred, because a snapshot
// filed under the wrong case would silently score against the wrong ground truth.
type CaptureRequest struct {
	RunName       string
	CaseID        string
	Model         string
	PromptVersion string
}

// Snapshot converts one validated review result into a single-case prediction
// set.
//
// It captures what the reviewer proposed and the domain model accepted — not what
// publication policy later decided to show. Those are different questions:
// suppression is measured by reading the policy's own reasons, while precision and
// recall are properties of the proposal. Capturing post-policy findings would make
// every threshold change look like a model change.
//
// A run that found nothing is captured as a case with an empty findings array,
// which is what lets a correctly clean case count as evidence rather than as a
// missing file.
func Snapshot(request CaptureRequest, result findings.ReviewResult) (PredictionSet, error) {
	set := PredictionSet{
		SchemaVersion: SchemaVersion,
		RunName:       strings.TrimSpace(request.RunName),
		Model:         strings.TrimSpace(request.Model),
		PromptVersion: strings.TrimSpace(request.PromptVersion),
		Cases: []PredictionCase{{
			ID:       strings.TrimSpace(request.CaseID),
			Findings: make([]Prediction, 0, len(result.Findings)),
		}},
	}

	for _, finding := range result.Findings {
		set.Cases[0].Findings = append(set.Cases[0].Findings, Prediction{
			ID:        finding.ID,
			Category:  finding.Category,
			File:      finding.File,
			StartLine: finding.StartLine,
			EndLine:   finding.EndLine,
			Title:     finding.Title,
		})
	}

	// A snapshot is held to exactly the same rules as a hand-written prediction
	// file: capture must not be a way to introduce data the evaluator would
	// reject on the way in.
	if err := validatePredictions(set); err != nil {
		return PredictionSet{}, fmt.Errorf("validate captured predictions: %w", err)
	}
	return set, nil
}

// WriteSnapshot writes one prediction set to a new file.
//
// The file must not already exist. Capture is the only thing in this tool that
// writes anything, and refusing to overwrite is what keeps it from being able to
// destroy a label set, an earlier snapshot, or any other file named by mistake:
// a re-run reports the collision instead of replacing evidence.
func WriteSnapshot(path string, set PredictionSet) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("capture path must not be empty")
	}
	if err := validatePredictions(set); err != nil {
		return fmt.Errorf("validate captured predictions: %w", err)
	}

	encoded, err := json.MarshalIndent(set, "", "  ")
	if err != nil {
		return fmt.Errorf("encode captured predictions: %w", err)
	}
	encoded = append(encoded, '\n')

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("capture file %s already exists; capture never overwrites a snapshot", path)
		}
		return fmt.Errorf("create capture file: %w", err)
	}
	defer file.Close()

	if _, err := file.Write(encoded); err != nil {
		return fmt.Errorf("write captured predictions: %w", err)
	}
	return file.Close()
}

// LoadPredictions reads a prediction snapshot from a file, or merges every
// snapshot in a directory into one set.
//
// A labelled suite spans many pull requests, and each `arc review` run can only
// know about its own. The directory form is what turns a set of per-run captures
// into the single input the evaluator scores, without any step that rewrites an
// existing snapshot.
func LoadPredictions(path string) (PredictionSet, error) {
	info, err := os.Stat(path)
	if err != nil {
		return PredictionSet{}, fmt.Errorf("open predictions: %w", err)
	}
	if !info.IsDir() {
		file, err := os.Open(path)
		if err != nil {
			return PredictionSet{}, fmt.Errorf("open predictions: %w", err)
		}
		defer file.Close()
		return DecodePredictions(file)
	}
	return loadPredictionsDir(path)
}

func loadPredictionsDir(dir string) (PredictionSet, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return PredictionSet{}, fmt.Errorf("read predictions directory: %w", err)
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		return PredictionSet{}, fmt.Errorf("predictions directory %s contains no .json snapshot", dir)
	}
	// Stable order keeps a merged set byte-identical between runs, so a diff
	// between two suites is a difference in findings and nothing else.
	sort.Strings(names)

	var merged PredictionSet
	origin := map[string]string{}

	for _, name := range names {
		file, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return PredictionSet{}, fmt.Errorf("open predictions: %w", err)
		}
		set, err := DecodePredictions(file)
		file.Close()
		if err != nil {
			return PredictionSet{}, fmt.Errorf("%s: %w", name, err)
		}

		if merged.SchemaVersion == 0 {
			merged = PredictionSet{
				SchemaVersion: set.SchemaVersion,
				RunName:       set.RunName,
				Model:         set.Model,
				PromptVersion: set.PromptVersion,
				Cases:         []PredictionCase{},
			}
		}

		// Snapshots from different runs describe different systems. Averaging
		// them would report a score no single configuration ever achieved.
		if err := sameRun(merged, set, name); err != nil {
			return PredictionSet{}, err
		}

		for _, c := range set.Cases {
			if previous, seen := origin[c.ID]; seen {
				return PredictionSet{}, fmt.Errorf(
					"case %q appears in both %s and %s; each case may be captured once per run",
					c.ID, previous, name)
			}
			origin[c.ID] = name
			merged.Cases = append(merged.Cases, c)
		}
	}

	sort.Slice(merged.Cases, func(i, j int) bool { return merged.Cases[i].ID < merged.Cases[j].ID })
	return merged, nil
}

func sameRun(merged, set PredictionSet, name string) error {
	for _, field := range []struct {
		label string
		want  string
		got   string
	}{
		{"run_name", merged.RunName, set.RunName},
		{"model", merged.Model, set.Model},
		{"prompt_version", merged.PromptVersion, set.PromptVersion},
	} {
		if field.want != field.got {
			return fmt.Errorf(
				"%s has %s %q but the directory already contains %q; score one run at a time",
				name, field.label, field.got, field.want)
		}
	}
	return nil
}
