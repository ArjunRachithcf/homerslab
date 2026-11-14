package edge_test

// The edge's request matrix: who asks what, through which listener, and which answer they get, against a real Caddy
// in containers (testdata/compose.yaml, compose project hltest-16-edge). It needs Docker: it runs when
// HOMERSLAB_TEST_DOCKER_HOST names the engine to use, and is skipped otherwise. The code under test never sees that
// engine (testenv points DOCKER_HOST at nowhere); only this harness does, explicitly.
//
// The harness copies this test binary into the world's machines and runs it there in a helper's role (TestMain): the
// agent's side of the admin socket, Admin's stub, a browser.

import (
	"archive/tar"
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/edge"
	"github.com/ArjunRachithcf/homerslab/internal/firewall"
	"github.com/ArjunRachithcf/homerslab/internal/home"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// project is the compose project; HOMERSLAB_TEST_EDGE_PROJECT names another (hltest-<ticket>) for a ticket's local
// run.
var project = cmp.Or(os.Getenv("HOMERSLAB_TEST_EDGE_PROJECT"), "hltest-16-edge")

// The world (testdata/compose.yaml): the edge on the mesh and the LAN, the edge's user and group, Admin's stub.
const (
	edgeMesh   = "10.216.1.10"
	edgeLAN    = "10.216.2.10"
	edgeUID    = "4100"
	otherUID   = "4242"
	adminSock  = "/run/homerslab-admin/admin.sock"
	adminFile  = "/run/homerslab-edge/admin.sock"
	credDir    = "/run/credentials/homerslab-edge.service"
	helperBin  = "/hl.test"
	signInStub = "10.216.3.10:9000"
	appStub    = "10.216.4.10:8080"
	edgeApps   = "10.216.4.20"
	listener   = "10.216.9.1" // the sign-in listener's address, on the edge machine's loopback
	helperMode = "HLTEST16_HELPER"
	// Home (#21): its user, its socket, and the manager's control socket and database it must never reach.
	homeUID     = "4300"
	homeSock    = "/run/homerslab-home/home.sock"
	controlSock = "/run/homerslab/manager.sock"
	managerDB   = "/var/lib/homerslab/manager.db"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperMode); mode != "" {
		os.Exit(helper(mode, os.Args[len(os.Args)-1]))
	}
	os.Exit(m.Run())
}

// helper is this binary's role inside the world. arg is the role's one argument, JSON.
func helper(mode, arg string) int {
	ctx := context.Background()
	switch mode {
	case "admin": // the manager's Admin, on its socket for the edge's group
		_ = os.Remove(adminSock)
		ln, err := net.Listen("unix", adminSock)
		if err != nil {
			fmt.Println(err)
			return 1
		}
		_ = os.Chmod(adminSock, 0o660)
		_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// What an upstream may send that the edge must not pass on as it is: cookies without Secure, and an HSTS
			// of its own that would cover the household's other names.
			w.Header().Add("Set-Cookie", "plain=1; Path=/; HttpOnly")
			w.Header().Add("Set-Cookie", "upper=2; SECURE; Path=/")
			w.Header().Add("Set-Cookie", "last=3; Path=/; Secure")
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
			fmt.Fprintf(w, "Admin, at %s", r.Host)
		}))
	case "manager": // the manager's control socket and database, for its own user only, as its unit keeps them
		_ = os.Remove(controlSock)
		ln, err := net.Listen("unix", controlSock)
		if err != nil {
			fmt.Println(err)
			return 1
		}
		_ = os.Chmod(controlSock, 0o600)
		if err := os.WriteFile(managerDB, []byte("SQLite format 3\x00"), 0o600); err != nil {
			fmt.Println(err)
			return 1
		}
		_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "the control socket") }))
	case "home": // Home itself, on its socket for the edge's group, reading its projection
		dir, _ := os.MkdirTemp("", "home")
		p := filepath.Join(dir, "home.json")
		if err := home.Write(p, home.Projection{Household: "The Ferreira household", Home: "https://home.example.test",
			SignIn: &home.SignIn{Issuer: "https://auth.example.test/application/o/homerslab-home/", ClientID: "home", Upstream: "http://" + signInStub},
			Apps:   []home.App{{ID: "bentopdf", Name: "BentoPDF", URL: "https://pdf.example.test", Audience: []string{"admin", "adult"}, Health: "healthy"}}}); err != nil {
			fmt.Println(err)
			return 1
		}
		_ = os.Remove(homeSock)
		ln, err := net.Listen("unix", homeSock)
		if err != nil {
			fmt.Println(err)
			return 1
		}
		_ = os.Chmod(homeSock, 0o660)
		h := home.New(home.Config{Projection: p, ClientSecret: filepath.Join(dir, "none"), Web: fstest.MapFS{"home.html": {Data: []byte("Home")}}})
		_ = http.Serve(ln, h.Handler())
	case "reach": // what this process's user can open of the manager's: Admin's socket, the control socket, the database
		for _, sock := range []string{adminSock, controlSock} {
			if c, err := net.Dial("unix", sock); err != nil {
				fmt.Printf("%s: refused (%v)\n", sock, errors.Unwrap(errors.Unwrap(err)))
			} else {
				c.Close()
				fmt.Printf("%s: opened\n", sock)
			}
		}
		if f, err := os.Open(managerDB); err != nil {
			fmt.Printf("%s: refused (%v)\n", managerDB, errors.Unwrap(err))
		} else {
			f.Close()
			fmt.Printf("%s: opened\n", managerDB)
		}
	case "authentik": // the sign-in's server, on the sign-in network: it says what reached it
		_ = http.ListenAndServe(signInStub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/outpost.goauthentik.io/auth/caddy" { // its embedded outpost, as the guard
				guard(w, r)
				return
			}
			fmt.Fprintf(w, "Authentik, at %s %s", r.Host, r.URL.RequestURI())
		}))
	case "app": // a guarded app: it says who the request names, and which cookies reached it
		_ = http.ListenAndServe(appStub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/cookies" {
				http.SetCookie(w, &http.Cookie{Name: "wide", Value: "1", Domain: "example.test", Path: "/"})
				http.SetCookie(w, &http.Cookie{Name: "WIDE2", Value: "1", Domain: ".EXAMPLE.test", Path: "/"})
				http.SetCookie(w, &http.Cookie{Name: "narrow", Value: "1", Path: "/"})
				w.Header().Add("Set-Cookie", "top=1; Path=/; Domain=test")             // above the household's domain
				w.Header().Add("Set-Cookie", "dotted=1; Path=/; Domain=example.test.") // with a dot at its end
				w.Header().Add("Set-Cookie", "own=1; Path=/; Domain=pdf.example.test") // the app's own name
			}
			var named []string
			for k, v := range r.Header {
				if k != "Cookie" && k != "Accept-Encoding" && k != "User-Agent" && !strings.HasPrefix(k, "X-Forwarded-") && k != "Via" {
					named = append(named, k+"="+strings.Join(v, ","))
				}
			}
			slices.Sort(named)
			fmt.Fprintf(w, "App, at %s, as [%s], cookies [%s]", r.Host, strings.Join(named, " "), r.Header.Get("Cookie"))
		}))
	case "load", "config": // the agent, on the edge's admin socket
		c := edge.NewClient(adminFile)
		if mode == "load" {
			b, _ := io.ReadAll(os.Stdin)
			if err := c.Load(ctx, b); err != nil {
				fmt.Println(err)
				return 1
			}
			return 0
		}
		b, err := c.Config(ctx)
		if err != nil {
			fmt.Println(err)
			return 1
		}
		_, _ = os.Stdout.Write(b)
	case "keys": // any file under the edge's directories that holds a private key
		for _, dir := range []string{"/var/lib/homerslab-edge", "/run/homerslab-edge"} {
			_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					fmt.Println("unreadable:", p, err)
					return nil
				}
				if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte("PRIVATE KEY")) {
					fmt.Println("holds a key:", p)
				}
				return nil
			})
		}
	case "request": // a browser
		var q request
		_ = json.Unmarshal([]byte(arg), &q)
		fmt.Print(q.do(ctx))
	}
	return 0
}

// guard is the stub of Authentik's embedded outpost: a request with the guard's cookie for ines is let on, as ines; any
// other goes to the sign-in.
func guard(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("authentik_proxy_7f3a"); err == nil && c.Value == "ines-session" && r.Header.Get("X-Forwarded-Host") == "pdf.example.test" {
		w.Header().Set("X-Authentik-Username", "ines")
		w.Header().Set("X-Authentik-Groups", "homerslab-adults")
		// The session, refreshed (an expiry has a comma in it), and a second cookie: each reaches the browser whole.
		w.Header().Add("Set-Cookie", "authentik_proxy_7f3a=ines-session; Path=/; Expires=Wed, 21 Oct 2026 07:28:00 GMT; HttpOnly; Secure")
		w.Header().Add("Set-Cookie", "authentik_proxy_7f3a_csrf=c; Path=/; HttpOnly; Secure")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "https://auth.example.test/application/o/authorize/?client_id=homerslab-bentopdf", http.StatusFound)
}

// request is one request a browser makes: to an address, with a TLS server name and a Host, for a path ("/" when
// empty).
type request struct {
	Method          string // GET when empty; any other sends {} as JSON
	Addr, SNI, Host string
	Path            string // as sent, escapes and all; "/" when empty
	Plain           bool   // plain HTTP, no TLS
	CA              string
	// Headers are sent as they are, a cookie or a forged identity among them; the answer then also says where it
	// redirects and which cookies it sets.
	Headers map[string]string
	// Target is a plain HTTP request's target, written on the wire as it is (an absolute-form one, say): Go's client
	// never sends some of them.
	Target string
}

// shownHeaders are the answer's headers a row may look at, after its status and body.
var shownHeaders = []string{"Location", "Strict-Transport-Security", "Set-Cookie"}

func (q request) do(ctx context.Context) string {
	if q.Target != "" {
		return q.raw(ctx)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(q.CA))
	tr := &http.Transport{
		DialContext: func(ctx context.Context, n, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, n, q.Addr)
		},
		TLSClientConfig: &tls.Config{ServerName: q.SNI, RootCAs: roots},
	}
	scheme := "https"
	if q.Plain {
		scheme = "http"
	}
	req, _ := http.NewRequestWithContext(ctx, cmp.Or(q.Method, "GET"), scheme+"://"+q.Host+"/", nil)
	if q.Method != "" && q.Method != "GET" {
		req.Body, req.ContentLength = io.NopCloser(strings.NewReader("{}")), 2
		req.Header.Set("Content-Type", "application/json")
	}
	if q.Path != "" {
		req.URL.Opaque = "//" + q.Host + q.Path // sent exactly as written
	}
	req.Host = q.Host
	for k, v := range q.Headers {
		req.Header.Set(k, v)
	}
	// A redirect is an answer to look at, never followed.
	client := &http.Client{Transport: tr, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "error: " + err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
	out := fmt.Sprintf("%d %s", resp.StatusCode, b)
	for _, h := range shownHeaders {
		for _, v := range resp.Header.Values(h) {
			out += "\n" + h + ": " + v
		}
	}
	return out
}

func TestTheEdgesRequestMatrix(t *testing.T) {
	testenv.Isolate(t)
	engine := os.Getenv("HOMERSLAB_TEST_DOCKER_HOST")
	if engine == "" {
		t.Skip("set HOMERSLAB_TEST_DOCKER_HOST to the engine for the matrix's containers")
	}
	ca, chain, key := testCA(t)
	w := up(t, engine, chain, key)

	cfg := edge.Config{AdminSocket: adminFile, Storage: "/var/lib/homerslab-edge", Listen: []netip.Addr{netip.MustParseAddr(edgeMesh)},
		Port: 443, Certificates: []edge.Certificate{{Domain: "example.test", CertFile: credDir + "/tls-example.test.crt", KeyFile: credDir + "/tls-example.test.key"}},
		Admin: &edge.Site{Name: "admin.example.test", Upstream: adminSock}, SignIn: &edge.Site{Name: "auth.example.test", Dial: signInStub},
		Home: &edge.Site{Name: "home.example.test", Upstream: homeSock},
		Apps: []edge.App{{Name: "pdf.example.test", Dial: appStub, Guard: edge.AuthentikGuard(signInStub)},
			{Name: "recipes.example.test", Dial: appStub, SignsIn: true, Refused: []string{"/api/auth/token"}}},
		SignInListen: netip.MustParseAddr(listener)}
	want, err := edge.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ask := func(from string, q request) string {
		t.Helper()
		q.CA = ca
		b, _ := json.Marshal(q)
		return w.helper(t, from, "", "request", nil, string(b))
	}

	t.Run("claim (#81): Caddy answers a request no route matches with an empty 200", func(t *testing.T) {
		// Reported, not verified (spec: Testing Decisions): why the edge's last route answers 421.
		var c map[string]any
		_ = json.Unmarshal(want, &c)
		srv := c["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["mesh"].(map[string]any)
		routes := srv["routes"].([]any)
		srv["routes"] = routes[:len(routes)-1] // without the catch-all
		b, _ := json.Marshal(c)
		w.load(t, b)
		got := ask("phone", request{Addr: edgeMesh + ":443", SNI: "films.example.test", Host: "films.example.test"})
		t.Logf("a name no route matches, without the catch-all: %q", got)
		if got != "200 " {
			t.Errorf("the claim doesn't hold: %q", got)
		}
	})

	w.load(t, want)
	toMesh := request{Addr: edgeMesh + ":443", SNI: "admin.example.test", Host: "admin.example.test"}
	t.Run("control: without the firewall table, a LAN host routing the mesh's addresses through the edge reaches Admin", func(t *testing.T) {
		// So the row below is red without the table: the route is real.
		if got := ask("laptop-on-the-lan", toMesh); !strings.HasPrefix(got, "200 Admin") {
			t.Fatalf("the LAN host's route to the mesh address: %q", got)
		}
	})
	toApp := request{Addr: appStub, Host: "pdf.example.test", Plain: true, Headers: map[string]string{"X-Authentik-Username": "ines-the-admin", "Remote-User": "ines-the-admin"}}
	t.Run("control: without the firewall table, a local unprivileged process reaches the app's upstream, its forged identity with it", func(t *testing.T) {
		// So the row below is red without the table: the app would believe the header.
		if got := w.helper(t, "edge", otherUID+":"+otherUID, "request", nil, toJSON(toApp)); !strings.Contains(got, "X-Authentik-Username=ines-the-admin") {
			t.Fatalf("a local process, straight to the app: %q", got)
		}
	})
	w.firewall(t)
	t.Run("a local unprivileged process connects to the app's upstream with a forged identity header: refused before the app sees anything (#82)", func(t *testing.T) {
		if got := w.helper(t, "edge", otherUID+":"+otherUID, "request", nil, toJSON(toApp)); !strings.HasPrefix(got, "error: ") || strings.Contains(got, "App,") {
			t.Errorf("a local process, straight to the app: %q", got)
		}
	})
	t.Run("the agent's configuration is what the edge runs, and holds no key", func(t *testing.T) {
		got := w.helper(t, "edge", "0:"+edgeUID, "config", nil, "")
		if !edge.Same([]byte(got), want) {
			t.Errorf("GET /config/ = %s", got)
		}
		for _, bad := range []string{"PRIVATE KEY", "load_pem", "BEGIN"} {
			if strings.Contains(got, bad) {
				t.Errorf("GET /config/ shows %q", bad)
			}
		}
	})

	for _, row := range []struct {
		name     string
		from     string
		q        request
		want     string   // the answer's start
		wants    []string // lines the answer has too
		notWants string
	}{
		{name: "Admin's name, from the mesh: Admin, with a valid certificate", from: "phone",
			q: request{Addr: edgeMesh + ":443", SNI: "admin.example.test", Host: "admin.example.test"}, want: "200 Admin, at admin.example.test"},
		{name: "a name that isn't on this node, from the mesh: 421", from: "phone",
			q: request{Addr: edgeMesh + ":443", SNI: "photos.example.test", Host: "photos.example.test"}, want: "421 "},
		{name: "Admin's connection, reused for a name that isn't on this node: 421", from: "phone",
			q: request{Addr: edgeMesh + ":443", SNI: "admin.example.test", Host: "photos.example.test"}, want: "421 "},
		{name: "another name's connection, reused for Admin on this node: Admin (coalescing on the mesh)", from: "phone",
			q: request{Addr: edgeMesh + ":443", SNI: "photos.example.test", Host: "admin.example.test"}, want: "200 Admin"},
		{name: "a name outside the household's domains: no certificate, no answer", from: "phone",
			q: request{Addr: edgeMesh + ":443", SNI: "admin.other.test", Host: "admin.other.test"}, want: "error: ", notWants: "Admin"},
		{name: "plain HTTP on the household's port: never Admin", from: "phone",
			q: request{Addr: edgeMesh + ":443", Host: "admin.example.test", Plain: true}, want: "400 ", notWants: "Admin"},

		// HTTPS only (#64): port 80 redirects to HTTPS on the same name, and proxies nothing.
		{name: "Admin's name over plain HTTP on port 80: a redirect to HTTPS on the same name and path, never Admin", from: "phone",
			q:    request{Addr: edgeMesh + ":80", Host: "admin.example.test", Path: "/apps/photos?tab=people", Plain: true},
			want: "308 ", wants: []string{"\nLocation: https://admin.example.test/apps/photos?tab=people"}, notWants: "Admin,"},
		{name: "Admin's name over plain HTTP on port 80, with its port: the same redirect", from: "phone",
			q:    request{Addr: edgeMesh + ":80", Host: "admin.example.test:80", Plain: true},
			want: "308 ", wants: []string{"\nLocation: https://admin.example.test/"}, notWants: "Admin,"},
		{name: "a name that isn't on this node, over port 80: 421, no redirect", from: "phone",
			q: request{Addr: edgeMesh + ":80", Host: "photos.example.test", Plain: true}, want: "421 ", notWants: "Location"},
		{name: "a name outside the household's domains, over port 80: 421, never a redirect to it", from: "phone",
			q: request{Addr: edgeMesh + ":80", Host: "evil.other.test", Plain: true}, want: "421 ", notWants: "Location"},
		{name: "an origin-form target on the wire, over port 80: the same-name redirect", from: "phone",
			q:    request{Addr: edgeMesh + ":80", Host: "admin.example.test", Target: "/apps?x=1", Plain: true},
			want: "308 ", wants: []string{"\nLocation: https://admin.example.test/apps?x=1"}},
		{name: "an opaque absolute-form target, http:@evil, over port 80: 421, never a Location at another host", from: "phone",
			q: request{Addr: edgeMesh + ":80", Host: "admin.example.test", Target: "http:@evil.test", Plain: true}, want: "421 ", notWants: "Location"},
		{name: "an opaque absolute-form target, http:evil/x, over port 80: 421, never a Location at another host", from: "phone",
			q: request{Addr: edgeMesh + ":80", Host: "admin.example.test", Target: "http:evil.test/x", Plain: true}, want: "421 ", notWants: "Location"},
		{name: "a redirect sets no HSTS: browsers ignore it over HTTP", from: "phone",
			q: request{Addr: edgeMesh + ":80", Host: "admin.example.test", Plain: true}, want: "308 ", notWants: "Strict-Transport-Security"},
		{name: "port 80 from the LAN, at the edge's LAN address: nothing listens", from: "laptop-on-the-lan",
			q: request{Addr: edgeLAN + ":80", Host: "admin.example.test", Plain: true}, want: "error: ", notWants: "Location"},
		{name: "port 80 from the LAN, at the mesh address routed through the edge's machine: the firewall table drops it", from: "laptop-on-the-lan",
			q: request{Addr: edgeMesh + ":80", Host: "admin.example.test", Plain: true}, want: "error: ", notWants: "Location"},

		// HSTS on every HomersLab name, a year, without includeSubDomains or preload: the household's domain may carry
		// sites that aren't HomersLab's. Admin's own HSTS is replaced, not passed on.
		{name: "Admin over HTTPS: HSTS for a year, for its name only", from: "phone",
			q: request{Addr: edgeMesh + ":443", SNI: "admin.example.test", Host: "admin.example.test"}, want: "200 Admin",
			wants: []string{"\nStrict-Transport-Security: max-age=31536000\n"}, notWants: "includeSubDomains"},
		{name: "a name that isn't on this node: no HSTS for it", from: "phone",
			q: request{Addr: edgeMesh + ":443", SNI: "photos.example.test", Host: "photos.example.test"}, want: "421 ", notWants: "Strict-Transport-Security"},

		// Every cookie through the edge is Secure (#64), whatever the upstream sent.
		{name: "Admin's cookies through the edge: each Secure, once", from: "phone",
			q: request{Addr: edgeMesh + ":443", SNI: "admin.example.test", Host: "admin.example.test"}, want: "200 Admin",
			wants: []string{"\nSet-Cookie: plain=1; Path=/; HttpOnly; Secure", "\nSet-Cookie: upper=2; Path=/; Secure", "\nSet-Cookie: last=3; Path=/; Secure"}},
		{name: "Admin's name, from the LAN, at the edge's LAN address: nothing listens", from: "laptop-on-the-lan",
			q: request{Addr: edgeLAN + ":443", SNI: "admin.example.test", Host: "admin.example.test"}, want: "error: ", notWants: "Admin"},
		{name: "Admin's name, from the LAN, at the mesh address routed through the edge's machine: the firewall table drops it",
			from: "laptop-on-the-lan", q: toMesh, want: "error: ", notWants: "Admin"},
		{name: "the sign-in, from the mesh: the provider, at its own name", from: "phone",
			q:    request{Addr: edgeMesh + ":443", SNI: "auth.example.test", Host: "auth.example.test", Path: "/if/flow/homerslab-sign-in/"},
			want: "200 Authentik, at auth.example.test /if/flow/homerslab-sign-in/"},
		{name: "the sign-in, from the LAN at the mesh address: dropped", from: "laptop-on-the-lan",
			q: request{Addr: edgeMesh + ":443", SNI: "auth.example.test", Host: "auth.example.test"}, want: "error: ", notWants: "Authentik"},
	} {
		t.Run(row.name, func(t *testing.T) {
			got := ask(row.from, row.q) + "\n"
			if !strings.HasPrefix(got, row.want) || (row.notWants != "" && strings.Contains(got, row.notWants)) {
				t.Errorf("%s asks %+v: %q, want %q…", row.from, row.q.Host, got, row.want)
			}
			for _, want := range row.wants {
				if !strings.Contains(got, want) {
					t.Errorf("%s asks %+v: %q, want it to have %q", row.from, row.q.Host, got, want)
				}
			}
			if strings.Contains(got, "\nSet-Cookie: ") {
				for _, line := range strings.Split(got, "\n") {
					if c, ok := strings.CutPrefix(line, "Set-Cookie: "); ok && !secure(c) {
						t.Errorf("a cookie through the edge without Secure: %q", c)
					}
				}
			}
		})
	}

	// A guarded app (#19): the guard is asked first, the client's identity never reaches the app, nor the guard's cookie,
	// nor a cookie the app sets for the whole household domain (#83).
	signedIn := map[string]string{"Cookie": "authentik_proxy_7f3a=ines-session; theme=dark",
		"X-Authentik-Username": "ines-the-admin", "Remote-User": "ines-the-admin", "X-Forwarded-User": "ines-the-admin"}
	app := func(path string, headers map[string]string) request {
		return request{Addr: edgeMesh + ":443", SNI: "pdf.example.test", Host: "pdf.example.test", Path: path, Headers: headers}
	}
	for _, row := range []struct {
		name            string
		q               request
		wants, notWants []string
	}{
		{name: "without the guard's cookie: a redirect to the sign-in, never the app", q: app("/", map[string]string{"X-Authentik-Username": "ines-the-admin"}),
			wants: []string{"302 ", "Location: https://auth.example.test/application/o/authorize/"}, notWants: []string{"App,"}},
		{name: "signed in, with a forged identity: the app sees only the guard's person", q: app("/", signedIn),
			wants: []string{"200 App, at pdf.example.test, as [X-Authentik-Groups=homerslab-adults X-Authentik-Username=ines]"}, notWants: []string{"ines-the-admin", "Remote-User"}},
		{name: "signed in: the app gets the household's cookies but never the guard's", q: app("/", signedIn),
			wants: []string{"cookies [theme=dark]\n"}, notWants: []string{"cookies [authentik_proxy", "dark; authentik_proxy"}},
		{name: "a cookie the app sets for the household's domain never reaches the browser; its own name's does", q: app("/cookies", signedIn),
			wants: []string{"narrow=1", "own=1"}, notWants: []string{"wide=1", "WIDE2=1", "top=1", "dotted=1"}},
		{name: "the guard's refreshed cookies reach the browser, each whole", q: app("/", signedIn),
			wants: []string{"\nSet-Cookie: authentik_proxy_7f3a=ines-session; Path=/; Expires=Wed, 21 Oct 2026 07:28:00 GMT; HttpOnly; Secure\n",
				"\nSet-Cookie: authentik_proxy_7f3a_csrf=c; Path=/; HttpOnly; Secure"}},
		{name: "the provider's own paths go to the provider, never the app", q: app("/outpost.goauthentik.io/callback?X-authentik-auth-callback=true", signedIn),
			wants: []string{"200 Authentik, at pdf.example.test /outpost.goauthentik.io/callback"}, notWants: []string{"App,"}},
	} {
		t.Run("a guarded app, "+row.name, func(t *testing.T) {
			got := ask("phone", row.q)
			for _, want := range row.wants {
				if !strings.Contains(got, want) {
					t.Errorf("%q, want %q", got, want)
				}
			}
			for _, bad := range row.notWants {
				if strings.Contains(got, bad) {
					t.Errorf("%q has %q", got, bad)
				}
			}
		})
	}

	// The sign-in listener (ADR 0020, #19): an app reaches the sign-in there, with SNI and Host both the sign-in's name;
	// nothing else is served on it, and nothing else of the edge's machine is open to the app.
	toListener := func(sni, host, path string) request {
		return request{Addr: listener + ":443", SNI: sni, Host: host, Path: path}
	}
	for _, row := range []struct {
		name, from string
		q          request
		want       string
		notWants   string
	}{
		{name: "an app reaches the sign-in through it: served", from: "app",
			q: toListener("auth.example.test", "auth.example.test", "/application/o/token/"), want: "200 Authentik, at auth.example.test /application/o/token/"},
		{name: "the sign-in's name, with the port in Host: served", from: "app",
			q: toListener("auth.example.test", "auth.example.test:443", "/"), want: "200 Authentik"},
		{name: "SNI the sign-in's, Host Admin's: refused", from: "app",
			q: toListener("auth.example.test", "admin.example.test", "/"), want: "421 ", notWants: "Admin"},
		{name: "SNI the sign-in's, Host another app's: refused", from: "app",
			q: toListener("auth.example.test", "pdf.example.test", "/"), want: "421 ", notWants: "App,"},
		{name: "SNI the sign-in's, Host Home's: refused", from: "app",
			q: toListener("auth.example.test", "home.example.test", "/"), want: "421 ", notWants: "auth/sign-in"},
		{name: "SNI Home's: no certificate, nothing served", from: "app",
			q: toListener("home.example.test", "home.example.test", "/"), want: "error: ", notWants: "auth/sign-in"},
		{name: "SNI Admin's: no certificate, nothing served", from: "app",
			q: toListener("admin.example.test", "admin.example.test", "/"), want: "error: ", notWants: "Admin"},
		{name: "SNI another app's: no certificate, nothing served", from: "app",
			q: toListener("pdf.example.test", "pdf.example.test", "/"), want: "error: ", notWants: "App,"},
		{name: "the first-admin flow: 403, never the provider", from: "app",
			q: toListener("auth.example.test", "auth.example.test", "/if/flow/initial-setup/"), want: "403 ", notWants: "Authentik"},
		{name: "the edge machine's mesh listener, from an app: dropped", from: "app",
			q: request{Addr: edgeApps + ":443", SNI: "auth.example.test", Host: "auth.example.test"}, want: "error: ", notWants: "Authentik"},
	} {
		t.Run("the sign-in listener, "+row.name, func(t *testing.T) {
			got := ask(row.from, row.q)
			if !strings.HasPrefix(got, row.want) || (row.notWants != "" && strings.Contains(got, row.notWants)) {
				t.Errorf("%s asks %s with SNI %s, Host %s: %q, want %q…", row.from, row.q.Addr, row.q.SNI, row.q.Host, got, row.want)
			}
		})
	}

	// Home (#21): served at its name over the mesh, from its own process. Every app is same-site with it, so a change
	// from an app's page is refused (#83); Home's own page's isn't. Its user opens nothing of the manager's.
	toHome := func(method, path string, headers map[string]string) request {
		return request{Method: method, Addr: edgeMesh + ":443", SNI: "home.example.test", Host: "home.example.test", Path: path, Headers: headers}
	}
	for _, row := range []struct {
		name            string
		q               request
		wants, notWants []string
	}{
		{name: "Home, from the mesh, signed out: to the sign-in", q: toHome("GET", "/", map[string]string{}),
			wants: []string{"303 ", "Location: /auth/sign-in"}},
		{name: "a cross-app POST to Home, from an app's page with Home's session cookie: refused (#83)",
			q: toHome("POST", "/auth/sign-out", map[string]string{"Origin": "https://pdf.example.test", "Sec-Fetch-Site": "same-site",
				"Cookie": "__Host-homerslab-home=a-session"}),
			wants: []string{"403 ", "cross-origin"}, notWants: []string{"Max-Age=0"}},
		{name: "a cross-app POST to Home that claims to be same-origin, with an app's Origin: refused (#83)",
			q: toHome("POST", "/auth/sign-out", map[string]string{"Origin": "https://pdf.example.test", "Sec-Fetch-Site": "same-origin",
				"Cookie": "__Host-homerslab-home=a-session"}),
			wants: []string{"403 ", "cross-origin"}},
		{name: "a POST from Home's own page: Home takes it", q: toHome("POST", "/auth/sign-out", map[string]string{"Origin": "https://home.example.test",
			"Sec-Fetch-Site": "same-origin", "Cookie": "__Host-homerslab-home=a-session"}),
			wants: []string{"204 ", "__Host-homerslab-home=; Path=/; Max-Age=0; HttpOnly; SameSite=Lax; Secure"}},
	} {
		t.Run("Home: "+row.name, func(t *testing.T) {
			got := ask("phone", row.q)
			for _, want := range row.wants {
				if !strings.Contains(got, want) {
					t.Errorf("%q, want %q", got, want)
				}
			}
			for _, bad := range row.notWants {
				if strings.Contains(got, bad) {
					t.Errorf("%q has %q", got, bad)
				}
			}
		})
	}
	t.Run("Home's user opens neither Admin's socket nor the control socket, nor reads the database (story 69)", func(t *testing.T) {
		got := w.helper(t, "home", homeUID+":"+homeUID, "reach", nil, "")
		for _, p := range []string{adminSock, controlSock, managerDB} {
			if !strings.Contains(got, p+": refused (permission denied)") {
				t.Errorf("Home's user, at %s:\n%s", p, got)
			}
		}
		// The control: the manager's own user opens them all.
		if got := w.helper(t, "admin", "4200:4100", "reach", nil, ""); strings.Count(got, ": opened") != 3 {
			t.Errorf("the manager's user:\n%s", got)
		}
	})

	// An app that signs people in itself (#20): no guard is asked, and the app answers; but the client's identity headers
	// never reach it all the same, nor a cookie it sets for the whole household domain (#83).
	native := func(path string, headers map[string]string) request {
		return request{Addr: edgeMesh + ":443", SNI: "recipes.example.test", Host: "recipes.example.test", Path: path, Headers: headers}
	}
	for _, row := range []struct {
		name            string
		q               request
		wants, notWants []string
	}{
		{name: "no guard: the app itself, with no identity a client named", q: native("/", signedIn),
			wants: []string{"200 App, at recipes.example.test, as []"}, notWants: []string{"ines-the-admin", "Remote-User", "302 "}},
		{name: "a cookie it sets for the household's domain never reaches the browser; its own does", q: native("/cookies", map[string]string{}),
			wants: []string{"narrow=1"}, notWants: []string{"wide=1", "WIDE2=1", "top=1", "dotted=1"}},
		{name: "its own password sign-in: 403, never the app", q: native("/api/auth/token", nil), wants: []string{"403 "}, notWants: []string{"App,"}},
		{name: "its own password sign-in, written otherwise: 403, never the app", q: native("/API/auth/%74oken/", nil), wants: []string{"403 "}, notWants: []string{"App,"}},
		{name: "beside its password sign-in: the app", q: native("/api/auth/tokens", nil), wants: []string{"200 App,"}},
	} {
		t.Run("an app with native sign-in, "+row.name, func(t *testing.T) {
			got := ask("phone", row.q)
			for _, want := range row.wants {
				if !strings.Contains(got, want) {
					t.Errorf("%q, want %q", got, want)
				}
			}
			for _, bad := range row.notWants {
				if strings.Contains(got, bad) {
					t.Errorf("%q has %q", got, bad)
				}
			}
		})
	}

	// Authentik's first-admin flow is refused on every listener before the provider sees it, its page and its executor
	// API alike, however the path is written (#84). Today the edge has one listener for people, the mesh one, and the
	// sign-in listener for apps (above); the public listener (#32) takes the same rows.
	for _, path := range []string{
		"/if/flow/initial-setup/", "/if/flow/initial-setup", "/if/flow/initial-setup/?next=/",
		"/api/v3/flows/executor/initial-setup/", "/api/v3/flows/executor/initial-setup/?query=", "/api/v3/flows/executor/initial-setup",
		"/api/v3/flows/instances/initial-setup/execute/", "/IF/FLOW/Initial-Setup/", "/if/flow/initial%2Dsetup/",
		"/api/v3/flows/executor/initial%2dsetup/", "/if/flow/initial-setup%2F",
	} {
		t.Run("the first-admin flow at "+path+": 403, never the provider", func(t *testing.T) {
			got := ask("phone", request{Addr: edgeMesh + ":443", SNI: "auth.example.test", Host: "auth.example.test", Path: path})
			if !strings.HasPrefix(got, "403 ") || strings.Contains(got, "Authentik") {
				t.Errorf("%s: %q", path, got)
			}
		})
	}

	t.Run("no file under the edge's directories holds a private key (#78)", func(t *testing.T) {
		if got := w.helper(t, "edge", edgeUID, "keys", nil, ""); strings.TrimSpace(got) != "" {
			t.Errorf("the edge's directories:\n%s", got)
		}
	})

	t.Run("the admin socket opens for the edge's group only (#78)", func(t *testing.T) {
		if _, err := w.helperErr("edge", otherUID+":"+otherUID, "config", nil, ""); err == nil {
			t.Error("another local user read the edge's configuration")
		}
		if _, err := w.helperErr("edge", "0:0", "config", nil, ""); err == nil {
			t.Error("root without the edge's group (and without CAP_DAC_OVERRIDE, as the agent's unit) read it: the group isn't what guards it")
		}
		if got := w.helper(t, "edge", "0:"+edgeUID, "config", nil, ""); !strings.Contains(got, "persist") {
			t.Errorf("the agent (root, in the edge's group): %q", got)
		}
	})

	t.Run("a restarted edge runs its boot configuration: autosave is off, and the agent pushes again", func(t *testing.T) {
		w.docker(t, nil, "restart", "-t", "2", container("edge"))
		var got string
		for range 50 {
			if out, err := w.helperErr("edge", "0:"+edgeUID, "config", nil, ""); err == nil {
				got = out
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		boot, _ := os.ReadFile(filepath.Join(w.dir, "..", "..", "..", "packaging", "edge", "boot.json"))
		if !edge.Same([]byte(got), boot) {
			t.Errorf("after a restart the edge runs %s, want its boot configuration", got)
		}
		// The restarted container has a network namespace of its own, without the listener's address the companion put
		// in the old one (the agent puts it back before it pushes): the push is without the listener.
		noListener := cfg
		noListener.SignInListen = netip.Addr{}
		again, err := edge.Render(noListener)
		if err != nil {
			t.Fatal(err)
		}
		w.load(t, again)
		if got := ask("phone", request{Addr: edgeMesh + ":443", SNI: "admin.example.test", Host: "admin.example.test"}); !strings.HasPrefix(got, "200 Admin") {
			t.Errorf("after the push again: %q", got)
		}
	})
}

// raw sends a plain HTTP request line as written, and reads the answer as do does.
func (q request) raw(ctx context.Context) string {
	c, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", q.Addr)
	if err != nil {
		return "error: " + err.Error()
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(8 * time.Second))
	fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", q.Target, q.Host)
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return "error: " + err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	out := fmt.Sprintf("%d %s", resp.StatusCode, b)
	for _, h := range shownHeaders {
		for _, v := range resp.Header.Values(h) {
			out += "\n" + h + ": " + v
		}
	}
	return out
}

// secure says whether a Set-Cookie value has the Secure attribute, once.
func secure(cookie string) bool {
	n := 0
	for _, a := range strings.Split(cookie, ";")[1:] {
		if strings.EqualFold(strings.TrimSpace(a), "secure") {
			n++
		}
	}
	return n == 1
}

// firewall applies the agent's table in the edge's machine: its mesh interface (the stand-in for wt0) and its LAN
// interface, found by their addresses.
func (w *world) firewall(t *testing.T) {
	t.Helper()
	out, err := w.cmd(nil, "exec", container("firewall"), "ip", "-o", "-4", "addr", "show").CombinedOutput()
	if err != nil {
		t.Fatalf("the edge's interfaces: %v %s", err, out)
	}
	var meshIf, lanIf, appsIf string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		name, _, _ := strings.Cut(f[1], "@")
		switch {
		case strings.HasPrefix(f[3], edgeMesh+"/"):
			meshIf = name
		case strings.HasPrefix(f[3], edgeLAN+"/"):
			lanIf = name
		case strings.HasPrefix(f[3], edgeApps+"/"):
			appsIf = name
		}
	}
	if meshIf == "" || lanIf == "" || appsIf == "" {
		t.Fatalf("the edge's mesh, LAN and apps' interfaces aren't there:\n%s", out)
	}
	// The apps' network stands in for an engine's bridge; the app's upstream and the sign-in's server are opened only by
	// the edge's user and root (the agent).
	edgeID, _ := strconv.Atoi(edgeUID)
	users := []uint32{uint32(edgeID), 0}
	table := firewall.Render(firewall.State{Mesh: []string{meshIf}, LAN: []string{lanIf},
		LANPrefixes: []netip.Prefix{netip.MustParsePrefix("10.216.2.0/24")}, Bridges: []string{appsIf}, Virtual: []string{appsIf},
		Upstreams:      []firewall.Upstream{{Addr: netip.MustParseAddrPort(appStub), UIDs: users}, {Addr: netip.MustParseAddrPort(signInStub), UIDs: users}},
		SignInListener: []netip.AddrPort{netip.MustParseAddrPort(listener + ":443")}})
	if out, err := w.cmd(strings.NewReader(table), "exec", "-i", container("firewall"), "nft", "-f", "-").CombinedOutput(); err != nil {
		t.Fatalf("applying the firewall table: %v %s\n%s", err, out, table)
	}
}

// testCA is a CA a test's browsers trust, and a wildcard certificate for *.example.test it issued, with its key.
func testCA(t *testing.T) (ca, chain, key string) {
	t.Helper()
	rk, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "HomersLab test CA"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	rd, err := x509.CreateCertificate(rand.Reader, root, root, &rk.PublicKey, rk)
	if err != nil {
		t.Fatal(err)
	}
	rootCert, _ := x509.ParseCertificate(rd)
	lk, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "*.example.test"}, DNSNames: []string{"*.example.test"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	ld, err := x509.CreateCertificate(rand.Reader, leaf, rootCert, &lk.PublicKey, rk)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalPKCS8PrivateKey(lk)
	pemOf := func(typ string, b []byte) string { return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: b})) }
	return pemOf("CERTIFICATE", rd), pemOf("CERTIFICATE", ld), pemOf("PRIVATE KEY", kd)
}

// world is the compose project.
type world struct {
	engine, dir string
}

func container(s string) string { return project + "-" + s + "-1" }

func up(t *testing.T, engine, chain, key string) *world {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	w := &world{engine: engine, dir: filepath.Join(filepath.Dir(self), "testdata")}
	w.compose(t, "down", "-v", "--remove-orphans")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := w.composeOut("logs", "--no-color", "--tail", "60")
			t.Logf("the containers' logs:\n%s", out)
		}
		w.compose(t, "down", "-v", "--remove-orphans")
	})
	w.compose(t, "create", "--quiet-pull", "--build")
	boot, err := os.ReadFile(filepath.Join(w.dir, "..", "..", "..", "packaging", "edge", "boot.json"))
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	// The edge's files as the packages and systemd leave them: the boot configuration, and the credentials that only
	// the edge's user reads.
	w.copy(t, "edge", []file{
		{path: "usr/share/homerslab/edge-boot.json", body: boot, mode: 0o644},
		{path: strings.TrimPrefix(credDir, "/") + "/tls-example.test.crt", body: []byte(chain), mode: 0o400, uid: 4100},
		{path: strings.TrimPrefix(credDir, "/") + "/tls-example.test.key", body: []byte(key), mode: 0o400, uid: 4100},
		{path: strings.TrimPrefix(helperBin, "/"), body: bin, mode: 0o755},
	})
	for _, m := range []string{"admin", "authentik", "app", "phone", "laptop-on-the-lan", "home"} {
		w.copy(t, m, []file{{path: strings.TrimPrefix(helperBin, "/"), body: bin, mode: 0o755}})
	}
	w.compose(t, "start")
	// The agent's part of the sign-in listener: its address on the loopback; and the apps' network as an engine's bridge,
	// whose veth is its one port, so the table treats it as one.
	w.docker(t, nil, "exec", container("firewall"), "sh", "-c", "ip address add "+listener+"/32 dev lo && ip link add br-apps type bridge && "+
		"dev=$(ip -o -4 address show | awk '$4 == \""+edgeApps+"/24\" {print $2}') && ip address del "+edgeApps+"/24 dev $dev && "+
		"ip link set $dev master br-apps && ip address add "+edgeApps+"/24 dev br-apps && ip link set br-apps up")
	w.docker(t, nil, "exec", "-u", "0", container("admin"), "sh", "-c", "chown 4200:4100 /run/homerslab-admin && chmod 0750 /run/homerslab-admin")
	w.docker(t, nil, "exec", "-d", "-u", "4200:4100", "-e", helperMode+"=admin", container("admin"), helperBin, "-")
	// The manager's folders, as its unit makes them: its own, 0700; Home's socket's folder, for Home and the edge (setgid:
	// its socket is the edge's group's, as SocketGroup makes it).
	w.docker(t, nil, "exec", "-u", "0", container("admin"), "sh", "-c",
		"chown 4200:4200 /run/homerslab /var/lib/homerslab && chmod 0700 /run/homerslab /var/lib/homerslab")
	w.docker(t, nil, "exec", "-u", "0", container("home"), "sh", "-c", "chown 4300:4100 /run/homerslab-home && chmod 2750 /run/homerslab-home")
	w.docker(t, nil, "exec", "-d", "-u", "4200:4200", "-e", helperMode+"=manager", container("admin"), helperBin, "-")
	w.docker(t, nil, "exec", "-d", "-u", homeUID+":"+homeUID, "-e", helperMode+"=home", container("home"), helperBin, "-")
	w.docker(t, nil, "exec", "-d", "-e", helperMode+"=authentik", container("authentik"), helperBin, "-")
	w.docker(t, nil, "exec", "-d", "-e", helperMode+"=app", container("app"), helperBin, "-")
	for range 100 {
		if _, err := w.helperErr("edge", "0:"+edgeUID, "config", nil, ""); err == nil {
			return w
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the edge's admin API never answered")
	return nil
}

type file struct {
	path string
	body []byte
	mode int64
	uid  int
}

// copy puts files into a machine with docker cp, from a tar stream: their owners and modes as given.
func (w *world) copy(t *testing.T, machine string, files []file) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	dirs := map[string]bool{}
	for _, f := range files {
		for d := filepath.Dir(f.path); d != "."; d = filepath.Dir(d) {
			dirs[d] = true
		}
	}
	sorted := slices.Sorted(maps.Keys(dirs))
	for _, d := range sorted {
		mode, uid := int64(0o755), 0
		if strings.HasPrefix(credDir, "/"+d) && d == strings.TrimPrefix(credDir, "/") {
			mode, uid = 0o500, 4100
		}
		_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: d + "/", Mode: mode, Uid: uid, Gid: uid})
	}
	for _, f := range files {
		_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: f.path, Mode: f.mode, Size: int64(len(f.body)), Uid: f.uid, Gid: f.uid})
		_, _ = tw.Write(f.body)
	}
	_ = tw.Close()
	cmd := w.cmd(&buf, "cp", "-a", "-", container(machine)+":/")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("copying into %s: %v %s", machine, err, out)
	}
}

// load is the agent pushing a configuration into the edge.
func (w *world) load(t *testing.T, cfg []byte) {
	t.Helper()
	if out, err := w.helperErr("edge", "0:"+edgeUID, "load", cfg, ""); err != nil {
		t.Fatalf("loading the edge: %v %s", err, out)
	}
}

func (w *world) helperErr(machine, user, mode string, stdin []byte, arg string) (string, error) {
	args := []string{"exec", "-i", "-e", helperMode + "=" + mode}
	if user != "" {
		args = append(args, "-u", user)
	}
	args = append(args, container(machine), helperBin, arg)
	if arg == "" {
		args[len(args)-1] = "-"
	}
	var in io.Reader
	if stdin != nil {
		in = bytes.NewReader(stdin)
	}
	cmd := w.cmd(in, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (w *world) helper(t *testing.T, machine, user, mode string, stdin []byte, arg string) string {
	t.Helper()
	out, err := w.helperErr(machine, user, mode, stdin, arg)
	if err != nil {
		t.Fatalf("%s in %s: %v %s", mode, machine, err, out)
	}
	return out
}

func (w *world) cmd(stdin io.Reader, args ...string) *exec.Cmd {
	cmd := exec.Command("docker", args...)
	cmd.Env = append(os.Environ(), "DOCKER_HOST="+w.engine)
	cmd.Dir = w.dir
	cmd.Stdin = stdin
	return cmd
}

func (w *world) docker(t *testing.T, stdin io.Reader, args ...string) {
	t.Helper()
	if out, err := w.cmd(stdin, args...).CombinedOutput(); err != nil {
		t.Fatalf("docker %s: %v %s", args[0], err, out)
	}
}

func (w *world) composeOut(args ...string) (string, error) {
	out, err := w.cmd(nil, append([]string{"compose", "-p", project, "-f", "compose.yaml"}, args...)...).CombinedOutput()
	return string(out), err
}

func (w *world) compose(t *testing.T, args ...string) {
	t.Helper()
	if out, err := w.composeOut(args...); err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
