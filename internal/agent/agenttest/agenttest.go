// Package agenttest runs a real agent for other packages' tests, on a socket in the test's own directory, with a
// stand-in for the machine's tools (Host): no test's agent ever runs nft, systemctl or netbird on this machine.
package agenttest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/host"
	"github.com/ArjunRachithcf/homerslab/internal/node"
)

// Primary is the system root of the invented primary in testdata/primary, with 412 GB free of 1 TB for apps' data.
func Primary() agent.Sysroot {
	_, self, _, _ := runtime.Caller(0)
	return WithSpace(os.DirFS(filepath.Join(filepath.Dir(self), "..", "..", "..", "testdata", "primary")), 412e9, 1e12)
}

// WithSpace is a system root whose filesystem for apps' data has that much space.
func WithSpace(root fs.FS, free, size uint64) agent.Sysroot {
	return space{root, node.Space{FreeBytes: free, SizeBytes: size}}
}

type space struct {
	fs.FS
	space node.Space
}

func (s space) Statfs(string) (node.Space, error) { return s.space, nil }

// Start runs an agent reading sysroot until the test ends, and returns its socket.
func Start(t testing.TB, sysroot agent.Sysroot) string {
	t.Helper()
	sock, _ := StartWith(t, agent.Config{Sysroot: sysroot})
	return sock
}

// StartWith runs an agent with c, filled in for the test: its socket, state and root in the test's own directories,
// and a fresh Host unless c.Run is set. It returns the socket and the Host.
func StartWith(t testing.TB, c agent.Config) (string, *Host) {
	t.Helper()
	dir := t.TempDir()
	h := NewHost(filepath.Join(dir, "root"))
	if c.Sysroot == nil {
		c.Sysroot = Primary()
	}
	if c.Socket == "" {
		c.Socket = filepath.Join(dir, "agent.sock")
	}
	c.SocketGID = os.Getgid()
	if c.StateDir == "" {
		c.StateDir = filepath.Join(dir, "state")
	}
	if c.Root == "" {
		c.Root = h.Root
	}
	if c.Run == nil {
		c.Run = h.Run
	}
	if c.IsBridge == nil {
		c.IsBridge = h.IsBridge
	}
	if c.HasInterface == nil {
		c.HasInterface = h.HasInterface
	}
	if c.Resolve == nil {
		c.Resolve = func(context.Context, string) ([]string, error) { return []string{"192.0.2.1"}, nil }
	}
	if c.LAN == nil {
		c.LAN = func() ([]string, []netip.Prefix) {
			return []string{"enp5s0"}, []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24"), netip.MustParsePrefix("2001:db8:1::/64")}
		}
	}
	if c.SetupPort == 0 {
		c.SetupPort = 7020
	}
	if c.JoinWait == 0 {
		c.JoinWait = 2 * time.Second
	}
	if c.Edge.Unit == "" {
		c.Edge = agent.DefaultEdge
		c.Edge.AdminSocket = filepath.Join(dir, "edge-admin.sock")
		h.Edge = startEdge(t, c.Edge.AdminSocket)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	if c.Executable == "" {
		c.Executable = Executable(t)
	}
	go func() { done <- agent.Serve(ctx, c) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("agent stopped with %v", err)
		}
	})
	for range 200 {
		if conn, err := net.Dial("unix", c.Socket); err == nil {
			conn.Close()
			return c.Socket, h
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the agent never listened on %s", c.Socket)
	return "", nil
}

// Host stands in for the machine's tools, as a test needs them: nft keeps one table in memory, systemd-creds seals
// with base64 (a marker, not a secret store), systemctl does nothing, netbird joins at once.
type Host struct {
	Root string // the machine's root directory, where the agent writes

	mu        sync.Mutex
	cmds      []host.Cmd
	table     string
	hasTable  bool
	dropInAt  map[string][]byte // the NetBird drop-in as it was when each command ran
	fail      map[string]error
	NetbirdIP string
	// Edge is the node's edge: Caddy's admin API, as a stand-in.
	Edge *Edge
	pid  int // NetBird's main process, 0 while it doesn't run
	// restartedWith is NetBird's drop-in as systemd had it loaded when the service last restarted: the running
	// service's environment. started is when its main process started; loaded is the drop-in as systemd last loaded
	// it (daemon-reload), and listed whether systemd lists it among the unit's drop-ins. The agent can't read the
	// running process's environment (its /proc entry is a root process with every capability), so it asks systemd.
	restartedWith []byte
	started       time.Time
	loaded        []byte
	listed        bool
	ignored       bool              // systemd doesn't apply the drop-in to the unit
	showFails     bool              // systemd is older than 248: `systemctl show --timestamp=us+utc` fails
	docker        map[string]string // the engine's bridge networks: ID → the bridge's name option ("" for none)
	notBridges    map[string]bool   // names the engines give that the kernel doesn't show as bridges
	absent        map[string]bool   // names the kernel has no interface for
	block         bool              // the engines and systemd never answer
	hang          []string          // commands, by prefix, that never answer
	dockerActive  string            // docker.service's ActiveState and LoadState, when set; else from Engines
	dockerLoad    string
	// Journal, when set, is what journalctl prints instead of the stub's few lines.
	Journal []byte
}

// IsBridge says whether the kernel would show a name as a bridge: every name an engine gives, but those NotABridge
// names.
func (h *Host) IsBridge(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.notBridges[name] && !h.absent[name]
}

// NotABridge makes an engine's name one the kernel doesn't show as a bridge (a NIC, say).
func (h *Host) NotABridge(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.notBridges == nil {
		h.notBridges = map[string]bool{}
	}
	h.notBridges[name] = true
}

// ABridgeAgain makes a name NotABridge or Absent took a bridge again (an engine made it).
func (h *Host) ABridgeAgain(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.notBridges, name)
	delete(h.absent, name)
}

// Absent makes a name one the kernel has no interface for (yet).
func (h *Host) Absent(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.absent == nil {
		h.absent = map[string]bool{}
	}
	h.absent[name] = true
}

// HasInterface says whether the kernel has an interface by that name: every name but those Absent.
func (h *Host) HasInterface(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.absent[name]
}

// Block makes Docker and systemd hang until the caller gives up, as a socket-activated engine waiting on the agent
// would.
func (h *Host) Block() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.block = true
}

// Hang makes every command starting with prefix (e.g. "docker network") hang until the caller gives up, until Mend.
func (h *Host) Hang(prefix string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hang = append(h.hang, prefix)
}

// DockerState makes systemd describe docker.service so (ActiveState, LoadState: "activating", "loaded"), whatever
// Engines says; "" goes back to Engines' word (active while it lists networks, inactive otherwise).
func (h *Host) DockerState(active, load string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dockerActive, h.dockerLoad = active, load
}

// Engines makes Docker run and list bridge networks (ID → the name it was given, or ""). Without it, Docker isn't
// running.
func (h *Host) Engines(docker map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.docker = docker
}

// NewHost is a machine whose root directory is root.
func NewHost(root string) *Host {
	return &Host{Root: root, NetbirdIP: "100.88.7.186", dropInAt: map[string][]byte{}, fail: map[string]error{}}
}

// Fail makes every command starting with prefix (e.g. "netbird up") fail.
func (h *Host) Fail(prefix string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fail[prefix] = errors.New("failed, as the test asked")
}

// Mend makes every command work again.
func (h *Host) Mend() {
	h.mu.Lock()
	defer h.mu.Unlock()
	clear(h.fail)
	h.hang = nil
}

// Run is the agent's host.Runner.
// JournalToken is the enrolment link's token in the stub's journal of the sign-in.
const JournalToken = "9d3afc7b-58ba-4c1e-9f0e-2b6a1c0d7e11"

func (h *Host) Run(ctx context.Context, c host.Cmd) ([]byte, error) {
	h.mu.Lock()
	if h.block && (c.Name == "docker" || c.Name == "systemctl") || slices.ContainsFunc(h.hang, func(p string) bool { return strings.HasPrefix(c.String(), p) }) {
		h.mu.Unlock()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	defer h.mu.Unlock()
	h.cmds = append(h.cmds, c)
	line := c.String()
	if b, err := os.ReadFile(filepath.Join(h.Root, "etc/systemd/system/netbird.service.d/10-homerslab.conf")); err == nil {
		h.dropInAt[line] = b
	}
	for prefix, err := range h.fail {
		if strings.HasPrefix(line, prefix) {
			return nil, &host.Error{Cmd: line, Err: err}
		}
	}
	switch {
	case line == "nft -f -":
		h.table, h.hasTable = string(c.Stdin), true
		return nil, nil
	case line == "nft -s list table inet homerslab":
		if !h.hasTable {
			return nil, &host.Error{Cmd: line, Err: errors.New("exit status 1"), Stderr: "Error: No such file or directory"}
		}
		return []byte(h.table), nil
	case line == "systemctl restart homerslab-edge.service" && h.Edge != nil:
		h.Edge.Restart()
		return nil, nil
	case line == "systemctl daemon-reload":
		h.loaded, h.listed = h.dropInAt[line], h.dropInAt[line] != nil && !h.ignored
		return nil, nil
	case line == "systemctl restart netbird.service" || line == "systemctl --no-block restart netbird.service":
		h.restartedWith, h.started = h.loaded, time.Now()
		h.pid++
		return nil, nil
	case line == "systemctl show netbird.service --timestamp=us+utc --property=DropInPaths,NeedDaemonReload,ExecMainStartTimestamp":
		if h.showFails {
			return nil, &host.Error{Cmd: line, Err: errors.New("unrecognized option '--timestamp=us+utc'")}
		}
		return h.showNetBird(), nil
	case line == "systemctl show -p ActiveState -p LoadState docker.service":
		active, load := h.dockerActive, h.dockerLoad
		if active == "" {
			active, load = "active", "loaded"
			if h.docker == nil {
				active = "inactive"
			}
		}
		return fmt.Appendf(nil, "LoadState=%s\nActiveState=%s\n", load, active), nil
	case c.Name == "systemctl":
		return nil, nil
	case c.Name == "docker" && h.docker != nil && len(c.Args) > 1 && c.Args[1] == "ls":
		var ids []string
		for id := range h.docker {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		return []byte(strings.Join(ids, "\n") + "\n"), nil
	case c.Name == "docker" && h.docker != nil && len(c.Args) > 1 && c.Args[1] == "inspect":
		var out []string
		for _, id := range c.Args[4:] {
			name := h.docker[id]
			if name == "" {
				name = "<no value>"
			}
			out = append(out, id+" "+name)
		}
		return []byte(strings.Join(out, "\n") + "\n"), nil
	case strings.HasPrefix(line, "netbird up"):
		return nil, nil
	case c.Name == "journalctl" && h.Journal != nil:
		return h.Journal, nil
	case c.Name == "journalctl":
		// The unit's journal, with request lines as an Authentik logging at info writes them: an enrolment link's
		// token in the URL, plain and URL-encoded.
		return []byte("2026-11-11T10:00:00+0000 primary docker[812]: server-1 | {\"event\": \"Starting authentik server\"}\n" +
			"2026-11-11T10:01:00+0000 primary docker[812]: server-1 | {\"event\": \"/if/flow/homerslab-passkey-enrolment/?itoken=" + JournalToken + "\"}\n" +
			"2026-11-11T10:01:01+0000 primary docker[812]: server-1 | {\"event\": \"/api/v3/flows/executor/homerslab-passkey-enrolment/?query=itoken%3D" + JournalToken + "&x=1\"}\n"), nil
	case line == "netbird status --json":
		return fmt.Appendf(nil, `{"management":{"connected":true},"netbirdIp":"%s/16","fqdn":"study-pc.netbird.selfhosted"}`, h.NetbirdIP), nil
	case c.Name == "systemd-creds" && len(c.Args) == 5 && c.Args[0] == "encrypt":
		return nil, os.WriteFile(c.Args[4], []byte("sealed:"+base64.StdEncoding.EncodeToString(c.Stdin)), 0o600)
	case c.Name == "systemd-creds" && len(c.Args) == 4 && c.Args[0] == "decrypt":
		b, err := os.ReadFile(c.Args[2])
		if err != nil {
			return nil, err
		}
		return base64.StdEncoding.DecodeString(strings.TrimPrefix(string(b), "sealed:"))
	}
	return nil, &host.Error{Cmd: line, Err: errors.New("not something this test's machine does")}
}

// showNetBird is `systemctl show netbird.service` for the drop-ins, whether a reload is due, and when the main process
// started, as systemd writes them.
func (h *Host) showNetBird() []byte {
	var b strings.Builder
	path := "/etc/systemd/system/netbird.service.d/10-homerslab.conf"
	file, err := os.ReadFile(filepath.Join(h.Root, path))
	fmt.Fprintf(&b, "ExecMainStartTimestamp=")
	if h.pid > 0 {
		b.WriteString(h.started.UTC().Format("Mon 2006-01-02 15:04:05.000000 MST"))
	}
	b.WriteString("\nDropInPaths=")
	if h.listed {
		b.WriteString(path)
	}
	// systemd notices a unit file changed on disk since it loaded it.
	if !h.ignored && (err == nil) != h.listed || err == nil && string(file) != string(h.loaded) {
		b.WriteString("\nNeedDaemonReload=yes\n")
	} else {
		b.WriteString("\nNeedDaemonReload=no\n")
	}
	return []byte(b.String())
}

// Cmds are the commands the agent ran, in order.
func (h *Host) Cmds() []host.Cmd {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.cmds)
}

// Ran says whether the agent ran a command line, and at which position (or -1).
// Times is how many times a command ran.
func (h *Host) Times(line string) int {
	n := 0
	for _, c := range h.Cmds() {
		if c.String() == line {
			n++
		}
	}
	return n
}

func (h *Host) Ran(line string) int {
	for i, c := range h.Cmds() {
		if c.String() == line || strings.HasPrefix(c.String(), line+" ") {
			return i
		}
	}
	return -1
}

// DropInWhen is the NetBird drop-in as it was when a command line ran (nil if it wasn't there).
func (h *Host) DropInWhen(prefix string) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	for line, b := range h.dropInAt {
		if strings.HasPrefix(line, prefix) {
			return b
		}
	}
	return nil
}

// Table is the firewall table as last applied, and whether there is one.
func (h *Host) Table() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.table, h.hasTable
}

// RestartNetBirdWithoutItsDropIn stands for someone removing the drop-in, reloading systemd and restarting NetBird's
// service.
func (h *Host) RestartNetBirdWithoutItsDropIn() {
	h.mu.Lock()
	defer h.mu.Unlock()
	_ = os.Remove(filepath.Join(h.Root, "etc/systemd/system/netbird.service.d/10-homerslab.conf"))
	h.loaded, h.listed, h.restartedWith, h.started = nil, false, nil, time.Now()
	h.pid++
}

// RestartNetBirdBeforeItsDropIn stands for NetBird's service running without the drop-in's environment, though the
// drop-in is there: it started before the drop-in's last write (someone wrote it again, but nobody restarted it).
func (h *Host) RestartNetBirdBeforeItsDropIn() {
	h.mu.Lock()
	defer h.mu.Unlock()
	path := filepath.Join(h.Root, "etc/systemd/system/netbird.service.d/10-homerslab.conf")
	h.restartedWith = nil
	h.pid++
	h.started = time.Now().Add(-time.Minute)
	_ = os.Chtimes(path, time.Now(), time.Now())
}

// NetBirdLoadedAnOlderDropIn stands for systemd running NetBird with a drop-in it loaded before the file on disk was
// written as it is now, and not reloaded since: the file is right, but the service runs with the old one.
func (h *Host) NetBirdLoadedAnOlderDropIn() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loaded, h.listed = []byte("[Service]\nEnvironment=NB_SOMETHING_ELSE=1\n"), true
	h.restartedWith, h.started = h.loaded, time.Now().Add(time.Minute)
	h.pid++
}

// NetBirdIgnoresItsDropIn stands for systemd not applying the drop-in to the unit NetBird runs as (another unit's
// name, say): systemd lists no drop-in, though the file is there and nothing waits for a reload.
func (h *Host) NetBirdIgnoresItsDropIn() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loaded, _ = os.ReadFile(filepath.Join(h.Root, "etc/systemd/system/netbird.service.d/10-homerslab.conf"))
	h.listed, h.ignored = false, true
	h.restartedWith, h.started = nil, time.Now().Add(time.Minute)
	h.pid++
}

// NetBirdSystemdTooOld stands for systemd older than 248, which refuses `systemctl show --timestamp=us+utc`.
func (h *Host) NetBirdSystemdTooOld() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.showFails = true
}

// NetBirdRestarts is how often NetBird's service was restarted.
func (h *Host) NetBirdRestarts() int {
	n := 0
	for _, c := range h.Cmds() {
		if l := c.String(); l == "systemctl restart netbird.service" || l == "systemctl --no-block restart netbird.service" {
			n++
		}
	}
	return n
}

// Tamper changes the firewall table behind the agent's back: "missing" deletes it, anything else edits it.
func (h *Host) Tamper(how string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if how == "missing" {
		h.hasTable = false
		return
	}
	h.table = string(bytes.Replace([]byte(h.table), []byte("policy drop"), []byte("policy accept"), 1))
}

// Main runs a test binary's tests, or, when the agent started it as a compose read's child, that read: the agent
// starts reads by running its own executable, which in a test is the test binary. Every package whose tests run an
// agent that reads calls it from TestMain.
func Main(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "agent" && os.Args[2] == "read-project" {
		os.Exit(agent.ReadProjectMain(os.Args[3:], os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}

var (
	copied     sync.Once
	copiedPath string
	copiedErr  error
)

// Executable is the program an agent in a test runs as a compose read's child: this test binary. Run as root, the
// child starts as nobody, who can't reach a test binary in Go's private build folder, so the binary is copied once
// to a folder everyone may read.
func Executable(t testing.TB) string {
	t.Helper()
	if os.Geteuid() != 0 {
		return os.Args[0]
	}
	copied.Do(func() {
		dir, err := os.MkdirTemp("", "homerslab-test-exe-")
		if err == nil {
			err = os.Chmod(dir, 0o755)
		}
		var b []byte
		if err == nil {
			b, err = os.ReadFile(os.Args[0])
		}
		if err == nil {
			copiedPath = filepath.Join(dir, filepath.Base(os.Args[0]))
			err = os.WriteFile(copiedPath, b, 0o755)
		}
		copiedErr = err
	})
	if copiedErr != nil {
		t.Fatal(copiedErr)
	}
	return copiedPath
}

// Edge stands in for Caddy's admin API on the edge's socket: it takes a configuration at /load and gives it back at
// /config/, re-marshalled as Caddy does. Restart puts it back to its boot configuration, which has no sites.
type Edge struct {
	mu      sync.Mutex
	config  []byte
	loads   int
	running bool // the unit has started: until then nothing answers on the socket
}

// Boot is the edge's configuration as it starts (packaging/edge/boot.json).
const Boot = `{"admin":{"listen":"unix//run/homerslab-edge/admin.sock|0660","config":{"persist":false}}}`

func startEdge(t testing.TB, sock string) *Edge {
	e := &Edge{config: []byte(Boot)}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(e.serve)}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return e
}

func (e *Edge) serve(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running { // as a socket nobody listens on: the connection goes
		if c, _, err := http.NewResponseController(w).Hijack(); err == nil {
			_ = c.Close()
		}
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/load":
		b, _ := io.ReadAll(r.Body)
		if !json.Valid(b) {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}
		e.config, e.loads = b, e.loads+1
	case r.Method == http.MethodGet && r.URL.Path == "/config/":
		var v any
		_ = json.Unmarshal(e.config, &v)
		b, _ := json.Marshal(v)
		_, _ = w.Write(b)
	default:
		http.NotFound(w, r)
	}
}

// Config is the configuration the edge runs.
func (e *Edge) Config() []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	return bytes.Clone(e.config)
}

// Loads is how many configurations the edge took.
func (e *Edge) Loads() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loads
}

// Restart is the edge starting again, on its boot configuration. The edge's unit starts only when the agent starts
// it.
func (e *Edge) Restart() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.config, e.running = []byte(Boot), true
}
