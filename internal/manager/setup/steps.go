package setup

import (
	"context"
	"encoding/json"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Built are the steps this version of HomersLab can take. The rest come with the tickets that build them.
var Built = map[string]Handler{
	"welcome":   welcome,
	"you":       you,
	"household": household,
}

// The limits of what someone types. A mail address is RFC 5321's longest, in octets.
const (
	NameMax = 64
	MailMax = 254
)

// Why a field's answer was refused.
const (
	Missing          = "missing"
	TooLong          = "too-long"
	HiddenCharacters = "hidden-characters" // control or bidi characters
	NotAMail         = "not-a-mail"
)

func welcome(context.Context, json.RawMessage) (map[string]string, error) { return nil, nil }

// you: the admin's name and mail address.
func you(_ context.Context, raw json.RawMessage) (map[string]string, error) {
	var a struct{ Name, Mail string }
	_ = json.Unmarshal(raw, &a)
	bad := &Invalid{}
	name, why := text(a.Name, NameMax)
	bad.add("name", why)
	addr, why := text(a.Mail, MailMax)
	if why == "" && len(addr) > MailMax {
		why = TooLong
	}
	if why == "" && !isMail(addr) {
		why = NotAMail
	}
	bad.add("mail", why)
	if bad.Fields != nil {
		return nil, bad
	}
	return map[string]string{"name": name, "mail": addr}, nil
}

// household: the household's name.
func household(_ context.Context, raw json.RawMessage) (map[string]string, error) {
	var a struct{ Name string }
	_ = json.Unmarshal(raw, &a)
	name, why := text(a.Name, NameMax)
	if why != "" {
		bad := &Invalid{}
		bad.add("name", why)
		return nil, bad
	}
	return map[string]string{"household": name}, nil
}

// text is one line someone typed, as it is kept: in NFC, without the spaces around it, and at most max characters.
// These answers reach terminals, mail headers and other people's screens, so it refuses control characters, which can
// rewrite a terminal, and the bidi overrides and isolates, which make the text around them read differently.
func text(s string, max int) (string, string) {
	s = strings.TrimSpace(norm.NFC.String(s))
	switch {
	case s == "":
		return "", Missing
	case hidden(s):
		return "", HiddenCharacters
	case !shows(s):
		return "", Missing // a name that shows as blank is no name
	case utf8.RuneCountInString(s) > max:
		return "", TooLong
	}
	return s, ""
}

// hidden says whether s holds a character that changes how the text around it reads or breaks, or carries what doesn't
// show:
//   - a control character (C0, DEL, C1);
//   - a format character (Unicode's Cf: the bidi overrides, isolates and marks, zero-width spaces, the BOM, the soft
//     hyphen, tags and the rest), but for:
//   - the zero-width non-joiner and joiner (U+200C, U+200D), which Persian, Indic scripts and emoji need;
//   - the Mongolian vowel separator (U+180E) between two Mongolian letters;
//   - a subdivision flag's tags: U+1F3F4, then a subdivision ID in tag characters (2 letters or 3 digits, then 1
//     to 4 letters or digits) and the cancel tag U+E007F, such as Scotland's;
//   - a variation selector (U+FE00–U+FE0F, U+E0100–U+E01EF, Mongolian's U+180B–U+180D and U+180F) anywhere but
//     right after a letter, number, symbol or punctuation mark: one at a time, as an emoji's presentation or an ideographic variant;
//   - a line or paragraph separator (U+2028, U+2029);
//   - a Hangul filler (U+115F, U+1160, U+3164, U+FFA0), which shows as nothing.
func hidden(s string) bool {
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\U0001f3f4':
			i += flagTags(rs[i+1:])
		case r == '\u200c', r == '\u200d':
		case r == '\u180e':
			if i == 0 || i == len(rs)-1 || !mongolian(rs[i-1]) || !mongolian(rs[i+1]) {
				return true
			}
		case variationSelector(r):
			if i == 0 || !base(rs[i-1]) {
				return true
			}
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r),
			hangulFiller(r):
			return true
		}
	}
	return false
}

// shows says whether s has something to see: a letter, number or symbol, not counting the braille blank or the Hangul
// fillers. Marks, joiners and spaces alone show as blank.
func shows(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		return (unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsSymbol(r)) && r != '\u2800' && !hangulFiller(r)
	})
}

func hangulFiller(r rune) bool {
	return r == '\u115f' || r == '\u1160' || r == '\u3164' || r == '\uffa0'
}

func variationSelector(r rune) bool {
	return r >= '\ufe00' && r <= '\ufe0f' || r >= '\U000e0100' && r <= '\U000e01ef' || r >= '\u180b' && r <= '\u180d' || r == '\u180f'
}

// base is a character a variation selector can follow: a letter, number, symbol or punctuation mark.
func base(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsSymbol(r) || unicode.IsPunct(r)
}

func mongolian(r rune) bool { return unicode.Is(unicode.Mongolian, r) && unicode.IsLetter(r) }

// flagTags is how many runes at the start of rs are a subdivision flag's tags: a subdivision ID (2 tag letters or 3
// tag digits, then 1 to 4 tag letters or digits) and the cancel tag. None if they aren't.
func flagTags(rs []rune) int {
	letter := func(i int) bool { return i < len(rs) && rs[i] >= '\U000e0061' && rs[i] <= '\U000e007a' }
	digit := func(i int) bool { return i < len(rs) && rs[i] >= '\U000e0030' && rs[i] <= '\U000e0039' }
	var n int
	switch {
	case letter(0) && letter(1):
		n = 2
	case digit(0) && digit(1) && digit(2):
		n = 3
	default:
		return 0
	}
	sub := 0
	for sub < 4 && (letter(n+sub) || digit(n+sub)) {
		sub++
	}
	if end := n + sub; sub > 0 && end < len(rs) && rs[end] == '\U000e007f' {
		return end + 1
	}
	return 0
}

// isMail is a bare address, name@domain with a dot in the domain.
func isMail(s string) bool {
	p, err := mail.ParseAddress(s)
	return err == nil && p.Address == s && p.Name == "" && strings.Contains(s[strings.LastIndex(s, "@"):], ".")
}
