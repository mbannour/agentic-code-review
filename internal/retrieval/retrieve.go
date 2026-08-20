package retrieval

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Retrieval bounds. Retrieval competes with the changed code for the review's
// context budget, so it is capped here as well as by the selector: a stage that
// can only ever offer a bounded amount is easier to reason about than one whose
// output depends on repository size.
const (
	// MaxSymbols caps how many touched identifiers are resolved. The ranking is by
	// how often the change mentions them, so the cut falls on the least-mentioned.
	MaxSymbols = 24

	// MaxSnippets caps the retrieved regions.
	MaxSnippets = 24

	// MaxCallersPerSymbol caps unchanged use sites per changed symbol. Two shows
	// that a symbol has outside users and what they expect; twenty shows the same
	// thing at ten times the cost.
	MaxCallersPerSymbol = 2

	// MaxSnippetBytes bounds one retrieved region.
	MaxSnippetBytes = 2 * 1024

	// MaxTotalBytes bounds all retrieved regions together.
	MaxTotalBytes = 48 * 1024

	// maxDefinitionLines bounds a definition snippet. A function longer than this
	// is summarized by its opening: the signature and the first decisions are what
	// a reviewer needs to judge a call into it.
	maxDefinitionLines = 40

	// callerContextBefore and callerContextAfter bound a use-site snippet.
	callerContextBefore = 2
	callerContextAfter  = 4
)

// Change is one changed file as retrieval needs it: the path, and the patch whose
// changed lines name the symbols worth resolving.
type Change struct {
	Path  string
	Patch string
}

// Retriever finds unchanged code related to a change. It is deterministic and
// stateless between calls: the same checkout and the same patches always produce
// the same snippets in the same order.
type Retriever struct{}

// NewRetriever returns a Retriever.
func NewRetriever() Retriever { return Retriever{} }

// Retrieve builds the bounded set of unchanged code regions related to the change.
//
// Absence degrades the review rather than ending it, exactly as a missing local
// checkout does for deterministic analysis: every reason for an empty result is
// reported as a skip with its cause, never as an error.
func (Retriever) Retrieve(ctx context.Context, root string, changes []Change) (Result, error) {
	if strings.TrimSpace(root) == "" {
		return skipped("local repository not provided"), nil
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return skipped("local repository not readable"), nil
	}
	if len(changes) == 0 {
		return skipped("no changed files to resolve symbols from"), nil
	}

	changed := map[string]bool{}
	for _, change := range changes {
		changed[filepath.ToSlash(strings.TrimSpace(change.Path))] = true
	}

	ranked := rankTouched(changes)
	if len(ranked) == 0 {
		return skipped("no resolvable identifiers on the changed lines"), nil
	}

	built, err := buildIndex(ctx, root, changed)
	if err != nil {
		return Result{}, err
	}
	if len(built.files) == 0 {
		return skipped("no indexable source files found in the checkout"), nil
	}

	result := Result{Stats: Stats{
		FilesIndexed:   len(built.files),
		FilesSkipped:   built.skipped,
		Definitions:    len(built.definitions),
		TouchedSymbols: len(ranked),
	}}

	budget := MaxTotalBytes

	// Definitions first: what the changed code calls into is what decides whether
	// the change is correct. Callers answer a second question — who else depended
	// on this — and only matter once the first is answerable.
	definitions, used := collectDefinitions(ctx, ranked, built, &result, budget)
	budget -= used
	callers, usedCallers := collectCallers(ctx, ranked, built, changed, budget, len(definitions))
	budget -= usedCallers

	result.Snippets = append(definitions, callers...)
	for _, snippet := range result.Snippets {
		result.Stats.Bytes += snippet.Bytes()
		if snippet.Truncated {
			result.Stats.Truncated = true
		}
	}
	if len(result.Snippets) >= MaxSnippets {
		result.Stats.Truncated = true
	}
	result.Stats.ResolvedSymbols = len(result.Symbols)

	if len(result.Snippets) == 0 {
		return skipped("no unchanged code resolved for the changed symbols"), nil
	}
	return result, nil
}

func skipped(reason string) Result {
	return Result{Skipped: true, Reason: reason}
}

// rankTouched merges the identifiers from every changed patch into one ranking.
func rankTouched(changes []Change) []rankedSymbol {
	counts := map[string]int{}
	for _, change := range changes {
		for _, symbol := range touchedSymbols(change.Patch) {
			counts[symbol.Name] += symbol.Occurrences
		}
	}

	ranked := make([]rankedSymbol, 0, len(counts))
	for name, count := range counts {
		ranked = append(ranked, rankedSymbol{Name: name, Occurrences: count})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Occurrences != ranked[j].Occurrences {
			return ranked[i].Occurrences > ranked[j].Occurrences
		}
		return ranked[i].Name < ranked[j].Name
	})
	if len(ranked) > MaxSymbols {
		ranked = ranked[:MaxSymbols]
	}
	return ranked
}

// collectDefinitions retrieves, for each ranked symbol, the definition site that
// the pull request did not change.
func collectDefinitions(
	ctx context.Context,
	ranked []rankedSymbol,
	built index,
	result *Result,
	budget int,
) ([]Snippet, int) {
	var snippets []Snippet
	used := 0
	seen := map[string]bool{}

	for _, symbol := range ranked {
		if ctx.Err() != nil || len(snippets) >= MaxSnippets || used >= budget {
			break
		}

		site, ok := bestDefinition(built.definitions[symbol.Name])
		if !ok {
			continue
		}

		snippet, ok := definitionSnippet(site, symbol.Name)
		if !ok {
			continue
		}
		key := snippet.Path + ":" + snippet.Location()
		if seen[key] {
			continue
		}
		if used+snippet.Bytes() > budget {
			continue
		}

		seen[key] = true
		used += snippet.Bytes()
		snippets = append(snippets, snippet)
		result.Symbols = append(result.Symbols, symbol.Name)
	}
	return snippets, used
}

// bestDefinition picks which definition of a symbol to show.
//
// Only unchanged files qualify: the changed ones are already in the diff, and
// spending retrieval budget on them would duplicate context the reviewer has. A
// non-test definition wins over a test one, since a test's helper of the same name
// is rarely the thing the change calls. Beyond that the earliest indexed path
// wins, which makes the choice stable rather than arbitrary.
func bestDefinition(sites []definitionSite) (definitionSite, bool) {
	var best definitionSite
	found := false

	for _, site := range sites {
		if site.Changed {
			continue
		}
		if !found {
			best, found = site, true
			continue
		}
		if betterDefinition(site, best) {
			best = site
		}
	}
	return best, found
}

func betterDefinition(candidate, current definitionSite) bool {
	candidateTest, currentTest := looksLikeTest(candidate.Path), looksLikeTest(current.Path)
	if candidateTest != currentTest {
		return currentTest
	}
	if candidate.sortIndex != current.sortIndex {
		return candidate.sortIndex < current.sortIndex
	}
	return candidate.Line < current.Line
}

func looksLikeTest(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, "_test.go") ||
		strings.Contains(lower, "/test/") || strings.Contains(lower, "/tests/") ||
		strings.Contains(lower, "src/test/") ||
		strings.HasSuffix(lower, "spec.scala") || strings.HasSuffix(lower, "test.scala") ||
		strings.Contains(lower, ".test.") || strings.Contains(lower, ".spec.")
}

// collectCallers retrieves unchanged use sites of symbols the change defines.
//
// This is the direction a diff cannot show: the change rewrote a function, and
// whether that is safe depends on code the pull request never opened.
func collectCallers(
	ctx context.Context,
	ranked []rankedSymbol,
	built index,
	changed map[string]bool,
	budget int,
	alreadyKept int,
) ([]Snippet, int) {
	wanted := map[string]bool{}
	for _, symbol := range ranked {
		for _, site := range built.definitions[symbol.Name] {
			// Defined by a file this pull request changed: its users are what the
			// diff cannot show.
			if site.Changed {
				wanted[symbol.Name] = true
				break
			}
		}
	}
	if len(wanted) == 0 || budget <= 0 {
		return nil, 0
	}

	perSymbol := map[string]int{}
	var snippets []Snippet
	used := 0

	for _, file := range built.files {
		if ctx.Err() != nil || alreadyKept+len(snippets) >= MaxSnippets || used >= budget {
			break
		}
		if changed[file.relative] {
			continue
		}
		lines, ok := readLines(file.path)
		if !ok {
			continue
		}

		for number, line := range lines {
			if alreadyKept+len(snippets) >= MaxSnippets || used >= budget {
				break
			}
			for _, identifier := range identifierPattern.FindAllString(line, -1) {
				if !wanted[identifier] || perSymbol[identifier] >= MaxCallersPerSymbol {
					continue
				}
				// The definition of the same name in another file is not a use of it.
				if len(definitionsIn(file.lang, line)) > 0 {
					continue
				}

				snippet := callerSnippet(file, lines, number, identifier)
				if used+snippet.Bytes() > budget {
					continue
				}
				perSymbol[identifier]++
				used += snippet.Bytes()
				snippets = append(snippets, snippet)
				break
			}
		}
	}

	// Grouping by symbol, then by path, keeps the section readable and the order
	// independent of the walk.
	sort.SliceStable(snippets, func(i, j int) bool {
		if snippets[i].Symbol != snippets[j].Symbol {
			return snippets[i].Symbol < snippets[j].Symbol
		}
		if snippets[i].Path != snippets[j].Path {
			return snippets[i].Path < snippets[j].Path
		}
		return snippets[i].StartLine < snippets[j].StartLine
	})
	return snippets, used
}

// definitionSnippet extracts a bounded region starting at a definition.
func definitionSnippet(site definitionSite, symbol string) (Snippet, bool) {
	lines, ok := readLines(site.fsPath)
	if !ok || site.Line > len(lines) {
		return Snippet{}, false
	}

	start := site.Line - 1
	startIndent := indentWidth(lines[start])
	end := start

	for i := start + 1; i < len(lines) && i-start < maxDefinitionLines; i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			end = i
			continue
		}
		// A following definition at the same or shallower indentation ends this
		// one. Reaching it means the whole body was captured.
		if indentWidth(line) <= startIndent && len(definitionsIn(site.lang, line)) > 0 {
			break
		}
		end = i
	}

	return buildSnippet(symbol, RelationDefinition, site.Path, lines, start, end), true
}

// callerSnippet extracts the neighbourhood of one use site.
func callerSnippet(file sourceFile, lines []string, number int, symbol string) Snippet {
	start := number - callerContextBefore
	if start < 0 {
		start = 0
	}
	end := number + callerContextAfter
	if end > len(lines)-1 {
		end = len(lines) - 1
	}
	return buildSnippet(symbol, RelationCaller, file.relative, lines, start, end)
}

// buildSnippet assembles and bounds a snippet from a line range.
func buildSnippet(symbol string, relation Relation, path string, lines []string, start, end int) Snippet {
	// Trailing blank lines add bytes and no information.
	for end > start && strings.TrimSpace(lines[end]) == "" {
		end--
	}

	content := strings.Join(lines[start:end+1], "\n")
	truncated := false
	if len(content) > MaxSnippetBytes {
		content = content[:MaxSnippetBytes]
		// Never end mid-line: a half line reads as code that does not exist.
		if cut := strings.LastIndexByte(content, '\n'); cut > 0 {
			content = content[:cut]
		}
		content += "\n[TRUNCATED: snippet exceeded " + strconv.Itoa(MaxSnippetBytes) + " bytes]"
		truncated = true
	}

	return Snippet{
		Symbol:    symbol,
		Relation:  relation,
		Path:      path,
		StartLine: start + 1,
		EndLine:   end + 1,
		Content:   content,
		Truncated: truncated,
	}
}

func indentWidth(line string) int {
	width := 0
	for _, r := range line {
		switch r {
		case ' ':
			width++
		case '\t':
			width += 4
		default:
			return width
		}
	}
	return width
}
