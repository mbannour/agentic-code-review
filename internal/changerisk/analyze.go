package changerisk

import (
	"regexp"
	"sort"
	"strings"
)

// Change is one changed file as risk analysis needs it.
type Change struct {
	Path      string
	Status    string
	Patch     string
	Additions int
	Deletions int
}

// Size thresholds that raise the risk band on breadth alone. A change touching a
// great many source files is risky for reasons no keyword captures: nobody can
// hold it all in mind at once.
const (
	broadChangeFiles     = 20
	broadChangeLines     = 800
	significantFileCount = 8
)

// maxSignalsPerArea bounds how many observations are recorded per area. The first
// few explain the assignment; the rest are the same statement repeated.
const maxSignalsPerArea = 3

// pathRule assigns an area from a path. Path signals are stronger than content
// signals: a file under `auth/` is authentication regardless of the diff text.
type pathRule struct {
	area    Area
	detail  string
	pattern *regexp.Regexp
}

// contentRule assigns an area from the text of changed lines.
type contentRule struct {
	area    Area
	detail  string
	pattern *regexp.Regexp
}

var pathRules = []pathRule{
	{AreaMigration, "migration file changed", regexp.MustCompile(`(?i)(^|/)(migrations?|flyway|liquibase|alembic)(/|$)|(^|/)v?\d+[_-].*\.sql$`)},
	{AreaAuthentication, "path names authentication", regexp.MustCompile(`(?i)(^|/)(auth|authn|login|session|oauth|oidc|saml|jwt|token|identity|credential)s?(/|_|\.|$)`)},
	{AreaAuthorization, "path names authorization", regexp.MustCompile(`(?i)(^|/)(authz|permission|role|rbac|acl|policy|policies|access|guard|entitlement)s?(/|_|\.|$)`)},
	{AreaPayments, "path names payments", regexp.MustCompile(`(?i)(^|/)(payment|billing|invoice|charge|capture|refund|settlement|payout|checkout|subscription|price|pricing|ledger)s?(/|_|\.|$)`)},
	{AreaDatabase, "path names persistence", regexp.MustCompile(`(?i)(^|/)(repository|repositories|dao|persistence|schema|model|entity|entities|query|queries|db|database|sql)s?(/|_|\.|$)`)},
	{AreaPublicAPI, "path names a public interface", regexp.MustCompile(`(?i)(^|/)(api|apis|openapi|swagger|proto|graphql|controller|controllers|handler|handlers|route|routes|endpoint|contract)s?(/|_|\.|$)|\.(proto|graphql|graphqls)$|openapi.*\.(ya?ml|json)$`)},
	{AreaCryptography, "path names cryptography", regexp.MustCompile(`(?i)(^|/)(crypto|cipher|encrypt|decrypt|signature|signing|hash|tls|ssl|certificate)s?(/|_|\.|$)`)},
	{AreaInfrastructure, "infrastructure definition changed", regexp.MustCompile(`(?i)(^|/)(\.github/workflows|deploy|deployment|helm|charts?|k8s|kubernetes|terraform|ansible|docker)(/|$)|(^|/)(dockerfile|docker-compose\.ya?ml)$|\.tf$`)},
	{AreaDependencies, "dependency manifest changed", regexp.MustCompile(`(?i)(^|/)(go\.mod|go\.sum|package\.json|package-lock\.json|yarn\.lock|pnpm-lock\.yaml|build\.sbt|requirements\.txt|poetry\.lock|gemfile(\.lock)?|cargo\.(toml|lock)|pom\.xml)$|(^|/)project/[^/]+\.sbt$`)},
	{AreaTests, "test file changed", regexp.MustCompile(`(?i)_test\.go$|(^|/)tests?(/|$)|src/test/|\.(test|spec)\.[jt]sx?$|(spec|test)\.scala$`)},
	{AreaDocumentation, "documentation changed", regexp.MustCompile(`(?i)\.(md|markdown|rst|adoc|txt)$|(^|/)docs?(/|$)`)},
	{AreaConfiguration, "configuration changed", regexp.MustCompile(`(?i)\.(ya?ml|toml|ini|properties|conf|env)$|(^|/)config(uration)?s?(/|$)|(^|/)\.env`)},
}

var contentRules = []contentRule{
	{AreaAuthentication, "changed lines mention authentication", regexp.MustCompile(`(?i)\b(authenticate|authentication|login|logout|session|jwt|bearer|oauth|password|credential|api[_-]?key|access[_-]?token|refresh[_-]?token)\b`)},
	{AreaAuthorization, "changed lines mention authorization", regexp.MustCompile(`(?i)\b(authorize|authorization|permission|forbidden|unauthorized|is[_-]?admin|has[_-]?role|can[_-]?access|require[_-]?role|tenant[_-]?id|owner[_-]?id|acl|rbac)\b`)},
	{AreaPayments, "changed lines mention payment handling", regexp.MustCompile(`(?i)\b(payment|capture|refund|settle|settlement|charge|invoice|payout|amount|currency|price|billing|idempotency[_-]?key)\b`)},
	{AreaDatabase, "changed lines contain database access", regexp.MustCompile(`(?i)\b(select\s+.*\s+from|insert\s+into|update\s+\w+\s+set|delete\s+from|create\s+table|alter\s+table|drop\s+table|begin\s+transaction|commit|rollback|\.query\(|\.exec\(|transaction\b)`)},
	{AreaMigration, "changed lines alter schema", regexp.MustCompile(`(?i)\b(alter\s+table|create\s+table|drop\s+(table|column)|add\s+column|rename\s+column|create\s+index|drop\s+index)\b`)},
	{AreaPublicAPI, "changed lines alter an interface or route", regexp.MustCompile(`(?i)(@(Get|Post|Put|Patch|Delete)Mapping|\b(router|route|app)\.(get|post|put|patch|delete)\b|@(app|router)\.(get|post)|\bhttp\.(Handle|HandleFunc)\b|\bpath\s*[:=]\s*"/|\boperationId\b)`)},
	// `go func` is the obvious form, but `go process(item)` launches a goroutine
	// just as surely, so a call preceded by `go` counts too.
	{AreaConcurrency, "changed lines involve concurrency", regexp.MustCompile(`(?i)(\bgo\s+(func\b|[\w.]+\()|\b(goroutine|sync\.(Mutex|RWMutex|WaitGroup|Once)|atomic\.|channel|Future|Promise\.all|threading|ExecutionContext)\b|\bselect\s*\{|\bZIO\.(fork|race|par)|\basync\s+def\b|\bawait\s+Promise)`)},
	{AreaCryptography, "changed lines involve cryptography", regexp.MustCompile(`(?i)\b(encrypt|decrypt|aes|rsa|hmac|sha1|sha256|md5|bcrypt|scrypt|argon2|rand\.Read|SecureRandom|tls\.Config|x509)\b`)},
	{AreaStateMachine, "changed lines alter a state transition", regexp.MustCompile(`(?i)\b(status|state)\s*(=|==|:=|\.set|=>)|\b(transition|state[_-]?machine)\b|\bstatus\s*(in|not\s+in)\b`)},
	{AreaSerialization, "changed lines alter serialization", regexp.MustCompile("(?i)\\b(json\\.(Marshal|Unmarshal)|Marshal|Unmarshal|Serializ|Deserializ|encoding/json|circe|Decoder|Encoder|toJson|fromJson|`json:)")},
	{AreaErrorHandling, "changed lines alter error or failure handling", regexp.MustCompile(`(?i)\b(try\s*\{|catch\b|except\b|recover\(\)|panic\(|throw\b|raise\b|retry|timeout|deadline|fallback|circuit[_-]?breaker)\b`)},
}

// Analyzer produces a risk profile from a change. It is deterministic and holds no
// state between calls.
type Analyzer struct{}

// NewAnalyzer returns an Analyzer.
func NewAnalyzer() Analyzer { return Analyzer{} }

// Analyze classifies a change.
//
// Path signals are gathered first and content signals second, because a path is
// the more reliable statement about what a file is for. Content signals are read
// only from changed lines: matching context lines would classify a change by the
// file it happens to live in.
func (Analyzer) Analyze(changes []Change) Profile {
	profile := Profile{Level: LevelMinimal, ChangedFiles: len(changes)}
	if len(changes) == 0 {
		return profile
	}

	seen := map[Area]int{}
	add := func(signal Signal) {
		if seen[signal.Area] >= maxSignalsPerArea {
			return
		}
		seen[signal.Area]++
		profile.Signals = append(profile.Signals, signal)
	}

	for _, change := range changes {
		path := strings.TrimSpace(strings.ReplaceAll(change.Path, "\\", "/"))
		if path == "" {
			continue
		}
		profile.Additions += change.Additions
		profile.Deletions += change.Deletions

		pathAreas := map[Area]bool{}
		for _, rule := range pathRules {
			if rule.pattern.MatchString(path) {
				pathAreas[rule.area] = true
				add(Signal{Area: rule.area, Path: path, Detail: rule.detail, FromPath: true})
			}
		}
		if isSourceFile(path, pathAreas) {
			profile.SourceFiles++
		}

		changed := changedLines(change.Patch)
		if changed == "" {
			continue
		}
		for _, rule := range contentRules {
			// A path signal already says this; a second observation of the same
			// area adds cost and no information.
			if pathAreas[rule.area] {
				continue
			}
			if rule.pattern.MatchString(changed) {
				add(Signal{Area: rule.area, Path: path, Detail: rule.detail})
			}
		}
	}

	profile.Areas = orderedAreas(profile.Signals)
	profile.Level = level(profile)
	return profile
}

// isSourceFile reports whether a path is production code rather than a test,
// document, or configuration file.
func isSourceFile(path string, areas map[Area]bool) bool {
	if areas[AreaTests] || areas[AreaDocumentation] || areas[AreaDependencies] {
		return false
	}
	for _, ext := range []string{
		".go", ".scala", ".sc", ".js", ".jsx", ".mjs", ".cjs",
		".ts", ".tsx", ".mts", ".cts", ".py", ".rb", ".java", ".kt", ".rs", ".sql",
	} {
		if strings.HasSuffix(strings.ToLower(path), ext) {
			return true
		}
	}
	return false
}

// changedLines returns only the added and removed lines of a patch.
func changedLines(patch string) string {
	if patch == "" {
		return ""
	}

	var out strings.Builder
	for _, line := range strings.Split(patch, "\n") {
		if len(line) == 0 {
			continue
		}
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if line[0] != '+' && line[0] != '-' {
			continue
		}
		out.WriteString(line[1:])
		out.WriteString("\n")
	}
	return out.String()
}

// orderedAreas returns the distinct areas in Areas() order.
func orderedAreas(signals []Signal) []Area {
	present := map[Area]bool{}
	for _, signal := range signals {
		present[signal.Area] = true
	}

	var areas []Area
	for _, area := range Areas() {
		if present[area] {
			areas = append(areas, area)
		}
	}
	return areas
}

// sensitiveAreas are the areas whose failure modes are expensive: money, access
// control, stored data, and published contracts.
func sensitiveAreas() []Area {
	return []Area{
		AreaAuthentication, AreaAuthorization, AreaPayments,
		AreaMigration, AreaCryptography, AreaPublicAPI,
	}
}

// supportingAreas are areas that raise attention without being decisive alone.
func supportingAreas() []Area {
	return []Area{
		AreaDatabase, AreaConcurrency, AreaStateMachine,
		AreaSerialization, AreaDependencies, AreaInfrastructure, AreaErrorHandling,
	}
}

// level decides the overall band.
//
// The rules are deliberately coarse and stated in one place. A fabricated score
// would imply a precision these signals do not have; a band a developer can
// predict is more useful than a number they cannot.
func level(profile Profile) Level {
	// A change with no production code in it cannot break production behaviour,
	// however many keywords its documentation contains.
	if profile.SourceFiles == 0 {
		if profile.HasAnyArea(AreaDependencies, AreaInfrastructure, AreaMigration) {
			return LevelMedium
		}
		if profile.HasArea(AreaTests) {
			return LevelLow
		}
		return LevelMinimal
	}

	broad := profile.ChangedFiles >= broadChangeFiles ||
		profile.Additions+profile.Deletions >= broadChangeLines

	if profile.HasAnyArea(sensitiveAreas()...) {
		// A sensitive area with reach, or with a second sensitive area alongside
		// it, is the case worth spending the most attention on.
		if broad || profile.SourceFiles >= significantFileCount || countAreas(profile, sensitiveAreas()) > 1 {
			return LevelHigh
		}
		return LevelMedium
	}
	if profile.HasAnyArea(supportingAreas()...) || broad {
		return LevelMedium
	}
	return LevelLow
}

func countAreas(profile Profile, areas []Area) int {
	count := 0
	for _, area := range areas {
		if profile.HasArea(area) {
			count++
		}
	}
	return count
}

// SortSignals orders signals by area then path, for stable reporting. Analyze
// already emits them in file order; this is for callers assembling their own view.
func SortSignals(signals []Signal) {
	sort.SliceStable(signals, func(i, j int) bool {
		if signals[i].Area != signals[j].Area {
			return signals[i].Area < signals[j].Area
		}
		return signals[i].Path < signals[j].Path
	})
}
