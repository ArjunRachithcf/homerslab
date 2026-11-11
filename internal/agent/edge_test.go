package agent_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/edge"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// wildcard is a certificate for *.<domain> and its key, PEM.
func wildcard(t *testing.T, domain string) (chain, key string) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(0x5eed), Subject: pkix.Name{CommonName: "*." + domain}, DNSNames: []string{"*." + domain},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalPKCS8PrivateKey(k)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}))
}

// joined is an agent on a node that has joined its mesh, at 100.88.7.186.
func joined(t *testing.T, c agent.Config) (string, *agenttest.Host, string) {
	t.Helper()
	if c.StateDir == "" {
		c.StateDir = t.TempDir()
	}
	sock, h := agenttest.StartWith(t, c)
	if code, body := call(t, sock, "POST", "/v1/mesh/join", joinBody()); code != http.StatusOK {
		t.Fatalf("join: %d %s", code, body)
	}
	return sock, h, c.StateDir
}

func withoutAdmin(t *testing.T, cfg []byte) []byte {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(cfg, &v); err != nil {
		t.Fatal(err)
	}
	delete(v, "admin")
	b, _ := json.Marshal(v)
	return b
}

func persistOff(t *testing.T, cfg []byte) bool {
	t.Helper()
	var v struct {
		Admin struct{ Config struct{ Persist *bool } }
	}
	if err := json.Unmarshal(cfg, &v); err != nil {
		t.Fatal(err)
	}
	return v.Admin.Config.Persist != nil && !*v.Admin.Config.Persist
}

func edgeStatus(t *testing.T, body []byte) agent.EdgeStatus {
	t.Helper()
	var s agent.EdgeStatus
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("%v in %s", err, body)
	}
	return s
}

// The certificate reaches the edge as files its unit loads, sealed by the agent: the key is never in the edge's
// configuration, so its admin API never shows it (#78). The edge serves Admin's name on the mesh address only, at the
// household's port.
func TestTheEdgeGetsItsCertificateAsSealedFilesNeverInline(t *testing.T) {
	testenv.Isolate(t)
	sock, h, state := joined(t, agent.Config{})
	chain, key := wildcard(t, "example.test")

	if code, body := call(t, sock, "PUT", "/v1/edge", agent.EdgeSites{Port: 9443, Admin: "admin.example.test"}); code != http.StatusOK {
		t.Fatalf("the sites: %d %s", code, body)
	}
	before := h.Ran("systemctl restart homerslab-edge.service")
	code, body := call(t, sock, "PUT", "/v1/edge/certificates/example.test", map[string]string{"chain": chain, "key": key})
	if code != http.StatusOK {
		t.Fatalf("the certificate: %d %s", code, body)
	}
	s := edgeStatus(t, body)
	if !s.Running || len(s.Certificates) != 1 || s.Certificates[0].Domain != "example.test" || s.Certificates[0].Serial != "5eed" {
		t.Errorf("the edge's status: %+v", s)
	}

	// Sealed by the agent, under the names the unit loads them by, the key on stdin only.
	for _, id := range []string{"tls-example.test.crt", "tls-example.test.key"} {
		b, err := os.ReadFile(filepath.Join(state, "edge", id+".cred"))
		if err != nil || bytes.Contains(b, []byte("PRIVATE KEY")) || bytes.Contains(b, []byte("CERTIFICATE")) {
			t.Errorf("%s: %v, sealed: %v", id, err, err == nil && !bytes.Contains(b, []byte("PRIVATE")))
		}
	}
	for _, c := range h.Cmds() {
		if strings.Contains(strings.Join(c.Args, " "), "PRIVATE KEY") {
			t.Errorf("the key was on a command line: %s", c)
		}
	}
	dropIn, err := os.ReadFile(filepath.Join(h.Root, "etc/systemd/system/homerslab-edge.service.d/50-homerslab-certificates.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"tls-example.test.crt", "tls-example.test.key"} {
		if want := "LoadCredentialEncrypted=" + id + ":" + filepath.Join(state, "edge", id+".cred") + "\n"; !strings.Contains(string(dropIn), want) {
			t.Errorf("the edge's drop-in lacks %q:\n%s", want, dropIn)
		}
	}
	// The unit gets its credentials only as it starts: written, reloaded, then restarted.
	reload, restart := h.Ran("systemctl daemon-reload"), h.Ran("systemctl restart homerslab-edge.service")
	if before >= 0 || restart < 0 || reload > restart {
		t.Errorf("the edge restarted at %d (before the certificate: %d), systemd reloaded at %d", restart, before, reload)
	}

	want, err := edge.Render(edge.Config{AdminSocket: agent.DefaultEdge.AdminSocket, Storage: "/var/lib/homerslab-edge",
		Listen: []netip.Addr{netip.MustParseAddr("100.88.7.186")}, Port: 9443,
		Certificates: []edge.Certificate{{Domain: "example.test", CertFile: "/run/credentials/homerslab-edge.service/tls-example.test.crt",
			KeyFile: "/run/credentials/homerslab-edge.service/tls-example.test.key"}},
		Admin: &edge.Site{Name: "admin.example.test", Upstream: "/run/homerslab-admin.sock"}})
	if err != nil {
		t.Fatal(err)
	}
	// The stand-in's admin socket is in the test's own directory: everything else is the real configuration.
	got := h.Edge.Config()
	if !edge.Same(withoutAdmin(t, got), withoutAdmin(t, want)) || !persistOff(t, got) {
		t.Errorf("the edge runs:\n%s\nwant:\n%s", got, want)
	}
	if bytes.Contains(got, []byte("PRIVATE KEY")) || bytes.Contains(got, []byte("BEGIN")) {
		t.Error("the edge's configuration holds key material")
	}

	other, otherKey := wildcard(t, "other.test")
	for name, b := range map[string]map[string]string{
		"another domain's certificate": {"chain": other, "key": otherKey},
		"another certificate's key":    {"chain": chain, "key": otherKey},
	} {
		if code, _ := call(t, sock, "PUT", "/v1/edge/certificates/example.test", b); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	if code, _ := call(t, sock, "PUT", "/v1/edge/certificates/..%2Fx", map[string]string{"chain": chain, "key": key}); code != http.StatusNotFound {
		t.Errorf("a domain that isn't one: %d, want 404", code)
	}
}

// The edge's autosave is off, so a restarted edge runs its boot configuration: the agent finds out and pushes the
// whole configuration again (#78).
func TestTheAgentPushesTheEdgesConfigurationAgainWhenItRestarts(t *testing.T) {
	testenv.Isolate(t)
	sock, h, _ := joined(t, agent.Config{CheckEvery: 50 * time.Millisecond})
	chain, key := wildcard(t, "example.test")
	call(t, sock, "PUT", "/v1/edge", agent.EdgeSites{Port: 443, Admin: "admin.example.test"})
	if code, body := call(t, sock, "PUT", "/v1/edge/certificates/example.test", map[string]string{"chain": chain, "key": key}); code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	want := h.Edge.Config()
	h.Edge.Restart()
	for range 100 {
		if edge.Same(h.Edge.Config(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("after a restart the edge runs %s", h.Edge.Config())
}

// Before the node is on the mesh, the edge has nowhere to listen: the agent keeps the sites and waits.
func TestTheEdgeWaitsForTheMesh(t *testing.T) {
	testenv.Isolate(t)
	sock, h := agenttest.StartWith(t, agent.Config{})
	if code, _ := call(t, sock, "PUT", "/v1/edge", agent.EdgeSites{Port: 443, Admin: "admin.example.test"}); code != http.StatusConflict {
		t.Errorf("the sites before the join: %d, want 409", code)
	}
	if h.Edge.Loads() != 0 {
		t.Error("the edge was loaded with nowhere to listen")
	}
	for _, bad := range []agent.EdgeSites{{Port: 0}, {Port: 70000}, {Port: 443, Admin: "admin..example.test"}} {
		if code, _ := call(t, sock, "PUT", "/v1/edge", bad); code != http.StatusBadRequest {
			t.Errorf("sites %+v: %d, want 400", bad, code)
		}
	}
}
