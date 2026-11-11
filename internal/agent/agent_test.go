package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// A read's child drops root and runs under its own limits; a file shaped to make Compose work hard is refused quickly.
func TestAReadRunsConfinedAndRefusesWorkFast(t *testing.T) {
	testenv.Isolate(t)
	cmd := exec.Command(os.Args[0], "agent", "read-project", "--cpu-seconds", "5", "homerslab-web")
	cmd.Stdin = strings.NewReader("services: {web: {image: x}}\n")
	cmd.Dir = t.TempDir()
	if os.Geteuid() == 0 {
		if err := os.Chown(cmd.Dir, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	var res struct {
		Project *engine.Project
		UID     int
		Limits  map[string]int
	}
	if err := json.Unmarshal(out, &res); err != nil || res.Project == nil {
		t.Fatalf("%v: %s", err, out)
	}
	if os.Geteuid() == 0 && res.UID != 65534 {
		t.Errorf("the child ran as %d, want nobody (65534)", res.UID)
	}
	for k, want := range map[string]int{"core": 0, "cpu": 5, "files": 32, "fileSize": 1 << 20} {
		if got, ok := res.Limits[k]; !ok || got != want {
			t.Errorf("limit %s = %d, want %d", k, got, want)
		}
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
