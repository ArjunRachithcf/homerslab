package netbird_test

// The mesh contract suite against a real, self-hosted NetBird v0.82.0 in containers (testdata/compose.yaml, compose
// project hltest-15). It needs Docker: it runs when HOMERSLAB_TEST_DOCKER_HOST names the engine to use, and is
// skipped otherwise. The code under test never sees that engine (testenv points DOCKER_HOST at nowhere); only this
// harness does, explicitly.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/ArjunRachithcf/homerslab/internal/host"
	"github.com/ArjunRachithcf/homerslab/internal/mesh"
	"github.com/ArjunRachithcf/homerslab/internal/mesh/meshtest"
	"github.com/ArjunRachithcf/homerslab/internal/mesh/netbird"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

const project = "hltest-15"

// managementURL is the server as the machines on the test's mesh reach it.
const managementURL = "http://nb.hltest15.test:80"

func TestTheMeshContractOnSelfHostedNetBird(t *testing.T) {
	testenv.Isolate(t)
	engine := os.Getenv("HOMERSLAB_TEST_DOCKER_HOST")
	if engine == "" {
		t.Skip("set HOMERSLAB_TEST_DOCKER_HOST to the engine for the contract suite's containers")
	}
	w := up(t, engine)
	ctx := context.Background()

	var own mesh.Token
	t.Run("a self-hosted NetBird is bootstrapped in one step", func(t *testing.T) {
		required, err := netbird.Probe(ctx, w.api)
		if err != nil || !required {
			t.Fatalf("a fresh server: setup required %v, %v", required, err)
		}
		var setupToken mesh.Secret
		var kept []mesh.Token
		keep := func(tk mesh.Token) error { kept = append(kept, tk); return nil }
		o := netbird.Owner{Name: "Inês Ferreira", Mail: "ines@ferreira.family", Password: netbird.NewPassword()}
		b, err := netbird.BootstrapSeeing(ctx, w.api, o, keep, &setupToken)
		if err != nil {
			t.Fatal(err)
		}
		own = b
		if len(kept) != 1 || kept[0].ID != own.ID {
			t.Error("the token wasn't handed to keep before the bootstrap finished")
		}
		if life := time.Until(own.ExpiresAt); life > mesh.MaxTokenLife || life < mesh.MaxTokenLife-48*time.Hour {
			t.Errorf("HomersLab's token lives %v, want 365 days", life)
		}
		users := w.raw(t, own.Value, "GET", "/api/users", nil)
		var list []struct {
			ID, Name, Role string
			IsServiceUser  bool `json:"is_service_user"`
		}
		decode(t, users, &list)
		var owner, svc bool
		for _, u := range list {
			switch {
			case u.IsServiceUser && u.Name == netbird.ServiceUser:
				svc = u.ID == own.UserID && u.Role == "admin"
			case u.Role == "owner":
				owner = true
			}
		}
		if !owner || !svc {
			t.Fatalf("users: owner %v, HomersLab's admin service user with the token %v", owner, svc)
		}
		if setupToken == "" {
			t.Fatal("no setup token was made")
		}
		setup, _ := netbird.Open(w.api, mesh.Token{Value: setupToken})
		if _, err := setup.Report(ctx); !errors.Is(err, mesh.ErrTokenRefused) {
			t.Errorf("the owner's one-day setup token after the bootstrap: %v, want it revoked", err)
		}
		if _, err := netbird.Bootstrap(ctx, w.api, netbird.Owner{Name: "Mallory", Mail: "m@example.org", Password: netbird.NewPassword()}, keep); !errors.Is(err, mesh.ErrAlreadySetUp) {
			t.Errorf("bootstrapping a second time: %v, want ErrAlreadySetUp", err)
		}
	})
	if own.Value == "" {
		t.Fatal("no token: nothing else can be checked")
	}

	t.Run("the admin's token is given once; HomersLab makes its own", func(t *testing.T) {
		// What the admin brings from the dashboard (the cloud's guided step): a token of a service user they made.
		given := w.adminToken(t, own.Value)
		next, err := netbird.Adopt(ctx, w.api, given)
		if err != nil {
			t.Fatal(err)
		}
		if next.UserID != own.UserID || next.ID == own.ID {
			t.Errorf("adopting again: user %s token %s, want the same service user %s with a new token", next.UserID, next.ID, own.UserID)
		}
		old, _ := netbird.Open(w.api, own)
		if _, err := old.Report(ctx); !errors.Is(err, mesh.ErrTokenRefused) {
			t.Errorf("HomersLab's earlier token after adopting again: %v, want it revoked", err)
		}
		if _, err := netbird.Adopt(ctx, w.api, "nbp_not-a-real-token-at-all-000000000000"); !errors.Is(err, mesh.ErrTokenRefused) {
			t.Errorf("a wrong token: %v, want ErrTokenRefused", err)
		}
		own = next
	})

	m, err := netbird.Open(w.api, own)
	if err != nil {
		t.Fatal(err)
	}
	w.m = m
	t.Run("the household's own policies that reach every peer are found, and switched off only when asked", func(t *testing.T) {
		tok := m.Token().Value
		var groups []struct{ ID, Name string }
		decode(t, w.raw(t, tok, "GET", "/api/groups", nil), &groups)
		var all string
		for _, g := range groups {
			if g.Name == "All" {
				all = g.ID
			}
		}
		var made struct{ ID string }
		decode(t, w.raw(t, tok, "POST", "/api/policies", map[string]any{"name": "ssh-for-everyone", "enabled": true,
			"rules": []map[string]any{{"name": "ssh", "enabled": true, "action": "accept", "bidirectional": false, "protocol": "tcp",
				"ports": []string{"22"}, "sources": []string{all}, "destinations": []string{all}}}}), &made)
		c, err := m.Conflicts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, p := range c {
			names = append(names, p.Name)
		}
		if !slices.Contains(names, "ssh-for-everyone") || !slices.Contains(names, "Default") {
			t.Errorf("conflicts: %v, want the household's policy and NetBird's Default", names)
		}
		var ids []string
		for _, p := range c {
			ids = append(ids, p.ID)
		}
		if err := m.Disable(ctx, ids); err != nil {
			t.Fatal(err)
		}
		if c, _ := m.Conflicts(ctx); len(c) != 0 {
			t.Errorf("after switching them off: %v", c)
		}
		var p struct{ Enabled bool }
		decode(t, w.raw(t, tok, "GET", "/api/policies/"+made.ID, nil), &p)
		if p.Enabled {
			t.Error("the household's policy is still on")
		}
	})

	w.m = meshtest.Run(t, w, meshtest.Machines{Primary: "primary", Second: "resolved", Device: "phone"}, mesh.Report{
		Mesh: "netbird", ControlPlane: "self-hosted", Version: "0.82.0", PerAppNames: "wildcard-records",
		PublicPath: "netbird-reverse-proxy", PublicPathReady: false, DomainRequired: true, PortMappingOff: true,
	})

	t.Run("policies that drifted are put back, and a new Default goes again", func(t *testing.T) {
		tok := w.m.Token().Value
		var groups []struct{ ID, Name string }
		decode(t, w.raw(t, tok, "GET", "/api/groups", nil), &groups)
		ids := map[string]string{}
		for _, g := range groups {
			ids[g.Name] = g.ID
		}
		allToAll := map[string]any{"name": "Default", "enabled": true, "rules": []map[string]any{{"name": "Default", "enabled": true,
			"action": "accept", "bidirectional": true, "protocol": "all", "sources": []string{ids["All"]}, "destinations": []string{ids["All"]}}}}
		w.raw(t, tok, "POST", "/api/policies", allToAll)
		var policies []struct{ ID, Name string }
		decode(t, w.raw(t, tok, "GET", "/api/policies", nil), &policies)
		for _, p := range policies {
			if p.Name == "homerslab-kids-to-nodes" {
				w.raw(t, tok, "PUT", "/api/policies/"+p.ID, map[string]any{"name": p.Name, "enabled": true, "rules": []map[string]any{{
					"name": p.Name, "enabled": true, "action": "accept", "bidirectional": true, "protocol": "tcp", "ports": []string{"22", "443"},
					"sources": []string{ids[mesh.RoleGroup(mesh.Kid)]}, "destinations": []string{ids[mesh.NodesGroup]}}}})
			}
		}
		if err := w.m.EnsurePolicies(ctx); err != nil {
			t.Fatal(err)
		}
		have := w.Policies(t)
		if _, ok := have["Default"]; ok {
			t.Error("a Default policy made again stayed")
		}
		if got := have["homerslab-kids-to-nodes"]; len(got.Ports) != 2 || got.Ports[0] != 80 || got.Ports[1] != 443 {
			t.Errorf("the kids' policy after the check: %+v", got)
		}
	})

	t.Run("each node's resolver was the kind it should be", func(t *testing.T) {
		// So the resolver check above covered both: NetBird beside systemd-resolved (Fedora, Ubuntu, Arch), and
		// NetBird rewriting a plain resolv.conf.
		for m, want := range map[meshtest.Machine]string{"resolved": "System DNS manager discovered: systemd", "primary": "System DNS manager discovered: file"} {
			out, _ := w.docker(nil, "logs", container(m)).CombinedOutput()
			if !bytes.Contains(out, []byte(want)) {
				t.Errorf("%s's client didn't log %q", m, want)
			}
		}
		if out, err := w.sh(t, "resolved", "resolvectl dns"); err != nil || !strings.Contains(out, "10.215.0.53") {
			t.Errorf("systemd-resolved lost the machine's own DNS server: %v %s", err, out)
		}
	})
}

// world is the compose project: the server, reached from the test at api, and its machines.
type world struct {
	engine string
	dir    string
	api    string
	m      mesh.Mesh
	keys   map[meshtest.Machine]string
}

func up(t *testing.T, engine string) *world {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	w := &world{engine: engine, dir: filepath.Join(filepath.Dir(self), "testdata"), keys: map[meshtest.Machine]string{}}
	env := []string{"HLTEST_RELAY_SECRET=" + strings.TrimRight(random(), "="), "HLTEST_COOKIE_KEY=" + random(),
		"HLTEST_STORE_KEY=" + random(), "HLTEST_NAT_MAPPER_OFF=" + strings.TrimPrefix(netbird.ServiceEnv[0], "NB_DISABLE_NAT_MAPPER=")}
	w.compose(t, env, "down", "-v", "--remove-orphans") // a leftover from an interrupted run
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := w.composeOut(env, "logs", "--no-color", "--tail", "60")
			t.Logf("the containers' logs:\n%s", out)
		}
		w.compose(t, env, "down", "-v", "--remove-orphans")
	})
	w.compose(t, env, "up", "-d", "--build", "--quiet-pull", "--wait")
	port := strings.TrimSpace(w.compose(t, env, "port", "server", "80"))
	ap, err := netip.ParseAddrPort(port)
	if err != nil || !ap.Addr().IsLoopback() {
		t.Fatalf("the server's port %q isn't on loopback", port)
	}
	w.api = "http://" + port
	for range 100 {
		if _, err := netbird.Probe(context.Background(), w.api); err == nil {
			return w
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the NetBird server never answered")
	return nil
}

func random() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

// docker runs the docker CLI on the contract suite's engine, and only there.
func (w *world) docker(env []string, args ...string) *exec.Cmd {
	cmd := exec.Command("docker", args...)
	cmd.Env = append(os.Environ(), "DOCKER_HOST="+w.engine)
	cmd.Env = append(cmd.Env, env...)
	cmd.Dir = w.dir
	return cmd
}

func (w *world) composeOut(env []string, args ...string) (string, error) {
	cmd := w.docker(env, append([]string{"compose", "-p", project, "-f", "compose.yaml"}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (w *world) compose(t *testing.T, env []string, args ...string) string {
	t.Helper()
	out, err := w.composeOut(env, args...)
	if err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func container(m meshtest.Machine) string { return project + "-" + string(m) + "-1" }

// in is a host.Runner inside a machine: the agent's own commands, run there.
func (w *world) in(m meshtest.Machine) host.Runner {
	return func(ctx context.Context, c host.Cmd) ([]byte, error) {
		args := []string{"exec", "-i"}
		for _, e := range c.Env {
			args = append(args, "-e", e)
		}
		args = append(append(args, container(m), c.Name), c.Args...)
		cmd := w.docker(nil, args...)
		if c.Stdin != nil {
			cmd.Stdin = bytes.NewReader(c.Stdin)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return out, fmt.Errorf("%s in %s: %v: %s", c, m, err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
}

func (w *world) sh(t *testing.T, m meshtest.Machine, script string) (string, error) {
	t.Helper()
	out, err := w.in(m)(context.Background(), host.Cmd{Name: "sh", Args: []string{"-c", script}})
	return string(out), err
}

// raw is a call to the management API outside the adapter, for what the suite checks or sets up by hand.
func (w *world) raw(t *testing.T, tok mesh.Secret, method, path string, body any) []byte {
	t.Helper()
	var r *bytes.Reader = bytes.NewReader(nil)
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, w.api+path, r)
	req.Header.Set("Authorization", "Token "+tok.Reveal())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("%s %s: %d", method, path, resp.StatusCode) // never the body: it may carry a token's hash
	}
	return buf.Bytes()
}

func decode(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%v in %.200s", err, b)
	}
}

// adminToken stands in for the admin's dashboard visit: a service user of their own, and its token.
func (w *world) adminToken(t *testing.T, tok mesh.Secret) mesh.Secret {
	t.Helper()
	var u struct{ ID string }
	decode(t, w.raw(t, tok, "POST", "/api/users", map[string]any{"name": "ines-for-homerslab", "role": "admin", "auto_groups": []string{}, "is_service_user": true}), &u)
	var p struct {
		Plain string `json:"plain_token"`
	}
	decode(t, w.raw(t, tok, "POST", "/api/users/"+u.ID+"/tokens", map[string]any{"name": "for-homerslab", "expires_in": 1}), &p)
	return mesh.Secret(p.Plain)
}

func (w *world) Mesh() mesh.Mesh { return w.m }

// Join is the agent's join, run in the machine: netbird up with the key in the environment, then its status.
func (w *world) Join(t *testing.T, m meshtest.Machine, k mesh.JoinKey) (netip.Addr, error) {
	t.Helper()
	n := netbird.Node{Run: w.in(m)}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := n.Up(ctx, managementURL, string(m), k.Value); err != nil {
		return netip.Addr{}, err
	}
	for range 50 {
		if s, err := n.Status(ctx); err == nil && s.Connected && s.IP.IsValid() {
			return s.IP, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return netip.Addr{}, errors.New("never connected")
}

func (w *world) JoinDevice(t *testing.T, m meshtest.Machine, r mesh.Role) netip.Addr {
	t.Helper()
	var groups []struct{ ID, Name string }
	decode(t, w.raw(t, w.m.Token().Value, "GET", "/api/groups", nil), &groups)
	var id string
	for _, g := range groups {
		if g.Name == mesh.RoleGroup(r) {
			id = g.ID
		}
	}
	var k struct{ Key string }
	decode(t, w.raw(t, w.m.Token().Value, "POST", "/api/setup-keys", map[string]any{"name": "device-" + string(m), "type": "one-off",
		"expires_in": 86400, "auto_groups": []string{id}, "usage_limit": 1, "ephemeral": false}), &k)
	a, err := w.Join(t, m, mesh.JoinKey{Value: mesh.Secret(k.Key)})
	if err != nil {
		t.Fatalf("the device didn't join: %v", err)
	}
	return a
}

type peer struct {
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	DNSLabel string `json:"dns_label"`
	Groups   []struct{ Name string }
}

func (w *world) peer(t *testing.T, m meshtest.Machine) peer {
	t.Helper()
	var peers []peer
	decode(t, w.raw(t, w.m.Token().Value, "GET", "/api/peers", nil), &peers)
	for _, p := range peers {
		if p.Name == string(m) || p.Hostname == string(m) {
			return p
		}
	}
	t.Fatalf("%s isn't a peer", m)
	return peer{}
}

func (w *world) Groups(t *testing.T, m meshtest.Machine) []string {
	var out []string
	for _, g := range w.peer(t, m).Groups {
		out = append(out, g.Name)
	}
	return out
}

func (w *world) MeshName(t *testing.T, m meshtest.Machine) string { return w.peer(t, m).DNSLabel }

func (w *world) OutsideName() string { return "outside.hltest15.test" }

func (w *world) Policies(t *testing.T) map[string]mesh.Rule {
	t.Helper()
	var list []struct {
		Name    string
		Enabled bool
		Rules   []struct {
			Protocol     string
			Action       string
			Ports        []string
			Sources      []struct{ Name string }
			Destinations []struct{ Name string }
		}
	}
	decode(t, w.raw(t, w.m.Token().Value, "GET", "/api/policies", nil), &list)
	out := map[string]mesh.Rule{}
	for _, p := range list {
		if !p.Enabled {
			continue // switched off: it lets nothing through
		}
		r := mesh.Rule{Name: p.Name}
		if len(p.Rules) == 1 && p.Rules[0].Protocol == "tcp" && p.Rules[0].Action == "accept" &&
			len(p.Rules[0].Sources) == 1 && len(p.Rules[0].Destinations) == 1 {
			r.From, r.To = p.Rules[0].Sources[0].Name, p.Rules[0].Destinations[0].Name
			for _, s := range p.Rules[0].Ports {
				var n int
				_, _ = fmt.Sscan(s, &n)
				r.Ports = append(r.Ports, n)
			}
		}
		out[p.Name] = r
	}
	return out
}

func (w *world) Listen(t *testing.T, m meshtest.Machine, port int) {
	t.Helper()
	cmd := w.docker(nil, "exec", "-d", container(m), "sh", "-c", fmt.Sprintf("while true; do echo hello | nc -l -p %d; done", port))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("listening on %s:%d: %v %s", m, port, err, out)
	}
}

func (w *world) Reaches(t *testing.T, from meshtest.Machine, to netip.Addr, port int) bool {
	t.Helper()
	for range 6 { // a fresh peer's ACL takes a moment
		if _, err := w.sh(t, from, fmt.Sprintf("nc -z -w 2 %s %d", to, port)); err == nil {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

// AskedForAPort reads the client's log: NetBird logs "port forwarding setup" when its NAT mapper runs, and "NAT
// port mapper disabled via NB_DISABLE_NAT_MAPPER" when it doesn't (client/internal/portforward, v0.82.0).
func (w *world) AskedForAPort(t *testing.T, m meshtest.Machine) bool {
	t.Helper()
	out, err := w.docker(nil, "logs", container(m)).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	asked := bytes.Contains(out, []byte("port forwarding setup")) || bytes.Contains(out, []byte("created port mapping"))
	off := bytes.Contains(out, []byte("NAT port mapper disabled via NB_DISABLE_NAT_MAPPER"))
	if asked == off {
		t.Fatalf("%s's log says neither or both of mapping and not mapping", m)
	}
	return asked
}

func (w *world) Resolves(t *testing.T, m meshtest.Machine, name string) bool {
	t.Helper()
	for range 20 { // the client applies its DNS a moment after connecting
		if out, err := w.sh(t, m, "getent hosts "+name); err == nil && strings.TrimSpace(out) != "" {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}
