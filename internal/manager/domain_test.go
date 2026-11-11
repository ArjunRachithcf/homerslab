package manager_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/certs"
	"github.com/ArjunRachithcf/homerslab/internal/dns"
	"github.com/ArjunRachithcf/homerslab/internal/manager"
	"github.com/ArjunRachithcf/homerslab/internal/mesh"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// The DNS tokens in these tests: the one the provider takes, and others.
const (
	dnsToken   = "ik_DNSTOKEN0000000000000000000000000000000000000000000"
	otherToken = "ik_OTHERTOKEN000000000000000000000000000000000000000000"
)

// fakeDomains stands in for the DNS provider and the certificate authority at the manager's seam; the dns and certs
// packages themselves pass the DNS provider contract suite against a test CA and a test DNS server (internal/certs).
type fakeDomains struct {
	mu     sync.Mutex
	token  string // the token the provider takes
	caa    error
	life   time.Duration
	issued int
	zones  []string // the zones it was asked for, and with which token: "zone token"
	last   manager.Issuance
}

func newFakeDomains() *fakeDomains { return &fakeDomains{token: dnsToken, life: 90 * 24 * time.Hour} }

func (f *fakeDomains) Zone(_ context.Context, s dns.Settings, token mesh.Secret, domain string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := s.Check(); err != nil {
		return "", err
	}
	if domain == "nowhere.test" {
		return "", fmt.Errorf("%w: %s", dns.ErrNoZone, domain)
	}
	if token.Reveal() != f.token {
		return "", manager.ErrDNSRefused
	}
	f.zones = append(f.zones, domain+".")
	return domain + ".", nil
}

func (f *fakeDomains) CAA(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.caa
}

func (f *fakeDomains) Ensure(_ context.Context, i manager.Issuance, have *certs.Certificate, now time.Time) (certs.Certificate, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = i
	if have != nil && !have.Due(now) {
		return *have, false, nil
	}
	if i.Token.Reveal() != f.token {
		return certs.Certificate{}, false, manager.ErrDNSRefused
	}
	if i.Account == "" || i.Zone == "" {
		return certs.Certificate{}, false, fmt.Errorf("no account (%v) or no zone (%q)", i.Account == "", i.Zone)
	}
	f.issued++
	chain, key := issue(i.Domain, int64(f.issued), now, f.life)
	c, err := certs.Parse(i.Domain, chain, key)
	return c, err == nil, err
}

func (f *fakeDomains) Authority() string { return "Let's Encrypt" }

func (f *fakeDomains) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.issued }

// issue is a certificate for *.domain, from now for life, and its key.
func issue(domain string, serial int64, now time.Time, life time.Duration) ([]byte, mesh.Secret) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "*." + domain}, DNSNames: []string{"*." + domain},
		NotBefore: now, NotAfter: now.Add(life)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	kd, _ := x509.MarshalPKCS8PrivateKey(k)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), mesh.Secret(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}))
}

func withDomains(f *fakeDomains) option { return func(p *primary) { p.domains = f } }

// atDomain is a primary whose setup has come to its domain step on NetBird's cloud, the steps before it stood in, and
// the steps after it (mail, the kit) stood in too, so the first half can end.
func atDomain(t *testing.T, opts ...option) (*primary, *browser, *fakeNetBird, *fakeDomains) {
	t.Helper()
	nb, fd := &fakeNetBird{cloudPort: 9443}, newFakeDomains()
	p := newPrimary(t, append([]option{withBuiltSteps("signin", "mail", "kit"), withNetBird(nb), withDomains(fd)}, opts...)...)
	nb.now = p.clock.Now
	b := p.claimed()
	b.firstThree()
	b.mustAnswer("signin", nil)
	b.mustAnswer("network", map[string]string{"mesh": "netbird", "controlPlane": "cloud"})
	if r := p.secret("mesh", givenToken); r.Code != http.StatusOK {
		t.Fatalf("the mesh's token: %d %s", r.Code, r.Body)
	}
	return p, b, nb, fd
}

var infomaniak = map[string]string{"domain": "Example.Test", "provider": "infomaniak"}

// throughDomain answers the domain step and gives the DNS token: the certificate is issued.
func throughDomain(t *testing.T, p *primary, b *browser) {
	t.Helper()
	b.mustAnswer("domain", infomaniak)
	if r := p.secret("dns", dnsToken); r.Code != http.StatusOK {
		t.Fatalf("the DNS token: %d %s", r.Code, r.Body)
	}
}

func (p *primary) edge(t *testing.T) agent.EdgeStatus {
	t.Helper()
	r := serveAgent(t, p.agent, "GET", "/v1/edge")
	var s agent.EdgeStatus
	r.json(t, &s)
	return s
}

func TestTheLANPageRefusesTheDNSTokenAndPointsToTheCommand(t *testing.T) {
	testenv.Isolate(t)
	p, b, _, fd := atDomain(t)
	for _, body := range []map[string]any{
		{"domain": "example.test", "provider": "infomaniak", "token": dnsToken},
		{"domain": "example.test", "provider": "rfc2136", "server": "192.0.2.53", "keyName": "homerslab", "secret": "x"},
		{"domain": "example.test", "provider": "infomaniak", "apiKey": "x"},
	} {
		r := b.answer("domain", body)
		var e struct{ Error, Command string }
		r.json(t, &e)
		if r.Code != http.StatusBadRequest || e.Error != "secret-over-http" || e.Command != "sudo homerslab setup secret dns" {
			t.Errorf("PUT domain %v: %d %s, want 400 secret-over-http with the command", body, r.Code, r.Body)
		}
	}
	if s := b.state(); stepState(s, "domain") != "todo" || fd.count() != 0 {
		t.Errorf("a refused token moved setup on: domain %s, issued %d", stepState(s, "domain"), fd.count())
	}
	assertNoSecretAt(t, p, dnsToken)
}

// The domain and the DNS token are given once: the domain on the LAN page, the token in this machine's terminal. The
// token never leaves the primary and is never in the database. Issuing the certificate ends setup's first half: mesh
// connected, certificate issued.
func TestTheDomainIsGivenOnceAndItsCertificateEndsTheFirstHalf(t *testing.T) {
	testenv.Isolate(t)
	p, b, nb, fd := atDomain(t)

	if r := p.secret("dns", dnsToken); r.Code != http.StatusConflict || r.errorCode(t) != "not-waiting" {
		t.Errorf("a DNS token before the domain: %d %s, want 409 not-waiting", r.Code, r.Body)
	}
	for _, bad := range []map[string]string{{"domain": "localhost", "provider": "infomaniak"}, {"domain": "example.test", "provider": "route53"},
		{"domain": "*.example.test", "provider": "infomaniak"}, {"domain": "example.test", "provider": "rfc2136", "server": "", "keyName": "x y"}} {
		if r := b.answer("domain", bad); r.Code != http.StatusBadRequest {
			t.Errorf("PUT domain %v: %d, want 400", bad, r.Code)
		}
	}
	s := b.mustAnswer("domain", infomaniak)
	if stepState(s, "domain") != "waiting" || s.Half != "lan" {
		t.Fatalf("after the domain: domain %s, half %s", stepState(s, "domain"), s.Half)
	}
	if r := b.do("GET", "/api/v1/setup", nil); !strings.Contains(string(r.Body), `"command":"sudo homerslab setup secret dns"`) {
		t.Errorf("the page isn't told the command: %s", r.Body)
	}
	if r := b.answer("mail", nil); r.Code != http.StatusConflict {
		t.Errorf("mail before the certificate: %d, want 409", r.Code)
	}

	if r := p.secret("dns", otherToken); r.Code != http.StatusBadRequest || r.errorCode(t) != "secret-refused" {
		t.Errorf("a token the provider refuses: %d %s, want 400 secret-refused", r.Code, r.Body)
	}
	if got := p.unseal(t, "manager-dns-token"); got != "" {
		t.Error("a refused token was sealed")
	}

	r := p.secret("dns", dnsToken+"\n")
	if r.Code != http.StatusOK {
		t.Fatalf("the DNS token from the terminal: %d %s", r.Code, r.Body)
	}
	if strings.Contains(string(r.Body), dnsToken) || strings.Contains(string(r.Body), "PRIVATE KEY") {
		t.Error("the answer carries a secret")
	}
	s = b.state()
	if stepState(s, "domain") != "done" || s.Answers["domain"] != "example.test" || s.Answers["dnsZone"] != "example.test." {
		t.Errorf("after the token: domain %s, answers %v", stepState(s, "domain"), s.Answers)
	}
	if fd.count() != 1 || fd.last.Contact != "ines@ferreira.family" || fd.last.Domain != "example.test" {
		t.Errorf("issued %d, for %+v", fd.count(), fd.last)
	}
	if got := p.unseal(t, "manager-dns-token"); got != dnsToken {
		t.Error("the DNS token isn't sealed by the agent")
	}
	key := p.unseal(t, "manager-tls-key")
	if !strings.Contains(key, "PRIVATE KEY") || p.unseal(t, "manager-acme-account") == "" {
		t.Error("the certificate's key or the ACME account's key isn't sealed")
	}
	assertNoSecretAt(t, p, dnsToken, strings.Split(key, "\n")[1])

	// On the primary's edge, Admin at its name, at the household's port: NetBird's cloud's (ADR 0021).
	e := p.edge(t)
	if !e.Running || e.Admin != "admin.example.test" || e.Port != 9443 || len(e.Certificates) != 1 || e.Certificates[0].Domain != "example.test" {
		t.Errorf("the edge: %+v", e)
	}
	if nb.port != 9443 || !slices.Equal(nb.names, []string{"example.test 100.88.7.186"}) {
		t.Errorf("the mesh: policies for port %d, names %v; want 9443 and the domain at the primary", nb.port, nb.names)
	}

	b.mustAnswer("mail", nil)
	if s := b.mustAnswer("kit", nil); s.Half != "mesh" || s.Current != "join" {
		t.Errorf("after the kit: half %s at %s, want the mesh half", s.Half, s.Current)
	}
	if r := p.secret("dns", dnsToken); r.Code != http.StatusConflict {
		t.Errorf("a DNS token once the step is done: %d, want 409", r.Code)
	}
}

// The domain step reads the zone's CAA records and says plainly when the authority isn't allowed (#80).
func TestCAARecordsThatForbidTheAuthorityAreSaidPlainly(t *testing.T) {
	testenv.Isolate(t)
	p, b, _, fd := atDomain(t)
	fd.caa = &certs.CAAForbids{Domain: "example.test", Authority: "Let's Encrypt", Allowed: []string{"sectigo.com"}}
	b.mustAnswer("domain", infomaniak)
	r := p.secret("dns", dnsToken)
	var e struct {
		Error     string
		Authority string
		Allowed   []string
	}
	r.json(t, &e)
	if r.Code != http.StatusBadRequest || e.Error != "caa-forbids" || !slices.Equal(e.Allowed, []string{"sectigo.com"}) || e.Authority != "Let's Encrypt" {
		t.Errorf("CAA for another authority: %d %s", r.Code, r.Body)
	}
	if fd.count() != 0 || stepState(b.state(), "domain") != "waiting" {
		t.Error("issued despite the CAA records")
	}
	if r := p.secret("dns", dnsToken); r.Code != http.StatusBadRequest {
		t.Errorf("again: %d", r.Code)
	}
	fd.caa = nil
	if r := p.secret("dns", dnsToken); r.Code != http.StatusOK {
		t.Errorf("once the CAA records allow it: %d %s", r.Code, r.Body)
	}
	if r := p.secret("dns", dnsToken); r.Code != http.StatusConflict {
		t.Errorf("after: %d", r.Code)
	}
	if b.answer("domain", map[string]string{"domain": "nowhere.test", "provider": "infomaniak"}).Code != http.StatusBadRequest {
		t.Error("the domain changed after its certificate: renaming it is a migration of its own")
	}
}

func TestADomainNoPublicZoneHoldsIsNamed(t *testing.T) {
	testenv.Isolate(t)
	p, b, _, _ := atDomain(t)
	b.mustAnswer("domain", map[string]string{"domain": "nowhere.test", "provider": "infomaniak"})
	if r := p.secret("dns", dnsToken); r.Code != http.StatusBadRequest || r.errorCode(t) != "no-zone" {
		t.Errorf("a domain no public zone holds: %d %s", r.Code, r.Body)
	}
}

func (p *primary) warnings(t *testing.T) []struct {
	Source, What string
	Resolved     time.Time
} {
	t.Helper()
	r := p.control("GET", "/api/v1/warnings", nil)
	var ws []struct {
		Source, What string
		Resolved     time.Time
	}
	r.json(t, &ws)
	return ws
}

// The scheduler renews the certificate once a third of its lifetime is left. The first renewal that fails raises a
// warning, long before the certificate expires; `homerslab dns token` replaces the token from the console, and the
// renewal runs right away (#80).
func TestRenewalAtAThirdAndAWarningOnTheFirstFailure(t *testing.T) {
	testenv.Isolate(t)
	p, b, _, fd := atDomain(t)
	throughDomain(t, p, b)
	ctx := context.Background()
	first := p.edge(t).Certificates[0].Serial

	p.clock.Add(60*24*time.Hour - time.Minute) // a minute before a third of 90 days is left
	p.m.RunDue(ctx)
	if fd.count() != 1 {
		t.Fatalf("renewed %d times before a third of the lifetime was left", fd.count()-1)
	}
	p.clock.Add(time.Minute)
	p.m.RunDue(ctx)
	if fd.count() != 2 || p.edge(t).Certificates[0].Serial == first {
		t.Fatalf("at a third of the lifetime left: issued %d, the edge's serial %s", fd.count(), p.edge(t).Certificates[0].Serial)
	}

	// The provider makes everyone rotate their tokens: HomersLab's stops working.
	fd.mu.Lock()
	fd.token = otherToken
	fd.mu.Unlock()
	p.clock.Add(60 * 24 * time.Hour)
	p.m.RunDue(ctx)
	ws := p.warnings(t)
	if len(ws) != 1 || ws[0].Source != "certificate:example.test" || !ws[0].Resolved.IsZero() ||
		!strings.Contains(ws[0].What, "sudo homerslab dns token") || !strings.Contains(ws[0].What, "*.example.test") {
		t.Fatalf("after the first failed renewal: %+v", ws)
	}
	p.clock.Add(time.Hour)
	p.m.RunDue(ctx)
	if ws := p.warnings(t); len(ws) != 1 {
		t.Errorf("a second failure raised another warning: %+v", ws)
	}
	r := p.control("GET", "/api/v1/dns", nil)
	if !strings.Contains(string(r.Body), `"failures":2`) || strings.Contains(string(r.Body), dnsToken) {
		t.Errorf("GET /api/v1/dns: %s", r.Body)
	}

	if r := p.control("PUT", "/api/v1/dns/token", map[string]string{"value": dnsToken}); r.Code != http.StatusBadRequest || r.errorCode(t) != "secret-refused" {
		t.Errorf("the old token from the console: %d %s", r.Code, r.Body)
	}
	r = p.control("PUT", "/api/v1/dns/token", map[string]string{"value": otherToken})
	var got struct{ Renewed bool }
	r.json(t, &got)
	if r.Code != http.StatusOK || !got.Renewed || fd.count() != 3 {
		t.Fatalf("the new token from the console: %d %s, issued %d", r.Code, r.Body, fd.count())
	}
	if ws := p.warnings(t); len(ws) != 1 || ws[0].Resolved.IsZero() {
		t.Errorf("the warning after the renewal: %+v", ws)
	}
	if p.unseal(t, "manager-dns-token") != otherToken {
		t.Error("the new token isn't the sealed one")
	}
	assertNoSecretAt(t, p, dnsToken, otherToken)
}

func TestTheConsoleTokenWantsADomainFirst(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	if r := p.control("PUT", "/api/v1/dns/token", map[string]string{"value": dnsToken}); r.Code != http.StatusConflict || r.errorCode(t) != "no-domain" {
		t.Errorf("before first-time setup's domain: %d %s", r.Code, r.Body)
	}
	if r := p.control("GET", "/api/v1/dns", nil); r.Code != http.StatusConflict {
		t.Errorf("GET /api/v1/dns: %d", r.Code)
	}
}

// A restore puts back the certificate while it is still valid, and issues none: restores and rehearsals never use up
// the authority's limits (#80). The archive brings the database and the certificate's key back (#31); the token is given
// again from the console.
func TestARestorePutsBackTheCertificateAndIssuesNothing(t *testing.T) {
	testenv.Isolate(t)
	p, b, _, fd := atDomain(t)
	throughDomain(t, p, b)
	serial := p.edge(t).Certificates[0].Serial
	archived := map[string]string{}
	for _, name := range []string{"manager-tls-key", "manager-acme-account", "manager-mesh-token"} {
		archived[name] = p.unseal(t, name)
	}
	if err := p.m.Close(); err != nil {
		t.Fatal(err)
	}

	// New hardware: a fresh agent, which joins the mesh, and the database and the credentials from the archive.
	restored := newPrimary(t, withNetBird(p.netbird.(*fakeNetBird)), withDomains(fd))
	restored.clock.now = p.clock.Now().Add(10 * 24 * time.Hour)
	if err := restored.m.Close(); err != nil {
		t.Fatal(err)
	}
	copyFile(t, p.db, restored.db)
	for name, value := range archived {
		c := struct{ name, value string }{name, value}
		if r := serveAgentBody(t, restored.agent, "PUT", "/v1/credentials/"+c.name, map[string]string{"value": c.value}); r.Code != http.StatusNoContent {
			t.Fatalf("putting back %s: %d", c.name, r.Code)
		}
	}
	if r := serveAgentBody(t, restored.agent, "POST", "/v1/mesh/join", agent.JoinRequest{Mesh: "netbird", ManagementURL: "https://api.netbird.io",
		SetupKey: "JOINKEY-0000-4000-8000-000000000001", Hostname: "study-pc"}); r.Code != http.StatusOK {
		t.Fatalf("the new machine's join: %d %s", r.Code, r.Body)
	}
	restored.open()
	if r := restored.control("PUT", "/api/v1/dns/token", map[string]string{"value": dnsToken}); r.Code != http.StatusOK {
		t.Fatalf("the token on the restored primary: %d %s", r.Code, r.Body)
	}
	if fd.count() != 1 {
		t.Errorf("the restore issued %d certificates", fd.count()-1)
	}
	if e := restored.edge(t); !e.Running || len(e.Certificates) != 1 || e.Certificates[0].Serial != serial {
		t.Errorf("the restored edge: %+v, want serial %s", e, serial)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	for _, suffix := range []string{"", "-wal"} {
		b, err := os.ReadFile(from + suffix)
		if err != nil {
			continue
		}
		if err := os.WriteFile(to+suffix, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Remove(to + "-shm")
}

// The household's port is chosen once, at the network step: 443 on a self-hosted NetBird, NetBird's cloud's TLS port on
// the cloud, even before anything is public (ADR 0021, #92).
func TestTheHouseholdsPortIsChosenAtTheNetworkStep(t *testing.T) {
	testenv.Isolate(t)
	p, b, nb, _ := atDomain(t)
	if s := b.state(); s.Answers["householdPort"] != "9443" || nb.port != 9443 {
		t.Errorf("on NetBird's cloud: port %q, policies for %d", s.Answers["householdPort"], nb.port)
	}
	_ = p

	self := &fakeNetBird{}
	q, c := atNetwork(t, self)
	c.mustAnswer("network", map[string]string{"mesh": "netbird", "controlPlane": "self-hosted", "url": "https://netbird.ferreira.family"})
	if r := q.secret("mesh", givenToken); r.Code != http.StatusOK {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if s := c.state(); s.Answers["householdPort"] != "443" || self.port != 443 {
		t.Errorf("on a self-hosted NetBird: port %q, policies for %d", s.Answers["householdPort"], self.port)
	}
}

// Admin answers at its own name only, through the edge on its socket; any other name gets 421.
func TestAdminAnswersAtItsNameOnly(t *testing.T) {
	testenv.Isolate(t)
	p, b, _, _ := atDomain(t, func(p *primary) {
		p.web = fstest.MapFS{"admin.html": {Data: []byte("<!doctype html><title>Admin</title>")}}
	})
	get := func(host string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "https://"+host+"/", nil)
		req.RemoteAddr = "@"
		rec := httptest.NewRecorder()
		p.m.Admin().ServeHTTP(rec, req)
		return rec
	}
	if rec := get("admin.example.test:9443"); rec.Code != http.StatusMisdirectedRequest {
		t.Errorf("before the domain: %d", rec.Code)
	}
	throughDomain(t, p, b)
	// Signed out, Admin sends the browser to the household account's sign-in (#17).
	if rec := get("admin.example.test:9443"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/auth/sign-in" {
		t.Errorf("Admin at its name: %d %s", rec.Code, rec.Header())
	}
	for _, host := range []string{"photos.example.test", "admin.other.test", "192.168.1.20"} {
		if rec := get(host); rec.Code != http.StatusMisdirectedRequest {
			t.Errorf("Admin at %s: %d, want 421", host, rec.Code)
		}
	}
}

// serveAgentBody is one request with a JSON body to an agent's socket.
func serveAgentBody(t *testing.T, sock, method, path string, body any) response {
	t.Helper()
	b, _ := json.Marshal(body)
	c := agentHTTP(sock)
	req, _ := http.NewRequest(method, "http://agent"+path, strings.NewReader(string(b)))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return response{resp.StatusCode, resp.Header, out}
}

func agentHTTP(sock string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
}

// A renewal that was issued but didn't reach the edge is put on the edge again at the next check, not left until the
// old certificate expires.
func TestARenewalThatDidntReachTheEdgeIsPutThereAgain(t *testing.T) {
	testenv.Isolate(t)
	p, b, _, fd := atDomain(t)
	throughDomain(t, p, b)
	ctx := context.Background()
	old := p.edge(t).Certificates[0].Serial
	p.clock.Add(61 * 24 * time.Hour)
	p.host.Fail("systemctl restart homerslab-edge.service")
	p.m.RunDue(ctx)
	if fd.count() != 2 || p.edge(t).Certificates[0].Serial != old {
		t.Fatalf("issued %d; the edge holds %s", fd.count(), p.edge(t).Certificates[0].Serial)
	}
	if ws := p.warnings(t); len(ws) != 1 {
		t.Errorf("a renewal that didn't reach the edge raised %d warnings", len(ws))
	}
	p.host.Mend()
	p.m.RunDue(ctx)
	if fd.count() != 2 {
		t.Errorf("the next check issued again: %d", fd.count())
	}
	if got := p.edge(t).Certificates[0].Serial; got == old {
		t.Error("the renewed certificate never reached the edge")
	}
	if ws := p.warnings(t); len(ws) != 1 || ws[0].Resolved.IsZero() {
		t.Errorf("the warning: %+v", ws)
	}
}

// An agent that doesn't answer for a moment never costs the household its ACME account or a certificate: a key that
// can't be read isn't taken for a key that isn't there (#80).
func TestAnAgentThatDoesntAnswerCostsNoAccountAndNoCertificate(t *testing.T) {
	testenv.Isolate(t)
	p, b, _, fd := atDomain(t)
	throughDomain(t, p, b)
	account := p.unseal(t, "manager-acme-account")
	p.clock.Add(61 * 24 * time.Hour)
	p.host.Fail("systemd-creds decrypt")
	p.m.RunDue(context.Background())
	if r := p.control("PUT", "/api/v1/dns/token", map[string]string{"value": dnsToken}); r.Code == http.StatusOK {
		t.Errorf("the console's token with the agent failing: %d", r.Code)
	}
	p.host.Mend()
	if fd.count() != 1 {
		t.Errorf("issued %d certificates while the agent couldn't unseal", fd.count()-1)
	}
	if got := p.unseal(t, "manager-acme-account"); got != account {
		t.Error("the ACME account's key was replaced")
	}
}
