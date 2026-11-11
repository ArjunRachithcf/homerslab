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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/firewall"
	"github.com/ArjunRachithcf/homerslab/internal/host"
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
	// SetupPort is the setup link's LAN listener, the firewall table's one exception until it closes.
	SetupPort int
	// CheckEvery is how often the firewall table is checked for drift.
	CheckEvery time.Duration
	// JoinWait is how long a join waits for the mesh's client to connect.
	JoinWait time.Duration
	Now      func() time.Time
	Log      *slog.Logger
}

// Agent is a running agent.
type Agent struct {
	cfg Config
	log *slog.Logger
	fw  *firewall.Firewall

	mu    sync.Mutex // the state below, and joins
	state state
}

// state is what the agent keeps across restarts, in StateDir/agent.json. None of it is secret.
type state struct {
	// Enforcing: the firewall table is on, since the node joined its mesh.
	Enforcing bool     `json:"enforcing"`
	Mesh      []string `json:"mesh"`
	WireGuard []int    `json:"wireGuard"`
	// SetupClosed: the setup link's LAN listener was closed, for good.
	SetupClosed bool `json:"setupClosed"`
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
		c.CheckEvery = 30 * time.Second
	}
	if c.JoinWait == 0 {
		c.JoinWait = 30 * time.Second
	}
	if c.Resolve == nil {
		c.Resolve = net.DefaultResolver.LookupHost
	}
	a := &Agent{cfg: c, log: c.Log, fw: &firewall.Firewall{Run: c.Run, Now: c.Now}}
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
		if err := a.fw.Apply(ctx, a.firewallState()); err != nil {
			return nil, err
		}
		a.log.Info("firewall table restored", "setupListener", !a.state.SetupClosed)
	}
	return a, nil
}

func (a *Agent) firewallState() firewall.State {
	s := firewall.State{Mesh: a.state.Mesh, WireGuard: a.state.WireGuard}
	if !a.state.SetupClosed {
		s.SetupPort = a.cfg.SetupPort
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
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { fail(w, http.StatusNotFound, "not-found") })
	return mux
}

// Watch checks the firewall table for drift until ctx ends, and puts it back when it drifted.
func (a *Agent) Watch(ctx context.Context) {
	t := time.NewTicker(a.cfg.CheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.check(ctx)
		}
	}
}

func (a *Agent) check(ctx context.Context) {
	d, err := a.fw.Check(ctx)
	if err != nil {
		a.log.Error("checking the firewall table", "err", err)
		return
	}
	if d != nil {
		a.log.Warn("the firewall table had drifted, and was put back", "what", d.What)
	}
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
	go a.Watch(watch)
	srv := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	return serveUntil(ctx, srv, ln)
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
