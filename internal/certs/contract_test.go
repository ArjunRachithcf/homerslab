package certs_test

// The DNS provider contract suite: a test ACME CA (Pebble) and a test DNS server (BIND, through libdns's RFC 2136
// provider), in containers (testdata/compose.yaml, compose project hltest-16). It needs Docker: it runs when
// HOMERSLAB_TEST_DOCKER_HOST names the engine to use, and is skipped otherwise. The code under test never sees that
// engine (testenv points DOCKER_HOST at nowhere); only this harness does, explicitly.
//
// The suite runs on the world's primary: the harness copies this test binary into that container and runs
// TestTheDNSProviderContractOnThePrimary there, where the machine's own resolver is the mesh's local zone. So the
// issuance below is the one #79 asks for: on a primary that resolves the household's domain through a mesh zone with
// no SOA, beside the public path's wildcard CNAME.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libdns/libdns"
	mdns "github.com/miekg/dns"

	"github.com/ArjunRachithcf/homerslab/internal/certs"
	"github.com/ArjunRachithcf/homerslab/internal/dns"
	"github.com/ArjunRachithcf/homerslab/internal/mesh"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

const project = "hltest-16"

// The world's addresses (testdata/compose.yaml).
const (
	dnsServer = "10.216.0.53:53"
	pebbleIP  = "10.216.0.14"
	meshIP    = "100.88.7.186" // the primary's address on the mesh, as the mesh's zone answers it
)

func TestTheDNSProviderContract(t *testing.T) {
	testenv.Isolate(t)
	engine := os.Getenv("HOMERSLAB_TEST_DOCKER_HOST")
	if engine == "" || os.Getenv("HLTEST16_INSIDE") == "1" {
		t.Skip("set HOMERSLAB_TEST_DOCKER_HOST to the engine for the contract suite's containers")
	}
	w := up(t, engine)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	w.docker(t, nil, nil, "cp", self, container("primary")+":/hl.test")
	ca := w.docker(t, nil, nil, "cp", container("pebble")+":/test/certs/pebble.minica.pem", "-")
	w.docker(t, nil, bytes.NewReader([]byte(ca)), "cp", "-", container("primary")+":/")
	out, err := w.dockerOut(nil, nil, "exec", "-e", "HLTEST16_INSIDE=1", "-e", "HLTEST_TSIG="+w.tsig, container("primary"),
		"/hl.test", "-test.run", "^TestTheDNSProviderContractOnThePrimary$", "-test.v", "-test.count=1")
	t.Logf("on the primary:\n%s", out)
	if err != nil {
		if strings.Contains(out, "not found") || strings.Contains(out, "no such file") {
			t.Fatalf("the test binary didn't run on the primary (build it with CGO_ENABLED=0): %v", err)
		}
		t.Fatalf("the contract on the primary: %v", err)
	}
	if !strings.Contains(out, "--- PASS: TestTheDNSProviderContractOnThePrimary") {
		t.Fatal("the contract didn't run on the primary")
	}
}

// TestTheDNSProviderContractOnThePrimary is the suite itself, inside the world's primary.
func TestTheDNSProviderContractOnThePrimary(t *testing.T) {
	testenv.Isolate(t)
	if os.Getenv("HLTEST16_INSIDE") != "1" {
		t.Skip("runs inside the contract suite's primary container (TestTheDNSProviderContract)")
	}
	meshZone(t)
	ctx := context.Background()
	tsig := mesh.Secret(os.Getenv("HLTEST_TSIG"))
	settings := dns.Settings{Provider: "rfc2136", Server: dnsServer, KeyName: "homerslab.", KeyAlgorithm: "hmac-sha256"}
	resolver := dns.Resolver{Servers: []string{dnsServer}} // the stand-in for the public resolvers
	ca := pebble(t)
	authority := certs.Authority{Name: "Pebble", Directory: "https://pebble:14000/dir", IssuerDomains: []string{"pebble.test"},
		HTTP: ca.client, DNS: resolver, Propagation: 30 * time.Second, Poll: 200 * time.Millisecond}
	open := func(t *testing.T, token mesh.Secret, b dns.Book) (*dns.Owned, *counting) {
		t.Helper()
		p, err := dns.Open(settings, token)
		if err != nil {
			t.Fatal(err)
		}
		c := &counting{Provider: p}
		return &dns.Owned{Provider: c, Zone: "example.test.", Domain: "example.test", Book: b}, c
	}
	theBook := &book{}

	t.Run("the primary resolves the household's domain through the mesh's zone, which has no SOA", func(t *testing.T) {
		addrs, err := net.DefaultResolver.LookupHost(ctx, "admin.example.test")
		if err != nil || !slices.Equal(addrs, []string{meshIP}) {
			t.Fatalf("the machine's own resolver: %v %v, want the mesh's %s", addrs, err, meshIP)
		}
		// Claim (#79), as reported: the zone lookup through the machine's own resolver finds no zone.
		system := dns.Resolver{Servers: []string{"127.0.0.1:53"}}
		if z, err := system.Zone(ctx, "_acme-challenge.example.test"); !errors.Is(err, dns.ErrNoZone) {
			t.Errorf("through the machine's own resolver: zone %q, %v; want none", z, err)
		}
		if z, err := resolver.Zone(ctx, "_acme-challenge.example.test"); err != nil || z != "example.test." {
			t.Errorf("through the public resolvers: zone %q, %v", z, err)
		}
	})

	t.Run("the provider lists the zone with its token, and refuses another", func(t *testing.T) {
		o, _ := open(t, tsig, theBook)
		recs, err := o.Provider.GetRecords(ctx, "example.test.")
		if err != nil || !hasRR(recs, "@", "MX") || !hasRR(recs, "*", "CNAME") {
			t.Fatalf("the zone through its token: %v %v", recs, err)
		}
		bad, _ := open(t, mesh.Secret(base64.StdEncoding.EncodeToString(make([]byte, 32))), theBook)
		if _, err := bad.Provider.GetRecords(ctx, "example.test."); err == nil {
			t.Error("a wrong TSIG key listed the zone")
		}
	})

	t.Run("only HomersLab's own records are written, and an inventory lists them", func(t *testing.T) {
		o, _ := open(t, tsig, theBook)
		if err := o.Create(ctx, dns.Record{Name: "www", Type: "TXT", Value: "beside a CNAME"}); !errors.Is(err, dns.ErrTaken) {
			t.Errorf("a TXT beside the household's CNAME: %v, want ErrTaken", err)
		}
		if err := o.Create(ctx, dns.Record{Name: "_dmarc", Type: "TXT", Value: "v=DMARC1; p=none"}); !errors.Is(err, dns.ErrTaken) {
			t.Errorf("a TXT beside the household's DMARC: %v, want ErrTaken", err)
		}
		if free, err := o.Free(ctx, "photos"); err != nil || !free {
			t.Errorf("photos is free in the provider's listing: %v %v (the wildcard answers it in DNS)", free, err)
		}
		r := dns.Record{Name: "_acme-challenge", Type: "TXT", Value: "a-test-value", TTL: time.Minute}
		if err := o.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
		if ok, err := resolver.HasTXT(ctx, "example.test.", "_homerslab.example.test.", "homerslab=1; _acme-challenge TXT"); err != nil || !ok {
			t.Errorf("the inventory record in DNS: %v %v", ok, err)
		}
		if err := o.Delete(ctx, dns.Record{Name: "_dmarc", Type: "TXT", Value: "v=DMARC1; p=quarantine"}); !errors.Is(err, dns.ErrNotOurs) {
			t.Errorf("deleting the household's DMARC: %v, want ErrNotOurs", err)
		}
		if err := o.Delete(ctx, r); err != nil {
			t.Fatal(err)
		}
		recs, err := o.Provider.GetRecords(ctx, "example.test.")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range [][2]string{{"@", "MX"}, {"@", "TXT"}, {"_dmarc", "TXT"}, {"*", "CNAME"}, {"www", "CNAME"}} {
			if !hasRR(recs, want[0], want[1]) {
				t.Errorf("the household's %s %s is gone", want[1], want[0])
			}
		}
		if hasRR(recs, "_homerslab", "TXT") || hasRR(recs, "_acme-challenge", "TXT") {
			t.Errorf("HomersLab's records stayed: %v", recs)
		}
	})

	t.Run("CAA records that don't allow the authority are named", func(t *testing.T) {
		o, _ := open(t, tsig, theBook)
		caa := libdns.CAA{Name: "@", TTL: time.Minute, Tag: "issue", Value: "sectigo.com"}
		if _, err := o.Provider.AppendRecords(ctx, "example.test.", []libdns.Record{caa}); err != nil {
			t.Fatal(err)
		}
		var forbids *certs.CAAForbids
		if err := authority.CheckCAA(ctx, "example.test"); !errors.As(err, &forbids) || !slices.Equal(forbids.Allowed, []string{"sectigo.com"}) {
			t.Errorf("with CAA for another authority: %v", err)
		} else if !strings.Contains(err.Error(), "sectigo.com") || !strings.Contains(err.Error(), "Pebble") {
			t.Errorf("the reason doesn't say who may issue: %v", err)
		}
		if _, err := o.Provider.DeleteRecords(ctx, "example.test.", []libdns.Record{caa}); err != nil {
			t.Fatal(err)
		}
		if err := authority.CheckCAA(ctx, "example.test"); err != nil {
			t.Errorf("without CAA records: %v", err)
		}
	})

	account, err := certs.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	var cert certs.Certificate
	t.Run("issuance on the primary, beside the public wildcard CNAME", func(t *testing.T) {
		o, _ := open(t, tsig, theBook)
		c, issued, err := authority.Ensure(ctx, certs.Request{Domain: "example.test", Zone: o, AccountKey: account,
			Contact: "ines@ferreira.family"}, nil, time.Now())
		if err != nil || !issued {
			t.Fatalf("issued %v: %v", issued, err)
		}
		cert = c
		ca.verify(t, c, "admin.example.test")
		if life := c.NotAfter.Sub(c.NotBefore); life < 89*24*time.Hour || life > 91*24*time.Hour {
			t.Errorf("the certificate lives %v", life)
		}
		recs, _ := o.Provider.GetRecords(ctx, "example.test.")
		if hasRR(recs, "_acme-challenge", "TXT") || hasRR(recs, "_homerslab", "TXT") {
			t.Errorf("the challenge's records stayed: %v", recs)
		}
		if b, _ := theBook.Records(ctx, "example.test."); len(b) != 0 {
			t.Errorf("the book still lists %v", b)
		}
	})
	if cert.Serial == "" {
		t.Fatal("no certificate: the renewals can't be checked")
	}

	due := cert.RenewAt().Add(time.Minute) // the fake clock: a third of the lifetime left
	t.Run("a renewal with a broken token fails, and leaves the certificate as it was", func(t *testing.T) {
		o, _ := open(t, mesh.Secret(base64.StdEncoding.EncodeToString(make([]byte, 32))), theBook)
		got, issued, err := authority.Ensure(ctx, certs.Request{Domain: "example.test", Zone: o, AccountKey: account}, &cert, due)
		if err == nil || issued {
			t.Fatalf("a renewal with a broken token: issued %v, %v", issued, err)
		}
		if got.Serial != "" && got.Serial != cert.Serial {
			t.Errorf("a failed renewal gave %s", got.Serial)
		}
	})

	t.Run("the replaced token renews", func(t *testing.T) {
		o, _ := open(t, tsig, theBook)
		got, issued, err := authority.Ensure(ctx, certs.Request{Domain: "example.test", Zone: o, AccountKey: account}, &cert, due)
		if err != nil || !issued || got.Serial == cert.Serial {
			t.Fatalf("renewed %v %s: %v", issued, got.Serial, err)
		}
		ca.verify(t, got, "photos.example.test")
		cert = got
	})

	t.Run("a restore puts back a valid certificate, and issues nothing", func(t *testing.T) {
		o, c := open(t, tsig, &book{}) // a new primary: its database restored, its book empty
		restored, err := certs.Parse("example.test", cert.Chain, cert.Key)
		if err != nil {
			t.Fatal(err)
		}
		got, issued, err := authority.Ensure(ctx, certs.Request{Domain: "example.test", Zone: o, AccountKey: account}, &restored, time.Now())
		if err != nil || issued || got.Serial != cert.Serial {
			t.Fatalf("after a restore: issued %v, serial %s (had %s): %v", issued, got.Serial, cert.Serial, err)
		}
		if n := c.writes.Load(); n != 0 {
			t.Errorf("the restore wrote %d DNS records", n)
		}
		// The machine was lost or stolen: its key may be in someone's hands, so a new one is issued all the same.
		got, issued, err = authority.Ensure(ctx, certs.Request{Domain: "example.test", Zone: o, AccountKey: account, Fresh: true}, &restored, time.Now())
		if err != nil || !issued || got.Serial == cert.Serial {
			t.Fatalf("after a lost machine: issued %v: %v", issued, err)
		}
	})
}

// counting is a provider that counts its writes.
type counting struct {
	dns.Provider
	writes atomic.Int32
}

func (c *counting) AppendRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	c.writes.Add(1)
	return c.Provider.AppendRecords(ctx, zone, recs)
}

func (c *counting) DeleteRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	c.writes.Add(1)
	return c.Provider.DeleteRecords(ctx, zone, recs)
}

// book is the manager's list of HomersLab's records, in memory.
type book struct {
	mu   sync.Mutex
	recs map[string][]dns.Record
}

func (b *book) Records(_ context.Context, zone string) ([]dns.Record, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.recs[zone]), nil
}
func (b *book) Keep(_ context.Context, zone string, r dns.Record) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.recs == nil {
		b.recs = map[string][]dns.Record{}
	}
	b.recs[zone] = append(b.recs[zone], r)
	return nil
}
func (b *book) Forget(_ context.Context, zone string, r dns.Record) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.recs[zone] = slices.DeleteFunc(b.recs[zone], func(k dns.Record) bool { return k.Name == r.Name && k.Type == r.Type && k.Value == r.Value })
	return nil
}

func hasRR(recs []libdns.Record, name, typ string) bool {
	return slices.ContainsFunc(recs, func(r libdns.Record) bool { rr := r.RR(); return rr.Name == name && rr.Type == typ })
}

// meshZone makes this machine's own resolver the mesh's local zone: the household's names at the primary's mesh
// address, and nothing else, no SOA included.
func meshZone(t *testing.T) {
	t.Helper()
	h := mdns.HandlerFunc(func(w mdns.ResponseWriter, q *mdns.Msg) {
		m := new(mdns.Msg)
		m.SetReply(q)
		m.Authoritative = true
		name := strings.ToLower(q.Question[0].Name)
		if q.Question[0].Qtype == mdns.TypeA && strings.HasSuffix(name, ".example.test.") {
			rr, _ := mdns.NewRR(name + " 60 IN A " + meshIP)
			m.Answer = append(m.Answer, rr)
		}
		_ = w.WriteMsg(m)
	})
	pc, err := net.ListenPacket("udp", "127.0.0.1:53")
	if err != nil {
		t.Fatal(err)
	}
	srv := &mdns.Server{PacketConn: pc, Handler: h}
	go srv.ActivateAndServe()
	t.Cleanup(func() { _ = srv.Shutdown() })
}

// ca is the test CA: an HTTP client that trusts its directory, and its root, which issued the certificates.
type ca struct {
	client *http.Client
	roots  *x509.CertPool
}

func pebble(t *testing.T) ca {
	t.Helper()
	b, err := os.ReadFile("/pebble.minica.pem")
	if err != nil {
		t.Fatal(err)
	}
	dirRoots := x509.NewCertPool()
	dirRoots.AppendCertsFromPEM(b)
	// The machine's own resolver is the mesh's: the CA is reached at its address, by its name.
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, _ := net.SplitHostPort(addr)
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(pebbleIP, port))
	}
	c := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: dial, TLSClientConfig: &tls.Config{RootCAs: dirRoots}}}
	resp, err := c.Get("https://pebble:15000/roots/0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	root, _ := io.ReadAll(resp.Body)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(root) {
		t.Fatalf("Pebble's root: %s", root)
	}
	return ca{client: c, roots: roots}
}

// verify checks that a certificate chains to the CA's root, and is valid for name.
func (c ca) verify(t *testing.T, cert certs.Certificate, name string) {
	t.Helper()
	var chain []*x509.Certificate
	rest := cert.Chain
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		x, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		chain = append(chain, x)
	}
	inter := x509.NewCertPool()
	for _, x := range chain[1:] {
		inter.AddCert(x)
	}
	if _, err := chain[0].Verify(x509.VerifyOptions{DNSName: name, Roots: c.roots, Intermediates: inter}); err != nil {
		t.Errorf("the certificate for %s: %v", name, err)
	}
	if !slices.Equal(chain[0].DNSNames, []string{"*.example.test"}) {
		t.Errorf("the certificate names %v, want only the wildcard", chain[0].DNSNames)
	}
}

// world is the compose project.
type world struct {
	engine, dir, tsig string
}

func container(s string) string { return project + "-" + s + "-1" }

func up(t *testing.T, engine string) *world {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	w := &world{engine: engine, dir: filepath.Join(filepath.Dir(self), "testdata"), tsig: base64.StdEncoding.EncodeToString(key)}
	w.compose(t, "down", "-v", "--remove-orphans")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := w.composeOut("logs", "--no-color", "--tail", "80")
			t.Logf("the containers' logs:\n%s", out)
		}
		w.compose(t, "down", "-v", "--remove-orphans")
	})
	w.compose(t, "up", "-d", "--quiet-pull", "--wait")
	return w
}

func (w *world) cmd(env []string, stdin io.Reader, args ...string) *exec.Cmd {
	cmd := exec.Command("docker", args...)
	cmd.Env = append(os.Environ(), "DOCKER_HOST="+w.engine, "HLTEST_TSIG="+w.tsig)
	cmd.Env = append(cmd.Env, env...)
	cmd.Dir = w.dir
	cmd.Stdin = stdin
	return cmd
}

func (w *world) dockerOut(env []string, stdin io.Reader, args ...string) (string, error) {
	out, err := w.cmd(env, stdin, args...).CombinedOutput()
	return string(out), err
}

func (w *world) docker(t *testing.T, env []string, stdin io.Reader, args ...string) string {
	t.Helper()
	cmd := w.cmd(env, stdin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", args[0], err, stderr.String())
	}
	return string(out)
}

func (w *world) composeOut(args ...string) (string, error) {
	return w.dockerOut(nil, nil, append([]string{"compose", "-p", project, "-f", "compose.yaml"}, args...)...)
}

func (w *world) compose(t *testing.T, args ...string) {
	t.Helper()
	if out, err := w.composeOut(args...); err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
