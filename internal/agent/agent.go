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
	"io/fs"
	"log/slog"
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
	// LAN is the machine's LAN: the interfaces with the default route and their prefixes; lan.OnLink when nil.
	LAN func() ([]string, []netip.Prefix)
	// Bridges are the container engines' bridges, by name; asked of Docker and Podman when nil. Only names that
	// IsBridge confirms (the kernel's /sys/class/net/<name>/bridge, from Sysroot, when nil) are used.
	Bridges  func(ctx context.Context) []string
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
	Now      func() time.Time
	Log      *slog.Logger
	// Executable is the agent's own program, which it runs as each compose read's child; os.Executable() when "".
	Executable string
}

// Agent is a running agent.
type Agent struct {
	cfg Config
	log *slog.Logger
	fw  *firewall.Firewall

	mu        sync.Mutex // the state below, and joins
	state     state
	natAt     time.Time        // when the mesh client's NAT mapper was last checked
	bridgesAt time.Time        // when the engines were last asked for their bridges
	refused   map[string]bool  // engine bridge names refused, reported once each
	meshDrift []firewall.Drift // what the agent found changed in the mesh client, and put back
	reads     *reads           // compose reads, one at a time
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
	SetupPort   int  `json:"setupPort,omitempty"`
	// Bridges are the engines' bridges as last confirmed: what the table uses until the engines answer again, at
	// boot before any of them runs.
	Bridges []string `json:"bridges,omitempty"`
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
	if c.Resolve == nil {
		c.Resolve = net.DefaultResolver.LookupHost
	}
	if c.LAN == nil {
		c.LAN = lan.OnLink
	}
	if c.Bridges == nil {
		run, root := c.Run, c.Sysroot
		c.Bridges = func(ctx context.Context) []string { return engineBridges(ctx, run, root) }
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
	a := &Agent{cfg: c, log: c.Log, fw: &firewall.Firewall{Run: c.Run, Now: c.Now}, refused: map[string]bool{}, reads: newReads(c.Executable)}
	if c.StateDir != "" {
		a.fw.SavePath = filepath.Join(c.StateDir, "firewall.nft")
	}
	if c.StateDir != "" {
		if err := os.MkdirAll(c.StateDir, 0o700); err != nil {
			return nil, err
		}
		b, err := os.ReadFile(filepath.Join(c.StateDir, "agent.json"))
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return nil, err
		default:
			if err := json.Unmarshal(b, &a.state); err != nil {
				return nil, fmt.Errorf("the agent's state: %w", err)
			}
		}
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
		Bridges: a.state.Bridges, Resolvers: resolvers(a.cfg.Sysroot)}
	if !a.state.SetupClosed {
		s.SetupPort = a.state.SetupPort
		if s.SetupPort == 0 {
			s.SetupPort = a.cfg.SetupPort
		}
	}
	return s
}

func (a *Agent) save() error {
	if a.cfg.StateDir == "" {
		return errors.New("the agent has no state directory")
	}
	b, err := json.Marshal(a.state)
	if err != nil {
		return err
	}
	path := filepath.Join(a.cfg.StateDir, "agent.json")
	if err := os.WriteFile(path+".new", b, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".new", path)
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
	mux.HandleFunc("DELETE /v1/credentials/{name}", a.deleteCredential)
	mux.HandleFunc("GET /v1/mesh", a.getMesh)
	mux.HandleFunc("POST /v1/projects/read", a.readProject)
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
	var ok, refused []string
	for _, n := range a.cfg.Bridges(ctx) {
		if firewall.ValidName(n) && a.cfg.IsBridge(n) {
			ok = append(ok, n)
		} else {
			refused = append(refused, n)
		}
	}
	slices.Sort(ok)
	ok = slices.Compact(ok)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bridgesAt = a.cfg.Now()
	for _, n := range refused {
		if !a.refused[n] {
			a.refused[n] = true
			a.log.Warn("an engine's bridge was refused: not a bridge, or a name nft can't take", "name", fmt.Sprintf("%q", n))
			a.fw.Note(fmt.Sprintf("refused-bridge %q", n))
		}
	}
	if !slices.Equal(ok, a.state.Bridges) {
		a.state.Bridges = ok
		if err := a.save(); err != nil {
			a.log.Error("saving the engines' bridges", "err", err)
		}
	}
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
	default:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid-compose", "detail": err.Error()})
	}
}

// ReadDeadline is how long a compose read may take before the agent stops it. The manager waits longer, so that it
// hears the agent's read-timeout.
const ReadDeadline = 10 * time.Second

var readDeadline = ReadDeadline

// readCommand is how the agent starts a compose read: its executable, in its read-project mode. Tests replace it.
var readCommand = func(exe string) ([]string, error) {
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return nil, err
		}
	}
	return []string{exe, "agent", "read-project"}, nil
}

var (
	errBusy        = errors.New("another compose read is running")
	errReadTimeout = errors.New("the compose read ran out of time")
	errReadTooBig  = errors.New("the compose read ran out of memory")
	errNoReader    = errors.New("the compose reader didn't start")
)

// reads runs compose reads one at a time, each in a child process (ReadProjectMain) that the agent kills at the
// deadline: the deadline really stops the work, and the slot is free again at once. The plain-file rules
// (engine/compose) bound how long a read can take; the child's own limits and the agent's unit bound the rest.
type reads struct {
	slot chan struct{}
	exe  string
}

func newReads(exe string) *reads { return &reads{slot: make(chan struct{}, 1), exe: exe} }

// childCommand is the command that runs one compose read in dir, its own empty folder, for project name. When the
// agent is root, the child starts as nobody (65534) with no groups: the IDs are set in the fork, before the parent-death
// signal and the exec, so the child never runs as root and still dies with the agent.
func childCommand(ctx context.Context, argv []string, dir, name string, cpuSeconds int) *exec.Cmd {
	cmd := exec.CommandContext(ctx, argv[0], append(argv[1:], "--cpu-seconds", strconv.Itoa(cpuSeconds), "--", name)...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + dir, "TMPDIR=" + dir, "DOCKER_CONFIG=" + filepath.Join(dir, ".docker"), "PATH=/usr/bin:/bin"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Setpgid: true}
	if os.Geteuid() == 0 {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: nobody, Gid: nobody, Groups: []uint32{}}
	}
	cmd.WaitDelay = time.Second
	return cmd
}

// childDir is a read's own empty folder, owned by the user its child runs as.
func childDir() (string, error) {
	dir, err := os.MkdirTemp("", "homerslab-read-")
	if err != nil {
		return "", err
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(dir, nobody, nobody); err != nil {
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
	argv, err := readCommand(rs.exe)
	if err != nil {
		return engine.Project{}, fmt.Errorf("%w: %w", errNoReader, err)
	}
	dir, err := childDir()
	if err != nil {
		return engine.Project{}, fmt.Errorf("%w: %w", errNoReader, err)
	}
	defer os.RemoveAll(dir)
	cmd := childCommand(ctx, argv, dir, name, int(readDeadline/time.Second)+1)
	cmd.Stdin = bytes.NewReader(file)
	var out, errOut capped
	cmd.Stdout, cmd.Stderr = &out, &errOut
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return engine.Project{}, errReadTimeout
	}
	var res readResult
	if err := json.Unmarshal(out.b.Bytes(), &res); err != nil {
		// The child ended without an answer: stopped by its CPU limit, or out of memory (Go's own error, or the
		// kernel's kill), which are the file's doing; anything else is the reader's.
		var exit *exec.ExitError
		switch {
		case errors.As(runErr, &exit) && signalled(exit, syscall.SIGXCPU):
			return engine.Project{}, errReadTimeout
		case strings.Contains(errOut.b.String(), "out of memory"), errors.As(runErr, &exit) && signalled(exit, syscall.SIGKILL):
			return engine.Project{}, errReadTooBig
		}
		return engine.Project{}, fmt.Errorf("%w: %v (%v) %s", errNoReader, err, runErr, strings.TrimSpace(errOut.b.String()))
	}
	switch res.Error {
	case "":
		return *res.Project, nil
	case "not-ours":
		return engine.Project{}, compose.ErrNotOurs
	case "not-plain":
		return engine.Project{}, fmt.Errorf("%w (%s)", compose.ErrNotPlain, res.Detail)
	default:
		return engine.Project{}, errors.New(res.Detail)
	}
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
