package setup

import (
	"bytes"
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
	var a struct {
		Name string `json:"name"`
	}
	if !decode(raw, &a) {
		return nil, &Invalid{Fields: []string{"answer"}}
	}
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
