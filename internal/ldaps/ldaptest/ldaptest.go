// Package ldaptest is an LDAP client for tests: a simple bind (RFC 4511 §4.2) over a connection the test opens, plain
// or TLS, and a stand-in directory that answers binds. It holds no dependency: the few BER bytes a bind needs are
// written here.
package ldaptest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// Success and InvalidCredentials are RFC 4511's result codes a bind's answer most often has; InsufficientAccess is what
// Authentik's outpost answers someone it refuses.
const (
	Success            = 0
	InvalidCredentials = 49
	InsufficientAccess = 50
)

// ErrRefused is a bind the directory answered with another result than success.
var ErrRefused = errors.New("ldap: bind refused")

// Bind binds as dn with password over c, and unbinds. It returns nil only when the directory answered success.
func Bind(c net.Conn, dn, password string) error {
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	req := tlv(0x30, cat(tlv(0x02, []byte{1}), tlv(0x60, cat(tlv(0x02, []byte{3}), tlv(0x04, []byte(dn)), tlv(0x80, []byte(password))))))
	if _, err := c.Write(req); err != nil {
		return err
	}
	tag, body, err := read(bufio.NewReader(c))
	if err != nil {
		return err
	}
	code, err := result(tag, body, 0x61)
	_, _ = c.Write(tlv(0x30, cat(tlv(0x02, []byte{2}), []byte{0x42, 0x00})))
	if err != nil {
		return err
	}
	if code != Success {
		return fmt.Errorf("%w: result %d", ErrRefused, code)
	}
	return nil
}

// Entry is one object a search found: its DN, and its attributes' values.
type Entry struct {
	DN    string
	Attrs map[string][]string
}

// Search binds as dn with password over c, then searches the whole subtree of base for every object (objectClass=*),
// asking for every attribute, and returns what came back. A refused bind is ErrRefused; a search the directory refuses
// is an error with its result code.
func Search(c net.Conn, dn, password, base string) ([]Entry, error) {
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	r := bufio.NewReader(c)
	bind := tlv(0x30, cat(tlv(0x02, []byte{1}), tlv(0x60, cat(tlv(0x02, []byte{3}), tlv(0x04, []byte(dn)), tlv(0x80, []byte(password))))))
	if _, err := c.Write(bind); err != nil {
		return nil, err
	}
	tag, body, err := read(r)
	if err != nil {
		return nil, err
	}
	if code, err := result(tag, body, 0x61); err != nil {
		return nil, err
	} else if code != Success {
		return nil, fmt.Errorf("%w: result %d", ErrRefused, code)
	}
	req := tlv(0x63, cat(tlv(0x04, []byte(base)), tlv(0x0a, []byte{2}), tlv(0x0a, []byte{0}), tlv(0x02, []byte{0}),
		tlv(0x02, []byte{0}), tlv(0x01, []byte{0}), tlv(0x87, []byte("objectClass")), tlv(0x30, nil)))
	if _, err := c.Write(tlv(0x30, cat(tlv(0x02, []byte{2}), req))); err != nil {
		return nil, err
	}
	var out []Entry
	for {
		tag, body, err := read(r)
		if err != nil || tag != 0x30 {
			return out, fmt.Errorf("ldap: the search's answer: %v", err)
		}
		_, _, rest, err := next(body)
		if err != nil {
			return out, err
		}
		op, v, _, err := next(rest)
		if err != nil {
			return out, err
		}
		switch op {
		case 0x64: // an entry
			_, name, attrs, err := next(v)
			if err != nil {
				return out, err
			}
			_, list, _, _ := next(attrs)
			e := Entry{DN: string(name), Attrs: map[string][]string{}}
			for len(list) > 0 {
				var one []byte
				if _, one, list, err = next(list); err != nil {
					return out, err
				}
				_, typ, vals, _ := next(one)
				_, set, _, _ := next(vals)
				for len(set) > 0 {
					var val []byte
					if _, val, set, err = next(set); err != nil {
						return out, err
					}
					e.Attrs[string(typ)] = append(e.Attrs[string(typ)], string(val))
				}
			}
			out = append(out, e)
		case 0x73: // a referral elsewhere: not followed
		case 0x65: // done
			_, code, _, err := next(v)
			if err != nil || len(code) != 1 {
				return out, errors.New("ldap: no result code")
			}
			_, _ = c.Write(tlv(0x30, cat(tlv(0x02, []byte{3}), []byte{0x42, 0x00})))
			if code[0] != Success {
				return out, fmt.Errorf("ldap: search: result %d", code[0])
			}
			return out, nil
		default:
			return out, fmt.Errorf("ldap: answered with operation %#x", op)
		}
	}
}

// result is the result code of an LDAPMessage whose operation is op.
func result(tag byte, body []byte, op byte) (int, error) {
	if tag != 0x30 {
		return 0, errors.New("ldap: not a message")
	}
	_, _, rest, err := next(body) // messageID
	if err != nil {
		return 0, err
	}
	t, v, _, err := next(rest)
	if err != nil || t != op {
		return 0, fmt.Errorf("ldap: answered with operation %#x", t)
	}
	t, code, _, err := next(v)
	if err != nil || t != 0x0a || len(code) != 1 {
		return 0, errors.New("ldap: no result code")
	}
	return int(code[0]), nil
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func tlv(tag byte, v []byte) []byte {
	n := len(v)
	switch {
	case n < 0x80:
		return append([]byte{tag, byte(n)}, v...)
	case n < 0x100:
		return append([]byte{tag, 0x81, byte(n)}, v...)
	default:
		return append([]byte{tag, 0x82, byte(n >> 8), byte(n)}, v...)
	}
}

// next splits the first TLV off b.
func next(b []byte) (byte, []byte, []byte, error) {
	if len(b) < 2 {
		return 0, nil, nil, io.ErrUnexpectedEOF
	}
	tag, n, i := b[0], int(b[1]), 2
	if n&0x80 != 0 {
		k := n & 0x7f
		if k == 0 || k > 2 || len(b) < 2+k {
			return 0, nil, nil, errors.New("ldap: bad length")
		}
		n = 0
		for _, x := range b[2 : 2+k] {
			n = n<<8 | int(x)
		}
		i += k
	}
	if len(b) < i+n {
		return 0, nil, nil, io.ErrUnexpectedEOF
	}
	return tag, b[i : i+n], b[i+n:], nil
}

// read reads one TLV from r.
func read(r *bufio.Reader) (byte, []byte, error) {
	tag, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	l, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	n := int(l)
	if l&0x80 != 0 {
		k := int(l & 0x7f)
		if k == 0 || k > 3 {
			return 0, nil, errors.New("ldap: bad length")
		}
		n = 0
		for range k {
			x, err := r.ReadByte()
			if err != nil {
				return 0, nil, err
			}
			n = n<<8 | int(x)
		}
	}
	if n > 1<<20 {
		return 0, nil, errors.New("ldap: message too long")
	}
	body := make([]byte, n)
	_, err = io.ReadFull(r, body)
	return tag, body, err
}

// Directory is a stand-in LDAP server that answers binds: success for its one DN and password, invalid credentials
// for anything else. It serves until ln closes.
func Directory(ln net.Listener, dn, password string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(30 * time.Second))
			r := bufio.NewReader(c)
			for {
				tag, body, err := read(r)
				if err != nil || tag != 0x30 {
					return
				}
				_, id, rest, err := next(body)
				if err != nil {
					return
				}
				op, v, _, err := next(rest)
				if err != nil || op != 0x60 {
					return // an unbind, or anything else: the end
				}
				_, _, v, _ = next(v) // version
				_, gotDN, v, err1 := next(v)
				_, gotPW, _, err2 := next(v)
				code := byte(InvalidCredentials)
				if err1 == nil && err2 == nil && string(gotDN) == dn && string(gotPW) == password {
					code = Success
				}
				_, _ = c.Write(tlv(0x30, cat(tlv(0x02, id), tlv(0x61, cat(tlv(0x0a, []byte{code}), tlv(0x04, nil), tlv(0x04, nil))))))
			}
		}()
	}
}
