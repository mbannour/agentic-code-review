// Package publish decides what a validated review result may become on GitHub.
//
// It sits between the domain model and the API client, and it is where authority
// lives: policy chooses which findings earn an inline comment, the diff mapper
// decides whether a location genuinely exists in the pull request, and the
// renderer produces the exact Markdown that will be posted. A model proposes
// findings; nothing a model returns can widen what this package permits.
//
// Everything here is deterministic and offline. The only network call in the whole
// publication path is the single review creation performed by internal/github.
package publish

import (
	"sort"
	"strconv"
	"strings"

	"github.com/your-company/agentic-code-review/internal/contextselect"
	"github.com/your-company/agentic-code-review/internal/findings"
	"github.com/your-company/agentic-code-review/internal/github"
)

// DiffLocation is a position GitHub will accept for an inline review comment.
//
// Line and Side address the last line of the comment, which is how GitHub models a
// range: StartLine and StartSide are set only for a genuine multi-line range and are
// otherwise zero. A DiffLocation is only ever produced by mapping a finding onto a
// real diff hunk, never by arithmetic on a source line number.
type DiffLocation struct {
	Path      string
	Line      int
	Side      string
	StartLine int
	StartSide string
}

// Multiline reports whether the location spans more than one line.
func (l DiffLocation) Multiline() bool { return l.StartLine > 0 && l.StartLine < l.Line }

// Describe renders the location for terminal output and error messages.
func (l DiffLocation) Describe() string {
	if l.Multiline() {
		return l.Path + ":" + strconv.Itoa(l.StartLine) + "-" + strconv.Itoa(l.Line) + " " + l.Side
	}
	return l.Path + ":" + strconv.Itoa(l.Line) + " " + l.Side
}

// Comment converts the location and a rendered body into an API comment.
//
// The range fields are supplied only for a genuine range, and always as a pair:
// GitHub rejects a half-specified range outright.
func (l DiffLocation) Comment(body string) github.ReviewComment {
	comment := github.ReviewComment{
		Path: l.Path,
		Line: l.Line,
		Side: l.Side,
		Body: body,
	}
	if l.Multiline() {
		startLine, startSide := l.StartLine, l.StartSide
		comment.StartLine = &startLine
		comment.StartSide = &startSide
	}
	return comment
}

// DiffFile is one file's diff as the mapper needs it. It is deliberately not tied
// to a particular source, so the mapper can be fed GitHub's changed files or the
// selection that Claude actually saw.
type DiffFile struct {
	Path   string
	Status string
	Patch  string

	// Truncated reports that Patch is not the whole diff. A truncated patch still
	// maps the hunks it does contain; it simply cannot vouch for the rest.
	Truncated bool
}

// fileDiff is the parsed, line-addressable form of one file's patch.
//
// right and left hold the lines GitHub will accept a comment on, per side: an
// added line and a context line exist on the right, a deleted line and a context
// line exist on the left. A line absent from both sets is not part of the diff,
// and no comment may be placed there.
type fileDiff struct {
	path      string
	status    string
	truncated bool

	right map[int]bool
	left  map[int]bool
}

// Mapper turns a finding's source location into a GitHub diff location.
//
// It answers one question — does this line exist in the pull request diff, and on
// which side — and it answers only from the patch. When the patch cannot support a
// location the mapper says so; it never searches nearby lines for something GitHub
// might accept, because a comment on the wrong line is worse than no comment.
type Mapper struct {
	files map[string]fileDiff
}

// NewMapper builds a Mapper from diff files. Files without a usable patch are kept
// but map nothing, so a missing patch is reported as unmappable rather than
// silently absent.
func NewMapper(diffFiles []DiffFile) Mapper {
	files := make(map[string]fileDiff, len(diffFiles))

	for _, f := range diffFiles {
		diff := fileDiff{
			path:      f.Path,
			status:    strings.ToLower(strings.TrimSpace(f.Status)),
			truncated: f.Truncated,
			right:     map[int]bool{},
			left:      map[int]bool{},
		}
		parsePatch(f.Patch, &diff)
		files[normalizePath(f.Path)] = diff
	}

	return Mapper{files: files}
}

// NewMapperFromChangedFiles builds a Mapper from GitHub's changed-file listing,
// which is the most complete diff available.
func NewMapperFromChangedFiles(changed []github.ChangedFile) Mapper {
	diffFiles := make([]DiffFile, 0, len(changed))
	for _, f := range changed {
		diffFiles = append(diffFiles, DiffFile{Path: f.Filename, Status: f.Status, Patch: f.Patch})
	}
	return NewMapper(diffFiles)
}

// NewMapperFromSelection builds a Mapper from the selected context. It is the
// fallback when the raw changed-file listing is not at hand; a patch the selector
// truncated maps only the hunks it kept.
func NewMapperFromSelection(selected contextselect.SelectedContext) Mapper {
	diffFiles := make([]DiffFile, 0, len(selected.Files))
	for _, f := range selected.Files {
		diffFiles = append(diffFiles, DiffFile{
			Path:      f.Path,
			Status:    f.Status,
			Patch:     f.Patch,
			Truncated: f.Truncated,
		})
	}
	return NewMapper(diffFiles)
}

// Files returns the paths the mapper knows about, sorted, for diagnostics.
func (m Mapper) Files() []string {
	paths := make([]string, 0, len(m.files))
	for path := range m.files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// Reasons a finding cannot be attached to a line. They are stable strings so
// terminal output and tests do not drift apart.
const (
	ReasonNoPatch       = "no diff available for this file"
	ReasonFileNotInDiff = "file is not part of the pull request diff"
	ReasonLineNotInDiff = "line is not part of the pull request diff"
)

// Map returns the GitHub location for finding, or ok=false with the reason it
// cannot be placed inline.
//
// A finding that cannot be mapped is never dropped and never relocated: the caller
// moves it into the review summary, where it is still reported in full.
func (m Mapper) Map(finding findings.Finding) (DiffLocation, bool, string) {
	diff, known := m.files[normalizePath(finding.File)]
	if !known {
		return DiffLocation{}, false, ReasonFileNotInDiff
	}
	if len(diff.right) == 0 && len(diff.left) == 0 {
		return DiffLocation{}, false, ReasonNoPatch
	}

	start, end := finding.StartLine, finding.EndLine
	if end < start {
		end = start
	}

	// A deleted file has no right side at all, so its findings are addressed
	// against the old version. Every other file is addressed against the new
	// version, which is what a reviewer of the change wants to see.
	sides := []struct {
		name  string
		lines map[int]bool
	}{
		{github.SideRight, diff.right},
		{github.SideLeft, diff.left},
	}
	if diff.status == "removed" {
		sides = []struct {
			name  string
			lines map[int]bool
		}{
			{github.SideLeft, diff.left},
			{github.SideRight, diff.right},
		}
	}

	for _, side := range sides {
		// A range is used only when every line in it is in the diff. A partially
		// covered range is not narrowed to a guess; it falls back to the single
		// line the finding itself named.
		if end > start && coversRange(side.lines, start, end) {
			return DiffLocation{
				Path:      diff.path,
				Line:      end,
				Side:      side.name,
				StartLine: start,
				StartSide: side.name,
			}, true, ""
		}
		if side.lines[start] {
			return DiffLocation{Path: diff.path, Line: start, Side: side.name}, true, ""
		}
	}

	return DiffLocation{}, false, ReasonLineNotInDiff
}

// coversRange reports whether every line from start to end is commentable.
func coversRange(lines map[int]bool, start, end int) bool {
	for line := start; line <= end; line++ {
		if !lines[line] {
			return false
		}
	}
	return true
}

// parsePatch walks a unified diff and records which lines exist on which side.
//
// It tracks the old and new line counters exactly as the diff does: a hunk header
// resets both, an added line advances only the new counter, a deleted line only
// the old one, and a context line advances both and is addressable on either side.
// Anything it does not understand — a malformed header, a stray line — leaves the
// counters alone rather than inventing a position.
func parsePatch(patch string, diff *fileDiff) {
	if strings.TrimSpace(patch) == "" {
		return
	}

	oldLine, newLine := 0, 0
	inHunk := false

	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "@@") {
			start, ok := parseHunkHeader(line)
			if !ok {
				// An unparseable header means we no longer know where we are.
				// Stopping is the only safe response; guessing would place
				// comments on lines that do not exist.
				inHunk = false
				continue
			}
			oldLine, newLine = start.oldStart, start.newStart
			inHunk = true
			continue
		}
		if !inHunk || line == "" {
			continue
		}

		switch line[0] {
		case '+':
			diff.right[newLine] = true
			newLine++
		case '-':
			diff.left[oldLine] = true
			oldLine++
		case ' ':
			diff.right[newLine] = true
			diff.left[oldLine] = true
			newLine++
			oldLine++
		case '\\':
			// "\ No newline at end of file" annotates the previous line.
		default:
			// Not a diff body line — a truncation marker, say. Stop trusting the
			// counters until the next hunk header.
			inHunk = false
		}
	}
}

// hunkStart is the pair of first line numbers a hunk header declares.
type hunkStart struct {
	oldStart int
	newStart int
}

// parseHunkHeader reads "@@ -80,5 +80,7 @@" into its two starting line numbers.
func parseHunkHeader(header string) (hunkStart, bool) {
	body := header[2:]
	if idx := strings.Index(body, "@@"); idx >= 0 {
		body = body[:idx]
	}

	fields := strings.Fields(body)
	if len(fields) < 2 {
		return hunkStart{}, false
	}

	oldStart, ok := parseRangeStart(fields[0], '-')
	if !ok {
		return hunkStart{}, false
	}
	newStart, ok := parseRangeStart(fields[1], '+')
	if !ok {
		return hunkStart{}, false
	}

	return hunkStart{oldStart: oldStart, newStart: newStart}, true
}

// parseRangeStart reads the starting line from a "-80,5" or "+80" range spec.
func parseRangeStart(spec string, sign byte) (int, bool) {
	if spec == "" || spec[0] != sign {
		return 0, false
	}

	value := spec[1:]
	if idx := strings.IndexByte(value, ','); idx >= 0 {
		value = value[:idx]
	}

	start, err := strconv.Atoi(value)
	if err != nil || start < 0 {
		return 0, false
	}
	return start, true
}

// normalizePath makes path comparison insensitive to a leading "./" and to
// surrounding whitespace, matching how findings validation compares paths.
func normalizePath(path string) string {
	return strings.TrimPrefix(strings.TrimSpace(path), "./")
}
