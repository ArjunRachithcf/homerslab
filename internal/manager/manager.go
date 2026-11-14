// Package manager is HomersLab's manager: the unprivileged process on the primary that keeps the household's desired
// state in its database and serves the one API that Admin, the CLI and first-time setup use. It reaches the host only
// through the agent.
//
// It answers on three listeners:
//   - the control socket, a Unix socket only root and the manager's own user can open, for the CLI on this machine;
//   - the setup listener, on this machine and its LAN, for first-time setup's page, opened by a one-time link;
//   - Admin's socket, which only the edge opens, for admin.<main domain> over the mesh.
package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ArjunRachithcf/homerslab/catalogs"
	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/catalog"
	"github.com/ArjunRachithcf/homerslab/internal/certs"
	"github.com/ArjunRachithcf/homerslab/internal/lan"
	"github.com/ArjunRachithcf/homerslab/internal/manager/setup"
	"github.com/ArjunRachithcf/homerslab/internal/manager/store"
	"github.com/ArjunRachithcf/homerslab/internal/surfaces"
)

// Config is how a manager is wired.
type Config struct {
	DB          string // the database file
	AgentSocket string // the primary's agent
	SetupPort   int    // the setup listener's port, for the links
	// Local is the loopback address the setup listener answers on, for the link on this machine itself; the zero
	// Addr when it doesn't.
	Local netip.Addr
	// Now is the wall clock, for what the manager records and shows. Elapsed is the time since the machine booted
	// (CLOCK_BOOTTIME), which no one sets and which goes on across the manager's restarts, and Boot is that boot's ID:
	// how old a link or a session is goes by them. Setting the wall clock moves only Now.
	Now     func() time.Time
	Elapsed func() time.Duration
	Boot    func() string
	// LAN is the home network; LANAddress is this machine's address on it, or the zero Addr when the setup listener
	// isn't on the LAN.
	LAN        func() lan.LAN
	LANAddress func() netip.Addr
	// Hosts are the names the setup listener answers to, with SetupPort: the link's own addresses, loopback and the
	// machine's hostname. Any other Host is refused, against DNS rebinding.
	Hosts func() []string
	// Steps adds step handlers to the built ones (setup.Built and the manager's own).
	Steps map[string]setup.Handler
	// NetBird reaches NetBird's management API; the netbird package when nil.
	NetBird NetBird
	// Authority is the certificate authority (Let's Encrypt when its Directory is empty; its staging endpoint for
	// rehearsals and CI), and Domains how the manager reaches it and the DNS provider: Authority through the dns and
	// certs packages when nil.
	Authority certs.Authority
	Domains   Domains
	// SignIn reaches the household account's provider (the authentik package when nil); SignInUpstream is where its
	// server answers on this machine (the agent's sign-in address when empty). SignInWait is how long setting it up
	// waits for it to start, asking every SignInPoll.
	SignIn         SignIn
	SignInUpstream string
	SignInWait     time.Duration
	SignInPoll     time.Duration
	// ScheduleEvery is how often the scheduler looks for due jobs.
	ScheduleEvery time.Duration
	Web           fs.FS // the built pages; surfaces.Dist() when nil
	// Catalog is the catalog the Store shows; the official one when nil.
	Catalog fs.FS
	Log     *slog.Logger
}

// Manager is a running manager.
type Manager struct {
	cfg       Config
	db        *store.Store
	setup     *setup.Setup
	agent     *agentClient
	netbird   NetBird
	domains   Domains
	web       fs.FS
	catalog   *catalog.Catalog
	catalogFS fs.FS
	log       *slog.Logger
	lanClosed atomic.Bool
	certMu    sync.Mutex // one issuance or renewal at a time
	signInMu  sync.Mutex // one setting-up of the household account at a time
	attempts  attempts   // sign-ins to Admin under way

	// given is what the page sees of the secrets the terminal gives (#64): those being checked, and why the last one
	// given for each was refused. By the secret's name; never the secret.
	givenMu  sync.Mutex
	checking map[string]bool
	refusals map[string]string
}

// New opens the manager's database.
func New(c Config) (*Manager, error) {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Elapsed == nil {
		c.Elapsed = elapsed()
	}
	if c.Boot == nil {
		id := bootID()
		c.Boot = func() string { return id }
	}
	if c.LAN == nil {
		c.LAN = lan.Cached(lan.Current, 5*time.Second)
	}
	if c.Hosts == nil {
		hostname, _ := os.Hostname()
		c.Hosts = func() []string { return lan.Names(hostname, c.LAN()) }
	}
	if c.LANAddress == nil {
		c.LANAddress = func() netip.Addr { return netip.Addr{} }
	}
	// Asked on every request with a Host the manager doesn't know: never let a LAN host make it walk the machine.
	c.LANAddress = lan.Cached(c.LANAddress, 5*time.Second)
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.Web == nil {
		c.Web = surfaces.Dist()
	}
	if c.NetBird == nil {
		c.NetBird = realNetBird{}
	}
	if c.Authority.Directory == "" {
		c.Authority = certs.LetsEncrypt
	}
	if c.SignIn == nil {
		c.SignIn = realSignIn{}
	}
	if c.SignInWait == 0 {
		c.SignInWait = 15 * time.Minute // the first start pulls Authentik's images
	}
	if c.SignInPoll == 0 {
		c.SignInPoll = 3 * time.Second
	}
	if c.ScheduleEvery == 0 {
		c.ScheduleEvery = time.Hour
	}
	if c.Catalog == nil {
		c.Catalog = catalogs.Official()
	}
	cat, err := catalog.Load(c.Catalog)
	if err != nil {
		return nil, fmt.Errorf("the catalog: %w", err)
	}
	db, err := store.Open(c.DB, setup.StoreSteps())
	if err != nil {
		return nil, err
	}
	if c.Domains == nil {
		a := c.Authority
		a.Log = c.Log
		c.Domains = realDomains{a: a, book: db.Book()}
	}
	m := &Manager{cfg: c, db: db, agent: newAgentClient(c.AgentSocket), netbird: c.NetBird, domains: c.Domains, web: c.Web, log: c.Log,
		catalog: cat, catalogFS: c.Catalog}
	handlers := map[string]setup.Handler{"signin": m.signin, "network": m.network, "domain": m.domain, "done": m.finish}
	for id, h := range setup.Built {
		handlers[id] = h
	}
	for id, h := range c.Steps {
		handlers[id] = h
	}
	m.setup = setup.New(db, setup.Clock{Now: c.Now, Elapsed: c.Elapsed, Boot: c.Boot}, handlers, map[string]setup.SecretHandler{"mesh": m.meshSecret, "dns": m.dnsSecret})
	answers, err := db.Answers(context.Background())
	if err != nil {
		db.Close()
		return nil, err
	}
	m.lanClosed.Store(answers[ansLANClosed] != "")
	return m, nil
}

// Close closes the database.
func (m *Manager) Close() error { return m.db.Close() }

// Serve answers on the setup listener, the control socket and Admin's socket (for the edge; none when adminLn is
// nil) until ctx ends.
func (m *Manager) Serve(ctx context.Context, setupLn, controlLn, adminLn net.Listener) error {
	servers := []*http.Server{
		{Handler: m.Setup(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
			// The network step bootstraps a mesh and joins it, waiting for the client to connect: up to a few minutes.
			WriteTimeout: 3 * time.Minute, IdleTimeout: 2 * time.Minute},
		{Handler: m.Control(), ReadHeaderTimeout: 10 * time.Second},
		{Handler: m.Admin(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
			WriteTimeout: agent.ReadDeadline + 30*time.Second, IdleTimeout: 2 * time.Minute},
	}
	listeners := []net.Listener{setupLn, controlLn, adminLn}
	if adminLn == nil {
		servers, listeners = servers[:2], listeners[:2]
	}
	scheduled, stop := context.WithCancel(ctx)
	defer stop()
	go m.schedule(scheduled)
	errc := make(chan error, len(servers))
	for i, s := range servers {
		go func() { errc <- s.Serve(listeners[i]) }()
	}
	var err error
	select {
	case err = <-errc:
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdown)
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// Control is the API on the control socket. The socket's file mode is its guard.
func (m *Manager) Control() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/setup/links", m.newLink)
	mux.HandleFunc("PUT /api/v1/setup/secrets/{name}", m.putSecret)
	mux.HandleFunc("POST /api/v1/setup/join", m.postJoin)
	mux.HandleFunc("POST /api/v1/setup/passkey", m.postPasskey)
	mux.HandleFunc("GET /api/v1/drift", m.getDrift)
	mux.HandleFunc("GET /api/v1/mesh", m.getMesh)
	mux.HandleFunc("GET /api/v1/mesh/owner-password", m.getOwnerPassword)
	mux.HandleFunc("GET /api/v1/setup", m.getControlSetup)
	mux.HandleFunc("GET /api/v1/nodes/primary", m.getPrimary)
	mux.HandleFunc("GET /api/v1/dns", m.getDNS)
	mux.HandleFunc("PUT /api/v1/dns/token", m.putDNSToken)
	mux.HandleFunc("GET /api/v1/warnings", m.getWarnings)
	m.catalogRoutes(mux)
	mux.HandleFunc("/", notFound)
	return mux
}

type linkResponse struct {
	URL       string    `json:"url"`
	LocalURL  string    `json:"localUrl,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (m *Manager) newLink(w http.ResponseWriter, r *http.Request) {
	if m.lanClosed.Load() {
		fail(w, http.StatusConflict, "lan-closed")
		return
	}
	l, err := m.setup.NewLink(r.Context())
	if errors.Is(err, setup.ErrLANHalfDone) {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		// Only root and the manager's user reach the control socket, so its answer may name the database's folder.
		m.fault(w, "making a link", err, map[string]string{"folder": filepath.Dir(m.cfg.DB)})
		return
	}
	// The key rides in the fragment, which browsers never send: it stays out of logs and Referer headers.
	at := func(a netip.Addr) string {
		return fmt.Sprintf("http://%s/setup#k=%s", netip.AddrPortFrom(a, uint16(m.cfg.SetupPort)), l.Key)
	}
	resp := linkResponse{ExpiresAt: l.ExpiresAt}
	if m.cfg.Local.IsValid() {
		resp.LocalURL = at(m.cfg.Local)
	}
	resp.URL = resp.LocalURL
	if a := m.cfg.LANAddress(); a.IsValid() {
		resp.URL = at(a)
	}
	m.log.Info("made a one-time setup link", "expires", l.ExpiresAt)
	writeJSON(w, http.StatusCreated, resp)
}

func (m *Manager) getSetup(w http.ResponseWriter, r *http.Request) {
	st, err := m.setup.State(r.Context())
	if err != nil {
		m.internal(w, "reading setup", err)
		return
	}
	writeJSON(w, http.StatusOK, m.withGiven(st))
}

// getControlSetup is setup for the terminal: the page's state, and who claimed the setup session that is open, so
// `homerslab setup secret` can say whose answers decide where a secret goes (#64).
func (m *Manager) getControlSetup(w http.ResponseWriter, r *http.Request) {
	st, err := m.setup.State(r.Context())
	if err != nil {
		m.internal(w, "reading setup", err)
		return
	}
	c, err := m.setup.Claimed(r.Context())
	if err != nil {
		m.internal(w, "reading setup's claim", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		setup.State
		Claim *setup.Claimant `json:"claim,omitempty"`
	}{m.withGiven(st), c})
}

// withGiven shows a waiting step's secret as the terminal gives it: checking while it is checked, and why the last one
// was refused.
func (m *Manager) withGiven(st setup.State) setup.State {
	m.givenMu.Lock()
	defer m.givenMu.Unlock()
	for i, s := range st.Steps {
		if s.State != "waiting" || s.Secret == "" {
			continue
		}
		if m.checking[s.Secret] {
			st.Steps[i].State = "checking"
		} else {
			st.Steps[i].Refused = m.refusals[s.Secret]
		}
	}
	return st
}

// given records a secret's progress: checking it, then done with it, refused for why ("" when it was taken).
func (m *Manager) given(name string, checking bool, why string) {
	m.givenMu.Lock()
	defer m.givenMu.Unlock()
	if m.checking == nil {
		m.checking, m.refusals = map[string]bool{}, map[string]string{}
	}
	m.checking[name] = checking
	if !checking {
		m.refusals[name] = why
	}
}

func (m *Manager) getPrimary(w http.ResponseWriter, r *http.Request) {
	facts, err := m.agent.node(r.Context())
	if err != nil {
		m.log.Warn("asking the agent for its facts", "err", err)
		fail(w, http.StatusServiceUnavailable, "agent-unreachable")
		return
	}
	writeJSON(w, http.StatusOK, facts)
}

// stepBasis reads the view of the step an answer was made from: its revision in If-Match (a quoted number, as the
// state gave it), and ?edit=true when the answer changes a step that is done.
func stepBasis(r *http.Request) (setup.Basis, string) {
	h := r.Header.Get("If-Match")
	if h == "" {
		return setup.Basis{}, "revision-required"
	}
	n, err := strconv.ParseInt(strings.Trim(h, `"`), 10, 64)
	if err != nil || n < 0 || !strings.HasPrefix(h, `"`) || !strings.HasSuffix(h, `"`) {
		return setup.Basis{}, "bad-revision"
	}
	switch r.URL.Query().Get("edit") {
	case "":
		return setup.Basis{Revision: n}, ""
	case "true":
		return setup.Basis{Revision: n, Edit: true}, ""
	}
	return setup.Basis{}, "bad-edit"
}

func (m *Manager) putStep(w http.ResponseWriter, r *http.Request) {
	basis, bad := stepBasis(r)
	if bad != "" {
		status := http.StatusBadRequest
		if bad == "revision-required" {
			status = http.StatusPreconditionRequired
		}
		fail(w, status, bad)
		return
	}
	var raw json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&raw); err != nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	st, err := m.setup.Answer(r.Context(), r.PathValue("id"), setup.LAN, raw, basis)
	var invalid *setup.Invalid
	var overHTTP *setup.SecretOverHTTP
	switch {
	case err == nil:
		if secret := setup.Secrets[r.PathValue("id")]; secret != "" {
			m.given(secret, false, "") // a new answer: a new wait for its secret
		}
		writeJSON(w, http.StatusOK, m.withGiven(st))
	case errors.As(err, &overHTTP):
		// Refused whatever its value: no secret travels over plain HTTP (#64). The page shows the command.
		m.log.Warn("refused a secret sent over the LAN page", "step", r.PathValue("id"))
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "secret-over-http", "command": overHTTP.Command})
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid", "fields": invalid.Fields, "reasons": invalid.Reasons})
	case errors.Is(err, setup.ErrUnknownStep):
		fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, setup.ErrOverTheMesh):
		fail(w, http.StatusForbidden, err.Error())
	case errors.Is(err, setup.ErrEarlierStepOpen), errors.Is(err, setup.ErrLANHalfDone), errors.Is(err, setup.ErrStale), errors.Is(err, setup.ErrStepDone):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, setup.ErrNotBuilt):
		fail(w, http.StatusNotImplemented, err.Error())
	default:
		m.internal(w, "answering a step", err)
	}
}

// internal answers an error the request can't help: 507 disk-full when the disk under the database is full, else 500.
func (m *Manager) internal(w http.ResponseWriter, what string, err error) { m.fault(w, what, err, nil) }

// fault is internal, with more to say when the disk is full.
func (m *Manager) fault(w http.ResponseWriter, what string, err error, onFullDisk map[string]string) {
	m.log.Error(what, "err", err)
	if !errors.Is(err, store.ErrDiskFull) {
		fail(w, http.StatusInternalServerError, "internal")
		return
	}
	body := map[string]string{"error": "disk-full"}
	for k, v := range onFullDisk {
		body[k] = v
	}
	writeJSON(w, http.StatusInsufficientStorage, body)
}

func notFound(w http.ResponseWriter, _ *http.Request) { fail(w, http.StatusNotFound, "not-found") }

func fail(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
