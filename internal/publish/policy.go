package publish

import (
	"fmt"
	"sort"

	"github.com/your-company/agentic-code-review/internal/findings"
	"github.com/your-company/agentic-code-review/internal/verification"
)

// Disposition is what becomes of one finding.
//
// Exactly one applies to every finding the reviewer proposed. There is no fourth state and
// no "maybe": a finding is attached to a line, reported in the body, or withheld, and the
// reasons say which and why.
type Disposition string

const (
	// DispositionInline attaches the finding to a line of the diff.
	DispositionInline Disposition = "inline"

	// DispositionSummary reports the finding in the review body.
	DispositionSummary Disposition = "summary"

	// DispositionSuppress withholds the finding from GitHub entirely. It is still reported
	// locally, with its reasons.
	DispositionSuppress Disposition = "suppress"
)

// Display renders the disposition for terminal output.
func (d Disposition) Display() string {
	switch d {
	case DispositionInline:
		return "INLINE"
	case DispositionSummary:
		return "SUMMARY"
	case DispositionSuppress:
		return "SUPPRESS"
	default:
		return "?"
	}
}

// Decision is the complete record of what policy concluded about one finding.
//
// The finding is the reviewer's original, unmodified. Verification is the verdict when one
// exists, and nil when policy did not call for one. Nothing here is a model's judgement:
// every field is derived from the inputs by the rules below.
type Decision struct {
	Finding      findings.Finding
	Verification *verification.Result

	Disposition Disposition
	Reasons     Reasons

	// Location is the mapped diff position, set only for an inline decision.
	Location DiffLocation

	// Mappable reports whether a valid diff location existed, whatever the disposition.
	Mappable bool

	// VerificationStatus is what happened at the verification stage.
	VerificationStatus verification.Status
}

// Inline reports whether the finding will become a line comment.
func (d Decision) Inline() bool { return d.Disposition == DispositionInline }

// Published reports whether the finding reaches GitHub in any form.
func (d Decision) Published() bool { return d.Disposition != DispositionSuppress }

// VerifierEvidenceStrength returns the verdict's evidence band, or LOW when unverified.
// Verification status is always checked separately, so the fallback cannot admit a result.
func (d Decision) VerifierEvidenceStrength() findings.EvidenceStrength {
	if d.Verification == nil {
		return findings.EvidenceStrengthLow
	}
	return d.Verification.EvidenceStrength()
}

// Stats is the tally of one policy run.
type Stats struct {
	Input      int
	Inline     int
	Summary    int
	Suppressed int

	SuppressedInvalidVerifier     int
	SuppressedUncertainVerifier   int
	SuppressedVerificationFailed  int
	SuppressedLowEvidenceStrength int
	SuppressedEvidence            int
	LimitedByInlineCap            int
	LimitedBySummaryCap           int
	LimitedByTotalCap             int
}

// categoryPriority orders categories for tie-breaking among otherwise comparable findings.
//
// It affects ordering only, never severity: a medium security finding is still medium, and
// this cannot promote it past a high correctness one. It decides which of two equally
// serious, equally well-evidenced findings gets the last inline slot.
func categoryPriority(category findings.Category) int {
	switch category {
	case findings.CategorySecurity:
		return 1
	case findings.CategoryCorrectness:
		return 2
	case findings.CategoryRequirement:
		return 3
	case findings.CategoryTesting:
		return 4
	case findings.CategoryArchitecture:
		return 5
	case findings.CategoryMaintainability:
		return 6
	default:
		return 7
	}
}

// Policy is the deterministic publication decision.
//
// It is the only thing in this system that decides whether a finding reaches a reader.
// Both model invocations feed it; neither can widen it. Given the same findings, the same
// verdicts, and the same diff, it always reaches the same conclusion.
type Policy struct {
	config Config
}

// NewPolicy returns a Policy using the default configuration.
func NewPolicy() Policy { return Policy{config: DefaultConfig()} }

// NewPolicyWithConfig returns a Policy using config, refusing an invalid one.
//
// The error is not advisory. A malformed policy must fail at construction rather than fall
// back to something plausible, because the fallback would publish under rules nobody chose.
func NewPolicyWithConfig(config Config) (Policy, error) {
	if err := config.Validate(); err != nil {
		return Policy{}, err
	}
	return Policy{config: config}, nil
}

// Config returns the configuration in force.
func (p Policy) Config() Config {
	if p.config == (Config{}) {
		return DefaultConfig()
	}
	return p.config
}

// Candidate is one verified finding entering the policy.
type Candidate struct {
	Finding findings.Finding

	// Verification is the outcome from the verification stage. A zero value means no
	// verification was attempted, which for a finding that requires one is fail-closed.
	Verification verification.Outcome
}

// CandidatesFrom converts a verification report into policy candidates.
func CandidatesFrom(report verification.Report) []Candidate {
	candidates := make([]Candidate, 0, len(report.Outcomes))
	for _, outcome := range report.Outcomes {
		candidates = append(candidates, Candidate{Finding: outcome.Finding, Verification: outcome})
	}
	return candidates
}

// BuildPlan decides the disposition of every candidate and assembles the publication plan.
//
// The order of work is the policy: each finding is judged on its own merits first — verdict,
// evidence, evidence strength, category, mappability — and only then do the quotas apply. That
// order matters, because a quota is a bound on noise, not a judgement about a finding: a
// finding pushed out of the inline slots is still worth reporting in the body, and one
// pushed out of the body is still worth showing locally.
func (p Policy) BuildPlan(
	candidates []Candidate,
	mapper Mapper,
	headSHA string,
	summary string,
) Plan {
	config := p.Config()

	decisions := make([]Decision, 0, len(candidates))
	for _, candidate := range candidates {
		decisions = append(decisions, p.evaluate(candidate, mapper))
	}

	p.applyQuotas(decisions, config)

	return assemblePlan(decisions, headSHA, summary)
}

// Evaluate decides one finding's disposition, before quotas.
//
// It is exported so a caller can explain a single decision without building a plan.
func (p Policy) Evaluate(candidate Candidate, mapper Mapper) Decision {
	return p.evaluate(candidate, mapper)
}

// evaluate applies every per-finding rule, in the order that makes the decision safe.
func (p Policy) evaluate(candidate Candidate, mapper Mapper) Decision {
	config := p.Config()
	finding := candidate.Finding

	decision := Decision{
		Finding:            finding,
		VerificationStatus: candidate.Verification.Status,
	}
	if candidate.Verification.Status == verification.StatusVerified {
		result := candidate.Verification.Result
		decision.Verification = &result
	}

	location, mappable, mapReason := mapper.Map(finding)
	decision.Mappable = mappable
	if mappable {
		decision.Location = location
	}

	// Evidence comes first. A finding without the evidence its category requires has not
	// been substantiated, whatever evidence strength a model assigned it, and no later gate can repair
	// that.
	if missing, ok := evidenceShortfall(finding); !ok {
		return suppress(decision, missing)
	}

	// Then the verdict. A contradicted or unestablished claim is withheld regardless of how
	// certain the reviewer was — that asymmetry is the entire point of having a verifier.
	if verdictReason, ok := p.verdictGate(candidate); !ok {
		return suppress(decision, verdictReason)
	}
	if decision.Verification != nil {
		decision.Reasons = append(decision.Reasons,
			reason(ReasonVerifiedValid, "verifier evidence strength %s", decision.VerifierEvidenceStrength().Display()))
	} else {
		decision.Reasons = append(decision.Reasons, Reason{Code: ReasonVerificationNotRequired})
	}

	// Low severity is never inline. It is worth mentioning if the reviewer was reasonably
	// sure, and worth nothing otherwise.
	if finding.Severity == findings.SeverityLow {
		if !finding.EvidenceStrength().AtLeast(config.LowSummaryStrength) {
			return suppress(decision, reason(ReasonLowEvidenceStrength,
				"reviewer evidence strength %s is below required %s for a low-severity finding",
				finding.EvidenceStrength().Display(), config.LowSummaryStrength.Display()))
		}
		return summarize(decision, Reason{Code: ReasonLowSeverity})
	}

	// The two ordinal assessments are gated independently and never combined. A HIGH
	// reviewer assessment and LOW verifier assessment is not MEDIUM; it is a strongly
	// supported claim nobody could confirm.
	if reviewerMinimum := config.ReviewerStrength(finding.Severity); !finding.EvidenceStrength().AtLeast(reviewerMinimum) {
		return suppress(decision, reason(ReasonLowEvidenceStrength,
			"reviewer evidence strength %s is below required %s for %s",
			finding.EvidenceStrength().Display(), reviewerMinimum.Display(), finding.Severity))
	}
	if verifierMinimum := config.VerifierStrength(finding.Severity); !decision.VerifierEvidenceStrength().AtLeast(verifierMinimum) {
		return suppress(decision, reason(ReasonLowVerifierEvidenceStrength,
			"verifier evidence strength %s is below required %s for %s",
			decision.VerifierEvidenceStrength().Display(), verifierMinimum.Display(), finding.Severity))
	}

	// Category rules decide placement rather than admission: a finding that reaches here is
	// established, and the only question left is whether a line comment is the right way to
	// say it.
	if categoryReason, inlineAllowed := p.categoryPlacement(decision); !inlineAllowed {
		return summarize(decision, categoryReason)
	}

	// Finally, GitHub has to have somewhere to put it. A valid finding is never withheld
	// merely because the diff cannot carry it.
	if !mappable {
		return summarize(decision, reason(ReasonNotDiffMappable, "%s", mapReason))
	}

	decision.Disposition = DispositionInline
	return decision
}

// verdictGate applies the verification-stage rules.
//
// A finding that required verification and did not get a usable one is withheld. This is
// the fail-closed half of the policy: a timeout is not a pass.
func (p Policy) verdictGate(candidate Candidate) (Reason, bool) {
	finding := candidate.Finding

	switch candidate.Verification.Status {
	case verification.StatusVerified:
		switch candidate.Verification.Result.Verdict {
		case verification.VerdictValid:
			return Reason{}, true
		case verification.VerdictInvalid:
			return reason(ReasonVerifierInvalid, "%s", candidate.Verification.Result.Reason), false
		case verification.VerdictUncertain:
			return reason(ReasonVerifierUncertain, "%s", candidate.Verification.Result.Reason), false
		default:
			return Reason{Code: ReasonVerificationMissing}, false
		}

	case verification.StatusNotRequired:
		// Only a finding that genuinely needs no verification may pass unverified. If the
		// two stages ever disagree about which those are, this refuses rather than trusts.
		if verification.RequiresVerification(finding) {
			return reason(ReasonVerificationMissing,
				"%s findings require verification", finding.Severity), false
		}
		return Reason{}, true

	case verification.StatusFailed:
		return reason(ReasonVerificationFailed, "%s", candidate.Verification.FailureReason), false

	default:
		return Reason{Code: ReasonVerificationMissing}, false
	}
}

// categoryPlacement reports whether this finding's category permits an inline comment.
func (p Policy) categoryPlacement(decision Decision) (Reason, bool) {
	config := p.Config()
	finding := decision.Finding

	switch finding.Category {
	case findings.CategorySecurity:
		// A security finding clears a stricter verifier bar to reach a line. Below it the
		// finding is still reported, because a plausible security concern is worth a
		// reader's attention even when it is not certain enough to interrupt them.
		if !decision.VerifierEvidenceStrength().AtLeast(config.SecurityVerifierStrength) {
			return reason(ReasonCategoryPolicy,
				"security findings need verifier evidence strength %s to be inline; this is %s",
				config.SecurityVerifierStrength.Display(), decision.VerifierEvidenceStrength().Display()), false
		}

	case findings.CategoryArchitecture:
		// An architectural argument about one line of a diff reads as a demand. Blocker and
		// high architecture findings still earn a line; medium ones are discussed in the body.
		if finding.Severity == findings.SeverityMedium && !config.ArchitectureMediumInline {
			return reason(ReasonCategoryPolicy,
				"medium architecture findings are reported in the review body"), false
		}

	case findings.CategoryMaintainability:
		// Maintainability is where subjective criticism hides. Medium maintainability has
		// already had to survive the verifier's materiality bar to get here; it is still
		// reported in the body rather than on a line, because "this is expensive" is an
		// argument, not a defect at a position.
		if finding.Severity == findings.SeverityMedium {
			return reason(ReasonCategoryPolicy,
				"medium maintainability findings are reported in the review body"), false
		}
	}

	return Reason{}, true
}

// evidenceShortfall reports whether a finding carries the evidence its category requires.
//
// The requirements are deliberately about kind, not quantity. A correctness claim that
// cites no code has not been located; a requirement claim that cites neither the ticket nor
// a rule has no requirement to compare against, only an assumption about one. Structurally
// valid JSON is not the same as a substantiated claim.
func evidenceShortfall(finding findings.Finding) (Reason, bool) {
	has := func(types ...findings.EvidenceType) bool {
		for _, evidence := range finding.Evidence {
			for _, want := range types {
				if evidence.Type == want {
					return true
				}
			}
		}
		return false
	}

	switch finding.Category {
	case findings.CategoryCorrectness, findings.CategorySecurity, findings.CategoryMaintainability:
		if !has(findings.EvidenceCode) {
			return reason(ReasonEvidenceMissing,
				"%s findings require code evidence", finding.Category), false
		}

	case findings.CategoryRequirement:
		if !has(findings.EvidenceJira, findings.EvidenceRule, findings.EvidenceDocument) {
			return reason(ReasonRequirementEvidenceMissing,
				"requirement findings require Jira, repository-rule, or configured document evidence"), false
		}

	case findings.CategoryTesting:
		if !has(findings.EvidenceCode, findings.EvidenceTest, findings.EvidenceVet) {
			return reason(ReasonEvidenceMissing,
				"testing findings require code or deterministic test evidence"), false
		}

	case findings.CategoryArchitecture:
		if !has(findings.EvidenceCode, findings.EvidenceRule, findings.EvidenceDocument) {
			return reason(ReasonEvidenceMissing,
				"architecture findings require code, repository-rule, or configured document evidence"), false
		}
	}

	return Reason{}, true
}

// applyQuotas enforces the inline, summary, and total bounds over already-judged decisions.
//
// Overflow always demotes by one step rather than dropping: inline becomes body, body
// becomes local-only. A quota says "this review is long enough", not "that finding was
// wrong".
func (p Policy) applyQuotas(decisions []Decision, config Config) {
	order := orderedIndexes(decisions)

	inlineUsed := 0
	for _, i := range order {
		if decisions[i].Disposition != DispositionInline {
			continue
		}
		if inlineUsed < config.MaxInlineComments {
			inlineUsed++
			continue
		}
		decisions[i] = summarize(decisions[i], reason(ReasonCommentLimit,
			"the inline comment limit of %d was reached", config.MaxInlineComments))
	}

	summaryUsed := 0
	for _, i := range order {
		if decisions[i].Disposition != DispositionSummary {
			continue
		}
		if summaryUsed < config.MaxSummaryFindings {
			summaryUsed++
			continue
		}
		decisions[i] = suppress(decisions[i], reason(ReasonSummaryLimit,
			"the review body limit of %d findings was reached", config.MaxSummaryFindings))
	}

	// The total bound is applied last, and takes from the body rather than from the lines:
	// an inline comment is the more useful of the two, and demoting one would only move the
	// same text into a body that is already full.
	published := inlineUsed + summaryUsed
	if published <= config.MaxPublishedFindings {
		return
	}

	excess := published - config.MaxPublishedFindings
	for i := len(order) - 1; i >= 0 && excess > 0; i-- {
		index := order[i]
		if decisions[index].Disposition != DispositionSummary {
			continue
		}
		decisions[index] = suppress(decisions[index], reason(ReasonTotalLimit,
			"the published-finding limit of %d was reached", config.MaxPublishedFindings))
		excess--
	}
}

// orderedIndexes returns decision indexes in publication priority order.
//
// The keys are, in order: severity, category priority, reviewer evidence strength descending,
// verifier evidence strength descending, file, start line, and ID. The last three exist so the
// order never depends on the order a model happened to emit its findings in.
func orderedIndexes(decisions []Decision) []int {
	order := make([]int, len(decisions))
	for i := range decisions {
		order[i] = i
	}

	sort.SliceStable(order, func(a, b int) bool {
		x, y := decisions[order[a]], decisions[order[b]]

		if rx, ry := x.Finding.Severity.Rank(), y.Finding.Severity.Rank(); rx != ry {
			return rx < ry
		}
		if cx, cy := categoryPriority(x.Finding.Category), categoryPriority(y.Finding.Category); cx != cy {
			return cx < cy
		}
		if sx, sy := x.Finding.EvidenceStrength().Rank(), y.Finding.EvidenceStrength().Rank(); sx != sy {
			return sx > sy
		}
		if sx, sy := x.VerifierEvidenceStrength().Rank(), y.VerifierEvidenceStrength().Rank(); sx != sy {
			return sx > sy
		}
		if x.Finding.File != y.Finding.File {
			return x.Finding.File < y.Finding.File
		}
		if x.Finding.StartLine != y.Finding.StartLine {
			return x.Finding.StartLine < y.Finding.StartLine
		}
		return x.Finding.ID < y.Finding.ID
	})

	return order
}

// assemblePlan turns decisions into the plan the renderer and publisher consume.
func assemblePlan(decisions []Decision, headSHA string, summary string) Plan {
	plan := Plan{ResultSummary: summary, HeadSHA: headSHA, Decisions: decisions}

	for _, i := range orderedIndexes(decisions) {
		decision := decisions[i]

		switch decision.Disposition {
		case DispositionInline:
			plan.Inline = append(plan.Inline, InlineFinding{
				Finding:  decision.Finding,
				Location: decision.Location,
			})
		case DispositionSummary:
			plan.Summary = append(plan.Summary, decision.Finding)
		default:
			plan.Suppressed = append(plan.Suppressed, SuppressedFinding{
				Finding: decision.Finding,
				Reason:  decision.Reasons.Primary().String(),
			})
		}
	}

	plan.Stats = tallyDecisions(decisions)
	return plan
}

// tallyDecisions counts the dispositions and the grounds for each suppression.
func tallyDecisions(decisions []Decision) Stats {
	stats := Stats{Input: len(decisions)}

	for _, decision := range decisions {
		switch decision.Disposition {
		case DispositionInline:
			stats.Inline++
		case DispositionSummary:
			stats.Summary++
		default:
			stats.Suppressed++
		}

		if decision.Reasons.Has(ReasonCommentLimit) {
			stats.LimitedByInlineCap++
		}
		if decision.Reasons.Has(ReasonSummaryLimit) {
			stats.LimitedBySummaryCap++
		}
		if decision.Reasons.Has(ReasonTotalLimit) {
			stats.LimitedByTotalCap++
		}

		if decision.Disposition != DispositionSuppress {
			continue
		}
		switch decision.Reasons.Primary().Code {
		case ReasonVerifierInvalid:
			stats.SuppressedInvalidVerifier++
		case ReasonVerifierUncertain:
			stats.SuppressedUncertainVerifier++
		case ReasonVerificationFailed, ReasonVerificationMissing:
			stats.SuppressedVerificationFailed++
		case ReasonLowEvidenceStrength, ReasonLowVerifierEvidenceStrength:
			stats.SuppressedLowEvidenceStrength++
		case ReasonEvidenceMissing, ReasonRequirementEvidenceMissing:
			stats.SuppressedEvidence++
		}
	}

	return stats
}

// suppress finalizes a decision as withheld, recording the decisive reason first.
func suppress(decision Decision, why Reason) Decision {
	decision.Disposition = DispositionSuppress
	decision.Reasons = append(Reasons{why}, decision.Reasons...)
	return decision
}

// summarize finalizes a decision as body-only.
//
// The placement reason goes first, because it is the decisive one: whatever else is true of
// the finding, this is why it is in the body rather than on a line.
func summarize(decision Decision, why Reason) Decision {
	decision.Disposition = DispositionSummary
	decision.Reasons = append(Reasons{why}, decision.Reasons...)
	return decision
}

// Explain renders one decision as a short multi-line block for terminal output.
func (d Decision) Explain() string {
	out := fmt.Sprintf("  severity:       %s\n", d.Finding.Severity.Display())
	out += fmt.Sprintf("  reviewer:       %s evidence\n", d.Finding.EvidenceStrength().Display())

	switch {
	case d.Verification != nil:
		out += fmt.Sprintf("  verifier:       %s / %s evidence\n",
			d.Verification.Verdict.Display(), d.Verification.EvidenceStrength().Display())
	case d.VerificationStatus == verification.StatusNotRequired:
		out += "  verification:   not required\n"
	case d.VerificationStatus == verification.StatusFailed:
		out += "  verification:   failed\n"
	default:
		out += "  verification:   none\n"
	}

	if d.Mappable {
		out += "  location:       valid\n"
	} else {
		out += "  location:       not mappable\n"
	}

	out += fmt.Sprintf("  decision:       %s\n", d.Disposition.Display())
	if primary := d.Reasons.Primary(); primary.Code != "" {
		out += fmt.Sprintf("  reason:         %s\n", primary.String())
	}
	return out
}
