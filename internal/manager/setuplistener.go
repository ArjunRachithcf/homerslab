package manager

import (
	"encoding/json"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"net/netip"
	"strings"

	"github.com/ArjunRachithcf/homerslab/internal/lan"
	"github.com/ArjunRachithcf/homerslab/internal/manager/setup"
)

const sessionCookie = "homerslab_setup"

// Setup is the API and the page on the setup listener. Every request must come from this machine or its LAN; the
// API wants the session a claimed link opened, and state changes must come from the page itself.
func (m *Manager) Setup() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("POST /api/v1/setup/claim", m.claim)
	api.Handle("GET /api/v1/setup", m.session(http.HandlerFunc(m.getSetup)))
	api.Handle("PUT /api/v1/setup/steps/{id}", m.session(http.HandlerFunc(m.putStep)))
	api.Handle("GET /api/v1/nodes/primary", m.session(http.HandlerFunc(m.getPrimary)))
	api.HandleFunc("/api/", notFound)
	api.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/setup", http.StatusSeeOther) })
	api.HandleFunc("GET /setup", m.page)
	api.Handle("GET /assets/{file}", http.FileServerFS(m.web)) // files only: no listing
	api.HandleFunc("/", notFound)
	return securityHeaders(m.fromHere(sameOrigin(api)))
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
	// Plain HTTP on the LAN, so not Secure; HttpOnly and SameSite=Strict keep it out of scripts and other sites.
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) session(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err == nil {
			from, _ := client(r)
			err = m.setup.Session(r.Context(), c.Value, from.String())
		}
		if errors.Is(err, setup.ErrNoSession) || errors.Is(err, http.ErrNoCookie) {
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

// client is the address a request comes from, without a port or an interface.
func client(r *http.Request) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap().WithZone(""), true
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
