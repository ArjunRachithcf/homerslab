package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/firewall"
	"github.com/ArjunRachithcf/homerslab/internal/host"
	"github.com/ArjunRachithcf/homerslab/internal/mesh"
	"github.com/ArjunRachithcf/homerslab/internal/mesh/netbird"
	"github.com/ArjunRachithcf/homerslab/internal/node"
)

// JoinRequest asks the agent to join its node to a mesh. SetupKey is secret: it is never logged or answered.
type JoinRequest struct {
	Mesh          string `json:"mesh"`
	ManagementURL string `json:"managementUrl"`
	SetupKey      string `json:"setupKey"`
	Hostname      string `json:"hostname"`
}

// Joined is the node on its mesh.
type Joined struct {
	Interface string     `json:"interface"`
	IP        netip.Addr `json:"ip"`
	FQDN      string     `json:"fqdn"`
}

var label = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// join joins the node to the mesh, then turns the firewall table on: NetBird's client first gets its environment
// (NAT mapper off) and a restart, so it never asks the router for a port, not even once.
func (a *Agent) join(w http.ResponseWriter, r *http.Request) {
	var req JoinRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	switch {
	case req.Mesh != "netbird":
		fail(w, http.StatusBadRequest, "unknown-mesh")
		return
	case req.SetupKey == "" || len(req.SetupKey) > 256:
		fail(w, http.StatusBadRequest, "no-setup-key")
		return
	case !label.MatchString(req.Hostname):
		fail(w, http.StatusBadRequest, "bad-hostname")
		return
	}
	u, err := mesh.CheckURL(req.ManagementURL)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad-management-url")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	ctx := r.Context()
	n := netbird.Node{Run: a.cfg.Run}
	steps := []func() error{
		func() error { return writeFile(filepath.Join(a.cfg.Root, netbird.DropInPath), netbird.DropIn()) },
		func() error { return a.run(ctx, "systemctl", "daemon-reload") },
		func() error { return a.run(ctx, "systemctl", "enable", "netbird.service") },
		func() error { return a.run(ctx, "systemctl", "restart", "netbird.service") },
		func() error { return n.Up(ctx, u.String(), req.Hostname, mesh.Secret(req.SetupKey)) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			a.log.Error("joining the mesh", "err", err)
			fail(w, http.StatusBadGateway, "join-failed")
			return
		}
	}
	var st netbird.Status
	for deadline := a.cfg.Now().Add(a.cfg.JoinWait); ; {
		st, err = n.Status(ctx)
		if err == nil && st.Connected && st.IP.IsValid() {
			break
		}
		if !a.cfg.Now().Before(deadline) || ctx.Err() != nil {
			a.log.Error("the mesh's client never connected", "err", err)
			fail(w, http.StatusBadGateway, "not-connected")
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	// The machine's own resolver must still work beside the mesh's DNS: the control plane's name is one it has to
	// answer.
	if _, err := netip.ParseAddr(u.Hostname()); err != nil {
		if _, err := a.cfg.Resolve(ctx, u.Hostname()); err != nil {
			a.log.Error("the machine's resolver stopped answering after the join", "name", u.Hostname(), "err", err)
			fail(w, http.StatusBadGateway, "resolver-broken")
			return
		}
	}
	next := a.state
	next.Enforcing = true
	if !slices.Contains(next.Mesh, netbird.Interface) {
		next.Mesh = append(slices.Clone(next.Mesh), netbird.Interface)
	}
	if !slices.Contains(next.WireGuard, netbird.WireGuardPort) {
		next.WireGuard = append(slices.Clone(next.WireGuard), netbird.WireGuardPort)
	}
	if !slices.Contains(next.MeshIPs, st.IP) {
		next.MeshIPs = append(slices.Clone(next.MeshIPs), st.IP)
	}
	// Kept first, so a restart always brings the table back; undone if the table can't be applied.
	prev := a.state
	a.state = next
	if err := a.save(); err != nil {
		a.state = prev
		a.log.Error("saving the agent's state", "err", err)
		fail(w, http.StatusInternalServerError, "state-unsaved")
		return
	}
	if err := a.fw.Apply(ctx, a.firewallState()); err != nil {
		a.state = prev
		_ = a.save()
		a.log.Error("turning the firewall table on", "err", err)
		fail(w, http.StatusInternalServerError, "firewall-failed")
		return
	}
	a.log.Info("joined the mesh; the firewall table keeps the LAN out", "mesh", "netbird", "ip", st.IP)
	writeJSON(w, http.StatusOK, Joined{Interface: netbird.Interface, IP: st.IP, FQDN: st.FQDN})
}

func (a *Agent) run(ctx context.Context, name string, args ...string) error {
	_, err := a.cfg.Run(ctx, host.Cmd{Name: name, Args: args})
	return err
}

func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path+".new", b, 0o644); err != nil {
		return err
	}
	return os.Rename(path+".new", path)
}

// FirewallStatus is the firewall table and the setup link's LAN listener.
type FirewallStatus struct {
	firewall.Status
	SetupListener SetupListener `json:"setupListener"`
}

// SetupListener is the setup link's LAN listener in the firewall table.
type SetupListener struct {
	Port int  `json:"port"`
	Open bool `json:"open"`
}

func (a *Agent) firewallStatus() FirewallStatus {
	s := FirewallStatus{Status: a.fw.Status(), SetupListener: SetupListener{Port: a.cfg.SetupPort, Open: !a.state.SetupClosed}}
	if s.Drift == nil {
		s.Drift = []firewall.Drift{}
	}
	return s
}

func (a *Agent) getFirewall(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	writeJSON(w, http.StatusOK, a.firewallStatus())
}

// checkFirewall checks the table for drift now, and puts it back if it drifted.
func (a *Agent) checkFirewall(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.reconcile(r.Context()); err != nil {
		a.log.Error("checking the firewall table", "err", err)
		fail(w, http.StatusInternalServerError, "check-failed")
		return
	}
	writeJSON(w, http.StatusOK, a.firewallStatus())
}

// setupListener closes the setup link's LAN listener in the firewall table, for good: it never opens again.
func (a *Agent) setupListener(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Open *bool `json:"open"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil || req.Open == nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if *req.Open {
		if a.state.SetupClosed {
			fail(w, http.StatusConflict, "closed-for-good")
			return
		}
		writeJSON(w, http.StatusOK, a.firewallStatus())
		return
	}
	// Closed for good, saved first; the table follows, and a retry applies it again until it holds.
	if !a.state.SetupClosed {
		a.state.SetupClosed = true
		if err := a.save(); err != nil {
			a.state.SetupClosed = false
			a.log.Error("saving the agent's state", "err", err)
			fail(w, http.StatusInternalServerError, "state-unsaved")
			return
		}
		a.log.Info("the setup link's LAN listener is closed, for good")
	}
	if a.state.Enforcing {
		if err := a.fw.Apply(r.Context(), a.firewallState()); err != nil {
			a.fw.Want(a.firewallState())
			a.log.Error("closing the setup listener in the firewall table", "err", err)
			fail(w, http.StatusInternalServerError, "firewall-failed")
			return
		}
	}
	writeJSON(w, http.StatusOK, a.firewallStatus())
}

// The manager's own credentials (its mesh token, the self-hosted mesh owner's password) are systemd credentials,
// sealed by the agent (ADR 0011) and handed back only over this socket, to root and the manager's group. App
// credentials are never handed back: only names under "manager-" are.
var managerCredential = regexp.MustCompile(`^manager-[a-z0-9][a-z0-9-]{0,47}$`)

func (a *Agent) credentialPath(name string) string {
	return filepath.Join(a.cfg.StateDir, "credentials", name+".cred")
}

func (a *Agent) putCredential(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !managerCredential.MatchString(name) {
		fail(w, http.StatusNotFound, "not-found")
		return
	}
	var req struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil || req.Value == "" {
		fail(w, http.StatusBadRequest, "no-value")
		return
	}
	if err := a.seal(r.Context(), name, []byte(req.Value)); err != nil {
		a.log.Error("sealing a credential", "name", name, "err", err)
		fail(w, http.StatusInternalServerError, "seal-failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Agent) getCredential(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !managerCredential.MatchString(name) {
		fail(w, http.StatusNotFound, "not-found")
		return
	}
	path := a.credentialPath(name)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		fail(w, http.StatusNotFound, "not-found")
		return
	}
	out, err := a.cfg.Run(r.Context(), host.Cmd{Name: "systemd-creds", Args: []string{"decrypt", "--name=" + name, path, "-"}})
	if err != nil {
		a.log.Error("unsealing a credential", "name", name, "err", err)
		fail(w, http.StatusInternalServerError, "unseal-failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": string(out)})
}

// seal seals one of the manager's credentials.
func (a *Agent) seal(ctx context.Context, name string, value []byte) error {
	path := a.credentialPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return a.sealAt(ctx, path, name, value)
}

// unsealAt decrypts a credential the agent sealed at path, under its name.
func (a *Agent) unsealAt(ctx context.Context, path, name string) ([]byte, error) {
	return a.cfg.Run(ctx, host.Cmd{Name: "systemd-creds", Args: []string{"decrypt", "--name=" + name, path, "-"}})
}

// sealAt seals a value under its name at path, with the TPM where the machine has one, and the host key always:
// explicitly, never "auto" (ADR 0011).
func (a *Agent) sealAt(ctx context.Context, path, name string, value []byte) error {
	key := "host"
	if a.cfg.Sysroot != nil {
		if f, err := node.Read(a.cfg.Sysroot); err == nil && f.TPM.Present {
			key = "host+tpm2"
		}
	}
	if _, err := a.cfg.Run(ctx, host.Cmd{Name: "systemd-creds",
		Args: []string{"encrypt", "--with-key=" + key, "--name=" + name, "-", path + ".new"}, Stdin: value}); err != nil {
		return err
	}
	return os.Rename(path+".new", path)
}
