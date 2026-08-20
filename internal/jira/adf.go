package jira

import (
	"encoding/json"
	"strconv"
	"strings"
)

// adfNode is one node of an Atlassian Document Format tree. Only the fields the
// text extractor needs are decoded.
type adfNode struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Content []adfNode       `json:"content"`
	Attrs   map[string]any  `json:"attrs"`
	Raw     json.RawMessage `json:"-"`
}

// adfDocument is a Jira rich-text field. Jira Cloud v3 returns ADF for
// description fields, but older or partial payloads may carry a plain string, so
// both shapes are accepted.
type adfDocument struct {
	// text holds the value when the field arrived as a plain JSON string.
	text string

	// node holds the root when the field arrived as an ADF object.
	node *adfNode

	// present records whether the field was anything other than null.
	present bool
}

// UnmarshalJSON accepts a plain string, an ADF object, or null.
func (d *adfDocument) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}

	// A plain string description.
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		d.text = s
		d.present = true
		return nil
	}

	var node adfNode
	if err := json.Unmarshal(data, &node); err != nil {
		return err
	}
	d.node = &node
	d.present = true
	return nil
}

// PlainText renders the document as plain text.
func (d adfDocument) PlainText() string {
	if !d.present {
		return ""
	}
	if d.node == nil {
		return strings.TrimSpace(d.text)
	}
	return adfToPlainText(*d.node)
}

// maxADFDepth bounds recursion so a pathologically nested document cannot blow
// the stack.
const maxADFDepth = 50

// adfToPlainText converts an ADF tree to plain text.
//
// The conversion is deliberately conservative: it recovers readable prose and
// structure for the node types Jira commonly uses, and for anything unrecognised
// it descends into the node's content rather than failing or dropping text.
func adfToPlainText(root adfNode) string {
	blocks := renderBlocks(root, 0)
	return strings.TrimSpace(strings.Join(blocks, "\n\n"))
}

// renderBlocks turns a node into zero or more block-level strings.
func renderBlocks(n adfNode, depth int) []string {
	if depth > maxADFDepth {
		return nil
	}

	switch n.Type {
	case "doc", "blockquote", "panel":
		var blocks []string
		for _, child := range n.Content {
			blocks = append(blocks, renderBlocks(child, depth+1)...)
		}
		return blocks

	case "paragraph", "heading":
		text := strings.TrimRight(renderInline(n, depth+1), " \t")
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []string{text}

	case "codeBlock":
		text := renderInline(n, depth+1)
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []string{text}

	case "bulletList":
		return []string{renderList(n, depth+1, false)}

	case "orderedList":
		return []string{renderList(n, depth+1, true)}

	case "rule":
		return nil

	case "text", "hardBreak", "mention", "emoji", "inlineCard":
		// An inline node appearing where a block was expected: render it alone.
		text := strings.TrimSpace(renderInline(n, depth+1))
		if text == "" {
			return nil
		}
		return []string{text}

	default:
		// Unknown node: keep any text it holds rather than discarding it.
		var blocks []string
		for _, child := range n.Content {
			blocks = append(blocks, renderBlocks(child, depth+1)...)
		}
		if len(blocks) == 0 {
			if text := strings.TrimSpace(n.Text); text != "" {
				return []string{text}
			}
		}
		return blocks
	}
}

// renderList renders a bullet or ordered list as one block, one item per line.
func renderList(n adfNode, depth int, ordered bool) string {
	if depth > maxADFDepth {
		return ""
	}

	var lines []string
	number := 1

	for _, item := range n.Content {
		if item.Type != "listItem" {
			// Tolerate a list whose children are not listItems.
			if text := strings.TrimSpace(renderInline(item, depth+1)); text != "" {
				lines = append(lines, "- "+text)
			}
			continue
		}

		// A listItem holds blocks; nested lists become indented lines.
		var itemBlocks []string
		for _, child := range item.Content {
			itemBlocks = append(itemBlocks, renderBlocks(child, depth+1)...)
		}

		text := strings.TrimSpace(strings.Join(itemBlocks, "\n"))
		if text == "" {
			continue
		}

		marker := "- "
		if ordered {
			marker = strconv.Itoa(number) + ". "
			number++
		}

		// Indent continuation lines under the marker.
		itemLines := strings.Split(text, "\n")
		lines = append(lines, marker+itemLines[0])
		for _, cont := range itemLines[1:] {
			lines = append(lines, strings.Repeat(" ", len(marker))+cont)
		}
	}

	return strings.Join(lines, "\n")
}

// renderInline flattens a node's inline content into a single string.
func renderInline(n adfNode, depth int) string {
	if depth > maxADFDepth {
		return ""
	}

	var b strings.Builder

	switch n.Type {
	case "text":
		b.WriteString(n.Text)
	case "hardBreak":
		b.WriteString("\n")
	case "mention":
		b.WriteString(attrString(n.Attrs, "text"))
	case "emoji":
		if text := attrString(n.Attrs, "text"); text != "" {
			b.WriteString(text)
		} else {
			b.WriteString(attrString(n.Attrs, "shortName"))
		}
	case "inlineCard":
		b.WriteString(attrString(n.Attrs, "url"))
	default:
		// Fall through to the children below.
		if n.Text != "" {
			b.WriteString(n.Text)
		}
	}

	for _, child := range n.Content {
		b.WriteString(renderInline(child, depth+1))
	}

	return b.String()
}

// attrString reads a string attribute, returning "" when absent or not a string.
func attrString(attrs map[string]any, name string) string {
	if attrs == nil {
		return ""
	}
	if v, ok := attrs[name].(string); ok {
		return v
	}
	return ""
}
