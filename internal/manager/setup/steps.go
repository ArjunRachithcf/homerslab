package setup

import (
	"context"
	"encoding/json"
)

// Built are the steps this version of HomersLab can take. The rest come with the tickets that build them.
var Built = map[string]Handler{
	"welcome":   welcome,
	"you":       you,
	"household": household,
}

// The limits of what someone types. A name is counted in code points, so an accent or an emoji can count as more
// than one; a mail address is RFC 5321's longest, in octets.
const (
	NameMax = 64
	MailMax = 254
)

// Why a field's answer was refused.
const (
	Missing          = "missing"
	TooLong          = "too-long"
	HiddenCharacters = "hidden-characters" // an invisible character, or one a name can't use
	NotAMail         = "not-a-mail"
)

func welcome(context.Context, json.RawMessage) (map[string]string, error) { return nil, nil }

// you: the admin's name and mail address.
func you(_ context.Context, raw json.RawMessage) (map[string]string, error) {
	var a struct{ Name, Mail string }
	_ = json.Unmarshal(raw, &a)
	bad := &Invalid{}
	n, why := name(a.Name)
	bad.add("name", why)
	addr, why := mailAddress(a.Mail)
	bad.add("mail", why)
	if bad.Fields != nil {
		return nil, bad
	}
	return map[string]string{"name": n, "mail": addr}, nil
}

// household: the household's name.
func household(_ context.Context, raw json.RawMessage) (map[string]string, error) {
	var a struct{ Name string }
	_ = json.Unmarshal(raw, &a)
	n, why := name(a.Name)
	if why != "" {
		bad := &Invalid{}
		bad.add("name", why)
		return nil, bad
	}
	return map[string]string{"household": n}, nil
}
