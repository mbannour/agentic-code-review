package technology

import (
	"path"
	"strings"
)

// javascriptLibrarySignals are the frontend dependencies whose semantics materially
// change what a reviewer should inspect. Coordinates include the JSON key separator
// where practical so ordinary prose does not accidentally enable guidance.
func javascriptLibrarySignals() []librarySignal {
	return []librarySignal{
		{coordinate: `"next":`, label: "next.js", framework: true},
		{coordinate: `"react":`, label: "react"},
		{coordinate: `"vitest":`, label: "vitest"},
		{coordinate: `"playwright`, label: "playwright"},
		{coordinate: `"@reduxjs/toolkit":`, label: "redux-toolkit"},
		{coordinate: `"@mui/material":`, label: "mui"},
		{coordinate: `"i18next":`, label: "i18next"},
	}
}

// IsNPMBuildFile reports whether path is an npm/TypeScript build or dependency
// definition worth prioritizing alongside changed frontend source.
func IsNPMBuildFile(filePath string) bool {
	clean := strings.TrimPrefix(path.Clean(strings.TrimSpace(filePath)), "./")
	lower := strings.ToLower(clean)
	switch path.Base(lower) {
	case "package.json", "package-lock.json", "tsconfig.json",
		"next.config.js", "next.config.mjs", "next.config.ts",
		"eslint.config.js", "eslint.config.mjs", "eslint.config.cjs":
		return true
	default:
		return false
	}
}
