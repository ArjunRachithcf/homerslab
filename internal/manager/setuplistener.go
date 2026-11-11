package manager

import (
	"encoding/json"
	"errors"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/ArjunRachithcf/homerslab/internal/lan"
	"github.com/ArjunRachithcf/homerslab/internal/manager/setup"
)

// Setup is the API and the page on the setup listener. Every request must come from this machine or its LAN, and name
// one of its own hosts; the API wants the session a claimed link opened, and state changes must come from the page
// itself.
func (m *Manager) Setup() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("POST /api/v1/setup/claim", m.claim)
	api.Handle("GET /api/v1/setup", m.session(http.HandlerFunc(m.getSetup)))
	api.Handle("PUT /api/v1/setup/steps/{id}", m.session(http.HandlerFunc(m.putStep)))
	api.Handle("GET /api/v1/nodes/primary", m.session(http.HandlerFunc(m.getPrimary)))
	api.HandleFunc("/api/", notFound)
	api.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/setup", http.StatusSeeOther) })
	api.HandleFunc("GET /setup", m.page)
	api.HandleFunc("GET /assets/{file}", m.asset)
	api.HandleFunc("/", notFound)
	return securityHeaders(m.open(m.fromHere(m.ownHost(sameOrigin(api)))))
}

// open answers nothing once the LAN listener is closed for good, at the admin's first sign-in over the mesh.
func (m *Manager) open(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.lanClosed.Load() {
			fail(w, http.StatusGone, "lan-closed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; style-src-attr 'unsafe-inline'; "+
			"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// fromHere refuses anything that doesn't come from this machine or its LAN.
func (m *Manager) fromHere(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a, ok := client(r); !ok || !lan.FromHere(a, m.cfg.LAN()) {
			fail(w, http.StatusForbidden, "not-from-lan")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ownHost answers only requests that name this machine, at the setup listener's port: a site whose name was rebound
// to this address gets nothing.
func (m *Manager) ownHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.Host)
		if err != nil && !strings.Contains(r.Host, ":") { // no port: a browser leaves out 80
			host, port, err = r.Host, "80", nil
		}
		if err != nil || host == "" || port != strconv.Itoa(m.cfg.SetupPort) || !m.ownName(strings.ToLower(host)) {
			fail(w, http.StatusMisdirectedRequest, "wrong-host")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ownName says whether host names this machine: one of its names, or an address its links carry.
func (m *Manager) ownName(host string) bool {
	if slices.Contains(m.cfg.Hosts(), host) {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && (a == m.cfg.LANAddress() || a == m.cfg.Local)
}

// sameOrigin wants a change to be JSON sent by the setup page: no form post or cross-site request can make one.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
			fail(w, http.StatusForbidden, "cross-origin")
			return
		}
		if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" && s != "none" {
			fail(w, http.StatusForbidden, "cross-origin")
			return
		}
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			fail(w, http.StatusUnsupportedMediaType, "not-json")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (m *Manager) claim(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || body.Key == "" {
		fail(w, http.StatusForbidden, setup.ErrLinkRefused.Error())
		return
	}
	from, _ := client(r)
	tok, err := m.setup.Claim(r.Context(), body.Key, from.String())
	if errors.Is(err, setup.ErrLinkRefused) {
		m.log.Warn("refused a setup link", "from", r.RemoteAddr)
		fail(w, http.StatusForbidden, err.Error())
		return
	}
	if err != nil {
		m.internal(w, "claiming a link", err)
		return
	}
	m.log.Info("setup link claimed", "from", r.RemoteAddr)
	// Not a cookie: cookies ignore ports, so every other service on this machine's addresses would be sent it. The
	// page keeps the token in its tab and sends it as a bearer token.
	writeJSON(w, http.StatusOK, map[string]string{"token": tok})
}

func (m *Manager) session(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		err := setup.ErrNoSession
		if ok && tok != "" {
			from, _ := client(r)
			err = m.setup.Session(r.Context(), tok, from.String())
		}
		if errors.Is(err, setup.ErrNoSession) {
			fail(w, http.StatusUnauthorized, setup.ErrNoSession.Error())
			return
		}
		if err != nil {
			m.internal(w, "checking a session", err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// client is the address a request comes from, without its port. A link-local address keeps its interface: the same
// address on another interface is another device.
func client(r *http.Request) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap(), true
}

// asset serves one file of the built pages' assets folder: a plain name, never a folder, never a path out of it.
func (m *Manager) asset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if name == "" || name == "." || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		notFound(w, r)
		return
	}
	path := "assets/" + name
	if fi, err := fs.Stat(m.web, path); err != nil || fi.IsDir() {
		notFound(w, r)
		return
	}
	http.ServeFileFS(w, r, m.web, path)
}

// page serves the setup page. It carries no data: the page asks the API, with the key from the link's fragment.
func (m *Manager) page(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(m.web, "setup.html")
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(strings.TrimSpace(`This HomersLab was built without its web pages.`) + "\n"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}
