package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/your-company/agentic-code-review/internal/analysis"
	"github.com/your-company/agentic-code-review/internal/claude"
	"github.com/your-company/agentic-code-review/internal/contextselect"
	"github.com/your-company/agentic-code-review/internal/evaluation"
	"github.com/your-company/agentic-code-review/internal/evidence"
	"github.com/your-company/agentic-code-review/internal/findings"
	"github.com/your-company/agentic-code-review/internal/github"
	"github.com/your-company/agentic-code-review/internal/jira"
	"github.com/your-company/agentic-code-review/internal/publish"
	"github.com/your-company/agentic-code-review/internal/reporules"
	"github.com/your-company/agentic-code-review/internal/review"
	"github.com/your-company/agentic-code-review/internal/technology"
	"github.com/your-company/agentic-code-review/internal/verification"
)

func Run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	switch args[0] {
	case "review":
		return runReview(args[1:])
	case "evaluate", "eval":
		return runEvaluate(args[1:])

	case "help", "-h", "--help":
		printUsage()
		return nil

	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runReview(args []string) error {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)

	prURL := fs.String(
		"pr",
		"",
		"GitHub pull request URL",
	)

	ticket := fs.String(
		"ticket",
		"",
		"Jira ticket key",
	)

	format := fs.String(
		"format",
		"markdown",
		"output format: markdown or json",
	)

	repoDir := fs.String(
		"repo-dir",
		"",
		"path to a local checkout to run deterministic checks against (optional)",
	)

	evidenceConfig := fs.String(
		"evidence-config",
		"",
		"versioned JSON configuration for read-only external evidence connectors",
	)

	capturePredictions := fs.String(
		"capture-predictions",
		"",
		"write this run's validated findings to a new prediction snapshot for arc evaluate",
	)

	captureCase := fs.String(
		"capture-case",
		"",
		"evaluation case ID this run is captured under (default: owner/repo#number)",
	)

	captureRun := fs.String(
		"capture-run",
		"",
		"evaluation run name recorded in the snapshot (required with --capture-predictions)",
	)

	useClaude := fs.Bool(
		"claude",
		false,
		"run the review through the locally installed Claude Code CLI",
	)

	doPublish := fs.Bool(
		"publish",
		false,
		"publish the validated findings as a GitHub pull request review (requires --claude)",
	)

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Publishing is the only write this tool performs, and it can only describe a
	// review that actually ran. Refusing the combination up front keeps the flag
	// from looking like it did something.
	if *doPublish && !*useClaude {
		return errors.New("--publish requires --claude")
	}

	// Capture records what the reviewer proposed, so there is nothing to capture
	// without a reviewer run. Refusing here keeps a long review from ending in a
	// flag error, and keeps the flag from looking like it produced a snapshot.
	if *capturePredictions != "" && !*useClaude {
		return errors.New("--capture-predictions requires --claude")
	}
	if *capturePredictions == "" && (*captureCase != "" || *captureRun != "") {
		return errors.New("--capture-case and --capture-run require --capture-predictions")
	}
	if *capturePredictions != "" && strings.TrimSpace(*captureRun) == "" {
		return errors.New("--capture-run is required with --capture-predictions")
	}

	if *prURL == "" {
		return errors.New("--pr is required")
	}

	pr, err := github.ParsePullRequestURL(*prURL)
	if err != nil {
		return fmt.Errorf("invalid --pr: %w", err)
	}

	switch *format {
	case "markdown", "json":
	default:
		return fmt.Errorf(
			"invalid format %q: expected markdown or json",
			*format,
		)
	}

	// Validate explicit ticket input up front so a typo fails before any
	// network call.
	if *ticket != "" {
		if _, err := jira.ParseTicketKey(*ticket); err != nil {
			return fmt.Errorf("invalid --ticket: %w", err)
		}
	}

	// Parse and validate connector capability configuration before any network
	// request. Collection happens later, but an unsafe path or arbitrary source
	// shape is rejected up front.
	var evidenceConnectors []evidence.Connector
	if *evidenceConfig != "" {
		config, root, err := evidence.LoadConfig(*evidenceConfig)
		if err != nil {
			return err
		}
		evidenceConnectors, err = evidence.BuildConnectors(config, root)
		if err != nil {
			return err
		}
	}

	client, err := github.NewClientFromEnv()
	if err != nil {
		return err
	}

	ctx := context.Background()

	details, err := client.GetPullRequest(ctx, pr)
	if err != nil {
		return fmt.Errorf("fetch pull request: %w", err)
	}

	files, err := client.GetPullRequestFiles(ctx, pr)
	if err != nil {
		return fmt.Errorf("fetch changed files: %w", err)
	}

	ticketKey, ticketFound, ticketErr := jira.ResolveTicketKey(jira.TicketSources{
		Explicit: *ticket,
		Title:    details.Title,
		Branch:   details.HeadBranch,
		Body:     details.Body,
	})

	// A pull request without a ticket is still reviewable, so only reach for
	// Jira once a key is known.
	var issue *jira.Issue
	if ticketFound {
		fetched, err := fetchIssue(ctx, ticketKey)
		if err != nil {
			return err
		}
		issue = &fetched
	}

	// Read the repository's own guidance from the exact snapshot under review,
	// so rules the pull request itself changes are the ones that apply.
	rules, err := reporules.NewLoader(client).Load(ctx, pr.Owner, pr.Repo, details.HeadSHA)
	if err != nil {
		return fmt.Errorf("load repository rules: %w", err)
	}

	var evidenceReport evidence.Report
	if len(evidenceConnectors) > 0 {
		evidenceReport, err = evidence.NewCollector().Collect(ctx, evidenceConnectors)
		printEvidenceCollection(evidenceReport)
		if err != nil {
			return fmt.Errorf("collect external evidence: %w", err)
		}
	}

	reviewCtx := review.BuildContext(pr, details, files, issue, rules)
	reviewCtx = review.WithEvidence(reviewCtx, evidenceReport.Documents)

	printContext(reviewCtx, ticketFound, ticketErr, *format)

	// What the repository is built with is decided once, here, from its own manifests
	// at the reviewed commit. Everything downstream — which checks run, which language
	// semantics the review weighs — reads that answer rather than guessing again.
	profile, err := technology.NewDetector(client).Detect(ctx, pr.Owner, pr.Repo, details.HeadSHA, changedPaths(files))
	if err != nil {
		return fmt.Errorf("detect technology: %w", err)
	}

	printTechnology(profile)

	// Deterministic analysis needs a local checkout. Without one the review is still
	// valid, just with less evidence — and the cost is stated rather than left implicit.
	var analysisResult analysis.Result
	if *repoDir == "" {
		printAnalysisSkipped("local repository not provided")
		printQualityNote(profile)
	} else {
		printAnalysisProgressHeader()
		runner := newAnalysisProgressRunner(analysis.NewCommandRunner())
		analyzer := analysis.NewAnalyzerForProfile(runner, profile)
		analysisResult, err = analyzer.Analyze(ctx, *repoDir)
		if err != nil {
			return fmt.Errorf("deterministic analysis: %w", err)
		}
		printAnalysis(analysisResult)
	}

	// Reduce the search space before any later stage sees it.
	selected, err := contextselect.NewSelector().SelectWithTechnology(ctx, reviewCtx, analysisResult, profile)
	if err != nil {
		return fmt.Errorf("context selection: %w", err)
	}

	printSelection(selected)

	// Claude execution is opt-in, so a review never consumes Claude usage without
	// being asked.
	if !*useClaude {
		printClaudeSkipped("--claude not provided")
		return nil
	}

	return runClaudeReview(ctx, claudeStage{
		api:          client,
		pullRequest:  pr,
		reviewedSHA:  details.HeadSHA,
		changedFiles: files,
		selected:     selected,
		repoDir:      *repoDir,
		publish:      *doPublish,
		jiraKey:      ticketKeyString(ticketFound, ticketKey),
		capture: capture{
			path:    *capturePredictions,
			runName: *captureRun,
			caseID:  captureCaseID(*captureCase, pr),
		},
	})
}

// ticketKeyString renders the resolved ticket key, or "" when none was found. Only
// the key travels onward; the ticket body is review input, never output.
func ticketKeyString(found bool, key jira.TicketKey) string {
	if !found {
		return ""
	}
	return key.String()
}

// claudeStage is everything the review-and-publish stage needs. It is a struct
// rather than a long parameter list so that adding a stage cannot silently reorder
// arguments.
type claudeStage struct {
	// api is the GitHub surface publication may use: two reads and one write, and
	// nothing else. Holding the interface rather than the client is what lets the
	// no-write guarantee be tested here.
	api          publish.API
	pullRequest  github.PullRequest
	reviewedSHA  string
	changedFiles []github.ChangedFile
	selected     contextselect.SelectedContext
	repoDir      string
	publish      bool
	jiraKey      string
	capture      capture
}

// capture is where a run's validated findings are recorded for later scoring.
// An empty path disables it, which is the default: measurement is opt-in.
type capture struct {
	path    string
	runName string
	caseID  string
}

// captureCaseID defaults the evaluation case to the pull request it reviewed, so
// a suite's case IDs are stable and mean something without a lookup table.
func captureCaseID(explicit string, pr github.PullRequest) string {
	if trimmed := strings.TrimSpace(explicit); trimmed != "" {
		return trimmed
	}
	return fmt.Sprintf("%s/%s#%d", pr.Owner, pr.Repo, pr.Number)
}

// runClaudeReview hands the selected context to the Claude adapter, then decides
// what may be done with the result.
//
// The CLI never executes Claude itself, never parses Claude's output, and never
// decides which findings matter or whether they may be published: it prints what the
// domain model validated and what the publication policy planned.
func runClaudeReview(ctx context.Context, stage claudeStage) error {
	reviewer := claude.NewReviewer(claude.NewClient())
	if err := reviewer.Available(ctx); err != nil {
		return err
	}

	outcome, err := reviewer.Review(ctx, stage.selected, stage.repoDir)
	if err != nil {
		return fmt.Errorf("Claude review: %w", err)
	}

	printClaudeOutcome(outcome)

	// Capture the validated proposal before verification and policy narrow it. What
	// this snapshot measures is the reviewer's precision and recall; what policy did
	// with the result is a separate, already-printed question.
	if stage.capture.path != "" {
		if err := captureRun(stage, outcome.Result); err != nil {
			return err
		}
	}

	// Adversarial verification sits between the reviewer and publication. Every finding
	// that could reach a line is attacked before it may reach one; a finding that could
	// not — low severity, summary-only — is not worth another invocation.
	report, err := claude.NewVerifier(claude.NewClient()).
		VerifyAll(ctx, outcome.Result, stage.selected, stage.repoDir)
	if err != nil {
		return fmt.Errorf("verification: %w", err)
	}

	printVerification(report)

	// One deterministic policy decides every disposition, from the reviewer's evidence
	// strength, the verifier's verdict and evidence strength, the severity, the category, and
	// whether GitHub has a line to attach the comment to. Neither model has a vote.
	mapper := publish.NewMapperFromChangedFiles(stage.changedFiles)
	plan := publish.NewPolicy().BuildPlan(
		publish.CandidatesFrom(report),
		mapper,
		stage.reviewedSHA,
		outcome.Result.Summary,
	)

	printPolicyDecisions(plan)
	printPlan(plan)

	if !stage.publish {
		printPublicationSkipped("--publish not provided")
		return nil
	}

	return runPublication(ctx, stage, plan)
}

// captureRun writes this run's validated findings as a prediction snapshot.
//
// A capture failure fails the review rather than being reported and stepped over: a
// measurement run whose snapshot silently went missing is worse than one that stopped,
// because the absence looks like a case that found nothing.
func captureRun(stage claudeStage, result findings.ReviewResult) error {
	set, err := evaluation.Snapshot(evaluation.CaptureRequest{
		RunName: stage.capture.runName,
		CaseID:  stage.capture.caseID,
	}, result)
	if err != nil {
		return err
	}
	if err := evaluation.WriteSnapshot(stage.capture.path, set); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("Prediction Capture")
	fmt.Println()
	fmt.Printf("Run:      %s\n", set.RunName)
	fmt.Printf("Case:     %s\n", set.Cases[0].ID)
	fmt.Printf("Findings: %d\n", len(set.Cases[0].Findings))
	fmt.Printf("Written:  %s\n", stage.capture.path)
	return nil
}

// printVerification renders the verdict on each candidate finding, then the tally.
//
// Both sides are shown — what the reviewer claimed and what the verifier concluded —
// because the disagreement is the useful part, and a suppressed finding a developer never
// sees is indistinguishable from one that was never found.
func printVerification(report verification.Report) {
	fmt.Println()
	fmt.Println("Verification")

	if report.Empty() {
		fmt.Println()
		fmt.Println("  no findings to verify")
		return
	}

	for _, item := range report.Outcomes {
		fmt.Println()
		fmt.Printf("%s\n", item.Finding.ID)
		fmt.Printf("  Severity:           %s\n", item.Finding.Severity.Display())
		fmt.Printf("  Reviewer evidence:  %s\n", item.Finding.EvidenceStrength().Display())

		switch item.Status {
		case verification.StatusVerified:
			fmt.Printf("  Verdict:            %s\n", item.Result.Verdict.Display())
			fmt.Printf("  Verifier evidence:  %s\n", item.Result.EvidenceStrength().Display())
			printWrapped("  Reason:    ", item.Result.Reason)
		case verification.StatusNotRequired:
			fmt.Printf("  Verdict:   SKIPPED\n")
			printWrapped("  Reason:    ", item.SkipReason)
		case verification.StatusFailed:
			fmt.Printf("  Verdict:   FAILED\n")
			printWrapped("  Reason:    ", item.FailureReason)
		}
	}

	stats := report.Stats
	fmt.Println()
	fmt.Println("Verification summary:")
	fmt.Printf("  Valid:     %d\n", stats.Valid)
	fmt.Printf("  Invalid:   %d\n", stats.Invalid)
	fmt.Printf("  Uncertain: %d\n", stats.Uncertain)
	fmt.Printf("  Skipped:   %d\n", stats.Skipped)
	if stats.Failed > 0 {
		fmt.Printf("  Failed:    %d\n", stats.Failed)
	}
	fmt.Printf("  Context:   %s\n", formatBytes(stats.ContextBytes))
}

// printWrapped prints text under a label, wrapping continuation lines under the label's
// width so a long reason stays readable in a terminal.
func printWrapped(label string, text string) {
	const width = 66

	words := strings.Fields(text)
	if len(words) == 0 {
		return
	}

	indent := strings.Repeat(" ", len(label))
	line := label

	for i, word := range words {
		candidate := word
		if i > 0 && len(line)+1+len(candidate) > width+len(label) {
			fmt.Println(line)
			line = indent + candidate
			continue
		}
		if i > 0 {
			line += " "
		}
		line += candidate
	}
	fmt.Println(line)
}

// runPublication performs the single permitted write.
func runPublication(ctx context.Context, stage claudeStage, plan publish.Plan) error {
	if plan.NothingToPublish() {
		printNoFindingsPublication()
		return nil
	}

	result, err := publish.NewPublisher(stage.api).Publish(ctx, publish.Request{
		PullRequest:     stage.pullRequest,
		Plan:            plan,
		ReviewedHeadSHA: stage.reviewedSHA,
		JiraKey:         stage.jiraKey,
		Checks:          stage.selected.Analysis,
	})

	var stale *publish.StaleHeadError
	if errors.As(err, &stale) {
		printStalePublication(stale)
		return err
	}
	if err != nil {
		return fmt.Errorf("publish review: %w", err)
	}

	printPublication(result)
	return nil
}

// printPolicyDecisions explains what policy decided about every finding, and why.
//
// It prints the inputs beside the outcome deliberately: a developer who disagrees with a
// suppression should be able to see which gate closed, rather than having to guess whether
// the tool found nothing or withheld something.
func printPolicyDecisions(plan publish.Plan) {
	if len(plan.Decisions) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("Publication Policy")

	for _, decision := range plan.Decisions {
		fmt.Println()
		fmt.Printf("%s\n", decision.Finding.ID)
		fmt.Print(decision.Explain())
	}
}

// printPlan renders the publication plan: the counts, then where each finding landed.
func printPlan(plan publish.Plan) {
	stats := plan.Stats

	fmt.Println()
	fmt.Println("Publication Plan")
	fmt.Println()
	fmt.Printf("Inline:      %d\n", stats.Inline)
	fmt.Printf("Summary:     %d\n", stats.Summary)
	fmt.Printf("Suppressed:  %d\n", stats.Suppressed)

	printPlannedInline(plan.Inline)
	printPlannedSummary(plan)
	printSuppressed(plan.Suppressed)
}

// printPlannedInline lists each inline comment and the diff location it maps to.
func printPlannedInline(inline []publish.InlineFinding) {
	if len(inline) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("Inline comments:")
	fmt.Println()
	for _, item := range inline {
		fmt.Printf("  %-7s %-10s %s\n",
			item.Finding.Severity.Display(), item.Finding.ID, item.Location.Describe())
	}
}

// printPlannedSummary lists the findings destined for the review body, with the reason each
// one is not inline.
func printPlannedSummary(plan publish.Plan) {
	if len(plan.Summary) == 0 {
		return
	}

	reasons := map[string]string{}
	for _, decision := range plan.Decisions {
		if decision.Disposition == publish.DispositionSummary {
			reasons[decision.Finding.ID] = decision.Reasons.Primary().String()
		}
	}

	fmt.Println()
	fmt.Println("Summary findings:")
	fmt.Println()
	for _, finding := range plan.Summary {
		fmt.Printf("  %-7s %-10s %s\n",
			finding.Severity.Display(), finding.ID, finding.Location())
		if why := reasons[finding.ID]; why != "" {
			fmt.Printf("          %s\n", why)
		}
	}
}

// printSuppressed lists what policy withheld from GitHub. It is printed in full locally:
// a finding nobody can see is indistinguishable from one that was never found.
func printSuppressed(suppressed []publish.SuppressedFinding) {
	if len(suppressed) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("Suppressed:")
	fmt.Println()
	for _, item := range suppressed {
		fmt.Printf("  %-7s %-10s %s\n",
			item.Finding.Severity.Display(), item.Finding.ID, item.Finding.Location())
		fmt.Printf("          %s\n", item.Reason)
	}
}

// printPublicationSkipped states that no GitHub write was attempted.
func printPublicationSkipped(reason string) {
	fmt.Println()
	fmt.Println("GitHub publication:")
	fmt.Printf("SKIPPED (%s)\n", reason)
}

// printNoFindingsPublication reports a clean review. Zero findings is a successful
// review, not a reason to post an empty one.
func printNoFindingsPublication() {
	fmt.Println()
	fmt.Println("No actionable findings.")
	fmt.Println("GitHub publication skipped.")
}

// printPublication reports the result of the write.
func printPublication(result publish.Outcome) {
	fmt.Println()
	fmt.Println("GitHub Publication")
	fmt.Println()
	fmt.Printf("Reviewed head: %s\n", result.ReviewedHeadSHA)
	fmt.Printf("Current head:  %s\n", result.CurrentHeadSHA)
	fmt.Println()

	switch result.Status {
	case publish.StatusPublished:
		fmt.Println("Published successfully.")
		if result.Review.HTMLURL != "" {
			fmt.Printf("  %s\n", result.Review.HTMLURL)
		}
	case publish.StatusSkippedDuplicate:
		fmt.Printf("ARC review already published for head %s\n", result.ReviewedHeadSHA)
		if result.ExistingReview.HTMLURL != "" {
			fmt.Printf("  %s\n", result.ExistingReview.HTMLURL)
		}
	case publish.StatusSkippedNoFindings:
		fmt.Println("No actionable findings.")
		fmt.Println("GitHub publication skipped.")
	default:
		fmt.Printf("%s\n", result.Status)
	}
}

// printStalePublication reports that the pull request moved. The new commit is not
// reviewed and nothing is retried against it: the findings describe code that is no
// longer there.
func printStalePublication(stale *publish.StaleHeadError) {
	fmt.Println()
	fmt.Println("GitHub Publication")
	fmt.Println()
	fmt.Println("ABORTED")
	fmt.Println()
	fmt.Printf("Reviewed head: %s\n", stale.ReviewedHeadSHA)
	fmt.Printf("Current head:  %s\n", stale.CurrentHeadSHA)
	fmt.Println()
	fmt.Println("PR changed while ARC was reviewing it.")
	fmt.Println()
	fmt.Println("Rerun the review before publishing.")
}

// printClaudeSkipped states that no Claude review was run.
func printClaudeSkipped(reason string) {
	fmt.Println()
	fmt.Println("Claude Review")
	fmt.Println()
	fmt.Printf("Skipped: %s\n", reason)
}

// printClaudeOutcome renders the invocation detail and then the findings report.
// The raw transport output is kept in the outcome for debugging but never printed.
func printClaudeOutcome(outcome claude.Outcome) {
	fmt.Println()
	fmt.Println("Claude Review")
	fmt.Println()
	fmt.Println("Status: completed")
	fmt.Printf("Duration: %s\n", outcome.Transport.Duration.Round(100*time.Millisecond))
	fmt.Printf("Findings: %d\n", outcome.Result.Count())

	if outcome.Transport.Truncated {
		fmt.Println("Output truncated: yes")
	}

	fmt.Println()
	fmt.Print(findings.Render(outcome.Result))
}

// printSelection renders a summary of what the selector kept. The selected
// content itself is input for later stages, not terminal output.
func printSelection(selected contextselect.SelectedContext) {
	fmt.Println()
	fmt.Println("Context Selection")

	printProfile(selected.Profile)

	stats := selected.Stats
	fmt.Println()
	fmt.Printf("Candidate files: %d\n", stats.CandidateFiles)
	fmt.Printf("Selected files:  %d\n", stats.SelectedFiles)
	fmt.Printf("Dropped files:   %d\n", stats.DroppedFiles)
	if stats.CandidateEvidence > 0 {
		fmt.Printf("Evidence docs:   %d of %d selected", stats.SelectedEvidence, stats.CandidateEvidence)
		if stats.DroppedEvidence > 0 {
			fmt.Printf(" (%d dropped)", stats.DroppedEvidence)
		}
		fmt.Println()
	}

	fmt.Println()
	fmt.Println("Context size:")
	fmt.Printf("  Original: %s\n", formatBytes(stats.OriginalBytes))
	fmt.Printf("  Selected: %s\n", formatBytes(stats.SelectedBytes))
	fmt.Printf("  Budget:   %s\n", formatBytes(stats.BudgetBytes))

	if stats.Truncated {
		fmt.Println()
		fmt.Println("Context truncated: yes")
	}

	printSelectedFiles(selected.Files)
	printDroppedFiles(stats.Dropped)
}

// printProfile lists the detected language and technologies as carried into the
// selection.
func printProfile(profile contextselect.TechnologyProfile) {
	fmt.Println()
	fmt.Println("Language:")
	if len(profile.Languages) == 0 {
		fmt.Println("  not detected")
	}
	for _, language := range profile.Languages {
		fmt.Printf("  %s\n", language)
	}

	technologies := profile.Technologies()
	if len(technologies) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("Technologies:")
	for _, name := range technologies {
		fmt.Printf("  %s\n", name)
	}
}

// changedPaths lists the paths a pull request touched, for detection.
func changedPaths(files []github.ChangedFile) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Filename)
	}
	return paths
}

// printTechnology renders what the repository is built with, and which checks that
// implies. Stating the toolchain before running it makes a wrong detection obvious
// rather than mysterious.
func printTechnology(profile technology.Profile) {
	fmt.Println()
	fmt.Println("Technology")
	fmt.Println()

	if profile.Empty() {
		fmt.Println("  no language or build system detected")
		return
	}

	fmt.Println("Languages:")
	if len(profile.Languages) == 0 {
		fmt.Println("  not detected")
	}
	for _, label := range profile.LanguageLabels() {
		fmt.Printf("  %s\n", label)
	}

	fmt.Println()
	fmt.Println("Build systems:")
	if len(profile.BuildSystems) == 0 {
		fmt.Println("  not detected")
	}
	for _, label := range profile.BuildSystemLabels() {
		fmt.Printf("  %s\n", label)
	}

	if len(profile.Frameworks) > 0 {
		fmt.Println()
		fmt.Println("Frameworks:")
		for _, name := range profile.Frameworks {
			fmt.Printf("  %s\n", name)
		}
	}
	if len(profile.Libraries) > 0 {
		fmt.Println()
		fmt.Println("Libraries:")
		for _, name := range profile.Libraries {
			fmt.Printf("  %s\n", name)
		}
	}
}

// printQualityNote states what the review is missing when no checks could run.
//
// A review without build or test evidence is still useful, but it is a weaker review,
// and that should be visible to whoever reads it rather than inferred from an absence.
func printQualityNote(profile technology.Profile) {
	checks := analysis.ChecksForProfile(profile)

	fmt.Println()
	fmt.Println("Review quality note:")

	if len(checks) == 0 {
		fmt.Println("language-specific build/test checks were not executed.")
		return
	}

	commands := make([]string, 0, len(checks))
	for _, check := range checks {
		commands = append(commands, check.DisplayCommand())
	}

	languages := profile.LanguageLabels()
	if len(languages) == 0 {
		fmt.Printf("%s evidence is unavailable.\n", strings.Join(commands, ", "))
		return
	}

	fmt.Printf("%s-specific reasoning is enabled, but %s evidence is unavailable.\n",
		strings.Join(languages, "/"), strings.Join(commands, " and "))
}

// printSelectedFiles lists each kept file with its importance and reason.
func printSelectedFiles(files []contextselect.SelectedFile) {
	if len(files) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("Selected:")
	fmt.Println()

	for _, f := range files {
		fmt.Printf("  %-5s %s", f.Importance.Display(), f.Path)
		if f.Truncated {
			fmt.Print("  (truncated)")
		}
		fmt.Println()
		fmt.Printf("        %s\n", f.Reason)
	}
}

// printDroppedFiles lists what did not fit, so a surprising omission is visible.
func printDroppedFiles(dropped []contextselect.DroppedFile) {
	if len(dropped) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("Dropped:")
	fmt.Println()

	for _, d := range dropped {
		fmt.Printf("  %-5s %s\n", d.Importance.Display(), d.Path)
		fmt.Printf("        %s\n", d.Reason)
	}
}

// formatBytes renders a byte count in the largest sensible unit.
func formatBytes(n int) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%d KB", n/1024)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// printAnalysisSkipped reports that no checks were run at all.
func printAnalysisSkipped(reason string) {
	fmt.Println()
	fmt.Println("Deterministic Analysis")
	fmt.Println()
	fmt.Printf("  skipped: %s\n", reason)
}

// printEvidenceCollection makes every connector outcome visible without
// printing document content or credentials.
func printEvidenceCollection(report evidence.Report) {
	fmt.Println()
	fmt.Println("External Evidence")
	fmt.Println()
	for _, outcome := range report.Outcomes {
		fmt.Printf("%-9s %-20s %-16s", strings.ToUpper(string(outcome.Status)), outcome.ID, outcome.Kind)
		if outcome.Status == evidence.StatusCollected {
			fmt.Printf(" %s", formatBytes(outcome.Bytes))
		} else if outcome.Error != "" {
			fmt.Printf(" %s", outcome.Error)
		}
		if outcome.Required {
			fmt.Print("  [required]")
		}
		fmt.Println()
	}
}

// printAnalysis renders one line per check plus a bounded snippet for failures.
// Full command output is evidence for later stages, not terminal noise.
func printAnalysis(result analysis.Result) {
	fmt.Println()
	fmt.Println("Deterministic Analysis")
	fmt.Println()

	if result.Empty() {
		fmt.Println("  no checks ran")
		return
	}

	for _, check := range result.Checks {
		fmt.Printf("%-8s %-18s %s\n", checkStatus(check), check.Command, checkDetail(check))
	}

	for _, check := range result.FailedChecks() {
		if snippet := failureSnippet(check); len(snippet) > 0 {
			fmt.Println()
			fmt.Printf("  %s output:\n", check.Name)
			for _, line := range snippet {
				fmt.Printf("    %s\n", line)
			}
		}
	}
}

// checkStatus is the leading status word for a check.
func checkStatus(check analysis.CheckResult) string {
	switch {
	case check.Skipped:
		return "SKIP"
	case check.TimedOut:
		return "TIMEOUT"
	case check.Passed:
		return "PASS"
	default:
		return "FAIL"
	}
}

// checkDetail is the trailing column: a skip reason, or how long the check took.
func checkDetail(check analysis.CheckResult) string {
	if check.Skipped {
		return check.SkipReason
	}
	return check.Duration.Round(100 * time.Millisecond).String()
}

// Bounds on how much failure output reaches the terminal. Lines alone are not
// enough: a single very long line would flood just as badly.
const (
	maxSnippetLines     = 10
	maxSnippetLineBytes = 200
)

// failureSnippet retains both ends of a failed check's output, preferring
// stdout. Build tools usually put setup at the beginning and their failure
// summary at the end.
func failureSnippet(check analysis.CheckResult) []string {
	output := strings.TrimSpace(check.Stdout)
	if output == "" {
		output = strings.TrimSpace(check.Stderr)
	}
	if output == "" {
		return nil
	}

	lines := strings.Split(output, "\n")

	omitted := 0
	if len(lines) > maxSnippetLines {
		omitted = len(lines) - maxSnippetLines
		headLines := (maxSnippetLines + 1) / 2
		tailLines := maxSnippetLines - headLines
		kept := append([]string{}, lines[:headLines]...)
		kept = append(kept, fmt.Sprintf("... %d lines omitted", omitted))
		lines = append(kept, lines[len(lines)-tailLines:]...)
	}

	for i, line := range lines {
		if len(line) > maxSnippetLineBytes {
			lines[i] = line[:maxSnippetLineBytes] + "…"
		}
	}

	return lines
}

// fetchIssue loads the Jira issue for key. Missing Jira configuration is a clear
// error at this point: a ticket was detected, so the user meant to use Jira.
func fetchIssue(ctx context.Context, key jira.TicketKey) (jira.Issue, error) {
	client, err := jira.NewClientFromEnv()
	if err != nil {
		return jira.Issue{}, fmt.Errorf("ticket %s detected but Jira is not configured: %w", key, err)
	}

	issue, err := client.GetIssue(ctx, key)
	if err != nil {
		return jira.Issue{}, fmt.Errorf("fetch Jira issue %s: %w", key, err)
	}
	return issue, nil
}

// printContext renders a compact summary of the review context. Patches and the
// full Jira description are review input for later stages, not output.
func printContext(c review.Context, ticketFound bool, ticketErr error, format string) {
	fmt.Println("Review Context")
	fmt.Println()

	printPullRequestSection(c.PullRequest)
	printChangesSection(c.Changes)
	printJiraSection(c, ticketFound, ticketErr)
	printRulesSection(c.Rules)

	fmt.Println()
	fmt.Printf("Format:       %s\n", format)
}

func printPullRequestSection(pr review.PullRequestContext) {
	fmt.Println("Pull Request")
	fmt.Printf("  Repository: %s\n", pr.Slug())
	fmt.Printf("  Number:     %d\n", pr.Number)
	fmt.Printf("  URL:        %s\n", pr.URL)
	fmt.Printf("  Title:      %s\n", pr.Title)
	fmt.Printf("  Author:     %s\n", pr.Author)
	fmt.Printf("  State:      %s\n", pr.State)
	fmt.Printf("  Draft:      %t\n", pr.Draft)
	fmt.Printf("  Base:       %s\n", pr.BaseBranch)
	fmt.Printf("  Head:       %s\n", pr.HeadBranch)
	fmt.Printf("  SHA:        %s\n", pr.HeadSHA)
}

func printChangesSection(changes review.ChangeContext) {
	fmt.Println()
	fmt.Println("Changes")
	fmt.Printf("  Files:      %d\n", changes.FileCount)
	fmt.Printf("  Additions:  %d\n", changes.Additions)
	fmt.Printf("  Deletions:  %d\n", changes.Deletions)
	fmt.Printf("  Changes:    %d\n", changes.Changes)

	if len(changes.Files) == 0 {
		return
	}

	fmt.Println()
	for _, f := range changes.Files {
		fmt.Printf("  %s\n", f.Filename)
		fmt.Printf("    %s  +%d -%d", f.Status, f.Additions, f.Deletions)
		if !f.HasPatch() {
			// Binary or oversized files arrive without a diff; say so rather
			// than looking like an empty change.
			fmt.Print("  (no patch available)")
		}
		fmt.Println()
	}
}

// printJiraSection reports the ticket. A missing or ambiguous ticket is stated,
// never fatal.
func printJiraSection(c review.Context, ticketFound bool, ticketErr error) {
	fmt.Println()
	fmt.Println("Jira")

	if c.HasTicket() {
		t := c.Ticket
		fmt.Printf("  Key:        %s\n", t.Key)
		fmt.Printf("  Summary:    %s\n", t.Summary)
		fmt.Printf("  Status:     %s\n", t.Status)
		fmt.Printf("  Type:       %s\n", t.IssueType)
		fmt.Printf("  Priority:   %s\n", t.Priority)
		if t.ParentKey != "" {
			fmt.Printf("  Parent:     %s\n", t.ParentKey)
		}
		if len(t.Labels) > 0 {
			fmt.Printf("  Labels:     %s\n", strings.Join(t.Labels, ", "))
		}
		return
	}

	var ambiguous *jira.AmbiguousTicketError
	switch {
	case errors.As(ticketErr, &ambiguous):
		fmt.Printf("  Ticket:     ambiguous (%s)\n", strings.Join(keyStrings(ambiguous.Keys), ", "))
		fmt.Printf("              %s names several tickets; pass --ticket to choose one\n", ambiguous.Source)
	case ticketErr != nil:
		fmt.Printf("  Ticket:     not detected (%v)\n", ticketErr)
	case ticketFound:
		// Resolution succeeded but no issue was attached; should not happen.
		fmt.Println("  Ticket:     detected but not fetched")
	default:
		fmt.Println("  Ticket:     not detected")
	}
}

// printRulesSection lists which rule documents were loaded. Their content is
// review input for later stages, not output.
func printRulesSection(rules review.RuleContext) {
	fmt.Println()
	fmt.Println("Repository Rules")
	fmt.Println()
	fmt.Printf("Loaded: %d\n", rules.Count())

	if rules.Count() == 0 {
		return
	}

	fmt.Println()
	for _, d := range rules.Documents {
		fmt.Printf("  %s", d.Path)
		if d.Truncated {
			fmt.Print("  (truncated)")
		}
		fmt.Println()
	}
}

// keyStrings renders ticket keys for display.
func keyStrings(keys []jira.TicketKey) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.String())
	}
	return out
}

func printUsage() {
	fmt.Print(`Agentic Code Review

Usage:

  arc review --pr <github-pr-url> [--evidence-config FILE] [--claude] [--publish]
  arc evaluate --labels <labels.json> --predictions <file-or-dir>

Options:

  --pr         GitHub pull request URL
  --ticket     Jira ticket key (optional)
  --format     markdown | json
  --repo-dir   local checkout to run deterministic checks against (optional)
  --evidence-config versioned connector configuration for external review evidence (optional)
  --capture-predictions  write this run's validated findings to a new snapshot (optional)
  --capture-run          run name recorded in the snapshot (required with capture)
  --capture-case         evaluation case ID (default: owner/repo#number)
  --claude     run the review through the local Claude Code CLI (optional)
  --publish    publish the findings as a GitHub PR review (requires --claude)

Evaluation:

  --labels       human-labelled ground truth JSON
  --predictions  captured reviewer predictions: one snapshot, or a directory of them
  --format       markdown | json (default: markdown)

Example:

  arc review \
    --pr https://github.com/acme/payments/pull/123 \
    --ticket PAY-431

  arc review \
    --pr https://github.com/acme/payments/pull/123 \
    --repo-dir . \
    --evidence-config examples/evidence-config.json \
    --claude \
    --publish

  arc evaluate \
    --labels evaluations/seed-labels.json \
    --predictions evaluations/seed-predictions.json
`)
}
