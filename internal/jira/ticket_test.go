package jira

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestExtractTicketKey(t *testing.T) {
	tests := []struct {
		name string
		text string
		want TicketKey
		ok   bool
	}{
		// Matches.
		{name: "bare key", text: "PAY-431", want: "PAY-431", ok: true},
		{name: "key in a sentence", text: "Implement payment retries for PAY-431", want: "PAY-431", ok: true},
		{name: "key at the beginning", text: "PAY-431 retry payments", want: "PAY-431", ok: true},
		{name: "key at the end", text: "retry payments PAY-431", want: "PAY-431", ok: true},
		{name: "key embedded in a branch name", text: "feature/PAY-431-retry-payment", want: "PAY-431", ok: true},
		{name: "key in a slash-separated branch", text: "bugfix/PLAT-1024", want: "PLAT-1024", ok: true},
		{name: "long project key", text: "PLAT-1024", want: "PLAT-1024", ok: true},
		{name: "short issue number", text: "ABC-7", want: "ABC-7", ok: true},
		{name: "digit in project key", text: "ABC1-55", want: "ABC1-55", ok: true},
		{name: "digit-suffixed project key", text: "TEAM2-9", want: "TEAM2-9", ok: true},
		{name: "key in parentheses", text: "retries (PAY-431)", want: "PAY-431", ok: true},
		{name: "key after a colon", text: "fix: PAY-431 retries", want: "PAY-431", ok: true},
		{name: "key across newlines", text: "Improve retries.\n\nRelated to PAY-431.\n", want: "PAY-431", ok: true},
		{name: "multi-digit issue number", text: "PAY-999999", want: "PAY-999999", ok: true},
		{name: "issue number containing zeros", text: "PAY-101", want: "PAY-101", ok: true},
		{name: "first of several keys", text: "PAY-431 depends on PAY-432", want: "PAY-431", ok: true},

		// Non-matches.
		{name: "lowercase key", text: "pay-431"},
		{name: "mixed case key", text: "Pay-431"},
		{name: "missing issue number", text: "PAY-"},
		{name: "issue number zero", text: "PAY-0"},
		{name: "issue number with a leading zero", text: "PAY-0431"},
		{name: "reversed order", text: "431-PAY"},
		{name: "single letter project key", text: "A-1"},
		{name: "no hyphen", text: "PAY431"},
		{name: "empty string", text: ""},
		{name: "prose without a key", text: "Improve failed payment retry handling."},
		{name: "project key glued to a word", text: "xPAY-431"},
		{name: "underscore before the key", text: "release_PAY_431"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ExtractTicketKey(tt.text)

			if ok != tt.ok {
				t.Fatalf("ExtractTicketKey(%q) ok = %t, want %t (got key %q)", tt.text, ok, tt.ok, got)
			}
			if got != tt.want {
				t.Errorf("ExtractTicketKey(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

// TestExtractTicketKeyLeadingHyphen documents a deliberate asymmetry: a hyphen
// is a normal separator in branch names, so extraction sees a key after one,
// while ParseTicketKey still rejects the same string as explicit input.
func TestExtractTicketKeyLeadingHyphen(t *testing.T) {
	got, ok := ExtractTicketKey("-PAY-431")
	if !ok || got != "PAY-431" {
		t.Errorf("ExtractTicketKey(%q) = %q, %t; want PAY-431, true", "-PAY-431", got, ok)
	}

	if _, err := ParseTicketKey("-PAY-431"); err == nil {
		t.Error("ParseTicketKey(\"-PAY-431\") = nil error, want an error")
	}
}

func TestExtractTicketKeys(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []TicketKey
	}{
		{
			name: "no keys",
			text: "Improve failed payment retry handling.",
			want: nil,
		},
		{
			name: "empty text",
			text: "",
			want: nil,
		},
		{
			name: "single key",
			text: "PAY-431 retry payments",
			want: []TicketKey{"PAY-431"},
		},
		{
			name: "multiple distinct keys in the same text",
			text: "PAY-431 depends on PAY-432",
			want: []TicketKey{"PAY-431", "PAY-432"},
		},
		{
			name: "duplicate key is collapsed",
			text: "PAY-431 supersedes PAY-431",
			want: []TicketKey{"PAY-431"},
		},
		{
			name: "duplicates and distinct keys keep occurrence order",
			text: "PLAT-9 then PAY-431 then PLAT-9 then ABC-7",
			want: []TicketKey{"PLAT-9", "PAY-431", "ABC-7"},
		},
		{
			name: "keys across lines",
			text: "Fixes PAY-431.\nAlso touches PLAT-1024.\n",
			want: []TicketKey{"PAY-431", "PLAT-1024"},
		},
		{
			name: "different projects",
			text: "PAY-1 PLAT-2 ABC1-3 TEAM2-4",
			want: []TicketKey{"PAY-1", "PLAT-2", "ABC1-3", "TEAM2-4"},
		},
		{
			name: "invalid keys are skipped",
			text: "pay-431 PAY-0 431-PAY but PAY-431 is real",
			want: []TicketKey{"PAY-431"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractTicketKeys(tt.text)

			if len(got) != len(tt.want) {
				t.Fatalf("ExtractTicketKeys(%q) = %v, want %v", tt.text, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("key %d = %q, want %q (order not preserved?)", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseTicketKey(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    TicketKey
		wantErr bool
	}{
		{name: "bare key", value: "PAY-431", want: "PAY-431"},
		{name: "long project key", value: "PLAT-1024", want: "PLAT-1024"},
		{name: "digit in project key", value: "ABC1-55", want: "ABC1-55"},
		{name: "surrounding whitespace is trimmed", value: "  PAY-431  ", want: "PAY-431"},

		{name: "key with surrounding prose", value: "ticket PAY-431", wantErr: true},
		{name: "key with a trailing word", value: "PAY-431 retries", wantErr: true},
		{name: "arbitrary word", value: "whatever", wantErr: true},
		{name: "lowercase", value: "pay-431", wantErr: true},
		{name: "missing issue number", value: "PAY-", wantErr: true},
		{name: "issue number zero", value: "PAY-0", wantErr: true},
		{name: "reversed", value: "431-PAY", wantErr: true},
		{name: "leading hyphen", value: "-PAY-431", wantErr: true},
		{name: "two keys", value: "PAY-431 PAY-432", wantErr: true},
		{name: "empty", value: "", wantErr: true},
		{name: "whitespace only", value: "   ", wantErr: true},
		{name: "url", value: "https://jira.example.com/browse/PAY-431", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTicketKey(tt.value)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseTicketKey(%q) = %q, want error", tt.value, got)
				}

				var invalid *InvalidTicketKeyError
				if !errors.As(err, &invalid) {
					t.Fatalf("ParseTicketKey(%q) error %v is not an *InvalidTicketKeyError", tt.value, err)
				}
				if invalid.Value != tt.value {
					t.Errorf("InvalidTicketKeyError.Value = %q, want %q", invalid.Value, tt.value)
				}
				if got != "" {
					t.Errorf("ParseTicketKey(%q) = %q on error, want empty", tt.value, got)
				}
				return
			}

			if err != nil {
				t.Fatalf("ParseTicketKey(%q) returned error: %v", tt.value, err)
			}
			if got != tt.want {
				t.Errorf("ParseTicketKey(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestResolveTicketKey(t *testing.T) {
	tests := []struct {
		name    string
		sources TicketSources
		want    TicketKey
		wantOK  bool
	}{
		{
			name:    "ticket in the title",
			sources: TicketSources{Title: "PAY-431 Add payment retries"},
			want:    "PAY-431",
			wantOK:  true,
		},
		{
			name:    "ticket in the branch",
			sources: TicketSources{Title: "Add retry handling", Branch: "feature/PAY-431-retry-payments"},
			want:    "PAY-431",
			wantOK:  true,
		},
		{
			name: "ticket in the body",
			sources: TicketSources{
				Title:  "Add retry handling",
				Branch: "feature/retry-payments",
				Body:   "Improve failed payment retry handling. Fixes PAY-431.",
			},
			want:   "PAY-431",
			wantOK: true,
		},
		{
			name: "explicit ticket wins over every other source",
			sources: TicketSources{
				Explicit: "PAY-100",
				Title:    "PAY-431 Add payment retries",
				Branch:   "feature/PAY-999",
				Body:     "Related to PAY-500",
			},
			want:   "PAY-100",
			wantOK: true,
		},
		{
			name: "title wins over branch and body",
			sources: TicketSources{
				Title:  "PAY-431 Add payment retries",
				Branch: "feature/PAY-999",
				Body:   "Related to PAY-500",
			},
			want:   "PAY-431",
			wantOK: true,
		},
		{
			name: "branch wins over body",
			sources: TicketSources{
				Title:  "Add payment retries",
				Branch: "feature/PAY-999-retry",
				Body:   "Related to PAY-500",
			},
			want:   "PAY-999",
			wantOK: true,
		},
		{
			name:    "explicit ticket alone",
			sources: TicketSources{Explicit: "PAY-431"},
			want:    "PAY-431",
			wantOK:  true,
		},
		{
			name:    "explicit ticket is trimmed",
			sources: TicketSources{Explicit: "  PAY-431 "},
			want:    "PAY-431",
			wantOK:  true,
		},
		{
			name: "no ticket anywhere",
			sources: TicketSources{
				Title:  "Add retry handling",
				Branch: "feature/retry-payments",
				Body:   "Improve failed payment retry handling.",
			},
			wantOK: false,
		},
		{
			name:    "all sources empty",
			sources: TicketSources{},
			wantOK:  false,
		},
		{
			name: "invalid keys in every source find nothing",
			sources: TicketSources{
				Title:  "pay-431 retries",
				Branch: "feature/PAY-0",
				Body:   "see 431-PAY",
			},
			wantOK: false,
		},
		{
			name: "duplicate key in the title is not ambiguous",
			sources: TicketSources{
				Title: "PAY-431 follow-up to PAY-431",
			},
			want:   "PAY-431",
			wantOK: true,
		},
		{
			name: "ambiguity in an earlier source is skipped when explicit is set",
			sources: TicketSources{
				Explicit: "PAY-100",
				Title:    "PAY-431 depends on PAY-432",
			},
			want:   "PAY-100",
			wantOK: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := ResolveTicketKey(tt.sources)
			if err != nil {
				t.Fatalf("ResolveTicketKey(%+v) returned error: %v", tt.sources, err)
			}
			if ok != tt.wantOK {
				t.Fatalf("ResolveTicketKey() ok = %t, want %t (key %q)", ok, tt.wantOK, got)
			}
			if got != tt.want {
				t.Errorf("ResolveTicketKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestResolveTicketKeyUsesTitleBeforeBranch mirrors the precedence example from
// the specification.
func TestResolveTicketKeyUsesTitleBeforeBranch(t *testing.T) {
	key, _, err := ResolveTicketKey(TicketSources{
		Title:  "PAY-431 retry payments",
		Branch: "feature/PAY-999-test",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "PAY-431" {
		t.Fatalf("expected PAY-431, got %s", key)
	}
}

func TestResolveTicketKeyInvalidExplicit(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
	}{
		{name: "arbitrary word", explicit: "whatever"},
		{name: "key with prose", explicit: "ticket PAY-431"},
		{name: "lowercase", explicit: "pay-431"},
		{name: "issue number zero", explicit: "PAY-0"},
		{name: "two keys", explicit: "PAY-431,PAY-432"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			// A valid ticket elsewhere must not paper over bad explicit input.
			got, ok, err := ResolveTicketKey(TicketSources{
				Explicit: tt.explicit,
				Title:    "PAY-431 Add payment retries",
			})

			var invalid *InvalidTicketKeyError
			if !errors.As(err, &invalid) {
				t.Fatalf("ResolveTicketKey() error = %v, want *InvalidTicketKeyError", err)
			}
			if ok {
				t.Error("ResolveTicketKey() ok = true, want false")
			}
			if got != "" {
				t.Errorf("ResolveTicketKey() = %q, want empty", got)
			}
			if !strings.Contains(err.Error(), tt.explicit) {
				t.Errorf("error %q does not name the offending value %q", err, tt.explicit)
			}
		})
	}
}

func TestResolveTicketKeyAmbiguity(t *testing.T) {
	tests := []struct {
		name       string
		sources    TicketSources
		wantSource string
		wantKeys   []TicketKey
	}{
		{
			name:       "two keys in the title",
			sources:    TicketSources{Title: "PAY-431 depends on PAY-432"},
			wantSource: "pull request title",
			wantKeys:   []TicketKey{"PAY-431", "PAY-432"},
		},
		{
			name: "two keys in the branch",
			sources: TicketSources{
				Title:  "Add retry handling",
				Branch: "feature/PAY-431-and-PAY-432",
			},
			wantSource: "head branch",
			wantKeys:   []TicketKey{"PAY-431", "PAY-432"},
		},
		{
			name: "two keys in the body",
			sources: TicketSources{
				Title:  "Add retry handling",
				Branch: "feature/retry",
				Body:   "Fixes PAY-431 and PLAT-1024.",
			},
			wantSource: "pull request body",
			wantKeys:   []TicketKey{"PAY-431", "PLAT-1024"},
		},
		{
			name:       "three keys",
			sources:    TicketSources{Title: "PAY-431 PAY-432 PLAT-9"},
			wantSource: "pull request title",
			wantKeys:   []TicketKey{"PAY-431", "PAY-432", "PLAT-9"},
		},
		{
			name: "ambiguous title is reported instead of falling through to a clean branch",
			sources: TicketSources{
				Title:  "PAY-431 depends on PAY-432",
				Branch: "feature/PAY-999",
			},
			wantSource: "pull request title",
			wantKeys:   []TicketKey{"PAY-431", "PAY-432"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := ResolveTicketKey(tt.sources)

			var ambiguous *AmbiguousTicketError
			if !errors.As(err, &ambiguous) {
				t.Fatalf("ResolveTicketKey() error = %v, want *AmbiguousTicketError", err)
			}
			if ok {
				t.Error("ResolveTicketKey() ok = true, want false")
			}
			if got != "" {
				t.Errorf("ResolveTicketKey() = %q, want empty (no guessing)", got)
			}
			if ambiguous.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", ambiguous.Source, tt.wantSource)
			}
			if len(ambiguous.Keys) != len(tt.wantKeys) {
				t.Fatalf("Keys = %v, want %v", ambiguous.Keys, tt.wantKeys)
			}
			for i := range tt.wantKeys {
				if ambiguous.Keys[i] != tt.wantKeys[i] {
					t.Errorf("Keys[%d] = %q, want %q", i, ambiguous.Keys[i], tt.wantKeys[i])
				}
			}

			// The message must list the candidates so a human can pick one.
			for _, key := range tt.wantKeys {
				if !strings.Contains(err.Error(), key.String()) {
					t.Errorf("error %q does not mention %q", err, key)
				}
			}
		})
	}
}

func TestTicketKeyString(t *testing.T) {
	if got := TicketKey("PAY-431").String(); got != "PAY-431" {
		t.Errorf("String() = %q, want %q", got, "PAY-431")
	}
	if got := fmt.Sprintf("%s", TicketKey("PLAT-1024")); got != "PLAT-1024" {
		t.Errorf("formatted = %q, want %q", got, "PLAT-1024")
	}
}

// TestRegexpCompiledOnce guards the "do not compile per call" requirement: the
// package-level regexps must already be non-nil before any call is made.
func TestRegexpCompiledOnce(t *testing.T) {
	if exactKeyRE == nil || embeddedKeyRE == nil {
		t.Fatal("ticket regexps are not compiled at package initialization")
	}
}

func BenchmarkExtractTicketKey(b *testing.B) {
	text := "Improve failed payment retry handling as described in PAY-431."
	for i := 0; i < b.N; i++ {
		if _, ok := ExtractTicketKey(text); !ok {
			b.Fatal("expected a ticket")
		}
	}
}
