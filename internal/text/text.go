// Package text says which free text HomersLab takes from a catalog or a request: a name shown to people is one line
// of printable text, so it can't end a line of any file it might reach. YAML breaks lines at U+0085, U+2028 and
// U+2029 too, not only at \n and \r (#195's review).
package text

import (
	"unicode"
	"unicode/utf8"
)

// OneLine says whether s is one line of printable text: valid UTF-8, with no control character (C0, DEL or C1, NEL
// among them), no line or paragraph separator, and no bidirectional override or isolate, which would show a name
// other than the one it is.
func OneLine(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case unicode.IsControl(r), unicode.In(r, unicode.Zl, unicode.Zp):
			return false
		case r >= '‪' && r <= '‮', r >= '⁦' && r <= '⁩', r == '‎', r == '‏', r == '؜':
			return false
		}
	}
	return true
}
