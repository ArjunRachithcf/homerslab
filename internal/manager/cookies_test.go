package manager_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// Every cookie HomersLab sets is Secure (#64), and Admin's are __Host- cookies (#83): Secure, for Admin's own name
// only, and for every path. The setup listener sets none at all (the browser helper fails a test on any). This walks
// every surface the manager serves over the edge and its control socket.
func TestEveryCookieTheManagerSetsIsSecure(t *testing.T) {
	testenv.Isolate(t)
	p, b, _, _ := atDomain(t, func(p *primary) {
		p.web = fstest.MapFS{"admin.html": {Data: []byte("<!doctype html><title>Admin</title>")}, "assets/a.js": {Data: []byte("1")}}
	})
	throughDomain(t, p, b)
	for _, s := range []struct {
		name string
		h    http.Handler
	}{{"Admin", p.m.Admin()}, {"the control socket", p.m.Control()}} {
		for _, path := range []string{"/", "/store", "/store/apps/immich", "/plans/x", "/assets/a.js", "/api/v1/catalog", "/api/v1/setup",
			"/api/v1/dns", "/api/v1/warnings", "/nowhere"} {
			req := httptest.NewRequest("GET", "https://admin.example.test:9443"+path, nil)
			req.RemoteAddr = "@"
			rec := httptest.NewRecorder()
			s.h.ServeHTTP(rec, req)
			if raw, parsed := rec.Header().Values("Set-Cookie"), rec.Result().Cookies(); len(raw) != len(parsed) {
				t.Errorf("%s at %s sets cookies that don't parse: %q", s.name, path, raw)
			}
			for _, c := range rec.Result().Cookies() {
				if !c.Secure || c.Domain != "" || c.Path != "/" || !strings.HasPrefix(c.Name, "__Host-") {
					t.Errorf("%s at %s sets the cookie %q: Secure %v, Domain %q, Path %q; want a __Host- cookie", s.name, path, c.Name, c.Secure, c.Domain, c.Path)
				}
			}
		}
	}
}
