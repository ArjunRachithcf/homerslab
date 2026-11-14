package edge_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/edge"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

var update = flag.Bool("update", false, "write the golden renders anew")

// thePrimary is the edge on a primary right after first-time setup's first half: one mesh address, the household's
// port, the wildcard certificate from the agent's sealed credentials, and Admin.
func thePrimary() edge.Config {
	return edge.Config{
		AdminSocket:  "/run/homerslab-edge/admin.sock",
		Storage:      "/var/lib/homerslab-edge",
		Listen:       []netip.Addr{netip.MustParseAddr("100.88.7.186")},
		Port:         443,
		Certificates: []edge.Certificate{{Domain: "example.test", CertFile: "/run/credentials/homerslab-edge.service/tls-example.test.crt", KeyFile: "/run/credentials/homerslab-edge.service/tls-example.test.key"}},
		Admin:        &edge.Site{Name: "admin.example.test", Upstream: "/run/homerslab-admin.sock"},
	}
}

// Golden renders: what the agent loads into the edge is compared byte for byte with a committed baseline. A
// deliberate change runs `go test ./internal/edge -update` and explains the diff.
func TestTheEdgesConfigurationIsItsGoldenRender(t *testing.T) {
	testenv.Isolate(t)
	twoAddresses := thePrimary()
	twoAddresses.Listen = append(twoAddresses.Listen, netip.MustParseAddr("fd7a:115c:a1e0::1"))
	twoAddresses.Port = 9443
	for name, c := range map[string]edge.Config{
		"primary":          thePrimary(),
		"with-sign-in":     withSignIn(),
		"guarded-app":      withAGuardedApp(),
		"sign-in-listener": withTheSignInListener(),
		"with-home":        withHome(),
		"cloud-port-two":   twoAddresses,
		"before-the-cert": {AdminSocket: "/run/homerslab-edge/admin.sock", Storage: "/var/lib/homerslab-edge",
			Listen: []netip.Addr{netip.MustParseAddr("100.88.7.186")}, Port: 443},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := edge.Render(c)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", "golden", name+".json")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update to write it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("the render differs from %s:\n%s", path, got)
			}
		})
	}
}

// No private key is readable through the edge (#78): autosave is off, and certificates are loaded from files, never
// inline, so the admin API's GET /config/ never shows a key.
func TestTheEdgeNeverHoldsAKeyInItsConfiguration(t *testing.T) {
	testenv.Isolate(t)
	b, err := edge.Render(thePrimary())
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Admin struct {
			Listen string
			Config struct{ Persist *bool }
		}
		Apps struct {
			TLS struct {
				Certificates map[string]json.RawMessage
			}
		}
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if p := c.Admin.Config.Persist; p == nil || *p {
		t.Errorf("admin.config.persist = %v, want false: Caddy would autosave the configuration", p)
	}
	if !strings.HasPrefix(c.Admin.Listen, "unix//run/homerslab-edge/") {
		t.Errorf("the admin API listens on %q, want a Unix socket in the edge's own directory", c.Admin.Listen)
	}
	for loader := range c.Apps.TLS.Certificates {
		if loader != "load_files" {
			t.Errorf("certificates loaded with %s, want load_files only", loader)
		}
	}
	for _, bad := range []string{"load_pem", "PRIVATE KEY", "BEGIN", "automate", "acme"} {
		if bytes.Contains(b, []byte(bad)) {
			t.Errorf("the configuration holds %q", bad)
		}
	}
}

// The edge listens on the node's mesh addresses only, at the household's port, and never on the LAN or every
// address. A name that isn't on this node gets 421, so a browser that reused a connection opens a new one (#81).
func TestTheEdgeListensOnTheMeshOnlyAndAnswersOtherNames421(t *testing.T) {
	testenv.Isolate(t)
	c := thePrimary()
	c.Port = 9443
	b, err := edge.Render(c)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Listen []string
					Routes []struct {
						Match  []map[string][]string
						Handle []map[string]any
					}
					AutomaticHTTPS struct{ Disable bool } `json:"automatic_https"`
					Protocols      []string
				}
			}
		}
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Apps.HTTP.Servers) != 2 {
		t.Fatalf("servers: %v, want the mesh listener and its port 80, which only redirects", cfg.Apps.HTTP.Servers)
	}
	s := cfg.Apps.HTTP.Servers["mesh"]
	if len(s.Listen) != 1 || s.Listen[0] != "100.88.7.186:9443" {
		t.Errorf("listens on %v, want 100.88.7.186:9443 only", s.Listen)
	}
	if !s.AutomaticHTTPS.Disable {
		t.Error("Caddy's automatic HTTPS is on: it would manage certificates of its own")
	}
	if strings.Join(s.Protocols, ",") != "h1,h2" {
		t.Errorf("protocols %v, want h1 and h2 (no UDP listener for HTTP/3: the mesh policies are TCP)", s.Protocols)
	}
	last := s.Routes[len(s.Routes)-1]
	if len(last.Match) != 0 || len(last.Handle) != 1 || last.Handle[0]["handler"] != "static_response" || last.Handle[0]["status_code"] != float64(421) {
		t.Errorf("the last route is %+v, want a catch-all 421", last)
	}
	if hosts := s.Routes[0].Match[0]["host"]; len(hosts) != 1 || hosts[0] != "admin.example.test" {
		t.Errorf("the first route matches %v, want Admin's name", hosts)
	}

	for _, bad := range []edge.Config{
		{Port: 443},
		{Listen: []netip.Addr{netip.MustParseAddr("0.0.0.0")}, Port: 443},
		{Listen: []netip.Addr{netip.MustParseAddr("192.168.1.20")}, Port: 0},
		{Listen: []netip.Addr{netip.MustParseAddr("100.88.7.186")}, Port: 80}, // port 80 only redirects
	} {
		bad.AdminSocket, bad.Storage = "/run/homerslab-edge/admin.sock", "/var/lib/homerslab-edge"
		if _, err := edge.Render(bad); err == nil {
			t.Errorf("rendered %+v, want it refused", bad)
		}
	}
}

// Before the certificate exists the edge listens nowhere: a server without TLS would answer plain HTTP on the
// household's port.
func TestTheEdgeListensNowhereWithoutACertificate(t *testing.T) {
	testenv.Isolate(t)
	c := thePrimary()
	c.Certificates = nil
	b, err := edge.Render(c)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(`"listen": [`)) || bytes.Contains(b, []byte("servers")) {
		t.Errorf("the edge listens without a certificate:\n%s", b)
	}
}

// The sign-in, auth.<main domain>, on the primary: proxied to the identity provider at its fixed address on the
// sign-in network, after Authentik's first-admin flow is refused outright, its page and its executor API alike (#84).
// The provider makes that flow apply to nobody; the edge never even passes it on.
func TestTheSignInIsServedWithTheFirstAdminFlowCutOff(t *testing.T) {
	testenv.Isolate(t)
	c := thePrimary()
	c.SignIn = &edge.Site{Name: "auth.example.test", Dial: "172.31.250.10:9000"}
	b, err := edge.Render(c)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Match  []map[string][]string
						Handle []struct {
							Handler string
							Routes  []struct {
								Match  []map[string][]string
								Handle []map[string]any
							}
						}
					}
				}
			}
		}
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	var signIn *struct {
		Match  []map[string][]string
		Handle []map[string]any
	}
	for _, r := range cfg.Apps.HTTP.Servers["mesh"].Routes {
		// Its HSTS and Secure cookies first (#64), then the subroute.
		if len(r.Match) == 1 && strings.Join(r.Match[0]["host"], ",") == "auth.example.test" && len(r.Handle) == 2 &&
			r.Handle[0].Handler == "headers" && r.Handle[1].Handler == "subroute" {
			subs := r.Handle[1].Routes
			if len(subs) != 2 {
				t.Fatalf("the sign-in's subroutes: %+v", subs)
			}
			paths := strings.Join(subs[0].Match[0]["path"], " ")
			for _, p := range []string{"/if/flow/initial-setup/*", "/api/v3/flows/executor/initial-setup/*", "/api/v3/flows/instances/initial-setup/*"} {
				if !strings.Contains(paths, p) {
					t.Errorf("the first refused paths %q miss %s", paths, p)
				}
			}
			if subs[0].Handle[0]["handler"] != "static_response" || subs[0].Handle[0]["status_code"] != float64(403) {
				t.Errorf("the first-admin paths are answered with %v, want 403", subs[0].Handle[0])
			}
			signIn = &subs[1]
		}
	}
	if signIn == nil {
		t.Fatalf("no route for the sign-in:\n%s", b)
	}
	if signIn.Handle[0]["handler"] != "reverse_proxy" || !strings.Contains(string(b), `"dial": "172.31.250.10:9000"`) {
		t.Errorf("the sign-in proxies with %v", signIn.Handle[0])
	}
	if bad := c; true {
		bad.SignIn = &edge.Site{Name: "auth.example.test", Dial: "10.0.0.1"}
		if _, err := edge.Render(bad); err == nil {
			t.Error("rendered a sign-in without a port to dial")
		}
	}
}

// withSignIn is the primary once the household account is set up: the sign-in beside Admin.
func withSignIn() edge.Config {
	c := thePrimary()
	c.SignIn = &edge.Site{Name: "auth.example.test", Dial: "172.31.250.10:9000"}
	return c
}

// withHome is the primary once Home can sign people in (#21): Home's site beside Admin and the sign-in, to Home's own
// socket, which only the edge may open.
func withHome() edge.Config {
	c := withSignIn()
	c.Home = &edge.Site{Name: "home.example.test", Upstream: "/run/homerslab-home.sock"}
	return c
}

// withAGuardedApp is the primary with the sign-in and one guarded app, BentoPDF at pdf.example.test, on its one upstream.
func withAGuardedApp() edge.Config {
	c := withSignIn()
	c.Apps = []edge.App{{Name: "pdf.example.test", Dial: "172.31.240.2:8080", Guard: edge.AuthentikGuard("172.31.250.10:9000")}}
	return c
}

// A guarded app's site (#19): the client's identity headers are stripped before anything else; the provider's own
// paths go to the provider; every other request asks the guard first, and only a 2xx answer lets it on to the app, with
// the identity the guard names. The guard's own cookie never reaches the app.
func TestAGuardedAppsSiteAsksTheGuardFirst(t *testing.T) {
	testenv.Isolate(t)
	b, err := edge.Render(withAGuardedApp())
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Match  []struct{ Host []string }
						Handle []struct {
							Handler string
							Routes  []struct {
								Match  []map[string]any
								Handle []map[string]any
							}
						}
					}
				}
			}
		}
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, r := range c.Apps.HTTP.Servers["mesh"].Routes {
		if len(r.Match) == 0 || len(r.Match[0].Host) == 0 || r.Match[0].Host[0] != "pdf.example.test" {
			continue
		}
		h := r.Handle[len(r.Handle)-1] // after the headers every name gets (HSTS, Secure cookies)
		for _, sub := range h.Routes {
			for _, h := range sub.Handle {
				step := h["handler"].(string)
				if ups, ok := h["upstreams"].([]any); ok {
					step += " " + ups[0].(map[string]any)["dial"].(string)
				}
				if _, ok := h["rewrite"]; ok {
					step += " (the guard)"
				}
				if len(sub.Match) > 0 {
					step += " if " + fmt_(sub.Match)
				}
				steps = append(steps, step)
			}
		}
	}
	want := []string{
		"headers",
		"reverse_proxy 172.31.250.10:9000 if [{path:[/outpost.goauthentik.io/*]}]",
		"reverse_proxy 172.31.250.10:9000 (the guard)",
		"headers",
		"reverse_proxy 172.31.240.2:8080",
	}
	if strings.Join(steps, "\n") != strings.Join(want, "\n") {
		t.Errorf("the guarded app's site:\n%s\nwant\n%s", strings.Join(steps, "\n"), strings.Join(want, "\n"))
	}
	for _, h := range []string{"X-Authentik-*", "Remote-*", "X-Remote-*", "X-Forwarded-User", "X-Forwarded-Email", "X-Forwarded-Preferred-Username"} {
		if !strings.Contains(string(b), `"`+h+`"`) {
			t.Errorf("the site doesn't strip %s", h)
		}
	}
	// An app without its guard, or a guard without its provider, isn't rendered.
	for _, bad := range []edge.App{{Name: "pdf.example.test", Dial: "172.31.240.2:8080"}, {Name: "pdf.example.test", Dial: "172.31.240.2:8080", Guard: &edge.Guard{}},
		{Name: "pdf.example.test", Dial: "nowhere", Guard: edge.AuthentikGuard("172.31.250.10:9000")}, {Dial: "172.31.240.2:8080", Guard: edge.AuthentikGuard("172.31.250.10:9000")}} {
		c := withSignIn()
		c.Apps = []edge.App{bad}
		if _, err := edge.Render(c); err == nil {
			t.Errorf("rendered %+v", bad)
		}
	}
}

// HTTPS only, once the certificate exists (#64): port 80 answers every name the edge serves with a redirect to HTTPS
// on the same name, at the household's port, and proxies nothing; any other name gets 421. Each name it serves has
// HSTS for a year, without includeSubDomains or preload, and every cookie it passes on is Secure.
func TestPort80OnlyRedirectsAndEveryNameHasHSTSAndSecureCookies(t *testing.T) {
	testenv.Isolate(t)
	type handler struct {
		Handler    string              `json:"handler"`
		StatusCode any                 `json:"status_code"`
		Headers    map[string][]string `json:"headers"`
		Response   *struct {
			Set     map[string][]string `json:"set"`
			Replace map[string][]struct {
				SearchRegexp string `json:"search_regexp"`
				Replace      string `json:"replace"`
			} `json:"replace"`
			Deferred bool `json:"deferred"`
		} `json:"response"`
	}
	type server struct {
		Listen []string
		Routes []struct {
			Match  []map[string][]string
			Handle []handler
		}
		TLS []any `json:"tls_connection_policies"`
	}
	for port, location := range map[int]string{443: "https://admin.example.test{http.request.uri}", 9443: "https://admin.example.test:9443{http.request.uri}"} {
		c := thePrimary()
		c.Port = port
		b, err := edge.Render(c)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Apps struct {
				HTTP struct{ Servers map[string]server }
			}
		}
		if err := json.Unmarshal(b, &cfg); err != nil {
			t.Fatal(err)
		}
		plain, ok := cfg.Apps.HTTP.Servers["redirect"]
		if !ok || len(plain.Listen) != 1 || plain.Listen[0] != "100.88.7.186:80" || plain.TLS != nil {
			t.Fatalf("port %d: the plain server is %+v, want one on the mesh address's port 80, without TLS", port, plain)
		}
		for _, r := range plain.Routes {
			for _, h := range r.Handle {
				if h.Handler != "static_response" {
					t.Errorf("port %d: port 80 has a %s handler: it only redirects", port, h.Handler)
				}
			}
		}
		first := plain.Routes[0]
		if hosts := first.Match[0]["host"]; len(hosts) != 1 || hosts[0] != "admin.example.test" || strings.Join(first.Match[0]["path"], " ") != "/*" ||
			first.Handle[0].StatusCode != float64(308) || strings.Join(first.Handle[0].Headers["Location"], "") != location {
			t.Errorf("port %d: port 80's first route is %+v, want Admin's name redirected to %s", port, first, location)
		}
		if last := plain.Routes[len(plain.Routes)-1]; len(last.Match) != 0 || last.Handle[0].StatusCode != float64(421) {
			t.Errorf("port %d: port 80's last route is %+v, want a catch-all 421", port, last)
		}

		admin := cfg.Apps.HTTP.Servers["mesh"].Routes[0]
		h := admin.Handle[0]
		if h.Handler != "headers" || h.Response == nil || !h.Response.Deferred {
			t.Fatalf("port %d: Admin's route starts with %+v, want the headers set as the answer is written", port, h)
		}
		if sts := h.Response.Set["Strict-Transport-Security"]; len(sts) != 1 || sts[0] != "max-age=31536000" {
			t.Errorf("port %d: HSTS is %v, want max-age=31536000 only", port, sts)
		}
		if len(h.Response.Replace["Set-Cookie"]) == 0 {
			t.Errorf("port %d: Admin's cookies aren't made Secure", port)
		}
		if catchAll := cfg.Apps.HTTP.Servers["mesh"].Routes[len(cfg.Apps.HTTP.Servers["mesh"].Routes)-1]; len(catchAll.Handle) != 1 {
			t.Errorf("port %d: the catch-all has %+v, want its 421 alone, no HSTS for names that aren't HomersLab's here", port, catchAll.Handle)
		}
	}
}

func fmt_(m []map[string]any) string {
	b, _ := json.Marshal(m)
	return strings.NewReplacer(`"`, "").Replace(string(b))
}

func withTheSignInListener() edge.Config {
	c := withAGuardedApp()
	c.SignInListen = netip.MustParseAddr("172.31.255.241")
	return c
}

// The sign-in listener (ADR 0020, #19): apps reach the sign-in at an address of its own, on the household's port, and
// it carries the sign-in's route only. A handshake for any other name finds no certificate; another Host gets 421.
func TestTheSignInListenerCarriesTheSignInsRouteOnly(t *testing.T) {
	testenv.Isolate(t)
	withEverySite := withTheSignInListener()
	withEverySite.Home = withHome().Home // Home is people's, on the mesh; never an app's way in (#21)
	b, err := edge.Render(withEverySite)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Listen   []string `json:"listen"`
					Policies []struct {
						Match struct {
							SNI []string `json:"sni"`
						} `json:"match"`
					} `json:"tls_connection_policies"`
					Routes []map[string]any `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	s, ok := cfg.Apps.HTTP.Servers["sign-in"]
	if !ok || !reflect.DeepEqual(s.Listen, []string{"172.31.255.241:443"}) || len(s.Policies) != 1 ||
		!reflect.DeepEqual(s.Policies[0].Match.SNI, []string{"auth.example.test"}) || len(s.Routes) != 2 {
		t.Fatalf("the sign-in listener: %+v", s)
	}
	route, _ := json.Marshal(s.Routes)
	for _, want := range []string{`"host":["auth.example.test"]`, `"dial":"172.31.250.10:9000"`, `"/if/flow/initial-setup"`, `"status_code":421`} {
		if !strings.Contains(string(route), want) {
			t.Errorf("the sign-in listener's routes lack %s: %s", want, route)
		}
	}
	for _, bad := range []string{"admin.example.test", "pdf.example.test", "home.example.test", "unix/"} {
		if strings.Contains(string(route), bad) {
			t.Errorf("the sign-in listener carries %s: %s", bad, route)
		}
	}
	if !slices.Contains(cfg.Apps.HTTP.Servers["mesh"].Listen, "100.88.7.186:443") || slices.Contains(cfg.Apps.HTTP.Servers["mesh"].Listen, "172.31.255.241:443") {
		t.Errorf("the mesh listener: %+v", cfg.Apps.HTTP.Servers["mesh"].Listen)
	}
	// Without the sign-in on this node, a mesh address, or loopback: no listener.
	for name, c := range map[string]edge.Config{
		"no sign-in":     func() edge.Config { c := withTheSignInListener(); c.SignIn = nil; return c }(),
		"a mesh address": func() edge.Config { c := withTheSignInListener(); c.SignInListen = c.Listen[0]; return c }(),
		"no certificate for its name": func() edge.Config {
			c := withTheSignInListener()
			c.SignIn = &edge.Site{Name: "auth.home.example.test", Dial: c.SignIn.Dial}
			return c
		}(),
		"loopback": func() edge.Config {
			c := withTheSignInListener()
			c.SignInListen = netip.MustParseAddr("127.0.0.1")
			return c
		}(),
	} {
		if _, err := edge.Render(c); err == nil {
			t.Errorf("%s: rendered", name)
		}
	}
}

// The edge's rule for cookies, as Caddy runs it on each Set-Cookie value: Secure, once, whatever the upstream sent.
func TestEveryCookieThroughTheEdgeIsSecure(t *testing.T) {
	testenv.Isolate(t)
	for in, want := range map[string]string{
		"a=1":                        "a=1; Secure",
		"a=1; Path=/; HttpOnly":      "a=1; Path=/; HttpOnly; Secure",
		"a=1; Secure":                "a=1; Secure",
		"a=1; secure; Path=/":        "a=1; Path=/; Secure",
		"a=1;Secure;HttpOnly":        "a=1;HttpOnly; Secure",
		"a=1; SECURE ; SameSite=Lax": "a=1; SameSite=Lax; Secure",
		"secure=1; Path=/":           "secure=1; Path=/; Secure",
	} {
		if got := edge.SecureCookie(in); got != want {
			t.Errorf("%q through the edge: %q, want %q", in, got, want)
		}
	}
}
