package manager

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
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
	api.HandleFunc("GET /setup", func(w http.ResponseWriter, _ *http.Request) { m.servePage(w, "setup.html") })
	api.HandleFunc("GET /assets/{file}", m.asset)
	api.HandleFunc("/", notFound)
	return securityHeaders(m.open(m.fromHere(m.ownHost(m.noSecrets(sameOrigin(api))))))
}

// bodyMax is the largest body the setup listener reads: a step's answer's limit.
const bodyMax = 64 << 10

// noSecrets refuses every request that carries a secret, before anything else looks at it (its session, its step, its
// order): no secret travels over the LAN's plain HTTP (#64). A field named like one, in a query, a JSON body at any
// depth or a form, is refused whatever its value, and so is any request to the control socket's address for secrets.
// The refusal names the terminal command that gives the secret instead.
func (m *Manager) noSecrets(next http.Handler) http.Handler {
	refuse := func(w http.ResponseWriter, r *http.Request, command string) {
		m.log.Warn("refused a secret sent to the setup listener") // never its path or query: either may hold it
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "secret-over-http", "command": command})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		command := setup.AnyCommand
		if id, ok := strings.CutPrefix(r.URL.Path, "/api/v1/setup/steps/"); ok {
			command = setup.CommandFor(id)
		}
		if name, ok := strings.CutPrefix(r.URL.Path, "/api/v1/setup/secrets/"); ok {
			if slices.Contains(slices.Collect(maps.Values(setup.Secrets)), name) {
				command = setup.Command(name)
			}
			refuse(w, r, command)
			return
		}
		for k := range r.URL.Query() {
			if setup.SecretField(k) {
				refuse(w, r, command)
				return
			}
		}
		if r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, bodyMax+1))
		if err != nil {
			fail(w, http.StatusBadRequest, "unreadable")
			return
		}
		if len(b) > bodyMax {
			fail(w, http.StatusRequestEntityTooLarge, "too-large")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
		var allowed []string
		if r.URL.Path == "/api/v1/setup/claim" {
			allowed = []string{"key"} // the link's own key
		}
		if setup.CarriesSecret(b, allowed...) || (!json.Valid(b) && formCarriesSecret(b)) {
			refuse(w, r, command)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// formCarriesSecret says whether a body read as a form has a field named like a secret. The listener takes no form,
// but a secret in one is refused as a secret, with its command.
func formCarriesSecret(b []byte) bool {
	v, err := url.ParseQuery(string(b))
	if err != nil {
		return false
	}
	for k := range v {
		if setup.SecretField(k) {
			return true
		}
	}
	return false
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
		if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" {
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
		Key   string `json:"key"`
		Nonce string `json:"nonce"` // the claiming tab's, so that tab can send the claim again
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || body.Key == "" {
		fail(w, http.StatusForbidden, setup.ErrLinkRefused.Error())
		return
	}
	from, _ := client(r)
	tok, again, err := m.setup.Claim(r.Context(), body.Key, body.Nonce, from.String())
	if errors.Is(err, setup.ErrBadNonce) { // the page's own bug, or an old page: not a reason to let go of the key
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, setup.ErrLinkRefused) {
		m.log.Warn("refused a setup link", "from", r.RemoteAddr)
		fail(w, http.StatusForbidden, err.Error())
		return
	}
	if err != nil {
		m.internal(w, "claiming a link", err)
		return
	}
	if again {
		m.log.Info("setup link's claim sent again by the tab that claimed it: the same session, a new token", "from", r.RemoteAddr)
	} else {
		m.log.Info("setup link claimed", "from", r.RemoteAddr)
	}
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
