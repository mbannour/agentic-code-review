// Package findings is the domain model for review results.
//
// It is the boundary where a model's prose stops and this application's own data
// begins. Claude analyses, reasons, and proposes candidate findings; everything in
// this package exists to parse that proposal strictly, bound it, and reject what
// does not hold up. Nothing here consults a model, reads the network, or writes
// anything: given the same bytes it always reaches the same conclusion.
//
// A finding describes a problem. It deliberately carries no patch, no replacement
// code, and no command, so a validated ReviewResult can never be mistaken for
// something to execute or apply.
package findings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Category is the closed set of problem classes a finding may report.
type Category string

const (
	CategoryCorrectness     Category = "correctness"
	CategorySecurity        Category = "security"
	CategoryTesting         Category = "testing"
	CategoryArchitecture    Category = "architecture"
	CategoryRequirement     Category = "requirement"
	CategoryMaintainability Category = "maintainability"
)

// Categories lists every allowed category, in reporting priority order.
func Categories() []Category {
	return []Category{
		CategoryCorrectness,
		CategorySecurity,
		CategoryTesting,
		CategoryArchitecture,
		CategoryRequirement,
		CategoryMaintainability,
	}
}

// Valid reports whether c is an allowed category.
func (c Category) Valid() bool {
	for _, allowed := range Categories() {
		if c == allowed {
			return true
		}
	}
	return false
}

// Severity is how much the problem matters. There is deliberately no style
// severity: formatting and naming preferences are not findings.
type Severity string

const (
	SeverityBlocker Severity = "blocker"
	SeverityHigh    Severity = "high"
	SeverityMedium  Severity = "medium"
	SeverityLow     Severity = "low"
)

// Severities lists every allowed severity, most serious first.
func Severities() []Severity {
	return []Severity{SeverityBlocker, SeverityHigh, SeverityMedium, SeverityLow}
}

// Valid reports whether s is an allowed severity.
func (s Severity) Valid() bool {
	for _, allowed := range Severities() {
		if s == allowed {
			return true
		}
	}
	return false
}

// Rank orders severities for deterministic reporting, lower being more serious.
// An unknown severity sorts last; validation rejects it before that matters.
func (s Severity) Rank() int {
	for i, allowed := range Severities() {
		if s == allowed {
			return i
		}
	}
	return len(Severities())
}

// Display renders the severity for terminal output.
func (s Severity) Display() string { return strings.ToUpper(string(s)) }

// EvidenceType is the closed set of things that can substantiate a finding.
type EvidenceType string

const (
	EvidenceCode EvidenceType = "code"
	EvidenceJira EvidenceType = "jira"
	EvidenceRule EvidenceType = "rule"
	// EvidenceDocument is an operator-configured requirement, architecture, or
	// reference document from a source such as Confluence or a customer file.
	EvidenceDocument EvidenceType = "document"
	// EvidenceSchema is observed database schema metadata. It may support a
	// code claim but never substitutes for locating the problem in changed code.
	EvidenceSchema EvidenceType = "schema"
	EvidenceTest   EvidenceType = "test"
	EvidenceVet    EvidenceType = "vet"
)

// EvidenceTypes lists every allowed evidence type.
func EvidenceTypes() []EvidenceType {
	return []EvidenceType{
		EvidenceCode, EvidenceJira, EvidenceRule, EvidenceTest, EvidenceVet,
		EvidenceDocument, EvidenceSchema,
	}
}

// Valid reports whether t is an allowed evidence type.
func (t EvidenceType) Valid() bool {
	for _, allowed := range EvidenceTypes() {
		if t == allowed {
			return true
		}
	}
	return false
}

// Display renders the evidence type for terminal output.
func (t EvidenceType) Display() string { return strings.ToUpper(string(t)) }

// Evidence is one substantiation of a finding: where the support comes from, and
// what it says.
type Evidence struct {
	Type   EvidenceType `json:"type"`
	Source string       `json:"source"`
	Detail string       `json:"detail"`
}

// Finding is one concrete problem in the changed code.
//
// It is explanatory only. Suggestion describes the smallest reasonable
// remediation in prose; it is never a patch, a replacement body, or a command.
type Finding struct {
	ID         string   `json:"id"`
	Category   Category `json:"category"`
	Severity   Severity `json:"severity"`
	Confidence float64  `json:"confidence"`

	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`

	Title      string `json:"title"`
	Problem    string `json:"problem"`
	Impact     string `json:"impact"`
	Suggestion string `json:"suggestion"`

	Evidence []Evidence `json:"evidence"`
}

// Location renders the finding's position as "file:start-end", collapsing a
// single-line range to "file:line".
func (f Finding) Location() string {
	if f.EndLine <= f.StartLine {
		return fmt.Sprintf("%s:%d", f.File, f.StartLine)
	}
	return fmt.Sprintf("%s:%d-%d", f.File, f.StartLine, f.EndLine)
}

// ConfidencePercent renders confidence as whole percentage points.
func (f Finding) ConfidencePercent() int { return int(f.Confidence*100 + 0.5) }

// ReviewResult is the complete outcome of one review. A result with no findings is
// valid and expected: reporting nothing is better than inventing a problem.
type ReviewResult struct {
	Summary  string    `json:"summary"`
	Findings []Finding `json:"findings"`
}

// HasFindings reports whether anything actionable was reported.
func (r ReviewResult) HasFindings() bool { return len(r.Findings) > 0 }

// Count returns the number of findings.
func (r ReviewResult) Count() int { return len(r.Findings) }

// CountBySeverity returns how many findings carry the given severity.
func (r ReviewResult) CountBySeverity(severity Severity) int {
	count := 0
	for _, f := range r.Findings {
		if f.Severity == severity {
			count++
		}
	}
	return count
}

// Sentinel errors callers can match with errors.Is.
var (
	// ErrEmptyResponse means there was nothing to decode.
	ErrEmptyResponse = errors.New("empty review response")

	// ErrNotJSON means the response did not contain a JSON object. Prose and
	// apologies land here. A single exact ```json fence is tolerated because
	// some model versions add one despite an explicit JSON-only instruction.
	ErrNotJSON = errors.New("review response is not a JSON object")

	// ErrMalformedJSON means the JSON could not be decoded.
	ErrMalformedJSON = errors.New("malformed review JSON")

	// ErrUnknownField means the response carried a field this model does not
	// define. It is reported rather than ignored so output drift is visible
	// instead of silently discarded.
	ErrUnknownField = errors.New("unknown field in review JSON")

	// ErrTrailingContent means something followed the JSON object.
	ErrTrailingContent = errors.New("unexpected content after review JSON")
)

// Decode parses a review response strictly.
//
// Strictness is the point. Unknown fields are refused, so a change in what the
// model emits surfaces as an error rather than as a quietly dropped field, and
// prose or a second object before or after it is refused too. One exact JSON
// Markdown fence is tolerated; decoding remains strict inside it. Decoding
// checks shape only; Validate applies the rules.
func Decode(raw string) (ReviewResult, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ReviewResult{}, ErrEmptyResponse
	}
	trimmed = unwrapJSONFence(trimmed)
	if trimmed[0] != '{' {
		return ReviewResult{}, fmt.Errorf("%w: expected the response to start with '{'", ErrNotJSON)
	}

	reader := strings.NewReader(trimmed)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()

	var result ReviewResult
	if err := decoder.Decode(&result); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return ReviewResult{}, fmt.Errorf("%w: %v", ErrUnknownField, err)
		}
		return ReviewResult{}, fmt.Errorf("%w: %v", ErrMalformedJSON, err)
	}

	// Anything the decoder did not consume is either still buffered or still in
	// the reader, so both are checked. A well-formed response has only optional
	// trailing whitespace left.
	rest, err := io.ReadAll(io.MultiReader(decoder.Buffered(), reader))
	if err != nil {
		return ReviewResult{}, fmt.Errorf("%w: %v", ErrMalformedJSON, err)
	}
	if leftover := strings.TrimSpace(string(rest)); leftover != "" {
		return ReviewResult{}, fmt.Errorf("%w: %s", ErrTrailingContent, firstLine(leftover))
	}

	return result, nil
}

// unwrapJSONFence removes one exact Markdown JSON fence and nothing else. The
// decoder remains strict about the enclosed schema and trailing content; this
// only tolerates a presentation wrapper Claude sometimes emits despite the
// response contract saying not to.
func unwrapJSONFence(s string) string {
	const opening = "```json"
	if !strings.HasPrefix(s, opening) {
		return s
	}

	rest := s[len(opening):]
	switch {
	case strings.HasPrefix(rest, "\r\n"):
		rest = rest[2:]
	case strings.HasPrefix(rest, "\n"):
		rest = rest[1:]
	default:
		return s
	}

	rest = strings.TrimSpace(rest)
	if !strings.HasSuffix(rest, "```") {
		return s
	}
	return strings.TrimSpace(strings.TrimSuffix(rest, "```"))
}

// firstLine returns a short, single-line fragment of s for an error message.
func firstLine(s string) string {
	const maxLen = 120

	line := s
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	line = strings.TrimSpace(line)
	if len(line) > maxLen {
		line = line[:maxLen] + "…"
	}
	return line
}
