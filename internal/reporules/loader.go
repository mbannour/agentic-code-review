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

// Load reads every rule file that exists in owner/repo at ref, in RulePaths
// order. ref should be the pull request's head SHA so the guidance matches the
// snapshot under review.
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
