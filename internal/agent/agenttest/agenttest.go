// Package agenttest runs a real agent for other packages' tests, on a socket in the test's own directory, with a
// stand-in for the machine's tools (Host): no test's agent ever runs nft, systemctl or netbird on this machine.
package agenttest

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
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
)

// Primary is the system root of the invented primary in testdata/primary.
func Primary() agent.Sysroot {
	_, self, _, _ := runtime.Caller(0)
	return os.DirFS(filepath.Join(filepath.Dir(self), "..", "..", "..", "testdata", "primary"))
}

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
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
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
}

// Run is the agent's host.Runner.
func (h *Host) Run(_ context.Context, c host.Cmd) ([]byte, error) {
	h.mu.Lock()
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
	case c.Name == "systemctl":
		return nil, nil
	case strings.HasPrefix(line, "netbird up"):
		return nil, nil
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

// Cmds are the commands the agent ran, in order.
func (h *Host) Cmds() []host.Cmd {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.cmds)
}

// Ran says whether the agent ran a command line, and at which position (or -1).
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
