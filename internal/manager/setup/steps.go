package setup

import (
	"context"
	"encoding/json"
	"net/mail"
	"strings"
	"unicode/utf8"
)

// Built are the steps this version of HomersLab can take. The rest come with the tickets that build them.
var Built = map[string]Handler{
	"welcome":   welcome,
	"you":       you,
	"household": household,
}

func welcome(context.Context, json.RawMessage, map[string]string) (map[string]string, error) {
	return nil, nil
}

// you: the admin's name and mail address.
func you(_ context.Context, raw json.RawMessage, _ map[string]string) (map[string]string, error) {
	var a struct{ Name, Mail string }
	_ = json.Unmarshal(raw, &a)
	var bad []string
	name := strings.TrimSpace(a.Name)
	if !plain(name) {
		bad = append(bad, "name")
	}
	mailAddr := strings.TrimSpace(a.Mail)
	if p, err := mail.ParseAddress(mailAddr); err != nil || p.Address != mailAddr || p.Name != "" || !strings.Contains(mailAddr[strings.LastIndex(mailAddr, "@"):], ".") {
		bad = append(bad, "mail")
	}
	if bad != nil {
		return nil, &Invalid{Fields: bad}
	}
	return map[string]string{"name": name, "mail": mailAddr}, nil
}

// household: the household's name.
func household(_ context.Context, raw json.RawMessage, _ map[string]string) (map[string]string, error) {
	var a struct{ Name string }
	_ = json.Unmarshal(raw, &a)
	name := strings.TrimSpace(a.Name)
	if !plain(name) {
		return nil, &Invalid{Fields: []string{"name"}}
	}
	return map[string]string{"household": name}, nil
}

// plain is a name someone typed: not empty, not huge, one line.
func plain(s string) bool {
	return s != "" && utf8.RuneCountInString(s) <= 120 && !strings.ContainsAny(s, "\r\n\x00")
}
