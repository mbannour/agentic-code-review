package jira

import (
	"encoding/json"
	"strings"
	"testing"
)

// adfDoc wraps content nodes in a doc envelope.
func adfDoc(content string) string {
	return `{"type":"doc","version":1,"content":[` + content + `]}`
}

const adfParagraph = `{"type":"paragraph","content":[{"type":"text","text":"Retry failed payments"}]}`

func TestADFPlainText(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "simple paragraph",
			json: adfDoc(adfParagraph),
			want: "Retry failed payments",
		},
		{
			name: "multiple text nodes in one paragraph",
			json: adfDoc(`{"type":"paragraph","content":[
			        {"type":"text","text":"Retry "},
			        {"type":"text","text":"failed ","marks":[{"type":"strong"}]},
			        {"type":"text","text":"payments"}]}`),
			want: "Retry failed payments",
		},
		{
			name: "multiple paragraphs are blank-line separated",
			json: adfDoc(`{"type":"paragraph","content":[{"type":"text","text":"First paragraph."}]},
			             {"type":"paragraph","content":[{"type":"text","text":"Second paragraph."}]}`),
			want: "First paragraph.\n\nSecond paragraph.",
		},
		{
			name: "heading",
			json: adfDoc(`{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Acceptance criteria"}]}`),
			want: "Acceptance criteria",
		},
		{
			name: "heading followed by a paragraph",
			json: adfDoc(`{"type":"heading","attrs":{"level":1},"content":[{"type":"text","text":"Background"}]},
			             {"type":"paragraph","content":[{"type":"text","text":"Cards fail intermittently."}]}`),
			want: "Background\n\nCards fail intermittently.",
		},
		{
			name: "bullet list",
			json: adfDoc(`{"type":"bulletList","content":[
			        {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Retry twice"}]}]},
			        {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Back off exponentially"}]}]}]}`),
			want: "- Retry twice\n- Back off exponentially",
		},
		{
			name: "ordered list is numbered from one",
			json: adfDoc(`{"type":"orderedList","content":[
			        {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Authorize"}]}]},
			        {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Capture"}]}]},
			        {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Settle"}]}]}]}`),
			want: "1. Authorize\n2. Capture\n3. Settle",
		},
		{
			name: "hard break inside a paragraph",
			json: adfDoc(`{"type":"paragraph","content":[
			        {"type":"text","text":"Line one"},
			        {"type":"hardBreak"},
			        {"type":"text","text":"Line two"}]}`),
			want: "Line one\nLine two",
		},
		{
			name: "code block keeps its text",
			json: adfDoc(`{"type":"codeBlock","attrs":{"language":"go"},"content":[{"type":"text","text":"if err != nil {\n\treturn err\n}"}]}`),
			want: "if err != nil {\n\treturn err\n}",
		},
		{
			name: "paragraph, list, and code block together",
			json: adfDoc(`{"type":"paragraph","content":[{"type":"text","text":"Steps:"}]},
			             {"type":"bulletList","content":[
			               {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Retry"}]}]}]},
			             {"type":"codeBlock","content":[{"type":"text","text":"retry()"}]}`),
			want: "Steps:\n\n- Retry\n\nretry()",
		},
		{
			name: "unknown node type keeps its nested text",
			json: adfDoc(`{"type":"someFutureNode","content":[{"type":"paragraph","content":[{"type":"text","text":"Still readable"}]}]}`),
			want: "Still readable",
		},
		{
			name: "unknown inline node is skipped without losing siblings",
			json: adfDoc(`{"type":"paragraph","content":[
			        {"type":"text","text":"Before "},
			        {"type":"weirdInline","attrs":{"foo":"bar"}},
			        {"type":"text","text":"after"}]}`),
			want: "Before after",
		},
		{
			name: "unknown node with no content yields nothing",
			json: adfDoc(`{"type":"mysteryNode"},{"type":"paragraph","content":[{"type":"text","text":"Kept"}]}`),
			want: "Kept",
		},
		{
			name: "empty doc",
			json: `{"type":"doc","version":1,"content":[]}`,
			want: "",
		},
		{
			name: "doc with no content key",
			json: `{"type":"doc","version":1}`,
			want: "",
		},
		{
			name: "empty paragraphs are dropped",
			json: adfDoc(`{"type":"paragraph","content":[]},
			             {"type":"paragraph","content":[{"type":"text","text":"Only line"}]},
			             {"type":"paragraph"}`),
			want: "Only line",
		},
		{
			name: "horizontal rule is dropped",
			json: adfDoc(`{"type":"paragraph","content":[{"type":"text","text":"Above"}]},
			             {"type":"rule"},
			             {"type":"paragraph","content":[{"type":"text","text":"Below"}]}`),
			want: "Above\n\nBelow",
		},
		{
			name: "blockquote is flattened",
			json: adfDoc(`{"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"text","text":"Quoted"}]}]}`),
			want: "Quoted",
		},
		{
			name: "panel is flattened",
			json: adfDoc(`{"type":"panel","attrs":{"panelType":"info"},"content":[{"type":"paragraph","content":[{"type":"text","text":"Note this"}]}]}`),
			want: "Note this",
		},
		{
			name: "mention renders its display text",
			json: adfDoc(`{"type":"paragraph","content":[
			        {"type":"text","text":"Assigned to "},
			        {"type":"mention","attrs":{"id":"123","text":"@alice"}}]}`),
			want: "Assigned to @alice",
		},
		{
			name: "emoji renders its text",
			json: adfDoc(`{"type":"paragraph","content":[{"type":"emoji","attrs":{"shortName":":warning:","text":"⚠️"}}]}`),
			want: "⚠️",
		},
		{
			name: "inline card renders its url",
			json: adfDoc(`{"type":"paragraph","content":[{"type":"inlineCard","attrs":{"url":"https://example.com/x"}}]}`),
			want: "https://example.com/x",
		},
		{
			name: "nested list inside a list item is indented",
			json: adfDoc(`{"type":"bulletList","content":[
			        {"type":"listItem","content":[
			          {"type":"paragraph","content":[{"type":"text","text":"Outer"}]},
			          {"type":"bulletList","content":[
			            {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Inner"}]}]}]}]}]}`),
			want: "- Outer\n  - Inner",
		},
		{
			name: "list item with two paragraphs",
			json: adfDoc(`{"type":"bulletList","content":[
			        {"type":"listItem","content":[
			          {"type":"paragraph","content":[{"type":"text","text":"First"}]},
			          {"type":"paragraph","content":[{"type":"text","text":"Second"}]}]}]}`),
			want: "- First\n  Second",
		},
		{
			name: "empty list items are skipped",
			json: adfDoc(`{"type":"orderedList","content":[
			        {"type":"listItem","content":[]},
			        {"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Real item"}]}]}]}`),
			want: "1. Real item",
		},
		{
			name: "text node at the top level",
			json: `{"type":"doc","version":1,"content":[{"type":"text","text":"Bare text"}]}`,
			want: "Bare text",
		},
		{
			name: "leading and trailing whitespace is trimmed",
			json: adfDoc(`{"type":"paragraph","content":[{"type":"text","text":"  padded  "}]}`),
			want: "padded",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var doc adfDocument
			if err := json.Unmarshal([]byte(tt.json), &doc); err != nil {
				t.Fatalf("Unmarshal() returned error: %v", err)
			}

			if got := doc.PlainText(); got != tt.want {
				t.Errorf("PlainText() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestADFDocumentUnmarshal(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{name: "null description", json: `null`, want: ""},
		{name: "plain string description", json: `"Retry failed payments"`, want: "Retry failed payments"},
		{name: "plain string is trimmed", json: `"  padded  "`, want: "padded"},
		{name: "empty string", json: `""`, want: ""},
		{name: "adf document", json: adfDoc(adfParagraph), want: "Retry failed payments"},
		{name: "empty object", json: `{}`, want: ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var doc adfDocument
			if err := json.Unmarshal([]byte(tt.json), &doc); err != nil {
				t.Fatalf("Unmarshal(%s) returned error: %v", tt.json, err)
			}
			if got := doc.PlainText(); got != tt.want {
				t.Errorf("PlainText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestADFAbsentFieldIsEmpty covers a description key that is missing entirely.
func TestADFAbsentFieldIsEmpty(t *testing.T) {
	var wrapper struct {
		Description adfDocument `json:"description"`
	}
	if err := json.Unmarshal([]byte(`{"summary":"x"}`), &wrapper); err != nil {
		t.Fatalf("Unmarshal() returned error: %v", err)
	}
	if got := wrapper.Description.PlainText(); got != "" {
		t.Errorf("PlainText() = %q, want empty", got)
	}
}

// TestADFDeepNestingDoesNotPanic guards the recursion depth limit.
func TestADFDeepNestingDoesNotPanic(t *testing.T) {
	// Build a document nested far past the depth limit.
	const depth = 500
	body := `{"type":"text","text":"deep"}`
	for i := 0; i < depth; i++ {
		body = `{"type":"blockquote","content":[` + body + `]}`
	}

	var doc adfDocument
	if err := json.Unmarshal([]byte(adfDoc(body)), &doc); err != nil {
		t.Fatalf("Unmarshal() returned error: %v", err)
	}

	// The only requirement is that this returns rather than exhausting the stack.
	_ = doc.PlainText()
}

// TestADFMalformedDoesNotPanic feeds structurally odd but valid JSON.
func TestADFMalformedDoesNotPanic(t *testing.T) {
	inputs := []string{
		`{"type":"doc","content":[{"type":"paragraph","content":null}]}`,
		`{"type":"doc","content":[{"type":"bulletList","content":[{"type":"text","text":"not a listItem"}]}]}`,
		`{"type":"doc","content":[{"type":"orderedList"}]}`,
		`{"type":"doc","content":[{"type":"listItem","content":[{"type":"text","text":"orphan"}]}]}`,
		`{"type":"","content":[{"type":"text","text":"typeless"}]}`,
		`{"content":[{"text":"no types at all"}]}`,
		`{"type":"doc","content":[{"type":"mention","attrs":{}}]}`,
		`{"type":"doc","content":[{"type":"emoji","attrs":{"shortName":":x:"}}]}`,
	}

	for _, in := range inputs {
		var doc adfDocument
		if err := json.Unmarshal([]byte(in), &doc); err != nil {
			t.Errorf("Unmarshal(%s) returned error: %v", in, err)
			continue
		}
		got := doc.PlainText() // must not panic
		if strings.Contains(got, "\x00") {
			t.Errorf("PlainText() produced a NUL byte for %s", in)
		}
	}
}

// TestADFInvalidJSONIsAnError checks a non-object, non-string value is rejected
// rather than silently ignored.
func TestADFInvalidJSONIsAnError(t *testing.T) {
	for _, in := range []string{`123`, `true`, `[1,2,3]`, `{`} {
		var doc adfDocument
		if err := json.Unmarshal([]byte(in), &doc); err == nil {
			t.Errorf("Unmarshal(%s) = nil error, want an error", in)
		}
	}
}
