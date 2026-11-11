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
	case strings.ContainsFunc(s, hidden):
		return "", HiddenCharacters
	case utf8.RuneCountInString(s) > max:
		return "", TooLong
	}
	return s, ""
}

// hidden is a control character (C0, DEL, C1) or a bidi override or isolate (U+202A–U+202E, U+2066–U+2069).
func hidden(r rune) bool {
	return unicode.IsControl(r) || r >= '\u202a' && r <= '\u202e' || r >= '\u2066' && r <= '\u2069'
}

// isMail is a bare address, name@domain with a dot in the domain.
func isMail(s string) bool {
	p, err := mail.ParseAddress(s)
	return err == nil && p.Address == s && p.Name == "" && strings.Contains(s[strings.LastIndex(s, "@"):], ".")
}
