package findings

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/your-company/agentic-code-review/internal/contextselect"
	evidencepkg "github.com/your-company/agentic-code-review/internal/evidence"
)

// Limits bound a review result. They exist so a malfunctioning or manipulated
// model cannot produce output large enough to flood the terminal, a later
// publishing step, or this process's memory. They are centralized here so every
// stage agrees on the same ceilings.
const (
	// MaxFindings is the largest number of findings one review may report.
	MaxFindings = 20

	// MaxEvidencePerFinding bounds how much substantiation one finding may carry.
	MaxEvidencePerFinding = 10

	MaxSummaryChars    = 2000
	MaxIDChars         = 64
	MaxFileChars       = 512
	MaxTitleChars      = 200
	MaxProblemChars    = 2000
	MaxImpactChars     = 1500
	MaxSuggestionChars = 1500

	MaxEvidenceSourceChars = 500
	MaxEvidenceDetailChars = 1500

	// MaxLine is a sanity ceiling on line numbers. No real source file is this
	// long, so a larger value means the model invented a location.
	MaxLine = 1_000_000
)

// ErrInvalidResult is the sentinel every validation failure wraps.
var ErrInvalidResult = errors.New("invalid review result")

// Issue is one specific reason a result was rejected.
type Issue struct {
	// Index is the position of the offending finding, or -1 for a result-level
	// problem such as an oversized summary.
	Index int

	// FindingID is the offending finding's ID when it has one.
	FindingID string

	// Field names the offending field, in its JSON spelling.
	Field string

	// Message states what is wrong.
	Message string
}

// String renders one issue as a stable single line.
func (i Issue) String() string {
	var where string
	switch {
	case i.Index < 0:
		where = "result"
	case i.FindingID != "":
		where = fmt.Sprintf("findings[%d] (%s)", i.Index, i.FindingID)
	default:
		where = fmt.Sprintf("findings[%d]", i.Index)
	}

	if i.Field == "" {
		return fmt.Sprintf("%s: %s", where, i.Message)
	}
	return fmt.Sprintf("%s: %s: %s", where, i.Field, i.Message)
}

// ValidationError reports every rule a result broke, not just the first, so one
// review round surfaces all the drift at once.
type ValidationError struct {
	Issues []Issue
}

// Error renders the issues deterministically, in finding order.
func (e *ValidationError) Error() string {
	if len(e.Issues) == 0 {
		return ErrInvalidResult.Error()
	}

	lines := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		lines = append(lines, issue.String())
	}

	return fmt.Sprintf("%s: %d problem(s):\n  - %s",
		ErrInvalidResult.Error(), len(e.Issues), strings.Join(lines, "\n  - "))
}

// Unwrap lets callers match ErrInvalidResult with errors.Is.
func (e *ValidationError) Unwrap() error { return ErrInvalidResult }

// Validator applies the review rules to a decoded result.
//
// It is a value type with no state and no dependencies: the same result and the
// same selected context always produce the same verdict.
type Validator struct{}

// NewValidator returns a Validator.
func NewValidator() Validator { return Validator{} }

// Validate checks result against the rules and against what the pull request
// actually changed. It is the package-level entry point; the Validator method
// exists for callers that prefer to hold the dependency explicitly.
func Validate(result ReviewResult, selected contextselect.SelectedContext) error {
	return NewValidator().Validate(result, selected)
}

// Validate reports every rule violation in result.
func (v Validator) Validate(result ReviewResult, selected contextselect.SelectedContext) error {
	var issues []Issue

	if n := utf8.RuneCountInString(result.Summary); n > MaxSummaryChars {
		issues = append(issues, Issue{Index: -1, Field: "summary",
			Message: tooLong(n, MaxSummaryChars)})
	}
	if strings.TrimSpace(result.Summary) == "" {
		issues = append(issues, Issue{Index: -1, Field: "summary", Message: "must not be empty"})
	}
	if len(result.Findings) > MaxFindings {
		issues = append(issues, Issue{Index: -1, Field: "findings",
			Message: fmt.Sprintf("%d findings exceed the limit of %d", len(result.Findings), MaxFindings)})
	}

	changed := changedFiles(selected)
	patches := patchRanges(selected)
	external := externalEvidenceByID(selected)

	seenIDs := map[string]int{}
	seenKeys := map[string]int{}

	for i, finding := range result.Findings {
		issues = append(issues, v.validateFinding(i, finding, changed, patches, external)...)

		id := strings.TrimSpace(finding.ID)
		if id != "" {
			if first, duplicate := seenIDs[id]; duplicate {
				issues = append(issues, Issue{Index: i, FindingID: id, Field: "id",
					Message: fmt.Sprintf("duplicates the id of findings[%d]", first)})
			} else {
				seenIDs[id] = i
			}
		}

		// Two findings that name the same problem at the same place are the same
		// finding, whatever their IDs say.
		key := duplicateKey(finding)
		if first, duplicate := seenKeys[key]; duplicate {
			issues = append(issues, Issue{Index: i, FindingID: id,
				Message: fmt.Sprintf("duplicates findings[%d]: same category, file, start line, and title", first)})
		} else {
			seenKeys[key] = i
		}
	}

	if len(issues) == 0 {
		return nil
	}
	return &ValidationError{Issues: issues}
}

// validateFinding checks one finding's structure, semantics, and location.
func (v Validator) validateFinding(
	index int,
	finding Finding,
	changed map[string]bool,
	patches map[string][]lineRange,
	external map[string]contextselect.SelectedEvidence,
) []Issue {
	id := strings.TrimSpace(finding.ID)
	var issues []Issue

	add := func(field, message string) {
		issues = append(issues, Issue{Index: index, FindingID: id, Field: field, Message: message})
	}

	if id == "" {
		add("id", "must not be empty")
	} else if n := utf8.RuneCountInString(id); n > MaxIDChars {
		add("id", tooLong(n, MaxIDChars))
	}

	// The ID prefix is a human convention only. The category field is what
	// decides the category.
	if !finding.Category.Valid() {
		add("category", fmt.Sprintf("%q is not one of %s",
			string(finding.Category), joinCategories()))
	}
	if !finding.Severity.Valid() {
		add("severity", fmt.Sprintf("%q is not one of %s",
			string(finding.Severity), joinSeverities()))
	}

	// Confidence is evidence strength, independent of severity.
	switch {
	case finding.Confidence < 0:
		add("confidence", fmt.Sprintf("%s is below 0.0", formatFloat(finding.Confidence)))
	case finding.Confidence > 1:
		add("confidence", fmt.Sprintf("%s is above 1.0", formatFloat(finding.Confidence)))
	}

	issues = append(issues, v.validateLocation(index, id, finding, changed, patches)...)

	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{"title", finding.Title, MaxTitleChars},
		{"problem", finding.Problem, MaxProblemChars},
		{"impact", finding.Impact, MaxImpactChars},
		{"suggestion", finding.Suggestion, MaxSuggestionChars},
	} {
		if strings.TrimSpace(field.value) == "" {
			add(field.name, "must not be empty")
			continue
		}
		if n := utf8.RuneCountInString(field.value); n > field.limit {
			add(field.name, tooLong(n, field.limit))
		}
	}

	if len(finding.Evidence) == 0 {
		add("evidence", "must contain at least one item")
	}
	if len(finding.Evidence) > MaxEvidencePerFinding {
		add("evidence", fmt.Sprintf("%d items exceed the limit of %d",
			len(finding.Evidence), MaxEvidencePerFinding))
	}

	for j, evidence := range finding.Evidence {
		field := "evidence[" + strconv.Itoa(j) + "]"

		if !evidence.Type.Valid() {
			add(field+".type", fmt.Sprintf("%q is not one of %s",
				string(evidence.Type), joinEvidenceTypes()))
		}
		if strings.TrimSpace(evidence.Source) == "" {
			add(field+".source", "must not be empty")
		} else if n := utf8.RuneCountInString(evidence.Source); n > MaxEvidenceSourceChars {
			add(field+".source", tooLong(n, MaxEvidenceSourceChars))
		}
		if strings.TrimSpace(evidence.Detail) == "" {
			add(field+".detail", "must not be empty")
		} else if n := utf8.RuneCountInString(evidence.Detail); n > MaxEvidenceDetailChars {
			add(field+".detail", tooLong(n, MaxEvidenceDetailChars))
		}

		if evidence.Type == EvidenceDocument || evidence.Type == EvidenceSchema {
			sourceID := strings.TrimSpace(evidence.Source)
			document, found := external[sourceID]
			if !found {
				add(field+".source", fmt.Sprintf("%q is not a selected external evidence id", sourceID))
				continue
			}
			if evidence.Type == EvidenceSchema && document.Kind != evidencepkg.KindDatabaseSchema {
				add(field+".type", fmt.Sprintf("schema evidence %q is kind %s", sourceID, document.Kind))
			}
			if evidence.Type == EvidenceDocument && document.Kind == evidencepkg.KindDatabaseSchema {
				add(field+".type", fmt.Sprintf("database schema evidence %q must use type schema", sourceID))
			}
			if finding.Category == CategoryRequirement && document.Kind != evidencepkg.KindRequirement {
				add(field+".source", fmt.Sprintf("requirement finding cites %s evidence %q", document.Kind, sourceID))
			}
			if finding.Category == CategoryArchitecture && document.Kind != evidencepkg.KindArchitecture {
				add(field+".source", fmt.Sprintf("architecture finding cites %s evidence %q", document.Kind, sourceID))
			}
		}
	}

	return issues
}

// externalEvidenceByID is the closed namespace the reviewer may cite. Exact ids
// make provenance deterministic and prevent a model from inventing a document
// path or silently substituting a similarly named source.
func externalEvidenceByID(selected contextselect.SelectedContext) map[string]contextselect.SelectedEvidence {
	documents := make(map[string]contextselect.SelectedEvidence, len(selected.Evidence))
	for _, document := range selected.Evidence {
		documents[document.ID] = document
	}
	return documents
}

// validateLocation enforces the changed-file restriction and the line structure.
//
// The changed-file rule is what keeps a review about this pull request: a problem
// in a file the pull request never touched is not this review's business, however
// real it may be, and admitting such findings is how unrelated legacy noise gets
// in.
func (v Validator) validateLocation(
	index int,
	id string,
	finding Finding,
	changed map[string]bool,
	patches map[string][]lineRange,
) []Issue {
	var issues []Issue

	add := func(field, message string) {
		issues = append(issues, Issue{Index: index, FindingID: id, Field: field, Message: message})
	}

	file := strings.TrimSpace(finding.File)
	switch {
	case file == "":
		add("file", "must not be empty")
	case utf8.RuneCountInString(file) > MaxFileChars:
		add("file", tooLong(utf8.RuneCountInString(file), MaxFileChars))
	case !changed[normalizePath(file)]:
		add("file", fmt.Sprintf("%q is not a file changed by this pull request", file))
	}

	switch {
	case finding.StartLine <= 0:
		add("start_line", fmt.Sprintf("%d must be greater than 0", finding.StartLine))
	case finding.StartLine > MaxLine:
		add("start_line", fmt.Sprintf("%d exceeds the maximum line number %d", finding.StartLine, MaxLine))
	}
	if finding.EndLine < finding.StartLine {
		add("end_line", fmt.Sprintf("%d is before start_line %d", finding.EndLine, finding.StartLine))
	}
	if finding.EndLine > MaxLine {
		add("end_line", fmt.Sprintf("%d exceeds the maximum line number %d", finding.EndLine, MaxLine))
	}

	// A structurally valid line is checked against the diff only when the patch
	// is actually available and complete. A missing or truncated patch is a gap
	// in our own evidence, not a reason to discard a finding; the exact inline
	// location check belongs to the publishing step.
	if finding.StartLine > 0 && finding.EndLine >= finding.StartLine {
		if ranges, ok := patches[normalizePath(file)]; ok && len(ranges) > 0 {
			if !overlapsAny(ranges, finding.StartLine, finding.EndLine) {
				add("start_line", fmt.Sprintf("lines %d-%d fall outside the changed regions of %s (%s)",
					finding.StartLine, finding.EndLine, file, renderRanges(ranges)))
			}
		}
	}

	return issues
}

// lineRange is an inclusive span of new-file line numbers covered by a patch hunk.
type lineRange struct {
	start int
	end   int
}

// changedFiles is the set of paths this pull request touched, including files the
// selector had to drop for budget reasons: the budget decides what Claude could
// read, not what counts as changed.
func changedFiles(selected contextselect.SelectedContext) map[string]bool {
	changed := make(map[string]bool, len(selected.Files)+len(selected.Stats.Dropped))

	for _, f := range selected.Files {
		changed[normalizePath(f.Path)] = true
	}
	for _, d := range selected.Stats.Dropped {
		changed[normalizePath(d.Path)] = true
	}
	return changed
}

// patchRanges maps each selected file to the new-file line spans its patch covers.
// Files without a usable patch are absent, which disables the line check for them.
func patchRanges(selected contextselect.SelectedContext) map[string][]lineRange {
	ranges := make(map[string][]lineRange)

	for _, f := range selected.Files {
		if f.Patch == "" || f.Truncated {
			continue
		}
		if hunks := parseHunkRanges(f.Patch); len(hunks) > 0 {
			ranges[normalizePath(f.Path)] = hunks
		}
	}
	return ranges
}

// parseHunkRanges reads the new-file spans from a unified diff's hunk headers,
// which look like "@@ -10,6 +10,12 @@". Anything it cannot parse is skipped, so a
// diff in an unexpected shape simply yields no constraint.
func parseHunkRanges(patch string) []lineRange {
	var ranges []lineRange

	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, "@@") {
			continue
		}

		plus := strings.Index(line, "+")
		if plus < 0 {
			continue
		}
		spec := line[plus+1:]
		if idx := strings.IndexAny(spec, " \t"); idx >= 0 {
			spec = spec[:idx]
		}

		start, count := spec, "1"
		if idx := strings.Index(spec, ","); idx >= 0 {
			start, count = spec[:idx], spec[idx+1:]
		}

		startLine, err := strconv.Atoi(start)
		if err != nil || startLine <= 0 {
			continue
		}
		length, err := strconv.Atoi(count)
		if err != nil || length < 0 {
			continue
		}
		if length == 0 {
			// A zero-length hunk describes a pure deletion; the surrounding line
			// is still a reasonable anchor.
			length = 1
		}

		ranges = append(ranges, lineRange{start: startLine, end: startLine + length - 1})
	}

	sort.Slice(ranges, func(i, j int) bool { return ranges[i].start < ranges[j].start })
	return ranges
}

// overlapsAny reports whether [start,end] intersects any of the ranges.
func overlapsAny(ranges []lineRange, start, end int) bool {
	for _, r := range ranges {
		if start <= r.end && end >= r.start {
			return true
		}
	}
	return false
}

// renderRanges describes the changed spans for an error message.
func renderRanges(ranges []lineRange) string {
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		parts = append(parts, fmt.Sprintf("%d-%d", r.start, r.end))
	}
	return strings.Join(parts, ", ")
}

// duplicateKey is the deterministic identity of a finding for duplicate
// detection: the same class of problem, in the same place, under the same title.
// No model and no embedding is involved.
func duplicateKey(finding Finding) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(string(finding.Category))),
		normalizePath(finding.File),
		strconv.Itoa(finding.StartLine),
		normalizeText(finding.Title),
	}, "\x00")
}

// normalizePath makes path comparison insensitive to a leading "./" and to
// surrounding whitespace.
func normalizePath(path string) string {
	trimmed := strings.TrimSpace(path)
	return strings.TrimPrefix(trimmed, "./")
}

// normalizeText collapses case and whitespace so cosmetic differences in a title
// do not create a second copy of the same finding.
func normalizeText(text string) string {
	return strings.Join(strings.Fields(strings.ToLower(text)), " ")
}

// tooLong renders a length-limit message.
func tooLong(actual, limit int) string {
	return fmt.Sprintf("%d characters exceed the limit of %d", actual, limit)
}

// formatFloat renders a confidence value without exponent noise.
func formatFloat(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func joinCategories() string {
	parts := make([]string, 0, len(Categories()))
	for _, c := range Categories() {
		parts = append(parts, string(c))
	}
	return strings.Join(parts, ", ")
}

func joinSeverities() string {
	parts := make([]string, 0, len(Severities()))
	for _, s := range Severities() {
		parts = append(parts, string(s))
	}
	return strings.Join(parts, ", ")
}

func joinEvidenceTypes() string {
	parts := make([]string, 0, len(EvidenceTypes()))
	for _, t := range EvidenceTypes() {
		parts = append(parts, string(t))
	}
	return strings.Join(parts, ", ")
}
