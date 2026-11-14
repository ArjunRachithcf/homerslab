package home_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	homeOrigin   = "https://home.example.test:9443"
	signInOrigin = "https://auth.example.test:9443"
	issuerURL    = signInOrigin + "/application/o/homerslab-home/"
	clientID     = "home-client"
	clientSecret = "HOME-CLIENT-SECRET-PLACEHOLDER-000000000000"
)

// issuer stands in for the household account's sign-in, as Home's client sees it: an OIDC issuer at the sign-in's
// upstream, signing ID tokens with a key of its own.
type issuer struct {
	mu    sync.Mutex
	srv   *httptest.Server
	key   *rsa.PrivateKey
	codes map[string]map[string]any
	now   func() time.Time
	// reauth: the person signs in again when an authorize URL asks for a recent sign-in.
	reauth bool
	// discovered counts the reads of its discovery document.
	discovered int
}

func newIssuer(t *testing.T, now func() time.Time) *issuer {
	t.Helper()
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	is := &issuer{key: k, codes: map[string]map[string]any{}, now: now, reauth: true}
	is.srv = httptest.NewServer(http.HandlerFunc(is.serve))
	t.Cleanup(is.srv.Close)
	return is
}

func (is *issuer) serve(w http.ResponseWriter, r *http.Request) {
	if r.Host != "auth.example.test:9443" || r.Header.Get("X-Forwarded-Proto") != "https" {
		http.Error(w, "not the sign-in's origin", http.StatusMisdirectedRequest)
		return
	}
	switch r.URL.Path {
	case "/application/o/homerslab-home/.well-known/openid-configuration":
		is.mu.Lock()
		is.discovered++
		is.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuerURL, "authorization_endpoint": signInOrigin + "/application/o/authorize/",
			"token_endpoint": signInOrigin + "/application/o/token/", "jwks_uri": signInOrigin + "/application/o/homerslab-home/jwks/"})
	case "/application/o/homerslab-home/jwks/":
		e := big.NewInt(int64(is.key.E)).Bytes()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(is.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(e)}}})
	case "/application/o/token/":
		id, secret, _ := r.BasicAuth()
		_ = r.ParseForm()
		is.mu.Lock()
		claims, ok := is.codes[r.Form.Get("code")]
		delete(is.codes, r.Form.Get("code"))
		is.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if id != clientID || secret != clientSecret || !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != claims["_challenge"] ||
			r.Form.Get("redirect_uri") != homeOrigin+"/auth/callback" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		delete(claims, "_challenge")
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": is.sign(claims)})
	default:
		http.NotFound(w, r)
	}
}

func (is *issuer) sign(claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	c, _ := json.Marshal(claims)
	in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	sum := sha256.Sum256([]byte(in))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, is.key, crypto.SHA256, sum[:])
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// code is what the sign-in hands the browser once a person signed in, for Home's authorize URL.
func (is *issuer) code(t *testing.T, authorize string, p person) (state, code string) {
	t.Helper()
	u, err := url.Parse(authorize)
	if err != nil || !strings.HasPrefix(authorize, signInOrigin+"/application/o/authorize/") {
		t.Fatalf("not the sign-in's authorize URL: %s", authorize)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != clientID || q.Get("redirect_uri") != homeOrigin+"/auth/callback" {
		t.Fatalf("authorize URL %s", authorize)
	}
	// On the session they signed in to an hour before, unless the URL asks for a recent sign-in (max_age), as Authentik
	// does: then just now.
	at := is.now().Add(-time.Hour)
	if q.Get("max_age") != "" && is.reauth {
		at = is.now()
	}
	code = fmt.Sprintf("code-%d", time.Now().UnixNano())
	is.mu.Lock()
	is.codes[code] = map[string]any{"iss": issuerURL, "aud": clientID, "sub": "hashed-" + p.username, "preferred_username": p.username,
		"name": p.name, "groups": p.groups, "nonce": q.Get("nonce"), "exp": is.now().Add(5 * time.Minute).Unix(), "iat": is.now().Unix(),
		"auth_time": at.Unix(), "_challenge": q.Get("code_challenge")}
	is.mu.Unlock()
	return q.Get("state"), code
}

// person is someone in the household account, as the sign-in names them.
type person struct {
	username, name string
	groups         []string
}

var (
	ines   = person{"ines", "Inês Ferreira", []string{"homerslab-admins"}}
	rui    = person{"rui", "Rui Ferreira", []string{"homerslab-adults", "book-club"}}
	tomas  = person{"tomas", "Tomás Ferreira", []string{"homerslab-kids"}}
	guest  = person{"guest", "A guest", []string{"book-club"}}
	nobody = person{}
)
