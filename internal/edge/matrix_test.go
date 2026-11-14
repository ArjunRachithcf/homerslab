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
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/edge"
	"github.com/ArjunRachithcf/homerslab/internal/firewall"
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
	helperMode = "HLTEST16_HELPER"
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
	case "authentik": // the sign-in's server, on the sign-in network: it says what reached it
		_ = http.ListenAndServe(signInStub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "Authentik, at %s %s", r.Host, r.URL.RequestURI())
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

// request is one request a browser makes: to an address, with a TLS server name and a Host, for a path ("/" when
// empty).
type request struct {
	Addr, SNI, Host string
	Path            string // as sent, escapes and all; "/" when empty
	Plain           bool   // plain HTTP, no TLS
	CA              string
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
	req, _ := http.NewRequestWithContext(ctx, "GET", scheme+"://"+q.Host+"/", nil)
	if q.Path != "" {
		req.URL.Opaque = "//" + q.Host + q.Path // sent exactly as written
	}
	req.Host = q.Host
	// A redirect is an answer to look at, never followed.
	client := &http.Client{Transport: tr, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
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
		Admin: &edge.Site{Name: "admin.example.test", Upstream: adminSock}, SignIn: &edge.Site{Name: "auth.example.test", Dial: signInStub}}
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
	w.firewall(t)
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

	// Authentik's first-admin flow is refused on every listener before the provider sees it, its page and its executor
	// API alike, however the path is written (#84). Today the edge has one listener for the sign-in, the mesh one; the
	// sign-in listener (#19) and the public listener (#32) take the same rows.
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
		w.load(t, want)
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
	var meshIf, lanIf string
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
		}
	}
	if meshIf == "" || lanIf == "" {
		t.Fatalf("the edge's mesh and LAN interfaces aren't there:\n%s", out)
	}
	table := firewall.Render(firewall.State{Mesh: []string{meshIf}, LAN: []string{lanIf},
		LANPrefixes: []netip.Prefix{netip.MustParsePrefix("10.216.2.0/24")}})
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
	for _, m := range []string{"admin", "authentik", "phone", "laptop-on-the-lan"} {
		w.copy(t, m, []file{{path: strings.TrimPrefix(helperBin, "/"), body: bin, mode: 0o755}})
	}
	w.compose(t, "start")
	w.docker(t, nil, "exec", "-u", "0", container("admin"), "sh", "-c", "chown 4200:4100 /run/homerslab-admin && chmod 0750 /run/homerslab-admin")
	w.docker(t, nil, "exec", "-d", "-u", "4200:4100", "-e", helperMode+"=admin", container("admin"), helperBin, "-")
	w.docker(t, nil, "exec", "-d", "-e", helperMode+"=authentik", container("authentik"), helperBin, "-")
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
