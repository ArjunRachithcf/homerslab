package manager_test

import (
	"net/http"
	"net/netip"
	"strings"
	"sync"
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
	for _, from := range []string{thisMachine, onTheLAN, lanV6, linkLocalV6 + "%enp5s0", linkLocalV6 + "%wlp4s0", "::1"} {
		t.Run(from, func(t *testing.T) {
			p := newPrimary(t)
			l := p.newLink()
			first := p.browser(from)
			if r := first.claim(l); r.Code != http.StatusOK {
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
	for _, from := range []string{theInternet, onTheMesh, aContainer, onAnother, "2001:db8::9",
		linkLocalV6 + "%docker0", linkLocalV6 + "%virbr0", linkLocalV6 + "%podman0", linkLocalV6} {
		if r := p.browser(from).claim(l); r.Code != http.StatusForbidden || r.errorCode(t) != "not-from-lan" {
			t.Errorf("claim from %s: %d %s, want 403 not-from-lan", from, r.Code, r.Body)
		}
	}
	if r := p.browser(onTheLAN).claim(l); r.Code != http.StatusOK {
		t.Errorf("refusing strangers must not use the link up: the LAN's claim got %d %s", r.Code, r.Body)
	}
}

func TestALinkIsRefusedAfter30Minutes(t *testing.T) {
	testenv.Isolate(t)
	for _, c := range []struct {
		after time.Duration
		code  int
	}{{30*time.Minute - time.Second, http.StatusOK}, {30 * time.Minute, http.StatusForbidden}, {25 * time.Hour, http.StatusForbidden}} {
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
	stranger.token = "guessed"
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
	moved := &browser{p: p, from: theInternet, token: b.token}
	for _, path := range []string{"/api/v1/setup", "/api/v1/nodes/primary", "/setup", "/"} {
		if r := moved.do("GET", path, nil); r.Code != http.StatusForbidden {
			t.Errorf("GET %s from outside with a valid session: %d, want 403", path, r.Code)
		}
	}
}

// The session is no cookie: cookies ignore ports, so every other service on this machine's addresses would get it.
// The page keeps the token in its tab and sends it as a bearer token.
func TestTheSessionIsATokenNotACookie(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed() // the helper fails the test on any Set-Cookie
	if len(b.token) < 43 {
		t.Fatalf("token %q", b.token)
	}
	asCookie := p.browser(onTheLAN)
	cookie := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "homerslab_setup", Value: b.token}) }
	if r := asCookie.do("GET", "/api/v1/setup", nil, cookie); r.Code != http.StatusUnauthorized {
		t.Errorf("the token as a cookie: %d, want 401", r.Code)
	}
	for _, h := range []string{"Bearer", "Basic " + b.token, "bearer" + b.token} {
		if r := p.browser(onTheLAN).do("GET", "/api/v1/setup", nil, func(r *http.Request) { r.Header.Set("Authorization", h) }); r.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: %d, want 401", h, r.Code)
		}
	}
	if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusOK {
		t.Errorf("with the bearer token: %d %s", r.Code, r.Body)
	}
}

func TestALinkIsClaimedOnceWhenManyTryAtOnce(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	for round := range 20 {
		key := p.newLink().key(t)
		var wg sync.WaitGroup
		codes := make([]int, 16)
		for i := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i] = serve(p.m.Setup(), newRequest("POST", "/api/v1/setup/claim", map[string]string{"key": key}, onTheLAN)).Code
			}()
		}
		wg.Wait()
		won := 0
		for _, c := range codes {
			switch c {
			case http.StatusOK:
				won++
			case http.StatusForbidden:
			default:
				t.Errorf("round %d: a claim answered %d", round, c)
			}
		}
		if won != 1 {
			t.Fatalf("round %d: %d claims won, want exactly one (%v)", round, won, codes)
		}
	}
}

// Link-local addresses are the same on every interface; the zone says which one. Only the LAN's interfaces count,
// and a session stays on the interface it was claimed from.
func TestLinkLocalCountsOnlyOnTheLANsInterfaces(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.browser(linkLocalV6 + "%enp5s0")
	if r := b.claim(p.newLink()); r.Code != http.StatusOK {
		t.Fatalf("claim over enp5s0: %d %s", r.Code, r.Body)
	}
	other := &browser{p: p, from: linkLocalV6 + "%wlp4s0", token: b.token}
	if r := other.do("GET", "/api/v1/setup", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("the same address on another interface: %d, want 401", r.Code)
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

func TestASessionStaysWithTheDeviceThatClaimedIt(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	neighbour := &browser{p: p, from: "192.168.1.77", token: b.token} // the same LAN, someone else's device
	if r := neighbour.do("GET", "/api/v1/setup", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("the session's cookie from another device on the LAN: %d %s, want 401", r.Code, r.Body)
	}
	if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusOK {
		t.Errorf("the device that claimed it: %d %s", r.Code, r.Body)
	}
}

func TestASessionEndsTwoHoursAfterTheClaimEvenWhenUsed(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	for range 4 {
		p.clock.Add(29 * time.Minute)
		if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusOK {
			t.Fatalf("a visit at %s: %d", p.clock.Now(), r.Code)
		}
	}
	p.clock.Add(2*time.Hour - 4*29*time.Minute - time.Second) // 2h - 1s after the claim
	if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusOK {
		t.Errorf("a second before two hours: %d, want 200", r.Code)
	}
	p.clock.Add(time.Second)
	if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("two hours after the claim: %d %s, want 401", r.Code, r.Body)
	}
}

// DNS rebinding: a page on another site whose name now points here gets nothing. Only the link's own names answer.
func TestOnlyTheLinksOwnHostsAreAnswered(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	host := func(h string) func(*http.Request) { return func(r *http.Request) { r.Host = h } }
	for _, h := range []string{"192.168.1.20:7020", "127.0.0.1:7020", "[::1]:7020", "localhost:7020", "study-pc:7020", "[fd00:1::20]:7020"} {
		if r := b.do("GET", "/api/v1/setup", nil, host(h)); r.Code != http.StatusOK {
			t.Errorf("Host %s: %d %s, want 200", h, r.Code, r.Body)
		}
	}
	for _, h := range []string{"evil.example:7020", "evil.example", "192.168.1.20", "192.168.1.20:8080", "192.168.1.21:7020", ""} {
		for _, req := range [][2]string{{"GET", "/api/v1/setup"}, {"GET", "/setup"}, {"POST", "/api/v1/setup/claim"}} {
			var body any
			if req[0] == "POST" {
				body = map[string]string{"key": "x"}
			}
			if r := b.do(req[0], req[1], body, host(h)); r.Code != http.StatusMisdirectedRequest || r.errorCode(t) != "wrong-host" {
				t.Errorf("%s %s with Host %q: %d %s, want 421 wrong-host", req[0], req[1], h, r.Code, r.Body)
			}
		}
	}
	rebound := func(r *http.Request) {
		r.Host = "evil.example:7020"
		r.Header.Set("Origin", "http://evil.example:7020")
	}
	if r := b.do("PUT", "/api/v1/setup/steps/welcome", map[string]string{}, rebound); r.Code != http.StatusMisdirectedRequest {
		t.Errorf("a rebound PUT: %d %s, want 421", r.Code, r.Body)
	}
}

func TestALinkHasNoLocalURLWhenTheListenerIsNotOnLoopback(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t, func(p *primary) { p.local = netip.Addr{} })
	if l := p.newLink(); l.LocalURL != "" {
		t.Errorf("localUrl = %q, want none", l.LocalURL)
	}
	p = newPrimary(t, func(p *primary) { p.local = netip.IPv6Loopback() })
	if l := p.newLink(); !strings.HasPrefix(l.LocalURL, "http://[::1]:7020/setup#k=") {
		t.Errorf("localUrl = %q, want [::1]", l.LocalURL)
	}
}

func TestTheBuiltPagesAreNotListed(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	if r := p.browser(onTheLAN).do("GET", "/assets/", nil); r.Code != http.StatusNotFound {
		t.Errorf("GET /assets/: %d, want 404", r.Code)
	}
}

// The link's own addresses always answer, even where the list of the machine's names misses them (a listener bound
// to one address); and a browser leaves out port 80.
func TestTheLinksOwnAddressesAlwaysAnswer(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t, func(p *primary) { p.hosts = []string{"study-pc"} })
	b := p.claimed()
	for _, h := range []string{"192.168.1.20:7020", "127.0.0.1:7020"} {
		if r := b.do("GET", "/api/v1/setup", nil, func(r *http.Request) { r.Host = h }); r.Code != http.StatusOK {
			t.Errorf("Host %s, the link's own: %d %s", h, r.Code, r.Body)
		}
	}
	p = newPrimary(t, func(p *primary) { p.port = 80 })
	b = p.claimed()
	if r := b.do("GET", "/api/v1/setup", nil, func(r *http.Request) { r.Host = "192.168.1.20" }); r.Code != http.StatusOK {
		t.Errorf("port 80 without a port in Host: %d %s", r.Code, r.Body)
	}
	if r := b.do("GET", "/api/v1/setup", nil, func(r *http.Request) { r.Host = "192.168.1.20:7020" }); r.Code != http.StatusMisdirectedRequest {
		t.Errorf("another port: %d, want 421", r.Code)
	}
}
