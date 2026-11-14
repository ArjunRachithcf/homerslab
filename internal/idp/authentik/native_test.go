package authentik_test

// Stories 27 and 32 (#20) in a real browser: a person signed in to the household account opens an app with native
// sign-in and is in it, with no second login. The app is the official catalog's Mealie, from its own compose file,
// with the settings and the client's secret file the agent hands it (the manifest's signIn env); the edge is the real
// one (a Caddy running edge.Render's configuration, in the host's network namespace, as the edge runs on a node), its
// sign-in listener included (ADR 0020, #19): Mealie's server fetches the discovery document and exchanges the code at
// auth.<main domain>, which inside its container resolves to the listener's address. The app's network is internal:
// it has no other way to the sign-in.
//
// What differs from a node, and why it doesn't change what is checked:
//   - the listener's address is the app network's bridge address, not 172.31.255.241 on the node's loopback, which
//     only the agent makes (with the firewall table that keeps the app off the rest of the host); the app reaches it
//     through its bridge either way, and it serves only the sign-in;
//   - the mesh is a second internal network's bridge address, where Chromium reaches the edge from this machine;
//   - the household's certificate is a test CA's, which Mealie's server trusts through SSL_CERT_FILE and
//     REQUESTS_CA_BUNDLE, and Chromium through the leaf's public key.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/appunit"
	"github.com/ArjunRachithcf/homerslab/internal/catalog"
	"github.com/ArjunRachithcf/homerslab/internal/edge"
	"github.com/ArjunRachithcf/homerslab/internal/idp"
	"github.com/ArjunRachithcf/homerslab/internal/idp/authentik"
	"github.com/ArjunRachithcf/homerslab/internal/manager/plan"
	"github.com/ArjunRachithcf/homerslab/internal/oidc"
)

// The native case's own addresses: the mesh, where Chromium reaches the edge, and the app's network, where the edge
// has the sign-in listener and the app its upstream; the edge's are the two bridges' own, on this machine. Its compose
// project is the suite's, with -native.
const (
	nativeMesh     = "10.217.9.1"
	nativeListener = "10.217.8.1"
	nativeApp      = "10.217.8.10"
	// The edge's image, as the edge's request matrix runs it (internal/edge/testdata/compose.yaml).
	edgeImage = "caddy:2.11.9-alpine@sha256:0bbb055bda6793c999bde78b1b5ce3e89f829fb031af2cf50056ea1246559fee"
	// A device on the mesh, idle: compose makes only the networks a service is on.
	phoneImage = "alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f"
)

func nativeSignIn(t *testing.T, w *world, a *authentik.Authentik) {
	ctx := context.Background()
	_, self, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(self), "..", "..", "..")
	cat, err := catalog.Load(os.DirFS(filepath.Join(root, "catalogs", "official")))
	if err != nil {
		t.Fatal(err)
	}
	var app *catalog.App
	for i := range cat.Apps {
		if cat.Apps[i].ID == "mealie" {
			app = &cat.Apps[i]
		}
	}
	if app == nil || len(app.SignIn) == 0 || app.SignIn[0].Method != catalog.OIDC || app.SignIn[0].Env == nil {
		t.Fatal("the official catalog's Mealie, with its OIDC sign-in")
	}
	m := app.SignIn[0]
	host := app.Names.Name + "." + household.Domain
	appURL := plan.URL(host, household.Port)

	// The person, an admin (Admin's client is theirs alone), and the two clients: Admin's, where they sign in first,
	// and the app's, made as installing it makes it (the manager's roles for its audience, its redirects on its URL).
	if err := a.EnsurePerson(ctx, idp.Person{Username: "ana", Name: "Ana Ferreira", Mail: "ana@ferreira.family", Role: idp.Admin}); err != nil {
		t.Fatal(err)
	}
	link, err := a.EnrolmentLink(ctx, "ana")
	if err != nil {
		t.Fatal(err)
	}
	c := w.OIDC()
	admin, err := a.EnsureClient(ctx, idp.Client{Slug: "homerslab-admin", Name: "Admin", RedirectURIs: []string{redirect}, Roles: []idp.Role{idp.Admin}, PasskeyOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	c.Issuer, c.ID, c.Secret = admin.Issuer, admin.ID, admin.Secret
	e, err := c.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var redirects []string
	for _, p := range m.RedirectURIs {
		redirects = append(redirects, appURL+p)
	}
	if app.Audience.Default != "adults" {
		t.Fatalf("Mealie's audience: %q", app.Audience.Default)
	}
	creds, err := a.EnsureClient(ctx, idp.Client{Slug: "homerslab-mealie", Name: app.Name, RedirectURIs: redirects, Roles: []idp.Role{idp.Admin, idp.Adult}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.DeleteClient(context.Background(), "homerslab-mealie") })

	// The app's environment, as the agent writes it (internal/agent's oidcEnv; its own test pins every value).
	values := map[string]string{m.Env.Issuer: creds.Issuer, m.Env.Discovery: creds.Issuer + ".well-known/openid-configuration",
		m.Env.ClientID: creds.ID, m.Env.ClientSecretFile: appunit.ClientSecretPath, m.Env.RedirectURI: redirects[0], m.Env.URL: appURL,
		m.Env.AdminGroup: idp.RoleGroup(idp.Admin)}
	env := map[string]string{"SSL_CERT_FILE": "/etc/homerslab-test/ca.crt", "REQUESTS_CA_BUNDLE": "/etc/homerslab-test/ca.crt"}
	for _, n := range m.Env.Names() {
		if n != "" {
			env[n] = values[n]
		}
	}

	dir := t.TempDir()
	ca, chain, key, spki := householdCertificate(t, household.Domain)
	files := map[string]string{"ca.crt": ca, "tls.crt": chain, "tls.key": key, "oidc-client-secret": creds.Secret.Reveal(),
		"app.compose.yaml": string(app.Compose)}
	cfg, err := edge.Render(edge.Config{AdminSocket: "/run/homerslab-edge/admin.sock", Storage: "/var/lib/homerslab-edge",
		Listen: []netip.Addr{netip.MustParseAddr(nativeMesh)}, Port: household.Port,
		Certificates: []edge.Certificate{{Domain: household.Domain, CertFile: "/etc/homerslab-test/tls.crt", KeyFile: "/etc/homerslab-test/tls.key"}},
		SignIn:       &edge.Site{Name: household.SignIn(), Dial: strings.TrimPrefix(upstream, "http://")},
		SignInListen: netip.MustParseAddr(nativeListener),
		Apps:         []edge.App{{Name: host, Dial: nativeApp + ":" + strconv.Itoa(app.Names.Port), SignsIn: true, Refused: m.RefusePaths}}})
	if err != nil {
		t.Fatal(err)
	}
	files["edge.json"] = string(cfg)
	for name, v := range files {
		// The test can't chown: readable by all, in a folder of the test's own.
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	envJSON, _ := json.Marshal(env)
	override := `name: ` + project + `-native
networks:
  mesh:
    internal: true
    ipam: {config: [{subnet: 10.217.9.0/24, gateway: ` + nativeMesh + `}]}
  apps:
    internal: true
    ipam: {config: [{subnet: 10.217.8.0/24, gateway: ` + nativeListener + `}]}
services:
  edge:
    image: ` + edgeImage + `
    command: ["caddy", "run", "--config", "/etc/homerslab-test/edge.json"]
    user: "4100:4100"
    cap_drop: [ALL]
    cap_add: [NET_BIND_SERVICE]
    environment: {HOME: /var/lib/homerslab-edge, XDG_DATA_HOME: /var/lib/homerslab-edge, XDG_CONFIG_HOME: /var/lib/homerslab-edge}
    tmpfs:
      - /run/homerslab-edge:uid=4100,gid=4100,mode=0750
      - /var/lib/homerslab-edge:uid=4100,gid=4100,mode=0700
    volumes: ["` + dir + `:/etc/homerslab-test:ro"]
    labels: {homerslab-test: native}
    # On the host, as on a node: it reaches the sign-in from the sign-in network's gateway, the one address Authentik
    # takes X-Forwarded-* from, and listens on the bridges' addresses, which exist once the networks do.
    network_mode: host
    depends_on: [` + app.Names.Service + `]
  ` + app.Names.Service + `:
    networks:
      apps: {ipv4_address: ` + nativeApp + `}
    extra_hosts: ["` + household.SignIn() + `=` + nativeListener + `"]
    environment: ` + string(envJSON) + `
    secrets: [` + appunit.ClientSecret + `]
    volumes: ["` + dir + `/ca.crt:/etc/homerslab-test/ca.crt:ro"]
    labels: {homerslab-test: native}
  phone:
    image: ` + phoneImage + `
    command: ["sleep", "infinity"]
    init: true
    labels: {homerslab-test: native}
    networks: [mesh]
secrets:
  ` + appunit.ClientSecret + `: {file: ` + dir + `/oidc-client-secret}
`
	if err := os.WriteFile(filepath.Join(dir, "native.compose.yaml"), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("/sys/fs/selinux/enforce"); err == nil {
		if out, err := exec.Command("chcon", "-R", "-t", "container_file_t", "-l", "s0", dir).CombinedOutput(); err != nil {
			t.Fatalf("labelling the files: %v %s", err, out)
		}
	}
	compose := func(args ...string) (string, error) {
		cmd := exec.Command("docker", append([]string{"compose", "-p", project + "-native", "--project-directory", dir,
			"-f", filepath.Join(dir, "app.compose.yaml"), "-f", filepath.Join(dir, "native.compose.yaml")}, args...)...)
		cmd.Env = append(os.Environ(), "DOCKER_HOST="+w.engine)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	_, _ = compose("down", "-v", "--remove-orphans")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := compose("logs", "--no-color", "--tail", "120")
			t.Logf("the edge's and the app's logs:\n%s", out)
		}
		if os.Getenv("HOMERSLAB_TEST_KEEP") == "" {
			_, _ = compose("down", "-v", "--remove-orphans")
		}
	})
	if out, err := compose("up", "-d", "--quiet-pull"); err != nil {
		t.Fatalf("docker compose up: %v\n%s", err, out)
	}

	// The browser's way in: the edge's mesh address, for every name of the household's.
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(ca))
	browser := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots},
			DialContext: func(ctx context.Context, n, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, n, nativeMesh+":443")
			}}}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		resp, err := browser.Get(appURL + "/api/app/about")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Mealie never answered through the edge: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
	// Its default admin's published password, at the route Mealie still takes it on: the edge answers, never Mealie.
	resp, err := browser.PostForm(appURL+"/api/auth/token", url.Values{"username": {"changeme@example.com"}, "password": {"MyPassword"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Mealie's default admin's password: %d, want the edge's 403", resp.StatusCode)
	}

	spec, _ := json.Marshal(map[string]string{"address": nativeMesh + ":443", "spki": spki, "enrolment": link.URL.Reveal(),
		"admin": c.AuthorizeURL(e, oidc.FreshAttempt(time.Now())), "landing": landing, "app": appURL, "username": "ana",
		"name": "Ana Ferreira", "mail": "ana@ferreira.family", "out": filepath.Join(dir, "browser.txt")})
	world := filepath.Join(dir, "native.json")
	if err := os.WriteFile(world, spec, 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("HOMERSLAB_TEST_BROWSER") == "external" {
		shareWithTheBrowser(t, dir, world)
		t.Logf("waiting for the browser: HOMERSLAB_TEST_NATIVE=%s npx playwright test e2e/native-sign-in.idp.spec.ts --project=desktop", world)
		for deadline := time.Now().Add(10 * time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
			if _, err := os.Stat(filepath.Join(dir, "browser.txt")); err == nil {
				break
			}
		}
	} else {
		cmd := exec.Command("npx", "playwright", "test", "e2e/native-sign-in.idp.spec.ts", "--project=desktop", "--reporter=list")
		cmd.Dir = filepath.Join(root, "web")
		cmd.Env = append(os.Environ(), "HOMERSLAB_TEST_NATIVE="+world)
		got, err := cmd.CombinedOutput()
		t.Logf("the browser:\n%s", got)
		if err != nil {
			t.Fatalf("the browser's side failed: %v", err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "browser.txt")); err != nil || strings.TrimSpace(string(b)) != "in, with no second login" {
		t.Fatalf("the browser's answer: %q %v", b, err)
	}
}

// householdCertificate is a test CA and the household's wildcard certificate from it, as PEM, with the leaf's public
// key hash, which Chromium takes as trusted. A CA proper, not a self-signed leaf: Python's strict verification wants one.
func householdCertificate(t *testing.T, domain string) (ca, chain, key, spki string) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(20), Subject: pkix.Name{CommonName: "HomersLab test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(2 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, SubjectKeyId: []byte{20, 20}}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTmpl := &x509.Certificate{SerialNumber: big.NewInt(21), Subject: pkix.Name{CommonName: "*." + domain},
		DNSNames: []string{"*." + domain}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(2 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(leafKey)
	pub, _ := x509.MarshalPKIXPublicKey(&leafKey.PublicKey)
	sum := sha256.Sum256(pub)
	enc := func(kind string, b []byte) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: b}))
	}
	return enc("CERTIFICATE", caDER), enc("CERTIFICATE", leafDER) + enc("CERTIFICATE", caDER), enc("PRIVATE KEY", keyDER),
		base64.StdEncoding.EncodeToString(sum[:])
}

// shareWithTheBrowser lets a browser that runs as another user (a developer's, where Go runs in a container as root)
// read the world's file and write its answer beside it. Only for HOMERSLAB_TEST_BROWSER=external: the folder is the
// test's own, and what is in it is the test's.
func shareWithTheBrowser(t *testing.T, dir, world string) {
	t.Helper()
	// t.TempDir's own parent, made for the test as its folder is: passed through, never listed.
	if err := os.Chmod(filepath.Dir(dir), 0o711); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(world, 0o644); err != nil {
		t.Fatal(err)
	}
}
