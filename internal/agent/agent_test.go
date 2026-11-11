package agent_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"testing"
	"testing/fstest"

	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
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
		Hostname:     "study-pc",
		Distro:       node.Distro{ID: "fedora", Name: "Fedora Linux", Version: "43", Pretty: "Fedora Linux 43 (Workstation Edition)"},
		Architecture: "x86_64",
		MemoryBytes:  32768000 * 1024,
		Disks: []node.Disk{
			{Name: "nvme0n1", Model: "Samsung SSD 980 PRO 1TB", SizeBytes: 1953525168 * 512},
			{Name: "sda", Model: "WDC WD40EFRX-68N", SizeBytes: 7814037168 * 512, Rotational: true},
		},
		TPM:  node.TPM{Present: true, Version: "2.0"},
		GPUs: []node.GPU{{Vendor: "NVIDIA", Model: "GeForce RTX 3060 Lite Hash Rate", PCIID: "10de:2504"}},
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
