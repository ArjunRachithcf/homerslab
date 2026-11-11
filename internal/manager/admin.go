package manager

import (
	"io/fs"
	"net"
	"net/http"
	"strings"
)

// Admin is Admin's surface, on the manager's Admin socket, which only the edge may open (the socket's group): it
// answers at admin.<main domain> over the mesh, through the edge, and nowhere else. The LAN never reaches it: the
// edge listens on the mesh only, and the setup listener carries none of it.
func (m *Manager) Admin() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", m.adminPage)
	mux.HandleFunc("/", notFound)
	return securityHeaders(m.adminHost(mux))
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
