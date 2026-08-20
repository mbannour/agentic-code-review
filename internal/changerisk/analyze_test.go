package changerisk

import (
	"strings"
	"testing"
)

func patch(lines ...string) string {
	var out strings.Builder
	out.WriteString("@@ -1,5 +1,9 @@\n")
	for _, line := range lines {
		out.WriteString(line + "\n")
	}
	return out.String()
}

func analyze(changes ...Change) Profile { return NewAnalyzer().Analyze(changes) }

func TestPathSignalsClassifyByPurpose(t *testing.T) {
	cases := []struct {
		path string
		want Area
	}{
		{"internal/auth/middleware.go", AreaAuthentication},
		{"src/main/scala/authz/PolicyCheck.scala", AreaAuthorization},
		{"internal/payments/capture.go", AreaPayments},
		{"db/migrations/0007_add_status.sql", AreaMigration},
		{"api/openapi.yaml", AreaPublicAPI},
		{"internal/crypto/sign.go", AreaCryptography},
		{"Dockerfile", AreaInfrastructure},
		{"go.mod", AreaDependencies},
		{"internal/payments/capture_test.go", AreaTests},
		{"docs/architecture.md", AreaDocumentation},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			profile := analyze(Change{Path: tc.path, Patch: patch("+something changed")})
			if !profile.HasArea(tc.want) {
				t.Errorf("areas = %v, want %s", profile.Areas, tc.want)
			}
			for _, signal := range profile.SignalsFor(tc.want) {
				if signal.Path != tc.path {
					t.Errorf("signal path = %q, want %q", signal.Path, tc.path)
				}
			}
		})
	}
}

func TestContentSignalsClassifyChangedLines(t *testing.T) {
	cases := []struct {
		name string
		line string
		want Area
	}{
		{"authorization", "+  if !user.HasRole(\"admin\") { return Forbidden }", AreaAuthorization},
		{"payments", "+  capture(payment.Amount, idempotencyKey)", AreaPayments},
		{"database", "+  rows, err := db.Query(\"SELECT id FROM accounts\")", AreaDatabase},
		{"concurrency", "+  go func() { mu.Lock() }()", AreaConcurrency},
		{"state machine", "+  order.status = \"shipped\"", AreaStateMachine},
		{"serialization", "+  json.Unmarshal(body, &request)", AreaSerialization},
		{"error handling", "+  return retry(ctx, attempt+1)", AreaErrorHandling},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A neutral path, so only the content can produce the area.
			profile := analyze(Change{Path: "internal/thing/thing.go", Patch: patch(tc.line)})
			if !profile.HasArea(tc.want) {
				t.Errorf("areas = %v, want %s", profile.Areas, tc.want)
			}
		})
	}
}

// Context lines describe the file, not the change. Classifying by them would make
// every change inherit the risk of its neighbourhood.
func TestContextLinesDoNotProduceSignals(t *testing.T) {
	profile := analyze(Change{
		Path: "internal/thing/thing.go",
		Patch: "@@ -1,5 +1,6 @@\n" +
			" if !user.HasRole(\"admin\") { return Forbidden }\n" +
			" capture(payment.Amount)\n" +
			"+return nil\n",
	})

	if profile.HasArea(AreaAuthorization) || profile.HasArea(AreaPayments) {
		t.Errorf("areas = %v, want none from context lines", profile.Areas)
	}
}

// A path signal is the stronger statement; repeating it from content adds cost
// and no information.
func TestPathSignalSuppressesTheSameAreaFromContent(t *testing.T) {
	profile := analyze(Change{
		Path:  "internal/auth/session.go",
		Patch: patch("+  session := newSession(token)"),
	})

	signals := profile.SignalsFor(AreaAuthentication)
	if len(signals) != 1 {
		t.Fatalf("signals = %+v, want exactly one", signals)
	}
	if !signals[0].FromPath {
		t.Error("the retained signal is not the path signal")
	}
}

func TestLevels(t *testing.T) {
	cases := []struct {
		name   string
		change []Change
		want   Level
	}{
		{
			name:   "documentation only",
			change: []Change{{Path: "README.md", Patch: patch("+prose")}},
			want:   LevelMinimal,
		},
		{
			name:   "tests only",
			change: []Change{{Path: "internal/app/app_test.go", Patch: patch("+func TestThing(t *testing.T) {}")}},
			want:   LevelLow,
		},
		{
			name:   "ordinary source",
			change: []Change{{Path: "internal/format/format.go", Patch: patch("+  return strings.TrimSpace(s)")}},
			want:   LevelLow,
		},
		{
			name:   "supporting area only",
			change: []Change{{Path: "internal/worker/worker.go", Patch: patch("+  go process(item)")}},
			want:   LevelMedium,
		},
		{
			name:   "one sensitive area, narrow",
			change: []Change{{Path: "internal/auth/token.go", Patch: patch("+  return verify(token)"), Additions: 3}},
			want:   LevelMedium,
		},
		{
			name: "two sensitive areas",
			change: []Change{
				{Path: "internal/auth/token.go", Patch: patch("+  return verify(token)")},
				{Path: "internal/payments/capture.go", Patch: patch("+  capture(amount)")},
			},
			want: LevelHigh,
		},
		{
			name: "sensitive area with reach",
			change: []Change{{
				Path: "internal/payments/capture.go", Patch: patch("+  capture(amount)"),
				Additions: 700, Deletions: 200,
			}},
			want: LevelHigh,
		},
		{
			name:   "dependency manifest with no source",
			change: []Change{{Path: "go.mod", Patch: patch("+require example.com/x v1.2.3")}},
			want:   LevelMedium,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewAnalyzer().Analyze(tc.change).Level; got != tc.want {
				t.Errorf("level = %s, want %s", got, tc.want)
			}
		})
	}
}

// A change with no production code cannot break production behaviour, however
// many sensitive-sounding words its documentation contains.
func TestDocumentationAboutPaymentsIsNotAPaymentChange(t *testing.T) {
	profile := analyze(Change{
		Path:  "docs/payments.md",
		Patch: patch("+The capture endpoint refunds the payment amount."),
	})

	if profile.SourceFiles != 0 {
		t.Errorf("source files = %d, want 0", profile.SourceFiles)
	}
	if profile.Level != LevelMinimal {
		t.Errorf("level = %s, want minimal", profile.Level)
	}
}

func TestAnalysisIsDeterministic(t *testing.T) {
	changes := []Change{
		{Path: "internal/auth/middleware.go", Patch: patch("+  if !authorized(user) { return }"), Additions: 4},
		{Path: "internal/payments/capture.go", Patch: patch("+  emitCaptureEvent(payment)"), Additions: 9},
		{Path: "db/migrations/0007.sql", Patch: patch("+ALTER TABLE payments ADD COLUMN status text;")},
	}

	first := NewAnalyzer().Analyze(changes)
	for i := 0; i < 5; i++ {
		again := NewAnalyzer().Analyze(changes)
		if strings.Join(areaStrings(again.Areas), ",") != strings.Join(areaStrings(first.Areas), ",") {
			t.Fatalf("areas changed between runs: %v then %v", first.Areas, again.Areas)
		}
		if again.Level != first.Level {
			t.Fatalf("level changed between runs: %s then %s", first.Level, again.Level)
		}
		if len(again.Signals) != len(first.Signals) {
			t.Fatalf("signal count changed between runs")
		}
	}
}

func areaStrings(areas []Area) []string {
	out := make([]string, 0, len(areas))
	for _, area := range areas {
		out = append(out, string(area))
	}
	return out
}

// Areas are reported in a fixed order so output never depends on map iteration.
func TestAreasAreReportedInRegistryOrder(t *testing.T) {
	profile := analyze(
		Change{Path: "docs/x.md", Patch: patch("+prose")},
		Change{Path: "internal/auth/x.go", Patch: patch("+verify()")},
		Change{Path: "internal/payments/y.go", Patch: patch("+capture()")},
	)

	positions := map[Area]int{}
	for i, area := range Areas() {
		positions[area] = i
	}
	for i := 1; i < len(profile.Areas); i++ {
		if positions[profile.Areas[i-1]] > positions[profile.Areas[i]] {
			t.Errorf("areas out of registry order: %v", profile.Areas)
		}
	}
}

func TestSignalsPerAreaAreBounded(t *testing.T) {
	var changes []Change
	for i := 0; i < 20; i++ {
		changes = append(changes, Change{
			Path:  "internal/auth/file" + string(rune('a'+i)) + ".go",
			Patch: patch("+  verify(token)"),
		})
	}

	profile := NewAnalyzer().Analyze(changes)
	if got := len(profile.SignalsFor(AreaAuthentication)); got > maxSignalsPerArea {
		t.Errorf("signals = %d, want at most %d", got, maxSignalsPerArea)
	}
}

func TestEmptyChangeIsMinimal(t *testing.T) {
	profile := NewAnalyzer().Analyze(nil)
	if profile.Level != LevelMinimal || !profile.Empty() {
		t.Errorf("profile = %+v, want an empty minimal profile", profile)
	}
}

// A binary or oversized file arrives with no patch. It must classify by path and
// not panic on the absent content.
func TestFileWithoutAPatchStillClassifiesByPath(t *testing.T) {
	profile := analyze(Change{Path: "internal/payments/capture.go", Status: "modified"})

	if !profile.HasArea(AreaPayments) {
		t.Errorf("areas = %v, want payments from the path alone", profile.Areas)
	}
}
