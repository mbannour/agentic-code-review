package retrieval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Walk bounds. They exist so retrieval cannot become the slowest stage in the
// pipeline on a large repository, and so a pathological tree fails fast instead
// of consuming the review's whole time budget.
const (
	// MaxIndexedFiles caps how many source files are read per pass.
	MaxIndexedFiles = 4000

	// MaxFileBytes skips a single file larger than this. A generated bundle or a
	// vendored blob is not review context.
	MaxFileBytes = 512 * 1024

	// MaxFileLines skips a file with more lines than this, for the same reason.
	MaxFileLines = 20000
)

// excludedDirs are never walked. Vendored dependencies and build output would
// dominate a symbol index while telling a reviewer nothing about the change.
var excludedDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "vendor": true, "target": true,
	"build": true, "dist": true, "out": true, "bin": true,
	".idea": true, ".vscode": true, ".venv": true, "venv": true,
	"__pycache__": true, ".next": true, ".gradle": true, ".metals": true,
	".bloop": true, ".bsp": true, "coverage": true,
}

// sourceFile is one indexable file found by the walk.
type sourceFile struct {
	// path is the absolute path on disk.
	path string
	// relative is the repository-relative path, with forward slashes.
	relative string
	lang     language
}

// walkSources lists the indexable source files beneath root.
//
// Only extensions belonging to a supported language are returned, and only files
// small enough to be worth reading. The count of everything else is reported so a
// thin index can be explained rather than guessed at.
func walkSources(ctx context.Context, root string) ([]sourceFile, int, error) {
	var files []sourceFile
	skipped := 0

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// An unreadable subtree is not a reason to abandon the index.
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			skipped++
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		if info.IsDir() {
			if path != root && (excludedDirs[info.Name()] || strings.HasPrefix(info.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		lang, ok := languageFor(path)
		if !ok {
			return nil
		}
		if info.Size() > MaxFileBytes {
			skipped++
			return nil
		}
		if len(files) >= MaxIndexedFiles {
			skipped++
			return nil
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			skipped++
			return nil
		}
		files = append(files, sourceFile{
			path:     path,
			relative: filepath.ToSlash(relative),
			lang:     lang,
		})
		return nil
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, skipped, ctxErr
		}
		return nil, skipped, fmt.Errorf("walk repository: %w", err)
	}
	return files, skipped, nil
}

// definitionSite is where a symbol is defined.
type definitionSite struct {
	Path      string
	Line      int
	Changed   bool
	lang      language
	fsPath    string
	sortIndex int
}

// index is the symbol table built from one pass over the sources.
type index struct {
	// definitions maps a symbol to every place it is defined. A symbol defined in
	// several packages has several entries; nothing here can tell them apart, and
	// the ranking prefers unchanged files over changed ones rather than guessing.
	definitions map[string][]definitionSite
	files       []sourceFile
	skipped     int
}

// buildIndex reads every source file once and records what it defines.
func buildIndex(ctx context.Context, root string, changed map[string]bool) (index, error) {
	files, skipped, err := walkSources(ctx, root)
	if err != nil {
		return index{}, err
	}

	built := index{definitions: map[string][]definitionSite{}, files: files, skipped: skipped}

	for i, file := range files {
		if err := ctx.Err(); err != nil {
			return index{}, err
		}
		lines, ok := readLines(file.path)
		if !ok {
			built.skipped++
			continue
		}

		for number, line := range lines {
			for _, name := range definitionsIn(file.lang, line) {
				built.definitions[name] = append(built.definitions[name], definitionSite{
					Path:      file.relative,
					Line:      number + 1,
					Changed:   changed[file.relative],
					lang:      file.lang,
					fsPath:    file.path,
					sortIndex: i,
				})
			}
		}
	}
	return built, nil
}

// readLines reads a file for indexing, refusing anything implausibly large.
func readLines(path string) ([]string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > MaxFileBytes {
		return nil, false
	}
	// A file with no newlines at all is almost always minified or generated.
	content := string(raw)
	lines := strings.Split(content, "\n")
	if len(lines) > MaxFileLines {
		return nil, false
	}
	return lines, true
}
