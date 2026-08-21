// Package benchmark measures whether ARC finds defects that are known to be there.
//
// Every quality claim about a code reviewer is an argument until it is scored
// against changes whose answer is already known. Hand-labelling real pull requests
// gives the most faithful answer and costs the most human judgement; seeding a
// known fault into real code gives a cheaper answer whose ground truth is
// generated rather than judged, and is therefore not subject to the labeller's
// opinion about what ARC should have found.
//
// A mutant is a real defect in real code: an invariant deleted, a guard inverted,
// an assertion weakened. The diff ARC receives is the diff a developer would have
// produced by making that mistake, and the expected finding is known before the
// review runs. Clean controls — changes that alter nothing meaningful — measure the
// other half, which is whether the reviewer stays quiet when it should.
//
// Nothing here modifies the repository. Mutants are applied to copies in a
// temporary directory, and the diff is computed between the copies.
//
// The whole harness lives in test files on purpose. It writes files — temporary
// copies of the checkout — and the source scan behind this tool's no-write
// invariant covers every non-test file. Allow-listing this package would have
// weakened that invariant to buy a measurement; keeping the harness test-only
// keeps the guarantee exact, and it is honest about what this code is.
package benchmark

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/your-company/agentic-code-review/internal/findings"
)

// Mutant is one seeded change with a known correct answer.
type Mutant struct {
	// ID identifies the case in the report.
	ID string

	// Path is the repository-relative file to mutate.
	Path string

	// Original is the exact text to replace. It must appear exactly once in the
	// file, so a mutant either applies precisely or fails loudly.
	Original string

	// Mutated is what replaces it.
	Mutated string

	// Defect describes, in one line, what was broken. It is for the report only
	// and is never shown to the reviewer.
	Defect string

	// WantCategory is the category a correct finding would carry. Empty for a
	// clean control, where any finding is a false positive.
	WantCategory findings.Category

	// WantTerms are substrings, any one of which in a finding's title or problem
	// marks it as having identified this defect. They are deliberately about the
	// mechanism rather than the wording, so a correct finding phrased differently
	// still counts.
	WantTerms []string

	// Clean marks a control case: a change that breaks nothing. The correct
	// behaviour is to report nothing.
	Clean bool
}

// Change is the diff a mutant produces, in the shape the review pipeline wants.
type Change struct {
	Path      string
	Patch     string
	Additions int
	Deletions int
}

// Apply produces the diff for a mutant without touching the repository.
//
// The original and mutated files are written to a temporary directory and diffed
// there, so a failed run can never leave the working tree modified. The path in
// the resulting patch is rewritten to the repository-relative one, because that is
// what the changed-file scope rule and the retrieval stage key off.
func (m Mutant) Apply(repoRoot string) (Change, error) {
	sourcePath := filepath.Join(repoRoot, filepath.FromSlash(m.Path))
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return Change{}, fmt.Errorf("read %s: %w", m.Path, err)
	}
	original := string(raw)

	if m.Clean && m.Original == "" {
		return Change{}, fmt.Errorf("mutant %s: a clean control still needs a change to make", m.ID)
	}
	if count := strings.Count(original, m.Original); count != 1 {
		return Change{}, fmt.Errorf(
			"mutant %s: the text to replace appears %d times in %s, want exactly once",
			m.ID, count, m.Path)
	}

	mutated := strings.Replace(original, m.Original, m.Mutated, 1)
	if mutated == original {
		return Change{}, fmt.Errorf("mutant %s: the replacement changed nothing", m.ID)
	}

	dir, err := os.MkdirTemp("", "arc-mutation-")
	if err != nil {
		return Change{}, fmt.Errorf("temporary directory: %w", err)
	}
	defer os.RemoveAll(dir)

	base := filepath.Base(m.Path)
	before := filepath.Join(dir, "before_"+base)
	after := filepath.Join(dir, "after_"+base)
	if err := os.WriteFile(before, []byte(original), 0o600); err != nil {
		return Change{}, err
	}
	if err := os.WriteFile(after, []byte(mutated), 0o600); err != nil {
		return Change{}, err
	}

	patch, err := unifiedDiff(before, after, m.Path)
	if err != nil {
		return Change{}, err
	}

	additions, deletions := countChangedLines(patch)
	return Change{Path: m.Path, Patch: patch, Additions: additions, Deletions: deletions}, nil
}

// ApplyToCopy seeds the mutant into a throwaway copy of the repository and returns
// the diff plus the copy's path.
//
// This exists because of a flaw the benchmark found in itself. When the mutation
// lived only in the diff, the verifier read the real file, correctly observed that
// the code the finding described was not there, and refuted every true positive.
// That measured the harness, not the reviewer. A seeded defect has to exist on disk
// for the same reason a real one does: every stage after the diff — verification,
// retrieval, the deterministic checks — reads the checkout.
//
// The caller must invoke cleanup.
func (m Mutant) ApplyToCopy(repoRoot string) (Change, string, func(), error) {
	change, err := m.Apply(repoRoot)
	if err != nil {
		return Change{}, "", func() {}, err
	}

	workdir, err := os.MkdirTemp("", "arc-benchmark-repo-")
	if err != nil {
		return Change{}, "", func() {}, fmt.Errorf("temporary repository: %w", err)
	}
	cleanup := func() { os.RemoveAll(workdir) }

	// cp -a preserves the tree cheaply. The git directory and build output are
	// excluded afterwards: neither is read by a review, and both are large.
	if out, err := exec.Command("cp", "-a", repoRoot+"/.", workdir).CombinedOutput(); err != nil {
		cleanup()
		return Change{}, "", func() {}, fmt.Errorf("copy repository: %v: %s", err, out)
	}
	for _, discard := range []string{".git", "bin"} {
		os.RemoveAll(filepath.Join(workdir, discard))
	}

	target := filepath.Join(workdir, filepath.FromSlash(m.Path))
	raw, err := os.ReadFile(target)
	if err != nil {
		cleanup()
		return Change{}, "", func() {}, fmt.Errorf("read copy of %s: %w", m.Path, err)
	}
	mutated := strings.Replace(string(raw), m.Original, m.Mutated, 1)
	if mutated == string(raw) {
		cleanup()
		return Change{}, "", func() {}, fmt.Errorf("mutant %s did not apply to the copy", m.ID)
	}
	if err := os.WriteFile(target, []byte(mutated), 0o600); err != nil {
		cleanup()
		return Change{}, "", func() {}, fmt.Errorf("write copy of %s: %w", m.Path, err)
	}

	return change, workdir, cleanup, nil
}

// unifiedDiff computes a patch between two files, with the repository-relative path
// substituted into the headers.
//
// git exits 1 when the files differ, which is the expected case here, so only a
// higher status is an error.
func unifiedDiff(before, after, displayPath string) (string, error) {
	cmd := exec.Command("git", "diff", "--no-index", "--unified=8", before, after)
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() > 1 {
			return "", fmt.Errorf("diff %s: %w", displayPath, err)
		}
	}
	if len(out) == 0 {
		return "", fmt.Errorf("diff %s produced no output", displayPath)
	}

	// Rewrite the temporary paths so the patch describes the real file. The
	// changed-file scope rule and retrieval both key off this path.
	var kept []string
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git"),
			strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "similarity "),
			strings.HasPrefix(line, "rename "):
			continue
		case strings.HasPrefix(line, "--- "):
			kept = append(kept, "--- a/"+displayPath)
		case strings.HasPrefix(line, "+++ "):
			kept = append(kept, "+++ b/"+displayPath)
		default:
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n"), nil
}

func countChangedLines(patch string) (additions, deletions int) {
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			additions++
		case strings.HasPrefix(line, "-"):
			deletions++
		}
	}
	return additions, deletions
}

// Outcome is what a review did with one mutant.
type Outcome struct {
	Mutant Mutant

	// Findings are every validated finding the reviewer proposed.
	Findings []findings.Finding

	// Published are the findings publication policy would have shown.
	Published []findings.Finding

	// Detected reports whether a published finding identified the seeded defect.
	Detected bool

	// DetectedPreVerification reports whether the reviewer proposed it at all,
	// before verification and policy. The gap between the two is what the
	// suppression stages cost in recall.
	DetectedPreVerification bool

	// FalsePositives is how many published findings did not concern the defect.
	// On a clean control, every published finding is one.
	FalsePositives int

	// Suppressed records what happened to a finding that identified the defect but
	// was not published, with the reason policy gave. The gap between proposal and
	// publication is where a benchmark earns its cost: a reviewer that finds a
	// defect and a pipeline that withholds it are two different problems, and only
	// this distinction tells them apart.
	Suppressed []string

	// Error is a run that failed rather than a review that found nothing.
	Error string
}

// Identifies reports whether a finding names this mutant's defect.
//
// The test is deliberately generous about wording and strict about location: the
// finding must be about the mutated file, and must mention the mechanism. A finding
// that happens to be in the right file while describing something else is not a
// detection.
func (m Mutant) Identifies(finding findings.Finding) bool {
	if m.Clean {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(finding.File), strings.TrimSpace(m.Path)) {
		return false
	}

	haystack := strings.ToLower(finding.Title + " " + finding.Problem + " " + finding.Impact)
	for _, term := range m.WantTerms {
		if strings.Contains(haystack, strings.ToLower(term)) {
			return true
		}
	}
	return false
}

// Report aggregates outcomes into the numbers worth quoting.
type Report struct {
	Outcomes []Outcome
}

// Metrics is the scorecard for a benchmark run.
type Metrics struct {
	Mutants        int
	Detected       int
	ProposedOnly   int
	CleanCases     int
	CleanQuiet     int
	FalsePositives int
	Failed         int
}

// Metrics computes the scorecard.
func (r Report) Metrics() Metrics {
	var m Metrics
	for _, outcome := range r.Outcomes {
		if outcome.Error != "" {
			m.Failed++
			continue
		}
		if outcome.Mutant.Clean {
			m.CleanCases++
			if len(outcome.Published) == 0 {
				m.CleanQuiet++
			}
			m.FalsePositives += outcome.FalsePositives
			continue
		}

		m.Mutants++
		if outcome.Detected {
			m.Detected++
		} else if outcome.DetectedPreVerification {
			m.ProposedOnly++
		}
		m.FalsePositives += outcome.FalsePositives
	}
	return m
}

// Recall is detected defects over seeded defects.
func (m Metrics) Recall() float64 {
	if m.Mutants == 0 {
		return 0
	}
	return float64(m.Detected) / float64(m.Mutants)
}

// Precision is detections over all published findings.
//
// A false positive here is any published finding that did not concern the seeded
// defect. That is stricter than a human labeller would be — ARC may well have found
// something real that the mutation did not introduce — so this number is a floor
// rather than an estimate.
func (m Metrics) Precision() float64 {
	published := m.Detected + m.FalsePositives
	if published == 0 {
		return 0
	}
	return float64(m.Detected) / float64(published)
}

// CleanRate is how many controls stayed quiet.
func (m Metrics) CleanRate() float64 {
	if m.CleanCases == 0 {
		return 0
	}
	return float64(m.CleanQuiet) / float64(m.CleanCases)
}

// Render writes the report as readable text.
func (r Report) Render() string {
	var out strings.Builder
	metrics := r.Metrics()

	out.WriteString("Mutation benchmark\n\n")
	for _, outcome := range r.Outcomes {
		status := "MISS"
		switch {
		case outcome.Error != "":
			status = "ERROR"
		case outcome.Mutant.Clean && len(outcome.Published) == 0:
			status = "QUIET"
		case outcome.Mutant.Clean:
			status = "NOISE"
		case outcome.Detected:
			status = "FOUND"
		case outcome.DetectedPreVerification:
			status = "SUPPRESSED"
		}

		fmt.Fprintf(&out, "%-11s %-22s %s\n", status, outcome.Mutant.ID, outcome.Mutant.Defect)
		if outcome.Error != "" {
			fmt.Fprintf(&out, "            error: %s\n", outcome.Error)
			continue
		}
		for _, finding := range outcome.Published {
			marker := " "
			if outcome.Mutant.Identifies(finding) {
				marker = "*"
			}
			fmt.Fprintf(&out, "          %s %s %s — %s (%s)\n",
				marker, finding.Severity.Display(), finding.Category, finding.Title, finding.Location())
		}
		for _, suppressed := range outcome.Suppressed {
			fmt.Fprintf(&out, "          ! withheld: %s\n", suppressed)
		}
	}

	fmt.Fprintf(&out, "\nSeeded defects:   %d\n", metrics.Mutants)
	fmt.Fprintf(&out, "Detected:         %d\n", metrics.Detected)
	fmt.Fprintf(&out, "Proposed but suppressed: %d\n", metrics.ProposedOnly)
	fmt.Fprintf(&out, "Clean controls:   %d, stayed quiet: %d\n", metrics.CleanCases, metrics.CleanQuiet)
	fmt.Fprintf(&out, "Unrelated published findings: %d\n", metrics.FalsePositives)
	fmt.Fprintf(&out, "Failed runs:      %d\n", metrics.Failed)
	fmt.Fprintf(&out, "\nRecall:     %.2f\n", metrics.Recall())
	fmt.Fprintf(&out, "Precision:  %.2f (floor; unrelated findings may still be real)\n", metrics.Precision())
	fmt.Fprintf(&out, "Clean rate: %.2f\n", metrics.CleanRate())
	return out.String()
}
