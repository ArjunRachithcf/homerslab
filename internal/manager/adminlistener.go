package manager

import (
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// Admin is Admin's pages and API, on Admin's own listener. Until Admin signs people in with the household account
// (#17), which moves it to the mesh, the listener answers only this machine itself, by one of its loopback names at
// Admin's port, and takes changes only as JSON sent by Admin's own page. Nothing it serves changes the household:
// it shows the catalog, and makes plans, which change nothing until they are applied.
func (m *Manager) Admin() http.Handler {
	mux := http.NewServeMux()
	m.catalogRoutes(mux)
	mux.HandleFunc("/api/", notFound)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/store", http.StatusSeeOther) })
	for _, p := range []string{"GET /store", "GET /store/apps/{id}", "GET /plans/{id}"} {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) { m.servePage(w, "admin.html") })
	}
	mux.HandleFunc("GET /assets/{file}", m.asset)
	mux.HandleFunc("/", notFound)
	return securityHeaders(m.thisMachine(m.adminHost(sameOrigin(mux))))
}

// catalogRoutes are the catalog's and the install plans' resources, the same on Admin's listener and the control
// socket.
func (m *Manager) catalogRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/catalog", m.getCatalog)
	mux.HandleFunc("GET /api/v1/catalog/apps/{id}", m.getCatalogApp)
	mux.HandleFunc("GET /api/v1/catalog/apps/{id}/icon", m.getIcon)
	mux.HandleFunc("GET /api/v1/catalog/apps/{id}/screenshots/{n}", m.getScreenshot)
	mux.HandleFunc("POST /api/v1/plans", m.newPlan)
	mux.HandleFunc("GET /api/v1/plans/{id}", m.getPlan)
}

// thisMachine refuses any request that doesn't come from this machine itself.
func (m *Manager) thisMachine(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a, ok := client(r); !ok || !a.IsLoopback() {
			fail(w, http.StatusForbidden, "not-this-machine")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminHost answers only requests that name this machine by a loopback name at Admin's port: a site whose name was
// rebound to 127.0.0.1 gets nothing.
func (m *Manager) adminHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.Host)
		switch strings.ToLower(host) {
		case "127.0.0.1", "localhost", "::1":
		default:
			err = fmt.Errorf("not this machine's name")
		}
		if err != nil || port != strconv.Itoa(m.cfg.AdminPort) {
			fail(w, http.StatusMisdirectedRequest, "wrong-host")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminURL is where Admin opens on this machine.
func (m *Manager) adminURL() string { return fmt.Sprintf("http://127.0.0.1:%d/store", m.cfg.AdminPort) }

func (m *Manager) getAdmin(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"url": m.adminURL()})
}

// servePage serves one of the built pages. It carries no data: the page asks the API.
func (m *Manager) servePage(w http.ResponseWriter, name string) {
	b, err := fs.ReadFile(m.web, name)
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("This HomersLab was built without its web pages.\n"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}
