package benchmark

import (
	"context"
	"fmt"

	"github.com/your-company/agentic-code-review/internal/analysis"
	"github.com/your-company/agentic-code-review/internal/changerisk"
	"github.com/your-company/agentic-code-review/internal/claude"
	"github.com/your-company/agentic-code-review/internal/contextselect"
	"github.com/your-company/agentic-code-review/internal/findings"
	"github.com/your-company/agentic-code-review/internal/publish"
	"github.com/your-company/agentic-code-review/internal/retrieval"
	"github.com/your-company/agentic-code-review/internal/review"
	"github.com/your-company/agentic-code-review/internal/specialist"
	"github.com/your-company/agentic-code-review/internal/technology"
)

// Runner reviews mutants through the real pipeline.
//
// It deliberately uses the same reviewer, verifier, and publication policy the CLI
// uses, and skips only what needs a network: there is no GitHub, no Jira, and no
// publication. A benchmark that measured a simplified pipeline would measure
// something nobody runs.
type Runner struct {
	// RepoRoot is the checkout mutants are seeded into and retrieved from.
	RepoRoot string

	// Retrieve enables symbol retrieval, as `--retrieve` does.
	Retrieve bool

	// Verify enables the adversarial verifier and the publication policy. With it
	// off, the report measures what the reviewer proposed and nothing more.
	Verify bool
}

// Run reviews one mutant and scores the outcome.
func (r Runner) Run(ctx context.Context, mutant Mutant) Outcome {
	outcome := Outcome{Mutant: mutant}

	// The defect must exist on disk, not only in the diff: verification and
	// retrieval both read the checkout, and a verifier that reads unmutated code
	// will refute every true finding for the right reason.
	change, workdir, cleanup, err := mutant.ApplyToCopy(r.RepoRoot)
	if err != nil {
		outcome.Error = err.Error()
		return outcome
	}
	defer cleanup()

	selected, err := r.selectContext(ctx, mutant, change, workdir)
	if err != nil {
		outcome.Error = err.Error()
		return outcome
	}

	reviewer := claude.NewReviewer(claude.NewClient())
	if err := reviewer.Available(ctx); err != nil {
		outcome.Error = "claude unavailable: " + err.Error()
		return outcome
	}

	result, err := reviewer.Review(ctx, selected, workdir)
	if err != nil {
		outcome.Error = "review: " + err.Error()
		return outcome
	}
	outcome.Findings = result.Result.Findings

	for _, finding := range outcome.Findings {
		if mutant.Identifies(finding) {
			outcome.DetectedPreVerification = true
			break
		}
	}

	if !r.Verify {
		outcome.Published = outcome.Findings
		r.score(&outcome)
		return outcome
	}

	report, err := claude.NewVerifier(claude.NewClient()).
		VerifyAll(ctx, result.Result, selected, workdir)
	if err != nil {
		outcome.Error = "verification: " + err.Error()
		return outcome
	}

	// The same policy the CLI applies, minus the diff mapper: a benchmark has no
	// GitHub diff to attach comments to, and mappability is not what is being
	// measured. Everything else — evidence requirements, strength gates, verdict
	// gating, category placement, quotas — applies unchanged.
	plan := publish.NewPolicy().BuildPlan(
		publish.CandidatesFrom(report),
		publish.NewMapperFromChangedFiles(nil),
		"benchmark",
		result.Result.Summary,
	)

	outcome.Published = plan.Summary
	for _, inline := range plan.Inline {
		outcome.Published = append(outcome.Published, inline.Finding)
	}

	// Why a true positive was withheld is the most useful line in the report.
	for _, suppressed := range plan.Suppressed {
		if mutant.Identifies(suppressed.Finding) {
			outcome.Suppressed = append(outcome.Suppressed,
				suppressed.Finding.Title+" — "+suppressed.Reason)
		}
	}

	r.score(&outcome)
	return outcome
}

func (r Runner) score(outcome *Outcome) {
	for _, finding := range outcome.Published {
		if outcome.Mutant.Identifies(finding) {
			outcome.Detected = true
			continue
		}
		outcome.FalsePositives++
	}
}

// selectContext builds the bounded review context for one mutant, through the same
// deterministic stages the CLI runs.
func (r Runner) selectContext(
	ctx context.Context,
	mutant Mutant,
	change Change,
	workdir string,
) (contextselect.SelectedContext, error) {
	reviewCtx := review.Context{
		PullRequest: review.PullRequestContext{
			Owner:      "benchmark",
			Repository: "agentic-code-review",
			Number:     1,
			Title:      "Benchmark change",
			Author:     "benchmark",
			BaseBranch: "main",
			HeadBranch: "benchmark",
			HeadSHA:    "benchmark",
			// No description: the benchmark must not hint at what changed, or it
			// would be measuring reading comprehension rather than review.
		},
		Changes: review.ChangeContext{Files: []review.FileChange{{
			Filename:  change.Path,
			Status:    "modified",
			Patch:     change.Patch,
			Additions: change.Additions,
			Deletions: change.Deletions,
		}}},
	}

	profile := technology.Profile{
		Languages:    []technology.Language{technology.LanguageGo},
		BuildSystems: []technology.BuildSystem{technology.BuildSystemGo},
	}

	riskProfile := changerisk.NewAnalyzer().Analyze([]changerisk.Change{{
		Path: change.Path, Status: "modified", Patch: change.Patch,
		Additions: change.Additions, Deletions: change.Deletions,
	}})
	routing := specialist.NewRouter().Route(riskProfile)

	retrieved := retrieval.Result{Skipped: true, Reason: "retrieval not requested"}
	if r.Retrieve {
		result, err := retrieval.NewRetriever().Retrieve(ctx, workdir,
			[]retrieval.Change{{Path: change.Path, Patch: change.Patch}})
		if err != nil {
			return contextselect.SelectedContext{}, fmt.Errorf("retrieval: %w", err)
		}
		retrieved = result
	}

	budget := contextselect.BudgetForRiskLevel(string(riskProfile.Level))
	selected, err := contextselect.NewSelectorWithBudget(budget).
		SelectWithRetrieval(ctx, reviewCtx, analysis.Result{}, profile, retrieved)
	if err != nil {
		return contextselect.SelectedContext{}, fmt.Errorf("context selection: %w", err)
	}

	selected.Focus = focusFrom(riskProfile, routing)
	return selected, nil
}

// focusFrom mirrors the CLI's flattening of risk and routing into review focus.
func focusFrom(profile changerisk.Profile, plan specialist.Plan) contextselect.ReviewFocus {
	focus := contextselect.ReviewFocus{
		RiskLevel:   string(profile.Level),
		RiskReasons: profile.Reasons(),
	}
	for _, area := range profile.Areas {
		focus.RiskAreas = append(focus.RiskAreas, string(area))
	}
	for _, selection := range plan.Selected {
		focus.Specialists = append(focus.Specialists, contextselect.FocusSpecialist{
			ID:      string(selection.Specialist.ID),
			Title:   selection.Specialist.Title,
			Purpose: selection.Specialist.Purpose,
			Focus:   selection.Specialist.Focus,
			Reasons: selection.Reasons,
		})
	}
	return focus
}

// RunAll reviews every mutant in order and returns the report.
//
// Sequential on purpose: the point is a measurement, not a load test, and one
// review at a time keeps the model's own behaviour comparable between cases.
func (r Runner) RunAll(ctx context.Context, mutants []Mutant, progress func(Outcome)) Report {
	var report Report
	for _, mutant := range mutants {
		if err := ctx.Err(); err != nil {
			break
		}
		outcome := r.Run(ctx, mutant)
		report.Outcomes = append(report.Outcomes, outcome)
		if progress != nil {
			progress(outcome)
		}
	}
	return report
}

// FindingsByCategory counts published findings by category across a report, which is
// how a specialist's contribution shows up.
func (r Report) FindingsByCategory() map[findings.Category]int {
	counts := map[findings.Category]int{}
	for _, outcome := range r.Outcomes {
		for _, finding := range outcome.Published {
			counts[finding.Category]++
		}
	}
	return counts
}
