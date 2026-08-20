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

// RuleDocument is one rule file read from a repository.
type RuleDocument struct {
	// Path is the repository-relative path the document was read from.
	Path string

	// Content is the document's text, truncated if it exceeded
	// MaxRuleDocumentBytes.
	Content string

	// Truncated reports whether Content was cut down to the size limit.
	Truncated bool
}

// Rules is the ordered set of rule documents found in a repository. Order follows
// RulePaths, from most review-specific to least.
type Rules struct {
	Documents []RuleDocument
}

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
