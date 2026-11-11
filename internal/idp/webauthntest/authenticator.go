// Package webauthntest is a software authenticator for tests: it answers a relying party's WebAuthn registration and
// assertion options the way a browser and a passkey would, as JSON in the shape of PublicKeyCredential.toJSON(). Its
// knobs make the answers a real browser would never send (no user verification, another origin), so a contract test
// can check that the relying party refuses them.
//
// It is never used outside tests. The real browser's side is the flow test with Chromium's virtual authenticator.
package webauthntest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// Authenticator holds passkeys, one key pair per credential, as a phone or a security key would.
type Authenticator struct {
	// Origin is the page the browser says it is on: https://auth.<domain>[:port].
	Origin string
	// Verified says the authenticator checked the person (a PIN, a fingerprint): the UV flag. A security key without
	// a PIN, or a touch alone, leaves it off.
	Verified bool
	creds    map[string]*cred
}

type cred struct {
	id     []byte
	key    *ecdsa.PrivateKey
	rpID   string
	user   []byte
	signed uint32
}

// New is an authenticator on origin that verifies the person.
func New(origin string) *Authenticator {
	return &Authenticator{Origin: origin, Verified: true, creds: map[string]*cred{}}
}

// CreationOptions is what a relying party sends to navigator.credentials.create(), as JSON.
type CreationOptions struct {
	Challenge string `json:"challenge"`
	RP        struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"rp"`
	User struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"user"`
	AuthenticatorSelection struct {
		UserVerification string `json:"userVerification"`
		ResidentKey      string `json:"residentKey"`
	} `json:"authenticatorSelection"`
}

// RequestOptions is what a relying party sends to navigator.credentials.get(), as JSON.
type RequestOptions struct {
	Challenge        string `json:"challenge"`
	RPID             string `json:"rpId"`
	UserVerification string `json:"userVerification"`
	AllowCredentials []struct {
		ID string `json:"id"`
	} `json:"allowCredentials"`
}

var b64 = base64.RawURLEncoding

// Create makes a new passkey for the options and answers as navigator.credentials.create() would. The RP ID is the
// one the options name, as a browser takes it.
func (a *Authenticator) Create(o CreationOptions) (map[string]any, error) {
	if o.RP.ID == "" || o.Challenge == "" {
		return nil, errors.New("the options name no RP ID or challenge")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	user, _ := b64.DecodeString(o.User.ID)
	c := &cred{id: id, key: key, rpID: o.RP.ID, user: user}
	a.creds[string(id)] = c

	pub, err := key.PublicKey.Bytes() // uncompressed point: 0x04 | X | Y
	if err != nil {
		return nil, err
	}
	cose := cbor{}.mapOf(
		pair{int64(1), int64(2)},   // kty: EC2
		pair{int64(3), int64(-7)},  // alg: ES256
		pair{int64(-1), int64(1)},  // crv: P-256
		pair{int64(-2), pub[1:33]}, // x
		pair{int64(-3), pub[33:]},  // y
	)
	var attested []byte
	attested = append(attested, make([]byte, 16)...) // AAGUID: none
	attested = binary.BigEndian.AppendUint16(attested, uint16(len(id)))
	attested = append(attested, id...)
	attested = append(attested, cose...)
	auth := a.authData(c, 0x40, attested) // AT: attested credential data included
	att := cbor{}.mapOf(pair{"fmt", "none"}, pair{"attStmt", cbor{}.mapOf()}, pair{"authData", auth})
	client, _ := json.Marshal(map[string]any{"type": "webauthn.create", "challenge": o.Challenge, "origin": a.Origin, "crossOrigin": false})
	return map[string]any{
		"id": b64.EncodeToString(id), "rawId": b64.EncodeToString(id), "type": "public-key",
		"authenticatorAttachment": "platform", "clientExtensionResults": map[string]any{},
		"response": map[string]any{
			"clientDataJSON": b64.EncodeToString(client), "attestationObject": b64.EncodeToString(att),
			"transports": []string{"internal"},
		},
	}, nil
}

// Get signs an assertion for the options with a passkey it holds for their RP ID, as navigator.credentials.get()
// would; it fails when it holds none.
func (a *Authenticator) Get(o RequestOptions) (map[string]any, error) {
	var c *cred
	for _, ac := range o.AllowCredentials {
		id, _ := b64.DecodeString(ac.ID)
		if have := a.creds[string(id)]; have != nil && have.rpID == o.RPID {
			c = have
		}
	}
	if c == nil && len(o.AllowCredentials) == 0 { // a discoverable credential: any for the RP ID
		for _, have := range a.creds {
			if have.rpID == o.RPID {
				c = have
			}
		}
	}
	if c == nil {
		return nil, fmt.Errorf("no passkey for %q", o.RPID)
	}
	c.signed++
	auth := a.authData(c, 0, nil)
	client, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": o.Challenge, "origin": a.Origin, "crossOrigin": false})
	sum := sha256.Sum256(client)
	digest := sha256.Sum256(append(append([]byte{}, auth...), sum[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, c.key, digest[:])
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id": b64.EncodeToString(c.id), "rawId": b64.EncodeToString(c.id), "type": "public-key",
		"authenticatorAttachment": "platform", "clientExtensionResults": map[string]any{},
		"response": map[string]any{
			"clientDataJSON": b64.EncodeToString(client), "authenticatorData": b64.EncodeToString(auth),
			"signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(c.user),
		},
	}, nil
}

// Passkeys is how many passkeys it holds.
func (a *Authenticator) Passkeys() int { return len(a.creds) }

// Assertions is how many assertions its passkeys have signed: how often the person was asked for one.
func (a *Authenticator) Assertions() int {
	n := 0
	for _, c := range a.creds {
		n += int(c.signed)
	}
	return n
}

// authData is the authenticator data: the RP ID's hash, the flags, the counter and, at registration, the credential.
func (a *Authenticator) authData(c *cred, flags byte, attested []byte) []byte {
	rp := sha256.Sum256([]byte(c.rpID))
	flags |= 0x01 // UP: the person was present
	if a.Verified {
		flags |= 0x04 // UV
	}
	out := append(rp[:], flags)
	out = binary.BigEndian.AppendUint32(out, c.signed)
	return append(out, attested...)
}

// cbor writes the few CBOR shapes WebAuthn needs: maps of ints, strings and byte strings, in the order given.
type cbor struct{}

type pair struct{ k, v any }

func (cbor) head(major byte, n uint64) []byte {
	switch {
	case n < 24:
		return []byte{major<<5 | byte(n)}
	case n < 1<<8:
		return []byte{major<<5 | 24, byte(n)}
	case n < 1<<16:
		return binary.BigEndian.AppendUint16([]byte{major<<5 | 25}, uint16(n))
	default:
		return binary.BigEndian.AppendUint32([]byte{major<<5 | 26}, uint32(n))
	}
}

func (e cbor) item(v any) []byte {
	switch x := v.(type) {
	case int64:
		if x >= 0 {
			return e.head(0, uint64(x))
		}
		return e.head(1, uint64(-1-x))
	case string:
		return append(e.head(3, uint64(len(x))), x...)
	case []byte:
		return append(e.head(2, uint64(len(x))), x...)
	case raw:
		return x
	}
	panic(fmt.Sprintf("webauthntest: no CBOR for %T", v))
}

type raw []byte

// raw is CBOR already encoded: a nested map.
func (e cbor) mapOf(ps ...pair) raw {
	out := e.head(5, uint64(len(ps)))
	for _, p := range ps {
		out = append(out, e.item(p.k)...)
		out = append(out, e.item(p.v)...)
	}
	return out
}
