// Package bidi isolates a name where it is shown inside other text (#124). A name is kept as typed, and a right-to-left
// one reorders the text around it in a terminal or a mail, and is reordered by it, unless it sits in an isolate.
package bidi

import "strings"

const (
	fsi = "⁨" // first-strong isolate: the name's own first strong letter sets its direction
	pdi = "⁩" // pop directional isolate
)

// Isolate is name between FSI and PDI, for text a terminal or a mail shows. An empty name stays empty. Names carry no
// bidi control of their own (they are refused when typed), so the pair can't be closed early.
func Isolate(name string) string {
	if name == "" {
		return ""
	}
	return fsi + name + pdi
}

// Join isolates each name, then joins them with sep.
func Join(names []string, sep string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = Isolate(n)
	}
	return strings.Join(out, sep)
}
