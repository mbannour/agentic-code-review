package evaluation

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/findings"
)

func reviewResult(items ...findings.Finding) findings.ReviewResult {
	return findings.ReviewResult{Summary: "captured", Findings: items}
}

func finding(id string, category findings.Category, file string, start, end int, title string) findings.Finding {
	return findings.Finding{
		ID: id, Category: category, Severity: findings.SeverityHigh, Confidence: 0.9,
		File: file, StartLine: start, EndLine: end,
		Title: title, Problem: "problem", Impact: "impact", Suggestion: "suggestion",
		Evidence: []findings.Evidence{{Type: findings.EvidenceCode, Source: file, Detail: "detail"}},
	}
}

func TestSnapshotKeepsOnlyTheFieldsScoringNeeds(t *testing.T) {
	set, err := Snapshot(
		CaptureRequest{RunName: "run-1", CaseID: "acme/payments#123", Model: "m", PromptVersion: "p"},
		reviewResult(finding("COR-001", findings.CategoryCorrectness, "internal/pay/retry.go", 84, 87, "Retries permanent declines")),
	)
	if err != nil {
		t.Fatalf("Snapshot() = %v", err)
	}

	if set.SchemaVersion != SchemaVersion {
		t.Errorf("schema version = %d, want %d", set.SchemaVersion, SchemaVersion)
	}
	if set.RunName != "run-1" || set.Model != "m" || set.PromptVersion != "p" {
		t.Errorf("run metadata = %q/%q/%q", set.RunName, set.Model, set.PromptVersion)
	}
	if len(set.Cases) != 1 || set.Cases[0].ID != "acme/payments#123" {
		t.Fatalf("cases = %+v", set.Cases)
	}

	got := set.Cases[0].Findings
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	want := Prediction{
		ID: "COR-001", Category: findings.CategoryCorrectness,
		File: "internal/pay/retry.go", StartLine: 84, EndLine: 87,
		Title: "Retries permanent declines",
	}
	if got[0] != want {
		t.Errorf("prediction = %+v, want %+v", got[0], want)
	}
}

// A clean case is a result, not a missing file: it is the only way a correctly
// silent review can score as evidence rather than as an absent capture.
func TestSnapshotRecordsAnEmptyCaseExplicitly(t *testing.T) {
	set, err := Snapshot(CaptureRequest{RunName: "run-1", CaseID: "clean"}, reviewResult())
	if err != nil {
		t.Fatalf("Snapshot() = %v", err)
	}
	if set.Cases[0].Findings == nil {
		t.Error("findings is nil; an empty case must encode as []")
	}
	if len(set.Cases[0].Findings) != 0 {
		t.Errorf("findings = %d, want 0", len(set.Cases[0].Findings))
	}
}

func TestSnapshotRejectsWhatTheEvaluatorWouldReject(t *testing.T) {
	cases := []struct {
		name    string
		request CaptureRequest
		result  findings.ReviewResult
	}{
		{"missing run name", CaptureRequest{CaseID: "c"}, reviewResult()},
		{"missing case id", CaptureRequest{RunName: "run-1"}, reviewResult()},
		{
			"absolute path",
			CaptureRequest{RunName: "run-1", CaseID: "c"},
			reviewResult(finding("A", findings.CategoryCorrectness, "/etc/passwd", 1, 1, "t")),
		},
		{
			"duplicate finding id",
			CaptureRequest{RunName: "run-1", CaseID: "c"},
			reviewResult(
				finding("A", findings.CategoryCorrectness, "a.go", 1, 2, "t"),
				finding("A", findings.CategoryCorrectness, "b.go", 3, 4, "u"),
			),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Snapshot(tc.request, tc.result); err == nil {
				t.Fatal("Snapshot() = nil error, want rejection")
			}
		})
	}
}

func TestWriteSnapshotRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.json")

	set, err := Snapshot(CaptureRequest{RunName: "run-1", CaseID: "c"}, reviewResult())
	if err != nil {
		t.Fatalf("Snapshot() = %v", err)
	}
	if err := WriteSnapshot(path, set); err != nil {
		t.Fatalf("WriteSnapshot() = %v", err)
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}

	err = WriteSnapshot(path, set)
	if err == nil {
		t.Fatal("WriteSnapshot() overwrote an existing file")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v, want an already-exists explanation", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if string(before) != string(after) {
		t.Error("the existing snapshot changed")
	}
}

// A written snapshot must survive the strict decoder it will be scored through.
func TestWriteSnapshotRoundTripsThroughTheStrictDecoder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	set, err := Snapshot(
		CaptureRequest{RunName: "run-1", CaseID: "acme/payments#1"},
		reviewResult(finding("SEC-001", findings.CategorySecurity, "a.go", 5, 9, "Token in log")),
	)
	if err != nil {
		t.Fatalf("Snapshot() = %v", err)
	}
	if err := WriteSnapshot(path, set); err != nil {
		t.Fatalf("WriteSnapshot() = %v", err)
	}

	loaded, err := LoadPredictions(path)
	if err != nil {
		t.Fatalf("LoadPredictions() = %v", err)
	}
	if loaded.RunName != set.RunName || len(loaded.Cases) != 1 {
		t.Fatalf("loaded = %+v", loaded)
	}
	if loaded.Cases[0].Findings[0] != set.Cases[0].Findings[0] {
		t.Errorf("prediction changed across the round trip")
	}
}

func writeSet(t *testing.T, dir, name string, set PredictionSet) {
	t.Helper()
	if err := WriteSnapshot(filepath.Join(dir, name), set); err != nil {
		t.Fatalf("WriteSnapshot(%s) = %v", name, err)
	}
}

func snapshotFor(t *testing.T, run, caseID string, items ...findings.Finding) PredictionSet {
	t.Helper()
	set, err := Snapshot(CaptureRequest{RunName: run, CaseID: caseID}, reviewResult(items...))
	if err != nil {
		t.Fatalf("Snapshot() = %v", err)
	}
	return set
}

func TestLoadPredictionsMergesADirectoryInCaseOrder(t *testing.T) {
	dir := t.TempDir()
	writeSet(t, dir, "second.json", snapshotFor(t, "run-1", "b-case",
		finding("COR-001", findings.CategoryCorrectness, "b.go", 2, 3, "b")))
	writeSet(t, dir, "first.json", snapshotFor(t, "run-1", "a-case",
		finding("SEC-001", findings.CategorySecurity, "a.go", 4, 5, "a")))
	// Not a snapshot, and not to be read as one.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}

	merged, err := LoadPredictions(dir)
	if err != nil {
		t.Fatalf("LoadPredictions() = %v", err)
	}
	if merged.RunName != "run-1" {
		t.Errorf("run name = %q", merged.RunName)
	}
	if len(merged.Cases) != 2 {
		t.Fatalf("cases = %d, want 2", len(merged.Cases))
	}
	// Ordered by case ID, not by filename, so a merged suite is reproducible.
	if merged.Cases[0].ID != "a-case" || merged.Cases[1].ID != "b-case" {
		t.Errorf("case order = %q, %q", merged.Cases[0].ID, merged.Cases[1].ID)
	}
}

func TestLoadPredictionsRejectsTheSameCaseTwice(t *testing.T) {
	dir := t.TempDir()
	writeSet(t, dir, "a.json", snapshotFor(t, "run-1", "same-case"))
	writeSet(t, dir, "b.json", snapshotFor(t, "run-1", "same-case"))

	_, err := LoadPredictions(dir)
	if err == nil {
		t.Fatal("LoadPredictions() = nil error, want a duplicate-case rejection")
	}
	if !strings.Contains(err.Error(), "same-case") {
		t.Errorf("error = %v, want it to name the duplicated case", err)
	}
}

// Merging runs would report a score no single configuration achieved.
func TestLoadPredictionsRejectsMixedRuns(t *testing.T) {
	dir := t.TempDir()
	writeSet(t, dir, "a.json", snapshotFor(t, "run-1", "case-a"))
	writeSet(t, dir, "b.json", snapshotFor(t, "run-2", "case-b"))

	_, err := LoadPredictions(dir)
	if err == nil {
		t.Fatal("LoadPredictions() = nil error, want a mixed-run rejection")
	}
	if !strings.Contains(err.Error(), "run_name") {
		t.Errorf("error = %v, want it to name the disagreeing field", err)
	}
}

func TestLoadPredictionsReportsAnEmptyDirectory(t *testing.T) {
	if _, err := LoadPredictions(t.TempDir()); err == nil {
		t.Fatal("LoadPredictions() = nil error, want a no-snapshot rejection")
	}
}

func TestLoadPredictionsReportsAMissingPath(t *testing.T) {
	_, err := LoadPredictions(filepath.Join(t.TempDir(), "absent.json"))
	if err == nil {
		t.Fatal("LoadPredictions() = nil error, want a missing-path rejection")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error = %v, want it to wrap os.ErrNotExist", err)
	}
}
