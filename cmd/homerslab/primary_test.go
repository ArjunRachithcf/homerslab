package main_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

const nobody = 65534

// On a real primary the one binary runs as two processes: the agent as root, the manager as an unprivileged user.
// This starts them so, from the same built binary, and opens first-time setup through both.
// It needs root, as in CI's Go container; elsewhere it is skipped.
func TestThePrimaryRunsTheManagerUnprivilegedAndTheAgentAsRoot(t *testing.T) {
	testenv.Isolate(t)
	if os.Geteuid() != 0 {
		t.Skip("needs root, to start the agent as root and the manager as another user")
	}
	dir, err := os.MkdirTemp("", "primary-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	must(t, os.Chmod(dir, 0o755))
	bin := filepath.Join(dir, "homerslab")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	must(t, build.Run())
	agentDir, managerDir := filepath.Join(dir, "agent"), filepath.Join(dir, "manager")
	must(t, os.Mkdir(agentDir, 0o755))
	must(t, os.Mkdir(managerDir, 0o700))
	must(t, os.Chown(managerDir, nobody, nobody))
	sysroot, err := filepath.Abs("../../testdata/primary")
	must(t, err)

	agentSock, controlSock := filepath.Join(agentDir, "agent.sock"), filepath.Join(managerDir, "manager.sock")
	agent := start(t, bin, nil, "agent", "--socket", agentSock, "--socket-group", fmt.Sprint(nobody), "--sysroot", sysroot,
		"--state-dir", filepath.Join(agentDir, "state"))
	waitForSocket(t, agentSock)
	manager := start(t, bin, &syscall.Credential{Uid: nobody, Gid: nobody, Groups: []uint32{}},
		"manager", "--db", filepath.Join(managerDir, "manager.db"), "--agent-socket", agentSock,
		"--control-socket", controlSock, "--listen", "127.0.0.1:0")
	waitForSocket(t, controlSock)

	if agent.Process.Pid == manager.Process.Pid {
		t.Fatal("one process")
	}
	if uid := processUID(t, agent.Process.Pid); uid != 0 {
		t.Errorf("the agent runs as uid %d, want root", uid)
	}
	if uid := processUID(t, manager.Process.Pid); uid != nobody {
		t.Errorf("the manager runs as uid %d, want %d", uid, nobody)
	}

	// The installer's step: the CLI, as root, asks the manager for a link.
	out, err := exec.Command(bin, "setup", "link", "--control-socket", controlSock).CombinedOutput()
	if err != nil {
		t.Fatalf("homerslab setup link: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`http://127\.0\.0\.1:(\d+)/setup#k=([A-Za-z0-9_-]+)`).FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("no link in:\n%s", out)
	}
	base := "http://127.0.0.1:" + m[1]

	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Post(base+"/api/v1/setup/claim", "application/json", strings.NewReader(`{"key":"`+m[2]+`","nonce":"a-tab-of-its-own-0123456789"}`))
	must(t, err)
	var session struct{ Token string }
	must(t, json.NewDecoder(resp.Body).Decode(&session))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || session.Token == "" {
		t.Fatalf("claiming the link: %s", resp.Status)
	}
	var facts struct {
		Hostname string
		Distro   struct{ Pretty string }
	}
	getJSON(t, c, base+"/api/v1/nodes/primary", session.Token, &facts)
	if facts.Hostname != "study-pc" || facts.Distro.Pretty != "Fedora Linux 43 (Workstation Edition)" {
		t.Errorf("the setup page's facts from the agent: %+v", facts)
	}

	// The control socket is the manager user's alone.
	fi, err := os.Stat(controlSock)
	must(t, err)
	if st := fi.Sys().(*syscall.Stat_t); fi.Mode().Perm() != 0o600 || st.Uid != nobody {
		t.Errorf("control socket: mode %o uid %d, want 600 and the manager's user", fi.Mode().Perm(), st.Uid)
	}
	fi, err = os.Stat(agentSock)
	must(t, err)
	if st := fi.Sys().(*syscall.Stat_t); fi.Mode().Perm() != 0o660 || st.Uid != 0 || st.Gid != nobody {
		t.Errorf("agent socket: mode %o %d:%d, want 660 root and the manager's group", fi.Mode().Perm(), st.Uid, st.Gid)
	}
}

func start(t *testing.T, bin string, cred *syscall.Credential, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), bin, args...)
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred, Pdeathsig: syscall.SIGKILL}
	stderr, err := cmd.StderrPipe()
	must(t, err)
	must(t, cmd.Start())
	read := make(chan struct{})
	go func() {
		defer close(read)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			t.Logf("%s: %s", args[0], sc.Text())
		}
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		<-read // its stderr ends when it exits; Wait comes after the last read
		_ = cmd.Wait()
	})
	return cmd
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	for range 300 {
		if c, err := net.Dial("unix", path); err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("nothing listens on %s", path)
}

func processUID(t *testing.T, pid int) int {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	must(t, err)
	for line := range strings.SplitSeq(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 2 && f[0] == "Uid:" {
			var uid int
			fmt.Sscan(f[2], &uid) // the effective uid
			return uid
		}
	}
	t.Fatal("no Uid in /proc status")
	return -1
}

func getJSON(t *testing.T, c *http.Client, url, token string, v any) {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Do(req)
	must(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s %s", url, resp.Status, b)
	}
	must(t, json.Unmarshal(b, v))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
