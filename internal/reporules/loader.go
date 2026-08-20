package reporules

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/your-company/agentic-code-review/internal/github"
)

// RulePaths is the allow-list of rule files, in priority order from most
// review-specific to least. The order is part of the contract: documents are
// returned in exactly this sequence, never sorted.
//
// This is deliberately an explicit list. The repository is never scanned, so no
// .env, credential, certificate, or other unexpected file can be pulled into the
// review context.
var RulePaths = []string{
	".ai-review/rules.md",
	"AGENTS.md",
	"CONTRIBUTING.md",
}

// truncationMarkerFormat is appended to a document that hit the size limit.
const truncationMarkerFormat = "\n\n[TRUNCATED: rule document exceeded %d bytes]\n"

// FileReader reads a single file from a repository at a given ref. The GitHub
// client satisfies it; tests supply a fake.
type FileReader interface {
	GetRepositoryFile(
		ctx context.Context,
		owner string,
		repo string,
		path string,
		ref string,
	) (string, error)
}

// Loader reads rule documents through a FileReader.
type Loader struct {
	reader FileReader

	// paths is the allow-list to read, defaulting to RulePaths.
	paths []string
}

// NewLoader returns a Loader reading through reader.
func NewLoader(reader FileReader) *Loader {
	return &Loader{reader: reader, paths: RulePaths}
}

// LoadForPullRequest reads the authoritative review policy and separately reports
// what the pull request proposes to change about it.
//
// The authoritative rules come from baseRef — the branch the change targets. This
// is a security boundary, not a preference: rules read from the head branch would
// let a pull request rewrite the standard it is judged against, so a change that
// deletes "authentication bypasses are blockers" is still reviewed under that rule,
// and the deletion is surfaced instead of obeyed.
//
// A missing baseRef is refused rather than quietly falling back to the head. A
// review conducted under rules the change itself supplied is worse than a review
// conducted under none, because it looks equally authoritative.
func (l *Loader) LoadForPullRequest(
	ctx context.Context,
	owner string,
	repo string,
	baseRef string,
	headRef string,
) (Rules, error) {
	if strings.TrimSpace(baseRef) == "" {
		return Rules{}, errors.New("load rules: base ref is required; head-branch rules are never authoritative")
	}

	rules, err := l.loadAt(ctx, owner, repo, baseRef, RevisionBase)
	if err != nil {
		return Rules{}, err
	}

	// Without a head ref there is nothing to compare, which is not an error: the
	// authoritative policy is already loaded.
	if strings.TrimSpace(headRef) == "" {
		return rules, nil
	}

	proposed, err := l.loadAt(ctx, owner, repo, headRef, RevisionHead)
	if err != nil {
		return Rules{}, err
	}
	rules.ProposedChanges = diffRules(rules.Documents, proposed.Documents)
	return rules, nil
}

// diffRules reports what changed between the authoritative and proposed rules.
//
// Comparison is by content length and content equality only. Nothing here reads a
// proposed rule as guidance; the question is solely whether policy is being
// changed, so that a human is told.
func diffRules(base, head []RuleDocument) []RuleChange {
	baseByPath := map[string]RuleDocument{}
	for _, document := range base {
		baseByPath[document.Path] = document
	}
	headByPath := map[string]RuleDocument{}
	for _, document := range head {
		headByPath[document.Path] = document
	}

	var changes []RuleChange
	// RulePaths order, so the report is deterministic.
	for _, path := range RulePaths {
		baseDoc, onBase := baseByPath[path]
		headDoc, onHead := headByPath[path]

		switch {
		case onBase && onHead:
			if baseDoc.Content != headDoc.Content {
				changes = append(changes, RuleChange{
					Path: path, Kind: ChangeModified,
					BaseBytes: len(baseDoc.Content), HeadBytes: len(headDoc.Content),
				})
			}
		case onHead:
			changes = append(changes, RuleChange{
				Path: path, Kind: ChangeAdded, HeadBytes: len(headDoc.Content),
			})
		case onBase:
			changes = append(changes, RuleChange{
				Path: path, Kind: ChangeRemoved, BaseBytes: len(baseDoc.Content),
			})
		}
	}
	return changes
}

// loadAt reads the allow-listed rule files at one revision.
func (l *Loader) loadAt(
	ctx context.Context,
	owner string,
	repo string,
	ref string,
	revision Revision,
) (Rules, error) {
	rules, err := l.Load(ctx, owner, repo, ref)
	if err != nil {
		return Rules{}, err
	}
	for i := range rules.Documents {
		rules.Documents[i].Revision = revision
		rules.Documents[i].Ref = ref
	}
	return rules, nil
}

// Load reads every rule file that exists in owner/repo at ref, in RulePaths
// order.
//
// It records no provenance and makes no authority judgement, so callers reviewing
// a pull request should use LoadForPullRequest instead: this method cannot tell a
// merged rule from one the change under review just wrote.
//
// A rule file that does not exist is skipped: these files are all optional. Any
// other failure — authentication, permissions, throttling, a server error, a
// network fault — is returned, because silently reviewing without rules the
// repository does define would be misleading.
//
// Empty and whitespace-only documents are skipped rather than contributing
// nothing but noise.
func (l *Loader) Load(ctx context.Context, owner string, repo string, ref string) (Rules, error) {
	var rules Rules

	for _, path := range l.paths {
		content, err := l.reader.GetRepositoryFile(ctx, owner, repo, path, ref)
		if err != nil {
			if errors.Is(err, github.ErrNotFound) {
				continue // an optional file that this repository does not have
			}
			return Rules{}, fmt.Errorf("load rule document %s: %w", path, err)
		}

		if strings.TrimSpace(content) == "" {
			continue // present but says nothing
		}

		rules.Documents = append(rules.Documents, newRuleDocument(path, content))
	}

	return rules, nil
}

// newRuleDocument builds a document, truncating content that exceeds the limit.
func newRuleDocument(path string, content string) RuleDocument {
	if len(content) <= MaxRuleDocumentBytes {
		return RuleDocument{Path: path, Content: content}
	}

	return RuleDocument{
		Path:      path,
		Content:   truncate(content) + fmt.Sprintf(truncationMarkerFormat, MaxRuleDocumentBytes),
		Truncated: true,
	}
}

// truncate cuts content to MaxRuleDocumentBytes without splitting a multi-byte
// rune, so the result is always valid UTF-8. The cut is purely positional and so
// deterministic: the same input always yields the same output.
func truncate(content string) string {
	cut := content[:MaxRuleDocumentBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
