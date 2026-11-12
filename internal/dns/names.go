package dns

import (
	"fmt"
	"strings"

	"golang.org/x/net/idna"
)

// names is IDNA's lookup mapping (UTS #46, nontransitional, as IDNA2008 reads names), with DNS's lengths checked and
// every A-label decoded and checked. It leaves out the rules that judge a name rather than read it, which zones break
// and DNS doesn't need: the host-name rules (STD3), which refuse the underscore of _acme-challenge; the hyphen rules,
// which refuse a label such as r3--cdn; and the Bidi rule, which refuses _acme-challenge beside a right-to-left label.
var names = idna.New(idna.MapForLookup(), idna.StrictDomainName(false), idna.CheckHyphens(false), idna.Transitional(false),
	idna.VerifyDNSLength(true))

// Canonical is a name in the one form HomersLab compares names in: ASCII (A-labels), in lower case, without a trailing
// dot. libdns's "@" (the zone's apex) stays "@", and a "*" is allowed as the first label. A name that isn't valid IDNA,
// or holds anything but letters, digits, hyphens and underscores once in ASCII, is refused (ErrBadName): it is never
// kept as it is, or compared in another form (#153).
func Canonical(name string) (string, error) {
	n := strings.TrimSuffix(name, ".")
	if n == "@" {
		return n, nil
	}
	a, err := names.ToASCII(n)
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrBadName, name)
	}
	for i, l := range strings.Split(a, ".") {
		if !(l == "*" && i == 0) && strings.ContainsFunc(l, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_')
		}) {
			return "", fmt.Errorf("%w: %q", ErrBadName, name)
		}
	}
	return a, nil
}

// sameValue is a value of HomersLab's as a provider may list it in its own form: a TXT in quotes (one string or
// several, as a long one is split), a CNAME's target in another case, without its trailing dot or in Unicode. Anything
// else must be the very value.
func sameValue(typ, ours, theirs string) bool {
	if ours == theirs {
		return true
	}
	switch strings.ToUpper(typ) {
	case "TXT":
		text, ok := quoted(theirs)
		return ok && text == ours
	case "CNAME":
		a, err := Canonical(ours)
		b, err2 := Canonical(theirs)
		return err == nil && err2 == nil && a == b
	}
	return false
}

// quoted is a TXT value in its presentation form, one or more quoted strings with \" and \\ escaped, as one text; ok
// is false for anything else.
func quoted(v string) (text string, ok bool) {
	var b strings.Builder
	for v = strings.TrimSpace(v); v != ""; v = strings.TrimLeft(v, " ") {
		if v[0] != '"' {
			return "", false
		}
		i := 1
		for ; i < len(v) && v[i] != '"'; i++ {
			if v[i] == '\\' {
				if i+1 == len(v) || v[i+1] != '"' && v[i+1] != '\\' {
					return "", false
				}
				i++
			}
			b.WriteByte(v[i])
		}
		if i == len(v) {
			return "", false
		}
		v = v[i+1:]
	}
	return b.String(), b.Len() > 0
}
