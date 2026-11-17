package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/atomicfile"
	"github.com/ArjunRachithcf/homeward/internal/firewall"
	"github.com/ArjunRachithcf/homeward/internal/host"
	"github.com/ArjunRachithcf/homeward/internal/mesh"
	"github.com/ArjunRachithcf/homeward/internal/mesh/netbird"
	"github.com/ArjunRachithcf/homeward/internal/mesh/tailscale"
	"github.com/ArjunRachithcf/homeward/internal/node"
)

// JoinRequest asks the agent to join its node to a mesh. SetupKey is secret: it is never logged or answered.
type JoinRequest struct {
	Mesh          string `json:"mesh"`
	ManagementURL string `json:"managementUrl"`
	SetupKey      string `json:"setupKey"`
	Hostname      string `json:"hostname"`
	// SetupPort is the setup listener's port, the manager's own: the table's one LAN exception until it closes.
	SetupPort int `json:"setupPort,omitempty"`
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
	case req.SetupPort < 0 || req.SetupPort > 65535:
		fail(w, http.StatusBadRequest, "bad-setup-port")
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
		func() error { return a.writeAt(netbird.DropInPath, netbird.DropIn()) },
		// Its control socket root's alone (#200) from this start on, if the watch loop hasn't put it in place yet.
		func() error { return a.writeAt(netbird.ControlSocket.DropInPath, netbird.ControlSocket.DropIn) },
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
	next.Enforcing, next.Lost = true, false
	if req.SetupPort != 0 {
		next.SetupPort = req.SetupPort
	}
	if !slices.Contains(next.Mesh, netbird.Interface) {
		next.Mesh = append(slices.Clone(next.Mesh), netbird.Interface)
	}
	if !slices.Contains(next.WireGuard, netbird.WireGuardPort) {
		next.WireGuard = append(slices.Clone(next.WireGuard), netbird.WireGuardPort)
	}
	// The node's address on its mesh, as the mesh gives it now: a rejoin may bring another, and the old one is gone.
	next.MeshIPs = []netip.Addr{st.IP}
	// Kept first, so a restart always brings the table back; undone if the table can't be applied. A join rebuilds a
	// state that was lost.
	prev, prevErr := a.state, a.stateErr
	a.state, a.stateErr = next, nil
	if err := a.save(); err != nil {
		a.state, a.stateErr = prev, prevErr
		a.log.Error("saving the agent's state", "err", err)
		fail(w, http.StatusInternalServerError, "state-unsaved")
		return
	}
	if err := a.fw.Apply(ctx, a.firewallState(ctx)); err != nil {
		a.state, a.stateErr = prev, prevErr
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

// RootWrites are the folders, under the machine's root, where the agent writes: systemd drop-ins for the mesh clients,
// the edge, the sign-in, its LDAP outpost and its LDAPS port (#204), and apps' units (UnitsDir), which systemd links
// from there. Its unit makes them writable
// (ReadWritePaths, under ProtectSystem=strict) and tmpfiles.d makes them; writeAt refuses any other.
var RootWrites = []string{filepath.Dir(netbird.DropInPath), filepath.Dir(tailscale.ControlSocket.DropInPath),
	"etc/systemd/system/" + DefaultEdge.Unit + ".d", "etc/systemd/system/" + SignInUnit + ".d", "etc/systemd/system/" + LDAPUnit + ".d",
	"etc/systemd/system/" + LDAPSUnit + ".d", UnitsDir}

// writeAt writes a file under the machine's root, in one of RootWrites.
func (a *Agent) writeAt(rel string, b []byte) error {
	if !slices.Contains(RootWrites, filepath.Dir(rel)) {
		return fmt.Errorf("the agent writes nothing in /%s", filepath.Dir(rel))
	}
	return writeFile(filepath.Join(a.cfg.Root, rel), b)
}

// writeFile replaces path whole, on the disk before it returns, readable by NetBird's service (0644).
func writeFile(path string, b []byte) error {
	return atomicfile.WriteMode(path, b, 0o644, 0o755)
}

// FirewallStatus is the firewall table and the setup link's LAN listener.
type FirewallStatus struct {
	firewall.Status
	SetupListener SetupListener `json:"setupListener"`
	// StateUnreadable: the agent's saved state couldn't be read when it started (a power cut mid-write, a disk
	// error), so it knows nothing of its join until the next change it saves.
	StateUnreadable bool `json:"stateUnreadable"`
}

// SetupListener is the setup link's LAN listener in the firewall table.
type SetupListener struct {
	Port int  `json:"port"`
	Open bool `json:"open"`
}

func (a *Agent) firewallStatus() FirewallStatus {
	s := FirewallStatus{Status: a.fw.Status(), SetupListener: SetupListener{Port: a.setupPort(), Open: !a.state.SetupClosed},
		StateUnreadable: a.stateErr != nil || a.state.Lost}
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
	a.refreshBridges(r.Context(), true)
	if err := a.checkNATMapper(r.Context(), true); err != nil {
		a.log.Error("checking NetBird's NAT mapper", "err", err)
	}
	if err := a.checkMeshSockets(r.Context(), true); err != nil {
		a.log.Error("checking the mesh clients' control sockets", "err", err)
	}
	ctx, cancel := context.WithTimeout(r.Context(), hostWait)
	defer cancel()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.reconcile(ctx, true); err != nil {
		a.log.Error("checking the firewall table", "err", err)
		fail(w, http.StatusInternalServerError, "check-failed")
		return
	}
	writeJSON(w, http.StatusOK, a.firewallStatus())
}

// setupListener closes the setup link's LAN listener in the firewall table, for good: it never opens again. While
// it is open, the manager gives its port here (open, with the port): kept in the agent's state, for the table and for
// the next boot's closed one.
func (a *Agent) setupListener(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Open *bool `json:"open"`
		Port *int  `json:"port"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil || req.Open == nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	if req.Port != nil && (*req.Port <= 0 || *req.Port > 65535 || !*req.Open) {
		fail(w, http.StatusBadRequest, "bad-setup-port")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if *req.Open {
		if a.state.SetupClosed {
			fail(w, http.StatusConflict, "closed-for-good")
			return
		}
		if req.Port != nil && *req.Port != a.state.SetupPort {
			prev := a.state.SetupPort
			a.state.SetupPort = *req.Port
			if err := a.save(); err != nil {
				a.state.SetupPort = prev
				a.log.Error("saving the agent's state", "err", err)
				fail(w, http.StatusInternalServerError, "state-unsaved")
				return
			}
			if a.state.Enforcing {
				want := a.firewallState(r.Context())
				if err := a.fw.Apply(r.Context(), want); err != nil {
					a.fw.Want(want)
					a.log.Error("moving the setup listener in the firewall table", "err", err)
					fail(w, http.StatusInternalServerError, "firewall-failed")
					return
				}
			}
		}
		writeJSON(w, http.StatusOK, a.firewallStatus())
		return
	}
	// Closed for good, saved first; the table follows, and a retry applies it again until it holds. Closing it again
	// (the manager says so at every run) leaves an applied table alone.
	wasClosed := a.state.SetupClosed
	if !wasClosed {
		a.state.SetupClosed = true
		if err := a.save(); err != nil {
			a.state.SetupClosed = false
			a.log.Error("saving the agent's state", "err", err)
			fail(w, http.StatusInternalServerError, "state-unsaved")
			return
		}
		a.log.Info("the setup link's LAN listener is closed, for good")
	}
	if a.state.Enforcing && (!wasClosed || !a.fw.Applied()) {
		want := a.firewallState(r.Context())
		if err := a.fw.Apply(r.Context(), want); err != nil {
			a.fw.Want(want)
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

// deleteCredential removes one of the manager's credentials (a new NetBird's setup token, once its work is done).
func (a *Agent) deleteCredential(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !managerCredential.MatchString(name) {
		fail(w, http.StatusNotFound, "not-found")
		return
	}
	if err := os.Remove(a.credentialPath(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.log.Error("removing a credential", "name", name, "err", err)
		fail(w, http.StatusInternalServerError, "remove-failed")
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
	if err := a.sealNew(ctx, path, name, value); err != nil {
		return err
	}
	return atomicfile.Commit(path + ".new")
}

// sealNew seals a value under its name at path+".new", for the caller to commit.
func (a *Agent) sealNew(ctx context.Context, path, name string, value []byte) error {
	key := "host"
	if a.cfg.Sysroot != nil {
		// TPM 2.0 only: systemd-creds can't seal to a TPM 1.2.
		if t := node.ReadTPM(a.cfg.Sysroot); t.Present && t.Version == "2.0" {
			key = "host+tpm2"
		}
	}
	if _, err := a.cfg.Run(ctx, host.Cmd{Name: "systemd-creds",
		Args: []string{"encrypt", "--with-key=" + key, "--name=" + name, "-", path + ".new"}, Stdin: value}); err != nil {
		return err
	}
	return nil
}

// MeshStatus is the node's mesh client, as the agent finds it right now.
type MeshStatus struct {
	Joined    bool   `json:"joined"`
	Interface string `json:"interface,omitempty"`
	// PortMappingOff: the client's drop-in is as the agent wrote it, and the running service has its environment.
	PortMappingOff bool             `json:"portMappingOff"`
	Drift          []firewall.Drift `json:"drift"`
}

func (a *Agent) getMesh(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	s := MeshStatus{Joined: slices.Contains(a.state.Mesh, netbird.Interface), Drift: slices.Clone(a.meshDrift)}
	a.mu.Unlock()
	if s.Drift == nil {
		s.Drift = []firewall.Drift{}
	}
	if s.Joined {
		s.Interface = netbird.Interface
		ctx, cancel := context.WithTimeout(r.Context(), hostWait)
		defer cancel()
		dropIn, env, _ := a.natMapper(ctx)
		s.PortMappingOff = dropIn && env
	}
	writeJSON(w, http.StatusOK, s)
}

// natMapper says whether NetBird's drop-in is as the agent writes it, and whether the running service has its
// environment.
//
// The agent can't read the running process's environment: /proc/<pid>/environ of a root process with every capability
// is refused to a reader without them all (the kernel's ptrace check), and the agent's unit hides other processes
// (ProtectProc=invisible). So it asks systemd: the service has the drop-in's environment when systemd lists the
// drop-in, has nothing to reload, and the main process started after the drop-in's last write.
//
// unknown: systemd couldn't say (before 248 it refuses --timestamp=us+utc, or its answer isn't understood). The agent
// then neither claims the setting is applied nor restarts the client for it.
func (a *Agent) natMapper(ctx context.Context) (dropIn, env, unknown bool) {
	path := filepath.Join(a.cfg.Root, netbird.DropInPath)
	b, err := os.ReadFile(path)
	dropIn = err == nil && string(b) == string(netbird.DropIn())
	fi, err := os.Stat(path)
	if !dropIn || err != nil {
		return dropIn, false, false
	}
	out, err := a.cfg.Run(ctx, host.Cmd{Name: "systemctl", Args: []string{"show", "netbird.service", "--timestamp=us+utc",
		"--property=DropInPaths,NeedDaemonReload,ExecMainStartTimestamp"}})
	if err != nil {
		return dropIn, false, true
	}
	props := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			props[k] = strings.TrimSpace(v)
		}
	}
	started, err := time.Parse("Mon 2006-01-02 15:04:05.000000 MST", props["ExecMainStartTimestamp"])
	if err != nil {
		return dropIn, false, true
	}
	return dropIn, props["NeedDaemonReload"] == "no" &&
		slices.Contains(strings.Fields(props["DropInPaths"]), "/"+netbird.DropInPath) &&
		!started.Before(fi.ModTime().Truncate(time.Microsecond)), false
}

// checkNATMapper puts NetBird's drop-in back, and queues the client's restart with it, when either drifted; the drift
// is reported. It runs outside the agent's lock, each host command for hostWait at most, so a hanging systemd never
// holds the agent; force checks now, otherwise every natEvery.
func (a *Agent) checkNATMapper(ctx context.Context, force bool) error {
	a.mu.Lock()
	due := slices.Contains(a.state.Mesh, netbird.Interface) && (force || a.cfg.Now().Sub(a.natAt) >= natEvery)
	if due {
		a.natAt = a.cfg.Now()
	}
	a.mu.Unlock()
	if !due {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, hostWait)
	defer cancel()
	dropIn, env, unknown := a.natMapper(ctx)
	if unknown {
		a.mu.Lock()
		defer a.mu.Unlock()
		if !a.natUnknown {
			a.natUnknown = true
			a.log.Warn("systemd can't say whether NetBird's client has the setting: leaving it alone")
			a.meshDrift = append(a.meshDrift, firewall.Drift{At: a.cfg.Now(), What: "nat-mapper-unknown"})
		}
		return nil
	}
	a.mu.Lock()
	a.natUnknown = false
	a.mu.Unlock()
	if dropIn && env {
		return nil
	}
	what := "nat-mapper-drop-in"
	if dropIn {
		what = "nat-mapper-environment"
	}
	a.log.Warn("NetBird's client could ask the router for a port: putting its setting back", "what", what)
	for _, step := range []func() error{
		func() error { return a.writeAt(netbird.DropInPath, netbird.DropIn()) },
		func() error { return a.run(ctx, "systemctl", "daemon-reload") },
		// Queued, not waited for: the watch loop never blocks on a systemd job.
		func() error { return a.run(ctx, "systemctl", "--no-block", "restart", "netbird.service") },
	} {
		if err := step(); err != nil {
			return fmt.Errorf("putting NetBird's NAT mapper setting back: %w", err)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.noteMeshDrift(what)
	return nil
}

// noteMeshDrift records what the agent found changed in the mesh clients, and put back. The caller holds a.mu.
func (a *Agent) noteMeshDrift(what string) {
	a.meshDrift = append(a.meshDrift, firewall.Drift{At: a.cfg.Now(), What: what})
	if len(a.meshDrift) > 100 {
		a.meshDrift = a.meshDrift[len(a.meshDrift)-100:]
	}
}

// meshSockets are the mesh clients' control sockets, which the agent keeps for root only (#200), whether or not the
// node has joined a mesh: a client's daemon that runs at all answers on it.
var meshSockets = []mesh.ControlSocket{netbird.ControlSocket, tailscale.ControlSocket}

// checkMeshSockets keeps each mesh client's control socket where only root may connect to it.
//   - A drop-in whose settings changed is put back, and a running client restarted with it (systemd makes the folder
//     again, with the drop-in's mode, at every start). One whose comments alone changed is rewritten, nothing more.
//   - A socket open to others is closed the same way: systemd reloaded (a reload that failed earlier is made up for),
//     the client restarted.
//
// Both are drift, but for the drop-ins written at the agent's first check, as a new install or an upgrade brings them.
// A socket still open a natEvery after its restart stays open, said once, and the client is restarted no more: no
// restart loop of the node's mesh connection. Like checkNATMapper, it runs each host command outside the agent's lock,
// for hostWait at most, and queues the restarts; force checks now, otherwise every natEvery.
func (a *Agent) checkMeshSockets(ctx context.Context, force bool) error {
	a.mu.Lock()
	first := a.sockAt.IsZero()
	due := force || first || a.cfg.Now().Sub(a.sockAt) >= natEvery
	if !due {
		a.mu.Unlock()
		return nil
	}
	a.sockAt = a.cfg.Now()
	if a.sockRestarted == nil {
		a.sockRestarted, a.sockStuck = map[string]time.Time{}, map[string]bool{}
	}
	// Decided under the lock, which a join holds throughout, so a check never decides on a join's half-done restart.
	var drift, restarts []string
	reload := false
	var errs []error
	for _, c := range meshSockets {
		running, open := a.socketState(c)
		b, err := os.ReadFile(filepath.Join(a.cfg.Root, c.DropInPath))
		if err != nil || string(b) != string(c.DropIn) {
			if err := a.writeAt(c.DropInPath, c.DropIn); err != nil {
				errs = append(errs, fmt.Errorf("writing %s's drop-in: %w", c.Unit, err))
				continue
			}
			if err == nil && settings(b) == settings(c.DropIn) {
				continue // its comments alone
			}
			reload = true
			if !first {
				drift = append(drift, c.Unit+"-socket-drop-in")
			}
			if running {
				restarts = append(restarts, c.Unit)
			}
			continue
		}
		if !open {
			delete(a.sockRestarted, c.Unit)
			delete(a.sockStuck, c.Unit)
			continue
		}
		at, restarted := a.sockRestarted[c.Unit]
		switch {
		case a.sockStuck[c.Unit]:
		case restarted && a.cfg.Now().Sub(at) < natEvery:
			// Restarted moments ago: systemd may not have started it yet.
		case restarted:
			a.sockStuck[c.Unit] = true
			a.log.Error("a mesh client's control socket stayed open to every user on the node after its restart; "+
				"leaving the client running", "unit", c.Unit)
			drift = append(drift, c.Unit+"-socket-stays-open")
		default:
			a.log.Warn("a mesh client's control socket was open to every user on the node: restarting it closed", "unit", c.Unit)
			drift = append(drift, c.Unit+"-socket-open")
			restarts = append(restarts, c.Unit)
			a.sockRestarted[c.Unit] = a.cfg.Now()
			reload = true
		}
	}
	for _, what := range drift {
		a.noteMeshDrift(what)
	}
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, hostWait)
	defer cancel()
	if reload {
		if err := a.run(ctx, "systemctl", "daemon-reload"); err != nil {
			return errors.Join(append(errs, err)...)
		}
	}
	for _, u := range restarts {
		// try-restart: a client that doesn't run stays stopped. Queued, not waited for, as the NAT mapper's.
		if err := a.run(ctx, "systemctl", "--no-block", "try-restart", u); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// settings are a unit file's lines but its comments and blank ones.
func settings(b []byte) string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") && !strings.HasPrefix(l, ";") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// socketState says whether a mesh client's daemon runs (its socket's folder is there, or something answers at
// NetBird's link), and whether its control socket is open to anyone but root: its folder not root's alone (mode 0700,
// the agent's own uid), or a listening socket at the CLI's path instead of the link (the daemon running without the
// drop-in). A socket file there that nothing answers on is a stopped daemon's leftover: no one can connect to it.
func (a *Agent) socketState(c mesh.ControlSocket) (running, open bool) {
	if fi, err := os.Lstat(filepath.Join(a.cfg.Root, c.Dir)); err == nil {
		running = true
		st, ok := fi.Sys().(*syscall.Stat_t)
		open = !fi.IsDir() || fi.Mode().Perm() != 0o700 || !ok || int(st.Uid) != os.Geteuid()
	}
	if c.Link == "" {
		return running, open
	}
	path := filepath.Join(a.cfg.Root, c.Link)
	fi, err := os.Lstat(path)
	switch {
	case err != nil:
	case fi.Mode()&fs.ModeSymlink != 0:
		if l, err := os.Readlink(path); err != nil || l != "/"+c.Socket {
			running, open = true, true
		}
	case fi.Mode()&fs.ModeSocket != 0:
		if conn, err := net.DialTimeout("unix", path, 100*time.Millisecond); err == nil {
			conn.Close()
			running, open = true, true
		}
	default:
		running, open = true, true
	}
	return running, open
}
