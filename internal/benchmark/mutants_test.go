package benchmark

import "github.com/your-company/agentic-code-review/internal/findings"

// Mutants returns the seeded defects.
//
// Every one is a real invariant of this codebase, deleted or inverted the way a
// developer plausibly would: a guard removed while refactoring, a comparison
// flipped, an assertion loosened to make a test pass. They are drawn from ARC's own
// source because the answer is then knowable — the invariant is documented, tested,
// and explained in a comment three lines away — and because a reviewer that cannot
// find a deleted security check in the tool's own code will not find one in yours.
//
// The clean controls matter as much. A reviewer that reports something on every
// change has no precision, and precision is what decides whether anyone keeps
// reading its comments.
func Mutants() []Mutant {
	return []Mutant{
		{
			ID:   "MUT-01-scope-rule-removed",
			Path: "internal/findings/validate.go",
			Original: `	case !changed[normalizePath(file)]:
		add("file", fmt.Sprintf("%q is not a file changed by this pull request", file))
	}`,
			Mutated: `	}`,
			Defect: "the changed-file scope rule is deleted, so a finding may blame " +
				"a file the pull request never touched",
			WantCategory: findings.CategoryCorrectness,
			WantTerms: []string{
				"changed file", "changed-file", "scope", "untouched", "not changed",
				"any file", "always returns", "validation is skipped", "never fails",
			},
		},
		{
			ID:       "MUT-02-capture-overwrites",
			Path:     "internal/evaluation/capture.go",
			Original: "	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)",
			Mutated:  "	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)",
			Defect: "O_EXCL becomes O_TRUNC, so capture silently overwrites an existing " +
				"snapshot instead of refusing",
			WantCategory: findings.CategoryCorrectness,
			WantTerms: []string{
				"overwrit", "truncat", "o_trunc", "o_excl", "existing file", "destroy",
				"clobber", "already exists",
			},
		},
		{
			ID:   "MUT-03-base-ref-fallback",
			Path: "internal/reporules/loader.go",
			Original: `	if strings.TrimSpace(baseRef) == "" {
		return Rules{}, errors.New("load rules: base ref is required; head-branch rules are never authoritative")
	}`,
			Mutated: `	if strings.TrimSpace(baseRef) == "" {
		baseRef = headRef
	}`,
			Defect: "a missing base ref falls back to the head branch, letting a pull " +
				"request supply the rules that judge it",
			WantCategory: findings.CategorySecurity,
			WantTerms: []string{
				"head", "own rules", "authoritative", "trust", "weaken", "bypass",
				"fall back", "fallback", "judge itself", "its own",
			},
		},
		{
			ID:   "MUT-04-stale-head-ignored",
			Path: "internal/publish/publisher.go",
			Original: `	if !strings.EqualFold(strings.TrimSpace(current.HeadSHA), reviewed) {
		outcome.Status = StatusAbortedStaleHead
		return outcome, &StaleHeadError{ReviewedHeadSHA: reviewed, CurrentHeadSHA: current.HeadSHA}
	}`,
			Mutated: `	if strings.TrimSpace(current.HeadSHA) == "" {
		outcome.Status = StatusAbortedStaleHead
		return outcome, &StaleHeadError{ReviewedHeadSHA: reviewed, CurrentHeadSHA: current.HeadSHA}
	}`,
			Defect: "the reviewed head is no longer compared with the current head, so a " +
				"review can be published against code that has moved",
			WantCategory: findings.CategoryCorrectness,
			WantTerms: []string{
				"stale", "head sha", "current head", "moved", "changed while", "no longer compared",
				"mismatch", "outdated", "different commit",
			},
		},
		{
			ID:   "MUT-05-dismissal-hides-security",
			Path: "internal/publish/dismissal.go",
			Original: `	if serious(decision.Finding) {
		return summarize(decision, reason)
	}
	return suppress(decision, reason)`,
			Mutated: `	return suppress(decision, reason)`,
			Defect: "a comment can now delete a blocker or a security finding entirely, " +
				"rather than demoting it to the review body",
			WantCategory: findings.CategorySecurity,
			WantTerms: []string{
				"security", "blocker", "suppress", "hidden", "silenc", "serious",
				"anyone who can comment", "removed entirely", "demot",
			},
		},
		{
			ID:   "MUT-06-verifier-uncertain-publishes",
			Path: "internal/publish/policy.go",
			Original: `		case verification.VerdictUncertain:
			return reason(ReasonVerifierUncertain, "%s", candidate.Verification.Result.Reason), false`,
			Mutated: `		case verification.VerdictUncertain:
			return Reason{}, true`,
			Defect: "an uncertain verifier verdict now passes the gate, so unconfirmed " +
				"findings reach the diff",
			WantCategory: findings.CategoryCorrectness,
			WantTerms: []string{
				"uncertain", "unconfirmed", "passes", "gate", "publish", "fail-closed",
				"fail closed", "verdict", "treated as valid",
			},
		},
		{
			ID:   "MUT-07-comment-bound-removed",
			Path: "internal/github/discussion.go",
			Original: `	trimmed := strings.TrimSpace(body)
	if len(trimmed) <= MaxCommentBytes {
		return trimmed, false
	}`,
			Mutated: `	trimmed := strings.TrimSpace(body)
	if true {
		return trimmed, false
	}`,
			Defect: "the per-comment size bound is disabled, so one comment can consume " +
				"the whole review context",
			WantCategory: findings.CategoryCorrectness,
			WantTerms: []string{
				"unbounded", "bound", "limit", "size", "maxcommentbytes", "unlimited",
				"dead code", "always", "never truncat",
			},
		},
		{
			ID:   "MUT-08-weakened-assertion",
			Path: "internal/publish/history_test.go",
			Original: `	if Fingerprint(first) != Fingerprint(moved) {
		t.Error("fingerprint changed when only the line, ID, severity, and prose moved")
	}`,
			Mutated: `	if false {
		t.Error("fingerprint changed when only the line, ID, severity, and prose moved")
	}`,
			Defect: "a test assertion is disabled, so the test passes whether the " +
				"fingerprint is stable or not",
			WantCategory: findings.CategoryTesting,
			WantTerms: []string{
				"assertion", "always pass", "never fail", "disabled", "if false",
				"no longer test", "does not test", "vacuous", "dead",
			},
		},

		// Clean controls. Each is a change a reviewer should have nothing to say
		// about: the behaviour is identical, and the only correct number of findings
		// is zero.
		{
			ID:    "CLEAN-01-comment-wording",
			Path:  "internal/changerisk/analyze.go",
			Clean: true,
			Original: "// Analyzer produces a risk profile from a change. It is deterministic and holds no\n" +
				"// state between calls.",
			Mutated: "// Analyzer produces a risk profile from a change. It is deterministic, and it\n" +
				"// keeps no state between calls.",
			Defect: "a comment is reworded; behaviour is identical",
		},
		{
			ID:    "CLEAN-02-local-rename",
			Path:  "internal/retrieval/retrieve.go",
			Clean: true,
			Original: `	ranked := rankTouched(changes)
	if len(ranked) == 0 {
		return skipped("no resolvable identifiers on the changed lines"), nil
	}`,
			Mutated: `	rankedSymbols := rankTouched(changes)
	if len(rankedSymbols) == 0 {
		return skipped("no resolvable identifiers on the changed lines"), nil
	}
	ranked := rankedSymbols`,
			Defect: "a local variable is renamed, with an alias so later code is unchanged",
		},
		{
			ID:    "CLEAN-03-equivalent-condition",
			Path:  "internal/contextselect/budget.go",
			Clean: true,
			Original: `	if b.Retrieval <= 0 {
		b.Retrieval = defaults.Retrieval
	}`,
			Mutated: `	if !(b.Retrieval > 0) {
		b.Retrieval = defaults.Retrieval
	}`,
			Defect: "a condition is rewritten to an equivalent form",
		},
	}
}
