package manager_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/mesh"
	"github.com/ArjunRachithcf/homerslab/internal/mesh/netbird"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// The tokens in these tests: the one the admin brings from NetBird's dashboard, and the ones NetBird makes for
// HomersLab's own service user.
const (
	givenToken = "nbp_GIVEN0000000000000000000000000000000"
	wrongToken = "nbp_WRONG0000000000000000000000000000000"
)

// fakeNetBird stands in for NetBird's management API at the manager's seam; the netbird package itself passes the
// mesh contract suite against a real NetBird (internal/mesh/netbird).
type fakeNetBird struct {
	mu            sync.Mutex
	now           func() time.Time
	setupRequired bool
	bootstrapped  []netbird.Owner
	adopted       []string
	issued        int
	revoked       []string
	ensured       int
	keys          int
}

func (f *fakeNetBird) token() mesh.Token {
	f.issued++
	n := string(rune('0' + f.issued))
	return mesh.Token{Value: mesh.Secret("nbp_OWN" + n + strings.Repeat("x", 33)), UserID: "svc", ID: "pat-" + n,
		ExpiresAt: f.now().Add(mesh.MaxTokenLife)}
}

func (f *fakeNetBird) Probe(_ context.Context, url string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if url == "https://nothing.example.org" {
		return false, mesh.ErrUnreachable
	}
	return f.setupRequired, nil
}

func (f *fakeNetBird) Bootstrap(_ context.Context, _ string, o netbird.Owner) (netbird.Bootstrapped, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.setupRequired {
		return netbird.Bootstrapped{}, mesh.ErrAlreadySetUp
	}
	f.setupRequired = false
	f.bootstrapped = append(f.bootstrapped, o)
	return netbird.Bootstrapped{Token: f.token(), OwnerPassword: "owner-PASSWORD-generated-Aa1"}, nil
}

func (f *fakeNetBird) Adopt(_ context.Context, _ string, given mesh.Secret) (mesh.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if given.Reveal() != givenToken {
		return mesh.Token{}, mesh.ErrTokenRefused
	}
	f.adopted = append(f.adopted, given.Reveal())
	return f.token(), nil
}

func (f *fakeNetBird) Open(_ string, t mesh.Token) (mesh.Mesh, error) {
	return &fakeMesh{f: f, t: t}, nil
}

type fakeMesh struct {
	f *fakeNetBird
	t mesh.Token
}

func (m *fakeMesh) Report(context.Context) (mesh.Report, error) {
	return mesh.Report{Mesh: "netbird"}, nil
}
func (m *fakeMesh) EnsurePolicies(context.Context) error {
	m.f.mu.Lock()
	defer m.f.mu.Unlock()
	m.f.ensured++
	return nil
}
func (m *fakeMesh) NewJoinKey(context.Context, string) (mesh.JoinKey, error) {
	m.f.mu.Lock()
	defer m.f.mu.Unlock()
	m.f.keys++
	return mesh.JoinKey{Value: "JOINKEY-0000-4000-8000-000000000001", ID: "k1", ExpiresAt: m.f.now().Add(mesh.JoinKeyLife)}, nil
}
func (m *fakeMesh) Rotate(context.Context) (mesh.Token, error) {
	m.f.mu.Lock()
	defer m.f.mu.Unlock()
	return m.f.token(), nil
}
func (m *fakeMesh) Revoke(_ context.Context, t mesh.Token) error {
	m.f.mu.Lock()
	defer m.f.mu.Unlock()
	m.f.revoked = append(m.f.revoked, t.ID)
	return nil
}
func (m *fakeMesh) Token() mesh.Token           { return m.t }
func (m *fakeMesh) With(t mesh.Token) mesh.Mesh { return &fakeMesh{f: m.f, t: t} }

func withNetBird(f *fakeNetBird) option { return func(p *primary) { p.netbird = f } }

// atNetwork is a primary whose setup has come to its network step, with the steps before it stood in.
func atNetwork(t *testing.T, nb *fakeNetBird) (*primary, *browser) {
	t.Helper()
	p := newPrimary(t, withBuiltSteps("signin"), withNetBird(nb))
	nb.now = p.clock.Now
	b := p.claimed()
	b.firstThree()
	b.mustAnswer("signin", nil)
	return p, b
}

func (p *primary) secret(name, value string) response {
	return p.control("PUT", "/api/v1/setup/secrets/"+name, map[string]string{"value": value})
}

func stepState(s state, id string) string {
	for _, st := range s.Steps {
		if st.ID == id {
			return st.State
		}
	}
	return ""
}

func TestTheLANPageRefusesEverySecretAndPointsToTheCommand(t *testing.T) {
	testenv.Isolate(t)
	nb := &fakeNetBird{}
	p, b := atNetwork(t, nb)
	for _, body := range []map[string]any{
		{"mesh": "netbird", "controlPlane": "cloud", "token": givenToken},
		{"mesh": "netbird", "controlPlane": "cloud", "Token": "x"},
		{"mesh": "netbird", "controlPlane": "self-hosted", "url": "https://netbird.ferreira.family", "apiKey": "x"},
		{"mesh": "netbird", "netbird": map[string]any{"setupKey": "x"}},
		{"mesh": "netbird", "list": []any{map[string]any{"password": "x"}}},
	} {
		r := b.answer("network", body)
		var e struct{ Error, Command string }
		r.json(t, &e)
		if r.Code != http.StatusBadRequest || e.Error != "secret-over-http" || e.Command != "sudo homerslab setup secret mesh" {
			t.Errorf("PUT network %v: %d %s, want 400 secret-over-http with the command", body, r.Code, r.Body)
		}
	}
	// Any step: a secret is refused wherever it is sent.
	if r := b.answer("you", map[string]string{"name": "Inês", "mail": "ines@ferreira.family", "password": "x"}); r.Code != http.StatusBadRequest || r.errorCode(t) != "secret-over-http" {
		t.Errorf("a password on the you step: %d %s", r.Code, r.Body)
	}
	s := b.state()
	if s.Current != "network" || stepState(s, "network") != "todo" || len(nb.adopted) != 0 {
		t.Errorf("a refused secret moved setup on: at %q, network %s, adopted %v", s.Current, stepState(s, "network"), nb.adopted)
	}
	assertNoSecretAt(t, p, givenToken)
}

func TestOnTheCloudTheTokenIsGivenOnceInTheTerminal(t *testing.T) {
	testenv.Isolate(t)
	nb := &fakeNetBird{}
	p, b := atNetwork(t, nb)

	if r := p.secret("mesh", givenToken); r.Code != http.StatusConflict || r.errorCode(t) != "not-waiting" {
		t.Errorf("a token before the network step was answered: %d %s, want 409 not-waiting", r.Code, r.Body)
	}
	s := b.mustAnswer("network", map[string]string{"mesh": "netbird", "controlPlane": "cloud"})
	if s.Current != "network" || stepState(s, "network") != "waiting" {
		t.Fatalf("after choosing NetBird's cloud: at %q, network %q, want it waiting", s.Current, stepState(s, "network"))
	}
	r := b.do("GET", "/api/v1/setup", nil)
	if !bytes.Contains(r.Body, []byte(`"secret":"mesh"`)) || !bytes.Contains(r.Body, []byte(`"command":"sudo homerslab setup secret mesh"`)) {
		t.Errorf("the page isn't told which command to show: %s", r.Body)
	}
	if r := b.do("PUT", "/api/v1/setup/secrets/mesh", map[string]string{"value": givenToken}); r.Code != http.StatusNotFound {
		t.Errorf("the secret over the LAN listener: %d %s, want 404: it's on the control socket only", r.Code, r.Body)
	}

	if r := p.secret("mesh", wrongToken); r.Code != http.StatusBadRequest || r.errorCode(t) != "secret-refused" {
		t.Errorf("a wrong token: %d %s, want 400 secret-refused", r.Code, r.Body)
	}
	r = p.secret("mesh", givenToken+"\n")
	if r.Code != http.StatusOK {
		t.Fatalf("the token from the terminal: %d %s", r.Code, r.Body)
	}
	for _, tok := range []string{givenToken, "nbp_OWN", "JOINKEY"} {
		if bytes.Contains(r.Body, []byte(tok)) {
			t.Errorf("the answer carries a secret (%s…)", tok[:7])
		}
	}
	s = b.state()
	if stepState(s, "network") != "done" || s.Current != "domain" {
		t.Errorf("after the token: network %q, at %q, want done and on to domain", stepState(s, "network"), s.Current)
	}
	if nb.ensured != 1 || nb.keys != 1 || len(nb.adopted) != 1 {
		t.Errorf("policies ensured %d, join keys %d, adopted %d: want 1 each", nb.ensured, nb.keys, len(nb.adopted))
	}
	if s.Answers["primaryMeshIp"] != "100.88.7.186" || s.Answers["meshUrl"] != "https://api.netbird.io" {
		t.Errorf("answers: %v", s.Answers)
	}

	// The primary joined with its one-time key, and its firewall table is on.
	up := p.host.Cmds()[p.host.Ran("netbird up")]
	if !strings.Contains(strings.Join(up.Env, " "), "NB_SETUP_KEY=JOINKEY-") || !strings.Contains(strings.Join(up.Args, " "), "--management-url https://api.netbird.io") {
		t.Errorf("netbird up %v", up.Args)
	}
	if table, on := p.host.Table(); !on || !strings.Contains(table, "tcp dport 7020 accept") {
		t.Errorf("the firewall table (%v):\n%s", on, table)
	}
	// What the agent sealed is HomersLab's own token, never the one the admin gave.
	if got := p.unseal(t, "manager-mesh-token"); !strings.HasPrefix(got, "nbp_OWN") {
		t.Errorf("the sealed mesh token is %.7s…, want HomersLab's own", got)
	}
	assertNoSecretAt(t, p, givenToken, "nbp_OWN", "JOINKEY")
	if r := p.secret("mesh", givenToken); r.Code != http.StatusConflict {
		t.Errorf("a token once the step is done: %d, want 409", r.Code)
	}
}

func TestANewSelfHostedNetBirdIsSetUpInOneStep(t *testing.T) {
	testenv.Isolate(t)
	nb := &fakeNetBird{setupRequired: true}
	p, b := atNetwork(t, nb)
	s := b.mustAnswer("network", map[string]string{"mesh": "netbird", "controlPlane": "self-hosted", "url": "https://netbird.ferreira.family/"})
	if stepState(s, "network") != "done" {
		t.Fatalf("network is %q, want done with no token at all", stepState(s, "network"))
	}
	if len(nb.bootstrapped) != 1 || nb.bootstrapped[0] != (netbird.Owner{Name: "Inês Ferreira", Mail: "ines@ferreira.family"}) {
		t.Errorf("bootstrapped for %v, want the admin", nb.bootstrapped)
	}
	if got := p.unseal(t, "manager-mesh-owner-password"); got != "owner-PASSWORD-generated-Aa1" {
		t.Error("the new NetBird's owner password isn't sealed for the recovery kit")
	}
	if s.Answers["meshUrl"] != "https://netbird.ferreira.family" || s.Answers["meshControlPlane"] != "self-hosted" {
		t.Errorf("answers %v", s.Answers)
	}
	assertNoSecretAt(t, p, "owner-PASSWORD", "nbp_OWN")
}

func TestASelfHostedNetBirdSetUpBeforeWaitsForItsToken(t *testing.T) {
	testenv.Isolate(t)
	nb := &fakeNetBird{}
	p, b := atNetwork(t, nb)
	s := b.mustAnswer("network", map[string]string{"mesh": "netbird", "controlPlane": "self-hosted", "url": "https://netbird.ferreira.family"})
	if stepState(s, "network") != "waiting" {
		t.Fatalf("network is %q, want waiting for the token", stepState(s, "network"))
	}
	if r := p.secret("mesh", givenToken); r.Code != http.StatusOK {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if s := b.state(); s.Answers["meshUrl"] != "https://netbird.ferreira.family" || stepState(s, "network") != "done" {
		t.Errorf("after the token: %v", s.Answers)
	}
}

func TestTheNetworkStepChecksItsAnswer(t *testing.T) {
	testenv.Isolate(t)
	_, b := atNetwork(t, &fakeNetBird{})
	for _, c := range []struct {
		body  map[string]string
		field string
	}{
		{map[string]string{"mesh": "netbird", "controlPlane": "self-hosted", "url": "http://netbird.ferreira.family"}, "url"},
		{map[string]string{"mesh": "netbird", "controlPlane": "self-hosted", "url": "https://nothing.example.org"}, "url"},
		{map[string]string{"mesh": "netbird", "controlPlane": "self-hosted", "url": ""}, "url"},
		{map[string]string{"mesh": "netbird", "controlPlane": "moon"}, "controlPlane"},
		{map[string]string{"mesh": "zerotier", "controlPlane": "cloud"}, "mesh"},
		{map[string]string{"mesh": "netbird", "controlPlane": "cloud", "extra": "x"}, "answer"},
	} {
		r := b.answer("network", c.body)
		var e struct {
			Error  string
			Fields []string
		}
		r.json(t, &e)
		if r.Code != http.StatusBadRequest || e.Error != "invalid" || len(e.Fields) != 1 || e.Fields[0] != c.field {
			t.Errorf("network %v: %d %s, want 400 invalid %s", c.body, r.Code, r.Body, c.field)
		}
	}
}

func TestTheSchedulerRotatesTheMeshTokenBeforeItExpires(t *testing.T) {
	testenv.Isolate(t)
	nb := &fakeNetBird{}
	p, b := atNetwork(t, nb)
	b.mustAnswer("network", map[string]string{"mesh": "netbird", "controlPlane": "cloud"})
	if r := p.secret("mesh", givenToken); r.Code != http.StatusOK {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	first := p.unseal(t, "manager-mesh-token")
	ctx := context.Background()

	p.clock.Add(mesh.MaxTokenLife - mesh.RotateBefore - 24*time.Hour) // 31 days left
	p.m.RunDue(ctx)
	if got := p.unseal(t, "manager-mesh-token"); got != first || len(nb.revoked) != 0 {
		t.Fatal("the token was rotated with a month still to go")
	}
	p.clock.Add(48 * time.Hour) // 29 days left
	p.m.RunDue(ctx)
	next := p.unseal(t, "manager-mesh-token")
	if next == first || !strings.HasPrefix(next, "nbp_OWN") {
		t.Fatal("the token wasn't rotated before it expired")
	}
	if len(nb.revoked) != 1 || nb.revoked[0] != "pat-1" {
		t.Errorf("revoked %v, want the old token", nb.revoked)
	}
	var s state
	p.control("GET", "/api/v1/setup", nil).json(t, &s) // the page's session ended months ago
	expires, err := time.Parse(time.RFC3339, s.Answers["meshTokenExpires"])
	if err != nil || !expires.Equal(p.clock.Now().Add(mesh.MaxTokenLife)) || s.Answers["meshTokenId"] != "pat-2" {
		t.Errorf("after the rotation the answers say %v", s.Answers)
	}
	p.m.RunDue(ctx)
	if len(nb.revoked) != 1 {
		t.Error("a fresh token was rotated again")
	}
}

func TestTheFirstSignInOverTheMeshClosesTheLANListenerForGood(t *testing.T) {
	testenv.Isolate(t)
	nb := &fakeNetBird{}
	p, b := atNetwork(t, nb)
	b.mustAnswer("network", map[string]string{"mesh": "netbird", "controlPlane": "cloud"})
	p.secret("mesh", givenToken)

	if err := p.m.CloseLANListener(context.Background()); err != nil {
		t.Fatal(err)
	}
	if table, _ := p.host.Table(); strings.Contains(table, "7020") {
		t.Errorf("the firewall table still lets the LAN listener in:\n%s", table)
	}
	check := func(when string) {
		for _, path := range []string{"/setup", "/api/v1/setup", "/api/v1/nodes/primary"} {
			if r := b.do("GET", path, nil); r.Code != http.StatusGone {
				t.Errorf("%s: GET %s on the LAN listener: %d, want 410", when, path, r.Code)
			}
		}
		if r := p.control("POST", "/api/v1/setup/links", nil); r.Code != http.StatusConflict {
			t.Errorf("%s: a new link: %d, want 409", when, r.Code)
		}
	}
	check("closed")
	p.restart()
	check("after a restart")
}

// unseal is a credential as the agent hands it back to the manager.
func (p *primary) unseal(t *testing.T, name string) string {
	t.Helper()
	r := serveAgent(t, p.agent, "GET", "/v1/credentials/"+name)
	var v struct{ Value string }
	r.json(t, &v)
	return v.Value
}

// assertNoSecretAt wants none of the secrets in the manager's database, in the clear.
func assertNoSecretAt(t *testing.T, p *primary, secrets ...string) {
	t.Helper()
	for _, f := range []string{p.db, p.db + "-wal"} {
		b, err := os.ReadFile(f)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range secrets {
			if bytes.Contains(b, []byte(s)) {
				t.Errorf("%s holds a secret (%.7s…)", filepath.Base(f), s)
			}
		}
	}
}

// serveAgent is one request to an agent's socket.
func serveAgent(t *testing.T, sock, method, path string) response {
	t.Helper()
	c := http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	req, _ := http.NewRequest(method, "http://agent"+path, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return response{resp.StatusCode, resp.Header, buf.Bytes()}
}
