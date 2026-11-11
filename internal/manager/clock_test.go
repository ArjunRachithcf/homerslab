package manager_test

import (
	"math/rand"
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

// A restart of the manager doesn't restart the machine's clock: the time since boot goes on, so a link's and a
// session's age are known across any number of restarts, whatever the wall clock does meanwhile.
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
			p.claimedBrowser.state() // what the database keeps of its last use, at most a minute old
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
		{"the clock went back a day", func(p *primary) { p.clock.Set(-24 * time.Hour) }, http.StatusOK},
		{"the clock went forward three hours", func(p *primary) { p.clock.Set(3 * time.Hour) }, http.StatusOK},
		{"the clock went back 20 minutes, after 20 idle", func(p *primary) {
			p.clock.Add(20 * time.Minute)
			p.clock.Set(-20 * time.Minute)
			p.clock.Add(10 * time.Minute)
		}, http.StatusUnauthorized},
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
		{"the clock went back a day", func(c *clock) { c.Set(-24 * time.Hour) }, http.StatusOK},
		{"the clock went forward an hour", func(c *clock) { c.Set(time.Hour) }, http.StatusOK},
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

// The independent review's probe: a link 25 minutes old, a restart, the clock stepped back 20 minutes, 20 minutes
// more. The link is 45 minutes old.
func TestALinkIsRefusedAfter30MinutesAcrossARestartAndTheClockGoingBack(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	l := p.newLink()
	p.clock.Add(25 * time.Minute)
	p.restart()
	p.clock.Set(-20 * time.Minute)
	p.clock.Add(20 * time.Minute)
	if r := p.browser(onTheLAN).claim(l); r.Code != http.StatusForbidden {
		t.Errorf("claiming a link 45 minutes after it was made, across a restart and the clock going back: %d %s, want 403", r.Code, r.Body)
	}
}

// The independent review's probe: a visit, 25 minutes, a restart and the clock stepped back 25 minutes, again and
// again. The session ends two hours after its claim.
func TestASessionEndsTwoHoursAfterTheClaimAcrossRestartsAndTheClockGoingBack(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	for since := time.Duration(0); since <= 3*time.Hour+36*time.Minute; since += 27 * time.Minute {
		r := b.do("GET", "/api/v1/setup", nil)
		if want := http.StatusOK; since >= 2*time.Hour {
			if r.Code != http.StatusUnauthorized {
				t.Fatalf("a visit %s after the claim: %d, want 401", since, r.Code)
			}
			return
		} else if r.Code != want {
			t.Fatalf("a visit %s after the claim: %d, want %d", since, r.Code, want)
		}
		p.clock.Add(2 * time.Minute)
		b.state()
		p.clock.Add(25 * time.Minute)
		p.restart()
		p.clock.Set(-25 * time.Minute)
	}
	t.Fatal("the loop ended before two hours")
}

// After the machine boots again the time since boot starts over, so a link's or a session's age is unknown: they have
// ended, and a new link continues setup where it stopped (#14 Q1).
func TestALinkAndASessionEndWhenTheMachineBootsAgain(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	p.reboot()
	if r := b.do("GET", "/api/v1/setup", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("a session after the machine booted again: %d, want 401", r.Code)
	}
	l := p.newLink()
	p.clock.Add(time.Minute)
	p.reboot()
	if r := p.browser(onTheLAN).claim(l); r.Code != http.StatusForbidden {
		t.Errorf("a link after the machine booted again: %d %s, want 403", r.Code, r.Body)
	}
	if s := p.claimed().state(); s.Current != "welcome" {
		t.Errorf("a new link after the boot: setup at %q, want where it stopped", s.Current)
	}
}

// Once a restarted manager has taken a session over, the time since boot times it, whatever the wall clock does.
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

// The second independent review's property test: 300 random walks of time going by, the wall clock stepped either
// way, restarts, reboots, visits, new links and claims, each checked against the documented bounds. A link can be
// claimed for 30 minutes; a session lasts 2 hours at most and 30 minutes idle (after a restart, up to SeenEvery
// sooner); another boot ends both.
func TestTheClockKeepsItsBoundsOnRandomWalks(t *testing.T) {
	testenv.Isolate(t)
	for seed := int64(0); seed < 300; seed++ {
		rng := rand.New(rand.NewSource(seed))
		p := newPrimary(t)
		type sess struct {
			b              *browser
			opened, last   time.Duration
			boot           int
			restartedSince bool
			live           bool
		}
		var s *sess
		var pending *link
		var linkMade time.Duration
		linkBoot := 0
		var steps []string
		for step := 0; step < 40; step++ {
			switch op := rng.Intn(10); op {
			case 0, 1, 2:
				d := time.Duration(rng.Int63n(int64(35 * time.Minute)))
				p.clock.Add(d)
				steps = append(steps, "add "+d.String())
			case 3:
				d := time.Duration(rng.Int63n(int64(72*time.Hour))) - 36*time.Hour
				p.clock.Set(d)
				steps = append(steps, "set "+d.String())
			case 4:
				p.restart()
				if s != nil {
					s.restartedSince = true
				}
				steps = append(steps, "restart")
			case 5:
				if rng.Intn(4) == 0 {
					p.reboot()
					steps = append(steps, "reboot")
				}
			case 6:
				l := p.newLink()
				pending, linkMade, linkBoot = &l, p.clock.Elapsed(), p.clock.boots
				if s != nil {
					s.live = false
				}
				steps = append(steps, "newlink")
			case 7:
				if pending == nil {
					continue
				}
				b := p.browser(onTheLAN)
				r := b.claim(*pending)
				age := p.clock.Elapsed() - linkMade
				want := linkBoot == p.clock.boots && age < 30*time.Minute
				got := r.Code == http.StatusOK
				steps = append(steps, "claim")
				if got != want {
					t.Fatalf("seed %d: claim at link age %s (same boot %v): %d, want ok=%v; steps %v", seed, age,
						linkBoot == p.clock.boots, r.Code, want, steps)
				}
				pending = nil
				if got {
					e := p.clock.Elapsed()
					s = &sess{b: b, opened: e, last: e, boot: p.clock.boots, live: true}
				}
			default:
				if s == nil {
					continue
				}
				e := p.clock.Elapsed()
				same := s.boot == p.clock.boots
				idle, open := e-s.last, e-s.opened
				must := s.live && same && open < 2*time.Hour && idle < 29*time.Minute
				mustNot := !s.live || !same || open >= 2*time.Hour || idle >= 30*time.Minute
				if !s.restartedSince {
					must = s.live && same && open < 2*time.Hour && idle < 30*time.Minute
				}
				r := s.b.do("GET", "/api/v1/setup", nil)
				got := r.Code == http.StatusOK
				steps = append(steps, "visit")
				if got && mustNot || !got && must {
					t.Fatalf("seed %d: visit open %s idle %s same boot %v live %v restarted %v: %d; steps %v", seed, open,
						idle, same, s.live, s.restartedSince, r.Code, steps)
				}
				if got {
					s.last = e
					s.restartedSince = false
				} else {
					s.live = false
				}
			}
		}
	}
}
