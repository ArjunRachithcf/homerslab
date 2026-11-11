// Package manager is HomersLab's manager: the unprivileged process on the primary that keeps the household's desired
// state in its database and serves the one API that Admin, the CLI and first-time setup use. It reaches the host only
// through the agent.
//
// It answers on two listeners:
//   - the control socket, a Unix socket only root and the manager's own user can open, for the CLI on this machine;
//   - the setup listener, on this machine and its LAN, for first-time setup's page, opened by a one-time link.
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
	"sync/atomic"
	"time"

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
	Now   func() time.Time
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
	// ScheduleEvery is how often the scheduler looks for due jobs.
	ScheduleEvery time.Duration
	Web           fs.FS // the built pages; surfaces.Dist() when nil
	Log           *slog.Logger
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
	log       *slog.Logger
	lanClosed atomic.Bool
}

// New opens the manager's database.
func New(c Config) (*Manager, error) {
	if c.Now == nil {
		c.Now = time.Now
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
	if c.ScheduleEvery == 0 {
		c.ScheduleEvery = time.Hour
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
	m := &Manager{cfg: c, db: db, agent: newAgentClient(c.AgentSocket), netbird: c.NetBird, domains: c.Domains, web: c.Web, log: c.Log}
	handlers := map[string]setup.Handler{"network": m.network, "domain": m.domain}
	for id, h := range setup.Built {
		handlers[id] = h
	}
	for id, h := range c.Steps {
		handlers[id] = h
	}
	m.setup = setup.New(db, c.Now, handlers, map[string]setup.SecretHandler{"mesh": m.meshSecret, "dns": m.dnsSecret})
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
		{Handler: m.Admin(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute},
	}
	scheduled, stop := context.WithCancel(ctx)
	defer stop()
	go m.schedule(scheduled)
	errc := make(chan error, len(servers))
	go func() { errc <- servers[0].Serve(setupLn) }()
	go func() { errc <- servers[1].Serve(controlLn) }()
	if adminLn != nil {
		go func() { errc <- servers[2].Serve(adminLn) }()
	} else {
		servers = servers[:2]
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
	mux.HandleFunc("GET /api/v1/drift", m.getDrift)
	mux.HandleFunc("GET /api/v1/mesh", m.getMesh)
	mux.HandleFunc("GET /api/v1/setup", m.getSetup)
	mux.HandleFunc("GET /api/v1/nodes/primary", m.getPrimary)
	mux.HandleFunc("GET /api/v1/dns", m.getDNS)
	mux.HandleFunc("PUT /api/v1/dns/token", m.putDNSToken)
	mux.HandleFunc("GET /api/v1/warnings", m.getWarnings)
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
		m.internal(w, "making a link", err)
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
	writeJSON(w, http.StatusOK, st)
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

func (m *Manager) putStep(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&raw); err != nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	st, err := m.setup.Answer(r.Context(), r.PathValue("id"), setup.LAN, raw)
	var invalid *setup.Invalid
	var overHTTP *setup.SecretOverHTTP
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, st)
	case errors.As(err, &overHTTP):
		// Refused whatever its value: no secret travels over plain HTTP (#64). The page shows the command.
		m.log.Warn("refused a secret sent over the LAN page", "step", r.PathValue("id"))
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "secret-over-http", "command": overHTTP.Command})
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid", "fields": invalid.Fields})
	case errors.Is(err, setup.ErrUnknownStep):
		fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, setup.ErrOverTheMesh):
		fail(w, http.StatusForbidden, err.Error())
	case errors.Is(err, setup.ErrEarlierStepOpen), errors.Is(err, setup.ErrLANHalfDone):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, setup.ErrNotBuilt):
		fail(w, http.StatusNotImplemented, err.Error())
	default:
		m.internal(w, "answering a step", err)
	}
}

func (m *Manager) internal(w http.ResponseWriter, what string, err error) {
	m.log.Error(what, "err", err)
	fail(w, http.StatusInternalServerError, "internal")
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
