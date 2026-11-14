package manager

import (
	"io/fs"
	"net"
	"net/http"
	"strings"
)

// Admin is Admin's surface, on the manager's Admin socket, which only the edge may open (the socket's group): it
// answers at admin.<main domain> over the mesh, through the edge, and nowhere else. The LAN never reaches it: the
// edge listens on the mesh only, and the setup listener carries none of it. It signs the admin in with the household
// account (adminaccount.go); so far it is the Store: the catalog, and install plans, which change nothing until they
// are applied.
func (m *Manager) Admin() http.Handler {
	mux := http.NewServeMux()
	m.accountRoutes(mux)
	// The Store and the plans want an Admin session, as every page of Admin does (#17); the control socket serves the
	// same resources to the machine's terminal.
	store := http.NewServeMux()
	m.catalogRoutes(store)
	for _, p := range []string{"/api/v1/catalog", "/api/v1/catalog/", "/api/v1/plans", "/api/v1/plans/"} {
		mux.Handle(p, m.signedIn(store))
	}
	mux.HandleFunc("/api/", notFound)
	for _, p := range []string{"GET /{$}", "GET /store", "GET /store/apps/{id}", "GET /plans/{id}"} {
		mux.Handle(p, m.signedIn(http.HandlerFunc(m.adminPage)))
	}
	mux.HandleFunc("/", notFound)
	return securityHeaders(m.adminHost(adminOrigin(mux)))
}

// adminHost answers only Admin's own name, whatever the edge forwards.
func (m *Manager) adminHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ans, err := m.db.Answers(r.Context())
		if err != nil {
			m.internal(w, "reading the answers", err)
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			host = h
		}
		if ans[ansDomain] == "" || !strings.EqualFold(host, AdminName(ans[ansDomain])) {
			fail(w, http.StatusMisdirectedRequest, "wrong-host")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminPage is Admin's page, from the built web app; Admin itself comes with later tickets.
func (m *Manager) adminPage(w http.ResponseWriter, _ *http.Request) {
	b, err := fs.ReadFile(m.web, "admin.html")
	if err != nil {
		fail(w, http.StatusNotImplemented, "not-built")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}
