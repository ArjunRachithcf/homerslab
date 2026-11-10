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
	"time"

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
	// LAN lists the home network's prefixes; LANAddress is this machine's address on it, or the zero Addr when the
	// setup listener isn't on the LAN.
	LAN        func() []netip.Prefix
	LANAddress func() netip.Addr
	// Steps adds step handlers to the built ones (setup.Built).
	Steps map[string]setup.Handler
	Web   fs.FS // the built pages; surfaces.Dist() when nil
	Log   *slog.Logger
}

// Manager is a running manager.
type Manager struct {
	cfg   Config
	db    *store.Store
	setup *setup.Setup
	agent *agentClient
	web   fs.FS
	log   *slog.Logger
}

// New opens the manager's database.
func New(c Config) (*Manager, error) {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.LAN == nil {
		c.LAN = lan.Cached(lan.Networks, 5*time.Second)
	}
	if c.LANAddress == nil {
		c.LANAddress = func() netip.Addr { return netip.Addr{} }
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.Web == nil {
		c.Web = surfaces.Dist()
	}
	db, err := store.Open(c.DB, setup.StoreSteps())
	if err != nil {
		return nil, err
	}
	handlers := map[string]setup.Handler{}
	for id, h := range setup.Built {
		handlers[id] = h
	}
	for id, h := range c.Steps {
		handlers[id] = h
	}
	return &Manager{
		cfg:   c,
		db:    db,
		setup: setup.New(db, c.Now, handlers),
		agent: newAgentClient(c.AgentSocket),
		web:   c.Web,
		log:   c.Log,
	}, nil
}

// Close closes the database.
func (m *Manager) Close() error { return m.db.Close() }

// Serve answers on the setup listener and the control socket until ctx ends.
func (m *Manager) Serve(ctx context.Context, setupLn, controlLn net.Listener) error {
	servers := []*http.Server{
		{Handler: m.Setup(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute},
		{Handler: m.Control(), ReadHeaderTimeout: 10 * time.Second},
	}
	errc := make(chan error, 2)
	go func() { errc <- servers[0].Serve(setupLn) }()
	go func() { errc <- servers[1].Serve(controlLn) }()
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
	mux.HandleFunc("GET /api/v1/setup", m.getSetup)
	mux.HandleFunc("GET /api/v1/nodes/primary", m.getPrimary)
	mux.HandleFunc("/", notFound)
	return mux
}

type linkResponse struct {
	URL       string    `json:"url"`
	LocalURL  string    `json:"localUrl,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (m *Manager) newLink(w http.ResponseWriter, r *http.Request) {
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
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, st)
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
