// Package agent is the agent's API: the process on every node that owns its host and runs as root. It reports the
// node's facts, joins the node to the mesh, owns the firewall table and corrects its drift, seals the manager's own
// credentials, and reads apps' compose projects for planning, each in a confined child of its own.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/atomicfile"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/engine/compose"
	"github.com/ArjunRachithcf/homerslab/internal/firewall"
	"github.com/ArjunRachithcf/homerslab/internal/host"
	"github.com/ArjunRachithcf/homerslab/internal/lan"
	"github.com/ArjunRachithcf/homerslab/internal/node"
)

// Sysroot is the system root the agent reads its facts from: "/" on a real machine.
type Sysroot = fs.FS

// Config is where the agent listens, what it reads, and how it reaches the host.
type Config struct {
	// Socket is the Unix socket of the agent's API. Only root and SocketGID may connect to it.
	Socket string
	// SocketGID is the manager's group.
	SocketGID int
	Sysroot   Sysroot
	// Root is the machine's root directory for what the agent writes there (systemd drop-ins): "/" on a real
	// machine. StateDir keeps the agent's own state and the sealed credentials.
	Root     string
	StateDir string
	// Run runs the host's tools: host.Exec on a real machine.
	Run host.Runner
	// Resolve looks a name up through the machine's own resolver; net.DefaultResolver's when nil.
	Resolve func(ctx context.Context, name string) ([]string, error)
	// Network is what the firewall table needs of the machine's network, from one reading of the kernel: the LAN's
	// interfaces and prefixes, and the uplinks (a VPN's tunnel too, through which apps reach the internet);
	// lan.ReadNetwork when nil. While it can't be read, the table keeps the last reading; before the first, it knows no
	// LAN and no way out for apps (#148).
	Network func() (lan.Network, error)
	// Bridges are the container engines' bridges, by engine and name; asked of Docker and Podman when nil. An engine
	// missing from the answer didn't answer (an error, a timeout): its last confirmed list is kept. Only names that
	// IsBridge confirms (the kernel's /sys/class/net/<name>/bridge, from Sysroot, when nil) are used, kept ones too.
	Bridges  func(ctx context.Context) map[string][]string
	IsBridge func(name string) bool
	// HasInterface says whether the kernel has an interface by that name (/sys/class/net/<name>, from Sysroot, when
	// nil). A saved bridge the kernel doesn't have yet (at boot) stays in the table, for the moment its engine makes it.
	HasInterface func(name string) bool
	// SetupPort is the setup link's LAN listener, the firewall table's one exception until it closes.
	SetupPort int
	// CheckEvery is how often the firewall table is checked for drift: often, so an `nft flush ruleset` is undone
	// within seconds.
	CheckEvery time.Duration
	// JoinWait is how long a join waits for the mesh's client to connect.
	JoinWait time.Duration
	// Edge is where the node's edge lives; DefaultEdge when its Unit is empty.
	Edge EdgeConfig
	// SignInUpstream is the sign-in's server, host:port; SignInUpstream (the package's address) when empty.
	SignInUpstream string
	Now            func() time.Time
	Log            *slog.Logger
	// Executable is the agent's own program, which it runs as each compose read's child; os.Executable() when "".
	Executable string
	// Engine reads the apps' compose projects (#19): the Compose SDK on the environment's engine endpoint when nil (the
	// agent's unit sets none: Docker's own socket).
	Engine AppEngine
	// Users finds a user's uid by name, for the firewall table (the edge's and the manager's): /etc/passwd when nil.
	Users func(name string) (uint32, bool)
	// Restic is the pinned restic the node's backups run (backup.Restic when "").
	Restic string
	// Probe asks an app's upstream for a health check's path, as the edge would for the app's name: an HTTP request
	// when nil.
	Probe func(ctx context.Context, upstream netip.AddrPort, name, path string) (ok bool, detail string)
}

// Agent is a running agent.
type Agent struct {
	cfg Config
	log *slog.Logger
	fw  *firewall.Firewall

	mu         sync.Mutex // the state below, and joins
	state      state
	natUnknown bool            // the unknown NAT mapper state was reported, until systemd can say again
	natAt      time.Time       // when the mesh client's NAT mapper was last checked
	bridgesAt  time.Time       // when the engines were last asked for their bridges
	refused    map[string]bool // engine bridge names refused, reported once each
	// listenerUp: the sign-in listener's address is on the loopback; listenerFailed: putting it there failed, and
	// was reported.
	listenerUp, listenerFailed bool
	meshDrift                  []firewall.Drift // what the agent found changed in the mesh client, and put back
	reads                      *reads           // compose reads, one at a time
	installs                   sync.Mutex       // apps' installs, one at a time, outside mu
	backingUp                  sync.Mutex       // the node's backup runs, one at a time, outside mu
	edgeDown                   string           // the edge's last check's error, so a down edge is logged once
	network                    lan.Network      // the network's last reading, which the table keeps while the kernel can't be asked
	networkErr                 bool             // the network can't be read, which is logged once
	stateErr                   error            // why the saved state couldn't be read at start, if it couldn't
	brokenFile                 bool             // the state file is that unreadable one, not yet put aside
}

// state is what the agent keeps across restarts, in StateDir/agent.json. None of it is secret.
type state struct {
	// Enforcing: the firewall table is on, since the node joined its mesh.
	Enforcing bool     `json:"enforcing"`
	Mesh      []string `json:"mesh"`
	WireGuard []int    `json:"wireGuard"`
	// SetupClosed: the setup link's LAN listener was closed, for good. SetupPort is that listener's port, as the
	// manager gave it.
	SetupClosed bool `json:"setupClosed"`
	// MeshIPs are the node's addresses on its meshes, where its edge listens.
	MeshIPs []netip.Addr `json:"meshIps,omitempty"`
	// Edge is the edge's sites and certificates, once the manager has given them.
	Edge      *edgeState `json:"edge,omitempty"`
	SetupPort int        `json:"setupPort,omitempty"`
	// SignIn is the household account's provider, once the manager has installed it.
	SignIn *signInState `json:"signIn,omitempty"`
	// Apps are the apps installed on this node, by id (#19).
	Apps map[string]*appState `json:"apps,omitempty"`
	// Backup is the node's repository at the household's target, once the manager has given it (#22).
	Backup *backupState `json:"backup,omitempty"`
	// Bridges are the engines' bridges as each engine last confirmed them: what the table uses until an engine
	// answers again (at boot, before any of them runs; while one hangs).
	Bridges map[string][]string `json:"engineBridges,omitempty"`
	// OldBridges are the bridges an agent before #129 saved, all engines together: taken as Docker's until it
	// answers.
	OldBridges []string `json:"bridges,omitempty"`
	// Lost: the state couldn't be read once, and no join has rebuilt it since; what was saved after (the manager's
	// port, the listener closed) is kept, and the loss still reported.
	Lost bool `json:"lost,omitempty"`
}

// StateFile is the agent's state, in its state directory.
const StateFile = "agent.json"

// errUnparsable is a state file that was read, but isn't a state: cut short by a power cut, say. A file that can't be
// read at all (an I/O error, a wrong label) is another error: it may read again.
var errUnparsable = errors.New("not a whole state")

// readState reads the agent's state from dir: missing is the zero state.
func readState(dir string) (state, error) {
	var s state
	b, err := os.ReadFile(filepath.Join(dir, StateFile))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return state{}, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return state{}, fmt.Errorf("%s: %w: %w", StateFile, errUnparsable, err)
	}
	if len(s.OldBridges) > 0 && s.Bridges == nil {
		s.Bridges = map[string][]string{docker: s.OldBridges}
	}
	s.OldBridges = nil
	return s, nil
}

// SavedSetupPort is the setup listener's port for the first boot's closed table, from the agent's state in dir: the
// manager's own, as it last gave it to the agent; 0 once the listener closed for good, and while the state can't be
// read (it may read again, and say so). ok is false when the state doesn't say (no state yet, no port given, or a file
// that isn't a state): the caller's default applies then, since the manager's own listener refuses the LAN by itself
// once closed, and a machine without a mesh has no other way to reach first-time setup.
func SavedSetupPort(dir string) (port int, ok bool) {
	s, err := readState(dir)
	switch {
	case errors.Is(err, errUnparsable):
		return 0, false
	case err != nil: // it may read again, and may say closed for good: no exception until it does
		return 0, true
	case s.SetupClosed:
		return 0, true
	case s.SetupPort > 0 && s.SetupPort < 65536:
		return s.SetupPort, true
	}
	return 0, false
}

// New makes an agent and restores its firewall table, if it had one.
func New(ctx context.Context, c Config) (*Agent, error) {
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.Run == nil {
		c.Run = host.Exec
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Root == "" {
		c.Root = "/"
	}
	if c.CheckEvery == 0 {
		c.CheckEvery = 2 * time.Second
	}
	if c.JoinWait == 0 {
		c.JoinWait = 30 * time.Second
	}
	if c.Edge.Unit == "" {
		c.Edge = DefaultEdge
	}
	if c.Resolve == nil {
		c.Resolve = net.DefaultResolver.LookupHost
	}
	if c.Users == nil {
		c.Users = passwdUsers(passwdFile)
	}
	if c.Probe == nil {
		c.Probe = probe
	}
	if c.Engine == nil {
		e, err := compose.NewEngine("")
		if err != nil {
			return nil, err
		}
		c.Engine = e
	}
	if c.Network == nil {
		c.Network = lan.ReadNetwork
	}
	if c.Bridges == nil {
		run, root := c.Run, c.Sysroot
		c.Bridges = func(ctx context.Context) map[string][]string { return engineBridges(ctx, run, root) }
	}
	if c.IsBridge == nil {
		root := c.Sysroot
		c.IsBridge = func(name string) bool {
			if root == nil {
				return false
			}
			fi, err := fs.Stat(root, "sys/class/net/"+name+"/bridge")
			return err == nil && fi.IsDir()
		}
	}
	if c.HasInterface == nil {
		root := c.Sysroot
		c.HasInterface = func(name string) bool {
			if root == nil {
				return true // nothing to look at: no name is taken as one still to come
			}
			_, err := fs.Stat(root, "sys/class/net/"+name)
			return !errors.Is(err, fs.ErrNotExist)
		}
	}
	a := &Agent{cfg: c, log: c.Log, fw: &firewall.Firewall{Run: c.Run, Now: c.Now}, refused: map[string]bool{}, reads: newReads(c.Executable)}
	if c.StateDir != "" {
		a.fw.SavePath = filepath.Join(c.StateDir, "firewall.nft")
	}
	if c.StateDir != "" {
		if err := os.MkdirAll(c.StateDir, 0o700); err != nil {
			return nil, err
		}
		s, err := a.readStateWaiting(ctx)
		switch {
		case errors.Is(err, errUnparsable):
			// Starting anyway, and saying so, rather than failing again at every restart: the table homerslab-firewall-
			// restore loaded at boot stays as it is. The file is put aside before anything is saved over it.
			a.log.Error("the agent's state is lost: it starts without it, and leaves the firewall table as boot loaded it", "err", err)
			a.stateErr, a.brokenFile = err, true
		case err != nil:
			return nil, err
		}
		a.state = s
	}
	if a.state.Enforcing {
		// An agent that can't apply its table now keeps serving and tries again (Serve: before it says it's ready).
		want := a.firewallState(ctx)
		if err := a.fw.Apply(ctx, want); err != nil {
			a.log.Error("restoring the firewall table; trying again", "err", err)
			a.fw.Want(want)
		} else {
			a.log.Info("firewall table restored", "setupListener", !a.state.SetupClosed)
		}
	}
	return a, nil
}

// readStateWaiting reads the agent's state, trying again while the file can't be read (an I/O error may pass), until
// ctx ends: the agent never starts without a state it may still read, nor saves over it.
func (a *Agent) readStateWaiting(ctx context.Context) (state, error) {
	pause := 100 * time.Millisecond
	for tries := 0; ; tries++ {
		s, err := readState(a.cfg.StateDir)
		if err == nil || errors.Is(err, errUnparsable) {
			return s, err
		}
		if tries%20 == 0 {
			a.log.Error("the agent's state can't be read; trying again", "err", err)
		}
		select {
		case <-ctx.Done():
			return state{}, fmt.Errorf("the agent's state can't be read: %w", err)
		case <-time.After(pause):
		}
		pause = min(pause*2, 5*time.Second)
	}
}

// waitForTable returns once the firewall table is in place (at once when the node hasn't joined a mesh), trying
// again with a growing pause until ctx ends.
func (a *Agent) waitForTable(ctx context.Context) error {
	pause := 250 * time.Millisecond
	for {
		a.mu.Lock()
		if !a.state.Enforcing || a.fw.Applied() {
			a.mu.Unlock()
			return nil
		}
		err := a.reconcile(ctx, false)
		a.mu.Unlock()
		if err != nil {
			a.log.Error("applying the firewall table; trying again", "err", err, "in", pause)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pause):
		}
		pause = min(pause*2, 30*time.Second)
	}
}

// firewallState is the table as it should be now. It asks nothing of the engines: their bridges are the last ones
// confirmed (refreshBridges). The network is the kernel's, or its last reading while the kernel can't be asked: never
// an empty one taken for the real one, which would let apps reach the LAN's global prefixes (#148).
func (a *Agent) firewallState(context.Context) firewall.State {
	switch n, err := a.cfg.Network(); {
	case err != nil && !a.networkErr:
		a.log.Error("reading the network from the kernel; the firewall table keeps the last reading", "err", err)
		a.networkErr = true
	case err == nil:
		if a.networkErr {
			a.log.Info("the kernel answers again: the firewall table follows the network")
		}
		a.network, a.networkErr = n, false
	}
	n := a.network
	s := firewall.State{Mesh: a.state.Mesh, WireGuard: a.state.WireGuard, LAN: n.LAN, LANPrefixes: n.Prefixes,
		Uplinks4: n.Uplinks4, Uplinks6: n.Uplinks6, Virtual: n.Virtual, Bridges: a.bridges(), Resolvers: resolvers(a.cfg.Sysroot),
		Upstreams: a.appsFirewall()}
	if a.listening() && a.listenerUp {
		s.SignInListener = []netip.AddrPort{netip.AddrPortFrom(SignInListen, uint16(a.state.Edge.Port))}
	}
	if !a.state.SetupClosed {
		s.SetupPort = a.setupPort()
	}
	return s
}

// setupPort is the setup listener's port: the manager's, once it gave it, or the agent's configured one.
func (a *Agent) setupPort() int {
	if a.state.SetupPort != 0 {
		return a.state.SetupPort
	}
	return a.cfg.SetupPort
}

func (a *Agent) save() error {
	if a.cfg.StateDir == "" {
		return errors.New("the agent has no state directory")
	}
	path := filepath.Join(a.cfg.StateDir, StateFile)
	if a.brokenFile {
		// The first save after the state was lost, a join's too: the broken file is kept aside, as it was (a second
		// name for it, so it stays under its own until the new one replaces it).
		if err := keepAside(path, a.cfg.Now()); err != nil {
			return fmt.Errorf("putting the lost state aside: %w", err)
		}
	}
	if a.stateErr != nil {
		a.state.Lost = true // the loss is kept in the new file, until a join
	}
	b, err := json.Marshal(a.state)
	if err != nil {
		return err
	}
	if err := atomicfile.Write(path, b); err != nil {
		return err
	}
	a.brokenFile = false
	return nil
}

// keepAside links path under a name of its own, path.unreadable-<time>, never replacing one already there.
func keepAside(path string, now time.Time) error {
	base := path + ".unreadable-" + now.UTC().Format("20060102T150405.000000000Z")
	for i := 0; ; i++ {
		aside := base
		if i > 0 {
			aside = fmt.Sprintf("%s-%d", base, i)
		}
		err := os.Link(path, aside)
		switch {
		case err == nil, errors.Is(err, fs.ErrNotExist):
			return nil
		case !errors.Is(err, fs.ErrExist) || i >= 100:
			return err
		}
	}
}

// Handler serves the agent's API.
func (a *Agent) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/node", func(w http.ResponseWriter, _ *http.Request) {
		facts, err := node.Read(a.cfg.Sysroot)
		if err != nil {
			a.log.Error("reading the node's facts", "err", err)
			fail(w, http.StatusInternalServerError, "facts-unreadable")
			return
		}
		writeJSON(w, http.StatusOK, facts)
	})
	mux.HandleFunc("POST /v1/mesh/join", a.join)
	mux.HandleFunc("GET /v1/firewall", a.getFirewall)
	mux.HandleFunc("POST /v1/firewall/check", a.checkFirewall)
	mux.HandleFunc("PUT /v1/firewall/setup-listener", a.setupListener)
	mux.HandleFunc("PUT /v1/credentials/{name}", a.putCredential)
	mux.HandleFunc("GET /v1/credentials/{name}", a.getCredential)
	mux.HandleFunc("GET /v1/edge", a.getEdge)
	mux.HandleFunc("PUT /v1/edge", a.putEdge)
	mux.HandleFunc("POST /v1/edge/check", a.checkEdge)
	mux.HandleFunc("PUT /v1/edge/certificates/{domain}", a.putEdgeCertificate)
	mux.HandleFunc("DELETE /v1/credentials/{name}", a.deleteCredential)
	mux.HandleFunc("GET /v1/mesh", a.getMesh)
	mux.HandleFunc("PUT /v1/sign-in", a.putSignIn)
	mux.HandleFunc("GET /v1/sign-in", a.getSignIn)
	mux.HandleFunc("GET /v1/sign-in/logs", a.getSignInLogs)
	mux.HandleFunc("DELETE /v1/sign-in/bootstrap-token", a.deleteBootstrapToken)
	mux.HandleFunc("POST /v1/projects/read", a.readProject)
	mux.HandleFunc("PUT /v1/apps/{id}", a.putApp)
	mux.HandleFunc("GET /v1/apps", a.getApps)
	mux.HandleFunc("GET /v1/apps/{id}/logs", a.getAppLogs)
	mux.HandleFunc("PUT /v1/backups", a.putBackups)
	mux.HandleFunc("POST /v1/backups", a.postBackups)
	mux.HandleFunc("PUT /v1/backups/manager", a.putManagerBackup)
	mux.HandleFunc("POST /v1/backups/prune", a.postPrune)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { fail(w, http.StatusNotFound, "not-found") })
	return mux
}

// hostWait is how long the watch loop waits for any one of the host's tools, so a stuck engine or systemd job never
// holds the agent.
const hostWait = 3 * time.Second

// Watch keeps the firewall table and the mesh client as they should be until ctx ends.
func (a *Agent) Watch(ctx context.Context) {
	t := time.NewTicker(a.cfg.CheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.refreshBridges(ctx, false)
			if err := a.checkNATMapper(ctx, false); err != nil {
				a.log.Error("checking NetBird's NAT mapper", "err", err)
			}
			a.check(ctx)
		}
	}
}

func (a *Agent) check(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, hostWait)
	defer cancel()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.reconcile(ctx, false); err != nil {
		a.log.Error("checking the firewall table", "err", err)
	}
	// An edge that is down is said once, and again when it comes back: not at every check.
	err := a.reconcileEdge(ctx)
	switch {
	case err != nil && a.edgeDown != err.Error():
		a.log.Error("checking the edge; trying again at each check", "err", err)
		a.edgeDown = err.Error()
	case err == nil && a.edgeDown != "":
		a.log.Info("the edge answers again")
		a.edgeDown = ""
	}
}

// refreshBridges asks the engines for their bridges, outside the agent's lock and for hostWait at most, and keeps
// those the kernel confirms are bridges with names nft can take. A refused name is reported once, never rendered.
func (a *Agent) refreshBridges(ctx context.Context, force bool) {
	a.mu.Lock()
	due := a.state.Enforcing && (force || a.bridgesAt.IsZero() || a.cfg.Now().Sub(a.bridgesAt) >= bridgesEvery)
	a.mu.Unlock()
	if !due {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, hostWait)
	defer cancel()
	answered := a.cfg.Bridges(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bridgesAt = a.cfg.Now()
	// An engine that answered gives its list, of the names the kernel shows are bridges; one that didn't keeps the one
	// it last confirmed, as it was: at boot its bridges aren't in the kernel yet, and the table (bridges) takes each
	// as it appears.
	engines := slices.Collect(maps.Keys(a.state.Bridges))
	for e := range answered {
		engines = append(engines, e)
	}
	slices.Sort(engines)
	next := map[string][]string{}
	var refused []string
	for _, e := range slices.Compact(engines) {
		names, answer := answered[e]
		if !answer {
			if kept := a.state.Bridges[e]; len(kept) > 0 {
				next[e] = kept
			}
			continue
		}
		var good []string
		for _, n := range names {
			if firewall.ValidName(n) && a.cfg.IsBridge(n) {
				good = append(good, n)
			} else {
				refused = append(refused, n)
			}
		}
		slices.Sort(good)
		if good = slices.Compact(good); len(good) > 0 {
			next[e] = good
		}
	}
	for _, n := range refused {
		if !a.refused[n] {
			a.refused[n] = true
			a.log.Warn("an engine's bridge was refused: not a bridge, or a name nft can't take", "name", fmt.Sprintf("%q", n))
			a.fw.Note(fmt.Sprintf("refused-bridge %q", n))
		}
	}
	if !maps.EqualFunc(next, a.state.Bridges, slices.Equal) {
		a.state.Bridges = next
		if err := a.save(); err != nil {
			a.log.Error("saving the engines' bridges", "err", err)
		}
	}
}

// bridges are every engine's bridges, as last confirmed, that the kernel shows are bridges now or has no interface for
// yet (at boot, before the engine makes it): never one the kernel shows as something else. The caller holds a.mu.
func (a *Agent) bridges() []string {
	var out []string
	for _, names := range a.state.Bridges {
		for _, n := range names {
			if firewall.ValidName(n) && (a.cfg.IsBridge(n) || !a.cfg.HasInterface(n)) {
				out = append(out, n)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// bridgesEvery is how long the engines' list of bridges is taken as it was, between checks; a forced check asks again.
const bridgesEvery = 10 * time.Second

// natEvery is how often the mesh client's NAT mapper setting is checked; force checks it now.
const natEvery = 30 * time.Second

// reconcile keeps the firewall table as it should be: it follows the LAN and the engines' bridges as they change, and
// is put back when it drifted. It runs nft only, never an engine or a systemd job. The caller holds a.mu.
func (a *Agent) reconcile(ctx context.Context, force bool) error {
	var errs []error
	if a.state.Enforcing {
		if want := a.firewallState(ctx); !want.Equal(a.fw.Status().State) || !a.fw.Applied() {
			if err := a.fw.Apply(ctx, want); err != nil {
				a.fw.Want(want)
				return errors.Join(append(errs, err)...)
			}
		}
	}
	d, err := a.fw.Check(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	if d != nil {
		a.log.Warn("the firewall table had drifted, and was put back", "what", d.What)
	}
	return errors.Join(errs...)
}

// Serve runs the agent's API on its socket, and watches its firewall table, until ctx ends.
func Serve(ctx context.Context, c Config) error {
	a, err := New(ctx, c)
	if err != nil {
		return err
	}
	ln, err := listenUnix(c.Socket, c.SocketGID, 0o660)
	if err != nil {
		return err
	}
	watch, stop := context.WithCancel(ctx)
	defer stop()
	srv := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- serveUntil(ctx, srv, ln) }()
	// Nothing before READY asks an engine or waits on a systemd job: the engines start after the table is restored
	// (homerslab-firewall-restore), and a socket-activated engine would otherwise wait for the agent.
	// Ready only once the table is in place: systemd starts the network, the engines and the mesh client after this.
	watched := make(chan struct{})
	if err := a.waitForTable(ctx); err == nil {
		notifyReady(a.log)
		go func() { a.Watch(watch); close(watched) }()
	} else {
		close(watched)
	}
	err = <-served
	stop()
	<-watched // nothing of the agent's runs on after Serve
	return err
}

// notifyReady tells systemd the agent is up (Type=notify), when systemd asks.
func notifyReady(log *slog.Logger) {
	path := os.Getenv("NOTIFY_SOCKET")
	if path == "" {
		return
	}
	if path[0] == '@' {
		path = "\x00" + path[1:]
	}
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		log.Warn("telling systemd the agent is ready", "err", err)
		return
	}
	defer c.Close()
	if _, err := c.Write([]byte("READY=1")); err != nil {
		log.Warn("telling systemd the agent is ready", "err", err)
	}
}

// listenUnix listens on a fresh socket at path, owned by this process's user and gid, with mode perm.
func listenUnix(path string, gid int, perm os.FileMode) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("removing the old socket: %w", err)
	}
	// Born owner-only (connecting needs write), so nobody else can connect before it has its group and mode.
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chown(path, -1, gid); err != nil {
		ln.Close()
		return nil, fmt.Errorf("giving the socket to group %d: %w", gid, err)
	}
	if err := os.Chmod(path, perm); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func serveUntil(ctx context.Context, srv *http.Server, ln net.Listener) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			return err
		}
		if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// readProject reads an app's compose project for planning, in a confined child (reads).
func (a *Agent) readProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
		Compose string `json:"compose"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	p, err := a.reads.read(r.Context(), req.Project, []byte(req.Compose))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, p)
	case errors.Is(err, errBusy):
		fail(w, http.StatusServiceUnavailable, "busy")
	case errors.Is(err, errReadTimeout):
		a.log.Warn("a compose read ran out of time", "project", req.Project)
		fail(w, http.StatusGatewayTimeout, "read-timeout")
	case errors.Is(err, errReadTooBig):
		a.log.Warn("a compose read ran out of memory", "project", req.Project)
		fail(w, http.StatusUnprocessableEntity, "read-too-big")
	case errors.Is(err, errNoReader):
		a.log.Error("starting a compose read", "err", err)
		fail(w, http.StatusInternalServerError, "no-reader")
	case errors.Is(err, compose.ErrNotOurs):
		fail(w, http.StatusBadRequest, "not-ours")
	case errors.Is(err, compose.ErrNotPlain):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "not-plain", "detail": err.Error()})
	case errors.Is(err, compose.ErrOffFloor):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "off-floor", "detail": err.Error()})
	default:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid-compose", "detail": err.Error()})
	}
}

// ReadDeadline is how long a compose read may take before the agent stops it. The manager waits longer, so that it
// hears the agent's read-timeout.
const ReadDeadline = 10 * time.Second

var readDeadline = ReadDeadline

// readCPUSeconds is a read child's soft CPU limit: a second past the deadline, as a backstop for it.
var readCPUSeconds = func() int { return int(readDeadline/time.Second) + 1 }

// readCommand is how the agent starts a compose read: its executable (the one it runs, /proc/self/exe, even if a
// package has replaced the file since), in its read-project mode. Tests replace it.
var readCommand = func(exe string) ([]string, error) {
	if exe == "" {
		exe = "/proc/self/exe"
	}
	return []string{exe, "agent", "read-project"}, nil
}

// readArgv is the command a read's child runs. When it starts as cred (a root agent's read user), its program is an
// exec-only copy (execOnly), so that the kernel starts it not dumpable.
func readArgv(exe string, cred *syscall.Credential) ([]string, error) {
	argv, err := readCommand(exe)
	if err != nil || cred == nil {
		return argv, err
	}
	if argv[0], err = execOnly(argv[0]); err != nil {
		return nil, err
	}
	return argv, nil
}

// execOnlies are the exec-only copies the agent has made, by the program they copy.
var execOnlies = struct {
	sync.Mutex
	m map[string]execCopy
}{m: map[string]execCopy{}}

// execCopy is one exec-only copy: where it is, and its size.
type execCopy struct {
	path string
	size int64
}

// execOnly is a copy of the program src that a read's child may run but not read: the agent's own (root's), mode 0711,
// in a folder of its own that no one else may write: the unit's CacheDirectory (on disk, where a copy in /tmp would
// be held in memory on many distributions), or a temporary folder of mode 0711 where there is none. The kernel makes a
// process that runs a program it can't read not dumpable from the exec on (as it does for set-uid programs), and the
// child's IDs are set in the fork, which makes it not dumpable until the exec too. So a read's child is never
// dumpable, and no process of its user (one an earlier read left behind, stopped, even) can attach to it in the moment
// before it makes itself so, whatever Yama's ptrace_scope (#190).
//
// It is made once per program, and again when it is gone or changed: a cache may be emptied at any time, a temporary
// folder cleaned.
func execOnly(src string) (string, error) {
	execOnlies.Lock()
	defer execOnlies.Unlock()
	key := src + "\x00" + os.Getenv("CACHE_DIRECTORY")
	c, ok := execOnlies.m[key]
	if ok && stillExecOnly(c) {
		return c.path, nil
	}
	dir, err := execOnlyDir(c.path)
	if err != nil {
		return "", err
	}
	if c, err = copyProgram(src, dir); err != nil {
		return "", err
	}
	execOnlies.m[key] = c
	return c.path, nil
}

// execOnlyDir is the folder for a copy that was at was (if anywhere): the unit's CacheDirectory, if it is the agent's
// and no one else may write it, or a temporary folder of mode 0711, the one it was in if it is still so.
func execOnlyDir(was string) (string, error) {
	if dir := os.Getenv("CACHE_DIRECTORY"); dir != "" {
		var st syscall.Stat_t
		if err := syscall.Stat(dir, &st); err != nil {
			return "", err
		}
		if int(st.Uid) != os.Geteuid() || st.Mode&0o022 != 0 || st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
			return "", fmt.Errorf("%s isn't a folder only the agent may write", dir)
		}
		return dir, nil
	}
	if was != "" {
		var st syscall.Stat_t
		if dir := filepath.Dir(was); syscall.Lstat(dir, &st) == nil && int(st.Uid) == os.Geteuid() && st.Mode&0o7777 == 0o711 && st.Mode&syscall.S_IFMT == syscall.S_IFDIR {
			return dir, nil
		}
	}
	dir, err := os.MkdirTemp("", "homerslab-reader-")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o711); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// stillExecOnly says whether c is as the agent made it: a file of its own, mode 0711, of its size.
func stillExecOnly(c execCopy) bool {
	var st syscall.Stat_t
	if err := syscall.Lstat(c.path, &st); err != nil {
		return false
	}
	return st.Mode&syscall.S_IFMT == syscall.S_IFREG && st.Mode&0o7777 == 0o711 && int(st.Uid) == os.Geteuid() && st.Size == c.size
}

// copyProgram copies src into dir as read-project, mode 0711: written to a new file, then renamed over the old one, so
// that a child running the old one keeps it, and no file being written is ever run (which fails while it is open).
func copyProgram(src, dir string) (execCopy, error) {
	in, err := os.Open(src)
	if err != nil {
		return execCopy{}, err
	}
	defer in.Close()
	// No fork while the copy is open for writing: a child forked meanwhile would hold it until its own exec, and the
	// copy's exec would fail (ETXTBSY) while it does. Forks wait for the fork lock's readers.
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	out, err := os.CreateTemp(dir, ".read-project-")
	if err != nil {
		return execCopy{}, err
	}
	defer os.Remove(out.Name()) // once renamed, nothing
	n, err := io.Copy(out, in)
	if err == nil {
		err = out.Chmod(0o711)
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	p := filepath.Join(dir, "read-project")
	if err == nil {
		err = os.Rename(out.Name(), p)
	}
	if err != nil {
		return execCopy{}, err
	}
	return execCopy{path: p, size: n}, nil
}

// suidDumpableFile is fs.suid_dumpable. Tests replace it.
var suidDumpableFile = "/proc/sys/fs/suid_dumpable"

// execOnlyHolds says whether the kernel starts a program its process can't read not dumpable: with fs.suid_dumpable
// 0 or 2, not 1 ("debug"), where it leaves it dumpable as any other.
func execOnlyHolds() error {
	b, err := os.ReadFile(suidDumpableFile)
	if err != nil {
		return err
	}
	if v := strings.TrimSpace(string(b)); v != "0" && v != "2" {
		return fmt.Errorf("fs.suid_dumpable is %q, where a read's child would start dumpable", v)
	}
	return nil
}

// oomMu makes one read's child start at a time first in line for the OOM killer (startChild).
var oomMu sync.Mutex

// startChild starts a read's child first in line for the OOM killer (oom_score_adj 1000). The child can't set that
// itself: it starts not dumpable (execOnly), so its /proc files are root's. It inherits the agent's at the fork, so the
// agent raises its own for as long as the start takes, then puts it back, which needs no capability since it was no
// lower before. Another child the agent starts in that moment inherits it too, which only makes it the first killed.
func startChild(cmd *exec.Cmd) error {
	oomMu.Lock()
	defer oomMu.Unlock()
	const file = "/proc/self/oom_score_adj"
	was, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("oom_score_adj: %w", err)
	}
	if err := os.WriteFile(file, []byte("1000"), 0); err != nil {
		return fmt.Errorf("oom_score_adj: %w", err)
	}
	// Back to what it was, which can't fail for want of CAP_SYS_RESOURCE: the kernel refuses only a value below the
	// least one a process with that capability set, and the agent's was never below it.
	defer os.WriteFile(file, was, 0) //nolint:errcheck
	return cmd.Start()
}

var (
	errBusy        = errors.New("another compose read is running")
	errReadTimeout = errors.New("the compose read ran out of time")
	errReadTooBig  = errors.New("the compose read ran out of memory")
	errNoReader    = errors.New("the compose reader didn't start")
)

// reads runs compose reads one at a time, each in a child process (ReadProjectMain) that ends at the deadline
// (childCommand): the deadline really stops the work, and the slot is free again at once, or, for a child that is
// stopped, a WaitDelay later. The plain-file rules
// (engine/compose) bound how long a read can take; the child's own limits and the agent's unit bound the rest.
//
// A child that is stopped frees the slot past its deadline and WaitDelay, and is left to end and be reaped on its own
// (#183); live counts the children not yet reaped, the one reading among them, so that left children can't pile up:
// with maxLiveReads of them, a read is refused as busy until one ends.
type reads struct {
	slot chan struct{}
	live chan struct{}
	exe  string
}

// maxLiveReads is how many read children may be alive at once: the one reading, and those left behind stopped.
const maxLiveReads = 4

func newReads(exe string) *reads {
	return &reads{slot: make(chan struct{}, 1), live: make(chan struct{}, maxLiveReads), exe: exe}
}

// readCredential is who a read's child runs as: when the agent is root, homerslab-read (readUser) with no groups, or
// errNoReadUser, so that the agent refuses to read rather than run the child as anyone else, and an error where the
// child would start dumpable (execOnlyHolds); otherwise nil, the agent's own user.
func readCredential() (*syscall.Credential, error) {
	if os.Geteuid() != 0 {
		return nil, nil
	}
	uid, gid, err := readIDs()
	if err != nil {
		return nil, err
	}
	if err := execOnlyHolds(); err != nil {
		return nil, err
	}
	return &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{}}, nil
}

// childCommand is the command that runs one compose read in dir, its own empty folder, for project name, and the
// agent's ends of the child's control pipe, which the caller closes once the child is done. The child starts as cred
// (readCredential) when it isn't nil: the IDs are set in the fork, before the exec, so the child never runs as root.
//
// The child ends itself at what is left of ctx's deadline (readDeadline if it has none), and when the control pipe's
// write end closes: at ctx's end (cmd.Cancel), or when the agent dies, however it dies, since the kernel closes it then
// and the agent alone holds it (close-on-exec, so no other child of the agent inherits it). The agent's unit has no
// CAP_KILL, so a root agent can't signal a child that runs as another user (#138): the kill at ctx's end and the
// parent-death signal (set after the IDs, so changing them doesn't clear it) hold only where it may.
func childCommand(ctx context.Context, argv []string, dir, name string, cpuSeconds int, cred *syscall.Credential) (*exec.Cmd, io.Closer, error) {
	deadline := readDeadline
	if at, ok := ctx.Deadline(); ok {
		deadline = time.Until(at)
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.CommandContext(ctx, argv[0], append(argv[1:], "--cpu-seconds", strconv.Itoa(cpuSeconds),
		"--deadline", deadline.String(), "--control-fd", "3", "--", name)...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + dir, "TMPDIR=" + dir, "DOCKER_CONFIG=" + filepath.Join(dir, ".docker"), "PATH=/usr/bin:/bin"}
	cmd.ExtraFiles = []*os.File{r} // fd 3, the read end
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Setpgid: true, Credential: cred}
	cmd.Cancel = func() error {
		w.Close()
		_ = cmd.Process.Kill() // best effort: EPERM under the agent's unit
		return nil
	}
	cmd.WaitDelay = time.Second
	return cmd, controlEnds{w, r}, nil
}

// controlEnds is the agent's two ends of a child's control pipe. The child has its own copy of the read end once it
// starts; the agent's closes with the write end.
type controlEnds struct{ w, r *os.File }

func (c controlEnds) Close() error {
	c.w.Close() // closed already, at the deadline, by cmd.Cancel
	return c.r.Close()
}

// childDir is a read's own empty folder, owned by the user its child runs as, cred (readCredential) if not nil.
func childDir(cred *syscall.Credential) (string, error) {
	dir, err := os.MkdirTemp("", "homerslab-read-")
	if err != nil {
		return "", err
	}
	if cred != nil {
		if err := os.Chown(dir, int(cred.Uid), int(cred.Gid)); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

func (rs *reads) read(ctx context.Context, name string, file []byte) (engine.Project, error) {
	if !compose.OurName(name) {
		return engine.Project{}, compose.ErrNotOurs // no child for a name that isn't one of ours
	}
	ctx, cancel := context.WithTimeout(ctx, readDeadline)
	defer cancel()
	select {
	case rs.slot <- struct{}{}:
	case <-ctx.Done():
		return engine.Project{}, errBusy
	}
	defer func() { <-rs.slot }()
	// Then a place among the children alive, before anything else: with as many alive as reads may have, those left
	// behind stopped among them, the read is busy and makes nothing.
	select {
	case rs.live <- struct{}{}:
	default:
		return engine.Project{}, errBusy
	}
	started := false
	defer func() {
		if !started {
			<-rs.live
		}
	}()
	cred, err := readCredential()
	if err != nil {
		return engine.Project{}, fmt.Errorf("%w: %w", errNoReader, err)
	}
	argv, err := readArgv(rs.exe, cred)
	if err != nil {
		return engine.Project{}, fmt.Errorf("%w: %w", errNoReader, err)
	}
	dir, err := childDir(cred)
	if err != nil {
		return engine.Project{}, fmt.Errorf("%w: %w", errNoReader, err)
	}
	defer os.RemoveAll(dir)
	cmd, control, err := childCommand(ctx, argv, dir, name, readCPUSeconds(), cred)
	if err != nil {
		return engine.Project{}, fmt.Errorf("%w: %w", errNoReader, err)
	}
	defer control.Close()
	cmd.Stdin = bytes.NewReader(file)
	var out, errOut capped
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := startChild(cmd); err != nil {
		if ctx.Err() != nil {
			return engine.Project{}, errReadTimeout
		}
		return engine.Project{}, fmt.Errorf("%w: %w", errNoReader, err)
	}
	// A child that is stopped (SIGSTOP) runs neither its deadline nor its control pipe, its CPU limit doesn't count,
	// and a root agent without CAP_KILL can't end it, so Wait could block for good (#183). Past the deadline and the
	// child's WaitDelay, the slot is free again; Wait goes on in its goroutine and reaps the child whenever it ends.
	started = true // its place among the children alive is freed once it is reaped
	waited := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		<-rs.live
		waited <- err
	}()
	var runErr error
	select {
	case runErr = <-waited:
	case <-ctx.Done():
		select {
		case runErr = <-waited:
		case <-time.After(cmd.WaitDelay):
			return engine.Project{}, errReadTimeout
		}
	}
	if ctx.Err() != nil {
		return engine.Project{}, errReadTimeout
	}
	var res readResult
	if err := json.Unmarshal(out.b.Bytes(), &res); err != nil {
		// The child ended without an answer: killed at its hard CPU limit (it had used its soft one), or out of memory
		// (Go's own error, either way it words it, or the kernel's kill), which are the file's doing; anything else is
		// the reader's.
		var exit *exec.ExitError
		ended := errors.As(runErr, &exit)
		killed := ended && signalled(exit, syscall.SIGKILL)
		said := errOut.b.String()
		switch {
		case ended && signalled(exit, syscall.SIGXCPU), killed && cpuUsed(exit) >= time.Duration(readCPUSeconds())*time.Second:
			return engine.Project{}, errReadTimeout
		case killed, strings.Contains(said, "out of memory"), strings.Contains(said, "cannot allocate memory"):
			return engine.Project{}, errReadTooBig
		}
		return engine.Project{}, fmt.Errorf("%w: %v (%v) %s", errNoReader, err, runErr, strings.TrimSpace(said))
	}
	switch res.Error {
	case "cpu-limit", "deadline":
		return engine.Project{}, errReadTimeout
	case "":
		return *res.Project, nil
	case "not-ours":
		return engine.Project{}, compose.ErrNotOurs
	case "not-plain":
		return engine.Project{}, fmt.Errorf("%w (%s)", compose.ErrNotPlain, res.Detail)
	case "off-floor":
		return engine.Project{}, fmt.Errorf("%w (%s)", compose.ErrOffFloor, res.Detail)
	default:
		return engine.Project{}, errors.New(res.Detail)
	}
}

// cpuUsed is the CPU time a child used, user and system.
func cpuUsed(e *exec.ExitError) time.Duration {
	ru, ok := e.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func signalled(e *exec.ExitError, sig syscall.Signal) bool {
	ws, ok := e.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == sig
}

// capped is a child's output, of which it keeps the first MiB.
type capped struct{ b bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := 1<<20 - c.b.Len(); room > 0 {
		c.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func fail(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
