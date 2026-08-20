package contextselect

import (
	"strings"
	"testing"
)

func TestDefaultBudget(t *testing.T) {
	b := DefaultBudget()

	tests := []struct {
		name string
		got  int
		want int
	}{
		{name: "total", got: b.Total, want: 200 * 1024},
		{name: "rules", got: b.Rules, want: 30 * 1024},
		{name: "analysis", got: b.Analysis, want: 30 * 1024},
		{name: "ticket description", got: b.TicketDescription, want: 16 * 1024},
		{name: "per check output", got: b.PerCheckOutput, want: 8 * 1024},
	}

	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %d, want %d", tt.name, tt.got, tt.want)
		}
	}

	if b.Rules+b.Analysis+b.TicketDescription >= b.Total {
		t.Errorf("fixed allowances (%d) leave nothing for patches out of %d",
			b.Rules+b.Analysis+b.TicketDescription, b.Total)
	}
}

func TestBudgetNormalized(t *testing.T) {
	tests := []struct {
		name  string
		given Budget
		check func(t *testing.T, got Budget)
	}{
		{
			name:  "zero budget falls back to the defaults",
			given: Budget{},
			check: func(t *testing.T, got Budget) {
				if got != DefaultBudget() {
					t.Errorf("normalized() = %+v, want the defaults", got)
				}
			},
		},
		{
			name:  "explicit values are kept",
			given: Budget{Total: 1000, Rules: 100, Analysis: 200, TicketDescription: 50, PerCheckOutput: 40},
			check: func(t *testing.T, got Budget) {
				if got.Total != 1000 || got.Rules != 100 || got.Analysis != 200 {
					t.Errorf("normalized() = %+v, want the given values", got)
				}
			},
		},
		{
			name:  "a section larger than the total is clamped",
			given: Budget{Total: 1000, Rules: 5000, Analysis: 9000, TicketDescription: 4000},
			check: func(t *testing.T, got Budget) {
				for name, value := range map[string]int{"rules": got.Rules, "analysis": got.Analysis, "ticket": got.TicketDescription} {
					if value > got.Total {
						t.Errorf("%s allowance %d exceeds the total %d", name, value, got.Total)
					}
				}
			},
		},
		{
			name:  "per-check output cannot exceed the analysis allowance",
			given: Budget{Total: 10000, Analysis: 500, PerCheckOutput: 9000},
			check: func(t *testing.T, got Budget) {
				if got.PerCheckOutput > got.Analysis {
					t.Errorf("PerCheckOutput = %d, want at most Analysis = %d", got.PerCheckOutput, got.Analysis)
				}
			},
		},
		{
			name:  "negative values fall back to the defaults",
			given: Budget{Total: -1, Rules: -1, Analysis: -1, TicketDescription: -1, PerCheckOutput: -1},
			check: func(t *testing.T, got Budget) {
				if got != DefaultBudget() {
					t.Errorf("normalized() = %+v, want the defaults", got)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, tt.given.normalized())
		})
	}
}

func TestTruncateTo(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		limit         int
		wantTruncated bool
		wantExact     string
	}{
		{name: "empty content", content: "", limit: 100},
		{name: "content within the limit", content: "hello", limit: 100, wantExact: "hello"},
		{name: "content exactly at the limit", content: "hello", limit: 5, wantExact: "hello"},
		{name: "content one byte over", content: "hello", limit: 4, wantTruncated: true},
		{name: "content far over", content: strings.Repeat("x", 1000), limit: 10, wantTruncated: true},
		{name: "zero limit with content", content: "hello", limit: 0, wantTruncated: true, wantExact: MarkerPatch},
		{name: "zero limit with no content", content: "", limit: 0},
		{name: "negative limit", content: "hello", limit: -5, wantTruncated: true, wantExact: MarkerPatch},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got, truncated := truncateTo(tt.content, tt.limit, MarkerPatch)

			if truncated != tt.wantTruncated {
				t.Errorf("truncated = %t, want %t", truncated, tt.wantTruncated)
			}
			if tt.wantExact != "" && got != tt.wantExact {
				t.Errorf("content = %q, want %q", got, tt.wantExact)
			}
			if !tt.wantTruncated && strings.Contains(got, MarkerPatch) {
				t.Error("a marker was added to content that fit")
			}
			if tt.wantTruncated && tt.limit > 0 && !strings.Contains(got, MarkerPatch) {
				t.Errorf("content = %q, want it to carry the marker", got)
			}
			if tt.wantTruncated && tt.limit > 0 && !strings.HasPrefix(got, tt.content[:tt.limit]) {
				t.Error("truncated content does not start with the original text")
			}
		})
	}
}

func TestTruncateToIsDeterministic(t *testing.T) {
	content := strings.Repeat("patch line\n", 5000)

	first, _ := truncateTo(content, 1024, MarkerPatch)
	for i := 0; i < 5; i++ {
		got, _ := truncateTo(content, 1024, MarkerPatch)
		if got != first {
			t.Fatalf("run %d differed from the first", i)
		}
	}
}

// TestTruncateToKeepsValidUTF8 checks a cut never splits a rune.
func TestTruncateToKeepsValidUTF8(t *testing.T) {
	for offset := 0; offset < 4; offset++ {
		content := strings.Repeat("x", offset) + strings.Repeat("é", 200)

		got, truncated := truncateTo(content, 100, MarkerPatch)
		if !truncated {
			t.Fatalf("offset %d: expected truncation", offset)
		}
		if strings.Contains(got, "�") {
			t.Errorf("offset %d: output contains a replacement character", offset)
		}
	}
}

func TestTruncateLines(t *testing.T) {
	tests := []struct {
		name          string
		lines         int
		maxLines      int
		wantTruncated bool
	}{
		{name: "fewer lines than the limit", lines: 3, maxLines: 10},
		{name: "exactly at the limit", lines: 10, maxLines: 10},
		{name: "one line over", lines: 11, maxLines: 10, wantTruncated: true},
		{name: "far over", lines: 500, maxLines: 10, wantTruncated: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			content := strings.TrimSuffix(strings.Repeat("line\n", tt.lines), "\n")

			got, truncated := truncateLines(content, tt.maxLines)

			if truncated != tt.wantTruncated {
				t.Errorf("truncated = %t, want %t", truncated, tt.wantTruncated)
			}
			if !tt.wantTruncated {
				if got != content {
					t.Error("content was altered although it fit")
				}
				return
			}
			if !strings.Contains(got, "lines omitted") {
				t.Errorf("content = %q, want a note about omitted lines", got)
			}
			if count := strings.Count(got, "line\n"); count > tt.maxLines {
				t.Errorf("kept %d lines, want at most %d", count, tt.maxLines)
			}
		})
	}
}

func TestTruncateLinesKeepsHeadAndTail(t *testing.T) {
	content := "setup\nfirst failure\nnoise one\nnoise two\nfinal summary\nexit status"

	got, truncated := truncateLines(content, 4)

	if !truncated {
		t.Fatal("truncateLines() did not report truncation")
	}
	for _, want := range []string{"setup", "first failure", "final summary", "exit status", "lines omitted"} {
		if !strings.Contains(got, want) {
			t.Errorf("truncateLines() output %q does not contain %q", got, want)
		}
	}
}

// TestMarkersAreDistinct keeps each marker meaningful about what was cut.
func TestMarkersAreDistinct(t *testing.T) {
	markers := []string{MarkerPatch, MarkerRule, MarkerAnalysis, MarkerTicketDescription}

	seen := map[string]bool{}
	for _, m := range markers {
		if m == "" {
			t.Error("a marker is empty")
		}
		if !strings.HasPrefix(m, "[TRUNCATED:") {
			t.Errorf("marker %q does not announce truncation", m)
		}
		if seen[m] {
			t.Errorf("marker %q is duplicated", m)
		}
		seen[m] = true
	}
}
