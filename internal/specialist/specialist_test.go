package specialist

import (
	"strings"
	"testing"

	"github.com/your-company/agentic-code-review/internal/changerisk"
	"github.com/your-company/agentic-code-review/internal/findings"
)

func profileWith(level changerisk.Level, sourceFiles int, areas ...changerisk.Area) changerisk.Profile {
	profile := changerisk.Profile{Level: level, SourceFiles: sourceFiles, Areas: areas}
	for _, area := range areas {
		profile.Signals = append(profile.Signals, changerisk.Signal{
			Area: area, Path: "internal/x/x.go",
			Detail: string(area) + " signal", FromPath: true,
		})
	}
	return profile
}

func TestRoutingSelectsBySignal(t *testing.T) {
	cases := []struct {
		name    string
		profile changerisk.Profile
		want    []ID
		notWant []ID
	}{
		{
			name:    "auth middleware changed",
			profile: profileWith(changerisk.LevelHigh, 2, changerisk.AreaAuthentication, changerisk.AreaAuthorization),
			want:    []ID{IDCorrectness, IDSecurity, IDTestAdequacy},
			notWant: []ID{IDContract},
		},
		{
			name:    "database migration",
			profile: profileWith(changerisk.LevelHigh, 1, changerisk.AreaMigration, changerisk.AreaDatabase),
			want:    []ID{IDCorrectness, IDContract, IDReliability, IDTestAdequacy},
		},
		{
			name:    "payment retry logic",
			profile: profileWith(changerisk.LevelHigh, 3, changerisk.AreaPayments, changerisk.AreaErrorHandling),
			want:    []ID{IDCorrectness, IDContract, IDReliability, IDTestAdequacy},
		},
		{
			name:    "openapi schema changed",
			profile: profileWith(changerisk.LevelMedium, 1, changerisk.AreaPublicAPI),
			want:    []ID{IDCorrectness, IDContract, IDSecurity, IDTestAdequacy},
			notWant: []ID{IDReliability},
		},
		{
			name:    "ordinary refactor",
			profile: profileWith(changerisk.LevelLow, 2),
			want:    []ID{IDCorrectness},
			notWant: []ID{IDSecurity, IDContract, IDReliability, IDTestAdequacy},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := NewRouter().Route(tc.profile)

			for _, want := range tc.want {
				if !plan.Includes(want) {
					t.Errorf("%s was not selected; selected = %v", want, plan.IDs())
				}
			}
			for _, notWant := range tc.notWant {
				if plan.Includes(notWant) {
					t.Errorf("%s was selected but no signal calls for it", notWant)
				}
			}
		})
	}
}

// Not running every specialist on every change is the point: it is what keeps
// cost and noise from growing together.
func TestRoutingDoesNotSelectEverything(t *testing.T) {
	plan := NewRouter().Route(profileWith(changerisk.LevelLow, 1))

	if len(plan.Selected) == len(Registry()) {
		t.Errorf("every specialist was selected for a low-risk change: %v", plan.IDs())
	}
	if len(plan.Skipped) == 0 {
		t.Error("nothing was recorded as skipped")
	}
}

// A documentation-only change has no production code to be wrong about.
func TestNoProductionCodeSelectsNoBaselineReviewer(t *testing.T) {
	plan := NewRouter().Route(profileWith(changerisk.LevelMinimal, 0, changerisk.AreaDocumentation))

	if plan.Includes(IDCorrectness) {
		t.Error("the correctness reviewer was selected for a change with no production code")
	}
	for _, skip := range plan.Skipped {
		if skip.ID == IDCorrectness && !strings.Contains(skip.Reason, "no production code") {
			t.Errorf("skip reason = %q, want it to state the absence of production code", skip.Reason)
		}
	}
}

// A specialist too expensive for the risk band must say so, not vanish silently.
func TestMinimumLevelGatesExpensiveSpecialists(t *testing.T) {
	// Reliability triggers on concurrency but requires at least medium risk.
	low := NewRouter().Route(profileWith(changerisk.LevelLow, 1, changerisk.AreaConcurrency))
	if low.Includes(IDReliability) {
		t.Error("reliability was selected below its minimum risk level")
	}

	var reason string
	for _, skip := range low.Skipped {
		if skip.ID == IDReliability {
			reason = skip.Reason
		}
	}
	if !strings.Contains(reason, "below this specialist's minimum") {
		t.Errorf("skip reason = %q, want it to name the level gate", reason)
	}

	medium := NewRouter().Route(profileWith(changerisk.LevelMedium, 1, changerisk.AreaConcurrency))
	if !medium.Includes(IDReliability) {
		t.Error("reliability was not selected at medium risk with a concurrency signal")
	}
}

// A selection nobody can explain is a selection nobody can trust.
func TestEverySelectionCarriesAReason(t *testing.T) {
	plan := NewRouter().Route(profileWith(changerisk.LevelHigh, 4,
		changerisk.AreaAuthentication, changerisk.AreaPayments, changerisk.AreaConcurrency))

	for _, selection := range plan.Selected {
		if len(selection.Reasons) == 0 {
			t.Errorf("%s was selected with no reason", selection.Specialist.ID)
		}
		if explained := selection.Explain(); !strings.Contains(explained, "because") {
			t.Errorf("Explain() = %q, want it to state a cause", explained)
		}
	}
	for _, skip := range plan.Skipped {
		if strings.TrimSpace(skip.Reason) == "" {
			t.Errorf("%s was skipped with no reason", skip.ID)
		}
	}
}

func TestRoutingIsDeterministic(t *testing.T) {
	profile := profileWith(changerisk.LevelHigh, 5,
		changerisk.AreaPayments, changerisk.AreaStateMachine, changerisk.AreaPublicAPI)

	first := NewRouter().Route(profile)
	for i := 0; i < 5; i++ {
		again := NewRouter().Route(profile)
		if len(again.Selected) != len(first.Selected) {
			t.Fatalf("selection size changed between runs")
		}
		for j := range again.Selected {
			if again.Selected[j].Specialist.ID != first.Selected[j].Specialist.ID {
				t.Fatalf("selection order changed: %v then %v", first.IDs(), again.IDs())
			}
			if strings.Join(again.Selected[j].Reasons, "|") != strings.Join(first.Selected[j].Reasons, "|") {
				t.Fatalf("reasons changed between runs for %s", again.Selected[j].Specialist.ID)
			}
		}
	}
}

// Every specialist must be a bounded perspective: a stated purpose, concrete
// things to look for, and categories it may report. A specialist without those is
// the general reviewer wearing a label.
func TestRegistryEntriesAreWellFormed(t *testing.T) {
	seen := map[ID]bool{}

	for _, candidate := range Registry() {
		if candidate.ID == "" || seen[candidate.ID] {
			t.Errorf("specialist %+v has a missing or duplicate id", candidate)
		}
		seen[candidate.ID] = true

		if strings.TrimSpace(candidate.Title) == "" {
			t.Errorf("%s has no title", candidate.ID)
		}
		if !strings.HasSuffix(strings.TrimSpace(candidate.Purpose), "?") {
			t.Errorf("%s purpose = %q, want a question it answers", candidate.ID, candidate.Purpose)
		}
		if len(candidate.Focus) < 3 {
			t.Errorf("%s has %d focus items, want at least 3 concrete things to look for",
				candidate.ID, len(candidate.Focus))
		}
		if len(candidate.Categories) == 0 {
			t.Errorf("%s may report no category, so it can produce nothing", candidate.ID)
		}
		for _, category := range candidate.Categories {
			if !category.Valid() {
				t.Errorf("%s declares unknown category %q", candidate.ID, category)
			}
		}
		for _, area := range candidate.TriggerAreas {
			if !area.Valid() {
				t.Errorf("%s triggers on unknown area %q", candidate.ID, area)
			}
		}
	}

	// Exactly one baseline perspective, or "baseline" means nothing.
	baselines := 0
	for _, candidate := range Registry() {
		if len(candidate.TriggerAreas) == 0 {
			baselines++
		}
	}
	if baselines != 1 {
		t.Errorf("registry declares %d baseline specialists, want exactly 1", baselines)
	}
}

// The test-adequacy perspective is the only one allowed to own the testing
// category; if everything could report it, the routing would not shape anything.
func TestTestingCategoryBelongsToTestAdequacy(t *testing.T) {
	for _, candidate := range Registry() {
		if candidate.ID == IDTestAdequacy {
			continue
		}
		for _, category := range candidate.Categories {
			if category == findings.CategoryTesting {
				t.Errorf("%s may report testing findings, which belong to %s", candidate.ID, IDTestAdequacy)
			}
		}
	}
}

func TestByID(t *testing.T) {
	if _, ok := ByID(IDSecurity); !ok {
		t.Error("ByID(security) = not found")
	}
	if _, ok := ByID(ID("nonexistent")); ok {
		t.Error("ByID(nonexistent) = found")
	}
}
