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
	case utf8.RuneCountInString(s) > max:
		return "", TooLong
	}
	return s, ""
}

// hidden says whether s holds a character that changes how the text around it reads or breaks, or that looks blank:
//   - a control character (C0, DEL, C1);
//   - a format character (Unicode's Cf: the bidi overrides, isolates and marks, zero-width spaces, the BOM, the soft
//     hyphen, tags and the rest), but for the zero-width non-joiner and joiner (U+200C, U+200D), which Persian,
//     Indic scripts and emoji need, and the tags of a subdivision flag (U+1F3F4, then tag letters and digits, then
//     the cancel tag U+E007F), such as Scotland's;
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
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r),
			r == '\u115f', r == '\u1160', r == '\u3164', r == '\uffa0':
			return true
		}
	}
	return false
}

// flagTags is how many runes at the start of rs are a subdivision flag's tags: tag letters and digits, then the
// cancel tag. None if they aren't.
func flagTags(rs []rune) int {
	n := 0
	for n < len(rs) && (rs[n] >= '\U000e0061' && rs[n] <= '\U000e007a' || rs[n] >= '\U000e0030' && rs[n] <= '\U000e0039') {
		n++
	}
	if n == 0 || n >= len(rs) || rs[n] != '\U000e007f' {
		return 0
	}
	return n + 1
}

// isMail is a bare address, name@domain with a dot in the domain.
func isMail(s string) bool {
	p, err := mail.ParseAddress(s)
	return err == nil && p.Address == s && p.Name == "" && strings.Contains(s[strings.LastIndex(s, "@"):], ".")
}
