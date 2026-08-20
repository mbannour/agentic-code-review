package findings

import (
	"errors"
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/contextselect"
	evidencepkg "github.com/your-company/agentic-code-review/internal/evidence"
)

// selectedContext is the pull request the tests review: two changed files with a
// patch, one changed file whose patch was truncated, one changed file the selector
// dropped for budget reasons, and nothing else.
func selectedContext() contextselect.SelectedContext {
	return contextselect.SelectedContext{
		PullRequest: contextselect.PullRequestSummary{
			Owner: "acme", Repository: "payments", Number: 123,
		},
		Files: []contextselect.SelectedFile{
			{
				Path:   "internal/payment/retry.go",
				Status: "modified",
				Patch:  "@@ -80,6 +80,12 @@\n+\tif decline.Permanent {\n+\t\treturn RetryPayment(ctx, p)\n+\t}\n",
			},
			{
				Path:   "internal/payment/retry_test.go",
				Status: "modified",
				Patch:  "@@ -20,4 +20,9 @@\n+func TestRetryDeclined(t *testing.T) {\n",
			},
			{
				Path:      "internal/payment/ledger.go",
				Status:    "modified",
				Patch:     "@@ -1,4 +1,9 @@\n+// partial\n",
				Truncated: true,
			},
		},
		Profile: contextselect.TechnologyProfile{Languages: []string{"go"}},
		Stats: contextselect.SelectionStats{
			CandidateFiles: 4, SelectedFiles: 3, DroppedFiles: 1,
			Dropped: []contextselect.DroppedFile{
				{Path: "internal/payment/audit.go", Reason: "context budget exhausted"},
			},
		},
	}
}

// validFinding is a finding that satisfies every rule. Tests mutate a copy of it,
// so each case differs from a valid finding in exactly one way.
func validFinding() Finding {
	return Finding{
		ID:         "COR-001",
		Category:   CategoryCorrectness,
		Severity:   SeverityHigh,
		Confidence: 0.96,
		File:       "internal/payment/retry.go",
		StartLine:  84,
		EndLine:    87,
		Title:      "Permanent declines enter the retry path",
		Problem:    "The new branch treats a permanent decline as a retryable failure.",
		Impact:     "A declined card can be submitted repeatedly.",
		Suggestion: "Return before entering RetryPayment when the decline is permanent.",
		Evidence: []Evidence{
			{Type: EvidenceCode, Source: "internal/payment/retry.go:84-87",
				Detail: "The decline branch reaches RetryPayment."},
			{Type: EvidenceJira, Source: "PAY-431",
				Detail: "Permanent declines must not be retried."},
		},
	}
}

// validResult wraps validFinding in a complete result.
func validResult() ReviewResult {
	return ReviewResult{
		Summary:  "Found one actionable correctness issue.",
		Findings: []Finding{validFinding()},
	}
}

// resultWith returns a valid result whose single finding has been mutated.
func resultWith(mutate func(f *Finding)) ReviewResult {
	finding := validFinding()
	mutate(&finding)
	return ReviewResult{Summary: "One issue.", Findings: []Finding{finding}}
}

func TestValidateValidResult(t *testing.T) {
	if err := Validate(validResult(), selectedContext()); err != nil {
		t.Fatalf("Validate() rejected a valid result: %v", err)
	}
}

func TestValidateExternalEvidenceProvenance(t *testing.T) {
	selected := selectedContext()
	selected.Evidence = []contextselect.SelectedEvidence{
		{ID: "customer-contract", Kind: evidencepkg.KindRequirement, SourceType: evidencepkg.SourceFile},
		{ID: "stage-schema", Kind: evidencepkg.KindDatabaseSchema, SourceType: evidencepkg.SourcePostgresSchema},
	}

	requirement := validFinding()
	requirement.Category = CategoryRequirement
	requirement.Evidence = []Evidence{
		{Type: EvidenceCode, Source: requirement.File + ":84-87", Detail: "implementation behavior"},
		{Type: EvidenceDocument, Source: "customer-contract", Detail: "explicit requirement"},
	}
	if err := Validate(ReviewResult{Summary: "Requirement mismatch.", Findings: []Finding{requirement}}, selected); err != nil {
		t.Fatalf("Validate() rejected configured requirement evidence: %v", err)
	}

	tests := []struct {
		name     string
		evidence Evidence
		want     string
	}{
		{"invented id", Evidence{Type: EvidenceDocument, Source: "missing", Detail: "claim"}, "not a selected external evidence id"},
		{"schema typed as document", Evidence{Type: EvidenceDocument, Source: "stage-schema", Detail: "claim"}, "must use type schema"},
		{"wrong kind for requirement", Evidence{Type: EvidenceSchema, Source: "stage-schema", Detail: "claim"}, "requirement finding cites database_schema"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := requirement
			candidate.Evidence = []Evidence{{Type: EvidenceCode, Source: candidate.File + ":84", Detail: "code"}, test.evidence}
			err := Validate(ReviewResult{Summary: "Mismatch.", Findings: []Finding{candidate}}, selected)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want %q", err, test.want)
			}
		})
	}
}

// TestValidateZeroFindings is the case that must never be an error: a clean review
// is a result, not a failure.
func TestValidateZeroFindings(t *testing.T) {
	results := []ReviewResult{
		{Summary: "No actionable issues found.", Findings: []Finding{}},
		{Summary: "No actionable issues found."},
	}

	for _, result := range results {
		if err := Validate(result, selectedContext()); err != nil {
			t.Errorf("Validate() rejected a zero-finding result: %v", err)
		}
		if result.HasFindings() {
			t.Error("HasFindings() = true for a zero-finding result")
		}
	}
}

func TestValidateMultipleFindings(t *testing.T) {
	second := validFinding()
	second.ID = "TEST-001"
	second.Category = CategoryTesting
	second.Severity = SeverityMedium
	second.File = "internal/payment/retry_test.go"
	second.StartLine = 21
	second.EndLine = 21
	second.Title = "The permanent decline path is untested"
	second.Evidence = []Evidence{{
		Type: EvidenceTest, Source: "go test ./...",
		Detail: "TestRetryDeclined expected 0 retries but observed 3.",
	}}

	third := validFinding()
	third.ID = "MAINT-001"
	third.Category = CategoryMaintainability
	third.Severity = SeverityLow
	third.File = "internal/payment/ledger.go"
	third.StartLine = 4
	third.EndLine = 4
	third.Title = "The ledger write is unbounded"
	third.Evidence = []Evidence{{
		Type: EvidenceRule, Source: ".ai-review/rules.md",
		Detail: "Ledger writes must be bounded.",
	}}

	result := ReviewResult{
		Summary:  "Three issues.",
		Findings: []Finding{validFinding(), second, third},
	}

	if err := Validate(result, selectedContext()); err != nil {
		t.Fatalf("Validate() rejected three valid findings: %v", err)
	}
	if got := result.Count(); got != 3 {
		t.Errorf("Count() = %d, want 3", got)
	}
	if got := result.CountBySeverity(SeverityHigh); got != 1 {
		t.Errorf("CountBySeverity(high) = %d, want 1", got)
	}
}

// TestValidateRejections is the core table: every rule, one violation each.
func TestValidateRejections(t *testing.T) {
	tests := []struct {
		name   string
		result ReviewResult
		want   string
	}{
		{
			name:   "missing id",
			result: resultWith(func(f *Finding) { f.ID = "  " }),
			want:   "id: must not be empty",
		},
		{
			name:   "oversized id",
			result: resultWith(func(f *Finding) { f.ID = strings.Repeat("X", MaxIDChars+1) }),
			want:   "id: 65 characters exceed the limit of 64",
		},
		{
			name:   "invalid category",
			result: resultWith(func(f *Finding) { f.Category = Category("style") }),
			want:   `category: "style" is not one of`,
		},
		{
			name:   "empty category",
			result: resultWith(func(f *Finding) { f.Category = "" }),
			want:   "category:",
		},
		{
			name:   "invalid severity",
			result: resultWith(func(f *Finding) { f.Severity = Severity("style") }),
			want:   `severity: "style" is not one of`,
		},
		{
			name:   "confidence below zero",
			result: resultWith(func(f *Finding) { f.Confidence = -0.1 }),
			want:   "confidence: -0.1 is below 0.0",
		},
		{
			name:   "confidence above one",
			result: resultWith(func(f *Finding) { f.Confidence = 1.5 }),
			want:   "confidence: 1.5 is above 1.0",
		},
		{
			name:   "missing file",
			result: resultWith(func(f *Finding) { f.File = "" }),
			want:   "file: must not be empty",
		},
		{
			name:   "unchanged file",
			result: resultWith(func(f *Finding) { f.File = "internal/legacy/foo.go" }),
			want:   `"internal/legacy/foo.go" is not a file changed by this pull request`,
		},
		{
			name:   "start line zero",
			result: resultWith(func(f *Finding) { f.StartLine, f.EndLine = 0, 0 }),
			want:   "start_line: 0 must be greater than 0",
		},
		{
			name:   "negative start line",
			result: resultWith(func(f *Finding) { f.StartLine, f.EndLine = -3, -3 }),
			want:   "start_line: -3 must be greater than 0",
		},
		{
			name:   "line beyond the sanity ceiling",
			result: resultWith(func(f *Finding) { f.StartLine, f.EndLine = MaxLine+1, MaxLine+1 }),
			want:   "exceeds the maximum line number",
		},
		{
			name:   "end before start",
			result: resultWith(func(f *Finding) { f.StartLine, f.EndLine = 87, 84 }),
			want:   "end_line: 84 is before start_line 87",
		},
		{
			name:   "lines outside the changed regions",
			result: resultWith(func(f *Finding) { f.StartLine, f.EndLine = 400, 402 }),
			want:   "fall outside the changed regions of internal/payment/retry.go",
		},
		{
			name:   "missing title",
			result: resultWith(func(f *Finding) { f.Title = "" }),
			want:   "title: must not be empty",
		},
		{
			name:   "oversized title",
			result: resultWith(func(f *Finding) { f.Title = strings.Repeat("t", MaxTitleChars+1) }),
			want:   "title: 201 characters exceed the limit of 200",
		},
		{
			name:   "missing problem",
			result: resultWith(func(f *Finding) { f.Problem = "\n" }),
			want:   "problem: must not be empty",
		},
		{
			name:   "oversized problem",
			result: resultWith(func(f *Finding) { f.Problem = strings.Repeat("p", MaxProblemChars+1) }),
			want:   "problem: 2001 characters exceed the limit of 2000",
		},
		{
			name:   "missing impact",
			result: resultWith(func(f *Finding) { f.Impact = "" }),
			want:   "impact: must not be empty",
		},
		{
			name:   "oversized impact",
			result: resultWith(func(f *Finding) { f.Impact = strings.Repeat("i", MaxImpactChars+1) }),
			want:   "impact: 1501 characters exceed the limit of 1500",
		},
		{
			name:   "missing suggestion",
			result: resultWith(func(f *Finding) { f.Suggestion = "" }),
			want:   "suggestion: must not be empty",
		},
		{
			name:   "oversized suggestion",
			result: resultWith(func(f *Finding) { f.Suggestion = strings.Repeat("s", MaxSuggestionChars+1) }),
			want:   "suggestion: 1501 characters exceed the limit of 1500",
		},
		{
			name:   "zero evidence",
			result: resultWith(func(f *Finding) { f.Evidence = nil }),
			want:   "evidence: must contain at least one item",
		},
		{
			name: "too much evidence",
			result: resultWith(func(f *Finding) {
				f.Evidence = make([]Evidence, MaxEvidencePerFinding+1)
				for i := range f.Evidence {
					f.Evidence[i] = Evidence{Type: EvidenceCode, Source: "a.go:1", Detail: "d"}
				}
			}),
			want: "items exceed the limit of 10",
		},
		{
			name: "invalid evidence type",
			result: resultWith(func(f *Finding) {
				f.Evidence = []Evidence{{Type: EvidenceType("guess"), Source: "x", Detail: "y"}}
			}),
			want: `evidence[0].type: "guess" is not one of`,
		},
		{
			name: "missing evidence source",
			result: resultWith(func(f *Finding) {
				f.Evidence = []Evidence{{Type: EvidenceCode, Source: " ", Detail: "y"}}
			}),
			want: "evidence[0].source: must not be empty",
		},
		{
			name: "missing evidence detail",
			result: resultWith(func(f *Finding) {
				f.Evidence = []Evidence{{Type: EvidenceCode, Source: "x", Detail: ""}}
			}),
			want: "evidence[0].detail: must not be empty",
		},
		{
			name: "oversized evidence detail",
			result: resultWith(func(f *Finding) {
				f.Evidence = []Evidence{{Type: EvidenceCode, Source: "x",
					Detail: strings.Repeat("d", MaxEvidenceDetailChars+1)}}
			}),
			want: "evidence[0].detail: 1501 characters exceed the limit of 1500",
		},
		{
			name:   "empty summary",
			result: ReviewResult{Summary: "  ", Findings: []Finding{validFinding()}},
			want:   "summary: must not be empty",
		},
		{
			name: "oversized summary",
			result: ReviewResult{
				Summary:  strings.Repeat("s", MaxSummaryChars+1),
				Findings: []Finding{validFinding()},
			},
			want: "summary: 2001 characters exceed the limit of 2000",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.result, selectedContext())
			if err == nil {
				t.Fatal("Validate() accepted an invalid result")
			}
			if !errors.Is(err, ErrInvalidResult) {
				t.Errorf("Validate() error does not match ErrInvalidResult: %v", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Validate() error = %q, want it to contain %q", err.Error(), tt.want)
			}

			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("Validate() error is not a *ValidationError: %v", err)
			}
			if len(validationErr.Issues) == 0 {
				t.Error("*ValidationError carries no issues")
			}
		})
	}
}

// TestValidateDuplicateID rejects two findings sharing an ID, whatever else
// differs.
func TestValidateDuplicateID(t *testing.T) {
	second := validFinding()
	second.Title = "A different problem in the same place"
	second.StartLine, second.EndLine = 85, 86

	result := ReviewResult{Summary: "Two.", Findings: []Finding{validFinding(), second}}

	err := Validate(result, selectedContext())
	if err == nil {
		t.Fatal("Validate() accepted a duplicate id")
	}
	if !strings.Contains(err.Error(), "duplicates the id of findings[0]") {
		t.Errorf("Validate() error = %q, want a duplicate-id message", err.Error())
	}
}

// TestValidateSemanticDuplicate rejects the same problem reported twice under
// different IDs. Normalization is deliberate: case and spacing in a title do not
// make a second finding.
func TestValidateSemanticDuplicate(t *testing.T) {
	second := validFinding()
	second.ID = "COR-002"
	second.Severity = SeverityMedium
	second.Confidence = 0.5
	second.Title = "  permanent   DECLINES enter the RETRY path "
	second.Problem = "Restated in other words."

	result := ReviewResult{Summary: "Two.", Findings: []Finding{validFinding(), second}}

	err := Validate(result, selectedContext())
	if err == nil {
		t.Fatal("Validate() accepted an obvious duplicate finding")
	}
	if !strings.Contains(err.Error(), "same category, file, start line, and title") {
		t.Errorf("Validate() error = %q, want a duplicate-finding message", err.Error())
	}
}

// TestValidateNotDuplicate keeps the duplicate rule narrow: same title in a
// different place, or a different category, is a different finding.
func TestValidateNotDuplicate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(f *Finding)
	}{
		{name: "different start line", mutate: func(f *Finding) { f.StartLine, f.EndLine = 85, 88 }},
		{name: "different file", mutate: func(f *Finding) { f.File = "internal/payment/retry_test.go"; f.StartLine, f.EndLine = 21, 21 }},
		{name: "different category", mutate: func(f *Finding) { f.Category = CategorySecurity }},
		{name: "different title", mutate: func(f *Finding) { f.Title = "Another problem entirely" }},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			second := validFinding()
			second.ID = "COR-002"
			tt.mutate(&second)

			result := ReviewResult{Summary: "Two.", Findings: []Finding{validFinding(), second}}
			if err := Validate(result, selectedContext()); err != nil {
				t.Errorf("Validate() rejected two distinct findings: %v", err)
			}
		})
	}
}

func TestValidateTooManyFindings(t *testing.T) {
	result := ReviewResult{Summary: "Many."}
	for i := 0; i <= MaxFindings; i++ {
		finding := validFinding()
		finding.ID = "COR-" + string(rune('a'+i))
		finding.StartLine, finding.EndLine = 80+i%12, 80+i%12
		finding.Title = "Problem number " + finding.ID
		result.Findings = append(result.Findings, finding)
	}

	err := Validate(result, selectedContext())
	if err == nil {
		t.Fatal("Validate() accepted more than MaxFindings findings")
	}
	if !strings.Contains(err.Error(), "exceed the limit of 20") {
		t.Errorf("Validate() error = %q, want the findings limit message", err.Error())
	}
}

// TestValidateChangedFileScope covers the boundary that keeps a review on topic.
func TestValidateChangedFileScope(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		line    int
		wantErr bool
	}{
		{name: "selected source file", file: "internal/payment/retry.go", line: 84},
		{name: "selected test file", file: "internal/payment/retry_test.go", line: 21},
		{name: "changed but dropped for budget", file: "internal/payment/audit.go", line: 12},
		{name: "path written with a leading dot slash", file: "./internal/payment/retry.go", line: 84},
		{name: "unchanged file in repository context", file: "internal/legacy/foo.go", line: 12, wantErr: true},
		{name: "invented file", file: "internal/payment/nope.go", line: 1, wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			result := resultWith(func(f *Finding) {
				f.File = tt.file
				f.StartLine, f.EndLine = tt.line, tt.line
			})

			err := Validate(result, selectedContext())
			if tt.wantErr && err == nil {
				t.Fatalf("Validate() accepted a finding against %s", tt.file)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() rejected a finding against changed file %s: %v", tt.file, err)
			}
		})
	}
}

// TestValidateDiffLineTolerance is the deliberate non-rejection: a missing or
// truncated patch is a gap in our own evidence, so the line check stands down
// rather than discarding a finding.
func TestValidateDiffLineTolerance(t *testing.T) {
	selected := selectedContext()
	selected.Files = append(selected.Files, contextselect.SelectedFile{
		Path: "internal/payment/binary.bin", Status: "modified", Patch: "",
	})

	tests := []struct {
		name string
		file string
		line int
	}{
		{name: "patch missing entirely", file: "internal/payment/binary.bin", line: 900},
		{name: "patch truncated", file: "internal/payment/ledger.go", line: 900},
		{name: "file dropped for budget", file: "internal/payment/audit.go", line: 900},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			result := resultWith(func(f *Finding) {
				f.File = tt.file
				f.StartLine, f.EndLine = tt.line, tt.line+2
			})

			if err := Validate(result, selected); err != nil {
				t.Errorf("Validate() over-rejected on incomplete patch data: %v", err)
			}
		})
	}
}

// TestValidateReportsEveryProblem checks that one round surfaces all the drift
// rather than only the first violation.
func TestValidateReportsEveryProblem(t *testing.T) {
	result := resultWith(func(f *Finding) {
		f.ID = ""
		f.Category = "style"
		f.Severity = "nit"
		f.Confidence = 2
		f.Title = ""
		f.Evidence = nil
	})

	err := Validate(result, selectedContext())
	if err == nil {
		t.Fatal("Validate() accepted a thoroughly invalid finding")
	}

	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error is not a *ValidationError: %v", err)
	}
	if len(validationErr.Issues) < 6 {
		t.Errorf("got %d issues, want at least 6: %v", len(validationErr.Issues), err)
	}
}

// TestValidateIsDeterministic guards the property the whole package rests on.
func TestValidateIsDeterministic(t *testing.T) {
	result := resultWith(func(f *Finding) {
		f.Category = "style"
		f.File = "internal/legacy/foo.go"
		f.Evidence = nil
	})

	first := Validate(result, selectedContext()).Error()
	for i := 0; i < 20; i++ {
		if got := Validate(result, selectedContext()).Error(); got != first {
			t.Fatalf("Validate() is not deterministic:\n%s\nvs\n%s", first, got)
		}
	}
}

// TestValidateIgnoresIDPrefix keeps the ID a human convention: the category field
// is what is validated.
func TestValidateIgnoresIDPrefix(t *testing.T) {
	result := resultWith(func(f *Finding) {
		f.ID = "SEC-009"
		f.Category = CategoryCorrectness
	})

	if err := Validate(result, selectedContext()); err != nil {
		t.Errorf("Validate() judged the category by the id prefix: %v", err)
	}
}

func TestDecodeValid(t *testing.T) {
	const raw = `{
  "summary": "Found one actionable correctness issue.",
  "findings": [
    {
      "id": "COR-001",
      "category": "correctness",
      "severity": "high",
      "confidence": 0.96,
      "file": "internal/payment/retry.go",
      "start_line": 84,
      "end_line": 87,
      "title": "Permanent declines enter the retry path",
      "problem": "The new branch treats a permanent decline as a retryable failure.",
      "impact": "A declined card can be submitted repeatedly.",
      "suggestion": "Return before entering RetryPayment when the decline is permanent.",
      "evidence": [
        {"type": "code", "source": "internal/payment/retry.go:84-87", "detail": "The decline branch reaches RetryPayment."},
        {"type": "jira", "source": "PAY-431", "detail": "Permanent declines must not be retried."}
      ]
    }
  ]
}`

	result, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode() returned error: %v", err)
	}
	if result.Summary != "Found one actionable correctness issue." {
		t.Errorf("Summary = %q", result.Summary)
	}
	if result.Count() != 1 {
		t.Fatalf("Count() = %d, want 1", result.Count())
	}

	finding := result.Findings[0]
	if finding.ID != "COR-001" || finding.Category != CategoryCorrectness ||
		finding.Severity != SeverityHigh || finding.Confidence != 0.96 {
		t.Errorf("finding decoded incorrectly: %+v", finding)
	}
	if finding.StartLine != 84 || finding.EndLine != 87 {
		t.Errorf("lines decoded incorrectly: %d-%d", finding.StartLine, finding.EndLine)
	}
	if len(finding.Evidence) != 2 || finding.Evidence[1].Type != EvidenceJira {
		t.Errorf("evidence decoded incorrectly: %+v", finding.Evidence)
	}

	if err := Validate(result, selectedContext()); err != nil {
		t.Errorf("the decoded example does not validate: %v", err)
	}
}

func TestDecodeZeroFindings(t *testing.T) {
	result, err := Decode(`{"summary": "No actionable issues found.", "findings": []}`)
	if err != nil {
		t.Fatalf("Decode() returned error: %v", err)
	}
	if result.HasFindings() {
		t.Error("HasFindings() = true for an empty findings array")
	}
	if err := Validate(result, selectedContext()); err != nil {
		t.Errorf("Validate() rejected a zero-finding result: %v", err)
	}
}

// TestDecodeRejections covers the strict-parsing contract: shape drift is an
// error, never a silently dropped field.
func TestDecodeRejections(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want error
	}{
		{name: "empty", raw: "   ", want: ErrEmptyResponse},
		{name: "prose", raw: "Here is my review:", want: ErrNotJSON},
		{name: "unterminated markdown fence", raw: "```json\n{\"summary\":\"s\",\"findings\":[]}", want: ErrNotJSON},
		{name: "malformed json", raw: `{"summary": "s", "findings": [`, want: ErrMalformedJSON},
		{name: "not an object", raw: `[{"summary": "s"}]`, want: ErrNotJSON},
		{
			name: "wrong field type",
			raw:  `{"summary": "s", "findings": "none"}`,
			want: ErrMalformedJSON,
		},
		{
			name: "unknown top-level field",
			raw:  `{"summary": "s", "findings": [], "verdict": "approve"}`,
			want: ErrUnknownField,
		},
		{
			name: "unknown finding field",
			raw: `{"summary":"s","findings":[{"id":"COR-001","category":"correctness","severity":"high",
				"confidence":0.9,"file":"a.go","start_line":1,"end_line":1,"title":"t","problem":"p",
				"impact":"i","suggestion":"s","evidence":[],"patch":"--- a/a.go"}]}`,
			want: ErrUnknownField,
		},
		{
			name: "unknown evidence field",
			raw: `{"summary":"s","findings":[{"id":"COR-001","category":"correctness","severity":"high",
				"confidence":0.9,"file":"a.go","start_line":1,"end_line":1,"title":"t","problem":"p",
				"impact":"i","suggestion":"s","evidence":[{"type":"code","source":"a.go:1","detail":"d",
				"command":"rm -rf /"}]}]}`,
			want: ErrUnknownField,
		},
		{
			name: "trailing prose",
			raw:  `{"summary": "s", "findings": []}` + "\n\nLet me know if you want more detail.",
			want: ErrTrailingContent,
		},
		{
			name: "trailing json object",
			raw:  `{"summary": "s", "findings": []}{"summary": "other", "findings": []}`,
			want: ErrTrailingContent,
		},
		{
			name: "trailing fence",
			raw:  `{"summary": "s", "findings": []}` + "\n```",
			want: ErrTrailingContent,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode(tt.raw)
			if err == nil {
				t.Fatal("Decode() accepted an invalid response")
			}
			if !errors.Is(err, tt.want) {
				t.Errorf("Decode() error = %v, want it to match %v", err, tt.want)
			}
		})
	}
}

// TestDecodeAcceptsSurroundingWhitespace keeps the strictness about content, not
// about formatting.
func TestDecodeAcceptsSurroundingWhitespace(t *testing.T) {
	if _, err := Decode("\n\t  {\"summary\": \"s\", \"findings\": []}  \n\n"); err != nil {
		t.Errorf("Decode() rejected surrounding whitespace: %v", err)
	}
}

func TestDecodeAcceptsSingleJSONFence(t *testing.T) {
	for _, raw := range []string{
		"```json\n{\"summary\":\"s\",\"findings\":[]}\n```",
		"  ```json\r\n{\"summary\":\"s\",\"findings\":[]}\r\n```  ",
	} {
		result, err := Decode(raw)
		if err != nil {
			t.Fatalf("Decode() rejected fenced JSON: %v", err)
		}
		if result.Summary != "s" || result.Findings == nil || len(result.Findings) != 0 {
			t.Errorf("Decode() = %+v, want the enclosed zero-finding result", result)
		}
	}
}

// TestDecodeDoesNotValidate keeps the two responsibilities separate: shape here,
// rules in Validate.
func TestDecodeDoesNotValidate(t *testing.T) {
	raw := `{"summary":"s","findings":[{"id":"","category":"style","severity":"nit","confidence":9,
		"file":"","start_line":0,"end_line":-1,"title":"","problem":"","impact":"","suggestion":"",
		"evidence":[]}]}`

	result, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode() rejected structurally valid JSON: %v", err)
	}
	if err := Validate(result, selectedContext()); err == nil {
		t.Error("Validate() accepted a result that breaks every rule")
	}
}

func TestEnumsAreClosed(t *testing.T) {
	for _, c := range Categories() {
		if !c.Valid() {
			t.Errorf("Category %q reports itself invalid", c)
		}
	}
	for _, s := range Severities() {
		if !s.Valid() {
			t.Errorf("Severity %q reports itself invalid", s)
		}
	}
	for _, e := range EvidenceTypes() {
		if !e.Valid() {
			t.Errorf("EvidenceType %q reports itself invalid", e)
		}
	}

	for _, invalid := range []Category{"", "style", "nit", "Correctness", "performance"} {
		if invalid.Valid() {
			t.Errorf("Category %q reports itself valid", invalid)
		}
	}
	// There is deliberately no style severity.
	for _, invalid := range []Severity{"", "style", "critical", "High", "info"} {
		if invalid.Valid() {
			t.Errorf("Severity %q reports itself valid", invalid)
		}
	}
	for _, invalid := range []EvidenceType{"", "guess", "web", "Code"} {
		if invalid.Valid() {
			t.Errorf("EvidenceType %q reports itself valid", invalid)
		}
	}
}

func TestParseHunkRanges(t *testing.T) {
	tests := []struct {
		name  string
		patch string
		want  []lineRange
	}{
		{
			name:  "single hunk with a count",
			patch: "@@ -80,6 +80,12 @@ func Retry() {\n+\tx\n",
			want:  []lineRange{{start: 80, end: 91}},
		},
		{
			name:  "hunk without a count",
			patch: "@@ -1 +1 @@\n-a\n+b\n",
			want:  []lineRange{{start: 1, end: 1}},
		},
		{
			name:  "two hunks are sorted",
			patch: "@@ -200,4 +210,4 @@\n+b\n@@ -10,2 +10,3 @@\n+a\n",
			want:  []lineRange{{start: 10, end: 12}, {start: 210, end: 213}},
		},
		{
			name:  "zero-length hunk keeps an anchor line",
			patch: "@@ -5,3 +5,0 @@\n-gone\n",
			want:  []lineRange{{start: 5, end: 5}},
		},
		{name: "no hunk header", patch: "not a diff at all\n", want: nil},
		{name: "unparseable header", patch: "@@ -a,b +c,d @@\n", want: nil},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := parseHunkRanges(tt.patch)
			if len(got) != len(tt.want) {
				t.Fatalf("parseHunkRanges() = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("range %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
