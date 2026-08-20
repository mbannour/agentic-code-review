package retrieval

import (
	"regexp"
	"sort"
	"strings"
)

// language groups the file extensions and definition patterns for one language.
//
// Patterns are line-anchored and deliberately conservative: a missed definition
// costs one retrieval candidate, while a wrong one spends budget on unrelated
// code and teaches the reviewer to distrust the section.
type language struct {
	name       string
	extensions []string
	// definitions each capture the defined name in group 1.
	definitions []*regexp.Regexp
}

var languages = []language{
	{
		name:       "Go",
		extensions: []string{".go"},
		definitions: []*regexp.Regexp{
			// func Name(, and methods: func (r Receiver) Name(
			regexp.MustCompile(`^\s*func\s+([A-Za-z_]\w*)\s*[\(\[]`),
			regexp.MustCompile(`^\s*func\s+\([^)]*\)\s*([A-Za-z_]\w*)\s*[\(\[]`),
			regexp.MustCompile(`^\s*type\s+([A-Za-z_]\w*)\s`),
			// Anchored at column zero: an indented var or const is a local, and a
			// local's declaration is not a definition a reviewer needs retrieved.
			regexp.MustCompile(`^(?:var|const)\s+([A-Za-z_]\w*)\s`),
		},
	},
	{
		name:       "Scala",
		extensions: []string{".scala", ".sc"},
		definitions: []*regexp.Regexp{
			regexp.MustCompile(`\bdef\s+([A-Za-z_]\w*)`),
			regexp.MustCompile(`\b(?:class|trait|object|enum)\s+([A-Za-z_]\w*)`),
			// Scala members are indented inside their object, class, or trait, so
			// indentation cannot separate a member from a method-local binding here.
			// The identifier still has to be one the change mentions, which is what
			// keeps the noise bounded.
			regexp.MustCompile(`^\s{0,6}(?:private\s+|protected\s+|final\s+|lazy\s+|implicit\s+|override\s+)*(?:val|var|type)\s+([A-Za-z_]\w*)`),
		},
	},
	{
		name:        "TypeScript",
		extensions:  []string{".ts", ".tsx", ".mts", ".cts"},
		definitions: typeScriptDefinitions(),
	},
	{
		name:        "JavaScript",
		extensions:  []string{".js", ".jsx", ".mjs", ".cjs"},
		definitions: typeScriptDefinitions(),
	},
}

func typeScriptDefinitions() []*regexp.Regexp {
	return []*regexp.Regexp{
		regexp.MustCompile(`\bfunction\s+([A-Za-z_$][\w$]*)`),
		regexp.MustCompile(`\b(?:class|interface|type|enum)\s+([A-Za-z_$][\w$]*)`),
		// Module scope only, for the same reason as Go: an indented binding is a
		// local, not an exported definition.
		regexp.MustCompile(`^(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*[:=]`),
	}
}

// languageFor returns the language owning a path's extension.
func languageFor(path string) (language, bool) {
	lower := strings.ToLower(path)
	for _, lang := range languages {
		for _, ext := range lang.extensions {
			if strings.HasSuffix(lower, ext) {
				return lang, true
			}
		}
	}
	return language{}, false
}

// identifierPattern matches candidate identifiers, including the `$` TypeScript
// allows. Dotted access is matched one segment at a time, which is what makes a
// method name resolvable without resolving the receiver's type.
var identifierPattern = regexp.MustCompile(`[A-Za-z_$][\w$]*`)

// definitionsIn returns every symbol defined on a line, in pattern order.
func definitionsIn(lang language, line string) []string {
	// A commented-out definition is not a definition. This is a cheap check, not
	// a lexer: it costs nothing and removes the most common false positive.
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") ||
		strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "#") {
		return nil
	}

	var names []string
	for _, pattern := range lang.definitions {
		if match := pattern.FindStringSubmatch(line); match != nil {
			names = appendUnique(names, match[1])
		}
	}
	return names
}

// touchedSymbols extracts candidate identifiers from a unified diff's changed
// lines, ranked by how often they occur.
//
// Both additions and deletions count: a symbol the change stopped calling is as
// interesting as one it started calling. Context lines are ignored, because
// everything in the file's neighbourhood would qualify and the ranking would stop
// meaning anything.
func touchedSymbols(patch string) []rankedSymbol {
	counts := map[string]int{}

	for _, line := range strings.Split(patch, "\n") {
		if len(line) == 0 {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			continue
		case line[0] != '+' && line[0] != '-':
			continue
		}

		body := line[1:]
		trimmed := strings.TrimSpace(body)
		// Import lists name most of a file's vocabulary without saying anything
		// about what this change does with it.
		if isImportLine(trimmed) {
			continue
		}

		for _, identifier := range identifierPattern.FindAllString(body, -1) {
			if !plausibleSymbol(identifier) {
				continue
			}
			counts[identifier]++
		}
	}

	ranked := make([]rankedSymbol, 0, len(counts))
	for name, count := range counts {
		ranked = append(ranked, rankedSymbol{Name: name, Occurrences: count})
	}
	// Frequency first, then name, so the ranking never depends on map order.
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Occurrences != ranked[j].Occurrences {
			return ranked[i].Occurrences > ranked[j].Occurrences
		}
		return ranked[i].Name < ranked[j].Name
	})
	return ranked
}

// rankedSymbol is a candidate identifier and how often the change mentions it.
type rankedSymbol struct {
	Name        string
	Occurrences int
}

func isImportLine(trimmed string) bool {
	return strings.HasPrefix(trimmed, "import ") ||
		strings.HasPrefix(trimmed, "package ") ||
		strings.HasPrefix(trimmed, "from ") ||
		strings.HasPrefix(trimmed, "require(")
}

// plausibleSymbol filters identifiers that would retrieve noise.
//
// The length floor and the keyword list do most of the work. Everything that
// survives still has to match an indexed definition, so this is a cost filter
// rather than a correctness one.
func plausibleSymbol(identifier string) bool {
	if len(identifier) < 4 {
		return false
	}
	return !commonWords[strings.ToLower(identifier)]
}

// commonWords are keywords and near-universal names. A definition of one of these
// is real but tells a reviewer nothing about this change.
var commonWords = map[string]bool{
	// Control flow and declaration keywords across the supported languages.
	"case": true, "class": true, "const": true, "continue": true, "default": true,
	"defer": true, "else": true, "enum": true, "extends": true, "false": true,
	"final": true, "func": true, "function": true, "given": true, "implicit": true,
	"import": true, "interface": true, "lazy": true, "match": true, "null": true,
	"object": true, "override": true, "package": true, "private": true,
	"protected": true, "public": true, "return": true, "sealed": true, "static": true,
	"struct": true, "super": true, "switch": true, "this": true, "throw": true,
	"trait": true, "true": true, "type": true, "using": true, "while": true,
	"yield": true, "async": true, "await": true, "export": true, "extension": true,

	// Types and near-universal names that appear in nearly every file.
	"bool": true, "boolean": true, "byte": true, "double": true, "error": true,
	"float": true, "int32": true, "int64": true, "long": true, "number": true,
	"string": true, "unit": true, "void": true, "list": true, "seq": true,
	"array": true, "option": true, "some": true, "none": true, "either": true,
	"future": true, "value": true, "result": true, "data": true, "item": true,
	"name": true, "nil": true, "self": true, "args": true, "props": true,
	"context": true, "config": true, "logger": true, "println": true,
	"length": true, "size": true, "test": true, "tests": true, "assert": true,
}

func appendUnique(values []string, want string) []string {
	for _, v := range values {
		if v == want {
			return values
		}
	}
	return append(values, want)
}
