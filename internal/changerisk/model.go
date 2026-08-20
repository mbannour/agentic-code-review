// Package changerisk classifies what a change touches, deterministically.
//
// It exists to answer one question before anything expensive happens: which parts
// of the system could this change break? The answer decides which specialist
// reviewers are worth running, so it must be cheap, explainable, and reproducible
// — no model is involved, and the same diff always produces the same profile.
//
// The signals are lexical: paths, and the text of changed lines. That is a
// deliberate limit. A profile says "this change touches something that looks like
// authorization", never "this change has an authorization bug". It routes
// attention; it does not make claims, and nothing downstream may treat an area as
// evidence of a defect.
package changerisk

import "strings"

// Area is a part of the system a change can touch. The set is closed: an
// unrecognized signal produces no area rather than a guess.
type Area string

const (
	AreaAuthentication Area = "authentication"
	AreaAuthorization  Area = "authorization"
	AreaPayments       Area = "payments"
	AreaDatabase       Area = "database"
	AreaMigration      Area = "migration"
	AreaPublicAPI      Area = "public_api"
	AreaConfiguration  Area = "configuration"
	AreaConcurrency    Area = "concurrency"
	AreaCryptography   Area = "cryptography"
	AreaDependencies   Area = "dependencies"
	AreaInfrastructure Area = "infrastructure"
	AreaStateMachine   Area = "state_machine"
	AreaSerialization  Area = "serialization"
	AreaErrorHandling  Area = "error_handling"
	AreaTests          Area = "tests"
	AreaDocumentation  Area = "documentation"
)

// Areas lists every area in a stable order, so output never depends on map
// iteration.
func Areas() []Area {
	return []Area{
		AreaAuthentication, AreaAuthorization, AreaPayments, AreaDatabase,
		AreaMigration, AreaPublicAPI, AreaConfiguration, AreaConcurrency,
		AreaCryptography, AreaDependencies, AreaInfrastructure, AreaStateMachine,
		AreaSerialization, AreaErrorHandling, AreaTests, AreaDocumentation,
	}
}

// Valid reports whether a is a recognized area.
func (a Area) Valid() bool {
	for _, allowed := range Areas() {
		if a == allowed {
			return true
		}
	}
	return false
}

// Display renders the area for terminal output.
func (a Area) Display() string { return strings.ToUpper(strings.ReplaceAll(string(a), "_", " ")) }

// Level is the overall risk band. It orders the review budget, nothing else: a
// high-risk change is one worth spending more attention on, not one presumed
// broken.
type Level string

const (
	// LevelMinimal is documentation, or nothing recognizable at all.
	LevelMinimal Level = "minimal"

	// LevelLow is ordinary code with no sensitive area touched.
	LevelLow Level = "low"

	// LevelMedium is a sensitive area, or a broad change.
	LevelMedium Level = "medium"

	// LevelHigh is a sensitive area with reach: money, access control, schema,
	// or a public contract.
	LevelHigh Level = "high"
)

// Levels lists the bands from lowest to highest.
func Levels() []Level { return []Level{LevelMinimal, LevelLow, LevelMedium, LevelHigh} }

// Rank orders the bands, higher meaning more risk.
func (l Level) Rank() int {
	for i, level := range Levels() {
		if l == level {
			return i
		}
	}
	return 0
}

// AtLeast reports whether l is at least as risky as other.
func (l Level) AtLeast(other Level) bool { return l.Rank() >= other.Rank() }

// Display renders the level for terminal output.
func (l Level) Display() string { return strings.ToUpper(string(l)) }

// Signal is one concrete reason an area was assigned.
//
// Every area must be traceable to a signal, and every signal names the file that
// produced it. A profile a developer cannot argue with is a profile they cannot
// trust.
type Signal struct {
	Area Area

	// Path is the changed file the signal came from.
	Path string

	// Detail states what was observed, in plain language.
	Detail string

	// FromPath reports that the signal came from the file's path rather than from
	// its content. Path signals are the stronger of the two: a file living in
	// `auth/` is authentication regardless of what the diff says.
	FromPath bool
}

// Profile is the deterministic risk assessment of one change.
type Profile struct {
	Level Level

	// Areas are the areas touched, in Areas() order.
	Areas []Area

	// Signals are the observations behind those areas, in file order.
	Signals []Signal

	// ChangedFiles, Additions, and Deletions describe the change's size, which
	// feeds the level but is also worth reporting on its own.
	ChangedFiles int
	Additions    int
	Deletions    int

	// SourceFiles is how many changed files are source rather than test,
	// documentation, or configuration.
	SourceFiles int
}

// HasArea reports whether the profile includes an area.
func (p Profile) HasArea(area Area) bool {
	for _, a := range p.Areas {
		if a == area {
			return true
		}
	}
	return false
}

// HasAnyArea reports whether the profile includes at least one of the areas.
func (p Profile) HasAnyArea(areas ...Area) bool {
	for _, area := range areas {
		if p.HasArea(area) {
			return true
		}
	}
	return false
}

// SignalsFor returns the signals that produced an area, in order.
func (p Profile) SignalsFor(area Area) []Signal {
	var out []Signal
	for _, signal := range p.Signals {
		if signal.Area == area {
			out = append(out, signal)
		}
	}
	return out
}

// Reasons summarizes why each area was assigned, one line per area, using the
// first signal for each. It is what the terminal and the routing explanation print.
func (p Profile) Reasons() []string {
	var reasons []string
	for _, area := range p.Areas {
		signals := p.SignalsFor(area)
		if len(signals) == 0 {
			continue
		}
		reasons = append(reasons, string(area)+": "+signals[0].Detail)
	}
	return reasons
}

// Empty reports whether nothing was classified.
func (p Profile) Empty() bool { return len(p.Areas) == 0 }
