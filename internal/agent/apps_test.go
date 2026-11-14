package agent_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/catalogs"
	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/appunit"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/engine/compose"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// bentoPDF is the official catalog's BentoPDF as its install plan hands it to the agent.
func bentoPDF(t *testing.T) agent.AppRequest {
	t.Helper()
	file, err := fs.ReadFile(catalogs.Official(), "apps/bentopdf/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return agent.AppRequest{Plan: "p-8c125cd2f9f38dfdd95c9c0ba3cd2663", Product: "BentoPDF", Compose: string(file), Name: "pdf.example.test",
		Service: "web", Port: 8080, Guard: true, Secrets: []appunit.Secret{}, Health: []agent.AppHealth{{Service: "web", Port: 8080, Path: "/"}}}
}

// withSecrets is a guarded app with a database, whose password its two services read.
func withSecrets() agent.AppRequest {
	return agent.AppRequest{Plan: "p-0123456789abcdef0123456789abcdef", Product: "Ledger", Name: "ledger.example.test", Service: "web", Port: 3000, Guard: true,
		Compose: `services:
  web:
    image: ghcr.io/example/ledger:1@sha256:1111111111111111111111111111111111111111111111111111111111111111
    secrets: [db-password]
  db:
    image: docker.io/library/postgres:16@sha256:2222222222222222222222222222222222222222222222222222222222222222
    secrets: [db-password]
    healthcheck: {test: ["CMD", "pg_isready"]}
secrets:
  db-password:
    file: ./db-password
`,
		Secrets: []appunit.Secret{{Name: "db-password", UID: 999}},
		Health:  []agent.AppHealth{{Service: "web", Port: 3000, Path: "/health"}, {Service: "db", Compose: true}}}
}

// signedIn is an agent on a joined primary whose edge serves the sign-in: where a guarded app can be served.
func signedIn(t *testing.T, c agent.Config) (string, *agenttest.Host, string) {
	t.Helper()
	sock, h, state := joined(t, c)
	chain, key := wildcard(t, "example.test")
	call(t, sock, "PUT", "/v1/edge/certificates/example.test", map[string]string{"chain": chain, "key": key})
	call(t, sock, "PUT", "/v1/sign-in", map[string]string{"provider": "authentik", "bootstrapToken": bootstrap})
	if code, body := call(t, sock, "PUT", "/v1/edge", agent.EdgeSites{Port: 443, Admin: "admin.example.test", SignIn: "auth.example.test"}); code != http.StatusOK {
		t.Fatalf("the edge: %d %s", code, body)
	}
	return sock, h, state
}

func install(t *testing.T, sock, id string, r agent.AppRequest) agent.AppStatus {
	t.Helper()
	code, body := call(t, sock, "PUT", "/v1/apps/"+id, r)
	if code != http.StatusAccepted {
		t.Fatalf("installing %s: %d %s", id, code, body)
	}
	var s agent.AppStatus
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func apps(t *testing.T, sock string) agent.AppsStatus {
	t.Helper()
	code, body := call(t, sock, "GET", "/v1/apps", nil)
	var s agent.AppsStatus
	if code != http.StatusOK || json.Unmarshal(body, &s) != nil {
		t.Fatalf("the apps: %d %s", code, body)
	}
	return s
}

// The agent installs an app as its plan says (#19): one unit of its own, written where systemd links it from, running
// the standard compose command on the catalog's file and the agent's; systemd enables and starts it. The agent's
// compose file is the golden render's, from what the agent read of the project.
func TestAnAppIsOneUnitRunningTheStandardComposeCommand(t *testing.T) {
	testenv.Isolate(t)
	sock, h, state := signedIn(t, agent.Config{Sysroot: withSELinux{agenttest.Primary()}})
	r := bentoPDF(t)
	s := install(t, sock, "bentopdf", r)
	if s.Unit != "homerslab-app-bentopdf.service" || s.Project != "homerslab-bentopdf" || s.Upstream != "172.31.240.2:8080" || s.Plan != r.Plan {
		t.Errorf("the app: %+v", s)
	}
	dir := filepath.Join(state, "apps", "bentopdf")
	unit, err := os.ReadFile(filepath.Join(h.Root, agent.UnitsDir, "homerslab-app-bentopdf.service"))
	if err != nil {
		t.Fatal(err)
	}
	want := appunit.App{ID: "bentopdf", Product: "BentoPDF", Plan: r.Plan,
		Project: engine.Project{Name: "homerslab-bentopdf", Services: []engine.Service{{Name: "web", Image: "ghcr.io/alam00000/bentopdf-simple:2.8.10@sha256:b5227a308e796d2bd00d2eb6da9d7296093107a8be7efef38a006d06506dc3c0"}},
			Networks: []engine.Network{{Name: "default"}}},
		Service: "web", Upstream: mustAddrPort("172.31.240.2:8080"), Subnet: mustPrefix("172.31.240.0/28"), Level: "s0:c600,c601", Slot: 0, SELinux: true, Dir: dir}
	if !bytes.Equal(unit, appunit.UnitFile(want)) {
		t.Errorf("the unit:\n%s\nwant\n%s", unit, appunit.UnitFile(want))
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "homerslab.yaml")); !bytes.Equal(got, appunit.Override(want)) {
		t.Errorf("the agent's compose file:\n%s", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "compose.yaml")); string(got) != r.Compose {
		t.Errorf("the catalog's compose file, as it is:\n%s", got)
	}
	enable := h.Ran("systemctl enable /" + agent.UnitsDir + "/homerslab-app-bentopdf.service")
	reload := h.Ran("systemctl daemon-reload")
	start := h.Ran("systemctl --no-block restart homerslab-app-bentopdf.service")
	if enable < 0 || start < 0 || start < enable || h.Times("systemctl daemon-reload") == 0 {
		t.Errorf("systemd: enabled at %d, reloaded at %d, started at %d", enable, reload, start)
	}
	// Applying the same plan again writes the same files, and keeps the app where it was.
	if again := install(t, sock, "bentopdf", r); again.Upstream != s.Upstream {
		t.Errorf("applied again: %+v", again)
	}
}

// The edge serves the app's name behind its guard, to its one upstream; the firewall table lets only the edge's user and
// the agent's open that upstream (ADR 0020).
func TestTheEdgeAloneReachesTheApp(t *testing.T) {
	testenv.Isolate(t)
	users := func(name string) (uint32, bool) {
		return map[string]uint32{"homerslab-edge": 981, "homerslab": 980}[name], name == "homerslab-edge" || name == "homerslab"
	}
	sock, h, _ := signedIn(t, agent.Config{Users: users})
	install(t, sock, "bentopdf", bentoPDF(t))
	cfg := string(h.Edge.Config())
	for _, want := range []string{`"pdf.example.test"`, `"dial":"172.31.240.2:8080"`, `"uri":"/outpost.goauthentik.io/auth/caddy"`, `"X-Authentik-*"`} {
		if !strings.Contains(strings.ReplaceAll(cfg, " ", ""), strings.ReplaceAll(want, " ", "")) {
			t.Errorf("the edge's configuration lacks %s:\n%s", want, cfg)
		}
	}
	table, _ := h.Table()
	if !strings.Contains(table, "ip daddr . tcp dport . meta skuid { 172.31.240.2 . 8080 . 0, 172.31.240.2 . 8080 . 981, 172.31.250.10 . 9000 . 0, 172.31.250.10 . 9000 . 980, 172.31.250.10 . 9000 . 981 } accept") {
		t.Errorf("the firewall table:\n%s", table)
	}
}

// Without the sign-in served, the app's name isn't served at all: never unguarded.
func TestAGuardedAppIsNeverServedWithoutItsGuard(t *testing.T) {
	testenv.Isolate(t)
	sock, h, _ := joined(t, agent.Config{})
	chain, key := wildcard(t, "example.test")
	call(t, sock, "PUT", "/v1/edge/certificates/example.test", map[string]string{"chain": chain, "key": key})
	call(t, sock, "PUT", "/v1/edge", agent.EdgeSites{Port: 443, Admin: "admin.example.test"})
	install(t, sock, "bentopdf", bentoPDF(t))
	if bytes.Contains(h.Edge.Config(), []byte("pdf.example.test")) {
		t.Errorf("the edge serves the app before the sign-in:\n%s", h.Edge.Config())
	}
}

// Compose project names belong to HomersLab (#19): an install is refused while the engine runs a project of that name
// that this agent didn't create, and nothing is written for it.
func TestAnInstallIsRefusedOverAProjectHomersLabDidntCreate(t *testing.T) {
	testenv.Isolate(t)
	sock, h, state := signedIn(t, agent.Config{})
	h.Project(engine.State{Name: "homerslab-bentopdf", Containers: []engine.Container{{Service: "web", Name: "homerslab-bentopdf-web-1", State: "exited"}}})
	if code, body := call(t, sock, "PUT", "/v1/apps/bentopdf", bentoPDF(t)); code != http.StatusConflict || !strings.Contains(string(body), "not-ours") {
		t.Errorf("over someone else's project: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(state, "apps", "bentopdf")); err == nil || h.Ran("systemctl enable /"+agent.UnitsDir) >= 0 {
		t.Error("something was written for it")
	}
	// Only checking: refused the same, and an app that would install is only said to.
	if code, body := call(t, sock, "PUT", "/v1/apps/bentopdf?check=true", bentoPDF(t)); code != http.StatusConflict || !strings.Contains(string(body), "not-ours") {
		t.Errorf("checking over someone else's project: %d %s", code, body)
	}
	if code, body := call(t, sock, "PUT", "/v1/apps/ledger?check=true", withSecrets()); code != http.StatusOK || !strings.Contains(string(body), `"installable":true`) {
		t.Errorf("checking an app that would install: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(state, "apps", "ledger")); err == nil || h.Ran("systemctl enable /"+agent.UnitsDir) >= 0 || len(apps(t, sock).Apps) != 0 {
		t.Error("checking installed something")
	}
	h.EngineDown(true)
	if code, body := call(t, sock, "PUT", "/v1/apps/other", bentoPDF(t)); code != http.StatusServiceUnavailable {
		t.Errorf("with the engine down: %d %s", code, body)
	}
}

// An install that fails halfway (here, systemd won't start the unit) leaves the agent as it was: no app kept, so the
// firewall table and the edge never open to it, and its unit stopped.
func TestAnInstallThatFailsHalfwayLeavesNothingServed(t *testing.T) {
	testenv.Isolate(t)
	sock, h, _ := signedIn(t, agent.Config{})
	h.Fail("systemctl --no-block restart homerslab-app-ledger.service")
	if code, body := call(t, sock, "PUT", "/v1/apps/ledger", withSecrets()); code != http.StatusInternalServerError || !strings.Contains(string(body), "unit-failed") {
		t.Fatalf("a unit systemd won't start: %d %s", code, body)
	}
	if s := apps(t, sock); len(s.Apps) != 0 {
		t.Errorf("the agent keeps it: %+v", s.Apps)
	}
	if h.Ran("systemctl disable --now homerslab-app-ledger.service") < 0 {
		t.Error("its unit wasn't stopped")
	}
	if table, _ := h.Table(); strings.Contains(table, "172.31.240.2 . tcp . 3000") {
		t.Errorf("the firewall table opens to it:\n%s", table)
	}
	h.Mend()
	install(t, sock, "ledger", withSecrets())
}

// An app's name is its own: never Admin's, the sign-in's or another app's, which the edge would serve first.
func TestAnAppsNameIsItsOwn(t *testing.T) {
	testenv.Isolate(t)
	sock, _, _ := signedIn(t, agent.Config{})
	install(t, sock, "ledger", withSecrets())
	for _, name := range []string{"ledger.example.test", "admin.example.test", "auth.example.test"} {
		r := bentoPDF(t)
		r.Name = name
		if code, body := call(t, sock, "PUT", "/v1/apps/bentopdf", r); code != http.StatusConflict || !strings.Contains(string(body), "name-taken") {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	install(t, sock, "ledger", withSecrets()) // its own name, applied again
}

// The agent refuses what the floor and the wiring can't take, whatever the manager asks: a published port, a folder
// relabelled for SELinux (shared by every container that mounts it), a network mode, an upstream the project doesn't
// run, secrets other than the plan's, an app that isn't guarded, and ids that are HomersLab's own.
func TestAnAppsProjectIsRefusedWhenItCantBeWired(t *testing.T) {
	testenv.Isolate(t)
	sock, h, _ := signedIn(t, agent.Config{})
	edit := func(f func(*agent.AppRequest)) agent.AppRequest {
		r := withSecrets()
		f(&r)
		return r
	}
	for name, c := range map[string]struct {
		id   string
		r    agent.AppRequest
		code int
		why  string
	}{
		"a published port": {"ledger", edit(func(r *agent.AppRequest) {
			r.Compose = strings.Replace(r.Compose, "    secrets: [db-password]\n  db:", "    secrets: [db-password]\n    ports: [\"127.0.0.1:3000:3000\"]\n  db:", 1)
		}), 422, "publishes-ports"},
		"a relabelled folder": {"ledger", edit(func(r *agent.AppRequest) {
			r.Compose = strings.Replace(r.Compose, "  db:\n", "  db:\n    volumes: [\"/srv/ledger:/var/lib/postgresql/data:z\"]\n", 1)
		}), 422, "relabels-a-folder"},
		"a network mode": {"ledger", edit(func(r *agent.AppRequest) {
			r.Compose = strings.Replace(r.Compose, "  db:\n", "  db:\n    network_mode: host\n", 1)
		}), 422, "network-mode"},
		"its own security options, which would undo its level": {"ledger", edit(func(r *agent.AppRequest) {
			r.Compose = strings.Replace(r.Compose, "  db:\n", "  db:\n    security_opt: [\"label=disable\"]\n", 1)
		}), 422, "loosens-confinement"},
		"privileged": {"ledger", edit(func(r *agent.AppRequest) {
			r.Compose = strings.Replace(r.Compose, "  db:\n", "  db:\n    privileged: true\n", 1)
		}), 422, "loosens-confinement"},
		"the machine's process namespace": {"ledger", edit(func(r *agent.AppRequest) {
			r.Compose = strings.Replace(r.Compose, "  db:\n", "  db:\n    pid: host\n", 1)
		}), 422, "off-floor"},
		"the sign-in's network, as external": {"ledger", edit(func(r *agent.AppRequest) {
			r.Compose = strings.Replace(r.Compose, "  db:\n", "  db:\n    networks: [ak]\n", 1) +
				"networks:\n  ak: {external: true, name: homerslab-authentik_default}\n"
		}), 422, "network-not-its-own"},
		"the sign-in's network, by its name": {"ledger", edit(func(r *agent.AppRequest) {
			r.Compose = strings.Replace(r.Compose, "  db:\n", "  db:\n    networks: [ak]\n", 1) + "networks:\n  ak: {name: homerslab-authentik_default}\n"
		}), 422, "network-not-its-own"},
		"a network called as the upstream's": {"ledger", edit(func(r *agent.AppRequest) {
			r.Compose = strings.Replace(r.Compose, "  db:\n", "  db:\n    networks: [homerslab-upstream]\n", 1) + "networks:\n  homerslab-upstream: {}\n"
		}), 422, "network-not-its-own"},
		"a network on the LAN": {"ledger", edit(func(r *agent.AppRequest) {
			r.Compose = strings.Replace(r.Compose, "  db:\n", "  db:\n    networks: [lan]\n", 1) +
				"networks:\n  lan: {driver: macvlan, driver_opts: {parent: eth0}}\n"
		}), 422, "network-configured"},
		"a network's own addresses": {"ledger", edit(func(r *agent.AppRequest) {
			r.Compose = strings.Replace(r.Compose, "  db:\n", "  db:\n    networks: [back]\n", 1) +
				"networks:\n  back: {ipam: {config: [{subnet: 172.31.250.0/28}]}}\n"
		}), 422, "network-configured"},
		"an upstream it doesn't run":     {"ledger", edit(func(r *agent.AppRequest) { r.Service = "api" }), 422, "no-such-service"},
		"a secret the plan doesn't make": {"ledger", edit(func(r *agent.AppRequest) { r.Secrets = nil }), 422, "secrets-mismatch"},
		"a secret the file doesn't take": {"ledger", edit(func(r *agent.AppRequest) {
			r.Secrets = append(r.Secrets, appunit.Secret{Name: "api-key", UID: 1000})
		}), 422, "secrets-mismatch"},
		"not guarded":           {"ledger", edit(func(r *agent.AppRequest) { r.Guard = false }), 400, "sign-in-not-built"},
		"the sign-in's own id":  {"authentik", withSecrets(), 400, "bad-app"},
		"an id that isn't one":  {"Ledger", withSecrets(), 400, "bad-app"},
		"a plan that isn't one": {"ledger", edit(func(r *agent.AppRequest) { r.Plan = "latest" }), 400, "bad-app"},
		"a secret's odd name":   {"ledger", edit(func(r *agent.AppRequest) { r.Secrets[0].Name = "../db" }), 400, "bad-secrets"},
		"not a compose file":    {"ledger", edit(func(r *agent.AppRequest) { r.Compose = "include: [/etc/shadow]\n" }), 422, "invalid-compose"},
		"a check of no service": {"ledger", edit(func(r *agent.AppRequest) { r.Health[1].Service = "cache" }), 422, "no-such-service"},
	} {
		code, body := call(t, sock, "PUT", "/v1/apps/"+c.id, c.r)
		if code != c.code || !strings.Contains(string(body), `"`+c.why+`"`) {
			t.Errorf("%s: %d %s, want %d %s", name, code, body, c.code, c.why)
		}
	}
	if h.Ran("systemctl enable /"+agent.UnitsDir) >= 0 {
		t.Error("a refused app's unit was enabled")
	}
}

// Secrets (ADR 0011): made by the agent, sealed as root with the host key and the TPM, under <app>.<name>, once; never
// in an answer, the agent's state, or a file it writes in the clear.
func TestAnAppsSecretsAreMadeAndSealedByTheAgent(t *testing.T) {
	testenv.Isolate(t)
	sock, h, state := signedIn(t, agent.Config{Sysroot: withSELinux{agenttest.Primary()}})
	s := install(t, sock, "ledger", withSecrets())
	cred := filepath.Join(state, "apps", "ledger", "credentials", "db-password.cred")
	var sealed []string
	for _, c := range h.Cmds() {
		if c.Name == "systemd-creds" && c.Args[0] == "encrypt" && strings.Contains(c.String(), "/apps/") {
			sealed = append(sealed, strings.Join(c.Args, " "))
		}
	}
	if len(sealed) != 1 || sealed[0] != "encrypt --with-key=host+tpm2 --name=ledger.db-password - "+cred+".new" {
		t.Fatalf("sealed: %q", sealed)
	}
	b, err := os.ReadFile(cred)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(string(b), "sealed:"))
	if len(secret) < 32 {
		t.Fatalf("the secret is %d bytes", len(secret))
	}
	unit, _ := os.ReadFile(filepath.Join(h.Root, agent.UnitsDir, "homerslab-app-ledger.service"))
	if !strings.Contains(string(unit), "LoadCredentialEncrypted=ledger.db-password:"+cred+"\n") ||
		!strings.Contains(string(unit), "ExecStartPre=/usr/bin/chcon -t container_file_t -l "+"s0:c600,c601 /run/homerslab-app-ledger/db-password\n") {
		t.Errorf("the unit:\n%s", unit)
	}
	// Nowhere in the clear: not the answer, the state, the files, the logs.
	seen := []string{toString(s)}
	_ = filepath.Walk(state, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && p != cred {
			b, _ := os.ReadFile(p)
			seen = append(seen, string(b))
		}
		return nil
	})
	_ = filepath.Walk(h.Root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			b, _ := os.ReadFile(p)
			seen = append(seen, string(b))
		}
		return nil
	})
	for _, c := range h.Cmds() {
		seen = append(seen, strings.Join(c.Args, " "))
	}
	_, body := call(t, sock, "GET", "/v1/apps", nil)
	seen = append(seen, string(body))
	for _, txt := range seen {
		if strings.Contains(txt, string(secret)) {
			t.Fatalf("the secret is in the clear: %.200s", txt)
		}
	}
	// Made once: applying again seals nothing new.
	install(t, sock, "ledger", withSecrets())
	after, _ := os.ReadFile(cred)
	if !bytes.Equal(after, b) {
		t.Error("applying again made a new secret")
	}
}

// No SELinux-labelled folder is shared, and no two apps share a level: each app gets a level and an upstream network
// of its own.
func TestEachAppHasALevelAndANetworkOfItsOwn(t *testing.T) {
	testenv.Isolate(t)
	sock, _, state := signedIn(t, agent.Config{})
	a := install(t, sock, "bentopdf", bentoPDF(t))
	b := install(t, sock, "ledger", withSecrets())
	if a.Upstream == b.Upstream || b.Upstream != "172.31.240.18:3000" {
		t.Errorf("upstreams %s and %s", a.Upstream, b.Upstream)
	}
	ao, _ := os.ReadFile(filepath.Join(state, "apps", "bentopdf", "homerslab.yaml"))
	bo, _ := os.ReadFile(filepath.Join(state, "apps", "ledger", "homerslab.yaml"))
	if !strings.Contains(string(ao), "level:s0:c600,c601") || !strings.Contains(string(bo), "level:s0:c602,c603") {
		t.Errorf("levels:\n%s\n%s", ao, bo)
	}
}

// Status, health and logs for every app at once, from the engine through the Compose SDK; a container that runs
// without its unit is drift (#19).
func TestAppsReportTheirHealthAndContainersRunningWithoutTheirUnitAreDrift(t *testing.T) {
	testenv.Isolate(t)
	sock, h, _ := signedIn(t, agent.Config{})
	install(t, sock, "bentopdf", bentoPDF(t))
	install(t, sock, "ledger", withSecrets())
	// The sign-in's own project runs under its unit: never drift.
	h.Project(engine.State{Name: "homerslab-authentik", Containers: []engine.Container{{Service: "server", Name: "homerslab-authentik-server-1", State: "running"}}})
	s := apps(t, sock)
	if len(s.Apps) != 2 || s.Apps[0].ID != "bentopdf" || s.Apps[0].Health != "healthy" || !s.Apps[0].Active || s.Apps[1].Health != "healthy" || len(s.Drift) != 0 {
		t.Fatalf("installed: %+v", s)
	}
	if c := s.Apps[1].Checks; len(c) != 2 || c[0].Kind != "http" || !c[0].OK || c[1].Kind != "compose" || !c[1].OK {
		t.Errorf("ledger's checks: %+v", c)
	}

	// A service's check is as good as its worst container.
	h.Project(engine.State{Name: "homerslab-ledger", Containers: []engine.Container{
		{Service: "db", Name: "homerslab-ledger-db-1", State: "running", Health: "unhealthy"},
		{Service: "db", Name: "homerslab-ledger-db-2", State: "running", Health: "healthy"},
		{Service: "web", Name: "homerslab-ledger-web-1", State: "running"}}})
	if s = apps(t, sock); s.Apps[1].Health != "unhealthy" || s.Apps[1].Checks[1].OK || s.Apps[1].Checks[1].Detail != "unhealthy" || s.Apps[0].Health != "healthy" {
		t.Errorf("one replica sick: %+v", s.Apps)
	}

	h.Sick("homerslab-ledger", "unhealthy")
	h.Answer("172.31.240.2:8080", "Internal Server Error")
	s = apps(t, sock)
	if s.Apps[0].Health != "unhealthy" || s.Apps[1].Health != "unhealthy" {
		t.Errorf("sick: %+v", s.Apps)
	}

	// Someone stops the unit, and starts the containers by hand: the engine runs them without their unit.
	h.StopUnit("homerslab-app-bentopdf.service")
	h.Project(engine.State{Name: "homerslab-stray", Containers: []engine.Container{{Service: "x", Name: "homerslab-stray-x-1", State: "running"}}})
	s = apps(t, sock)
	if len(s.Drift) != 2 || s.Drift[0].Project != "homerslab-bentopdf" || s.Drift[0].What != "running-without-unit" ||
		s.Drift[1].Project != "homerslab-stray" || s.Apps[0].Active {
		t.Errorf("drift: %+v", s.Drift)
	}

	h.EngineDown(true)
	if s = apps(t, sock); s.Apps[0].Health != "unknown" {
		t.Errorf("with the engine down: %+v", s.Apps[0])
	}
}

// An app's logs, through the engine, with every secret of its own cut out of them, in whatever form it was written.
func TestAnAppsLogsNeverCarryItsSecrets(t *testing.T) {
	testenv.Isolate(t)
	sock, h, state := signedIn(t, agent.Config{})
	install(t, sock, "ledger", withSecrets())
	b, _ := os.ReadFile(filepath.Join(state, "apps", "ledger", "credentials", "db-password.cred"))
	secret, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(string(b), "sealed:"))
	h.SetLogs("homerslab-ledger", []compose.LogLine{
		{Container: "homerslab-ledger-db-1", Line: "connecting with password " + string(secret)},
		{Container: "homerslab-ledger-web-1", Line: "auth: Basic " + base64.StdEncoding.EncodeToString(secret), Err: true},
		{Container: "homerslab-ledger-web-1", Line: "GET /?p=" + url.QueryEscape(string(secret))},
		{Container: "homerslab-ledger-web-1", Line: "ready"},
	})
	code, body := call(t, sock, "GET", "/v1/apps/ledger/logs", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"ready"`) {
		t.Fatalf("%d %s", code, body)
	}
	for _, form := range []string{string(secret), base64.StdEncoding.EncodeToString(secret), url.QueryEscape(string(secret))} {
		if strings.Contains(string(body), form) {
			t.Errorf("the logs carry the secret: %s", body)
		}
	}
	if code, _ := call(t, sock, "GET", "/v1/apps/nothing/logs", nil); code != http.StatusNotFound {
		t.Errorf("an app that isn't installed: %d", code)
	}
}

func toString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func mustAddrPort(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }
func mustPrefix(s string) netip.Prefix     { return netip.MustParsePrefix(s) }
