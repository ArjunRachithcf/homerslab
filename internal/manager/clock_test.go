package manager_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// Ages in the manager's process go by its monotonic clock: setting the wall clock back or forward (NTP stepping it, a
// board without a real-time clock, someone fixing it by hand) neither lengthens nor cuts a link or a session (#65).

func TestASessionEndsTwoHoursAfterTheClaimEvenWhenTheClockGoesBack(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	p.clock.Set(-24 * time.Hour) // the clock steps back a day
	for range 4 {
		p.clock.Add(29 * time.Minute)
		if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusOK {
			t.Fatalf("a visit %s after the claim: %d, want 200", p.clock.Elapsed(), r.Code)
		}
	}
	p.clock.Add(2*time.Hour - 4*29*time.Minute)
	if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("two hours after the claim (the clock went back a day): %d, want 401", r.Code)
	}
}

func TestASessionEndsAfter30MinutesIdleEvenWhenTheClockGoesBack(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	p.clock.Set(-24 * time.Hour)
	p.clock.Add(30 * time.Minute)
	if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("30 idle minutes (the clock went back a day): %d, want 401", r.Code)
	}
}

func TestALinkIsRefusedAfter30MinutesEvenWhenTheClockGoesBack(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	l := p.newLink()
	p.clock.Set(-24 * time.Hour)
	p.clock.Add(30 * time.Minute)
	if r := p.browser(onTheLAN).claim(l); r.Code != http.StatusForbidden || r.errorCode(t) != "link-refused" {
		t.Errorf("claiming a link 30 minutes after it was made (the clock went back a day): %d %s, want 403", r.Code, r.Body)
	}
}

// A Raspberry Pi makes its link before NTP sets its clock: the link and its session still last their time.
func TestALinkAndASessionLastTheirTimeWhenTheClockJumpsForward(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	l := p.newLink()
	p.clock.Set(3 * 365 * 24 * time.Hour) // NTP's first answer
	p.clock.Add(29 * time.Minute)
	b := p.browser(onTheLAN)
	if r := b.claim(l); r.Code != http.StatusOK {
		t.Fatalf("claiming a link 29 minutes after it was made (the clock jumped forward): %d %s, want 200", r.Code, r.Body)
	}
	p.clock.Set(3 * 365 * 24 * time.Hour)
	p.clock.Add(29 * time.Minute)
	if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusOK {
		t.Errorf("a visit 29 minutes after the claim (the clock jumped forward): %d, want 200", r.Code)
	}
}

// After a restart the process's monotonic clock is new, so a session carries on only when the wall clock says it is
// within its limits; an age below zero means the clock went back, and the session's age is unknown.
func TestASessionSurvivesARestartOnlyWithinItsLimits(t *testing.T) {
	testenv.Isolate(t)
	for _, c := range []struct {
		name string
		move func(*primary)
		code int
	}{
		{"ten minutes later", func(p *primary) { p.clock.Add(10 * time.Minute) }, http.StatusOK},
		{"a visit, then 25 minutes", func(p *primary) {
			p.clock.Add(20 * time.Minute)
			p.claimedBrowser.state() // what last_seen keeps on disk, at most a minute old
			p.clock.Add(25 * time.Minute)
		}, http.StatusOK},
		{"30 idle minutes", func(p *primary) { p.clock.Add(30 * time.Minute) }, http.StatusUnauthorized},
		{"two hours after the claim", func(p *primary) {
			for range 4 {
				p.clock.Add(29 * time.Minute)
				p.claimedBrowser.state()
			}
			p.clock.Add(4 * time.Minute)
		}, http.StatusUnauthorized},
		{"the clock went back a minute", func(p *primary) { p.clock.Set(-time.Minute) }, http.StatusUnauthorized},
		{"the clock went forward three hours", func(p *primary) { p.clock.Set(3 * time.Hour) }, http.StatusUnauthorized},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newPrimary(t)
			p.claimedBrowser = p.claimed()
			c.move(p)
			p.restart()
			if r := p.claimedBrowser.do("GET", "/api/v1/setup", nil); r.Code != c.code {
				t.Errorf("after the restart: %d %s, want %d", r.Code, r.Body, c.code)
			}
		})
	}
}

func TestALinkSurvivesARestartOnlyWithinItsLimits(t *testing.T) {
	testenv.Isolate(t)
	for _, c := range []struct {
		name string
		move func(*clock)
		code int
	}{
		{"ten minutes later", func(c *clock) { c.Add(10 * time.Minute) }, http.StatusOK},
		{"30 minutes later", func(c *clock) { c.Add(30 * time.Minute) }, http.StatusForbidden},
		{"the clock went back a minute", func(c *clock) { c.Set(-time.Minute) }, http.StatusForbidden},
		{"the clock went forward an hour", func(c *clock) { c.Set(time.Hour) }, http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newPrimary(t)
			l := p.newLink()
			c.move(p.clock)
			p.restart()
			if r := p.browser(onTheLAN).claim(l); r.Code != c.code {
				t.Errorf("claiming after the restart: %d %s, want %d", r.Code, r.Body, c.code)
			}
		})
	}
}

// Once a restarted manager has taken a session over, its own monotonic clock times it.
func TestASessionTakenOverAfterARestartIsTimedByTheNewProcess(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	p.clock.Add(10 * time.Minute)
	p.restart()
	b.state()
	p.clock.Set(-24 * time.Hour)
	for range 3 {
		p.clock.Add(29 * time.Minute)
		if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusOK {
			t.Fatalf("a visit after the restart and the clock going back: %d, want 200", r.Code)
		}
	}
	p.clock.Add(2*time.Hour - 10*time.Minute - 3*29*time.Minute)
	if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("two hours after the claim, across a restart and the clock going back: %d, want 401", r.Code)
	}
}

// A session taken over after a restart is still bound to the device that claimed it, and ended by a new link.
func TestASessionTakenOverAfterARestartKeepsItsBounds(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	p.restart()
	neighbour := &browser{p: p, from: "192.168.1.77", token: b.token}
	if r := neighbour.do("GET", "/api/v1/setup", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("the session's token from another device, after a restart: %d, want 401", r.Code)
	}
	b.state()

	p = newPrimary(t)
	b = p.claimed()
	p.newLink()
	p.restart()
	if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("a session a new link ended, after a restart: %d, want 401", r.Code)
	}
}

// A manager that took a session over keeps the database's last use current, for the next restart.
func TestASessionSurvivesTwoRestarts(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	p.clock.Add(20 * time.Minute)
	b.state()
	p.clock.Add(5 * time.Minute)
	p.restart()
	b.state()
	p.clock.Add(26 * time.Minute)
	p.restart()
	if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusOK {
		t.Errorf("26 minutes after the last visit, across two restarts: %d, want 200", r.Code)
	}
}
