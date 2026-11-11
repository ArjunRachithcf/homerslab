// Package agent is the agent's API: the process on every node that owns its host and runs as root. It reports the
// node's facts, joins the node to the mesh, owns the firewall table and corrects its drift, and seals the manager's
// own credentials.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/atomicfile"
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
	// LAN is the machine's LAN: the interfaces with the default route and their prefixes; lan.OnLink when nil.
	LAN func() ([]string, []netip.Prefix)
	// Bridges are the container engines' bridges, by engine and name; asked of Docker and Podman when nil. An engine
	// missing from the answer didn't answer (an error, a timeout): its last confirmed list is kept. Only names that
	// IsBridge confirms (the kernel's /sys/class/net/<name>/bridge, from Sysroot, when nil) are used, kept ones too.
	Bridges  func(ctx context.Context) map[string][]string
	IsBridge func(name string) bool
	// Proc is the machine's /proc, for the mesh client's running environment.
	Proc string
	// SetupPort is the setup link's LAN listener, the firewall table's one exception until it closes.
	SetupPort int
	// CheckEvery is how often the firewall table is checked for drift: often, so an `nft flush ruleset` is undone
	// within seconds.
	CheckEvery time.Duration
	// JoinWait is how long a join waits for the mesh's client to connect.
	JoinWait time.Duration
	// Edge is where the node's edge lives; DefaultEdge when its Unit is empty.
	Edge EdgeConfig
	Now  func() time.Time
	Log  *slog.Logger
}

// Agent is a running agent.
type Agent struct {
	cfg Config
	log *slog.Logger
	fw  *firewall.Firewall

	mu         sync.Mutex // the state below, and joins
	state      state
	natAt      time.Time        // when the mesh client's NAT mapper was last checked
	bridgesAt  time.Time        // when the engines were last asked for their bridges
	refused    map[string]bool  // engine bridge names refused, reported once each
	meshDrift  []firewall.Drift // what the agent found changed in the mesh client, and put back
	edgeDown   string           // the edge's last check's error, so a down edge is logged once
	stateErr   error            // why the saved state couldn't be read at start, if it couldn't
	brokenFile bool             // the state file is that unreadable one, not yet put aside
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
	if c.LAN == nil {
		c.LAN = lan.OnLink
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
	if c.Proc == "" {
		c.Proc = "/proc"
	}
	a := &Agent{cfg: c, log: c.Log, fw: &firewall.Firewall{Run: c.Run, Now: c.Now}, refused: map[string]bool{}}
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
		if err := a.fw.Apply(ctx, a.firewallState(ctx)); err != nil {
			a.log.Error("restoring the firewall table; trying again", "err", err)
			a.fw.Want(a.firewallState(ctx))
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
// confirmed (refreshBridges).
func (a *Agent) firewallState(context.Context) firewall.State {
	ifaces, prefixes := a.cfg.LAN()
	s := firewall.State{Mesh: a.state.Mesh, WireGuard: a.state.WireGuard, LAN: ifaces, LANPrefixes: prefixes,
		Bridges: a.bridges(), Resolvers: resolvers(a.cfg.Sysroot)}
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

// bridges are every engine's bridges, as last confirmed, that the kernel shows are bridges now. The caller holds a.mu.
func (a *Agent) bridges() []string {
	var out []string
	for _, names := range a.state.Bridges {
		for _, n := range names {
			if firewall.ValidName(n) && a.cfg.IsBridge(n) {
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

func fail(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
