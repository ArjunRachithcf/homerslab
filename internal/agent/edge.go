package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/certs"
	"github.com/ArjunRachithcf/homerslab/internal/edge"
	"github.com/ArjunRachithcf/homerslab/internal/mesh"
)

// EdgeConfig is where the node's edge lives: the packaged unit homerslab-edge.service (packaging/systemd), which runs
// Caddy as its own unprivileged user.
type EdgeConfig struct {
	Unit string // homerslab-edge.service
	// AdminSocket is Caddy's admin API, in the edge's runtime directory (0750: the edge's user, and its group, which
	// only the agent is in).
	AdminSocket string
	// Storage is Caddy's data directory: it never holds a key.
	Storage string
	// Credentials is where the edge's unit finds its credentials once systemd has decrypted them.
	Credentials string
	// AdminUpstream is the manager's Admin socket, on the primary.
	AdminUpstream string
}

// The packaged edge.
var DefaultEdge = EdgeConfig{
	Unit:          "homerslab-edge.service",
	AdminSocket:   "/run/homerslab-edge/admin.sock",
	Storage:       "/var/lib/homerslab-edge",
	Credentials:   "/run/credentials/homerslab-edge.service",
	AdminUpstream: "/run/homerslab-admin.sock",
}

// EdgeSites are the edge's sites, as the manager wants them. None of it is secret.
type EdgeSites struct {
	// Port is the household's port (ADR 0021).
	Port int `json:"port"`
	// Admin is Admin's name, admin.<main domain>, on the primary; "" elsewhere.
	Admin string `json:"admin,omitempty"`
}

// edgeState is what the agent keeps of its edge, in agent.json.
type edgeState struct {
	EdgeSites
	// Domains are the domains whose certificates the edge holds, as sealed credentials.
	Domains []string `json:"domains"`
	// Serving are the certificates the edge started with: those it serves.
	Serving map[string]EdgeCertificate `json:"serving,omitempty"`
}

// EdgeStatus is the edge as the agent finds it.
type EdgeStatus struct {
	EdgeSites
	Listen       []netip.Addr      `json:"listen"`
	Certificates []EdgeCertificate `json:"certificates"`
	// Running says whether the edge runs the configuration the agent wants.
	Running bool `json:"running"`
}

// EdgeCertificate is a certificate the edge holds.
type EdgeCertificate struct {
	Domain   string    `json:"domain"`
	Serial   string    `json:"serial"`
	NotAfter time.Time `json:"notAfter"`
}

var domainName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

func (a *Agent) edgeDir() string { return filepath.Join(a.cfg.StateDir, "edge") }

// credential IDs of a domain's certificate and key, as the edge's unit loads them.
func certID(domain string) string { return "tls-" + domain + ".crt" }
func keyID(domain string) string  { return "tls-" + domain + ".key" }

// edgeConfig is the edge's whole configuration, from what the agent keeps; ok is false while it can't serve yet.
func (a *Agent) edgeConfig() (cfg []byte, ok bool, err error) {
	e := a.state.Edge
	// Nothing to serve before a certificate and the household's port: the edge's boot configuration is all it needs.
	if e == nil || len(a.state.MeshIPs) == 0 || len(e.Domains) == 0 || e.Port == 0 {
		return nil, false, nil
	}
	c := edge.Config{AdminSocket: a.cfg.Edge.AdminSocket, Storage: a.cfg.Edge.Storage, Listen: a.state.MeshIPs, Port: e.Port}
	for _, d := range e.Domains {
		c.Certificates = append(c.Certificates, edge.Certificate{Domain: d,
			CertFile: filepath.Join(a.cfg.Edge.Credentials, certID(d)), KeyFile: filepath.Join(a.cfg.Edge.Credentials, keyID(d))})
	}
	if e.Admin != "" {
		c.Admin = &edge.Site{Name: e.Admin, Upstream: a.cfg.Edge.AdminUpstream}
	}
	b, err := edge.Render(c)
	return b, err == nil, err
}

// pushEdge loads the configuration into the edge, waiting a while for an edge that is starting. The caller holds a.mu.
func (a *Agent) pushEdge(ctx context.Context) error {
	cfg, ok, err := a.edgeConfig()
	if err != nil || !ok {
		return err
	}
	c := edge.NewClient(a.cfg.Edge.AdminSocket)
	ctx, cancel := context.WithTimeout(ctx, min(a.cfg.JoinWait, 15*time.Second)) // an edge that is starting
	defer cancel()
	for {
		if err = c.Load(ctx, cfg); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// reconcileEdge pushes the configuration again when the edge doesn't run it: it started again (its boot
// configuration has no sites, and autosave is off), or something changed it. The caller holds a.mu.
func (a *Agent) reconcileEdge(ctx context.Context) error {
	cfg, ok, err := a.edgeConfig()
	if err != nil || !ok {
		return err
	}
	c := edge.NewClient(a.cfg.Edge.AdminSocket)
	running, err := c.Running(ctx, cfg)
	if err != nil || running {
		return err
	}
	if err := c.Load(ctx, cfg); err != nil {
		return err
	}
	a.log.Info("the edge didn't run its configuration (it restarted, or drifted); the agent pushed it again")
	return nil
}

func (a *Agent) putEdge(w http.ResponseWriter, r *http.Request) {
	var req EdgeSites
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	if req.Port <= 0 || req.Port > 65535 || (req.Admin != "" && !domainName.MatchString(req.Admin)) {
		fail(w, http.StatusBadRequest, "bad-sites")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	next := edgeState{}
	if a.state.Edge != nil {
		next = *a.state.Edge
	}
	next.EdgeSites = req
	a.saveEdge(w, r, next, nil)
}

// putEdgeCertificate takes a domain's certificate and key from the manager, over this socket only. They become
// systemd credentials sealed by the agent, which the edge's unit loads as files: the key is never in the edge's
// configuration, its admin API or its storage (#78).
func (a *Agent) putEdgeCertificate(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	if !domainName.MatchString(domain) {
		fail(w, http.StatusNotFound, "not-found")
		return
	}
	var req struct {
		Chain string `json:"chain"`
		Key   string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	c, err := certs.Parse(domain, []byte(req.Chain), mesh.Secret(req.Key))
	if err != nil {
		a.log.Warn("refused a certificate for the edge", "domain", domain, "err", err)
		fail(w, http.StatusBadRequest, "bad-certificate")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(a.edgeDir(), 0o700); err != nil {
		a.log.Error("the edge's credentials", "err", err)
		fail(w, http.StatusInternalServerError, "seal-failed")
		return
	}
	for id, v := range map[string][]byte{certID(domain): c.Chain, keyID(domain): []byte(c.Key.Reveal())} {
		if err := a.sealAt(r.Context(), filepath.Join(a.edgeDir(), id+".cred"), id, v); err != nil {
			a.log.Error("sealing the edge's certificate", "domain", domain, "err", err)
			fail(w, http.StatusInternalServerError, "seal-failed")
			return
		}
	}
	next := edgeState{}
	if a.state.Edge != nil {
		next = *a.state.Edge
	}
	if !slices.Contains(next.Domains, domain) {
		next.Domains = append(slices.Clone(next.Domains), domain)
		slices.Sort(next.Domains)
	}
	a.log.Info("the edge has a new certificate", "domain", domain, "serial", c.Serial, "notAfter", c.NotAfter)
	a.saveEdge(w, r, next, &EdgeCertificate{Domain: domain, Serial: c.Serial, NotAfter: c.NotAfter})
}

// saveEdge keeps the edge's new state, and puts it in place: with a new certificate, the unit's credentials and a
// restart (systemd hands them over only as the unit starts), then the whole configuration. The caller holds a.mu.
func (a *Agent) saveEdge(w http.ResponseWriter, r *http.Request, next edgeState, started *EdgeCertificate) {
	prev := a.state.Edge
	a.state.Edge = &next
	if err := a.save(); err != nil {
		a.state.Edge = prev
		a.log.Error("saving the agent's state", "err", err)
		fail(w, http.StatusInternalServerError, "state-unsaved")
		return
	}
	ctx := r.Context()
	if started != nil {
		steps := []func() error{
			func() error { return writeFile(filepath.Join(a.cfg.Root, a.edgeDropIn()), a.edgeCredentials()) },
			func() error { return a.run(ctx, "systemctl", "daemon-reload") },
			func() error { return a.run(ctx, "systemctl", "enable", a.cfg.Edge.Unit) },
			func() error { return a.run(ctx, "systemctl", "restart", a.cfg.Edge.Unit) },
		}
		for _, step := range steps {
			if err := step(); err != nil {
				a.log.Error("restarting the edge with its certificates", "err", err)
				fail(w, http.StatusInternalServerError, "edge-failed")
				return
			}
		}
		serving := maps.Clone(a.state.Edge.Serving)
		if serving == nil {
			serving = map[string]EdgeCertificate{}
		}
		serving[started.Domain] = *started
		a.state.Edge.Serving = serving
		if err := a.save(); err != nil {
			a.log.Error("saving the agent's state", "err", err)
		}
	}
	if len(a.state.MeshIPs) == 0 {
		fail(w, http.StatusConflict, "not-on-the-mesh") // kept: the edge serves once the node has joined
		return
	}
	if err := a.pushEdge(ctx); err != nil {
		a.log.Error("loading the edge's configuration", "err", err)
		fail(w, http.StatusBadGateway, "edge-unreachable")
		return
	}
	writeJSON(w, http.StatusOK, a.edgeStatus(ctx))
}

func (a *Agent) edgeDropIn() string {
	return filepath.Join("etc/systemd/system", a.cfg.Edge.Unit+".d", "50-homerslab-certificates.conf")
}

// edgeCredentials is the edge unit's drop-in: each domain's certificate and key, sealed by the agent, decrypted by
// systemd into the unit's own credentials directory as it starts.
func (a *Agent) edgeCredentials() []byte {
	var b strings.Builder
	b.WriteString("# Written by HomersLab's agent: the edge's certificates, sealed by the agent (ADR 0011).\n[Service]\n")
	for _, d := range a.state.Edge.Domains {
		for _, id := range []string{certID(d), keyID(d)} {
			fmt.Fprintf(&b, "LoadCredentialEncrypted=%s:%s\n", id, filepath.Join(a.edgeDir(), id+".cred"))
		}
	}
	return []byte(b.String())
}

func (a *Agent) edgeStatus(ctx context.Context) EdgeStatus {
	s := EdgeStatus{Listen: slices.Clone(a.state.MeshIPs), Certificates: []EdgeCertificate{}}
	if s.Listen == nil {
		s.Listen = []netip.Addr{}
	}
	if a.state.Edge == nil {
		return s
	}
	s.EdgeSites = a.state.Edge.EdgeSites
	for _, d := range a.state.Edge.Domains {
		if c, ok := a.state.Edge.Serving[d]; ok {
			s.Certificates = append(s.Certificates, c)
		}
	}
	if cfg, ok, err := a.edgeConfig(); err == nil && ok {
		s.Running, _ = edge.NewClient(a.cfg.Edge.AdminSocket).Running(ctx, cfg)
	}
	return s
}

func (a *Agent) getEdge(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	writeJSON(w, http.StatusOK, a.edgeStatus(r.Context()))
}

// checkEdge pushes the edge's configuration again now if it doesn't run it.
func (a *Agent) checkEdge(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.reconcileEdge(r.Context()); err != nil {
		a.log.Error("checking the edge", "err", err)
		fail(w, http.StatusBadGateway, "edge-unreachable")
		return
	}
	writeJSON(w, http.StatusOK, a.edgeStatus(r.Context()))
}
