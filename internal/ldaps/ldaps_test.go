package ldaps_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/ldaps"
	"github.com/ArjunRachithcf/homeward/internal/ldaps/ldaptest"
	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

// certificate writes a certificate for names, signed by a root of the test's own, and returns its files and the root.
func certificate(t *testing.T, names ...string) (string, string, *x509.CertPool) {
	t.Helper()
	rootKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rootTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test root"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	rootDER, _ := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	root, _ := x509.ParseCertificate(rootDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, root, &key.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	kd, _ := x509.MarshalPKCS8PrivateKey(key)
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	_ = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}), 0o600)
	pool := x509.NewCertPool()
	pool.AddCert(root)
	return certFile, keyFile, pool
}

const (
	bindDN = "cn=lena,ou=users,dc=jellyfin,dc=homerslab"
	bindPW = "lena's app password"
)

// world is the port in front of a stand-in outpost.
func world(t *testing.T, cfg ldaps.Config) (string, *x509.CertPool) {
	t.Helper()
	cert, key, roots := certificate(t, "*.example.test")
	outpost, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { outpost.Close() })
	go ldaptest.Directory(outpost, bindDN, bindPW)
	cfg.Name, cfg.CertFile, cfg.KeyFile = "auth.example.test", cert, key
	if cfg.Upstream == "" {
		cfg.Upstream = outpost.Addr().String()
	}
	s, err := ldaps.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return ln.Addr().String(), roots
}

func dial(addr string, c *tls.Config) (net.Conn, error) {
	return tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr, c)
}

func TestAnAppBindsThroughThePortCheckingTheSignInsCertificate(t *testing.T) {
	testenv.Isolate(t)
	addr, roots := world(t, ldaps.Config{})
	c, err := dial(addr, &tls.Config{ServerName: "auth.example.test", RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	if err := ldaptest.Bind(c, bindDN, bindPW); err != nil {
		t.Errorf("a bind through the port: %v", err)
	}
	c.Close()
	c, err = dial(addr, &tls.Config{ServerName: "auth.example.test", RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := ldaptest.Bind(c, bindDN, "wrong"); !errors.Is(err, ldaptest.ErrRefused) {
		t.Errorf("a wrong password: %v, want the outpost's refusal", err)
	}
}

func TestThePortServesOnlyTheSignInsName(t *testing.T) {
	testenv.Isolate(t)
	addr, roots := world(t, ldaps.Config{})
	for name, c := range map[string]*tls.Config{
		"another app's name":           {ServerName: "pdf.example.test", RootCAs: roots},
		"Admin's name":                 {ServerName: "admin.example.test", RootCAs: roots},
		"no name (an address)":         {ServerName: "", InsecureSkipVerify: true},
		"the bare domain":              {ServerName: "example.test", InsecureSkipVerify: true},
		"TLS 1.1":                      {ServerName: "auth.example.test", RootCAs: roots, MaxVersion: tls.VersionTLS11},
		"another app's name, no check": {ServerName: "pdf.example.test", InsecureSkipVerify: true},
	} {
		t.Run(name, func(t *testing.T) {
			conn, err := dial(addr, c)
			if err == nil {
				// The handshake's end can arrive after Dial returns on TLS 1.3: the bind must fail then.
				err = ldaptest.Bind(conn, bindDN, bindPW)
				conn.Close()
			}
			if err == nil {
				t.Error("bound")
			}
		})
	}
	t.Run("plain LDAP, no TLS", func(t *testing.T) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := ldaptest.Bind(conn, bindDN, bindPW); err == nil {
			t.Error("bound without TLS")
		}
	})
}

func TestThePortRefusesACertificateThatDoesntCoverTheSignIn(t *testing.T) {
	testenv.Isolate(t)
	cert, key, _ := certificate(t, "pdf.example.test")
	if _, err := ldaps.New(ldaps.Config{Name: "auth.example.test", CertFile: cert, KeyFile: key, Upstream: "127.0.0.1:3389"}, nil); err == nil {
		t.Error("a certificate for another name was taken")
	}
	cert, key, _ = certificate(t, "*.example.test")
	for _, name := range []string{"pdf.example.test", "*.example.test", "auth.example.test:636", ""} {
		if _, err := ldaps.New(ldaps.Config{Name: name, CertFile: cert, KeyFile: key, Upstream: "127.0.0.1:3389"}, nil); err == nil {
			t.Errorf("the name %q was taken", name)
		}
	}
	if _, err := ldaps.New(ldaps.Config{Name: "auth.example.test", CertFile: cert, KeyFile: key, Upstream: "nowhere"}, nil); err == nil {
		t.Error("an outpost with no port was taken")
	}
}

func TestThePortClosesConnectionsOverItsLimit(t *testing.T) {
	testenv.Isolate(t)
	addr, roots := world(t, ldaps.Config{MaxConns: 1})
	first, err := dial(addr, &tls.Config{ServerName: "auth.example.test", RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err := ldaptest.Bind(first, bindDN, bindPW); err != nil {
		t.Fatal(err)
	}
	second, err := dial(addr, &tls.Config{ServerName: "auth.example.test", RootCAs: roots})
	if err == nil {
		err = ldaptest.Bind(second, bindDN, bindPW)
		second.Close()
	}
	if err == nil {
		t.Error("a second connection was carried over the limit of one")
	}
}

func TestAnOutpostThatIsDownEndsTheConnection(t *testing.T) {
	testenv.Isolate(t)
	gone, _ := net.Listen("tcp", "127.0.0.1:0")
	down := gone.Addr().String()
	gone.Close()
	addr, roots := world(t, ldaps.Config{Upstream: down})
	c, err := dial(addr, &tls.Config{ServerName: "auth.example.test", RootCAs: roots})
	if err == nil {
		err = ldaptest.Bind(c, bindDN, bindPW)
		c.Close()
	}
	if err == nil || strings.Contains(err.Error(), "result") {
		t.Errorf("with the outpost down: %v, want the connection ended", err)
	}
}

func TestAQuietConnectionIsClosed(t *testing.T) {
	testenv.Isolate(t)
	addr, roots := world(t, ldaps.Config{Idle: 300 * time.Millisecond})
	c, err := dial(addr, &tls.Config{ServerName: "auth.example.test", RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte{0x30}); err != nil { // the start of a request, then nothing
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("a quiet connection, after its idle time: %v, want it closed by the port", err)
	}
}

func TestOneAppsNetworkCantHoldEveryConnection(t *testing.T) {
	testenv.Isolate(t)
	addr, roots := world(t, ldaps.Config{MaxConns: 8, PerNetwork: 2})
	var quiet []net.Conn
	for range 2 { // connections that say nothing
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		quiet = append(quiet, c)
	}
	time.Sleep(100 * time.Millisecond)
	c, err := dial(addr, &tls.Config{ServerName: "auth.example.test", RootCAs: roots})
	if err == nil {
		err = ldaptest.Bind(c, bindDN, bindPW)
		c.Close()
	}
	if err == nil {
		t.Error("a third connection from the same network was carried over its limit of two")
	}
	for _, q := range quiet {
		q.Close()
	}
	var bound error
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if c, err := dial(addr, &tls.Config{ServerName: "auth.example.test", RootCAs: roots}); err == nil {
			bound = ldaptest.Bind(c, bindDN, bindPW)
			c.Close()
			if bound == nil {
				return
			}
		}
	}
	t.Errorf("once the quiet connections closed, a bind: %v", bound)
}

func TestAConnectionThatSaysNothingAfterItsHandshakeIsClosed(t *testing.T) {
	testenv.Isolate(t)
	addr, roots := world(t, ldaps.Config{FirstRequest: 300 * time.Millisecond, Idle: time.Minute})
	c, err := dial(addr, &tls.Config{ServerName: "auth.example.test", RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Errorf("a connection quiet since its handshake: %v after %s, want it closed within its first-request time", err, time.Since(start))
	}
}

// Addresses in one /28 are one app's network; the next /28 is another app's, with slots of its own.
func TestTheLimitCountsAnAppsWholeNetwork(t *testing.T) {
	testenv.Isolate(t)
	for addr, want := range map[string]string{"172.31.240.17:4000": "172.31.240.16/28", "172.31.240.30:1": "172.31.240.16/28",
		"172.31.240.33:1": "172.31.240.32/28", "[::ffff:172.31.240.17]:9": "172.31.240.16/28", "[fd00::1]:9": "fd00::1"} {
		a, _ := net.ResolveTCPAddr("tcp", addr)
		if got := ldaps.Network(a); got != want {
			t.Errorf("%s: %s, want %s", addr, got, want)
		}
	}
}
