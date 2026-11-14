package agent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/appunit"
	"github.com/ArjunRachithcf/homerslab/internal/atomicfile"
	"github.com/ArjunRachithcf/homerslab/internal/catalog"
	"github.com/ArjunRachithcf/homerslab/internal/edge"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/engine/compose"
	"github.com/ArjunRachithcf/homerslab/internal/firewall"
	"github.com/ArjunRachithcf/homerslab/internal/host"
	"github.com/ArjunRachithcf/homerslab/internal/text"
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
	// listenerNet holds the sign-in listener's address (SignInListen), on the machine's loopback: no app's network.
	listenerNet = netip.MustParsePrefix("172.31.255.240/28")
)

// SignInListen is the sign-in listener's own address on every node (ADR 0020): where apps reach the sign-in, on the
// household's port. The agent puts it on the machine's loopback, so it is a local address no network leads to but this
// machine's own routing, and every app's container resolves auth.<main domain> to it.
var SignInListen = netip.MustParseAddr("172.31.255.241")

const (
	firstCategory = 600
	lastCategory  = 999 // the sign-in has c1000,c1001
)

// Users of this machine the firewall table names.
const (
	edgeUser    = "homerslab-edge"
	managerUser = "homerslab"
	homeUser    = "homerslab-home"
)

// reservedApps are ids an app can't have: their units, projects and sign-in clients (homerslab-<id>) are HomersLab's own.
var reservedApps = map[string]bool{"authentik": true, "edge": true, "agent": true, "manager": true, "admin": true, "read": true, "firewall": true,
	"home": true, "projection": true}

// AppRequest is an app as the manager wants it: what its install plan says, and the catalog's compose file, whose digest
// the plan carries. None of it is secret but an OIDC client's secret, which travels inward only.
type AppRequest struct {
	Plan    string `json:"plan"`
	Product string `json:"product"`
	Compose string `json:"compose"`
	// Name is the app's name on the main domain; Service and Port its upstream in the compose project.
	Name    string `json:"name"`
	Service string `json:"service"`
	Port    int    `json:"port"`
	// Guard: people sign in to it at the edge (its sign-in method is the guard). OIDC: it signs them in itself, with
	// this client (#20). One or the other.
	Guard   bool             `json:"guard"`
	OIDC    *AppOIDC         `json:"oidc,omitempty"`
	Secrets []appunit.Secret `json:"secrets"`
	Health  []AppHealth      `json:"health"`
}

// AppOIDC is the OIDC client the household account made for an app that signs people in itself (#20), and how the app
// takes it (the manifest's env): the settings as environment variables of its upstream's service, the secret as a
// file.
type AppOIDC struct {
	Env catalog.OIDCEnv `json:"env"`
	// Issuer is the client's issuer, at the sign-in this node serves; ClientID the client's id.
	Issuer   string `json:"issuer"`
	ClientID string `json:"clientId"`
	// RedirectURIs are the client's redirect addresses: the first on the app's own URL is the one the app is told.
	RedirectURIs []string `json:"redirectUris"`
	// AdminGroup is the admins' group, for an app that makes them its own admins.
	AdminGroup string `json:"adminGroup"`
	// SecretUID is the container user that reads the client's secret.
	SecretUID int `json:"secretUid"`
	// RefusePaths are the app's own other ways to sign in, which the edge refuses before the app sees them.
	RefusePaths []string `json:"refusePaths,omitempty"`
	// ClientSecret is the client's secret, inward only: the agent seals it as one of the app's credentials, and keeps
	// it nowhere else. Empty when the manager only checks the app (?check=true), before the client is made.
	ClientSecret string `json:"clientSecret,omitempty"`
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
	OIDC     *appunit.OIDC    `json:"oidc,omitempty"`    // its client's settings; never its secret
	Refused  []string         `json:"refused,omitempty"` // its own other ways to sign in, which the edge refuses
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
	// Pending is what of the app's wiring didn't take at its install, which the agent's watch puts right: the firewall
	// table (the upstream stays closed till then) or the edge (the name stays unserved).
	Pending []string `json:"pending,omitempty"`
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
		Subnet: s.Subnet, Level: s.Level, Slot: slot(s.Level), SELinux: a.selinux(), Secrets: s.Secrets, OIDC: s.OIDC,
		Dir: a.appDir(id), SignIn: a.signInName(), SignInAt: SignInListen}
}

// appsFollowSignIn writes every app's compose file again where the sign-in's name or its listener changed what it says
// (the sign-in served now, or renamed), and has systemd restart those apps, so their containers resolve it to the
// listener. The caller holds a.mu.
func (a *Agent) appsFollowSignIn(ctx context.Context) {
	for _, id := range slices.Sorted(maps.Keys(a.state.Apps)) {
		u := a.appUnit(id, a.state.Apps[id])
		want := appunit.Override(u)
		have, err := os.ReadFile(u.OverrideFile())
		switch {
		case err == nil && bytes.Equal(have, want):
			continue
		case err != nil && !errors.Is(err, os.ErrNotExist):
			a.log.Error("reading an app's compose file", "app", id, "err", err)
			continue
		}
		if err := atomicfile.Write(u.OverrideFile(), want); err != nil {
			a.log.Error("writing an app's compose file for the sign-in", "app", id, "err", err)
			continue
		}
		if err := a.run(ctx, "systemctl", "--no-block", "restart", appunit.Unit(id)); err != nil {
			a.log.Error("restarting an app for the sign-in", "app", id, "err", err)
			continue
		}
		a.log.Info("an app follows the sign-in: its containers resolve it to the listener", "app", id)
	}
}

// signInName is the sign-in's name where this node serves it, or "". The caller holds a.mu.
func (a *Agent) signInName() string {
	if e := a.state.Edge; e != nil && a.state.SignIn != nil {
		return e.SignIn
	}
	return ""
}

// slot is an app's number on this node, from its level (s0:c600,c601 is 0): unique as the level is.
func slot(level string) int {
	var c, d int
	if _, err := fmt.Sscanf(level, "s0:c%d,c%d", &c, &d); err != nil || c < firstCategory {
		return -1 // never placed: a bridge named hla-1… still begins with the prefix
	}
	return (c - firstCategory) / 2
}

var (
	httpPath = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()*+,;=:@%/?-]{0,255}$`)
	// An OIDC client's settings, as the agent takes them: an issuer and a redirect address are https URLs of plain
	// characters, an id and a group plain words, a variable's name a shell's, a secret printable ASCII.
	urlPath    = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()+,;=:@%/-]{0,255}$`)
	clientID   = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,255}$`)
	groupName  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	secretText = regexp.MustCompile(`^[\x21-\x7e]{16,512}$`)
)

// siteURL is a name's one URL on the household's port (ADR 0021).
func siteURL(name string, port int) string {
	if port == 0 || port == 443 {
		return "https://" + name
	}
	return fmt.Sprintf("https://%s:%d", name, port)
}

// under says whether u is an https URL at origin, with a path of plain characters.
func under(u, origin string) bool {
	rest, ok := strings.CutPrefix(u, origin)
	return ok && urlPath.MatchString(rest)
}

// oidcEnv is an app's client's settings, as its environment takes them: what the manifest names, from the client
// the manager made and the sign-in and port this node serves, or the code that refuses the request. sites are this
// node's, nil when it serves none. Only checking (before the manager has made the client), the client's issuer, id
// and secret may be missing.
func oidcEnv(r AppRequest, sites *EdgeSites, check bool) (*appunit.OIDC, string) {
	o := *r.OIDC
	if sites == nil || sites.SignIn == "" {
		return nil, "no-sign-in" // nothing to send people to sign in at
	}
	signIn, app := siteURL(sites.SignIn, sites.Port), siteURL(r.Name, sites.Port)
	if check {
		o.Issuer, o.ClientID = cmp.Or(o.Issuer, signIn+"/"), cmp.Or(o.ClientID, "unmade")
	} else if o.ClientSecret == "" {
		return nil, "no-client-secret"
	}
	for _, p := range o.RefusePaths {
		if !urlPath.MatchString(p) || p == "/" || strings.HasPrefix(p, "//") || slices.Contains(strings.Split(p, "/"), "..") {
			return nil, "bad-sign-in"
		}
	}
	if !under(o.Issuer, signIn) || !strings.HasSuffix(o.Issuer, "/") || !clientID.MatchString(o.ClientID) ||
		o.AdminGroup != "" && !groupName.MatchString(o.AdminGroup) || o.SecretUID < 0 || o.SecretUID > 1<<31-2 ||
		len(o.RedirectURIs) == 0 || o.ClientSecret != "" && !secretText.MatchString(o.ClientSecret) {
		return nil, "bad-sign-in"
	}
	redirect := ""
	for _, u := range o.RedirectURIs {
		switch {
		case under(u, app):
			redirect = cmp.Or(redirect, u)
		case strings.HasPrefix(u, "http") || !strings.Contains(u, ".") || len(u) > 255 || !text.OneLine(u):
			return nil, "bad-sign-in" // its own apps' addresses aside, every redirect address is on the app's own URL
		}
	}
	e := o.Env
	if e.ClientID == "" || e.ClientSecretFile == "" || e.Issuer == "" && e.Discovery == "" {
		return nil, "bad-sign-in"
	}
	values := map[string]string{e.Issuer: o.Issuer, e.Discovery: o.Issuer + ".well-known/openid-configuration", e.ClientID: o.ClientID,
		e.ClientSecretFile: appunit.ClientSecretPath, e.RedirectURI: redirect, e.URL: app, e.AdminGroup: o.AdminGroup}
	env := map[string]string{}
	for _, n := range e.Names() {
		if n == "" {
			continue
		}
		if _, twice := env[n]; twice || !catalog.EnvName.MatchString(n) || values[n] == "" {
			return nil, "bad-sign-in"
		}
		env[n] = values[n]
	}
	return &appunit.OIDC{Env: env, SecretUID: o.SecretUID}, ""
}

// validApp says what is wrong with an app's request, or "".
func validApp(id string, r AppRequest) string {
	switch {
	case !appunit.ID.MatchString(id) || reservedApps[id]:
		return "bad-app"
	case !appunit.PlanID.MatchString(r.Plan) || r.Product == "" || len(r.Product) > 100 || !text.OneLine(r.Product) || strings.Contains(r.Product, "%"):
		return "bad-app"
	case !domainName.MatchString(r.Name) || r.Service == "" || r.Port <= 0 || r.Port > 65535 || r.Compose == "":
		return "bad-app"
	case r.Guard && r.OIDC != nil:
		return "bad-app" // one way to sign in
	case !r.Guard && r.OIDC == nil:
		return "sign-in-not-built" // LDAP comes with #49
	}
	seen := map[string]bool{}
	for _, s := range r.Secrets {
		if !appunit.SecretName.MatchString(s.Name) || seen[s.Name] || s.UID < 0 || s.UID > 1<<31-2 || s.Name == appunit.ClientSecret {
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
// network mode (the machine's network is an extra), what loosens a container's confinement (privileged never; a
// capability, a device, its own security options, which would undo the app's level, or the machine's namespaces:
// extras, approved with the passkey, #25), a network that isn't the project's own bridge as the agent makes it (another
// project's, an external one, another driver, its own options or addresses), an address of its own for the sign-in's
// name (the agent's is the sign-in listener), an upstream the project doesn't run, or other secrets than the plan makes.
func checkProject(p engine.Project, r AppRequest, signIn string) (code, detail string) {
	upstream := false
	for _, s := range p.Services {
		switch {
		case signIn != "" && slices.Contains(s.Hosts, strings.ToLower(signIn)):
			return "names-the-sign-in", s.Name // its own address for the sign-in: the agent's is the listener
		case len(s.Published) > 0:
			return "publishes-ports", s.Name
		case len(s.Relabelled) > 0:
			return "relabels-a-folder", s.Name
		case s.NetworkMode != "":
			return "network-mode", s.Name
		case len(s.Loosened) > 0:
			return "loosens-confinement", s.Name + ": " + strings.Join(s.Loosened, ", ")
		}
		upstream = upstream || s.Name == r.Service
	}
	for _, n := range p.Networks {
		switch {
		case n.Name == appunit.UpstreamNetwork:
			return "network-not-its-own", n.Name
		case n.Joined != "":
			return "network-not-its-own", n.Name
		case n.Driver != "" && n.Driver != "bridge", n.Configured:
			return "network-configured", n.Name
		}
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
	check := r.URL.Query().Get("check") == "true"
	var oidc *appunit.OIDC
	if req.OIDC != nil {
		a.mu.Lock()
		var sites *EdgeSites
		if a.state.Edge != nil {
			e := a.state.Edge.EdgeSites
			sites = &e
		}
		a.mu.Unlock()
		var code string
		if oidc, code = oidcEnv(req, sites, check); code != "" {
			a.log.Warn("refused an app's OIDC client", "app", id, "why", code)
			fail(w, http.StatusBadRequest, code)
			return
		}
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
		code := "invalid-compose"
		if errors.Is(err, compose.ErrOffFloor) {
			code = "off-floor"
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": code})
		return
	}
	a.mu.Lock()
	signIn := ""
	if e := a.state.Edge; e != nil {
		signIn = e.SignIn // whether this node serves it yet or not
	}
	a.mu.Unlock()
	if code, detail := checkProject(p, req, signIn); code != "" {
		a.log.Warn("refused an app's compose project", "app", id, "why", code, "service", detail)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": code, "detail": detail})
		return
	}
	// Only checking (?check=true): the app would install. The manager asks this before it makes the app's guard.
	if check {
		writeJSON(w, http.StatusOK, map[string]bool{"installable": true})
		return
	}

	a.mu.Lock()
	if other := a.nameTaken(id, req.Name); other != "" {
		a.mu.Unlock()
		a.log.Warn("refused an app's name: another site has it", "app", id, "name", req.Name, "site", other)
		fail(w, http.StatusConflict, "name-taken")
		return
	}
	next, err := a.placeApp(id, prev)
	if err != nil {
		a.mu.Unlock()
		a.log.Error("placing an app", "app", id, "err", err)
		fail(w, http.StatusInsufficientStorage, "no-room")
		return
	}
	next.Product, next.Plan, next.Name, next.Service, next.Guard, next.OIDC = req.Product, req.Plan, req.Name, req.Service, req.Guard, oidc
	next.Refused = nil
	if req.OIDC != nil {
		next.Refused = slices.Clone(req.OIDC.RefusePaths)
	}
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

	// From here the install runs to its end whatever the manager does: a request given up on never leaves it halfway.
	ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), installWait)
	defer cancel()
	var clientSecret []byte
	if req.OIDC != nil {
		clientSecret = []byte(req.OIDC.ClientSecret)
	}
	if err := a.writeApp(ctx, u, []byte(req.Compose), clientSecret); err != nil {
		a.log.Error("writing an app's unit", "app", id, "err", err)
		// The app as it was: an app the agent keeps is one whose unit it wrote, and the firewall table and the edge open
		// only to those. A new one's unit, if it got that far, is stopped.
		a.mu.Lock()
		if prev == nil {
			delete(a.state.Apps, id)
		} else {
			a.state.Apps[id] = prev
		}
		if err := a.save(); err != nil {
			a.log.Error("saving the agent's state", "err", err)
		}
		a.mu.Unlock()
		if prev == nil {
			_ = a.run(ctx, "systemctl", "disable", "--now", appunit.Unit(id))
		}
		fail(w, http.StatusInternalServerError, "unit-failed")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// The app is installed: its unit runs it. The upstream opens to the edge (and the agent) only once the table says
	// so, and the edge serves the name after; what doesn't take now, the watch puts right (both fail closed), and the
	// answer says so, so the household keeps the app it has.
	st := a.appStatusLocked(id, next)
	a.listen(ctx) // the first app: the sign-in listener's address, which the table opens to the apps
	if err := a.reconcile(ctx, true); err != nil {
		a.log.Error("letting the edge reach the app; the watch tries again", "app", id, "err", err)
		st.Pending = append(st.Pending, "firewall")
	} else if err := a.pushEdge(ctx); err != nil {
		a.log.Error("serving the app's name on the edge; the watch tries again", "app", id, "err", err)
		st.Pending = append(st.Pending, "edge")
	}
	a.log.Info("an app is installed: its unit runs it", "app", id, "plan", req.Plan, "unit", appunit.Unit(id), "upstream", next.Upstream)
	writeJSON(w, http.StatusAccepted, st)
}

// nameTaken is the site that already serves name, if another does: Admin, the sign-in, or another app. The caller
// holds a.mu.
func (a *Agent) nameTaken(id, name string) string {
	name = strings.ToLower(name)
	if e := a.state.Edge; e != nil {
		switch name {
		case strings.ToLower(e.Admin):
			return "admin"
		case strings.ToLower(e.SignIn):
			return "sign-in"
		}
	}
	for other, s := range a.state.Apps {
		if other != id && strings.ToLower(s.Name) == name {
			return other
		}
	}
	return ""
}

// placeApp is the app's level and upstream network: the ones it has, or the first free ones. The caller holds a.mu.
func (a *Agent) placeApp(id string, prev *appState) (*appState, error) {
	if prev != nil {
		next := *prev
		return &next, nil
	}
	levels, nets := map[string]bool{}, map[netip.Prefix]bool{signInNet: true, listenerNet: true}
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

// writeApp writes an app's files, seals its secrets (once) and its OIDC client's (at every apply: the client may have
// been made again), and has systemd run its unit.
func (a *Agent) writeApp(ctx context.Context, u appunit.App, file, clientSecret []byte) error {
	if err := os.MkdirAll(filepath.Join(u.Dir, "credentials"), 0o700); err != nil {
		return err
	}
	if u.OIDC != nil {
		if err := a.sealAt(ctx, u.CredentialFile(appunit.ClientSecret), appunit.Credential(u.ID, appunit.ClientSecret), clientSecret); err != nil {
			return fmt.Errorf("sealing the client's secret: %w", err)
		}
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

// engineWait is how long the agent waits for the engine's answer; statusWait how long one app's checks take at most,
// all apps' checked a few at once; AppsDeadline how long the apps' status takes at most.
const (
	engineWait   = 10 * time.Second
	statusWait   = hostWait + time.Second
	AppsDeadline = engineWait + 2*statusWait
	// installWait bounds an install once its checks passed: sealing, the unit, the firewall table and the edge.
	installWait = 2 * time.Minute
	// InstallDeadline is how long an install takes at most: the engine's answer, the compose read, then the rest.
	InstallDeadline = engineWait + ReadDeadline + installWait
)

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
	states, engineErr := a.cfg.Engine.States(asked)
	cancel()
	if engineErr != nil {
		a.log.Warn("asking the engine for its projects", "err", engineErr)
	}
	out := AppsStatus{Apps: make([]AppStatus, len(apps)), Drift: []AppDrift{}}
	ids := slices.Sorted(maps.Keys(apps))
	// Each app's unit and checks, a few apps at once, each in its own time: a slow one doesn't hold up the others.
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxStatusAsks)
	for i, id := range ids {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(ctx, statusWait)
			defer cancel()
			s := apps[id]
			st := a.appStatusLocked(id, &s)
			st.Active = a.unitActive(ctx, appunit.Unit(id))
			if engineErr == nil {
				st.Containers = orNone(states[st.Project].Containers)
			}
			st.Health, st.Checks = a.health(ctx, &s, st, engineErr == nil)
			out.Apps[i] = st
		})
	}
	wg.Wait()
	// A project whose unit doesn't run it: an app's, or the sign-in's own (the unit the package ships).
	active := map[string]bool{}
	for _, st := range out.Apps {
		active[st.Project] = st.Active
	}
	if _, there := states[signInProject]; there {
		ctx, cancel := context.WithTimeout(ctx, statusWait)
		active[signInProject] = a.unitActive(ctx, SignInUnit)
		cancel()
	}
	for _, name := range slices.Sorted(maps.Keys(states)) {
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

// maxStatusAsks is how many apps' status is asked at once.
const maxStatusAsks = 4

// signInProject is the compose project of the sign-in's unit.
var signInProject = appunit.Project("authentik")

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
			// Every container of the service counts: the check is as good as its worst one.
			c := CheckResult{Service: h.Service, Kind: "compose", OK: true}
			found := false
			for _, ct := range st.Containers {
				if ct.Service != h.Service {
					continue
				}
				if !found || rank(ct.Health) > rank(c.Detail) {
					c.Detail = ct.Health
				}
				found = true
				c.OK = c.OK && ct.State == "running" && ct.Health == "healthy"
			}
			c.OK = c.OK && found
			switch {
			case c.Detail == "unhealthy":
				worse("unhealthy")
			case !c.OK:
				worse("starting")
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

// rank orders a container's health from best to worst: healthy, none yet or starting, unhealthy.
func rank(h string) int {
	switch h {
	case "healthy":
		return 0
	case "unhealthy":
		return 2
	}
	return 1
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
	for _, sec := range u.Handed() {
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
	// A secret inside something else that was base64-encoded (Basic's user:password, a token's claims) shows in no form
	// above: every run of base64 that decodes to text holding a secret goes too.
	return base64Run.ReplaceAllStringFunc(line, func(run string) string {
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			b, err := enc.DecodeString(run)
			if err != nil {
				continue
			}
			for _, s := range secrets {
				if len(s) > 0 && bytes.Contains(b, s) {
					return "…"
				}
			}
		}
		return run
	})
}

// base64Run is a run of base64 (either alphabet, padded or not) long enough to hold a secret.
var base64Run = regexp.MustCompile(`[A-Za-z0-9+/_-]{8,}={0,2}`)

// appsFirewall are the apps' upstreams, and the sign-in's, as the firewall table lets them be opened: each app's by the
// edge's user and root (the agent's probes), the sign-in's by the manager's and Home's too (their OIDC clients, #21). A
// user the machine doesn't have (yet) is left out, so the upstream is only root's. The caller holds a.mu.
func (a *Agent) appsFirewall() []firewall.Upstream {
	var edgeUIDs, signInUIDs []uint32
	if uid, ok := a.cfg.Users(edgeUser); ok {
		edgeUIDs = append(edgeUIDs, uid)
	}
	signInUIDs = slices.Clone(edgeUIDs)
	for _, u := range []string{managerUser, homeUser} {
		if uid, ok := a.cfg.Users(u); ok {
			signInUIDs = append(signInUIDs, uid)
		}
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

// edgeApps are the apps' sites on the edge, once the sign-in is served: a guarded app's behind its guard, an app that
// signs people in itself without one. The caller holds a.mu.
func (a *Agent) edgeApps() []edge.App {
	if a.state.SignIn == nil || a.state.Edge == nil || a.state.Edge.SignIn == "" {
		return nil // no sign-in to send anyone to: the app's name stays unserved, never unguarded
	}
	var out []edge.App
	for _, s := range a.state.Apps {
		switch {
		case s.Guard:
			out = append(out, edge.App{Name: s.Name, Dial: s.Upstream.String(), Guard: edge.AuthentikGuard(a.upstream())})
		case s.OIDC != nil:
			out = append(out, edge.App{Name: s.Name, Dial: s.Upstream.String(), SignsIn: true, Refused: s.Refused})
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
