package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/node"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

func get(t *testing.T, sock, path string) (*http.Response, []byte) {
	t.Helper()
	c := http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	resp, err := c.Get("http://agent" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp, body
}

func TestAgentReportsTheNodesFacts(t *testing.T) {
	testenv.Isolate(t)
	sock := agenttest.Start(t, agenttest.Primary())

	resp, body := get(t, sock, "/v1/node")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("GET /v1/node: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var got node.Facts
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	want := node.Facts{
		Hostname:             "study-pc",
		Distro:               node.Distro{ID: "fedora", Name: "Fedora Linux", Version: "43", Pretty: "Fedora Linux 43 (Workstation Edition)"},
		Architecture:         "x86_64",
		MemoryBytes:          32768000 * 1024,
		MemoryAvailableBytes: 20000000 * 1024,
		Disks: []node.Disk{
			{Name: "nvme0n1", Model: "Samsung SSD 980 PRO 1TB", SizeBytes: 1953525168 * 512},
			{Name: "sda", Model: "WDC WD40EFRX-68N", SizeBytes: 7814037168 * 512, Rotational: true},
		},
		TPM:   node.TPM{Present: true, Version: "2.0"},
		GPUs:  []node.GPU{{Vendor: "NVIDIA", Model: "GeForce RTX 3060 Lite Hash Rate", PCIID: "10de:2504"}},
		Space: &node.Space{Path: "/var/lib/homerslab", FreeBytes: 412e9, SizeBytes: 1e12},
	}
	if g, w := mustJSON(t, got), mustJSON(t, want); g != w {
		t.Fatalf("facts:\n got %s\nwant %s", g, w)
	}
}

func TestAgentReportsAMachineWithoutTPMOrGPU(t *testing.T) {
	testenv.Isolate(t)
	sock := agenttest.Start(t, fstest.MapFS{
		"usr/lib/os-release":            {Data: []byte("NAME=\"Debian GNU/Linux\"\nID=debian\nVERSION_ID=\"13\"\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n")},
		"proc/meminfo":                  {Data: []byte("MemTotal:        3884000 kB\n")},
		"proc/sys/kernel/arch":          {Data: []byte("aarch64\n")},
		"proc/sys/kernel/hostname":      {Data: []byte("pi\n")},
		"sys/block/mmcblk0/size":        {Data: []byte("62333952\n")},
		"sys/block/mmcblk0/device/name": {Data: []byte("SN64G\n")},
	})

	_, body := get(t, sock, "/v1/node")
	var got node.Facts
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	want := node.Facts{
		Hostname:     "pi",
		Distro:       node.Distro{ID: "debian", Name: "Debian GNU/Linux", Version: "13", Pretty: "Debian GNU/Linux 13 (trixie)"},
		Architecture: "arm64",
		MemoryBytes:  3884000 * 1024,
		Disks:        []node.Disk{{Name: "mmcblk0", Model: "SN64G", SizeBytes: 62333952 * 512}},
		TPM:          node.TPM{Present: false},
		GPUs:         []node.GPU{},
	}
	if g, w := mustJSON(t, got), mustJSON(t, want); g != w {
		t.Fatalf("facts:\n got %s\nwant %s", g, w)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if gpus, ok := raw["gpus"].([]any); !ok || len(gpus) != 0 {
		t.Errorf("gpus = %#v, want an empty list, not null", raw["gpus"])
	}
}

func TestAgentAnswersOnlyItsResources(t *testing.T) {
	testenv.Isolate(t)
	sock := agenttest.Start(t, agenttest.Primary())
	for _, path := range []string{"/", "/v1/nodes", "/v1/node/x"} {
		if resp, body := get(t, sock, path); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: %d %s, want 404", path, resp.StatusCode, body)
		}
	}
}

func TestAgentSocketIsOnlyForItsGroup(t *testing.T) {
	testenv.Isolate(t)
	sock := agenttest.Start(t, agenttest.Primary())
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o660 {
		t.Errorf("socket mode %o, want 660: the agent's owner and the manager's group only", perm)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The space for apps' data is the free space of the filesystem that holds HomersLab's own folder, read from the
// machine itself: the nearest folder that exists, when HomersLab's isn't there yet.
func TestAgentReportsTheSpaceOfTheFilesystemForAppsData(t *testing.T) {
	testenv.Isolate(t)
	root := t.TempDir()
	for name, data := range map[string]string{"etc/os-release": "ID=fedora\n", "proc/meminfo": "MemTotal: 1024 kB\n"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, body := get(t, agenttest.Start(t, node.Dir(root)), "/v1/node")
	var got node.Facts
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Space == nil || got.Space.Path != "/var/lib/homerslab" || got.Space.SizeBytes == 0 || got.Space.FreeBytes > got.Space.SizeBytes {
		t.Errorf("space = %+v", got.Space)
	}
}

func post(t *testing.T, sock, path string, body any) (*http.Response, []byte) {
	t.Helper()
	c := http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	b, _ := json.Marshal(body)
	resp, err := c.Post("http://agent"+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

// For planning, the agent reads an app's compose project through the Compose SDK, as the standard compose command
// would run it, and reaches no engine to do so.
func TestAgentReadsAComposeProjectForPlanning(t *testing.T) {
	testenv.Isolate(t)
	sock := agenttest.Start(t, agenttest.Primary())
	compose := "services:\n  web:\n    image: example/web:1@sha256:" + strings.Repeat("1", 64) + "\n"

	resp, body := post(t, sock, "/v1/projects/read", map[string]string{"project": "homerslab-web", "compose": compose})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var p engine.Project
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Name != "homerslab-web" || len(p.Services) != 1 || p.Services[0].Name != "web" || !strings.HasPrefix(p.Services[0].Image, "example/web:1@sha256:") {
		t.Errorf("read %+v", p)
	}

	for name, c := range map[string]struct {
		project, compose string
		status           int
		code             string
	}{
		"another project's name": {"homelab", compose, http.StatusBadRequest, "not-ours"},
		"a file naming another":  {"homerslab-web", "include: [/etc/shadow]\n" + compose, http.StatusUnprocessableEntity, "not-plain"},
		"a broken file":          {"homerslab-web", "services: {web: {imagee: x}}\n", http.StatusUnprocessableEntity, "invalid-compose"},
	} {
		resp, body := post(t, sock, "/v1/projects/read", map[string]string{"project": c.project, "compose": c.compose})
		var e struct{ Error string }
		_ = json.Unmarshal(body, &e)
		if resp.StatusCode != c.status || e.Error != c.code {
			t.Errorf("%s: %d %s, want %d %s", name, resp.StatusCode, body, c.status, c.code)
		}
	}
}

// A read runs in a child that the agent kills at the deadline: the work really stops, the slot is free again at once,
// and the agent answers for its facts while a hostile read burns CPU.
func TestAHostileReadIsKilledAtItsDeadline(t *testing.T) {
	testenv.Isolate(t)
	real := agent.HostileReads(t, 300*time.Millisecond)
	sock := agenttest.Start(t, agenttest.Primary())
	body := map[string]string{"project": "homerslab-web", "compose": "services: {web: {image: x}}\n"}

	facts := make(chan time.Duration, 1)
	go func() {
		time.Sleep(100 * time.Millisecond) // while the hostile read runs
		start := time.Now()
		if resp, _ := get(t, sock, "/v1/node"); resp.StatusCode == http.StatusOK {
			facts <- time.Since(start)
		}
		close(facts)
	}()
	start := time.Now()
	resp, out := post(t, sock, "/v1/projects/read", body)
	if took := time.Since(start); resp.StatusCode != http.StatusGatewayTimeout || !strings.Contains(string(out), "read-timeout") || took > 2*time.Second {
		t.Errorf("a hostile read: %d %s after %s, want 504 read-timeout at its deadline", resp.StatusCode, out, took)
	}
	if took, ok := <-facts; !ok || took > time.Second {
		t.Errorf("the facts during a hostile read: answered %v after %s", ok, took)
	}

	real()
	start = time.Now()
	if resp, out := post(t, sock, "/v1/projects/read", body); resp.StatusCode != http.StatusOK || time.Since(start) > 5*time.Second {
		t.Errorf("right after the deadline: %d %s after %s, want the slot free and the read done", resp.StatusCode, out, time.Since(start))
	}
}

// A read's child starts as nobody with no groups when the agent is root, under its own limits and first in line for
// the OOM killer, all read back from the kernel; a file shaped to make Compose work hard is refused quickly.
func TestAReadRunsConfinedAndRefusesWorkFast(t *testing.T) {
	testenv.Isolate(t)
	dir, err := agent.ChildDir()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	cmd := agent.ChildCommand(context.Background(), agenttest.Executable(t), dir, "homerslab-web", 5)
	cmd.Stdin = strings.NewReader("services: {web: {image: x}}\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	var res struct {
		Project  *engine.Project
		Confined struct {
			UID, GID    int
			Groups      string
			Limits      map[string]uint64
			OOMScoreAdj string
		}
	}
	if err := json.Unmarshal(out, &res); err != nil || res.Project == nil {
		t.Fatalf("%v: %s", err, out)
	}
	c := res.Confined
	if os.Geteuid() == 0 && (c.UID != 65534 || c.GID != 65534 || c.Groups != "") {
		t.Errorf("the child ran as uid %d, gid %d, groups %q; want nobody (65534) and no groups", c.UID, c.GID, c.Groups)
	}
	want := map[string]uint64{"core": 0, "cpu": 5, "files": 32, "fileSize": 1 << 20}
	if _, ok := c.Limits["addressSpace"]; ok {
		want["addressSpace"] = 3 << 29
	}
	for k, v := range want {
		if got, ok := c.Limits[k]; !ok || got != v {
			t.Errorf("limit %s = %d, want %d", k, got, v)
		}
	}
	if c.OOMScoreAdj != "1000" {
		t.Errorf("oom_score_adj = %q, want 1000", c.OOMScoreAdj)
	}

	var fan strings.Builder
	fan.WriteString("services:\n")
	for i := range 64 {
		fmt.Fprintf(&fan, "  s%d: {image: x, depends_on: [", i)
		for j := max(0, i-40); j < i; j++ {
			fmt.Fprintf(&fan, "s%d, ", j)
		}
		fan.WriteString("]}\n")
	}
	sock := agenttest.Start(t, agenttest.Primary())
	start := time.Now()
	resp, body := post(t, sock, "/v1/projects/read", map[string]string{"project": "homerslab-web", "compose": fan.String()})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "not-plain") || time.Since(start) > 2*time.Second {
		t.Errorf("a depends_on fan: %d %s after %s, want it refused fast", resp.StatusCode, body, time.Since(start))
	}
}

// A child that ends without answering is said for what ended it: its CPU limit is a read-timeout, running out of
// memory is the file's doing; neither is a reader that didn't start.
func TestAReadsEndingIsSaid(t *testing.T) {
	testenv.Isolate(t)
	body := map[string]string{"project": "homerslab-web", "compose": "services: {web: {image: x}}\n"}
	for name, c := range map[string]struct {
		script string
		status int
		code   string
	}{
		"its CPU limit":       {"cat >/dev/null; kill -XCPU $$", http.StatusGatewayTimeout, "read-timeout"},
		"Go's out of memory":  {"cat >/dev/null; echo 'fatal error: runtime: out of memory' >&2; exit 2", http.StatusUnprocessableEntity, "read-too-big"},
		"the kernel's kill":   {"cat >/dev/null; kill -KILL $$", http.StatusUnprocessableEntity, "read-too-big"},
		"a reader that broke": {"cat >/dev/null; echo broken >&2; exit 3", http.StatusInternalServerError, "no-reader"},
	} {
		t.Run(name, func(t *testing.T) {
			agent.ShellReads(t, 5*time.Second, c.script)
			sock := agenttest.Start(t, agenttest.Primary())
			resp, out := post(t, sock, "/v1/projects/read", body)
			if resp.StatusCode != c.status || !strings.Contains(string(out), c.code) {
				t.Errorf("%d %s, want %d %s", resp.StatusCode, out, c.status, c.code)
			}
		})
	}
}

// A name that isn't one of HomersLab's starts no child at all.
func TestANameThatIsntOursStartsNoChild(t *testing.T) {
	testenv.Isolate(t)
	marker := filepath.Join(t.TempDir(), "started")
	agent.ShellReads(t, 5*time.Second, "touch "+marker)
	sock := agenttest.Start(t, agenttest.Primary())
	for _, name := range []string{"homelab", "--cpu-seconds", "-x", "homerslab-web --help"} {
		resp, out := post(t, sock, "/v1/projects/read", map[string]string{"project": name, "compose": "services: {}\n"})
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(out), "not-ours") {
			t.Errorf("%q: %d %s, want 400 not-ours", name, resp.StatusCode, out)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a child started for a name that isn't ours")
	}
}

// A read's child dies with the agent, also when the agent is root and the child starts as nobody: the IDs are set in
// the fork before the parent-death signal, so changing them doesn't clear it.
func TestAReadsChildDiesWithTheAgent(t *testing.T) {
	testenv.Isolate(t)
	helper := exec.Command(os.Args[0], "-test.run=^$")
	helper.Env = append(os.Environ(), "HOMERSLAB_TEST_PARENT=1", "HOMERSLAB_TEST_EXE="+agenttest.Executable(t))
	// The child's standard input stays open after its parent dies, so only the parent-death signal can end it.
	in, keep, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	defer keep.Close()
	helper.Stdin = in
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Fscan(stdout, &pid); err != nil || pid <= 0 {
		t.Fatalf("the helper gave no child: %v", err)
	}
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	for range 100 {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); strings.Contains(string(b), ") Z ") {
			return // a zombie whose parent is gone: dead, waiting to be reaped
		}
		time.Sleep(20 * time.Millisecond)
	}
	status, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Errorf("the child %d outlived the agent:\n%s", pid, status)
}

// Reading a compose project never reaches a container engine, wherever the environment points.
func TestAgentReadsWithoutReachingAnEngine(t *testing.T) {
	testenv.Isolate(t)
	engineSock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", engineSock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var reached atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			reached.Add(1)
			c.Close()
		}
	}()
	t.Setenv("DOCKER_HOST", "unix://"+engineSock)
	sock := agenttest.Start(t, agenttest.Primary())
	resp, out := post(t, sock, "/v1/projects/read", map[string]string{"project": "homerslab-web", "compose": "services: {web: {image: x}}\n"})
	time.Sleep(50 * time.Millisecond)
	if resp.StatusCode != http.StatusOK || reached.Load() != 0 {
		t.Errorf("%d %s; the engine was reached %d times, want none", resp.StatusCode, out, reached.Load())
	}
}
