package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/appunit"
	"github.com/ArjunRachithcf/homerslab/internal/atomicfile"
	"github.com/ArjunRachithcf/homerslab/internal/edge"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/engine/compose"
	"github.com/ArjunRachithcf/homerslab/internal/firewall"
	"github.com/ArjunRachithcf/homerslab/internal/host"
)

// Apps on this node (#19). The manager applies an install plan by handing the agent what the plan says and the catalog's
// compose file; the agent:
//   - refuses a compose project of that name that it didn't create (Compose claims containers by their labels alone);
//   - reads the project in a confined child, and refuses one that publishes a port, relabels a folder for SELinux, sets
//     a network mode, or declares other secrets than the plan's;
//   - gives the app its own SELinux level and its one upstream, a fixed address on a network of its own;
//   - makes the app's secrets and seals them (ADR 0011), once;
//   - writes the app's one unit and its compose file (appunit), and has systemd run it;
//   - lets the edge's user (and its own, for its probes) open the upstream in the firewall table, and nothing else
//     reach into the app's networks (ADR 0020);
//   - serves the app's name on the edge, behind its guard.
//
// It reads the apps' state, health and logs through the Compose SDK (AppEngine), and reports a container that runs
// without its unit as drift.

// AppEngine is what the agent reads of the container engine's apps: compose.Engine on a real machine.
type AppEngine interface {
	// States are every project of HomersLab's that the engine has a container of.
	States(ctx context.Context) (map[string]engine.State, error)
	// Logs are a project's last lines.
	Logs(ctx context.Context, project string, tail int) ([]compose.LogLine, error)
}

// UnitsDir is where the agent writes apps' units, under the machine's root: a folder of its own outside systemd's
// search path, whose units it has systemd link (systemctl enable <path>), so that the agent writes nothing else under
// /etc/systemd.
const UnitsDir = "etc/homerslab/units"

// Where apps' upstreams and levels come from. Each app gets a /28 of upstreamRange (the sign-in's network excepted),
// and a pair of SELinux categories of its own from firstCategory up, which no other app and not the sign-in shares.
var (
	upstreamRange = netip.MustParsePrefix("172.31.240.0/20")
	signInNet     = netip.MustParsePrefix("172.31.250.0/28")
)

const (
	firstCategory = 600
	lastCategory  = 999 // the sign-in has c1000,c1001
)

// Users of this machine the firewall table names.
const (
	edgeUser    = "homerslab-edge"
	managerUser = "homerslab"
)

// reservedApps are ids an app can't have: their units and projects are HomersLab's own.
var reservedApps = map[string]bool{"authentik": true, "edge": true, "agent": true, "manager": true, "admin": true, "read": true, "firewall": true}

// AppRequest is an app as the manager wants it: what its install plan says, and the catalog's compose file, whose digest
// the plan carries. None of it is secret.
type AppRequest struct {
	Plan    string `json:"plan"`
	Product string `json:"product"`
	Compose string `json:"compose"`
	// Name is the app's name on the main domain; Service and Port its upstream in the compose project.
	Name    string `json:"name"`
	Service string `json:"service"`
	Port    int    `json:"port"`
	// Guard: people sign in to it at the edge (its sign-in method is the guard). The only method so far (#20).
	Guard   bool             `json:"guard"`
	Secrets []appunit.Secret `json:"secrets"`
	Health  []AppHealth      `json:"health"`
}

// AppHealth is one of an app's health checks: an HTTP request to a service's port, or the compose file's own check.
type AppHealth struct {
	Service string `json:"service"`
	Port    int    `json:"port,omitempty"`
	Path    string `json:"path,omitempty"`
	Compose bool   `json:"compose,omitempty"`
}

// appState is what the agent keeps of an installed app, in agent.json. None of it is secret.
type appState struct {
	Product  string           `json:"product"`
	Plan     string           `json:"plan"`
	Name     string           `json:"name"`
	Service  string           `json:"service"`
	Upstream netip.AddrPort   `json:"upstream"`
	Subnet   netip.Prefix     `json:"subnet"`
	Level    string           `json:"level"`
	Guard    bool             `json:"guard"`
	Secrets  []appunit.Secret `json:"secrets"`
	Health   []AppHealth      `json:"health"`
	Project  engine.Project   `json:"project"`
}

// AppStatus is an app as the agent finds it now. None of it is secret.
type AppStatus struct {
	ID       string `json:"id"`
	Product  string `json:"product"`
	Plan     string `json:"plan"`
	Name     string `json:"name"`
	Unit     string `json:"unit"`
	Project  string `json:"project"`
	Upstream string `json:"upstream"`
	// Active: systemd runs the app's unit.
	Active     bool               `json:"active"`
	Containers []engine.Container `json:"containers"`
	// Health is the app's, from its checks: healthy, starting, unhealthy, stopped, or unknown while the engine doesn't
	// answer.
	Health string        `json:"health"`
	Checks []CheckResult `json:"checks"`
}

// CheckResult is one health check as it went.
type CheckResult struct {
	Service string `json:"service"`
	Kind    string `json:"kind"` // http or compose
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
}

// AppsStatus is every app on this node, and what runs without its unit.
type AppsStatus struct {
	Apps []AppStatus `json:"apps"`
	// Drift: containers of a HomersLab project that run without the unit that owns them (stopped, or never installed).
	Drift []AppDrift `json:"drift"`
}

// AppDrift is a project whose containers run without their unit.
type AppDrift struct {
	Project string   `json:"project"`
	What    string   `json:"what"` // running-without-unit
	Running []string `json:"running"`
}

func (a *Agent) appDir(id string) string { return filepath.Join(a.cfg.StateDir, "apps", id) }

func (a *Agent) appUnit(id string, s *appState) appunit.App {
	return appunit.App{ID: id, Product: s.Product, Plan: s.Plan, Project: s.Project, Service: s.Service, Upstream: s.Upstream,
		Subnet: s.Subnet, Level: s.Level, SELinux: a.selinux(), Secrets: s.Secrets, Dir: a.appDir(id)}
}

var (
	planID   = regexp.MustCompile(`^p-[0-9a-f]{32}$`)
	httpPath = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()*+,;=:@%/?-]{0,255}$`)
)

// validApp says what is wrong with an app's request, or "".
func validApp(id string, r AppRequest) string {
	switch {
	case !appunit.ID.MatchString(id) || reservedApps[id]:
		return "bad-app"
	case !planID.MatchString(r.Plan) || r.Product == "" || len(r.Product) > 100 || strings.ContainsAny(r.Product, "\n\r\x00%"):
		return "bad-app"
	case !domainName.MatchString(r.Name) || r.Service == "" || r.Port <= 0 || r.Port > 65535 || r.Compose == "":
		return "bad-app"
	case !r.Guard:
		return "sign-in-not-built" // apps that sign people in themselves come with #20
	}
	seen := map[string]bool{}
	for _, s := range r.Secrets {
		if !appunit.SecretName.MatchString(s.Name) || seen[s.Name] || s.UID < 0 || s.UID > 1<<31-2 {
			return "bad-secrets"
		}
		seen[s.Name] = true
	}
	for _, h := range r.Health {
		if h.Service == "" || h.Compose == (h.Port != 0) || h.Port < 0 || h.Port > 65535 || (h.Port != 0 && !httpPath.MatchString(h.Path)) {
			return "bad-health"
		}
	}
	return ""
}

// checkProject says what in an app's compose project installing refuses, or "": a port published on the host (the
// floor), a bind it asks the engine to relabel (a relabelled folder is shared by every container that mounts it), a
// network mode (the machine's network is an extra), an upstream the project doesn't run, or other secrets than the
// plan makes.
func checkProject(p engine.Project, r AppRequest) (code, detail string) {
	upstream := false
	for _, s := range p.Services {
		switch {
		case len(s.Published) > 0:
			return "publishes-ports", s.Name
		case len(s.Relabelled) > 0:
			return "relabels-a-folder", s.Name
		case s.NetworkMode != "":
			return "network-mode", s.Name
		}
		upstream = upstream || s.Name == r.Service
	}
	if !upstream {
		return "no-such-service", r.Service
	}
	var want []string
	for _, s := range r.Secrets {
		want = append(want, s.Name)
	}
	slices.Sort(want)
	if !slices.Equal(want, p.Secrets) {
		return "secrets-mismatch", ""
	}
	for _, h := range r.Health {
		if !slices.ContainsFunc(p.Services, func(s engine.Service) bool { return s.Name == h.Service }) {
			return "no-such-service", h.Service
		}
	}
	return "", ""
}

// putApp installs an app, or applies a new plan to it.
func (a *Agent) putApp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req AppRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	if code := validApp(id, req); code != "" {
		fail(w, http.StatusBadRequest, code)
		return
	}
	a.installs.Lock()
	defer a.installs.Unlock()
	ctx := r.Context()
	project := appunit.Project(id)

	// Compose project names belong to HomersLab: one the engine already runs that this agent didn't install is someone
	// else's, and an install would take its containers over.
	asked, cancel := context.WithTimeout(ctx, engineWait)
	states, err := a.cfg.Engine.States(asked)
	cancel()
	if err != nil {
		a.log.Error("asking the engine for its projects", "err", err)
		fail(w, http.StatusServiceUnavailable, "engine-unreachable")
		return
	}
	a.mu.Lock()
	prev := a.state.Apps[id]
	a.mu.Unlock()
	if _, there := states[project]; there && prev == nil {
		a.log.Warn("refused to install over a compose project HomersLab didn't create", "project", project)
		fail(w, http.StatusConflict, "not-ours")
		return
	}

	p, err := a.reads.read(ctx, project, []byte(req.Compose))
	if err != nil {
		a.log.Warn("reading an app's compose file", "app", id, "err", err)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid-compose"})
		return
	}
	if code, detail := checkProject(p, req); code != "" {
		a.log.Warn("refused an app's compose project", "app", id, "why", code, "service", detail)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": code, "detail": detail})
		return
	}

	a.mu.Lock()
	next, err := a.placeApp(id, prev)
	if err != nil {
		a.mu.Unlock()
		a.log.Error("placing an app", "app", id, "err", err)
		fail(w, http.StatusInsufficientStorage, "no-room")
		return
	}
	next.Product, next.Plan, next.Name, next.Service, next.Guard = req.Product, req.Plan, req.Name, req.Service, req.Guard
	next.Upstream = netip.AddrPortFrom(next.Subnet.Addr().Next().Next(), uint16(req.Port))
	next.Secrets, next.Health, next.Project = slices.Clone(req.Secrets), slices.Clone(req.Health), p
	if a.state.Apps == nil {
		a.state.Apps = map[string]*appState{}
	}
	a.state.Apps[id] = next
	if err := a.save(); err != nil {
		if prev == nil {
			delete(a.state.Apps, id)
		} else {
			a.state.Apps[id] = prev
		}
		a.mu.Unlock()
		a.log.Error("saving the agent's state", "err", err)
		fail(w, http.StatusInternalServerError, "state-unsaved")
		return
	}
	u := a.appUnit(id, next)
	a.mu.Unlock()

	if err := a.writeApp(ctx, u, []byte(req.Compose)); err != nil {
		a.log.Error("writing an app's unit", "app", id, "err", err)
		fail(w, http.StatusInternalServerError, "unit-failed")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// The upstream opens to the edge (and the agent) only once the table says so; the edge serves the name after.
	if err := a.reconcile(ctx, true); err != nil {
		a.log.Error("letting the edge reach the app", "app", id, "err", err)
		fail(w, http.StatusInternalServerError, "firewall-failed")
		return
	}
	if err := a.pushEdge(ctx); err != nil {
		a.log.Error("serving the app's name on the edge", "app", id, "err", err)
		fail(w, http.StatusBadGateway, "edge-unreachable")
		return
	}
	a.log.Info("an app is installed: its unit runs it", "app", id, "plan", req.Plan, "unit", appunit.Unit(id), "upstream", next.Upstream)
	writeJSON(w, http.StatusAccepted, a.appStatusLocked(id, next))
}

// placeApp is the app's level and upstream network: the ones it has, or the first free ones. The caller holds a.mu.
func (a *Agent) placeApp(id string, prev *appState) (*appState, error) {
	if prev != nil {
		next := *prev
		return &next, nil
	}
	levels, nets := map[string]bool{}, map[netip.Prefix]bool{signInNet: true}
	for other, s := range a.state.Apps {
		if other != id {
			levels[s.Level], nets[s.Subnet] = true, true
		}
	}
	next := &appState{}
	for c := firstCategory; c+1 <= lastCategory; c += 2 {
		if l := fmt.Sprintf("s0:c%d,c%d", c, c+1); !levels[l] {
			next.Level = l
			break
		}
	}
	base := upstreamRange.Addr().As4()
	for i := 0; i < 1<<(28-upstreamRange.Bits()); i++ {
		b := base
		b[2] += byte(i >> 4)
		b[3] = byte(i&15) << 4
		if p := netip.PrefixFrom(netip.AddrFrom4(b), 28); !nets[p] {
			next.Subnet = p
			break
		}
	}
	if next.Level == "" || !next.Subnet.IsValid() {
		return nil, errors.New("no SELinux level or upstream network is left for another app")
	}
	return next, nil
}

// writeApp writes an app's files, seals its secrets (once), and has systemd run its unit.
func (a *Agent) writeApp(ctx context.Context, u appunit.App, file []byte) error {
	if err := os.MkdirAll(filepath.Join(u.Dir, "credentials"), 0o700); err != nil {
		return err
	}
	for _, s := range u.Secrets {
		if _, err := os.Stat(u.CredentialFile(s.Name)); err == nil {
			continue // made once: the app's data opens only with it
		}
		if err := a.sealAt(ctx, u.CredentialFile(s.Name), appunit.Credential(u.ID, s.Name), newSecret()); err != nil {
			return fmt.Errorf("sealing %s: %w", s.Name, err)
		}
	}
	if err := atomicfile.Write(u.ComposeFile(), file); err != nil {
		return err
	}
	if err := atomicfile.Write(u.OverrideFile(), appunit.Override(u)); err != nil {
		return err
	}
	unit := path.Join(UnitsDir, appunit.Unit(u.ID))
	if err := a.writeAt(unit, appunit.UnitFile(u)); err != nil {
		return err
	}
	for _, step := range [][]string{
		{"systemctl", "enable", "/" + unit}, // links it into systemd's search path, and enables it
		{"systemctl", "daemon-reload"},
		// Queued, not waited for: compose pulls images and starts containers for minutes.
		{"systemctl", "--no-block", "restart", appunit.Unit(u.ID)},
	} {
		if err := a.run(ctx, step[0], step[1:]...); err != nil {
			return err
		}
	}
	return nil
}

// engineWait is how long the agent waits for the engine's answer.
const engineWait = 10 * time.Second

// getApps is every app on this node, as systemd and the engine find it, and the containers that run without their
// unit.
func (a *Agent) getApps(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.appsStatus(r.Context()))
}

func (a *Agent) appsStatus(ctx context.Context) AppsStatus {
	a.mu.Lock()
	apps := map[string]appState{}
	for id, s := range a.state.Apps {
		apps[id] = *s
	}
	a.mu.Unlock()
	asked, cancel := context.WithTimeout(ctx, engineWait)
	defer cancel()
	states, engineErr := a.cfg.Engine.States(asked)
	if engineErr != nil {
		a.log.Warn("asking the engine for its projects", "err", engineErr)
	}
	out := AppsStatus{Apps: []AppStatus{}, Drift: []AppDrift{}}
	active := map[string]bool{}
	ids := slices.Sorted(func(yield func(string) bool) {
		for id := range apps {
			if !yield(id) {
				return
			}
		}
	})
	for _, id := range ids {
		s := apps[id]
		st := a.appStatusLocked(id, &s)
		st.Active = a.unitActive(asked, appunit.Unit(id))
		active[st.Project] = st.Active
		if engineErr == nil {
			st.Containers = orNone(states[st.Project].Containers)
		}
		st.Health, st.Checks = a.health(asked, &s, st, engineErr == nil)
		out.Apps = append(out.Apps, st)
	}
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for n := range states {
			if !yield(n) {
				return
			}
		}
	}) {
		if active[name] {
			continue
		}
		var running []string
		for _, c := range states[name].Containers {
			if c.State == "running" {
				running = append(running, c.Name)
			}
		}
		if len(running) > 0 {
			out.Drift = append(out.Drift, AppDrift{Project: name, What: "running-without-unit", Running: running})
		}
	}
	return out
}

func orNone(c []engine.Container) []engine.Container {
	if c == nil {
		return []engine.Container{}
	}
	return c
}

func (a *Agent) unitActive(ctx context.Context, unit string) bool {
	_, err := a.cfg.Run(ctx, host.Cmd{Name: "systemctl", Args: []string{"is-active", "--quiet", unit}})
	return err == nil
}

func (a *Agent) appStatusLocked(id string, s *appState) AppStatus {
	return AppStatus{ID: id, Product: s.Product, Plan: s.Plan, Name: s.Name, Unit: appunit.Unit(id), Project: appunit.Project(id),
		Upstream: s.Upstream.String(), Containers: []engine.Container{}, Health: "starting", Checks: []CheckResult{}}
}

// health runs an app's checks: its compose file's, from the engine's answer, and HTTP requests to its upstream, which
// the agent may open (the firewall table lets it, for these probes). A check of another service's port can't be asked
// from the host, and is left to that service's compose check.
func (a *Agent) health(ctx context.Context, s *appState, st AppStatus, engineAnswered bool) (string, []CheckResult) {
	checks := []CheckResult{}
	if !engineAnswered {
		return "unknown", checks
	}
	running := false
	for _, c := range st.Containers {
		running = running || c.State == "running"
	}
	if !st.Active && !running {
		return "stopped", checks
	}
	verdict := "healthy"
	worse := func(v string) {
		if v == "unhealthy" || verdict == "healthy" {
			verdict = v
		}
	}
	for _, h := range s.Health {
		switch {
		case h.Compose:
			c := CheckResult{Service: h.Service, Kind: "compose"}
			found := false
			for _, ct := range st.Containers {
				if ct.Service != h.Service {
					continue
				}
				found = true
				c.Detail = ct.Health
				c.OK = ct.State == "running" && ct.Health == "healthy"
			}
			switch {
			case !found || c.Detail == "starting" || c.Detail == "" && found:
				worse("starting")
			case !c.OK:
				worse("unhealthy")
			}
			checks = append(checks, c)
		case h.Service == s.Service && h.Port == int(s.Upstream.Port()):
			c := CheckResult{Service: h.Service, Kind: "http"}
			c.OK, c.Detail = a.cfg.Probe(ctx, s.Upstream, s.Name, h.Path)
			if !c.OK {
				worse(map[bool]string{true: "unhealthy", false: "starting"}[running && st.Active && c.Detail != "unreachable"])
			}
			checks = append(checks, c)
		}
	}
	for _, c := range st.Containers {
		if c.State != "running" {
			worse("starting") // the unit is bringing it up, or restarting it
		}
	}
	if len(st.Containers) == 0 {
		worse("starting")
	}
	return verdict, checks
}

// probe asks an app's upstream for path, as the edge would for its name: a 2xx or 3xx answer is healthy.
func probe(ctx context.Context, upstream netip.AddrPort, name, p string) (bool, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+upstream.String()+p, nil)
	if err != nil {
		return false, "bad-request"
	}
	req.Host = name
	c := &http.Client{Timeout: hostWait, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		return false, "unreachable"
	}
	resp.Body.Close()
	return resp.StatusCode < 400, http.StatusText(resp.StatusCode)
}

// getAppLogs is an app's last lines, with every secret of its own cut out of them.
func (a *Agent) getAppLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.mu.Lock()
	s, ok := a.state.Apps[id]
	var u appunit.App
	if ok {
		u = a.appUnit(id, s)
	}
	a.mu.Unlock()
	if !ok {
		fail(w, http.StatusNotFound, "no-such-app")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), engineWait)
	defer cancel()
	lines, err := a.cfg.Engine.Logs(ctx, appunit.Project(id), 200)
	if err != nil {
		a.log.Warn("reading an app's logs", "app", id, "err", err)
		fail(w, http.StatusServiceUnavailable, "engine-unreachable")
		return
	}
	var secrets [][]byte
	for _, sec := range u.Secrets {
		v, err := a.unsealAt(ctx, u.CredentialFile(sec.Name), appunit.Credential(id, sec.Name))
		if err != nil {
			a.log.Error("reading an app's secret, to cut it out of its logs", "app", id, "err", err)
			fail(w, http.StatusInternalServerError, "unseal-failed")
			return
		}
		secrets = append(secrets, v)
	}
	if lines == nil {
		lines = []compose.LogLine{}
	}
	for i := range lines {
		lines[i].Line = scrub(lines[i].Line, secrets)
	}
	writeJSON(w, http.StatusOK, map[string]any{"app": id, "lines": lines, "at": time.Now().UTC()})
}

// scrub cuts each secret out of a line, plain, base64 and URL-encoded.
func scrub(line string, secrets [][]byte) string {
	for _, s := range secrets {
		if len(s) == 0 {
			continue
		}
		for _, form := range []string{string(s), base64.StdEncoding.EncodeToString(s), base64.RawStdEncoding.EncodeToString(s),
			base64.URLEncoding.EncodeToString(s), base64.RawURLEncoding.EncodeToString(s), url.QueryEscape(string(s))} {
			line = strings.ReplaceAll(line, form, "…")
		}
	}
	return line
}

// appsFirewall are the apps' upstreams, and the sign-in's, as the firewall table lets them be opened: each app's by the
// edge's user and root (the agent's probes), the sign-in's by the manager's too. A user the machine doesn't have
// (yet) is left out, so the upstream is only root's. The caller holds a.mu.
func (a *Agent) appsFirewall() []firewall.Upstream {
	var edgeUIDs, signInUIDs []uint32
	if uid, ok := a.cfg.Users(edgeUser); ok {
		edgeUIDs = append(edgeUIDs, uid)
	}
	signInUIDs = slices.Clone(edgeUIDs)
	if uid, ok := a.cfg.Users(managerUser); ok {
		signInUIDs = append(signInUIDs, uid)
	}
	var out []firewall.Upstream
	if a.state.SignIn != nil {
		if up, err := netip.ParseAddrPort(a.upstream()); err == nil {
			out = append(out, firewall.Upstream{Addr: up, UIDs: append(signInUIDs, 0)})
		}
	}
	for _, s := range a.state.Apps {
		out = append(out, firewall.Upstream{Addr: s.Upstream, UIDs: append(slices.Clone(edgeUIDs), 0)})
	}
	slices.SortFunc(out, func(x, y firewall.Upstream) int { return x.Addr.Compare(y.Addr) })
	return out
}

// edgeApps are the apps' sites on the edge: each behind its guard, once the sign-in is served. The caller holds a.mu.
func (a *Agent) edgeApps() []edge.App {
	if a.state.SignIn == nil || a.state.Edge == nil || a.state.Edge.SignIn == "" {
		return nil // no sign-in to send anyone to: the app's name stays unserved, never unguarded
	}
	var out []edge.App
	for _, s := range a.state.Apps {
		if s.Guard {
			out = append(out, edge.App{Name: s.Name, Dial: s.Upstream.String(), Guard: edge.AuthentikGuard(a.upstream())})
		}
	}
	slices.SortFunc(out, func(x, y edge.App) int { return strings.Compare(x.Name, y.Name) })
	return out
}

// passwdUsers finds a user's uid in a passwd file.
func passwdUsers(file string) func(string) (uint32, bool) {
	return func(name string) (uint32, bool) {
		entries, err := idEntries(file)
		if err != nil {
			return 0, false
		}
		e, err := onlyOwn(entries, name, 4)
		if err != nil || e.id == 0 {
			return 0, false
		}
		return e.id, true
	}
}
