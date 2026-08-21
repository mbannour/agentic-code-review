// Package specialist decides which review perspectives a change deserves.
//
// A general reviewer asked to look for everything at once looks for nothing in
// particular. A specialist is a narrower question — is the access control right,
// is the behaviour tested, does this match what was asked for — and narrower
// questions produce better answers.
//
// Selection is deterministic and explained. Every specialist is chosen because a
// named signal in the change called for it, and the reason is printed, logged, and
// testable. Nothing here calls a model; a specialist is a description of what to
// look for, and this package decides which descriptions apply.
//
// Deliberately absent: any notion of a specialist that runs always. A perspective
// that applies to every change is not a specialist, it is the general reviewer.
package specialist

import (
	"sort"
	"strings"

	"github.com/your-company/agentic-code-review/internal/changerisk"
	"github.com/your-company/agentic-code-review/internal/findings"
)

// ID identifies a specialist.
type ID string

const (
	// IDCorrectness is the baseline perspective: logic, edge cases, invariants,
	// error handling, regression risk.
	IDCorrectness ID = "correctness"

	// IDSecurity examines access control, injection, secret handling, and trust
	// boundaries.
	IDSecurity ID = "security"

	// IDContract examines requirements, acceptance criteria, and published
	// interfaces — what was asked for against what was built.
	IDContract ID = "contract"

	// IDReliability examines retries, idempotency, concurrency, partial failure,
	// and transactional integrity.
	IDReliability ID = "reliability"

	// IDTestAdequacy asks not whether tests pass but whether the changed
	// behaviour is actually proven by one.
	IDTestAdequacy ID = "test_adequacy"
)

// Specialist is a review perspective: what it looks for, when it applies, and
// which finding categories it may legitimately produce.
//
// It is data rather than behaviour on purpose. A specialist that could execute
// something would be a second place where review policy lives.
type Specialist struct {
	ID ID

	// Title is the human-readable name used in output.
	Title string

	// Purpose is one sentence stating the question this specialist answers.
	Purpose string

	// Focus are the concrete things to examine. They become prompt guidance.
	Focus []string

	// Categories are the finding categories this perspective may report. It is a
	// bound on scope, not a licence: a specialist reporting outside its categories
	// is drifting back toward being a general reviewer.
	Categories []findings.Category

	// TriggerAreas are the risk areas that call for this specialist.
	TriggerAreas []changerisk.Area

	// MinimumLevel is the risk band below which this specialist is not worth its
	// cost, even when a trigger area is present.
	MinimumLevel changerisk.Level
}

// Registry is every specialist ARC knows, in a stable order.
//
// Adding a perspective means adding an entry here. Nothing in the router,
// orchestration, or output needs to change, which is the point of keeping a
// specialist as data.
func Registry() []Specialist {
	return []Specialist{
		{
			ID: IDCorrectness, Title: "Correctness",
			Purpose: "Does the changed code do what it is evidently meant to do?",
			Focus: []string{
				"logic that is wrong for an input the change makes reachable",
				"edge cases at boundaries the change introduces or moves",
				"invariants the surrounding code relies on and this change breaks",
				"unsafe or missing state transitions",
				"nil, empty, and error paths the change adds or bypasses",
				"behaviour the change silently alters for existing callers",
			},
			Categories: []findings.Category{
				findings.CategoryCorrectness, findings.CategoryMaintainability,
			},
			// Correctness applies to any production code change; the router adds
			// it as the baseline rather than by area.
			TriggerAreas: nil,
			MinimumLevel: changerisk.LevelLow,
		},
		{
			ID: IDSecurity, Title: "Security",
			Purpose: "Can this change be used to reach data or actions it should not?",
			Focus: []string{
				"authentication or authorization checks removed, weakened, or bypassed",
				"an identifier taken from a request and trusted without an ownership check",
				"input reaching a query, command, path, or template without validation",
				"secrets, tokens, or credentials written to logs, errors, or responses",
				"deserialization of untrusted input into executable structures",
				"a trust boundary crossed without revalidation",
			},
			Categories: []findings.Category{
				findings.CategorySecurity, findings.CategoryCorrectness,
			},
			TriggerAreas: []changerisk.Area{
				changerisk.AreaAuthentication, changerisk.AreaAuthorization,
				changerisk.AreaCryptography, changerisk.AreaPublicAPI,
				changerisk.AreaSerialization, changerisk.AreaDependencies,
				changerisk.AreaInfrastructure, changerisk.AreaConfiguration,
			},
			MinimumLevel: changerisk.LevelLow,
		},
		{
			ID: IDContract, Title: "Requirements and contracts",
			Purpose: "Does the implementation match what was asked for and what others depend on?",
			Focus: []string{
				"an acceptance criterion that is not implemented, or implemented partially",
				"behaviour that contradicts an explicit statement in the ticket",
				"a status code, error shape, or field name that differs from the stated contract",
				"a public type, enum, route, or schema changed in a way existing consumers would not survive",
				"a serialized field renamed, removed, or retyped",
				"an exhaustive match elsewhere that a new enum case would not satisfy",
			},
			Categories: []findings.Category{
				findings.CategoryRequirement, findings.CategoryArchitecture,
				findings.CategoryCorrectness,
			},
			TriggerAreas: []changerisk.Area{
				changerisk.AreaPublicAPI, changerisk.AreaSerialization,
				changerisk.AreaMigration, changerisk.AreaStateMachine,
				changerisk.AreaPayments,
			},
			MinimumLevel: changerisk.LevelLow,
		},
		{
			ID: IDReliability, Title: "Reliability",
			Purpose: "What happens when this runs twice, slowly, or halfway?",
			Focus: []string{
				"an operation that is not idempotent but can be retried",
				"a retry path that can duplicate an effect, event, or charge",
				"work spanning a transaction boundary that can commit partially",
				"shared state mutated without synchronization, or a lock held across I/O",
				"a missing timeout, deadline, or cancellation path",
				"an event emitted before the state it describes is durable",
			},
			Categories: []findings.Category{
				findings.CategoryCorrectness, findings.CategoryArchitecture,
			},
			TriggerAreas: []changerisk.Area{
				changerisk.AreaConcurrency, changerisk.AreaPayments,
				changerisk.AreaDatabase, changerisk.AreaMigration,
				changerisk.AreaStateMachine, changerisk.AreaErrorHandling,
			},
			MinimumLevel: changerisk.LevelMedium,
		},
		{
			ID: IDTestAdequacy, Title: "Test adequacy",
			Purpose: "Is the behaviour this change introduces actually proven by a test?",
			Focus: []string{
				"behaviour the diff introduces that no test exercises",
				"an acceptance criterion with no test that would fail if it regressed",
				"an assertion weakened, deleted, or replaced with a looser one",
				"a test that would pass whether or not the fix is present",
				"mocking so broad that the tested behaviour is the mock's, not the code's",
				"a removed test whose coverage was not replaced",
			},
			Categories: []findings.Category{findings.CategoryTesting},
			TriggerAreas: []changerisk.Area{
				changerisk.AreaTests, changerisk.AreaPayments,
				changerisk.AreaAuthentication, changerisk.AreaAuthorization,
				changerisk.AreaStateMachine, changerisk.AreaMigration,
				changerisk.AreaPublicAPI,
			},
			// Low rather than medium, because a change touching only tests lands at
			// low risk and is exactly where a weakened assertion hides. Without this
			// the one perspective that could catch it would never be selected.
			MinimumLevel: changerisk.LevelLow,
		},
	}
}

// ByID returns a specialist by identifier.
func ByID(id ID) (Specialist, bool) {
	for _, candidate := range Registry() {
		if candidate.ID == id {
			return candidate, true
		}
	}
	return Specialist{}, false
}

// Selection is one chosen specialist and why it was chosen.
type Selection struct {
	Specialist Specialist

	// Reasons name the signals that selected it, most specific first.
	Reasons []string
}

// Explain renders the selection as one readable line.
func (s Selection) Explain() string {
	if len(s.Reasons) == 0 {
		return string(s.Specialist.ID) + " selected"
	}
	return string(s.Specialist.ID) + " selected because " + strings.Join(s.Reasons, "; ")
}

// Plan is the routing decision for one change.
type Plan struct {
	// Selected are the specialists to run, in Registry() order.
	Selected []Selection

	// Skipped records the specialists that were not selected and why not, so a
	// perspective's absence is as explainable as its presence.
	Skipped []Skip

	// Level is the risk band the plan was built for.
	Level changerisk.Level
}

// Skip is a specialist that was not selected, and the reason.
type Skip struct {
	ID     ID
	Reason string
}

// IDs returns the selected specialist identifiers, in order.
func (p Plan) IDs() []ID {
	ids := make([]ID, 0, len(p.Selected))
	for _, selection := range p.Selected {
		ids = append(ids, selection.Specialist.ID)
	}
	return ids
}

// Includes reports whether a specialist was selected.
func (p Plan) Includes(id ID) bool {
	for _, selection := range p.Selected {
		if selection.Specialist.ID == id {
			return true
		}
	}
	return false
}

// Router selects specialists from a risk profile. It is deterministic: the same
// profile always yields the same plan, in the same order, with the same reasons.
type Router struct{}

// NewRouter returns a Router.
func NewRouter() Router { return Router{} }

// Route builds the routing plan for a change.
//
// Two rules shape it. Correctness is the baseline for any change containing
// production code, because a change that does the wrong thing is the failure mode
// every other perspective assumes away. Everything else must be called for by a
// named signal — running every specialist on every change would multiply cost and
// noise together, and noise is what makes people stop reading reviews.
func (Router) Route(profile changerisk.Profile) Plan {
	plan := Plan{Level: profile.Level}

	for _, candidate := range Registry() {
		reasons, selected := match(candidate, profile)
		if selected {
			plan.Selected = append(plan.Selected, Selection{Specialist: candidate, Reasons: reasons})
			continue
		}
		plan.Skipped = append(plan.Skipped, Skip{ID: candidate.ID, Reason: skipReason(candidate, profile)})
	}
	return plan
}

// match decides whether one specialist applies, and states why.
func match(candidate Specialist, profile changerisk.Profile) ([]string, bool) {
	if !profile.Level.AtLeast(candidate.MinimumLevel) {
		return nil, false
	}

	// The baseline perspective: no trigger areas, so it applies whenever there is
	// production code to be wrong about.
	if len(candidate.TriggerAreas) == 0 {
		if profile.SourceFiles == 0 {
			return nil, false
		}
		return []string{"the change modifies production code"}, true
	}

	var reasons []string
	for _, area := range candidate.TriggerAreas {
		if !profile.HasArea(area) {
			continue
		}
		signals := profile.SignalsFor(area)
		if len(signals) == 0 {
			reasons = append(reasons, string(area)+" was touched")
			continue
		}
		reasons = append(reasons, signals[0].Detail+" ("+signals[0].Path+")")
	}
	if len(reasons) == 0 {
		return nil, false
	}

	sort.Strings(reasons)
	return reasons, true
}

// skipReason states why a specialist was not selected.
func skipReason(candidate Specialist, profile changerisk.Profile) string {
	// The substantive reason first. A documentation-only change is also below the
	// risk gate, but "there is no production code here" is what a reader needs.
	if len(candidate.TriggerAreas) == 0 && profile.SourceFiles == 0 {
		return "the change contains no production code"
	}
	if !profile.Level.AtLeast(candidate.MinimumLevel) {
		return "change risk " + string(profile.Level) + " is below this specialist's minimum of " +
			string(candidate.MinimumLevel)
	}
	return "no signal in this change calls for it"
}
