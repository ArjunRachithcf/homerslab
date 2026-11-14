package agent_test

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/host"
	"github.com/ArjunRachithcf/homerslab/internal/mesh"
	"github.com/ArjunRachithcf/homerslab/internal/mesh/netbird"
	"github.com/ArjunRachithcf/homerslab/internal/mesh/tailscale"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

func meshStatus(t *testing.T, sock string) agent.MeshStatus {
	t.Helper()
	_, body := call(t, sock, "GET", "/v1/mesh", nil)
	var s agent.MeshStatus
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// stockDaemons stands for NetBird's daemon and tailscaled running as their packages start them: NetBird's socket at
// /run/netbird.sock, tailscaled's in a folder everyone may enter.
func stockDaemons(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "run/tailscale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "run/tailscale"), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(root, netbird.ControlSocket.Link))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
}

func mode(t *testing.T, root, rel string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestTheAgentPutsTheMeshClientsSocketsWhereOnlyRootMayGo(t *testing.T) {
	testenv.Isolate(t)
	sock, h := agenttest.StartWith(t, agent.Config{})
	stockDaemons(t, h.Root)
	call(t, sock, "POST", "/v1/firewall/check", nil)
	for _, c := range []mesh.ControlSocket{netbird.ControlSocket, tailscale.ControlSocket} {
		if b, err := os.ReadFile(filepath.Join(h.Root, c.DropInPath)); err != nil || string(b) != string(c.DropIn) {
			t.Errorf("%s's drop-in: %q, %v", c.Unit, b, err)
		}
		if h.Times("systemctl --no-block try-restart "+c.Unit) != 1 {
			t.Errorf("%s wasn't restarted once with its drop-in: %v", c.Unit, h.Cmds())
		}
		if m := mode(t, h.Root, c.Dir); m != 0o700 {
			t.Errorf("%s's socket folder is %v after the agent's run, want 0700", c.Unit, m)
		}
	}
	if l, err := os.Readlink(filepath.Join(h.Root, netbird.ControlSocket.Link)); err != nil || l != "/run/netbird/netbird.sock" {
		t.Errorf("NetBird's CLI path: %q, %v, want a link to the socket in its folder", l, err)
	}
	if s := meshStatus(t, sock); len(s.Drift) != 0 {
		t.Errorf("putting the drop-ins in place for the first time is drift: %+v", s.Drift)
	}
	before := len(h.Cmds())
	for range 3 {
		call(t, sock, "POST", "/v1/firewall/check", nil)
	}
	if got := h.Cmds()[before:]; len(got) != 0 && hasSystemctl(got) {
		t.Errorf("checks with everything in place ran %v", got)
	}
}

func TestTheMeshClientsSocketsAreLeftAloneWhileTheyDontRun(t *testing.T) {
	testenv.Isolate(t)
	sock, h := agenttest.StartWith(t, agent.Config{})
	call(t, sock, "POST", "/v1/firewall/check", nil)
	for _, c := range []mesh.ControlSocket{netbird.ControlSocket, tailscale.ControlSocket} {
		if h.Times("systemctl --no-block try-restart "+c.Unit) != 0 {
			t.Errorf("%s was restarted though it doesn't run", c.Unit)
		}
		if _, err := os.Stat(filepath.Join(h.Root, c.DropInPath)); err != nil {
			t.Errorf("%s has no drop-in for when it starts: %v", c.Unit, err)
		}
	}
	if h.Times("systemctl daemon-reload") != 1 {
		t.Errorf("systemd wasn't told of the drop-ins once: %v", h.Cmds())
	}
}

func TestASocketFolderOpenedAgainIsDriftAndClosed(t *testing.T) {
	testenv.Isolate(t)
	sock, h := agenttest.StartWith(t, agent.Config{})
	stockDaemons(t, h.Root)
	call(t, sock, "POST", "/v1/firewall/check", nil)
	for i, c := range []mesh.ControlSocket{netbird.ControlSocket, tailscale.ControlSocket} {
		if err := os.Chmod(filepath.Join(h.Root, c.Dir), 0o755); err != nil {
			t.Fatal(err)
		}
		call(t, sock, "POST", "/v1/firewall/check", nil)
		if m := mode(t, h.Root, c.Dir); m != 0o700 {
			t.Errorf("%s's socket folder is %v after the check, want 0700", c.Unit, m)
		}
		if n := h.Times("systemctl --no-block try-restart " + c.Unit); n != 2 {
			t.Errorf("%s restarted %d times, want twice (its drop-in, then the drift)", c.Unit, n)
		}
		s := meshStatus(t, sock)
		if want := c.Unit + "-socket-open"; len(s.Drift) != i+1 || s.Drift[i].What != want {
			t.Errorf("the drift after %s's folder opened: %+v, want %s", c.Unit, s.Drift, want)
		}
	}
}

func TestNetBirdsSocketBackAtItsStockPathIsDrift(t *testing.T) {
	testenv.Isolate(t)
	sock, h := agenttest.StartWith(t, agent.Config{})
	call(t, sock, "POST", "/v1/firewall/check", nil)
	// Someone started the daemon by hand, as its package would, and it listens at /run/netbird.sock again.
	if err := os.MkdirAll(filepath.Join(h.Root, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(h.Root, netbird.ControlSocket.Link))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	call(t, sock, "POST", "/v1/firewall/check", nil)
	if s := meshStatus(t, sock); len(s.Drift) != 1 || s.Drift[0].What != "netbird.service-socket-open" {
		t.Errorf("the drift: %+v", s.Drift)
	}
	if h.Times("systemctl --no-block try-restart netbird.service") != 1 {
		t.Errorf("NetBird wasn't restarted: %v", h.Cmds())
	}
	if l, err := os.Readlink(filepath.Join(h.Root, netbird.ControlSocket.Link)); err != nil || l != "/run/netbird/netbird.sock" {
		t.Errorf("NetBird's CLI path after the check: %q, %v", l, err)
	}
}

func TestAChangedSocketDropInIsDriftAndPutBack(t *testing.T) {
	testenv.Isolate(t)
	sock, h := agenttest.StartWith(t, agent.Config{})
	stockDaemons(t, h.Root)
	call(t, sock, "POST", "/v1/firewall/check", nil)
	path := filepath.Join(h.Root, tailscale.ControlSocket.DropInPath)
	if err := os.WriteFile(path, []byte("[Service]\nRuntimeDirectoryMode=0755\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h.Root, netbird.ControlSocket.DropInPath)); err != nil {
		t.Fatal(err)
	}
	call(t, sock, "POST", "/v1/firewall/check", nil)
	for _, c := range []mesh.ControlSocket{netbird.ControlSocket, tailscale.ControlSocket} {
		if b, err := os.ReadFile(filepath.Join(h.Root, c.DropInPath)); err != nil || string(b) != string(c.DropIn) {
			t.Errorf("%s's drop-in after the check: %q, %v", c.Unit, b, err)
		}
		if n := h.Times("systemctl --no-block try-restart " + c.Unit); n != 2 {
			t.Errorf("%s restarted %d times, want twice", c.Unit, n)
		}
	}
	s := meshStatus(t, sock)
	if len(s.Drift) != 2 || s.Drift[0].What != "netbird.service-socket-drop-in" || s.Drift[1].What != "tailscaled.service-socket-drop-in" {
		t.Errorf("the drift: %+v", s.Drift)
	}
}

func hasSystemctl(cmds []host.Cmd) bool {
	for _, c := range cmds {
		if c.Name == "systemctl" {
			return true
		}
	}
	return false
}

// A socket folder of another owner's is open to that owner. Only root can give it one, so this runs as root (CI's).
func TestASocketFolderOfAnotherOwnersIsDrift(t *testing.T) {
	testenv.Isolate(t)
	if os.Geteuid() != 0 {
		t.Skip("only root can give a folder to another user")
	}
	sock, h := agenttest.StartWith(t, agent.Config{})
	stockDaemons(t, h.Root)
	call(t, sock, "POST", "/v1/firewall/check", nil)
	if err := os.Chown(filepath.Join(h.Root, tailscale.ControlSocket.Dir), 65534, 65534); err != nil {
		t.Fatal(err)
	}
	call(t, sock, "POST", "/v1/firewall/check", nil)
	if s := meshStatus(t, sock); len(s.Drift) != 1 || s.Drift[0].What != "tailscaled.service-socket-open" {
		t.Errorf("the drift: %+v", s.Drift)
	}
}
