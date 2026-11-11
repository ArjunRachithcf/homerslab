package manager_test

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// A claim whose answer the network lost (#101): the tab sends it again, with the same key and its own nonce, from the
// same address, and gets the same session back for 60 seconds. Anyone else is still refused.

// lostAnswer claims l from b and drops the manager's answer: the claim reached the manager, its token never reached
// the tab.
func lostAnswer(t *testing.T, b *browser, l link) string {
	t.Helper()
	if r := b.claim(l); r.Code != http.StatusOK {
		t.Fatalf("the first claim: %d %s", r.Code, r.Body)
	}
	lost := b.token
	b.token = ""
	return lost
}

func TestAClaimWhoseAnswerWasLostGetsTheSameSessionAgain(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	l := p.newLink()
	b := p.browser(onTheLAN)
	lost := lostAnswer(t, b, l)

	p.clock.Add(59 * time.Second)
	if r := b.claim(l); r.Code != http.StatusOK {
		t.Fatalf("the same claim again, 59 s later: %d %s, want 200", r.Code, r.Body)
	}
	if s := b.state(); s.Current != "welcome" {
		t.Errorf("after the claim was sent again, setup is at %q, want welcome", s.Current)
	}
	// One session: the token whose answer was lost no longer opens it.
	stale := &browser{p: p, from: onTheLAN, token: lost}
	if r := stale.do("GET", "/api/v1/setup", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("the lost answer's token after the claim was sent again: %d, want 401", r.Code)
	}
}

// The session a resend gets is the one the claim opened: it ends two hours after the claim, not after the resend.
func TestAClaimSentAgainKeepsTheSessionsOwnTimes(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	l := p.newLink()
	b := p.browser(onTheLAN)
	lostAnswer(t, b, l)
	p.clock.Add(50 * time.Second)
	if r := b.claim(l); r.Code != http.StatusOK {
		t.Fatalf("sent again: %d %s", r.Code, r.Body)
	}
	for range 4 {
		p.clock.Add(29 * time.Minute)
		b.state()
	}
	p.clock.Add(2*time.Hour - 50*time.Second - 4*29*time.Minute) // two hours after the claim, 50 s short of the resend's
	if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("two hours after the claim: %d %s, want 401", r.Code, r.Body)
	}
}

func TestAClaimSentAgainIsRefusedAfter60Seconds(t *testing.T) {
	testenv.Isolate(t)
	for _, c := range []struct {
		after time.Duration
		code  int
	}{{59*time.Second + 999*time.Millisecond, http.StatusOK}, {60 * time.Second, http.StatusForbidden}, {10 * time.Minute, http.StatusForbidden}} {
		p := newPrimary(t)
		l := p.newLink()
		b := p.browser(onTheLAN)
		lostAnswer(t, b, l)
		p.clock.Add(c.after)
		if r := b.claim(l); r.Code != c.code {
			t.Errorf("sent again %s after the claim: %d %s, want %d", c.after, r.Code, r.Body, c.code)
		}
	}
}

// The window runs from the claim: sending it again doesn't make it longer.
func TestTheWindowRunsFromTheClaim(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	l := p.newLink()
	b := p.browser(onTheLAN)
	lostAnswer(t, b, l)
	p.clock.Add(40 * time.Second)
	if r := b.claim(l); r.Code != http.StatusOK {
		t.Fatalf("40 s after the claim: %d %s", r.Code, r.Body)
	}
	b.token = ""
	p.clock.Add(30 * time.Second)
	if r := b.claim(l); r.Code != http.StatusForbidden || r.errorCode(t) != "link-refused" {
		t.Errorf("70 s after the claim, 30 s after it was sent again: %d %s, want 403 link-refused", r.Code, r.Body)
	}
}

// Setting the wall clock neither lengthens the window nor cuts it: it goes by the time since boot.
func TestTheWindowIgnoresTheWallClock(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	l := p.newLink()
	b := p.browser(onTheLAN)
	lostAnswer(t, b, l)
	p.clock.Add(61 * time.Second)
	p.clock.Set(-30 * time.Second)
	if r := b.claim(l); r.Code != http.StatusForbidden {
		t.Errorf("61 s after the claim, the wall clock set back 30 s: %d %s, want 403", r.Code, r.Body)
	}

	l = p.newLink()
	lostAnswer(t, b, l)
	p.clock.Add(10 * time.Second)
	p.clock.Set(time.Hour)
	if r := b.claim(l); r.Code != http.StatusOK {
		t.Errorf("10 s after the claim, the wall clock set forward an hour: %d %s, want 200", r.Code, r.Body)
	}
}

// Every way a resend differs from the claim is refused, and the refusal leaves the claim's session alone.
func TestAClaimSentAgainIsRefusedToAnyoneElse(t *testing.T) {
	testenv.Isolate(t)
	for _, c := range []struct {
		name  string
		other func(b *browser) *browser
	}{
		{"another tab: another nonce, the same address", func(b *browser) *browser { return b.p.browser(b.from) }},
		{"another device: the same nonce, another address", func(b *browser) *browser {
			o := b.p.browser("192.168.1.43")
			o.nonce = b.nonce
			return o
		}},
		{"the same address on another interface", func(b *browser) *browser {
			o := b.p.browser(linkLocalV6 + "%wlp4s0")
			o.nonce = b.nonce
			return o
		}},
		{"this machine, the same nonce", func(b *browser) *browser {
			o := b.p.browser(thisMachine)
			o.nonce = b.nonce
			return o
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newPrimary(t)
			l := p.newLink()
			b := p.browser(linkLocalV6 + "%enp5s0")
			if r := b.claim(l); r.Code != http.StatusOK {
				t.Fatalf("the claim: %d %s", r.Code, r.Body)
			}
			p.clock.Add(time.Second)
			if r := c.other(b).claim(l); r.Code != http.StatusForbidden || r.errorCode(t) != "link-refused" {
				t.Errorf("%d %s, want 403 link-refused", r.Code, r.Body)
			}
			if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusOK {
				t.Errorf("the claim's own session after the refusal: %d %s, want 200", r.Code, r.Body)
			}
		})
	}
}

// The nonce goes with the key it was claimed with: the same tab's nonce with another link's key is refused.
func TestANonceGoesWithItsLink(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.browser(onTheLAN)
	first := p.newLink()
	lostAnswer(t, b, first)
	if r := b.claim(p.newLink()); r.Code != http.StatusOK {
		t.Fatalf("a new link, the same tab: %d %s", r.Code, r.Body)
	}
	if r := b.claim(first); r.Code != http.StatusForbidden {
		t.Errorf("the first link again, after a newer one ended it: %d %s, want 403", r.Code, r.Body)
	}
}

func TestAClaimSentAgainIsRefusedAfterANewLink(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	l := p.newLink()
	b := p.browser(onTheLAN)
	lostAnswer(t, b, l)
	p.newLink()
	if r := b.claim(l); r.Code != http.StatusForbidden || r.errorCode(t) != "link-refused" {
		t.Errorf("sent again after a new link: %d %s, want 403 link-refused", r.Code, r.Body)
	}
}

// The claim is kept in the database: a manager that restarted answers it again; a machine that rebooted doesn't.
func TestAClaimSentAgainSurvivesARestartButNotAReboot(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	l := p.newLink()
	b := p.browser(onTheLAN)
	lostAnswer(t, b, l)
	p.clock.Add(10 * time.Second)
	p.restart()
	if r := b.claim(l); r.Code != http.StatusOK {
		t.Fatalf("sent again after the manager restarted: %d %s, want 200", r.Code, r.Body)
	}
	b.state()

	q := newPrimary(t)
	l = q.newLink()
	b = q.browser(onTheLAN)
	lostAnswer(t, b, l)
	q.reboot()
	if r := b.claim(l); r.Code != http.StatusForbidden {
		t.Errorf("sent again after the machine rebooted: %d %s, want 403", r.Code, r.Body)
	}
}

// A claim must carry a nonce of at least 128 random bits, URL-safe: one without, or with a weak one, is refused as a
// bad request, not as a used-up link (the page would let go of a key that still works), and doesn't use the link up.
func TestAClaimWithoutAGoodNonceIsRefused(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	l := p.newLink()
	key := l.key(t)
	for _, body := range []any{
		map[string]string{"key": key},
		map[string]string{"key": key, "nonce": ""},
		map[string]string{"key": key, "nonce": strings.Repeat("a", 21)},        // 126 bits
		map[string]string{"key": key, "nonce": strings.Repeat("a", 129)},       // longer than any tab's
		map[string]string{"key": key, "nonce": strings.Repeat("a", 30) + "+/"}, // not URL-safe
		map[string]string{"key": key, "nonce": strings.Repeat("a", 30) + "=="},
	} {
		if r := p.browser(onTheLAN).do("POST", "/api/v1/setup/claim", body); r.Code != http.StatusBadRequest || r.errorCode(t) != "bad-nonce" {
			t.Errorf("claim with %v: %d %s, want 400 bad-nonce", body, r.Code, r.Body)
		}
	}
	// A body that isn't a claim at all is refused as before.
	if r := p.browser(onTheLAN).do("POST", "/api/v1/setup/claim", map[string]any{"key": key, "nonce": 1234567890123456789}); r.Code != http.StatusForbidden {
		t.Errorf("a nonce that isn't a string: %d %s, want 403", r.Code, r.Body)
	}
	if r := p.browser(onTheLAN).claim(l); r.Code != http.StatusOK {
		t.Errorf("the link after the refused claims: %d %s, want 200", r.Code, r.Body)
	}
}

// A session that was used got its answer: a resend can't take it over, from the same tab or anything that copied its
// address (key and nonce), even within the minute; the session carries on.
func TestAClaimIsNotSentAgainOnceItsSessionWasUsed(t *testing.T) {
	testenv.Isolate(t)
	for _, restart := range []bool{false, true} {
		p := newPrimary(t)
		l := p.newLink()
		b := p.browser(onTheLAN)
		if r := b.claim(l); r.Code != http.StatusOK {
			t.Fatalf("the claim: %d %s", r.Code, r.Body)
		}
		p.clock.Add(time.Second)
		b.state()
		if restart {
			p.restart()
		}
		copied := p.browser(onTheLAN)
		copied.nonce = b.nonce
		p.clock.Add(time.Second)
		if r := copied.claim(l); r.Code != http.StatusForbidden || r.errorCode(t) != "link-refused" {
			t.Errorf("restart %v: the claim sent again after its session was used: %d %s, want 403 link-refused", restart, r.Code, r.Body)
		}
		if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusOK {
			t.Errorf("restart %v: the session after the refused resend: %d %s, want 200", restart, r.Code, r.Body)
		}
	}
}

// The nonce is kept only as its hash.
func TestTheNonceIsKeptOnlyAsItsHash(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	assertNoSecretAt(t, p, b.nonce, b.token)
}

// The same tab sending its claim many times at once (a reload while it was out): each gets an answer, and one session
// is left, which the last answer opens.
func TestAClaimSentManyTimesAtOnceLeavesOneSession(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	l := p.newLink()
	tabs := make([]*browser, 8)
	nonce := newNonce()
	for i := range tabs {
		tabs[i] = p.browser(onTheLAN)
		tabs[i].nonce = nonce
	}
	var wg sync.WaitGroup
	codes := make([]int, len(tabs))
	for i, b := range tabs {
		wg.Go(func() { codes[i] = b.claim(l).Code })
	}
	wg.Wait()
	open := 0
	for i, b := range tabs {
		if codes[i] != http.StatusOK {
			t.Errorf("claim %d: %d, want 200", i, codes[i])
		}
		if b.do("GET", "/api/v1/setup", nil).Code == http.StatusOK {
			open++
		}
	}
	if open != 1 {
		t.Errorf("%d tokens open setup, want exactly one", open)
	}
}
