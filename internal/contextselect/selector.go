package contextselect

import (
	"context"
	"sort"
	"strings"

	"github.com/your-company/agentic-code-review/internal/analysis"
	"github.com/your-company/agentic-code-review/internal/retrieval"
	"github.com/your-company/agentic-code-review/internal/review"
	"github.com/your-company/agentic-code-review/internal/technology"
)

// maxCheckOutputLines bounds a failing check's snippet by line count as well as
// by bytes, since a handful of very long lines is rarely the useful part.
const maxCheckOutputLines = 60

// RepositoryReader reads one explicitly chosen file from the repository.
//
// It is declared here so a later step can enrich the selection with specific
// files — never to scan or search. The selector does not use it yet.
type RepositoryReader interface {
	ReadFile(ctx context.Context, path string, ref string) (string, error)
}

// candidate is a changed file under consideration, with its classification
// resolved. It is the selector's internal working type.
type candidate struct {
	Path   string
	Status string
	Patch  string

	Kind       FileKind
	Importance Importance
	Reason     string

	// order is the file's position in the original change list, used to keep
	// sorting stable.
	order int

	// rank is the file's selection priority, lowest first. It defaults to the rank
	// of its kind and is raised for files the detected technology makes important,
	// so priority can be language-aware without inventing new kinds.
	rank int
}

// Selector turns a review context and its analysis evidence into a bounded,
// ranked selection. It is deterministic and stateless between calls.
type Selector struct {
	budget Budget
}

// NewSelector returns a Selector using the default budget.
func NewSelector() *Selector {
	return &Selector{budget: DefaultBudget()}
}

// NewSelectorWithBudget returns a Selector using an explicit budget. Unset
// allowances fall back to the defaults.
func NewSelectorWithBudget(budget Budget) *Selector {
	return &Selector{budget: budget.normalized()}
}

// Budget returns the allowances in force.
func (s *Selector) Budget() Budget { return s.budget.normalized() }

// Select builds the bounded selection.
//
// The order of work matters: classify, then rank, then spend the budget from the
// highest priority down, so anything dropped is always the least valuable content
// rather than whatever happened to come last.
func (s *Selector) Select(
	ctx context.Context,
	reviewCtx review.Context,
	analysisResult analysis.Result,
) (SelectedContext, error) {
	return s.SelectWithTechnology(ctx, reviewCtx, analysisResult, technology.Profile{})
}

// SelectWithTechnology builds the selection using a profile already detected from the
// repository itself.
//
// The two detection paths are merged rather than one replacing the other: the
// repository manifests know the language even when the diff does not, and the diff
// knows about a dependency the manifests may not mention. The merged profile then
// steers priority — Scala build files matter more when Scala is what changed.
func (s *Selector) SelectWithTechnology(
	ctx context.Context,
	reviewCtx review.Context,
	analysisResult analysis.Result,
	detected technology.Profile,
) (SelectedContext, error) {
	return s.SelectWithRetrieval(ctx, reviewCtx, analysisResult, detected, retrieval.Result{
		Skipped: true, Reason: "retrieval not requested",
	})
}

// SelectWithRetrieval builds the selection including unchanged code retrieved from
// the local checkout.
//
// Retrieved code is spent last, after the changed patches have taken what they
// need. That ordering is the whole policy: context about a change is worth less
// than the change, so a large diff quietly costs retrieval its budget rather than
// costing the reviewer a patch.
func (s *Selector) SelectWithRetrieval(
	ctx context.Context,
	reviewCtx review.Context,
	analysisResult analysis.Result,
	detected technology.Profile,
	retrieved retrieval.Result,
) (SelectedContext, error) {
	if err := ctx.Err(); err != nil {
		return SelectedContext{}, err
	}

	budget := s.budget.normalized()

	candidates := classifyCandidates(reviewCtx.Changes.Files)
	markRelatedTests(candidates)

	profile := mergeProfiles(detected, candidates)
	prioritizeBuildFiles(candidates, profile)

	selected := SelectedContext{
		PullRequest: summarizePullRequest(reviewCtx.PullRequest),
		Profile:     profile,
	}

	stats := SelectionStats{
		CandidateFiles: len(candidates),
		BudgetBytes:    budget.Total,
	}

	// Fixed-allowance sections first: the ticket, repository rules, external
	// evidence, and analysis. Each is capped, so none can starve the diffs.
	// The description and the conversation share one allowance. Both answer the
	// same question — what did the people involved intend — and the description
	// takes what it needs first because the author's own statement of intent
	// outranks a remark about it.
	description, discussion, discussionBytes, discussionOriginal :=
		selectDiscussion(reviewCtx.PullRequest.Body, reviewCtx.Discussion.Comments, budget)
	selected.PullRequest.Description = description.Content
	selected.PullRequest.DescriptionTruncated = description.Truncated
	selected.Discussion = discussion

	ticket, ticketBytes, ticketOriginal := selectTicket(reviewCtx.Ticket, budget)
	selected.Ticket = ticket

	rules, ruleBytes, ruleOriginal := selectRules(reviewCtx.Rules.Documents, budget)
	selected.Rules = rules

	// Proposed policy changes are copied wholesale: they carry no content, so they
	// cost a handful of bytes and cannot crowd anything out.
	for _, change := range reviewCtx.Rules.ProposedChanges {
		selected.ProposedRuleChanges = append(selected.ProposedRuleChanges, ProposedRuleChange{
			Path: change.Path, Kind: change.Kind,
			BaseBytes: change.BaseBytes, HeadBytes: change.HeadBytes,
		})
	}

	evidenceBudget := budget
	evidenceBudget.Evidence = minInt(budget.Evidence,
		remainingBudget(budget.Total, discussionBytes+ticketBytes+ruleBytes))
	documents, evidenceBytes, evidenceOriginal, evidenceDropped :=
		selectEvidence(reviewCtx.Evidence.Documents, evidenceBudget)
	selected.Evidence = documents

	analysisBudget := budget
	analysisBudget.Analysis = minInt(budget.Analysis,
		remainingBudget(budget.Total, discussionBytes+ticketBytes+ruleBytes+evidenceBytes))
	if analysisBudget.PerCheckOutput > analysisBudget.Analysis {
		analysisBudget.PerCheckOutput = analysisBudget.Analysis
	}
	checks, analysisBytes, analysisOriginal := selectAnalysis(analysisResult, analysisBudget)
	selected.Analysis = checks

	stats.CandidateEvidence = len(reviewCtx.Evidence.Documents)
	stats.SelectedEvidence = len(documents)
	stats.DroppedEvidence = evidenceDropped
	stats.CandidateComments = len(reviewCtx.Discussion.Comments)
	stats.SelectedComments = len(discussion)
	stats.OriginalBytes = discussionOriginal + ticketOriginal + ruleOriginal +
		evidenceOriginal + analysisOriginal
	stats.SelectedBytes = discussionBytes + ticketBytes + ruleBytes + evidenceBytes + analysisBytes

	// Whatever is left belongs to the changed patches.
	remaining := budget.Total - stats.SelectedBytes
	if remaining < 0 {
		remaining = 0
	}

	files, dropped, patchBytes, patchOriginal := selectFiles(candidates, remaining)
	selected.Files = files

	stats.OriginalBytes += patchOriginal
	stats.SelectedBytes += patchBytes
	stats.SelectedFiles = len(files)
	stats.DroppedFiles = len(dropped)
	stats.Dropped = dropped

	// Retrieved code takes what the patches left, capped by its own allowance.
	retrievalBudget := minInt(budget.Retrieval, remainingBudget(budget.Total, stats.SelectedBytes))
	code, codeBytes, codeOriginal, codeDropped := selectCode(retrieved, retrievalBudget)
	selected.Code = code

	stats.CandidateCode = len(retrieved.Snippets)
	stats.SelectedCode = len(code)
	stats.DroppedCode = codeDropped
	stats.RetrievalSkipped = retrievalSkipReason(retrieved)
	stats.OriginalBytes += codeOriginal
	stats.SelectedBytes += codeBytes

	stats.Truncated = anyTruncated(selected, dropped) || evidenceDropped > 0 || codeDropped > 0

	selected.Stats = stats
	return selected, nil
}

// retrievalSkipReason explains an empty code section, so a reader can tell a
// repository with nothing to retrieve from a stage that was never asked to run.
func retrievalSkipReason(retrieved retrieval.Result) string {
	if retrieved.Skipped {
		return retrieved.Reason
	}
	return ""
}

// selectCode fits retrieved snippets into the remaining budget.
//
// Snippets arrive already ranked by the retriever — definitions before callers —
// so the budget is spent in that order and the first snippet that does not fit
// ends the section. A snippet is kept whole or not at all: half a function body
// is not context, it is a misleading fragment.
func selectCode(retrieved retrieval.Result, budget int) ([]SelectedCode, int, int, int) {
	if retrieved.Skipped || len(retrieved.Snippets) == 0 || budget <= 0 {
		original := 0
		for _, snippet := range retrieved.Snippets {
			original += snippet.Bytes()
		}
		return nil, 0, original, len(retrieved.Snippets)
	}

	var selected []SelectedCode
	used, original, dropped := 0, 0, 0

	for _, snippet := range retrieved.Snippets {
		original += snippet.Bytes()
		if used+snippet.Bytes() > budget {
			dropped++
			continue
		}
		used += snippet.Bytes()
		selected = append(selected, SelectedCode{
			Symbol:        snippet.Symbol,
			Relation:      snippet.Relation,
			Path:          snippet.Path,
			StartLine:     snippet.StartLine,
			EndLine:       snippet.EndLine,
			Content:       snippet.Content,
			OriginalBytes: snippet.Bytes(),
			Truncated:     snippet.Truncated,
		})
	}
	return selected, used, original, dropped
}

func remainingBudget(total, used int) int {
	if used >= total {
		return 0
	}
	return total - used
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// classifyCandidates resolves each changed file's kind, importance, and reason.
func classifyCandidates(files []review.FileChange) []candidate {
	candidates := make([]candidate, 0, len(files))

	for i, f := range files {
		kind := Classify(f.Filename, f.Patch)
		candidates = append(candidates, candidate{
			Path:       f.Filename,
			Status:     f.Status,
			Patch:      f.Patch,
			Kind:       kind,
			Importance: ImportanceFor(kind),
			Reason:     reasonFor(kind),
			order:      i,
		})
	}

	return candidates
}

// markRelatedTests notes tests that pair with a changed source file, by simple
// path matching: foo.go and foo_test.go in the same directory. No AST or call
// graph is involved.
func markRelatedTests(candidates []candidate) {
	sources := map[string]bool{}
	for _, c := range candidates {
		if c.Kind == FileKindSource {
			sources[c.Path] = true
		}
	}
	if len(sources) == 0 {
		return
	}

	for i := range candidates {
		if candidates[i].Kind != FileKindTest {
			continue
		}
		for _, source := range sourcesForTest(candidates[i].Path) {
			if sources[source] {
				candidates[i].Reason = "test related to changed source file"
				candidates[i].Importance = ImportanceHigh
				break
			}
		}
	}
}

// sourcesForTest maps a test path onto the source paths it may cover.
//
// The relation is lexical only, by the naming conventions each language uses: foo.go
// pairs with foo_test.go, and Foo.scala pairs with FooSpec.scala or FooTest.scala. A
// Scala test usually lives under src/test while its subject lives under src/main, so
// more than one candidate is returned. No symbol analysis is involved.
func sourcesForTest(testPath string) []string {
	if strings.HasSuffix(testPath, "_test.go") {
		return []string{strings.TrimSuffix(testPath, "_test.go") + ".go"}
	}
	if sources, ok := technology.ScalaSourceForTest(testPath); ok {
		return sources
	}
	return nil
}

// effectiveRank is the candidate's selection priority: its own rank when one was
// assigned, otherwise the rank of its kind.
func (c candidate) effectiveRank() int {
	if c.rank > 0 {
		return c.rank
	}
	return kindRank(c.Kind)
}

// mergeProfiles combines the repository-level profile with what the diff reveals.
func mergeProfiles(detected technology.Profile, candidates []candidate) TechnologyProfile {
	signals := make([]technology.FileSignal, 0, len(candidates))
	for _, c := range candidates {
		signals = append(signals, technology.FileSignal{Path: c.Path, Content: c.Patch})
	}

	return fromTechnologyProfile(technology.Merge(detected, technology.DetectFromFiles(signals)))
}

// prioritizeBuildFiles raises the standing of build definitions when the language they
// build is part of the review.
//
// A build.sbt change is ordinary configuration in a Go repository and load-bearing in
// a Scala one: it decides dependency versions, compiler options, and what the test
// task even runs. Raising it only when Scala is detected keeps unrelated build files
// from crowding out source diffs.
func prioritizeBuildFiles(candidates []candidate, profile TechnologyProfile) {
	for i := range candidates {
		if candidates[i].Kind == FileKindGenerated {
			continue
		}

		switch {
		case (profile.HasLanguage(LanguageScala) || profile.HasBuildSystem(BuildSystemSBT)) &&
			technology.IsSBTBuildFile(candidates[i].Path):
			candidates[i].Importance = ImportanceHigh
			candidates[i].Reason = "sbt build definition"
			candidates[i].rank = rankBuildDefinition

		case (profile.HasLanguage(LanguageJavaScript) || profile.HasLanguage(LanguageTypeScript) ||
			profile.HasBuildSystem(BuildSystemNPM)) && technology.IsNPMBuildFile(candidates[i].Path):
			candidates[i].Importance = ImportanceHigh
			candidates[i].Reason = "npm/frontend build definition"
			candidates[i].rank = rankBuildDefinition
		}
	}
}

// selectFiles spends the patch budget in priority order, truncating a patch that
// nearly fits and dropping what does not. Returns the kept files, the dropped
// ones, and the selected and original byte totals.
func selectFiles(candidates []candidate, budget int) ([]SelectedFile, []DroppedFile, int, int) {
	ordered := make([]candidate, len(candidates))
	copy(ordered, candidates)

	// Stable priority order: by kind rank, then by original position, so the
	// result never depends on anything but the input.
	sort.SliceStable(ordered, func(i, j int) bool {
		ri, rj := ordered[i].effectiveRank(), ordered[j].effectiveRank()
		if ri != rj {
			return ri < rj
		}
		return ordered[i].order < ordered[j].order
	})

	var (
		selected  []SelectedFile
		dropped   []DroppedFile
		usedBytes int
		original  int
		remaining = budget
	)

	for _, c := range ordered {
		size := len(c.Patch)
		original += size

		file := SelectedFile{
			Path:          c.Path,
			Status:        c.Status,
			Kind:          c.Kind,
			Importance:    c.Importance,
			Reason:        c.Reason,
			OriginalBytes: size,
		}

		switch {
		case size == 0:
			// A file with no patch — binary, or too large for the API — still
			// belongs in the selection as a fact about the change.
			selected = append(selected, file)

		case size <= remaining:
			file.Patch = c.Patch
			remaining -= size
			usedBytes += size
			selected = append(selected, file)

		case remaining >= minimumPatchBytes:
			// Enough room for a useful fragment: keep the head and say so.
			patch, truncated := truncateTo(c.Patch, remaining-len(MarkerPatch)-1, MarkerPatch)
			file.Patch = patch
			file.Truncated = truncated
			usedBytes += len(patch)
			remaining = 0
			selected = append(selected, file)

		default:
			dropped = append(dropped, DroppedFile{
				Path:       c.Path,
				Kind:       c.Kind,
				Importance: c.Importance,
				Reason:     "context budget exhausted",
				Bytes:      size,
			})
		}
	}

	return selected, dropped, usedBytes, original
}

// selectRules keeps the repository rule documents in their existing priority
// order, capped by the rules allowance.
func selectRules(documents []review.RuleDocument, budget Budget) ([]SelectedRule, int, int) {
	if len(documents) == 0 {
		return nil, 0, 0
	}

	var (
		rules     []SelectedRule
		usedBytes int
		original  int
		remaining = budget.Rules
	)

	for _, d := range documents {
		size := len(d.Content)
		original += size

		rule := SelectedRule{
			Path: d.Path, OriginalBytes: size,
			Revision: d.Revision, Ref: d.Ref,
		}

		switch {
		case size <= remaining:
			rule.Content = d.Content
			remaining -= size

		case remaining > len(MarkerRule)+1:
			content, truncated := truncateTo(d.Content, remaining-len(MarkerRule)-1, MarkerRule)
			rule.Content = content
			rule.Truncated = truncated
			remaining = 0

		default:
			// No room left for content, but the document's existence is itself
			// information: record the path with the marker only.
			rule.Content = MarkerRule
			rule.Truncated = true
			remaining = 0
		}

		usedBytes += len(rule.Content)
		rules = append(rules, rule)
	}

	return rules, usedBytes, original
}

// boundedText is a piece of text after the budget has been applied to it.
type boundedText struct {
	Content   string
	Truncated bool
}

// selectDiscussion fits the pull request description and the human comments into
// the discussion allowance.
//
// Order is the policy: the description first, because the author's own statement
// of what the change is for is the most useful sentence in the conversation, then
// comments on diff lines, then general conversation. Inline comments come before
// general ones because a remark attached to a line is about the code under review,
// while a general one is often about process.
//
// A comment is kept whole or dropped. Half a comment is worse than none: an
// explanation cut before its "but" reverses its meaning.
func selectDiscussion(
	description string,
	comments []review.Comment,
	budget Budget,
) (boundedText, []SelectedComment, int, int) {
	remaining := budget.Discussion
	original := len(strings.TrimSpace(description))

	var bounded boundedText
	if trimmed := strings.TrimSpace(description); trimmed != "" {
		switch {
		case len(trimmed) <= remaining:
			bounded.Content = trimmed
			remaining -= len(trimmed)
		case remaining > len(MarkerDescription)+1:
			content, truncated := truncateTo(trimmed, remaining-len(MarkerDescription)-1, MarkerDescription)
			bounded.Content = content
			bounded.Truncated = truncated
			remaining = 0
		default:
			remaining = 0
		}
	}

	var selected []SelectedComment
	for _, comment := range orderComments(comments) {
		original += len(comment.Body)
		if len(comment.Body) > remaining {
			continue
		}
		remaining -= len(comment.Body)
		selected = append(selected, SelectedComment{
			Kind: comment.Kind, Author: comment.Author, Body: comment.Body,
			Path: comment.Path, Line: comment.Line, Truncated: comment.Truncated,
		})
	}

	return bounded, selected, budget.Discussion - remaining, original
}

// orderComments puts comments on diff lines before general conversation, keeping
// the original order within each group.
func orderComments(comments []review.Comment) []review.Comment {
	ordered := make([]review.Comment, 0, len(comments))
	for _, comment := range comments {
		if comment.Path != "" {
			ordered = append(ordered, comment)
		}
	}
	for _, comment := range comments {
		if comment.Path == "" {
			ordered = append(ordered, comment)
		}
	}
	return ordered
}

// selectEvidence keeps external documents in explicit configuration order. A
// document may be truncated, but lower-priority documents are dropped once the
// combined allowance is exhausted rather than represented by misleading empty
// shells.
func selectEvidence(documents []review.EvidenceDocument, budget Budget) ([]SelectedEvidence, int, int, int) {
	if len(documents) == 0 {
		return nil, 0, 0, 0
	}

	var selected []SelectedEvidence
	used, original, dropped := 0, 0, 0
	remaining := budget.Evidence

	for _, document := range documents {
		size := len(document.Content)
		original += size
		if remaining <= len(MarkerEvidence)+1 {
			dropped++
			continue
		}

		item := SelectedEvidence{
			ID: document.ID, Kind: document.Kind, SourceType: document.SourceType,
			Locator: document.Locator, Title: document.Title, Revision: document.Revision,
			Digest: document.Digest, OriginalBytes: size, Truncated: document.Truncated,
		}
		if size <= remaining {
			item.Content = document.Content
			remaining -= size
		} else {
			item.Content, _ = truncateTo(document.Content,
				remaining-len(MarkerEvidence)-1, MarkerEvidence)
			item.Truncated = true
			remaining = 0
		}
		used += len(item.Content)
		selected = append(selected, item)
	}

	return selected, used, original, dropped
}

// selectAnalysis turns check results into evidence. A passing or skipped check
// contributes its status only; a failing one contributes a bounded snippet, since
// that is the part worth reading.
func selectAnalysis(result analysis.Result, budget Budget) ([]SelectedAnalysis, int, int) {
	if len(result.Checks) == 0 {
		return nil, 0, 0
	}

	var (
		checks    []SelectedAnalysis
		usedBytes int
		original  int
		remaining = budget.Analysis
	)

	for _, c := range result.Checks {
		selectedCheck := SelectedAnalysis{
			Name:     c.Name,
			Command:  c.Command,
			Passed:   c.Passed,
			Skipped:  c.Skipped,
			TimedOut: c.TimedOut,
			ExitCode: c.ExitCode,
		}

		raw := checkOutput(c)
		original += len(raw)

		if c.Passed || c.Skipped || raw == "" {
			// "PASS go vet ./..." is the whole message.
			checks = append(checks, selectedCheck)
			continue
		}

		selectedCheck.OriginalBytes = len(raw)

		snippet, lineTruncated := truncateLines(raw, maxCheckOutputLines)

		limit := budget.PerCheckOutput
		if remaining < limit {
			limit = remaining
		}

		snippet, byteTruncated := truncateTo(snippet, limit, MarkerAnalysis)
		selectedCheck.Output = snippet
		selectedCheck.Truncated = lineTruncated || byteTruncated

		usedBytes += len(snippet)
		remaining -= len(snippet)
		if remaining < 0 {
			remaining = 0
		}

		checks = append(checks, selectedCheck)
	}

	return checks, usedBytes, original
}

// checkOutput prefers stdout, where Go tools report detail, and falls back to
// stderr.
func checkOutput(c analysis.CheckResult) string {
	if out := strings.TrimSpace(c.Stdout); out != "" {
		return out
	}
	return strings.TrimSpace(c.Stderr)
}

// selectTicket carries the Jira context forward. The key and summary are never
// dropped; only the description is subject to the budget.
func selectTicket(ticket *review.TicketContext, budget Budget) (*TicketSummary, int, int) {
	if ticket == nil {
		return nil, 0, 0
	}

	summary := &TicketSummary{
		Key:       ticket.Key,
		Summary:   ticket.Summary,
		Status:    ticket.Status,
		IssueType: ticket.IssueType,
		Priority:  ticket.Priority,
		ParentKey: ticket.ParentKey,
	}

	if len(ticket.Labels) > 0 {
		summary.Labels = make([]string, len(ticket.Labels))
		copy(summary.Labels, ticket.Labels)
	}

	description, truncated := truncateTo(ticket.Description, budget.TicketDescription, MarkerTicketDescription)
	summary.Description = description
	summary.DescriptionTruncated = truncated

	// The key and summary are small and mandatory, so only the description counts
	// against the budget.
	return summary, len(description), len(ticket.Description)
}

// summarizePullRequest copies the metadata worth carrying forward. Nothing here
// touches credentials or environment data.
func summarizePullRequest(pr review.PullRequestContext) PullRequestSummary {
	return PullRequestSummary{
		Owner:      pr.Owner,
		Repository: pr.Repository,
		Number:     pr.Number,
		Title:      pr.Title,
		Author:     pr.Author,
		BaseBranch: pr.BaseBranch,
		HeadBranch: pr.HeadBranch,
		HeadSHA:    pr.HeadSHA,
		Draft:      pr.Draft,
	}
}

// anyTruncated reports whether anything was cut or dropped.
func anyTruncated(selected SelectedContext, dropped []DroppedFile) bool {
	if len(dropped) > 0 {
		return true
	}
	for _, f := range selected.Files {
		if f.Truncated {
			return true
		}
	}
	for _, r := range selected.Rules {
		if r.Truncated {
			return true
		}
	}
	for _, a := range selected.Analysis {
		if a.Truncated {
			return true
		}
	}
	for _, document := range selected.Evidence {
		if document.Truncated {
			return true
		}
	}
	return selected.Ticket != nil && selected.Ticket.DescriptionTruncated
}
