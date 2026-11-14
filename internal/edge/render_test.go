package edge_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
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
		"primary":        thePrimary(),
		"with-sign-in":   withSignIn(),
		"guarded-app":    withAGuardedApp(),
		"cloud-port-two": twoAddresses,
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
	if len(cfg.Apps.HTTP.Servers) != 1 {
		t.Fatalf("servers: %v, want the mesh listener only", cfg.Apps.HTTP.Servers)
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
		if len(r.Match) == 1 && strings.Join(r.Match[0]["host"], ",") == "auth.example.test" && len(r.Handle) == 1 && r.Handle[0].Handler == "subroute" {
			subs := r.Handle[0].Routes
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
		for _, sub := range r.Handle[0].Routes {
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

func fmt_(m []map[string]any) string {
	b, _ := json.Marshal(m)
	return strings.NewReplacer(`"`, "").Replace(string(b))
}
