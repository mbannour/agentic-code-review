// Package retrieval finds unchanged repository code a review needs in order to
// judge a change.
//
// A diff answers what was written. It does not answer what the changed function
// promised its callers, what the function it now calls actually does, or who
// depended on the behaviour that moved. Those answers are in code the pull request
// did not touch, and without them a reviewer is guessing.
//
// What this package builds is a symbol index: definitions and references located
// by language-aware pattern matching over the local checkout. It is deliberately
// not a compiler. It resolves no types, follows no imports, and distinguishes no
// two same-named symbols in different packages, so a retrieved snippet is a
// candidate for relevance and never a proof of one. That is also why every
// snippet travels as untrusted evidence, and why the changed-file scope rule on
// findings is unaffected: retrieval widens what a reviewer may read, never what a
// finding may blame.
//
// Nothing here reaches the network. It reads only the local checkout it is given,
// and only files whose extension belongs to a language the repository was
// detected to use.
package retrieval

import "strconv"

// Relation is why a snippet was retrieved. The two directions answer different
// review questions and are ranked differently under budget pressure.
type Relation string

const (
	// RelationDefinition is the definition of a symbol the change now uses:
	// what the changed code is calling into.
	RelationDefinition Relation = "definition"

	// RelationCaller is an unchanged use of a symbol the change modified: who
	// depended on what just moved.
	RelationCaller Relation = "caller"
)

// Display renders the relation for terminal output.
func (r Relation) Display() string {
	switch r {
	case RelationDefinition:
		return "DEF"
	case RelationCaller:
		return "USE"
	default:
		return "?"
	}
}

// Snippet is one bounded region of unchanged code, with the reason it was kept.
type Snippet struct {
	// Symbol is the identifier that linked this snippet to the change.
	Symbol   string
	Relation Relation

	// Path is repository-relative, always using forward slashes.
	Path      string
	StartLine int
	EndLine   int

	// Content is the snippet text, bounded at collection time.
	Content string

	// Truncated reports whether Content was cut.
	Truncated bool
}

// Bytes is the snippet's contribution to a context budget.
func (s Snippet) Bytes() int { return len(s.Content) }

// Location renders the snippet's position as "path:start-end".
func (s Snippet) Location() string {
	if s.EndLine <= s.StartLine {
		return s.Path + ":" + strconv.Itoa(s.StartLine)
	}
	return s.Path + ":" + strconv.Itoa(s.StartLine) + "-" + strconv.Itoa(s.EndLine)
}

// Result is everything retrieval concluded, including why it concluded nothing.
type Result struct {
	// Skipped reports that retrieval did not run. Reason says why.
	Skipped bool
	Reason  string

	// Symbols are the identifiers taken from the changed lines that resolved to a
	// definition somewhere in the repository, in ranked order.
	Symbols []string

	Snippets []Snippet
	Stats    Stats
}

// Empty reports whether nothing was retrieved.
func (r Result) Empty() bool { return len(r.Snippets) == 0 }

// Stats describes the work retrieval did, so a thin result can be told apart
// from a broken one.
type Stats struct {
	// FilesIndexed and FilesSkipped describe the walk. A large skipped count is
	// normal: vendored trees and build output are excluded by design.
	FilesIndexed int
	FilesSkipped int

	// Definitions is the number of distinct symbol definitions indexed.
	Definitions int

	// TouchedSymbols is how many identifiers were extracted from changed lines
	// before any were resolved.
	TouchedSymbols int

	// ResolvedSymbols is how many of those resolved to an indexed definition.
	ResolvedSymbols int

	// Bytes is the total size of the retrieved snippets.
	Bytes int

	// Truncated reports whether any snippet or the snippet list was cut.
	Truncated bool
}
