package oidc_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/oidc"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

var now = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

type issuer struct {
	key, other *rsa.PrivateKey
	srv        *httptest.Server
}

func newIssuer(t *testing.T) *issuer {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	o, _ := rsa.GenerateKey(rand.Reader, 2048)
	is := &issuer{key: k, other: o}
	is.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())}}})
	}))
	t.Cleanup(is.srv.Close)
	return is
}

func (is *issuer) token(head map[string]string, claims map[string]any, key *rsa.PrivateKey) string {
	h, _ := json.Marshal(head)
	c, _ := json.Marshal(claims)
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	if key == nil {
		return in + "."
	}
	sum := sha256.Sum256([]byte(in))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// An ID token is believed only when everything about it is checked: its signature by one of the issuer's keys, never
// "none"; the issuer; the audience; its lifetime; and the nonce of this sign-in.
func TestAnIDTokenIsCheckedBeforeAnythingInItIsBelieved(t *testing.T) {
	testenv.Isolate(t)
	is := newIssuer(t)
	c := oidc.Client{Issuer: "https://auth.example.test/application/o/homerslab-admin/", ID: "admin"}
	e := oidc.Endpoints{Keys: is.srv.URL}
	good := func() map[string]any {
		return map[string]any{"iss": c.Issuer, "aud": "admin", "sub": "s1", "preferred_username": "ines", "groups": []string{"homerslab-admins"},
			"nonce": "n1", "exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix()}
	}
	rs := map[string]string{"alg": "RS256", "kid": "k1"}
	cl, err := c.Verify(context.Background(), e, is.token(rs, good(), is.key), "n1", now)
	if err != nil || cl.Username != "ines" || cl.Groups[0] != "homerslab-admins" {
		t.Fatalf("a good token: %+v %v", cl, err)
	}
	for name, tok := range map[string]string{
		"another key's signature": is.token(rs, good(), is.other),
		"alg none":                is.token(map[string]string{"alg": "none"}, good(), nil),
		"HS256":                   is.token(map[string]string{"alg": "HS256", "kid": "k1"}, good(), is.key),
		"another issuer":          is.token(rs, with(good(), "iss", "https://evil.example/"), is.key),
		"another client":          is.token(rs, with(good(), "aud", "photos"), is.key),
		"two audiences, no azp":   is.token(rs, with(good(), "aud", []string{"admin", "photos"}), is.key),
		"expired":                 is.token(rs, with(good(), "exp", now.Add(-2*time.Minute).Unix()), is.key),
		"issued in the future":    is.token(rs, with(good(), "iat", now.Add(10*time.Minute).Unix()), is.key),
		"another sign-in's nonce": is.token(rs, with(good(), "nonce", "n2"), is.key),
		"no nonce":                is.token(rs, with(good(), "nonce", ""), is.key),
		"no subject":              is.token(rs, with(good(), "sub", ""), is.key),
		"not a token":             "abc",
	} {
		if _, err := c.Verify(context.Background(), e, tok, "n1", now); !errors.Is(err, oidc.ErrRefused) {
			t.Errorf("%s: %v, want ErrRefused", name, err)
		}
	}
	if _, err := c.Verify(context.Background(), e, is.token(rs, good(), is.key), "", now); !errors.Is(err, oidc.ErrRefused) {
		t.Errorf("no nonce expected: %v", err)
	}
}

func with(m map[string]any, k string, v any) map[string]any { m[k] = v; return m }

// The state must be this sign-in's; the authorize URL asks for the code flow with PKCE (S256), and a fresh sign-in
// for step-up.
func TestASignInIsBoundToItsAttempt(t *testing.T) {
	testenv.Isolate(t)
	c := oidc.Client{Issuer: "https://auth.example.test/application/o/x/", ID: "admin", Redirect: "https://admin.example.test/auth/callback"}
	a := oidc.FreshAttempt(now)
	u := c.AuthorizeURL(oidc.Endpoints{Authorize: "https://auth.example.test/application/o/authorize/"}, a)
	sum := sha256.Sum256([]byte(a.Verifier))
	for _, want := range []string{"response_type=code", "code_challenge_method=S256", "code_challenge=" + base64.RawURLEncoding.EncodeToString(sum[:]),
		"max_age=30", "state=" + a.State, "nonce=" + a.Nonce} {
		if !strings.Contains(u, want) {
			t.Errorf("the authorize URL lacks %s: %s", want, u)
		}
	}
	if strings.Contains(u, a.Verifier) {
		t.Error("the verifier left in the URL")
	}
	if u := c.AuthorizeURL(oidc.Endpoints{Authorize: "https://auth.example.test/application/o/authorize/"}, oidc.NewAttempt()); strings.Contains(u, "max_age") {
		t.Errorf("an attempt that isn't fresh asks for max_age: %s", u)
	}
	if _, err := c.Finish(context.Background(), oidc.Endpoints{}, a, "another", "code", now); !errors.Is(err, oidc.ErrRefused) {
		t.Errorf("another state: %v", err)
	}
}

// A fresh sign-in is one the person made after the attempt began: an ID token from an older session at the issuer, or
// one that doesn't say when the person signed in, is refused (review of #17).
func TestAFreshSignInIsRefusedAnOlderSession(t *testing.T) {
	testenv.Isolate(t)
	is := newIssuer(t)
	c := oidc.Client{Issuer: "https://auth.example.test/application/o/homerslab-admin/", ID: "admin", Secret: "s",
		Redirect: "https://admin.example.test/auth/callback"}
	var claims map[string]any
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": is.token(map[string]string{"alg": "RS256", "kid": "k1"}, claims, is.key)})
	}))
	defer tokens.Close()
	e := oidc.Endpoints{Keys: is.srv.URL, Token: tokens.URL}
	for name, tc := range map[string]struct {
		auth  any
		fresh bool
		ok    bool
	}{
		"signed in just now":                   {now.Add(5 * time.Second).Unix(), true, true},
		"signed in an hour before the attempt": {now.Add(-time.Hour).Unix(), true, false},
		"signed in a minute before":            {now.Add(-time.Minute).Unix(), true, false},
		"signed in within max_age":             {now.Add(-20 * time.Second).Unix(), true, true},
		"no auth_time":                         {nil, true, false},
		"an older session, not asked fresh":    {now.Add(-time.Hour).Unix(), false, true},
	} {
		a := oidc.NewAttempt()
		if tc.fresh {
			a = oidc.FreshAttempt(now)
		}
		claims = map[string]any{"iss": c.Issuer, "aud": "admin", "sub": "s1", "nonce": a.Nonce, "exp": now.Add(5 * time.Minute).Unix(),
			"iat": now.Unix()}
		if tc.auth != nil {
			claims["auth_time"] = tc.auth
		}
		_, err := c.Finish(context.Background(), e, a, a.State, "code", now)
		if tc.ok != (err == nil) {
			t.Errorf("%s: %v", name, err)
		}
		if !tc.ok && !errors.Is(err, oidc.ErrRefused) {
			t.Errorf("%s: %v, want ErrRefused", name, err)
		}
	}
}

// Through reaches only the sign-in's own origin at its upstream, with that origin's Host and HTTPS said.
func TestThroughReachesOnlyTheSignInsOrigin(t *testing.T) {
	testenv.Isolate(t)
	var host, proto string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { host, proto = r.Host, r.Header.Get("X-Forwarded-Proto") }))
	defer up.Close()
	c := &http.Client{Transport: oidc.Through("https://auth.example.test:9443", up.URL)}
	if _, err := c.Get("https://auth.example.test:9443/x"); err != nil || host != "auth.example.test:9443" || proto != "https" {
		t.Errorf("the sign-in: %v, Host %q, proto %q", err, host, proto)
	}
	for _, u := range []string{"https://evil.example/x", "http://auth.example.test:9443/x", "https://auth.example.test/x"} {
		if _, err := c.Get(u); err == nil {
			t.Errorf("%s was reached", u)
		}
	}
}
