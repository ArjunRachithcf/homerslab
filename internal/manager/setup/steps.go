package setup

import (
	"bytes"
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
	TooManyMarks     = "too-many-marks"    // more than MarksMax combining marks on one letter
	NotAMail         = "not-a-mail"
)

func welcome(_ context.Context, raw json.RawMessage, _ map[string]string) (map[string]string, error) {
	var a struct{}
	if !decode(raw, &a) {
		return nil, &Invalid{Fields: []string{"answer"}}
	}
	return nil, nil
}

// decode reads an answer into a, refusing any field a doesn't have.
func decode(raw json.RawMessage, a any) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(a) == nil
}

// you: the admin's name and mail address.
func you(_ context.Context, raw json.RawMessage, _ map[string]string) (map[string]string, error) {
	var a struct {
		Name string `json:"name"`
		Mail string `json:"mail"`
	}
	if !decode(raw, &a) {
		return nil, &Invalid{Fields: []string{"answer"}}
	}
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
func household(_ context.Context, raw json.RawMessage, _ map[string]string) (map[string]string, error) {
	var a struct {
		Name string `json:"name"`
	}
	if !decode(raw, &a) {
		return nil, &Invalid{Fields: []string{"answer"}}
	}
	n, why := name(a.Name)
	if why != "" {
		bad := &Invalid{}
		bad.add("name", why)
		return nil, bad
	}
	return map[string]string{"household": n}, nil
}
