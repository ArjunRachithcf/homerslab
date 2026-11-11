package manager

import (
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// catalogRoutes are the catalog's and the install plans' resources, the same on Admin's socket and the control
// socket.
func (m *Manager) catalogRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/catalog", m.getCatalog)
	mux.HandleFunc("GET /api/v1/catalog/apps/{id}", m.getCatalogApp)
	mux.HandleFunc("GET /api/v1/catalog/apps/{id}/icon", m.getIcon)
	mux.HandleFunc("GET /api/v1/catalog/apps/{id}/screenshots/{n}", m.getScreenshot)
	mux.HandleFunc("POST /api/v1/plans", m.newPlan)
	mux.HandleFunc("GET /api/v1/plans/{id}", m.getPlan)
}

// adminOrigin takes a change on Admin only as JSON sent by Admin's own page, which the edge serves over HTTPS at
// Admin's name: no form post or cross-site request can make one.
func adminOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			host = h
		}
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), host) {
				fail(w, http.StatusForbidden, "cross-origin")
				return
			}
		}
		if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" && s != "none" {
			fail(w, http.StatusForbidden, "cross-origin")
			return
		}
		if !isJSON(r) {
			fail(w, http.StatusUnsupportedMediaType, "not-json")
			return
		}
		next.ServeHTTP(w, r)
	})
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

// isJSON says whether a request's body is JSON, by its Content-Type.
func isJSON(r *http.Request) bool {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return mt == "application/json"
}
