package agent_test

// The mesh clients' real daemons, NetBird v0.82.0 and tailscaled v1.104.4, each in a container labelled for compose
// project hltest-200, started as their units start them with the drop-ins the agent wrote. It needs Docker: it runs
// when HOMEWARD_TEST_DOCKER_HOST names the engine to use, and is skipped otherwise. The agent never sees that engine
// (testenv points DOCKER_HOST at nowhere); only this harness does, explicitly.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/agent"
	"github.com/ArjunRachithcf/homeward/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homeward/internal/mesh"
	"github.com/ArjunRachithcf/homeward/internal/mesh/netbird"
	"github.com/ArjunRachithcf/homeward/internal/mesh/tailscale"
	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

// daemon is a mesh client's daemon in its image, with its packaged unit's [Service] lines (made to run in a
// container: no state on disk, no TUN, the log on the console), and its CLI's status command.
type daemon struct {
	sock   mesh.ControlSocket
	image  string
	unit   []string
	status []string
}

var daemons = []daemon{
	{
		sock:  netbird.ControlSocket,
		image: "netbirdio/netbird@sha256:eca5b274c635bfbb04ec6724a0712d1146190dd450eea199eeb83916fdd94965", // 0.82.0
		// As `netbird service install` writes it: the daemon's address on its command line.
		unit:   []string{"ExecStart=netbird service run --log-level info --daemon-addr unix:///var/run/netbird.sock --log-file console"},
		status: []string{"netbird", "status"},
	},
	{
		sock:  tailscale.ControlSocket,
		image: "tailscale/tailscale@sha256:ed6480b33c6eb67c9cbd203ed42b9fb57e361986725b6856def71b7a3d9e276c", // v1.104.4
		// cmd/tailscaled/tailscaled.service's.
		unit: []string{"RuntimeDirectory=tailscale", "RuntimeDirectoryMode=0755",
			"ExecStart=tailscaled --state=mem: --socket=/run/tailscale/tailscaled.sock --tun=userspace-networking"},
		status: []string{"tailscale", "status", "--json"},
	},
}

// startScript is what systemd does to start the unit with a drop-in, as a shell script: each drop-in line is merged
// over the unit's (a list setting adds to it, a single one replaces it; a folder named twice is made twice), and a setting the harness doesn't know fails
// the test, so no line of the agent's drop-ins goes untried.
func startScript(t *testing.T, unit []string, dropIn []byte) string {
	t.Helper()
	var dirs, env, pre []string
	mode, execStart := "0755", ""
	lines := slices.Clone(unit)
	for _, l := range strings.Split(string(dropIn), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") && l != "[Service]" {
			lines = append(lines, l)
		}
	}
	for _, l := range lines {
		k, v, _ := strings.Cut(l, "=")
		switch k {
		case "RuntimeDirectory":
			dirs = append(dirs, v)
		case "RuntimeDirectoryMode":
			mode = v
		case "Environment":
			env = append(env, v)
		case "ExecStartPre":
			pre = append(pre, strings.TrimPrefix(v, "-"))
		case "ExecStart":
			execStart = v
		default:
			t.Fatalf("the harness doesn't know %q", l)
		}
	}
	var b strings.Builder
	b.WriteString("set -e\n")
	for _, d := range dirs {
		fmt.Fprintf(&b, "mkdir -p /run/%s\nchmod %s /run/%s\n", d, mode, d)
	}
	for _, e := range env {
		fmt.Fprintf(&b, "export '%s'\n", e)
	}
	for _, p := range pre {
		b.WriteString(p + "\n")
	}
	b.WriteString("exec " + execStart + "\n")
	return b.String()
}

func TestTheMeshClientsControlSocketsAreRootsOnlyWithTheRealDaemons(t *testing.T) {
	testenv.Isolate(t)
	engine := os.Getenv("HOMEWARD_TEST_DOCKER_HOST")
	if engine == "" {
		t.Skip("set HOMEWARD_TEST_DOCKER_HOST to the engine for the daemons' containers")
	}
	docker := func(args ...string) ([]byte, error) {
		cmd := exec.Command("docker", args...)
		cmd.Env = []string{"DOCKER_HOST=" + engine, "HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.Bytes(), err
	}
	// The agent's run: its first check writes the drop-ins.
	sock, h := agenttest.StartWith(t, agent.Config{})
	call(t, sock, "POST", "/v1/firewall/check", nil)

	for _, d := range daemons {
		t.Run(d.sock.Unit, func(t *testing.T) {
			dropIn, err := os.ReadFile(filepath.Join(h.Root, d.sock.DropInPath))
			if err != nil {
				t.Fatal(err)
			}
			status := strings.Join(d.status, " ")
			// run starts the daemon in a container of its own (removed when it stops), ready once root's CLI answers.
			run := func(name string, dropIn []byte) {
				t.Helper()
				_, _ = docker("kill", name)
				if out, err := docker("run", "-d", "--rm", "--name", name, "--label", "com.docker.compose.project=hltest-200",
					"--network", "none", "--entrypoint", "sh", d.image, "-c", startScript(t, d.unit, dropIn)); err != nil {
					t.Fatalf("starting %s: %v\n%s", name, err, out)
				}
				t.Cleanup(func() { _, _ = docker("kill", name) })
				for i := 0; ; i++ {
					out, err := docker(append([]string{"exec", name}, d.status...)...)
					if err == nil {
						return
					}
					if i == 50 {
						logs, _ := docker("logs", name)
						t.Fatalf("%s as root: %v\n%s\n%s", status, err, out, logs)
					}
					time.Sleep(200 * time.Millisecond)
				}
			}
			nobody := func(name string) ([]byte, error) {
				return docker(append([]string{"exec", "-u", "65534:65534", name}, d.status...)...)
			}
			name := "hltest-200-" + strings.TrimSuffix(d.sock.Unit, ".service")

			// As packaged, any user on the node reaches the daemon: the hole the drop-in closes.
			run(name+"-stock", nil)
			if out, err := nobody(name + "-stock"); err != nil {
				t.Fatalf("nobody's %s with the stock unit: %v\n%s", status, err, out)
			}

			// After the agent's run, root's CLI still answers (run waits for it), and nobody's can't connect.
			run(name, dropIn)
			if out, err := nobody(name); err == nil || !strings.Contains(string(out), "connect: permission denied") {
				t.Errorf("nobody's %s after the agent's run: %v\n%s", status, err, out)
			}
			ls, _ := docker("exec", name, "stat", "-c", "%a %U", "/"+d.sock.Dir)
			if got := strings.TrimSpace(string(ls)); got != "700 root" {
				t.Errorf("/%s is %q, want 700 root", d.sock.Dir, got)
			}
			if d.sock.Link != "" {
				if l, _ := docker("exec", name, "readlink", "/"+d.sock.Link); strings.TrimSpace(string(l)) != "/"+d.sock.Socket {
					t.Errorf("/%s links to %q, want /%s", d.sock.Link, l, d.sock.Socket)
				}
			}
			// The folder opened again: anyone reaches the daemon, which is why the agent watches its mode.
			if out, err := docker("exec", name, "chmod", "0755", "/"+d.sock.Dir); err != nil {
				t.Fatalf("%v %s", err, out)
			}
			if out, err := nobody(name); err != nil {
				t.Errorf("nobody's %s with the folder opened: %v\n%s", status, err, out)
			}
		})
	}
}
