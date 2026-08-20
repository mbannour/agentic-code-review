// Package review holds the normalized review domain model.
//
// Context is a snapshot of everything known about a pull request at review time.
// It is deliberately free of transport concerns: no HTTP, no authentication, no
// environment variables, and no API JSON shapes. Later stages — the deterministic
// analyzer, the risk classifier, the context selector, and the Claude Code
// adapter — depend on this model rather than on how GitHub or Jira delivered the
// information.
package review

import (
	"strconv"

	"github.com/your-company/agentic-code-review/internal/evidence"
	"github.com/your-company/agentic-code-review/internal/github"
	"github.com/your-company/agentic-code-review/internal/jira"
	"github.com/your-company/agentic-code-review/internal/reporules"
)

// Context is the complete, normalized input to a review.
type Context struct {
	PullRequest PullRequestContext

	// Ticket is nil when no Jira issue is associated with the pull request. That
	// is a normal state, not an error.
	Ticket *TicketContext

	Changes ChangeContext

	// Rules holds the repository's own review guidance.
	Rules RuleContext

	// Evidence holds operator-configured, read-only context from systems such as
	// Confluence, customer requirement files, and database metadata.
	Evidence EvidenceContext
}

// PullRequestContext describes the pull request under review.
type PullRequestContext struct {
	Owner      string
	Repository string
	Number     int
	URL        string
	Title      string
	Body       string
	Author     string
	BaseBranch string
	HeadBranch string
	HeadSHA    string
	Draft      bool
	State      string
}

// TicketContext describes the associated Jira issue.
type TicketContext struct {
	Key         string
	Summary     string
	Description string
	Status      string
	IssueType   string
	Priority    string
	Labels      []string
	ParentKey   string
}

// ChangeContext describes what the pull request changes, with aggregates
// precomputed so consumers do not each re-derive them.
type ChangeContext struct {
	Files     []FileChange
	FileCount int
	Additions int
	Deletions int
	Changes   int
}

// RuleContext holds the repository-specific review guidance that applies to this
// snapshot.
type RuleContext struct {
	Documents []RuleDocument
}

// RuleDocument is one piece of repository review guidance.
type RuleDocument struct {
	// Path is the repository-relative path the guidance came from.
	Path string

	// Content is the guidance text, already size-limited by the loader.
	Content string

	// Truncated reports whether Content was cut down to fit a size limit.
	Truncated bool
}

// EvidenceContext is external context normalized independently of its transport.
type EvidenceContext struct {
	Documents []EvidenceDocument
}

// EvidenceDocument is one bounded external document. Content is untrusted data;
// Kind says how it may substantiate a review claim.
type EvidenceDocument struct {
	ID         string
	Kind       evidence.Kind
	SourceType evidence.SourceType
	Locator    string
	Title      string
	Revision   string
	Content    string
	Digest     string
	Truncated  bool
}

// FileChange is one file touched by the pull request.
type FileChange struct {
	Filename  string
	Status    string
	Additions int
	Deletions int
	Changes   int

	// Patch is the unified diff hunk, or "" when the source did not provide one
	// (binary or oversized files).
	Patch string
}

// BuildContext assembles a review context from the pull request reference, its
// metadata, its changed files, and an optional Jira issue.
//
// issue may be nil, in which case Context.Ticket is nil. Every slice is copied,
// so the returned context is a snapshot: later stages cannot mutate the data the
// GitHub and Jira layers still hold.
func BuildContext(
	pr github.PullRequest,
	details github.PullRequestDetails,
	files []github.ChangedFile,
	issue *jira.Issue,
	rules reporules.Rules,
) Context {
	ctx := Context{
		PullRequest: PullRequestContext{
			Owner:      pr.Owner,
			Repository: pr.Repo,
			Number:     pr.Number,
			URL:        details.HTMLURL,
			Title:      details.Title,
			Body:       details.Body,
			Author:     details.AuthorLogin,
			BaseBranch: details.BaseBranch,
			HeadBranch: details.HeadBranch,
			HeadSHA:    details.HeadSHA,
			Draft:      details.Draft,
			State:      details.State,
		},
		Changes: buildChangeContext(files),
		Rules:   buildRuleContext(rules),
	}

	// The pull request number is authoritative from the parsed URL, but fall
	// back to the API's value if the reference carried none.
	if ctx.PullRequest.Number == 0 {
		ctx.PullRequest.Number = details.Number
	}

	if issue != nil {
		ctx.Ticket = buildTicketContext(*issue)
	}

	return ctx
}

// WithEvidence returns a snapshot with external evidence attached. Keeping this
// separate from BuildContext avoids making GitHub/Jira assembly responsible for
// connector transport concerns.
func WithEvidence(ctx Context, documents []evidence.Document) Context {
	if len(documents) == 0 {
		return ctx
	}

	ctx.Evidence.Documents = make([]EvidenceDocument, 0, len(documents))
	for _, document := range documents {
		ctx.Evidence.Documents = append(ctx.Evidence.Documents, EvidenceDocument{
			ID: document.ID, Kind: document.Kind, SourceType: document.SourceType,
			Locator: document.Locator, Title: document.Title, Revision: document.Revision,
			Content: document.Content, Digest: document.Digest, Truncated: document.Truncated,
		})
	}
	return ctx
}

// buildChangeContext copies the changed files and sums the aggregates.
func buildChangeContext(files []github.ChangedFile) ChangeContext {
	changes := ChangeContext{FileCount: len(files)}
	if len(files) == 0 {
		return changes
	}

	changes.Files = make([]FileChange, 0, len(files))
	for _, f := range files {
		changes.Files = append(changes.Files, FileChange{
			Filename:  f.Filename,
			Status:    f.Status,
			Additions: f.Additions,
			Deletions: f.Deletions,
			Changes:   f.Changes,
			Patch:     f.Patch,
		})

		changes.Additions += f.Additions
		changes.Deletions += f.Deletions

		// Changes is summed from the per-file values the source reported, not
		// derived as Additions+Deletions.
		changes.Changes += f.Changes
	}

	return changes
}

// buildRuleContext copies the loaded rule documents into the review domain,
// preserving their priority order.
func buildRuleContext(rules reporules.Rules) RuleContext {
	if len(rules.Documents) == 0 {
		return RuleContext{}
	}

	documents := make([]RuleDocument, 0, len(rules.Documents))
	for _, d := range rules.Documents {
		documents = append(documents, RuleDocument{
			Path:      d.Path,
			Content:   d.Content,
			Truncated: d.Truncated,
		})
	}
	return RuleContext{Documents: documents}
}

// buildTicketContext copies a Jira issue into the review domain, including its
// labels.
func buildTicketContext(issue jira.Issue) *TicketContext {
	ticket := &TicketContext{
		Key:         issue.Key.String(),
		Summary:     issue.Summary,
		Description: issue.Description,
		Status:      issue.Status,
		IssueType:   issue.IssueType,
		Priority:    issue.Priority,
		ParentKey:   issue.ParentKey,
	}

	if issue.Labels != nil {
		ticket.Labels = make([]string, len(issue.Labels))
		copy(ticket.Labels, issue.Labels)
	}

	return ticket
}

// HasTicket reports whether a Jira issue is attached to this review.
func (c Context) HasTicket() bool { return c.Ticket != nil }

// IsDraft reports whether the pull request is a draft.
func (c Context) IsDraft() bool { return c.PullRequest.Draft }

// HasChanges reports whether the pull request touches any file.
func (c Context) HasChanges() bool { return c.Changes.FileCount > 0 }

// HasRules reports whether the repository supplied any review guidance.
func (c Context) HasRules() bool { return len(c.Rules.Documents) > 0 }

// HasEvidence reports whether any external evidence was collected.
func (c Context) HasEvidence() bool { return len(c.Evidence.Documents) > 0 }

// Count returns the number of rule documents.
func (r RuleContext) Count() int { return len(r.Documents) }

// Count returns the number of external evidence documents.
func (e EvidenceContext) Count() int { return len(e.Documents) }

// Paths returns the rule document paths, in order.
func (r RuleContext) Paths() []string {
	paths := make([]string, 0, len(r.Documents))
	for _, d := range r.Documents {
		paths = append(paths, d.Path)
	}
	return paths
}

// Slug renders the repository as "owner/repo".
func (p PullRequestContext) Slug() string { return p.Owner + "/" + p.Repository }

// Ref renders the pull request as "owner/repo#number".
func (p PullRequestContext) Ref() string {
	return p.Slug() + "#" + strconv.Itoa(p.Number)
}

// HasPatch reports whether a diff is available for this file.
func (f FileChange) HasPatch() bool { return f.Patch != "" }
