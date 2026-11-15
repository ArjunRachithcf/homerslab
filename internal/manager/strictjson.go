package manager

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// errNotExact is a change whose JSON doesn't say exactly one thing.
var errNotExact = errors.New("not-exact")

// decodeExact decodes a change into v only when the JSON says exactly what v holds, so a digest of v names that
// change and no other (#25: a step-up approves one change). Go's decoder matches keys without regard to case, takes
// the last of two equal keys, and leaves unknown ones; here, a key twice in one object (in any case), a key that isn't
// v's own spelling, an unknown key or a number v doesn't hold exactly refuses it.
func decodeExact(raw []byte, v any) error {
	if err := noDuplicateKeys(raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errNotExact
	}
	if dec.More() {
		return errNotExact
	}
	out, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var said, holds any
	if numbers(raw, &said) != nil || numbers(out, &holds) != nil || !same(said, holds) {
		return errNotExact
	}
	return nil
}

func numbers(b []byte, v *any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return d.Decode(v)
}

// same says the JSON sent (said) is what was decoded (holds): every key spelt as v spells it, every value the same,
// and a key on one side only where its value is empty.
func same(said, holds any) bool {
	switch s := said.(type) {
	case map[string]any:
		h, ok := holds.(map[string]any)
		if !ok {
			return false
		}
		for k, sv := range s {
			hv, there := h[k]
			if !there {
				if !empty(sv) {
					return false
				}
				continue
			}
			if !same(sv, hv) {
				return false
			}
		}
		for k, hv := range h {
			if _, there := s[k]; !there && !empty(hv) {
				return false
			}
		}
		return true
	case []any:
		h, ok := holds.([]any)
		if !ok || len(h) != len(s) {
			return false
		}
		for i := range s {
			if !same(s[i], h[i]) {
				return false
			}
		}
		return true
	case json.Number:
		h, ok := holds.(json.Number)
		return ok && s == h
	default:
		return said == holds
	}
}

func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case bool:
		return !x
	case json.Number:
		return x == "0"
	case map[string]any:
		return len(x) == 0
	case []any:
		return len(x) == 0
	}
	return false
}

// noDuplicateKeys refuses an object with a key twice, in any case.
func noDuplicateKeys(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	type frame struct {
		object bool
		keys   map[string]bool
		key    bool // the next token in this object is a key
	}
	var stack []*frame
	for {
		tok, err := d.Token()
		if err != nil {
			if len(stack) == 0 && errors.Is(err, io.EOF) {
				return nil
			}
			return errNotExact
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if top != nil && top.object && top.key {
			if k, ok := tok.(string); ok {
				if top.keys[strings.ToLower(k)] {
					return errNotExact
				}
				top.keys[strings.ToLower(k)] = true
				top.key = false
				continue
			}
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, &frame{object: true, keys: map[string]bool{}, key: true})
			continue
		case json.Delim('['):
			stack = append(stack, &frame{})
			continue
		case json.Delim('}'), json.Delim(']'):
			stack = stack[:len(stack)-1]
		}
		// A value ended: in an object, a key comes next.
		if len(stack) > 0 && stack[len(stack)-1].object {
			stack[len(stack)-1].key = true
		}
		if len(stack) == 0 {
			if d.More() {
				return errNotExact
			}
			return nil
		}
	}
}
