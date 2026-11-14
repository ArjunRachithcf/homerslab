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
// dot. libdns's "@" (the zone's apex) stays "@", and a "*" is allowed as the first label (#153).
//
// A name is read in two forms only, each with or without its trailing dot: its A-labels in any ASCII case, or exactly
// its U-labels as IDNA writes them (lower case, NFC). Anything IDNA's mapping would have to fold first (a fullwidth
// letter, an ideographic full stop, a soft hyphen, capitals outside ASCII) is refused, like a name that isn't valid
// IDNA or holds anything but letters, digits, hyphens and underscores once in ASCII (ErrBadName): a provider that keeps
// names as they are typed holds those as names of their own, so they are never taken for HomersLab's (#164's review).
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
	if ascii(n) && strings.ToLower(n) == a {
		return a, nil
	}
	if u, err := names.ToUnicode(a); err == nil && u == n {
		return a, nil
	}
	return "", fmt.Errorf("%w: %q", ErrBadName, name)
}

func ascii(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
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

// quoted is a TXT value in its presentation form (RFC 1035 §5.1), one or more quoted strings with \X and \DDD
// escapes, as one text; ok is false for anything else. (A value someone else wrote as a literal "tok", quotes and
// all, reads as tok: HomersLab's values are random tokens and its inventory, which nobody else writes.)
func quoted(v string) (text string, ok bool) {
	var b strings.Builder
	for v = strings.TrimSpace(v); v != ""; v = strings.TrimLeft(v, " ") {
		if v[0] != '"' {
			return "", false
		}
		i := 1
		for ; i < len(v) && v[i] != '"'; i++ {
			if v[i] == '\\' {
				switch {
				case i+1 == len(v):
					return "", false
				case isDigit(v[i+1]):
					if i+3 >= len(v) || !isDigit(v[i+2]) || !isDigit(v[i+3]) {
						return "", false
					}
					d := int(v[i+1]-'0')*100 + int(v[i+2]-'0')*10 + int(v[i+3]-'0')
					if d > 255 {
						return "", false
					}
					b.WriteByte(byte(d))
					i += 3
					continue
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

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
