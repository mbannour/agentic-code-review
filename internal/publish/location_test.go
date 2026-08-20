package publish

import (
	"testing"

	"github.com/your-company/agentic-code-review/internal/findings"
	"github.com/your-company/agentic-code-review/internal/github"
)

// TestMapLocations covers the whole mapping contract on the reference diff: which
// side a line lands on, when a range is allowed, and when the answer is "nowhere".
func TestMapLocations(t *testing.T) {
	mapper := mapperFixture()

	tests := []struct {
		name      string
		file      string
		start     int
		end       int
		wantOK    bool
		wantLine  int
		wantSide  string
		wantStart int
		wantSideS string
		wantWhy   string
	}{
		{
			name: "added line maps to the right side",
			file: testFile, start: 82, end: 82,
			wantOK: true, wantLine: 82, wantSide: github.SideRight,
		},
		{
			name: "first added line of the hunk",
			file: testFile, start: 81, end: 81,
			wantOK: true, wantLine: 81, wantSide: github.SideRight,
		},
		{
			name: "first line of the hunk is context on the right",
			file: testFile, start: 80, end: 80,
			wantOK: true, wantLine: 80, wantSide: github.SideRight,
		},
		{
			name: "last line of the hunk",
			file: testFile, start: 86, end: 86,
			wantOK: true, wantLine: 86, wantSide: github.SideRight,
		},
		{
			name: "context line maps to the right side",
			file: testFile, start: 85, end: 85,
			wantOK: true, wantLine: 85, wantSide: github.SideRight,
		},
		{
			name: "multiline range over added lines",
			file: testFile, start: 81, end: 83,
			wantOK: true, wantLine: 83, wantSide: github.SideRight,
			wantStart: 81, wantSideS: github.SideRight,
		},
		{
			// 84 is in the diff but 87 is past the hunk, so the range cannot be
			// used. Falling back to the start line is the only honest answer.
			name: "range that leaves the hunk falls back to one line",
			file: testFile, start: 84, end: 87,
			wantOK: true, wantLine: 84, wantSide: github.SideRight,
		},
		{
			name: "second hunk of a multi-hunk patch",
			file: testFileTest, start: 202, end: 202,
			wantOK: true, wantLine: 202, wantSide: github.SideRight,
		},
		{
			name: "line between hunks is not in the diff",
			file: testFileTest, start: 120, end: 120,
			wantWhy: ReasonLineNotInDiff,
		},
		{
			name: "line past the end of the patch",
			file: testFile, start: 900, end: 900,
			wantWhy: ReasonLineNotInDiff,
		},
		{
			name: "file without a patch",
			file: "internal/payment/ledger.pdf", start: 1, end: 1,
			wantWhy: ReasonNoPatch,
		},
		{
			name: "file outside the diff",
			file: "internal/other/thing.go", start: 1, end: 1,
			wantWhy: ReasonFileNotInDiff,
		},
		{
			name: "path written with a leading dot slash still matches",
			file: "./" + testFile, start: 82, end: 82,
			wantOK: true, wantLine: 82, wantSide: github.SideRight,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := findingFixture()
			f.File = tt.file
			f.StartLine = tt.start
			f.EndLine = tt.end

			location, ok, why := mapper.Map(f)

			if ok != tt.wantOK {
				t.Fatalf("Map() ok = %t, want %t (reason %q)", ok, tt.wantOK, why)
			}
			if !tt.wantOK {
				if why != tt.wantWhy {
					t.Errorf("reason = %q, want %q", why, tt.wantWhy)
				}
				return
			}

			if location.Line != tt.wantLine {
				t.Errorf("Line = %d, want %d", location.Line, tt.wantLine)
			}
			if location.Side != tt.wantSide {
				t.Errorf("Side = %q, want %q", location.Side, tt.wantSide)
			}
			if location.StartLine != tt.wantStart {
				t.Errorf("StartLine = %d, want %d", location.StartLine, tt.wantStart)
			}
			if location.StartSide != tt.wantSideS {
				t.Errorf("StartSide = %q, want %q", location.StartSide, tt.wantSideS)
			}
			if tt.wantStart > 0 && !location.Multiline() {
				t.Error("Multiline() = false, want true for a range location")
			}
		})
	}
}

// TestMapDeletedLineUsesLeftSide checks that a line only the old version has is
// commented on the left. Commenting it on the right would attach the finding to
// unrelated code.
func TestMapDeletedLineUsesLeftSide(t *testing.T) {
	// The hunk is deliberately offset — old lines 10-12, new lines 100-101 — so
	// that line 11 exists only on the left. In an aligned hunk the same number
	// would also be a context line on the right, and the right side wins.
	mapper := NewMapper([]DiffFile{{
		Path:   testFile,
		Status: "modified",
		Patch: `@@ -10,3 +100,2 @@
 keep
-removed line
 keep
`,
	}})

	f := findingFixture()
	f.StartLine, f.EndLine = 11, 11

	location, ok, why := mapper.Map(f)
	if !ok {
		t.Fatalf("Map() ok = false, reason %q", why)
	}
	if location.Side != github.SideLeft {
		t.Errorf("Side = %q, want %q for a deleted line", location.Side, github.SideLeft)
	}
	if location.Line != 11 {
		t.Errorf("Line = %d, want 11", location.Line)
	}
}

// TestMapDeletedFile checks a wholly removed file maps against the old version.
func TestMapDeletedFile(t *testing.T) {
	mapper := mapperFixture()

	f := findingFixture()
	f.File = "internal/legacy/old.go"
	f.StartLine, f.EndLine = 3, 3

	location, ok, why := mapper.Map(f)
	if !ok {
		t.Fatalf("Map() ok = false, reason %q", why)
	}
	if location.Side != github.SideLeft {
		t.Errorf("Side = %q, want %q for a deleted file", location.Side, github.SideLeft)
	}
}

// TestMapRenamedFile checks a rename with a patch maps normally: GitHub reports the
// new path, and that is the path a comment must use.
func TestMapRenamedFile(t *testing.T) {
	mapper := NewMapperFromChangedFiles([]github.ChangedFile{{
		Filename: "internal/payment/charge.go",
		Status:   "renamed",
		Patch: `@@ -1,3 +1,4 @@
 package payment
+// moved from billing

`,
	}})

	f := findingFixture()
	f.File = "internal/payment/charge.go"
	f.StartLine, f.EndLine = 2, 2

	location, ok, why := mapper.Map(f)
	if !ok {
		t.Fatalf("Map() ok = false, reason %q", why)
	}
	if location.Path != "internal/payment/charge.go" || location.Side != github.SideRight {
		t.Errorf("location = %+v, want the new path on the right side", location)
	}
}

// TestMapRenamedWithoutPatch checks a pure rename, which GitHub reports with no
// patch at all, is unmappable rather than mapped to line 1.
func TestMapRenamedWithoutPatch(t *testing.T) {
	mapper := NewMapperFromChangedFiles([]github.ChangedFile{
		{Filename: "internal/payment/charge.go", Status: "renamed", Patch: ""},
	})

	f := findingFixture()
	f.File = "internal/payment/charge.go"
	f.StartLine, f.EndLine = 1, 1

	if _, ok, why := mapper.Map(f); ok || why != ReasonNoPatch {
		t.Errorf("Map() ok = %t reason = %q, want false and %q", ok, why, ReasonNoPatch)
	}
}

// TestMapTruncatedPatch checks a patch cut mid-diff maps what it still contains and
// refuses everything beyond it. A truncated diff is a gap in our evidence, not
// permission to guess.
func TestMapTruncatedPatch(t *testing.T) {
	mapper := NewMapper([]DiffFile{{
		Path:      testFile,
		Status:    "modified",
		Truncated: true,
		Patch: `@@ -80,5 +80,7 @@
 func RetryPayment(p Payment) error {
+	if permanent {
[TRUNCATED: patch exceeded the budget]`,
	}})

	f := findingFixture()
	f.StartLine, f.EndLine = 81, 81
	if _, ok, why := mapper.Map(f); !ok {
		t.Errorf("Map() on a retained line: ok = false, reason %q", why)
	}

	f.StartLine, f.EndLine = 200, 200
	if _, ok, why := mapper.Map(f); ok || why != ReasonLineNotInDiff {
		t.Errorf("Map() beyond the truncation: ok = %t reason = %q, want false and %q",
			ok, why, ReasonLineNotInDiff)
	}
}

// TestMapMalformedHunk checks an unparseable hunk header maps nothing rather than
// counting lines from an invented starting point.
func TestMapMalformedHunk(t *testing.T) {
	tests := []struct {
		name  string
		patch string
	}{
		{name: "no numbers", patch: "@@ -a,b +c,d @@\n+added\n"},
		{name: "missing new range", patch: "@@ -80,5 @@\n+added\n"},
		{name: "not a diff", patch: "this is not a diff at all\n"},
		{name: "signs swapped", patch: "@@ +80,5 -80,7 @@\n+added\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper := NewMapper([]DiffFile{{Path: testFile, Status: "modified", Patch: tt.patch}})

			f := findingFixture()
			for line := 1; line <= 100; line++ {
				f.StartLine, f.EndLine = line, line
				if _, ok, _ := mapper.Map(f); ok {
					t.Fatalf("Map() mapped line %d from a malformed patch", line)
				}
			}
		})
	}
}

// TestMapNeverInventsLines is the property that matters most: every location the
// mapper returns must be a line the patch actually contains.
func TestMapNeverInventsLines(t *testing.T) {
	mapper := mapperFixture()

	for _, file := range []string{testFile, testFileTest, "internal/legacy/old.go"} {
		for line := 1; line <= 300; line++ {
			f := findingFixture()
			f.File = file
			f.StartLine, f.EndLine = line, line

			location, ok, _ := mapper.Map(f)
			if !ok {
				continue
			}
			if location.Line != line {
				t.Fatalf("Map(%s:%d) returned line %d; a mapped line must be the requested one",
					file, line, location.Line)
			}
			if location.Side != github.SideRight && location.Side != github.SideLeft {
				t.Fatalf("Map(%s:%d) returned side %q", file, line, location.Side)
			}
		}
	}
}

// TestMapFromSelection checks the selection-based constructor, which is the fallback
// when the raw changed-file listing is not available.
func TestMapFromSelection(t *testing.T) {
	mapper := NewMapperFromSelection(selectionFixture())

	f := findingFixture()
	f.StartLine, f.EndLine = 82, 82

	if _, ok, why := mapper.Map(f); !ok {
		t.Errorf("Map() ok = false, reason %q", why)
	}
}

// TestLocationDescribe checks the diagnostic rendering, which appears in dry-run
// output and in publication errors.
func TestLocationDescribe(t *testing.T) {
	single := DiffLocation{Path: testFile, Line: 84, Side: github.SideRight}
	if got, want := single.Describe(), testFile+":84 RIGHT"; got != want {
		t.Errorf("Describe() = %q, want %q", got, want)
	}

	multi := DiffLocation{Path: testFile, Line: 83, Side: github.SideRight, StartLine: 81, StartSide: github.SideRight}
	if got, want := multi.Describe(), testFile+":81-83 RIGHT"; got != want {
		t.Errorf("Describe() = %q, want %q", got, want)
	}
}

// TestLocationComment checks conversion to the API type, including that a
// single-line location leaves the range fields unset.
func TestLocationComment(t *testing.T) {
	single := DiffLocation{Path: testFile, Line: 84, Side: github.SideRight}.Comment("body")
	if single.StartLine != nil || single.StartSide != nil {
		t.Errorf("single-line comment = %+v, want no range fields", single)
	}
	if single.Multiline() {
		t.Error("Multiline() = true for a single-line comment")
	}

	multi := DiffLocation{Path: testFile, Line: 83, Side: github.SideRight, StartLine: 81, StartSide: github.SideRight}.Comment("body")
	if multi.StartLine == nil || *multi.StartLine != 81 ||
		multi.StartSide == nil || *multi.StartSide != github.SideRight || !multi.Multiline() {
		t.Errorf("multiline comment = %+v, want a populated range", multi)
	}
}

// TestMapperFiles checks the diagnostic listing is sorted and complete.
func TestMapperFiles(t *testing.T) {
	got := mapperFixture().Files()
	want := []string{
		"internal/legacy/old.go",
		"internal/payment/ledger.pdf",
		testFile,
		testFileTest,
	}

	if len(got) != len(want) {
		t.Fatalf("Files() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Files()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestMapZeroLineFinding checks a structurally impossible location — which
// validation should already have rejected — is refused rather than mapped.
func TestMapZeroLineFinding(t *testing.T) {
	f := findings.Finding{File: testFile, StartLine: 0, EndLine: 0}
	if _, ok, _ := mapperFixture().Map(f); ok {
		t.Error("Map() mapped a finding with no line number")
	}
}
