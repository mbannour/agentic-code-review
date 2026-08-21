// Package contextselect decides, deterministically, what a later review stage is
// allowed to see.
//
// It reduces the search space before any model is involved: changed files are
// classified and ranked, repository rules and analysis evidence are bounded, and
// everything is fitted into an explicit byte budget. Nothing here consults a
// model, and nothing here reads the repository — it works only on the normalized
// data already gathered in review.Context and analysis.Result.
package contextselect

import (
	"strconv"

	"github.com/your-company/agentic-code-review/internal/evidence"
	"github.com/your-company/agentic-code-review/internal/retrieval"
)

// FileKind is the deterministic classification of a changed file.
type FileKind string

const (
	FileKindSource        FileKind = "source"
	FileKindTest          FileKind = "test"
	FileKindConfig        FileKind = "config"
	FileKindDocumentation FileKind = "documentation"
	FileKindGenerated     FileKind = "generated"
	FileKindDependency    FileKind = "dependency"
	FileKindMigration     FileKind = "migration"
	FileKindUnknown       FileKind = "unknown"
)

// Importance is how strongly a file should be retained under budget pressure.
type Importance string

const (
	ImportanceHigh   Importance = "high"
	ImportanceMedium Importance = "medium"
	ImportanceLow    Importance = "low"
)

// Display renders the importance for a summary line.
func (i Importance) Display() string {
	switch i {
	case ImportanceHigh:
		return "HIGH"
	case ImportanceMedium:
		return "MED"
	case ImportanceLow:
		return "LOW"
	default:
		return "?"
	}
}

// SelectedContext is the bounded, ranked view handed to a later review stage.
type SelectedContext struct {
	PullRequest PullRequestSummary

	// Ticket is nil when no Jira issue is associated with the pull request.
	Ticket *TicketSummary

	Files    []SelectedFile
	Rules    []SelectedRule
	Evidence []SelectedEvidence
	Analysis []SelectedAnalysis

	// Discussion is the human conversation on the pull request, in the order it
	// will be presented: conversation comments, then comments on diff lines.
	Discussion []SelectedComment

	// ProposedRuleChanges are the review-policy changes this pull request proposes.
	// They are metadata for a human, not criteria for a reviewer.
	ProposedRuleChanges []ProposedRuleChange

	// Code is unchanged repository code retrieved as context. It is never part of
	// the change under review, and a finding may never be attributed to it.
	Code []SelectedCode

	Profile TechnologyProfile

	// Focus is the change-risk assessment and the specialist perspectives it
	// selected. It is assigned by the orchestrator after selection, because what
	// to look for is decided from the change itself rather than from the budget.
	Focus ReviewFocus

	Stats SelectionStats
}

// PullRequestSummary is the pull request metadata worth carrying forward.
type PullRequestSummary struct {
	Owner      string
	Repository string
	Number     int
	Title      string
	Author     string
	BaseBranch string
	HeadBranch string
	HeadSHA    string
	Draft      bool

	// Description is the pull request body: what the author says this change is
	// for. Untrusted text, bounded by the discussion budget.
	Description string

	// DescriptionTruncated reports whether Description was cut to fit.
	DescriptionTruncated bool
}

// Slug renders the repository as "owner/repo".
func (p PullRequestSummary) Slug() string { return p.Owner + "/" + p.Repository }

// TicketSummary is the Jira context. Key and Summary are never dropped.
type TicketSummary struct {
	Key         string
	Summary     string
	Description string
	Status      string
	IssueType   string
	Priority    string
	Labels      []string
	ParentKey   string

	// DescriptionTruncated reports whether Description was cut to fit.
	DescriptionTruncated bool
}

// SelectedFile is one changed file kept in the selection.
type SelectedFile struct {
	Path   string
	Status string
	Patch  string

	Kind       FileKind
	Importance Importance

	// Reason explains, in plain language, why this file was selected. It exists
	// to make the selection debuggable later.
	Reason string

	// OriginalBytes is the patch size before any truncation.
	OriginalBytes int

	// Truncated reports whether Patch was cut to fit the budget.
	Truncated bool
}

// DroppedFile records a candidate that did not fit, and why.
type DroppedFile struct {
	Path       string
	Kind       FileKind
	Importance Importance
	Reason     string
	Bytes      int
}

// SelectedRule is one repository rule document kept in the selection.
type SelectedRule struct {
	Path    string
	Content string

	// Revision and Ref record which revision supplied this guidance. Authoritative
	// rules come from the base branch; see review.RuleContext.
	Revision string
	Ref      string

	OriginalBytes int
	Truncated     bool
}

// SelectedComment is one human remark kept in the selection.
type SelectedComment struct {
	Kind   string
	Author string
	Body   string

	Path string
	Line int

	Truncated bool
}

// Location renders an inline comment's position, or an empty string.
func (c SelectedComment) Location() string {
	if c.Path == "" {
		return ""
	}
	if c.Line <= 0 {
		return c.Path
	}
	return c.Path + ":" + strconv.Itoa(c.Line)
}

// ProposedRuleChange is a rule file the change under review touches, carried
// without its content: a proposed rule is reported, never applied.
type ProposedRuleChange struct {
	Path      string
	Kind      string
	BaseBytes int
	HeadBytes int
}

// SelectedEvidence is one bounded external document retained for review.
type SelectedEvidence struct {
	ID         string
	Kind       evidence.Kind
	SourceType evidence.SourceType
	Locator    string
	Title      string
	Revision   string
	Content    string
	Digest     string

	OriginalBytes int
	Truncated     bool
}

// SelectedCode is one bounded region of unchanged code kept for context.
//
// It carries the symbol and relation that justified retrieving it, so a later
// stage can say why the reviewer was shown this and a reader can disagree.
type SelectedCode struct {
	Symbol   string
	Relation retrieval.Relation

	Path      string
	StartLine int
	EndLine   int
	Content   string

	OriginalBytes int
	Truncated     bool
}

// Location renders the region's position as "path:start-end".
func (c SelectedCode) Location() string {
	if c.EndLine <= c.StartLine {
		return c.Path + ":" + strconv.Itoa(c.StartLine)
	}
	return c.Path + ":" + strconv.Itoa(c.StartLine) + "-" + strconv.Itoa(c.EndLine)
}

// SelectedAnalysis is the evidence from one deterministic check. Output is empty
// for a passing check — that it passed is the whole message — and holds a bounded
// snippet for a failing one.
type SelectedAnalysis struct {
	Name     string
	Command  string
	Passed   bool
	Skipped  bool
	TimedOut bool
	ExitCode int
	Output   string

	OriginalBytes int
	Truncated     bool
}

// TechnologyProfile is the normalized view of what the repository is built with, as
// carried into the review.
//
// It mirrors technology.Profile in string form so that later stages depend on plain
// data rather than on the detector. Build systems are reported alongside languages
// because the two decide different things: the language shapes the review guidance,
// the build system decides which commands ran.
type TechnologyProfile struct {
	Languages    []string
	BuildSystems []string
	Frameworks   []string
	Libraries    []string
}

// Empty reports whether nothing was detected.
func (p TechnologyProfile) Empty() bool {
	return len(p.Languages) == 0 && len(p.BuildSystems) == 0 &&
		len(p.Frameworks) == 0 && len(p.Libraries) == 0
}

// HasLanguage reports whether the named language was detected.
func (p TechnologyProfile) HasLanguage(name string) bool { return contains(p.Languages, name) }

// HasBuildSystem reports whether the named build system was detected.
func (p TechnologyProfile) HasBuildSystem(name string) bool { return contains(p.BuildSystems, name) }

// Technologies returns the frameworks and libraries as one list, frameworks first.
// It is what review guidance keys off when the distinction does not matter.
func (p TechnologyProfile) Technologies() []string {
	out := make([]string, 0, len(p.Frameworks)+len(p.Libraries))
	out = append(out, p.Frameworks...)
	for _, library := range p.Libraries {
		if !contains(out, library) {
			out = append(out, library)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// HasTechnology reports whether the named framework or library was detected.
func (p TechnologyProfile) HasTechnology(name string) bool {
	return contains(p.Frameworks, name) || contains(p.Libraries, name)
}

// ReviewFocus is the deterministic assessment of what this change touches and
// which review perspectives it therefore deserves.
//
// It mirrors changerisk.Profile and the routing plan in plain strings, so this
// package and everything downstream depend on data rather than on those packages.
// It is guidance about where to look — never a claim that a defect exists.
type ReviewFocus struct {
	RiskLevel   string
	RiskAreas   []string
	RiskReasons []string

	// Specialists are the perspectives selected for this change, in routing order.
	Specialists []FocusSpecialist
}

// Empty reports whether no focus was computed.
func (f ReviewFocus) Empty() bool { return f.RiskLevel == "" && len(f.Specialists) == 0 }

// FocusSpecialist is one selected review perspective.
type FocusSpecialist struct {
	ID      string
	Title   string
	Purpose string
	Focus   []string
	Reasons []string
}

// SelectionStats describes what the selection kept and what it left behind.
type SelectionStats struct {
	CandidateFiles int
	SelectedFiles  int
	DroppedFiles   int

	CandidateEvidence int
	SelectedEvidence  int
	DroppedEvidence   int

	// Retrieval describes the unchanged-code section. RetrievalSkipped carries the
	// reason retrieval produced nothing, so an empty section is explained rather
	// than looking like a failure.
	CandidateCode    int
	SelectedCode     int
	DroppedCode      int
	RetrievalSkipped string

	// Discussion counts describe the human conversation.
	CandidateComments int
	SelectedComments  int

	// OriginalBytes is the total size of everything offered to the selector:
	// every candidate patch, rule document, external evidence document, analysis
	// snippet, and the Jira description.
	OriginalBytes int

	// SelectedBytes is the total size of everything kept. It never exceeds the
	// budget.
	SelectedBytes int

	// BudgetBytes is the ceiling that was applied.
	BudgetBytes int

	// Truncated reports whether anything at all had to be cut.
	Truncated bool

	// Dropped lists the candidates that did not fit, in priority order.
	Dropped []DroppedFile
}

// HasTicket reports whether Jira context is present.
func (c SelectedContext) HasTicket() bool { return c.Ticket != nil }

// FilesByImportance returns the selected files carrying the given importance, in
// selection order.
func (c SelectedContext) FilesByImportance(importance Importance) []SelectedFile {
	var files []SelectedFile
	for _, f := range c.Files {
		if f.Importance == importance {
			files = append(files, f)
		}
	}
	return files
}

// FailedAnalysis returns the checks that ran and did not pass.
func (c SelectedContext) FailedAnalysis() []SelectedAnalysis {
	var failed []SelectedAnalysis
	for _, a := range c.Analysis {
		if !a.Passed && !a.Skipped {
			failed = append(failed, a)
		}
	}
	return failed
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
