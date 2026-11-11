package manager_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/lan"
	"github.com/ArjunRachithcf/homerslab/internal/manager"
	"github.com/ArjunRachithcf/homerslab/internal/manager/setup"
)

// The addresses a request can come from.
const (
	thisMachine = "127.0.0.1"
	onTheLAN    = "192.168.1.42"
	onAnother   = "10.9.0.5"     // a private network this machine isn't on
	onTheMesh   = "100.64.0.7"   // a mesh peer: the second half's place, not the link's
	aContainer  = "172.17.0.2"   // a container on this machine's own bridge
	theInternet = "203.0.113.9"  // anyone else
	lanV6       = "fd00:1::42"   // the LAN's IPv6 prefix
	linkLocalV6 = "fe80::1234:1" // link-local: only with the zone of an interface that carries the default route
)

// clock is the test's time, moved by hand: the wall clock, which can be set, and the time since the machine booted
// (CLOCK_BOOTTIME), which only goes on, across restarts of the manager, until the machine boots again.
type clock struct {
	mu      sync.Mutex
	now     time.Time
	elapsed time.Duration
	boots   int
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *clock) Elapsed() time.Duration { c.mu.Lock(); defer c.mu.Unlock(); return c.elapsed }

// Boot is the machine's boot ID.
func (c *clock) Boot() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fmt.Sprintf("boot-%d", c.boots)
}

// Add lets d go by.
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now, c.elapsed = c.now.Add(d), c.elapsed+d
}

// Set moves the wall clock by d, as NTP stepping it or someone setting it does; no time goes by.
func (c *clock) Set(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// reboot boots the machine again: a new boot ID, and the time since boot starts over.
func (c *clock) reboot() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.elapsed, c.boots = 7*time.Second, c.boots+1
}

// primary is a manager with its own database and a real agent on the invented primary.
type primary struct {
	t     *testing.T
	m     *manager.Manager
	clock *clock
	db    string
	agent string
	local netip.Addr
	hosts []string
	port  int
	web   fs.FS
	// catalog is the Store's catalog; the official one when nil.
	catalog fs.FS
	// lanReads counts how often the manager asks for the machine's LAN address.
	lanReads atomic.Int32
	steps    map[string]setup.Handler
	// lanAddress is this machine's address on the LAN, for the links.
	lanAddress netip.Addr
	// claimedBrowser is a browser a test keeps across a restart.
	claimedBrowser *browser
	host           *agenttest.Host // the primary's machine, as its agent sees it
	netbird        manager.NetBird
	domains        manager.Domains
	// signIn stands in for the household account's provider, at signInUpstream.
	signIn         manager.SignIn
	signInUpstream string
	// agentConfig is the agent's, filled in by agenttest.
	agentConfig agent.Config
	log         *slog.Logger // the manager's and the agent's, when a test reads them
}

// withLogs gives the manager and its agent one logger, at debug level, writing to w.
func withLogs(w io.Writer) option {
	return func(p *primary) {
		p.log = slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
}

type option func(*primary)

// withBuiltSteps stands in for the steps later tickets build, so a test can walk the whole first half.
func withBuiltSteps(ids ...string) option {
	return func(p *primary) {
		for _, id := range ids {
			p.steps[id] = func(context.Context, json.RawMessage, map[string]string) (map[string]string, error) { return nil, nil }
		}
	}
}

func withAgent(sock string) option { return func(p *primary) { p.agent = sock } }

func newPrimary(t *testing.T, opts ...option) *primary {
	t.Helper()
	p := &primary{
		t:     t,
		clock: &clock{now: time.Date(2026, 11, 10, 13, 30, 0, 0, time.UTC)},
		db:    filepath.Join(t.TempDir(), "manager.db"),
		steps: map[string]setup.Handler{},
		local: netip.MustParseAddr("127.0.0.1"),
		hosts: []string{"192.168.1.20", "fd00:1::20", "study-pc", "127.0.0.1", "::1", "localhost"},
		port:  7020,

		lanAddress: netip.MustParseAddr("192.168.1.20"),
	}
	for _, o := range opts {
		o(p)
	}
	if p.agent == "" {
		cfg := p.agentConfig
		cfg.Log = p.log
		p.agent, p.host = agenttest.StartWith(t, cfg)
	}
	p.open()
	return p
}

func (p *primary) open() {
	p.t.Helper()
	m, err := manager.New(manager.Config{
		DB:          p.db,
		AgentSocket: p.agent,
		SetupPort:   p.port,
		Local:       p.local,
		Now:         p.clock.Now,
		Elapsed:     p.clock.Elapsed,
		Boot:        p.clock.Boot,
		LAN: func() lan.LAN {
			return lan.LAN{
				Prefixes:   []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24"), netip.MustParsePrefix("fd00:1::/64")},
				Interfaces: []string{"enp5s0", "wlp4s0"},
			}
		},
		Hosts:      func() []string { return p.hosts },
		LANAddress: func() netip.Addr { p.lanReads.Add(1); return p.lanAddress },
		Web:        p.web,
		Catalog:    p.catalog,
		Steps:      p.steps,
		NetBird:    p.netbird,
		Domains:    p.domains,
		SignIn:     p.signIn,
		// The provider's server: the stand-in's issuer. Waits are short, in real time.
		SignInUpstream: p.signInUpstream,
		SignInWait:     300 * time.Millisecond,
		SignInPoll:     10 * time.Millisecond,
		Log:            p.log,
	})
	if err != nil {
		p.t.Fatal(err)
	}
	p.m = m
	p.t.Cleanup(func() { _ = m.Close() })
}

// reboot stops the manager and boots the machine again before starting it.
func (p *primary) reboot() {
	p.t.Helper()
	if err := p.m.Close(); err != nil {
		p.t.Fatal(err)
	}
	p.clock.reboot()
	p.open()
}

// dbFolder is the folder that holds the manager's database.
func (p *primary) dbFolder() string { return filepath.Dir(p.db) }

// restart stops the manager and starts it again on the same database, as after a power cut.
func (p *primary) restart() {
	p.t.Helper()
	if err := p.m.Close(); err != nil {
		p.t.Fatal(err)
	}
	p.open()
}

type response struct {
	Code   int
	Header http.Header
	Body   []byte
}

func (r response) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("%v in %s", err, r.Body)
	}
}

func (r response) errorCode(t *testing.T) string {
	t.Helper()
	var e struct{ Error string }
	r.json(t, &e)
	return e.Error
}

func serve(h http.Handler, req *http.Request) response {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return response{rec.Code, rec.Header(), rec.Body.Bytes()}
}

// control is a request on the manager's local control socket: the CLI, run as root or as the manager's user.
func (p *primary) control(method, path string, body any) response {
	return serve(p.m.Control(), newRequest(method, path, body, "@"))
}

// browser is someone's browser on the setup listener, at one address, keeping its session's token as the page does
// (in sessionStorage, sent as a bearer token). Nonce is its tab's: sent with every claim, so a claim whose answer was
// lost can be sent again.
type browser struct {
	p     *primary
	from  string
	token string
	nonce string
}

func (p *primary) browser(from string) *browser { return &browser{p: p, from: from, nonce: newNonce()} }

// newNonce is a tab's random nonce, as the page makes it: 256 bits, URL-safe.
func newNonce() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (b *browser) do(method, path string, body any, edit ...func(*http.Request)) response {
	req := newRequest(method, path, body, b.from)
	if b.p.port != 7020 {
		req.Host = "192.168.1.20" // a browser leaves out port 80
	}
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	for _, e := range edit {
		e(req)
	}
	r := serve(b.p.m.Setup(), req)
	if c := r.Header.Values("Set-Cookie"); len(c) > 0 {
		b.p.t.Errorf("%s %s set a cookie: %v", method, path, c)
	}
	return r
}

func newRequest(method, path string, body any, from string) *http.Request {
	var r *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	} else {
		r = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "http://192.168.1.20:7020"+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch {
	case from == "@":
		req.RemoteAddr = "@" // a Unix socket's peer
	case strings.Contains(from, ":"):
		req.RemoteAddr = "[" + from + "]:51000"
	default:
		req.RemoteAddr = from + ":51000"
	}
	return req
}

type link struct {
	URL       string    `json:"url"`
	LocalURL  string    `json:"localUrl"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// key is the one-time key in a link, which the setup page sends to claim it.
func (l link) key(t *testing.T) string {
	t.Helper()
	u, err := url.Parse(l.URL)
	if err != nil {
		t.Fatal(err)
	}
	v, err := url.ParseQuery(u.Fragment)
	if err != nil || v.Get("k") == "" {
		t.Fatalf("no key in the link's fragment: %s", l.URL)
	}
	return v.Get("k")
}

func (p *primary) newLink() link {
	p.t.Helper()
	r := p.control("POST", "/api/v1/setup/links", nil)
	if r.Code != http.StatusCreated {
		p.t.Fatalf("making a link: %d %s", r.Code, r.Body)
	}
	var l link
	r.json(p.t, &l)
	return l
}

func (b *browser) claim(l link) response {
	r := b.do("POST", "/api/v1/setup/claim", map[string]string{"key": l.key(b.p.t), "nonce": b.nonce})
	if r.Code == http.StatusOK {
		var s struct{ Token string }
		r.json(b.p.t, &s)
		b.token = s.Token
	}
	return r
}

// claimed is a browser on this machine that has opened a fresh link.
func (p *primary) claimed() *browser {
	p.t.Helper()
	b := p.browser(onTheLAN)
	if r := b.claim(p.newLink()); r.Code != http.StatusOK {
		p.t.Fatalf("claiming a fresh link: %d %s", r.Code, r.Body)
	}
	return b
}

type state struct {
	Half    string `json:"half"`
	Current string `json:"current"`
	Steps   []struct {
		ID      string `json:"id"`
		Half    string `json:"half"`
		State   string `json:"state"`
		Confirm string `json:"confirm"`
	} `json:"steps"`
	Answers map[string]string `json:"answers"`
}

func (b *browser) state() state {
	b.p.t.Helper()
	r := b.do("GET", "/api/v1/setup", nil)
	if r.Code != http.StatusOK {
		b.p.t.Fatalf("GET /api/v1/setup: %d %s", r.Code, r.Body)
	}
	var s state
	r.json(b.p.t, &s)
	return s
}

func (b *browser) answer(step string, body any) response {
	if body == nil {
		body = map[string]string{}
	}
	return b.do("PUT", "/api/v1/setup/steps/"+step, body)
}

func (b *browser) mustAnswer(step string, body any) state {
	b.p.t.Helper()
	r := b.answer(step, body)
	if r.Code != http.StatusOK {
		b.p.t.Fatalf("PUT step %s: %d %s", step, r.Code, r.Body)
	}
	var s state
	r.json(b.p.t, &s)
	return s
}

// firstThree answers the steps this ticket builds.
func (b *browser) firstThree() state {
	b.mustAnswer("welcome", nil)
	b.mustAnswer("you", map[string]string{"name": "Inês Ferreira", "mail": "ines@ferreira.family"})
	return b.mustAnswer("household", map[string]string{"name": "The Ferreira household"})
}

// builtPages stand in for the web app's build: a page, its assets, and a folder and a file that are no asset.
var builtPages = fstest.MapFS{
	"setup.html":      {Data: []byte("<!doctype html><title>setup</title>")},
	"admin.html":      {Data: []byte("<!doctype html><title>admin</title>")},
	"secret.txt":      {Data: []byte("not an asset")},
	"assets/app.js":   {Data: []byte("console.log(1)")},
	"assets/sub/b.js": {Data: []byte("console.log(2)")},
}

func withPages(p *primary) { p.web = builtPages }
