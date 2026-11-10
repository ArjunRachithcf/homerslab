package manager_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

func TestALinkIsForTheLANAndThisMachine(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	l := p.newLink()
	if !strings.HasPrefix(l.URL, "http://192.168.1.20:7020/setup#k=") {
		t.Errorf("url = %s, want the machine's LAN address and the setup listener's port", l.URL)
	}
	if !strings.HasPrefix(l.LocalURL, "http://127.0.0.1:7020/setup#k=") {
		t.Errorf("localUrl = %s, want this machine's own address", l.LocalURL)
	}
	if want := p.clock.Now().Add(30 * time.Minute); !l.ExpiresAt.Equal(want) {
		t.Errorf("expiresAt = %s, want %s", l.ExpiresAt, want)
	}
	if k := l.key(t); len(k) < 43 {
		t.Errorf("key %q is shorter than 256 bits", k)
	}
	if other := p.newLink(); other.key(t) == l.key(t) {
		t.Error("two links share a key")
	}
}

func TestALinkIsClaimedOnce(t *testing.T) {
	testenv.Isolate(t)
	for _, from := range []string{thisMachine, onTheLAN, lanV6, linkLocalV6, "::1"} {
		t.Run(from, func(t *testing.T) {
			p := newPrimary(t)
			l := p.newLink()
			first := p.browser(from)
			if r := first.claim(l); r.Code != http.StatusNoContent {
				t.Fatalf("first claim: %d %s", r.Code, r.Body)
			}
			if s := first.state(); s.Current != "welcome" {
				t.Errorf("after the claim, setup is at %q, want welcome", s.Current)
			}
			for _, again := range []*browser{p.browser(from), first} {
				if r := again.claim(l); r.Code != http.StatusForbidden || r.errorCode(t) != "link-refused" {
					t.Errorf("second claim: %d %s, want 403 link-refused", r.Code, r.Body)
				}
			}
		})
	}
}

func TestALinkIsRefusedToAnyoneElse(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	l := p.newLink()
	for _, from := range []string{theInternet, onTheMesh, aContainer, onAnother, "2001:db8::9"} {
		if r := p.browser(from).claim(l); r.Code != http.StatusForbidden || r.errorCode(t) != "not-from-lan" {
			t.Errorf("claim from %s: %d %s, want 403 not-from-lan", from, r.Code, r.Body)
		}
	}
	if r := p.browser(onTheLAN).claim(l); r.Code != http.StatusNoContent {
		t.Errorf("refusing strangers must not use the link up: the LAN's claim got %d %s", r.Code, r.Body)
	}
}

func TestALinkIsRefusedAfter30Minutes(t *testing.T) {
	testenv.Isolate(t)
	for _, c := range []struct {
		after time.Duration
		code  int
	}{{30*time.Minute - time.Second, http.StatusNoContent}, {30 * time.Minute, http.StatusForbidden}, {25 * time.Hour, http.StatusForbidden}} {
		p := newPrimary(t)
		l := p.newLink()
		p.clock.Add(c.after)
		if r := p.browser(onTheLAN).claim(l); r.Code != c.code {
			t.Errorf("claim %s after the link was made: %d %s, want %d", c.after, r.Code, r.Body, c.code)
		}
	}
}

func TestAWrongKeyIsRefused(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	p.newLink()
	for _, body := range []any{map[string]string{"key": "not-the-key"}, map[string]string{}, map[string]int{"key": 1}} {
		if r := p.browser(onTheLAN).do("POST", "/api/v1/setup/claim", body); r.Code != http.StatusForbidden || r.errorCode(t) != "link-refused" {
			t.Errorf("claim with %v: %d %s, want 403 link-refused", body, r.Code, r.Body)
		}
	}
}

func TestANewLinkEndsTheEarlierOnes(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	unclaimed := p.newLink()
	b := p.claimed()
	p.newLink()
	if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("the earlier link's browser: %d %s, want 401", r.Code, r.Body)
	}
	if r := p.browser(onTheLAN).claim(unclaimed); r.Code != http.StatusForbidden {
		t.Errorf("an earlier, unclaimed link: %d %s, want 403", r.Code, r.Body)
	}
}

func TestSetupNeedsAClaimedLink(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	p.newLink()
	stranger := p.browser(onTheLAN)
	stranger.cookies = []*http.Cookie{{Name: "homerslab_setup", Value: "guessed"}}
	for _, b := range []*browser{p.browser(onTheLAN), stranger} {
		for _, req := range [][2]string{{"GET", "/api/v1/setup"}, {"GET", "/api/v1/nodes/primary"}, {"PUT", "/api/v1/setup/steps/welcome"}} {
			var body any
			if req[0] == "PUT" {
				body = map[string]string{}
			}
			if r := b.do(req[0], req[1], body); r.Code != http.StatusUnauthorized || r.errorCode(t) != "no-session" {
				t.Errorf("%s %s without a claimed link: %d %s, want 401 no-session", req[0], req[1], r.Code, r.Body)
			}
		}
	}
}

func TestEverySetupRequestComesFromTheLAN(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	moved := &browser{p: p, from: theInternet, cookies: b.cookies}
	for _, path := range []string{"/api/v1/setup", "/api/v1/nodes/primary", "/setup", "/"} {
		if r := moved.do("GET", path, nil); r.Code != http.StatusForbidden {
			t.Errorf("GET %s from outside with a valid session: %d, want 403", path, r.Code)
		}
	}
}

func TestTheSessionCookieStaysInTheBrowser(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	if len(b.cookies) != 1 {
		t.Fatalf("cookies = %v", b.cookies)
	}
	c := b.cookies[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Errorf("cookie %+v: want HttpOnly, SameSite=Strict, Path=/", c)
	}
}

func TestASessionEndsAfter30MinutesIdle(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	for range 3 {
		p.clock.Add(29 * time.Minute)
		b.state() // each visit keeps it open
	}
	p.clock.Add(30 * time.Minute)
	if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("after 30 idle minutes: %d %s, want 401", r.Code, r.Body)
	}
}

func TestLinksAreMadeOnlyOnTheControlSocket(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	if r := b.do("POST", "/api/v1/setup/links", map[string]string{}); r.Code != http.StatusNotFound {
		t.Errorf("POST /api/v1/setup/links on the setup listener: %d %s, want 404", r.Code, r.Body)
	}
}

func TestStateChangesComeFromTheSetupPageItself(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	cross := func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }
	if r := b.do("PUT", "/api/v1/setup/steps/welcome", map[string]string{}, cross); r.Code != http.StatusForbidden {
		t.Errorf("a PUT from another origin: %d %s, want 403", r.Code, r.Body)
	}
	form := func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }
	if r := b.do("PUT", "/api/v1/setup/steps/welcome", map[string]string{}, form); r.Code != http.StatusUnsupportedMediaType {
		t.Errorf("a PUT that isn't JSON: %d %s, want 415", r.Code, r.Body)
	}
	same := func(r *http.Request) { r.Header.Set("Origin", "http://192.168.1.20:7020") }
	if r := b.do("PUT", "/api/v1/setup/steps/welcome", map[string]string{}, same); r.Code != http.StatusOK {
		t.Errorf("a PUT from the setup page: %d %s, want 200", r.Code, r.Body)
	}
}

func TestSetupResponsesCarryTheSecurityHeaders(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	r := p.claimed().do("GET", "/api/v1/setup", nil)
	for h, want := range map[string]string{
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"X-Frame-Options":        "DENY",
	} {
		if got := r.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	if csp := r.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("Content-Security-Policy = %q", csp)
	}
}
