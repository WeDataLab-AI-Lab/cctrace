package envgen

import (
	"strings"
	"unicode"
)

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(`'"\\$&;()<>|*?[]{}!`, r)
	}) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func shValue(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func psValue(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
