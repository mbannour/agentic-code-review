// Package reporules loads repository-specific review guidance from a fixed set of
// conventional files.
//
// It represents rules; it knows nothing about how they will eventually be
// presented to a reviewer, human or otherwise.
package reporules

import "strings"

// MaxRuleDocumentBytes caps the size of a single rule document. A repository
// cannot push an arbitrarily large file into the review context.
const MaxRuleDocumentBytes = 100 * 1024

// Revision says which side of a pull request a rule document came from. It is the
// difference between policy and a proposal.
type Revision string

const (
	// RevisionBase is the branch the pull request targets. Rules read there are
	// authoritative: they were reviewed and merged before this change existed.
	RevisionBase Revision = "base"

	// RevisionHead is the pull request's own branch. Rules read there are a
	// proposal, never authority — a change must not be able to rewrite the
	// standard it is judged against.
	RevisionHead Revision = "head"
)

// RuleDocument is one rule file read from a repository.
type RuleDocument struct {
	// Path is the repository-relative path the document was read from.
	Path string

	// Revision and Ref record where this document came from, so a reader can
	// always tell authoritative policy from a proposal.
	Revision Revision
	Ref      string

	// Content is the document's text, truncated if it exceeded
	// MaxRuleDocumentBytes.
	Content string

	// Truncated reports whether Content was cut down to the size limit.
	Truncated bool
}

// Rules is the ordered set of rule documents found in a repository. Order follows
// RulePaths, from most review-specific to least.
type Rules struct {
	// Documents are the authoritative rules: the review policy actually in force.
	Documents []RuleDocument

	// ProposedChanges records rule files this pull request adds or modifies.
	//
	// They are reported, never applied. A pull request that deletes "authentication
	// bypasses are blockers" is reviewed under the rule it is deleting, and the
	// deletion is surfaced as a policy change for a human to weigh.
	ProposedChanges []RuleChange
}

// ChangeKind is what a pull request does to a rule file.
type ChangeKind string

const (
	// ChangeAdded means the file exists only on the head branch.
	ChangeAdded ChangeKind = "added"

	// ChangeModified means the file exists on both sides with different content.
	ChangeModified ChangeKind = "modified"

	// ChangeRemoved means the file exists only on the base branch. The base
	// version still governs this review.
	ChangeRemoved ChangeKind = "removed"
)

// RuleChange is a proposed modification to review policy.
//
// It carries no content. What a proposed rule *says* must not reach the reviewer as
// guidance, or the separation between policy and proposal would exist only in
// naming. Its bytes are reported so a human can see that something changed and read
// the diff themselves.
type RuleChange struct {
	Path string
	Kind ChangeKind

	// BaseBytes and HeadBytes are the document sizes on each side, or zero where
	// the document is absent.
	BaseBytes int
	HeadBytes int
}

// HasProposedChanges reports whether the pull request touches review policy.
func (r Rules) HasProposedChanges() bool { return len(r.ProposedChanges) > 0 }

// Empty reports whether no rule documents were found.
func (r Rules) Empty() bool { return len(r.Documents) == 0 }

// Count returns the number of rule documents.
func (r Rules) Count() int { return len(r.Documents) }

// Paths returns the paths of the documents, in order.
func (r Rules) Paths() []string {
	paths := make([]string, 0, len(r.Documents))
	for _, d := range r.Documents {
		paths = append(paths, d.Path)
	}
	return paths
}

// CombinedText concatenates the documents in order, each preceded by its path so
// the origin of a rule stays traceable. It is a plain-text join, not a prompt.
func (r Rules) CombinedText() string {
	if len(r.Documents) == 0 {
		return ""
	}

	var b strings.Builder
	for i, d := range r.Documents {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("# ")
		b.WriteString(d.Path)
		b.WriteString("\n\n")
		b.WriteString(d.Content)
	}
	return b.String()
}
