package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

const (
	// MaxRawSourceBytes bounds what ARC will read from any single source before
	// normalization. It protects the process from an unexpectedly large page,
	// schema, or customer file.
	MaxRawSourceBytes = 2 * 1024 * 1024

	// MaxDocumentBytes bounds normalized content before the global context
	// selector applies its smaller combined allowance.
	MaxDocumentBytes = 128 * 1024
)

const documentTruncationMarker = "\n\n[TRUNCATED: external evidence exceeded 131072 bytes]\n"

func normalizeDocumentContent(content string) (string, bool) {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\x00", ""))
	if len(content) <= MaxDocumentBytes {
		return content, false
	}

	limit := MaxDocumentBytes - len(documentTruncationMarker)
	if limit < 0 {
		limit = 0
	}
	cut := content[:limit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + documentTruncationMarker, true
}

func digest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}
