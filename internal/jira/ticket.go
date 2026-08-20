// Package jira holds Jira domain types. This step covers ticket key detection
// only; no Jira API calls are made.
package jira

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// TicketKey is a Jira issue key such as "PAY-431".
type TicketKey string

// String returns the key as a plain string.
func (t TicketKey) String() string { return string(t) }

// keyPattern is the shape of a Jira issue key: a project key of at least two
// characters starting with a letter, then a positive issue number.
const keyPattern = `[A-Z][A-Z0-9]+-[1-9][0-9]*`

// Compiled once at package load, never per call.
var (
	// exactKeyRE anchors the pattern so the whole input must be a key. Used for
	// explicit user input.
	exactKeyRE = regexp.MustCompile(`\A` + keyPattern + `\z`)

	// embeddedKeyRE finds keys inside free text such as a PR title or a branch
	// name. \b keeps it from matching a key glued to surrounding word
	// characters, so "xPAY-431" and "pay-431" are not matches.
	embeddedKeyRE = regexp.MustCompile(`\b` + keyPattern + `\b`)
)

// ErrNoTicketKey is returned when input contains no Jira ticket key.
var ErrNoTicketKey = errors.New("no Jira ticket key found")

// InvalidTicketKeyError reports explicit ticket input that is not a bare key.
type InvalidTicketKeyError struct {
	Value string
}

func (e *InvalidTicketKeyError) Error() string {
	return fmt.Sprintf("invalid Jira ticket key %q: expected a key like PAY-431", e.Value)
}

// AmbiguousTicketError reports that one source named several distinct tickets.
// The caller must disambiguate rather than the package guessing.
type AmbiguousTicketError struct {
	// Source names where the keys were found, e.g. "pull request title".
	Source string
	Keys   []TicketKey
}

func (e *AmbiguousTicketError) Error() string {
	keys := make([]string, 0, len(e.Keys))
	for _, k := range e.Keys {
		keys = append(keys, k.String())
	}
	return fmt.Sprintf("ambiguous Jira ticket: %s names %d keys (%s)",
		e.Source, len(e.Keys), strings.Join(keys, ", "))
}

// ParseTicketKey validates explicit ticket input. The entire value must be a
// ticket key, so "PAY-431" succeeds while "ticket PAY-431" does not. Surrounding
// whitespace is tolerated.
func ParseTicketKey(value string) (TicketKey, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", &InvalidTicketKeyError{Value: value}
	}
	if !exactKeyRE.MatchString(trimmed) {
		return "", &InvalidTicketKeyError{Value: value}
	}
	return TicketKey(trimmed), nil
}

// ExtractTicketKey returns the first ticket key in text.
func ExtractTicketKey(text string) (TicketKey, bool) {
	match := embeddedKeyRE.FindString(text)
	if match == "" {
		return "", false
	}
	return TicketKey(match), true
}

// ExtractTicketKeys returns every distinct ticket key in text, in order of first
// occurrence. Repeats of the same key collapse into one entry.
func ExtractTicketKeys(text string) []TicketKey {
	matches := embeddedKeyRE.FindAllString(text, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(matches))
	keys := make([]TicketKey, 0, len(matches))
	for _, m := range matches {
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		keys = append(keys, TicketKey(m))
	}
	return keys
}

// TicketSources holds the places a ticket key may be found, in precedence order.
type TicketSources struct {
	// Explicit is the user-supplied --ticket value. It must be a bare key.
	Explicit string
	Title    string
	Branch   string
	Body     string
}

// ResolveTicketKey finds the ticket for a pull request, checking the explicit
// value, then the title, then the branch, then the body.
//
// Per source: no match falls through to the next source, exactly one distinct
// match wins, and several distinct matches return an *AmbiguousTicketError
// rather than a guess. Finding nothing anywhere is not an error — it returns
// ("", false, nil).
func ResolveTicketKey(sources TicketSources) (TicketKey, bool, error) {
	if strings.TrimSpace(sources.Explicit) != "" {
		key, err := ParseTicketKey(sources.Explicit)
		if err != nil {
			return "", false, err
		}
		return key, true, nil
	}

	ordered := []struct {
		name string
		text string
	}{
		{"pull request title", sources.Title},
		{"head branch", sources.Branch},
		{"pull request body", sources.Body},
	}

	for _, source := range ordered {
		keys := ExtractTicketKeys(source.text)
		switch len(keys) {
		case 0:
			continue // fall through to the next source
		case 1:
			return keys[0], true, nil
		default:
			return "", false, &AmbiguousTicketError{Source: source.name, Keys: keys}
		}
	}

	return "", false, nil
}
