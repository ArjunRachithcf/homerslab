package manager_test

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// No API on the LAN listener takes a secret (#64): every one of them refuses a request that carries one, before
// anything else is checked (its session, its step, its order), and says which command gives it instead. The fields a
// secret would ride in, by the secret.
var secretFields = map[string][]string{
	"mesh": {"token", "meshToken", "netbirdApiToken", "setupKey", "apiKey", "PAT"},
	"dns":  {"dnsToken", "secret", "tsigSecret", "apiToken"},
	"mail": {"password", "smtpPassword", "mailPassword", "passwd", "credential"},
}

func TestNoAPIOnTheLANListenerTakesASecret(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	stranger := p.browser(onTheLAN) // no session: the secret is refused all the same
	anyOf := "sudo homerslab setup secret mesh|dns|mail"
	for secret, fields := range secretFields {
		for _, field := range fields {
			for _, c := range []struct {
				name, method, path string
				who                *browser
				body               any
				want               string
			}{
				// Each step, with the secret it would need, or none: its own command, or the list.
				{"the network step", "PUT", "/api/v1/setup/steps/network", b, map[string]any{"mesh": "netbird", field: "x"}, "sudo homerslab setup secret mesh"},
				{"the domain step", "PUT", "/api/v1/setup/steps/domain", b, map[string]any{"domain": "example.test", field: "x"}, "sudo homerslab setup secret dns"},
				{"the mail step", "PUT", "/api/v1/setup/steps/mail", b, map[string]any{field: "x"}, "sudo homerslab setup secret mail"},
				{"a step that is done", "PUT", "/api/v1/setup/steps/welcome", b, map[string]any{field: "x"}, anyOf},
				{"a step that isn't one", "PUT", "/api/v1/setup/steps/wifi", b, map[string]any{field: "x"}, anyOf},
				{"a step, nested", "PUT", "/api/v1/setup/steps/you", b, map[string]any{"name": "Inês", "more": []any{map[string]any{field: "x"}}}, anyOf},
				{"a step, without a session", "PUT", "/api/v1/setup/steps/network", stranger, map[string]any{field: "x"}, "sudo homerslab setup secret mesh"},
				{"the claim", "POST", "/api/v1/setup/claim", stranger, map[string]any{"key": "k", "nonce": newNonce(), field: "x"}, anyOf},
				{"the claim, nested", "POST", "/api/v1/setup/claim", stranger, map[string]any{"key": map[string]any{field: "x"}}, anyOf},
				{"a read, in its query", "GET", "/api/v1/setup?" + field + "=x", b, nil, anyOf},
				{"a read of the machine, in its query", "GET", "/api/v1/nodes/primary?" + field + "=x", b, nil, anyOf},
				// The control socket's own address for it, asked over the LAN: the command, whatever the field.
				{"the control socket's address for the secret", "PUT", "/api/v1/setup/secrets/" + secret, b, map[string]any{"value": "x"}, "sudo homerslab setup secret " + secret},
				{"a form's post", "POST", "/api/v1/setup/claim", stranger, nil, anyOf},
			} {
				r := c.who.do(c.method, c.path, c.body, func(r *http.Request) {
					r.Header.Set("If-Match", `"0"`)
					if c.body == nil && c.method == "POST" {
						form := url.Values{field: {"x"}}.Encode()
						r.Body, r.ContentLength = io.NopCloser(strings.NewReader(form)), int64(len(form))
						r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					}
				})
				var e struct{ Error, Command string }
				r.json(t, &e)
				if r.Code != http.StatusBadRequest || e.Error != "secret-over-http" || e.Command != c.want {
					t.Errorf("%s with %q (%s): %d %s, want 400 secret-over-http and %q", c.name, field, secret, r.Code, r.Body, c.want)
				}
			}
		}
	}
	if s := b.state(); s.Current != "welcome" {
		t.Errorf("a refused secret moved setup on: at %q", s.Current)
	}
}

// What a step's answer may carry stays possible: the names of non-secret things, which only look like a secret's.
func TestTheLANListenerTakesAnswersThatOnlyLookLikeSecrets(t *testing.T) {
	testenv.Isolate(t)
	_, b, _, _ := atDomain(t) // its claim's "key" is the link's, not a secret
	// The domain step's "keyName" names a TSIG key: it is not one.
	r := b.answer("domain", map[string]any{"domain": "example.test", "provider": "rfc2136", "server": "192.0.2.53", "keyName": "homerslab"})
	if r.Code != http.StatusOK || stepState(b.state(), "domain") != "waiting" {
		t.Errorf("a TSIG key's name: %d %s, want the step waiting for the key itself", r.Code, r.Body)
	}
}
