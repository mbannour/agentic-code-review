package verification

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/your-company/agentic-code-review/internal/contextselect"
	"github.com/your-company/agentic-code-review/internal/findings"
)

// Bounds on one verification context.
//
// The whole point of verifying per finding is that each check is cheap. Resending the
// pull request for every candidate would double the cost of the review and bury the
// claim under context, so the ceiling here is deliberately a small fraction of the
// review budget.
const (
	// MaxVerificationContextBytes bounds everything gathered for one finding.
	MaxVerificationContextBytes = 32 * 1024

	// maxPatchBytes bounds the changed patch, which is the most valuable section and
	// therefore gets the largest single allowance.
	maxPatchBytes = 16 * 1024

	// nearbyLines is how far either side of the finding's range counts as nearby code
	// when a patch has to be reduced to its relevant hunks.
	nearbyLines = 40

	// maxRelatedFiles bounds how many other changed files may be pulled in because the
	// finding's own evidence cited them.
	maxRelatedFiles = 3

	// maxRelatedPatchBytes bounds each of those.
	maxRelatedPatchBytes = 4 * 1024

	// maxTicketBytes bounds the Jira excerpt.
	maxTicketBytes = 4 * 1024

	// maxRuleBytes bounds one repository rule excerpt.
	maxRuleBytes = 4 * 1024

	// External evidence is selected only when the finding cites it. More than
	// three documents would turn targeted verification into another full review.
	maxExternalEvidence = 3
	maxExternalBytes    = 4 * 1024

	// maxAnalysisBytes bounds one check's output.
	maxAnalysisBytes = 4 * 1024
)

// ErrNoPatch is returned when the finding's file has no diff at all. Verification can
// still proceed, but the caller is told the strongest evidence is missing.
var ErrNoPatch = errors.New("no patch available for the finding's file")

// Excerpt is one bounded piece of evidence gathered for a verification.
type Excerpt struct {
	// Label names the source, e.g. a file path or a check name.
	Label string

	// Content is the excerpt text.
	Content string

	// Truncated reports whether Content was cut to fit.
	Truncated bool
}

// Bytes returns the excerpt's size.
func (e Excerpt) Bytes() int { return len(e.Content) }

// Context is everything one verification is allowed to see.
//
// It is deliberately small and deliberately about one finding. Nothing here is chosen by
// a model: the finding names a file and a line range, and the sections below are what
// that names, in a fixed order, up to a fixed size.
type Context struct {
	Finding findings.Finding

	// RelevantPatch is the diff of the finding's own file, reduced to the hunks around
	// the finding when the whole patch does not fit.
	RelevantPatch Excerpt

	// RelevantCode describes which lines of the new file the finding points at, so the
	// verifier can tell "the code is not there" from "the code does not behave as
	// claimed".
	RelevantCode Excerpt

	// RelatedPatches are other changed files the finding's own evidence cited. They are
	// how "surrounding code may invalidate the claim" becomes checkable.
	RelatedPatches []Excerpt

	// RelevantJira is the ticket excerpt, included only when the finding actually rests
	// on it.
	RelevantJira Excerpt

	// RelevantRules are the repository rule excerpts the finding cited.
	RelevantRules []Excerpt

	// RelevantEvidence contains the configured documents or schema metadata the
	// reviewer cited for this claim.
	RelevantEvidence []Excerpt

	// RelevantAnalysis is the deterministic check evidence: cited checks and failing
	// ones in detail, everything else as a one-line outcome.
	RelevantAnalysis []Excerpt

	// Profile is the detected technology, so the verifier reasons in the right language.
	Profile contextselect.TechnologyProfile

	// Trimmed reports whether anything had to be dropped to fit the budget.
	Trimmed bool

	// DroppedSections names what was dropped, in priority order, so a surprising
	// omission is visible rather than silent.
	DroppedSections []string

	// Bytes is the total size of the gathered evidence.
	Bytes int
}

// HasPatch reports whether the finding's own diff was available.
func (c Context) HasPatch() bool { return strings.TrimSpace(c.RelevantPatch.Content) != "" }

// ContextBuilder assembles a bounded verification context around one finding.
//
// It never reads the repository. Everything it can offer was already gathered by earlier
// stages and bounded there; this only selects the relevant part of it.
type ContextBuilder struct {
	// MaxBytes bounds the whole context. Zero means MaxVerificationContextBytes.
	MaxBytes int
}

// NewContextBuilder returns a ContextBuilder with the default bound.
func NewContextBuilder() *ContextBuilder {
	return &ContextBuilder{MaxBytes: MaxVerificationContextBytes}
}

// Build assembles the context for one finding.
//
// Sections are added in strict priority order — the finding, its patch, the lines it
// names, the files its evidence cites, the ticket, the rules, the check output, the
// profile — and the first section that does not fit ends the process. Nothing is
// truncated at random: a section either fits, is reduced by its own rule, or is dropped
// and named.
func (b *ContextBuilder) Build(
	finding findings.Finding,
	selected contextselect.SelectedContext,
) (Context, error) {
	budget := b.MaxBytes
	if budget <= 0 {
		budget = MaxVerificationContextBytes
	}

	vctx := Context{Finding: finding, Profile: selected.Profile}
	remaining := budget

	// Priority 1: the candidate finding itself. It is never trimmed and never dropped —
	// without it there is nothing to verify.
	remaining -= findingBytes(finding)

	// Priority 2: the exact changed patch, reduced to the hunks around the finding when
	// the whole thing does not fit.
	file, found := fileFor(finding.File, selected.Files)
	if !found || strings.TrimSpace(file.Patch) == "" {
		vctx.Bytes = budget - remaining
		return vctx, ErrNoPatch
	}

	patch, truncated := reducePatch(file.Patch, finding, min(remaining, maxPatchBytes))
	if patch == "" {
		vctx.Bytes = budget - remaining
		return vctx, ErrNoPatch
	}
	vctx.RelevantPatch = Excerpt{Label: file.Path, Content: patch, Truncated: truncated || file.Truncated}
	remaining -= len(patch)

	// Priority 3: which lines the finding points at, in the new file's numbering.
	code := describeLocation(finding, file)
	if len(code) <= remaining {
		vctx.RelevantCode = Excerpt{Label: finding.Location(), Content: code}
		remaining -= len(code)
	} else {
		vctx.dropped("relevant code description")
	}

	// Priority 4: other changed files this finding's own evidence cited.
	for _, related := range relatedFiles(finding, selected.Files) {
		excerpt, ok := fit(related.Path, related.Patch, min(remaining, maxRelatedPatchBytes))
		if !ok {
			vctx.dropped("related patch " + related.Path)
			break
		}
		vctx.RelatedPatches = append(vctx.RelatedPatches, excerpt)
		remaining -= excerpt.Bytes()
	}

	// Priority 5: the ticket, but only when the finding rests on it. An unrelated
	// requirement is context the verifier has no use for.
	if ticket := ticketExcerpt(finding, selected); ticket != "" {
		excerpt, ok := fit("jira", ticket, min(remaining, maxTicketBytes))
		if ok {
			vctx.RelevantJira = excerpt
			remaining -= excerpt.Bytes()
		} else {
			vctx.dropped("jira excerpt")
		}
	}

	// Priority 6: configured documents and schema metadata explicitly cited by
	// the finding. A source id that does not resolve yields no excerpt, which is
	// evidence for an uncertain verdict rather than permission to guess.
	for _, document := range relevantExternalEvidence(finding, selected.Evidence) {
		content := renderExternalEvidence(document)
		excerpt, ok := fit(document.ID, content, min(remaining, maxExternalBytes))
		if !ok {
			vctx.dropped("external evidence " + document.ID)
			break
		}
		vctx.RelevantEvidence = append(vctx.RelevantEvidence, excerpt)
		remaining -= excerpt.Bytes()
	}

	// Priority 7: the repository rules the finding cited.
	for _, rule := range relevantRules(finding, selected.Rules) {
		excerpt, ok := fit(rule.Path, rule.Content, min(remaining, maxRuleBytes))
		if !ok {
			vctx.dropped("rule " + rule.Path)
			break
		}
		vctx.RelevantRules = append(vctx.RelevantRules, excerpt)
		remaining -= excerpt.Bytes()
	}

	// Priority 8: deterministic check evidence.
	for _, excerpt := range analysisExcerpts(finding, selected.Analysis) {
		fitted, ok := fit(excerpt.Label, excerpt.Content, min(remaining, maxAnalysisBytes))
		if !ok {
			vctx.dropped("analysis " + excerpt.Label)
			break
		}
		vctx.RelevantAnalysis = append(vctx.RelevantAnalysis, fitted)
		remaining -= fitted.Bytes()
	}

	// Priority 9: the technology profile, which costs almost nothing and is already set.

	vctx.Bytes = budget - remaining
	return vctx, nil
}

// dropped records a section that did not fit.
func (c *Context) dropped(section string) {
	c.Trimmed = true
	c.DroppedSections = append(c.DroppedSections, section)
}

// findingBytes approximates the finding's own cost in the context.
func findingBytes(finding findings.Finding) int {
	total := len(finding.ID) + len(finding.File) + len(finding.Title) +
		len(finding.Problem) + len(finding.Impact) + len(finding.Suggestion)
	for _, evidence := range finding.Evidence {
		total += len(evidence.Source) + len(evidence.Detail)
	}
	return total
}

// fileFor finds the selected file for a path, tolerating a leading "./".
func fileFor(path string, files []contextselect.SelectedFile) (contextselect.SelectedFile, bool) {
	want := normalizePath(path)
	for _, f := range files {
		if normalizePath(f.Path) == want {
			return f, true
		}
	}
	return contextselect.SelectedFile{}, false
}

// hunkHeader matches a unified diff hunk header and captures the new-side start line.
var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// reducePatch returns the patch, or the hunks near the finding when the whole patch does
// not fit.
//
// Reduction is by hunk, never by byte: half a hunk is not diff, and a verifier reading a
// truncated hunk would be reasoning about code that does not exist. When no hunk covers
// the finding's range, the leading hunks are kept, since the alternative is no diff at
// all.
func reducePatch(patch string, finding findings.Finding, budget int) (string, bool) {
	if budget <= 0 {
		return "", true
	}
	if len(patch) <= budget {
		return patch, false
	}

	hunks := splitHunks(patch)
	if len(hunks) == 0 {
		return "", true
	}

	start, end := finding.StartLine-nearbyLines, finding.EndLine+nearbyLines

	var kept []string
	size := 0
	for _, h := range hunks {
		if h.start > end || h.end < start {
			continue
		}
		if size+len(h.text) > budget {
			break
		}
		kept = append(kept, h.text)
		size += len(h.text)
	}

	if len(kept) == 0 {
		for _, h := range hunks {
			if size+len(h.text) > budget {
				break
			}
			kept = append(kept, h.text)
			size += len(h.text)
		}
	}
	if len(kept) == 0 {
		return "", true
	}

	return strings.Join(kept, ""), true
}

// hunk is one hunk of a unified diff, with the new-side line span it covers.
type hunk struct {
	text  string
	start int
	end   int
}

// splitHunks divides a patch into its hunks. Text before the first hunk header is
// discarded, since it is not diff content.
func splitHunks(patch string) []hunk {
	lines := strings.SplitAfter(patch, "\n")

	var (
		hunks   []hunk
		current *hunk
		builder strings.Builder
	)

	flush := func() {
		if current == nil {
			return
		}
		current.text = builder.String()
		hunks = append(hunks, *current)
		builder.Reset()
		current = nil
	}

	for _, line := range lines {
		if match := hunkHeader.FindStringSubmatch(strings.TrimRight(line, "\n")); match != nil {
			flush()

			start, _ := strconv.Atoi(match[1])
			length := 1
			if match[2] != "" {
				if parsed, err := strconv.Atoi(match[2]); err == nil {
					length = parsed
				}
			}
			if length < 1 {
				length = 1
			}

			current = &hunk{start: start, end: start + length - 1}
			builder.WriteString(line)
			continue
		}
		if current != nil {
			builder.WriteString(line)
		}
	}
	flush()

	return hunks
}

// describeLocation states where the finding points, and whether the diff covers it.
func describeLocation(finding findings.Finding, file contextselect.SelectedFile) string {
	var out strings.Builder

	fmt.Fprintf(&out, "file: %s\n", file.Path)
	fmt.Fprintf(&out, "status: %s\n", file.Status)
	fmt.Fprintf(&out, "claimed lines (new file): %d-%d\n", finding.StartLine, finding.EndLine)

	covered := false
	for _, h := range splitHunks(file.Patch) {
		if finding.StartLine <= h.end && finding.EndLine >= h.start {
			covered = true
			break
		}
	}
	if covered {
		out.WriteString("the diff above covers these lines\n")
	} else {
		out.WriteString("the diff above does not cover these lines; the claim may point outside the change\n")
	}
	if file.Truncated {
		out.WriteString("note: this patch was truncated before it reached this stage\n")
	}

	return out.String()
}

// relatedFiles returns the other changed files this finding's evidence cited.
//
// Only the finding's own citations are followed. Pulling in neighbours by guesswork is
// how a targeted verification turns back into a second full review.
func relatedFiles(finding findings.Finding, files []contextselect.SelectedFile) []contextselect.SelectedFile {
	own := normalizePath(finding.File)

	var related []contextselect.SelectedFile
	seen := map[string]bool{own: true}

	for _, evidence := range finding.Evidence {
		if evidence.Type != findings.EvidenceCode {
			continue
		}
		for _, f := range files {
			path := normalizePath(f.Path)
			if seen[path] || !strings.Contains(evidence.Source, f.Path) {
				continue
			}
			if strings.TrimSpace(f.Patch) == "" {
				continue
			}
			seen[path] = true
			related = append(related, f)
			if len(related) >= maxRelatedFiles {
				return related
			}
		}
	}

	return related
}

// ticketExcerpt renders the Jira context when the finding actually rests on it.
//
// A requirement finding is about the ticket by definition, and any finding citing Jira
// evidence needs the wording it claims to be quoting. Every other finding gets nothing:
// the ticket cannot help verify a null dereference, and sending it would only add cost.
func ticketExcerpt(finding findings.Finding, selected contextselect.SelectedContext) string {
	if !selected.HasTicket() {
		return ""
	}

	cited := finding.Category == findings.CategoryRequirement
	for _, evidence := range finding.Evidence {
		if evidence.Type == findings.EvidenceJira {
			cited = true
			break
		}
	}
	if !cited {
		return ""
	}

	ticket := selected.Ticket

	var out strings.Builder
	fmt.Fprintf(&out, "key: %s\n", ticket.Key)
	fmt.Fprintf(&out, "status: %s\n", ticket.Status)
	fmt.Fprintf(&out, "summary: %s\n", ticket.Summary)
	if ticket.Description != "" {
		fmt.Fprintf(&out, "description:\n%s\n", ticket.Description)
	}
	if ticket.DescriptionTruncated {
		out.WriteString("note: this description was truncated before it reached this stage\n")
	}

	return out.String()
}

// relevantRules returns the rule documents this finding cited.
func relevantRules(finding findings.Finding, rules []contextselect.SelectedRule) []contextselect.SelectedRule {
	var cited []string
	for _, evidence := range finding.Evidence {
		if evidence.Type == findings.EvidenceRule {
			cited = append(cited, evidence.Source)
		}
	}
	if len(cited) == 0 {
		return nil
	}

	var relevant []contextselect.SelectedRule
	for _, rule := range rules {
		for _, source := range cited {
			// A citation may name the document or quote from it; matching the path is
			// the deterministic half, and a rule finding that cites nothing specific
			// gets every rule document that was loaded.
			if strings.Contains(source, rule.Path) || !mentionsAnyPath(cited, rules) {
				relevant = append(relevant, rule)
				break
			}
		}
	}

	return relevant
}

// relevantExternalEvidence resolves only explicit document/schema citations.
func relevantExternalEvidence(
	finding findings.Finding,
	documents []contextselect.SelectedEvidence,
) []contextselect.SelectedEvidence {
	var sources []string
	for _, item := range finding.Evidence {
		if item.Type == findings.EvidenceDocument || item.Type == findings.EvidenceSchema {
			sources = append(sources, strings.ToLower(strings.TrimSpace(item.Source)))
		}
	}
	if len(sources) == 0 {
		return nil
	}

	var relevant []contextselect.SelectedEvidence
	for _, document := range documents {
		for _, source := range sources {
			if source == "" {
				continue
			}
			if source == strings.ToLower(document.ID) {
				relevant = append(relevant, document)
				break
			}
		}
		if len(relevant) >= maxExternalEvidence {
			break
		}
	}
	return relevant
}

func renderExternalEvidence(document contextselect.SelectedEvidence) string {
	var out strings.Builder
	fmt.Fprintf(&out, "id: %s\n", document.ID)
	fmt.Fprintf(&out, "kind: %s\n", document.Kind)
	fmt.Fprintf(&out, "source type: %s\n", document.SourceType)
	fmt.Fprintf(&out, "locator: %s\n", document.Locator)
	if document.Revision != "" {
		fmt.Fprintf(&out, "revision: %s\n", document.Revision)
	}
	if document.Digest != "" {
		fmt.Fprintf(&out, "digest: %s\n", document.Digest)
	}
	if document.Title != "" {
		fmt.Fprintf(&out, "title: %s\n", document.Title)
	}
	fmt.Fprintf(&out, "content:\n%s\n", document.Content)
	if document.Truncated {
		out.WriteString("note: this evidence was truncated before verification\n")
	}
	return out.String()
}

// mentionsAnyPath reports whether any citation names any loaded rule document.
func mentionsAnyPath(cited []string, rules []contextselect.SelectedRule) bool {
	for _, source := range cited {
		for _, rule := range rules {
			if strings.Contains(source, rule.Path) {
				return true
			}
		}
	}
	return false
}

// analysisExcerpts returns the deterministic evidence worth sending.
//
// Cited and failing checks come with their output, because that is what a claim can be
// checked against. Everything else is reduced to its outcome, which is still needed: a
// passing suite does not disprove a finding unless it actually covers the behavior, and
// the verifier can only weigh that if it knows the suite passed.
func analysisExcerpts(finding findings.Finding, checks []contextselect.SelectedAnalysis) []Excerpt {
	cited := map[string]bool{}
	for _, evidence := range finding.Evidence {
		if evidence.Type != findings.EvidenceTest && evidence.Type != findings.EvidenceVet {
			continue
		}
		for _, check := range checks {
			if strings.Contains(evidence.Source, check.Name) || strings.Contains(evidence.Source, check.Command) {
				cited[check.Name] = true
			}
		}
	}

	var excerpts []Excerpt
	for _, check := range checks {
		var out strings.Builder
		fmt.Fprintf(&out, "check: %s\n", check.Name)
		fmt.Fprintf(&out, "command: %s\n", check.Command)
		fmt.Fprintf(&out, "status: %s\n", checkStatus(check))

		detailed := cited[check.Name] || (!check.Passed && !check.Skipped)
		if detailed && check.Output != "" {
			fmt.Fprintf(&out, "exit: %d\noutput:\n%s\n", check.ExitCode, check.Output)
		}

		excerpts = append(excerpts, Excerpt{Label: check.Name, Content: out.String()})
	}

	return excerpts
}

// checkStatus renders a check's outcome.
func checkStatus(check contextselect.SelectedAnalysis) string {
	switch {
	case check.Skipped:
		return "skipped"
	case check.TimedOut:
		return "timed out"
	case check.Passed:
		return "passed"
	default:
		return "failed"
	}
}

// fit returns content bounded to budget, reporting false when nothing useful fits.
//
// Prose sections are cut at a line boundary with a marker, which is honest about having
// been cut. Diffs never come through here — they are reduced by hunk instead.
func fit(label string, content string, budget int) (Excerpt, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return Excerpt{}, false
	}

	const minimumUseful = 200
	if budget < minimumUseful {
		return Excerpt{}, false
	}
	if len(trimmed) <= budget {
		return Excerpt{Label: label, Content: trimmed}, true
	}

	const marker = "\n[TRUNCATED for verification]\n"
	cut := budget - len(marker)
	if cut < 0 {
		cut = 0
	}
	if idx := strings.LastIndexByte(trimmed[:cut], '\n'); idx > 0 {
		cut = idx
	}

	return Excerpt{Label: label, Content: trimmed[:cut] + marker, Truncated: true}, true
}

// normalizePath makes path comparison insensitive to a leading "./" and to surrounding
// whitespace, matching how findings validation compares paths.
func normalizePath(path string) string {
	return strings.TrimPrefix(strings.TrimSpace(path), "./")
}

// min returns the smaller of two ints.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
