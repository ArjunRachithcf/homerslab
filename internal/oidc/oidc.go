// Package oidc is the relying party side of OpenID Connect that HomersLab's own surfaces use to sign people in with
// the household account: the authorization code flow with PKCE, a state and a nonce, and the ID token checked here
// (its signature from the issuer's keys, the issuer, the audience, its lifetime and the nonce) before anything in it
// is believed. Admin is the first such client (#17).
//
// Nothing else is taken from the provider's answers: no claim names how someone signed in (`amr` isn't relied on,
// ADR 0004 amended), so a client that needs a passkey gets it from the provider's own flow.
package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/mesh"
)

// Client is one relying party: its credentials at the issuer, and where the issuer sends people back.
type Client struct {
	Issuer   string
	ID       string
	Secret   mesh.Secret
	Redirect string
	// HTTP reaches the issuer's server; http.DefaultClient's transport when nil. On the primary, the manager reaches
	// the sign-in at its upstream (Through).
	HTTP *http.Client
}

// Endpoints are the issuer's, from its discovery document.
type Endpoints struct {
	Issuer    string `json:"issuer"`
	Authorize string `json:"authorization_endpoint"`
	Token     string `json:"token_endpoint"`
	Keys      string `json:"jwks_uri"`
}

// Errors of a sign-in, so a surface can say what went wrong without saying more.
var (
	ErrRefused = errors.New("the sign-in was refused") // a bad state, code, token or claim
)

func (c Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// Discover reads the issuer's discovery document; its issuer must be exactly the client's.
func (c Client) Discover(ctx context.Context) (Endpoints, error) {
	var e Endpoints
	if err := c.get(ctx, strings.TrimSuffix(c.Issuer, "/")+"/.well-known/openid-configuration", &e); err != nil {
		return e, err
	}
	if e.Issuer != c.Issuer || e.Authorize == "" || e.Token == "" || e.Keys == "" {
		return e, fmt.Errorf("%w: the discovery document names issuer %q, want %q", ErrRefused, e.Issuer, c.Issuer)
	}
	return e, nil
}

func (c Client) get(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the issuer answered GET %s with %d", u, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// Attempt is one sign-in under way: what the browser carries to the issuer, and what the relying party keeps until
// the browser comes back. State, Nonce and Verifier are secrets of the attempt.
type Attempt struct {
	State, Nonce, Verifier string
}

// NewAttempt makes a sign-in's state, nonce and PKCE verifier: 256 random bits each.
func NewAttempt() Attempt {
	r := func() string {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return Attempt{State: r(), Nonce: r(), Verifier: r()}
}

// AuthorizeURL is where the browser goes to sign in: the code flow, PKCE with S256. A fresh sign-in (step-up later)
// adds prompt=login and max_age=0.
func (c Client) AuthorizeURL(e Endpoints, a Attempt, fresh bool) string {
	sum := sha256.Sum256([]byte(a.Verifier))
	v := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.ID},
		"redirect_uri":          {c.Redirect},
		"scope":                 {"openid profile email"},
		"state":                 {a.State},
		"nonce":                 {a.Nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	if fresh {
		v.Set("prompt", "login")
		v.Set("max_age", "0")
	}
	sep := "?"
	if strings.Contains(e.Authorize, "?") {
		sep = "&"
	}
	return e.Authorize + sep + v.Encode()
}

// Claims are what HomersLab takes from a verified ID token.
type Claims struct {
	Subject  string    `json:"sub"`
	Username string    `json:"preferred_username"`
	Name     string    `json:"name"`
	Mail     string    `json:"email"`
	Groups   []string  `json:"groups"`
	AuthTime time.Time `json:"-"`
}

// Finish takes the browser's return: the state must be the attempt's, the code is exchanged (with the client's secret
// and the PKCE verifier), and the ID token verified. Any failure is ErrRefused, saying why only in the error chain.
func (c Client) Finish(ctx context.Context, e Endpoints, a Attempt, state, code string, now time.Time) (Claims, error) {
	if state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(a.State)) != 1 {
		return Claims{}, fmt.Errorf("%w: the state isn't this sign-in's", ErrRefused)
	}
	if code == "" {
		return Claims{}, fmt.Errorf("%w: no code", ErrRefused)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {c.Redirect},
		"code_verifier": {a.Verifier}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return Claims{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(c.ID), url.QueryEscape(c.Secret.Reveal()))
	resp, err := c.http().Do(req)
	if err != nil {
		return Claims{}, err
	}
	defer resp.Body.Close()
	var tok struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok)
	if resp.StatusCode != http.StatusOK || tok.IDToken == "" {
		return Claims{}, fmt.Errorf("%w: the token endpoint answered %d %s", ErrRefused, resp.StatusCode, tok.Error)
	}
	return c.Verify(ctx, e, tok.IDToken, a.Nonce, now)
}

// Verify checks an ID token: its signature with one of the issuer's keys (RS256 or ES256, never "none" or a shared
// secret), its issuer, that the client is its audience (and authorized party), that it is within its lifetime, and
// its nonce.
func (c Client) Verify(ctx context.Context, e Endpoints, token, nonce string, now time.Time) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, fmt.Errorf("%w: not a signed token", ErrRefused)
	}
	var head struct {
		Alg, Kid string
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(hb, &head) != nil {
		return Claims{}, fmt.Errorf("%w: its header", ErrRefused)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, fmt.Errorf("%w: its signature", ErrRefused)
	}
	var set struct{ Keys []jwk }
	if err := c.get(ctx, e.Keys, &set); err != nil {
		return Claims{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	ok := false
	for _, k := range set.Keys {
		if head.Kid != "" && k.Kid != head.Kid {
			continue
		}
		if k.verify(head.Alg, digest[:], sig) {
			ok = true
			break
		}
	}
	if !ok {
		return Claims{}, fmt.Errorf("%w: no key of the issuer's signed it (alg %q)", ErrRefused, head.Alg)
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, fmt.Errorf("%w: its claims", ErrRefused)
	}
	var raw struct {
		Claims
		Issuer   string          `json:"iss"`
		Audience json.RawMessage `json:"aud"`
		AZP      string          `json:"azp"`
		Exp      float64         `json:"exp"`
		Iat      float64         `json:"iat"`
		Auth     float64         `json:"auth_time"`
		Nonce    string          `json:"nonce"`
	}
	if err := json.Unmarshal(pb, &raw); err != nil {
		return Claims{}, fmt.Errorf("%w: its claims", ErrRefused)
	}
	var aud []string
	if json.Unmarshal(raw.Audience, &aud) != nil {
		var one string
		if json.Unmarshal(raw.Audience, &one) == nil {
			aud = []string{one}
		}
	}
	const skew = time.Minute
	switch {
	case raw.Issuer != c.Issuer:
		return Claims{}, fmt.Errorf("%w: issued by %q", ErrRefused, raw.Issuer)
	case !slices.Contains(aud, c.ID) || (len(aud) > 1 && raw.AZP != c.ID):
		return Claims{}, fmt.Errorf("%w: not for this client", ErrRefused)
	case raw.Exp == 0 || now.After(time.Unix(int64(raw.Exp), 0).Add(skew)):
		return Claims{}, fmt.Errorf("%w: expired", ErrRefused)
	case raw.Iat != 0 && time.Unix(int64(raw.Iat), 0).After(now.Add(skew)):
		return Claims{}, fmt.Errorf("%w: issued in the future", ErrRefused)
	case nonce == "" || subtle.ConstantTimeCompare([]byte(raw.Nonce), []byte(nonce)) != 1:
		return Claims{}, fmt.Errorf("%w: the nonce isn't this sign-in's", ErrRefused)
	case raw.Subject == "":
		return Claims{}, fmt.Errorf("%w: no subject", ErrRefused)
	}
	cl := raw.Claims
	if raw.Auth > 0 {
		cl.AuthTime = time.Unix(int64(raw.Auth), 0)
	}
	return cl, nil
}

// jwk is one of the issuer's public keys.
type jwk struct {
	Kty, Kid, Alg, Use string
	N, E               string
	Crv, X, Y          string
}

func (k jwk) verify(alg string, digest, sig []byte) bool {
	if k.Use != "" && k.Use != "sig" {
		return false
	}
	b := func(s string) *big.Int {
		v, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			return nil
		}
		return new(big.Int).SetBytes(v)
	}
	switch {
	case alg == "RS256" && k.Kty == "RSA":
		n, e := b(k.N), b(k.E)
		if n == nil || e == nil || !e.IsInt64() || n.BitLen() < 2048 {
			return false
		}
		return rsa.VerifyPKCS1v15(&rsa.PublicKey{N: n, E: int(e.Int64())}, crypto.SHA256, digest, sig) == nil
	case alg == "ES256" && k.Kty == "EC" && k.Crv == "P-256":
		x, y := b(k.X), b(k.Y)
		if x == nil || y == nil || len(sig) != 64 {
			return false
		}
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}
		return ecdsa.Verify(pub, digest, new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
	}
	return false
}

// Through is a transport that reaches an issuer's HTTPS origin at a local upstream instead, over plain HTTP, keeping
// the request's Host and saying it came over HTTPS: the manager's way to the sign-in on its own machine, at the
// sign-in network's address, where the edge reaches it too. Only the origin's own host is sent there.
func Through(origin, upstream string) http.RoundTripper {
	o, err1 := url.Parse(origin)
	u, err2 := url.Parse(upstream)
	return roundTrip(func(r *http.Request) (*http.Response, error) {
		if err1 != nil || err2 != nil || r.URL.Scheme != "https" || r.URL.Host != o.Host {
			return nil, fmt.Errorf("oidc: %s isn't the sign-in's origin", r.URL.Redacted())
		}
		out := r.Clone(r.Context())
		out.URL.Scheme, out.URL.Host = u.Scheme, u.Host
		out.Host = o.Host
		out.Header.Set("X-Forwarded-Proto", "https")
		return http.DefaultTransport.RoundTrip(out)
	})
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
