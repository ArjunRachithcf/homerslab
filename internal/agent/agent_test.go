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
	"strconv"
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
		"the host's processes":   {"homerslab-web", "services: {web: {image: x, pid: host}}\n", http.StatusUnprocessableEntity, "off-floor"},
	} {
		resp, body := post(t, sock, "/v1/projects/read", map[string]string{"project": c.project, "compose": c.compose})
		var e struct{ Error string }
		_ = json.Unmarshal(body, &e)
		if resp.StatusCode != c.status || e.Error != c.code {
			t.Errorf("%s: %d %s, want %d %s", name, resp.StatusCode, body, c.status, c.code)
		}
	}
}

// A read runs in a child that ends at the deadline: the work really stops, the slot is free again at once, and the
// agent answers for its facts while a hostile read burns CPU. It holds under the agent unit's own capabilities, where
// a root agent can't signal its child (#138): CI runs it with exactly those.
func TestAHostileReadIsKilledAtItsDeadline(t *testing.T) {
	testenv.Isolate(t)
	real := agent.HostileReads(t, agenttest.Executable(t), 300*time.Millisecond)
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

// A read's child starts as the agent's own read user (homerslab-read, not the shared nobody) with no groups when the
// agent is root, can't be dumped, runs under its own limits and is first in line for the OOM killer, all read back
// from the kernel; a file shaped to make Compose work hard is refused quickly.
func TestAReadRunsConfinedAndRefusesWorkFast(t *testing.T) {
	testenv.Isolate(t)
	dir, err := agent.ChildDir()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	cmd, control, err := agent.ChildCommand(ctx, agenttest.Executable(t), dir, "homerslab-web", 5)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	cmd.Stdin = strings.NewReader("services: {web: {image: x}}\n")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	was, _ := os.ReadFile("/proc/self/oom_score_adj")
	if err := agent.StartChild(cmd); err != nil {
		t.Fatal(err)
	}
	// The child has its OOM score from the agent, which raises its own only while the child starts.
	if now, _ := os.ReadFile("/proc/self/oom_score_adj"); string(now) != string(was) {
		t.Errorf("the agent's own oom_score_adj is %q after a read's start, want %q as before", now, was)
	}
	err = cmd.Wait()
	out := stdout.Bytes()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	var res struct {
		Project  *engine.Project
		Confined struct {
			UID, GID        int
			Groups          string
			Limits          map[string]uint64
			OOMScoreAdj     string
			DeadlineMs      int64
			Controlled      bool
			Dumpable        int
			DumpableAtStart int
		}
	}
	if err := json.Unmarshal(out, &res); err != nil || res.Project == nil {
		t.Fatalf("%v: %s", err, out)
	}
	c := res.Confined
	if os.Geteuid() == 0 {
		uid, gid, err := agent.ReadIDs()
		if err != nil {
			t.Fatal(err)
		}
		if c.UID != int(uid) || c.GID != int(gid) || c.Groups != "" || uid == 65534 {
			t.Errorf("the child ran as uid %d, gid %d, groups %q; want homerslab-read (%d:%d) and no groups", c.UID, c.GID, c.Groups, uid, gid)
		}
		// Not dumpable from the start, by the kernel's own rule: it runs a copy of the agent its user can't read
		// (mode 0711), so no process of its user, one left behind by an earlier read even, can attach to it before
		// it makes itself so (#190).
		if c.DumpableAtStart == 1 {
			t.Error("the child was dumpable at its start")
		}
	}
	if c.Dumpable != 0 {
		t.Errorf("the child is dumpable (%d)", c.Dumpable)
	}
	// the CPU limit is soft at 5 s, and the hard one a second later, so the child is told before it is killed
	want := map[string]uint64{"core": 0, "cpu": 5, "cpuHard": 6, "files": 32, "fileSize": 1 << 20}
	if _, ok := c.Limits["data"]; ok || !agent.RaceDetector {
		want["data"] = 384 << 20
	}
	for k, v := range want {
		if got, ok := c.Limits[k]; !ok || got != v {
			t.Errorf("limit %s = %d, want %d", k, got, v)
		}
	}
	if c.OOMScoreAdj != "1000" {
		t.Errorf("oom_score_adj = %q, want 1000", c.OOMScoreAdj)
	}
	// It was handed what is left of the agent's deadline, and watches its control pipe.
	if c.DeadlineMs <= 6000 || c.DeadlineMs > 7000 || !c.Controlled {
		t.Errorf("the child's deadline %d ms, controlled %v; want what is left of 7 s, and its control pipe watched", c.DeadlineMs, c.Controlled)
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
		"its own deadline":    {`cat >/dev/null; echo '{"error":"deadline"}'; exit 1`, http.StatusGatewayTimeout, "read-timeout"},
		"a real CPU limit":    {"cat >/dev/null; ulimit -S -t 1; while :; do :; done", http.StatusGatewayTimeout, "read-timeout"},
		"Go's out of memory":  {"cat >/dev/null; echo 'fatal error: runtime: out of memory' >&2; exit 2", http.StatusUnprocessableEntity, "read-too-big"},
		"Go can't allocate":   {"cat >/dev/null; echo 'fatal error: runtime: cannot allocate memory' >&2; exit 2", http.StatusUnprocessableEntity, "read-too-big"},
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

// A read's child ends itself at the deadline it was handed, on its own clock, and says so: a root agent without
// CAP_KILL can't signal a child that runs as another user (#138). It does with one core, as the agent's unit gives it, while
// the read burns CPU, and long before its CPU limit.
func TestAReadsChildEndsAtItsOwnDeadline(t *testing.T) {
	testenv.Isolate(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "agent", "read-project-burn", "--cpu-seconds", "30", "--deadline", "300ms", "--", "homerslab-web")
	child.Env = append(os.Environ(), "GOMAXPROCS=1")
	child.Stdin = strings.NewReader("services: {web: {image: x}}\n")
	child.Dir = t.TempDir()
	start := time.Now()
	said, _ := child.Output()
	if took := time.Since(start); !strings.Contains(string(said), `"error":"deadline"`) || took < 300*time.Millisecond || took > 2*time.Second {
		t.Errorf("a child past its deadline said %q after %s, want deadline at 300 ms", said, took)
	}
}

// A read's child ends when the write end of its control pipe closes, which the kernel does when the agent dies,
// however it dies: a root agent without CAP_KILL can't send the parent-death signal to a child that runs as another user
// (#138). While the pipe stays open, the child waits for its file.
func TestAReadsChildEndsWhenItsControlPipeCloses(t *testing.T) {
	testenv.Isolate(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	in, keep, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	defer keep.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "agent", "read-project", "--deadline", "1m", "--control-fd", "3", "--", "homerslab-web")
	child.Stdin, child.ExtraFiles, child.Dir = in, []*os.File{r}, t.TempDir()
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	r.Close()
	ended := make(chan time.Time, 1)
	go func() { _ = child.Wait(); ended <- time.Now() }()
	select {
	case <-ended:
		t.Fatal("the child ended while its control pipe was open")
	case <-time.After(300 * time.Millisecond):
	}
	closed := time.Now()
	w.Close()
	select {
	case at := <-ended:
		if took := at.Sub(closed); took > time.Second {
			t.Errorf("the child ended %s after its control pipe closed", took)
		}
	case <-time.After(4 * time.Second):
		t.Error("the child outlived its control pipe")
	}

	// A control file that isn't a pipe could stay open after the agent is gone: the child refuses to read.
	f, err := os.Create(filepath.Join(t.TempDir(), "not-a-pipe"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	child = exec.CommandContext(ctx, os.Args[0], "agent", "read-project", "--control-fd", "3", "--", "homerslab-web")
	child.Stdin, child.ExtraFiles, child.Dir = strings.NewReader("services: {web: {image: x}}\n"), []*os.File{f}, t.TempDir()
	if out, err := child.CombinedOutput(); err == nil || !strings.Contains(string(out), "not a pipe") {
		t.Errorf("a control file that isn't a pipe: %v %s, want it refused", err, out)
	}

	// Nor can a pipe's write end, which the child itself would hold open (#183).
	r2, w2, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	defer w2.Close()
	child = exec.CommandContext(ctx, os.Args[0], "agent", "read-project", "--control-fd", "3", "--", "homerslab-web")
	child.Stdin, child.ExtraFiles, child.Dir = strings.NewReader("services: {web: {image: x}}\n"), []*os.File{w2}, t.TempDir()
	if out, err := child.CombinedOutput(); err == nil || !strings.Contains(string(out), "not the read end") {
		t.Errorf("a control pipe's write end: %v %s, want it refused", err, out)
	}
}

// A read's child can't be dumped: no other process of its user, not even its parent, may open its memory or trace it,
// to read its file or fake its answer, wherever Yama allows that (#183). The child makes itself so before it reads.
func TestAReadsChildCantBeTraced(t *testing.T) {
	testenv.Isolate(t)
	peek := exec.Command(agenttest.Executable(t), "-test.run=^$")
	peek.Env = append(os.Environ(), "HOMERSLAB_TEST_PEEK=1")
	if os.Geteuid() == 0 { // as the agent's reads run
		uid, gid, err := agent.ReadIDs()
		if err != nil {
			t.Fatal(err)
		}
		peek.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{}}}
	}
	out, err := peek.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		t.Fatalf("%v: %s", err, exit.Stderr)
	} else if err != nil {
		t.Fatal(err)
	}
	var res struct{ Mem, Trace string }
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if res.Mem != "denied" || res.Trace != "denied" {
		t.Errorf("a process of the read's own user opened its memory: %s, traced it: %s; want both denied", res.Mem, res.Trace)
	}
}

// When the agent is root, its reads run as homerslab-read, a user only they use, which the packages make: never as
// nobody, whom other processes share and so could signal or trace a read's child. Without that user, a root agent
// refuses to read rather than fall back to another (#183).
func TestReadsRunAsAUserOnlyTheyUse(t *testing.T) {
	testenv.Isolate(t)
	dir := t.TempDir()
	passwd := func(name, lines string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("root:x:0:0:root:/root:/bin/sh\nnobody:x:65534:65534:Nobody:/:/usr/sbin/nologin\n"+lines), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	group := func(name, lines string) string {
		p := filepath.Join(dir, name+".group")
		if err := os.WriteFile(p, []byte("root:x:0:\nnogroup:x:65534:\nhomerslab:x:981:\n"+lines), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	goodGroup := group("good", "homerslab-read:x:979:\n")
	uid, gid, err := agent.ReadIDsFrom(passwd("good", "homerslab:x:981:981::/var/lib/homerslab:/usr/sbin/nologin\nhomerslab-read:x:980:979:HomersLab compose reads:/:/usr/sbin/nologin\n"), goodGroup)
	if err != nil || uid != 980 || gid != 979 {
		t.Errorf("homerslab-read = %d:%d %v, want 980:979", uid, gid, err)
	}
	for name, lines := range map[string]string{
		"missing":          "homerslab:x:981:981::/var/lib/homerslab:/usr/sbin/nologin\n",
		"root's uid":       "homerslab-read:x:0:979::/:/usr/sbin/nologin\n",
		"root's group":     "homerslab-read:x:980:0::/:/usr/sbin/nologin\n",
		"nobody's uid":     "homerslab-read:x:65534:979::/:/usr/sbin/nologin\n",
		"a shared uid":     "homerslab-read:x:980:979::/:/usr/sbin/nologin\nother:x:980:980::/:/bin/sh\n",
		"a broken entry":   "homerslab-read:x:nine:979::/:/usr/sbin/nologin\n",
		"a prefix of ours": "homerslab-reader:x:980:979::/:/usr/sbin/nologin\n",
		"another's group":  "homerslab-read:x:980:981::/:/usr/sbin/nologin\n",
		"nogroup":          "homerslab-read:x:980:65534::/:/usr/sbin/nologin\n",
	} {
		if uid, gid, err := agent.ReadIDsFrom(passwd(name, lines), goodGroup); err == nil {
			t.Errorf("%s: homerslab-read = %d:%d, want it refused", name, uid, gid)
		}
	}
	// Its group is its own too: named for it, and no other group has its gid (docker's, say, would open the engine).
	good := passwd("good", "homerslab-read:x:980:979::/:/usr/sbin/nologin\n")
	for name, lines := range map[string]string{
		"no group":       "",
		"another name":   "docker:x:979:\n",
		"a shared gid":   "homerslab-read:x:979:\ndocker:x:979:\n",
		"members":        "homerslab-read:x:979:arjun\n",
		"a broken group": "homerslab-read:x:nine:\n",
	} {
		if uid, gid, err := agent.ReadIDsFrom(good, group(name, lines)); err == nil {
			t.Errorf("%s: homerslab-read = %d:%d, want it refused", name, uid, gid)
		}
	}
	if _, _, err := agent.ReadIDsFrom(filepath.Join(dir, "none"), goodGroup); err == nil {
		t.Error("no passwd file: want the read user refused")
	}
	if _, _, err := agent.ReadIDsFrom(good, filepath.Join(dir, "none")); err == nil {
		t.Error("no group file: want the read user refused")
	}

	// A root agent without its read user starts no child, and says the reader didn't start.
	if os.Geteuid() != 0 {
		t.Log("not root: the agent's reads run as its own user, so the rest needs root (CI runs it so)")
		return
	}
	marker, err := os.MkdirTemp("", "homerslab-marker-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(marker)
	if err := os.Chmod(marker, 0o777); err != nil {
		t.Fatal(err)
	}
	agent.ShellReads(t, 5*time.Second, "cat >/dev/null; id -u >"+filepath.Join(marker, "started"))
	agent.ReadUsersFrom(t, passwd("without", "homerslab:x:981:981::/var/lib/homerslab:/usr/sbin/nologin\n"))
	sock := agenttest.Start(t, agenttest.Primary())
	resp, out := post(t, sock, "/v1/projects/read", map[string]string{"project": "homerslab-web", "compose": "services: {web: {image: x}}\n"})
	started, _ := os.ReadFile(filepath.Join(marker, "started"))
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(string(out), "no-reader") || len(started) != 0 {
		t.Errorf("a root agent without homerslab-read: %d %s, a child started as %q; want 500 no-reader and none", resp.StatusCode, out, started)
	}
}

// A root agent's read runs a copy of the agent that its child's user can't read, so the kernel starts the child not
// dumpable (#190); that holds only while fs.suid_dumpable isn't 1 ("debug"), where the kernel leaves it dumpable.
// There, a root agent refuses to read, and starts no child.
func TestARootAgentRefusesToReadWhereItsChildWouldBeDumpable(t *testing.T) {
	testenv.Isolate(t)
	if os.Geteuid() != 0 {
		t.Skip("not root: the agent's reads run as its own user (CI runs it as root)")
	}
	marker, err := os.MkdirTemp("", "homerslab-marker-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(marker)
	if err := os.Chmod(marker, 0o777); err != nil {
		t.Fatal(err)
	}
	agent.ShellReads(t, 5*time.Second, "cat >/dev/null; id -u >"+filepath.Join(marker, "started")+`; echo '{"error":"deadline"}'`)
	sysctl := filepath.Join(t.TempDir(), "suid_dumpable")
	agent.SuidDumpableFrom(t, sysctl)
	sock := agenttest.Start(t, agenttest.Primary())
	body := map[string]string{"project": "homerslab-web", "compose": "services: {web: {image: x}}\n"}
	for _, v := range []string{"1\n", "", "x\n"} {
		if err := os.WriteFile(sysctl, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
		resp, out := post(t, sock, "/v1/projects/read", body)
		started, _ := os.ReadFile(filepath.Join(marker, "started"))
		if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(string(out), "no-reader") || len(started) != 0 {
			t.Errorf("fs.suid_dumpable %q: %d %s, a child started as %q; want 500 no-reader and none", v, resp.StatusCode, out, started)
		}
	}
	for _, v := range []string{"0\n", "2\n"} {
		if err := os.WriteFile(sysctl, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Remove(filepath.Join(marker, "started"))
		resp, out := post(t, sock, "/v1/projects/read", body)
		if started, _ := os.ReadFile(filepath.Join(marker, "started")); resp.StatusCode != http.StatusGatewayTimeout || len(started) == 0 {
			t.Errorf("fs.suid_dumpable %q: %d %s, a child started as %q; want one started", v, resp.StatusCode, out, started)
		}
	}
}

// A read's child that is stopped (SIGSTOP, by itself or another process of its user) ignores its deadline and its
// control pipe, its CPU limit doesn't count, and a root agent without CAP_KILL can't end it: it still holds the read
// slot no longer than the deadline and the child's WaitDelay. The agent reaps it whenever it ends, and refuses new
// reads while MaxLiveReads children are alive, so left children can't pile up (#183). An agent that may kill the child
// ends it at once, so this means most under the unit's capabilities, as CI runs it.
func TestAStoppedReadDoesntHoldTheSlot(t *testing.T) {
	testenv.Isolate(t)
	real := agent.ShellReads(t, 300*time.Millisecond, "cat >/dev/null; kill -STOP $$")
	sock := agenttest.Start(t, agenttest.Primary())
	body := map[string]string{"project": "homerslab-web", "compose": "services: {web: {image: x}}\n"}
	type answer struct {
		status int
		out    string
	}
	read := func() (answer, time.Duration) {
		start := time.Now()
		got := make(chan answer, 1)
		go func() {
			resp, out := post(t, sock, "/v1/projects/read", body)
			got <- answer{resp.StatusCode, string(out)}
		}()
		select {
		case a := <-got:
			return a, time.Since(start)
		case <-time.After(5 * time.Second):
			return answer{}, time.Since(start)
		}
	}
	canKill := agentCanKill(t)
	stopped := stoppedChildren(t)
	for range agent.MaxLiveReads { // each needs the slot the one before it left
		if a, took := read(); a.status != http.StatusGatewayTimeout || !strings.Contains(a.out, "read-timeout") || took > 2*time.Second {
			t.Fatalf("a stopped read: %d %s after %s, want 504 read-timeout by its deadline and WaitDelay", a.status, a.out, took)
		}
		if canKill {
			break // the agent ended it: none is left behind
		}
	}
	left := stoppedChildren(t)
	for pid := range stopped {
		delete(left, pid)
	}
	if !canKill {
		if len(left) != agent.MaxLiveReads {
			t.Fatalf("%d stopped children left behind, want %d", len(left), agent.MaxLiveReads)
		}
		if a, took := read(); a.status != http.StatusServiceUnavailable || !strings.Contains(a.out, "busy") || took > time.Second {
			t.Errorf("with %d children left: %d %s after %s, want 503 busy at once", len(left), a.status, a.out, took)
		}
	}

	// Once one goes on, it ends (its deadline is long past), and the agent reaps it: no zombie is left.
	for pid := range left {
		cont := exec.Command("/bin/sh", "-c", "kill -CONT "+strconv.Itoa(pid))
		if os.Geteuid() == 0 { // the agent may not signal it; its own user may
			uid, gid, err := agent.ReadIDs()
			if err != nil {
				t.Fatal(err)
			}
			cont.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{}}}
		}
		if out, err := cont.CombinedOutput(); err != nil {
			t.Errorf("SIGCONT to %d: %v %s", pid, err, out)
		}
		reaped := false
		for range 100 {
			if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
				reaped = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !reaped {
			stat, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			t.Errorf("the stopped read's child %d wasn't reaped once it ended: %s", pid, stat)
		}
	}

	real()
	if a, took := read(); a.status != http.StatusOK {
		t.Errorf("after the stopped reads: %d %s after %s, want the slot free and the read done", a.status, a.out, took)
	}
}

// agentCanKill says whether this process may signal a read's child: as its own user, or with CAP_KILL.
func agentCanKill(t *testing.T) bool {
	t.Helper()
	if os.Geteuid() != 0 {
		return true
	}
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(line, "CapEff:"); ok {
			eff, err := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			return eff&(1<<5) != 0 // CAP_KILL
		}
	}
	t.Fatal("no CapEff in /proc/self/status")
	return false
}

// stoppedChildren is this process's stopped children.
func stoppedChildren(t *testing.T) map[int]bool {
	t.Helper()
	out := map[int]bool{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		// pid (comm) state ppid ...
		_, rest, ok := strings.Cut(string(b), ") ")
		if f := strings.Fields(rest); ok && len(f) > 1 && f[0] == "T" && f[1] == strconv.Itoa(os.Getpid()) {
			out[pid] = true
		}
	}
	return out
}

// A read's child dies with the agent, also when the agent is root and the child starts as homerslab-read: its control pipe
// closes with the agent. (The parent-death signal, which the IDs set in the fork before it don't clear, needs CAP_KILL,
// which the agent's unit doesn't have.)
func TestAReadsChildDiesWithTheAgent(t *testing.T) {
	testenv.Isolate(t)
	helper := exec.Command(os.Args[0], "-test.run=^$")
	helper.Env = append(os.Environ(), "HOMERSLAB_TEST_PARENT=1", "HOMERSLAB_TEST_EXE="+agenttest.Executable(t))
	// The child's standard input stays open after its parent dies, so only its control pipe (or the parent-death
	// signal, with CAP_KILL) can end it.
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

// A real read child stopped by its CPU limit says so, and the agent answers read-timeout: Go ignores SIGXCPU unless
// asked, so the child asks. A child that doesn't, killed at the hard limit, is told apart by the CPU time it used.
func TestAReadStoppedByItsCPULimitIsATimeout(t *testing.T) {
	testenv.Isolate(t)
	// The child itself says it reached its limit.
	child := exec.Command(os.Args[0], "agent", "read-project-burn", "--cpu-seconds", "1", "--", "homerslab-web")
	child.Stdin = strings.NewReader("services: {web: {image: x}}\n")
	child.Dir = t.TempDir()
	said, _ := child.Output()
	if !strings.Contains(string(said), `"error":"cpu-limit"`) {
		t.Errorf("a Go child past its soft CPU limit said %q, want cpu-limit", said)
	}

	body := map[string]string{"project": "homerslab-web", "compose": "services: {web: {image: x}}\n"}
	agent.ChildReads(t, agenttest.Executable(t), "read-project-burn", 20*time.Second, 1)
	sock := agenttest.Start(t, agenttest.Primary())
	start := time.Now()
	if resp, out := post(t, sock, "/v1/projects/read", body); resp.StatusCode != http.StatusGatewayTimeout || !strings.Contains(string(out), "read-timeout") {
		t.Errorf("a Go child past its CPU limit: %d %s after %s, want 504 read-timeout", resp.StatusCode, out, time.Since(start))
	}

	agent.ShellReads(t, 20*time.Second, "cat >/dev/null; ulimit -S -t 1; ulimit -H -t 2; trap '' XCPU; while :; do :; done")
	sock = agenttest.Start(t, agenttest.Primary())
	if resp, out := post(t, sock, "/v1/projects/read", body); resp.StatusCode != http.StatusGatewayTimeout || !strings.Contains(string(out), "read-timeout") {
		t.Errorf("a child that ignores SIGXCPU, killed at the hard limit: %d %s, want 504 read-timeout", resp.StatusCode, out)
	}
}

// A real read child that runs out of memory under its data limit is the file's doing: read-too-big, whichever way
// Go says it ("out of memory", or "cannot allocate memory").
func TestAReadOutOfMemoryIsTooBig(t *testing.T) {
	testenv.Isolate(t)
	if agent.RaceDetector {
		t.Skip("the race detector leaves the data limit off; CI runs this without it")
	}
	if agent.CgoOn {
		t.Skip("with cgo the child can die of runtime/cgo or the GC before it says why; CI runs this with CGO_ENABLED=0")
	}
	agent.ChildReads(t, agenttest.Executable(t), "read-project-hog", 20*time.Second, 10)
	sock := agenttest.Start(t, agenttest.Primary())
	body := map[string]string{"project": "homerslab-web", "compose": "services: {web: {image: x}}\n"}
	if resp, out := post(t, sock, "/v1/projects/read", body); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(out), "read-too-big") {
		t.Errorf("a child out of memory: %d %s, want 422 read-too-big", resp.StatusCode, out)
	}
}
