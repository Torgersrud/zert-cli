package main

import (
	"strings"
	"unicode"
)

// sanitize neutralizes terminal-escape/control characters in server-supplied
// text so it cannot inject escape sequences into the terminal. Every control
// rune (including ESC) becomes "?"; newlines and tabs are kept. The result is
// truncated to 500 runes.
func sanitize(s string) string {
	var b strings.Builder
	count := 0
	for _, r := range s {
		if count == 500 {
			break
		}
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			b.WriteByte('?')
		} else {
			b.WriteRune(r)
		}
		count++
	}
	return b.String()
}
