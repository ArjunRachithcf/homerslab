package agent_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/storage"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// sharePassword is a share's password in the tests: it must reach the sealed credential, and nothing else.
const sharePassword = "share-password-7a1f93c2d4"

func smbShare() agent.StorageRequest {
	return agent.StorageRequest{Kind: "share", Protocol: storage.SMB, Server: "100.64.1.2", Share: "media", Username: "homerslab", Password: sharePassword}
}

// A local folder is a storage location where the agent's checks pass (#28): a folder of its own, reached without a
// symlink, holding nothing of the machine's or HomersLab's.
func TestAFolderIsAStorageLocationWhereItsChecksPass(t *testing.T) {
	testenv.Isolate(t)
	sock, h, _ := joined(t, agent.Config{})
	for _, d := range []string{"srv/media", "etc"} {
		if err := os.MkdirAll(filepath.Join(h.Root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Symlink(filepath.Join(h.Root, "etc"), filepath.Join(h.Root, "srv/etc"))
	if code, body := call(t, sock, "PUT", "/v1/storage/films", agent.StorageRequest{Kind: "folder", Path: "/srv/media"}); code != http.StatusOK {
		t.Fatalf("a folder: %d %s", code, body)
	}
	for path, want := range map[string]string{"/etc": "forbidden-path", "/srv/etc": "symlink-in-path", "/srv/none": "not-a-folder", "/srv/../etc": "bad-path"} {
		code, body := call(t, sock, "PUT", "/v1/storage/other", agent.StorageRequest{Kind: "folder", Path: path})
		if code != http.StatusUnprocessableEntity || !strings.Contains(string(body), want) {
			t.Errorf("%s: %d %s, want %s", path, code, body, want)
		}
	}
	for name, r := range map[string]agent.StorageRequest{
		"no kind":                  {Path: "/srv/media"},
		"a folder with a password": {Kind: "folder", Path: "/srv/media", Password: "x"},
	} {
		if code, _ := call(t, sock, "PUT", "/v1/storage/other", r); code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, code)
		}
	}
	// A location inside another, or holding one, is refused: an app that writes to one could put a symlink in the
	// other's way. While no app uses a location, the manager may give its id another folder (it kept none).
	_ = os.MkdirAll(filepath.Join(h.Root, "srv/media/films"), 0o755)
	for _, p := range []string{"/srv/media/films", "/srv"} {
		if code, body := call(t, sock, "PUT", "/v1/storage/other", agent.StorageRequest{Kind: "folder", Path: p}); code != http.StatusConflict ||
			!strings.Contains(string(body), "storage-overlaps") {
			t.Errorf("%s beside /srv/media: %d %s", p, code, body)
		}
	}
	if code, _ := call(t, sock, "PUT", "/v1/storage/films", agent.StorageRequest{Kind: "folder", Path: "/srv/media/films"}); code != http.StatusOK {
		t.Errorf("another folder under an unused id: %d", code)
	}
	// The same location again answers as it was added, whatever its folder is like now (a disk not mounted yet).
	_ = os.Remove(filepath.Join(h.Root, "srv/media/films"))
	if code, _ := call(t, sock, "PUT", "/v1/storage/films", agent.StorageRequest{Kind: "folder", Path: "/srv/media/films"}); code != http.StatusOK {
		t.Errorf("the same folder again: %d", code)
	}
	call(t, sock, "PUT", "/v1/storage/films", agent.StorageRequest{Kind: "folder", Path: "/srv/media"})
	if code, _ := call(t, sock, "PUT", "/v1/storage/Not_An_ID", agent.StorageRequest{Kind: "folder", Path: "/srv/media"}); code != http.StatusBadRequest {
		t.Errorf("a bad id: %d", code)
	}
	var list agent.StorageList
	code, body := call(t, sock, "GET", "/v1/storage", nil)
	if code != http.StatusOK || json.Unmarshal(body, &list) != nil || len(list.Locations) != 1 ||
		list.Locations[0] != (agent.StorageStatus{ID: "films", Kind: "folder", Path: "/srv/media", Source: "/srv/media"}) {
		t.Errorf("the locations: %d %s", code, body)
	}
}

// A share is mounted only over the mesh, never over the LAN (#28): its server must be a mesh peer that the kernel routes
// through the mesh's interface, and the firewall table keeps this machine's packets to it on the mesh whatever the route
// says later. Its credentials are a sealed credential, and nowhere else: not in the agent's state, its unit, its logs,
// its answers or any command's arguments.
func TestAShareIsMountedOverTheMeshOnly(t *testing.T) {
	testenv.Isolate(t)
	var logs bytes.Buffer
	sock, h, state := joined(t, agent.Config{Sysroot: withSELinux{agenttest.Primary()}, Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})

	for name, c := range map[string]struct {
		server, route, want string
	}{
		"a LAN address":                        {"192.168.1.20", "", "not-on-the-mesh"},
		"a mesh-looking address on the LAN":    {"100.64.9.9", "enp5s0", "not-on-the-mesh"},
		"an address routed through a VPN":      {"100.64.9.10", "tun0", "not-on-the-mesh"},
		"another private range off the mesh":   {"10.0.0.5", "", "not-on-the-mesh"},
		"a name, which could resolve anywhere": {"nas.local", "", "bad-server"},
	} {
		if c.route != "" {
			h.Route(c.server, c.route)
		}
		r := smbShare()
		r.Server = c.server
		code, body := call(t, sock, "PUT", "/v1/storage/films", r)
		if code/100 != 4 || !strings.Contains(string(body), c.want) {
			t.Errorf("%s: %d %s, want %s", name, code, body, c.want)
		}
	}
	if h.Ran("systemctl enable /"+agent.UnitsDir+"/homerslab-storage-films.service") >= 0 {
		t.Fatal("a refused share was mounted")
	}

	code, body := call(t, sock, "PUT", "/v1/storage/films", smbShare())
	if code != http.StatusOK {
		t.Fatalf("a share on the mesh: %d %s", code, body)
	}
	unit, err := os.ReadFile(filepath.Join(h.Root, agent.UnitsDir, "homerslab-storage-films.service"))
	want := storage.ShareUnit(storage.Share{ID: "films", Protocol: storage.SMB, Server: netip.MustParseAddr("100.64.1.2"), Share: "media",
		Credential: filepath.Join(state, "storage", "films.cred"), SELinux: true})
	if err != nil || !bytes.Equal(unit, want) {
		t.Errorf("the share's unit: %v\n%s\nwant\n%s", err, unit, want)
	}
	enable, restart := h.Ran("systemctl enable /"+agent.UnitsDir+"/homerslab-storage-films.service"), h.Ran("systemctl restart homerslab-storage-films.service")
	if enable < 0 || restart < enable {
		t.Errorf("systemd: enabled at %d, mounted at %d", enable, restart)
	}
	sealed, err := os.ReadFile(filepath.Join(state, "storage", "films.cred"))
	if got, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(string(sealed), "sealed:")); err != nil || string(got) != "username=homerslab\npassword="+sharePassword+"\n" {
		t.Errorf("the sealed credential: %q %v", got, err)
	}
	enc := -1
	for i, c := range h.Cmds() {
		if c.Name == "systemd-creds" && slices.Contains(c.Args, "--name=storage-films") && enc < 0 {
			enc = i
		}
	}
	if enc < 0 || enc > restart {
		t.Errorf("sealed at %d, mounted at %d", enc, restart)
	}
	// The table keeps the share's server on the mesh, from before the mount on.
	if table, _ := h.Table(); !strings.Contains(table, `ip daddr { 100.64.1.2 } oifname != { "wt0" } drop`) {
		t.Errorf("the table doesn't keep the share on the mesh:\n%s", table)
	}

	// The password is nowhere but in the sealed credential.
	files := map[string][]byte{"the answer": body, "the logs": logs.Bytes(), "the unit": unit}
	files["the agent's state"], _ = os.ReadFile(filepath.Join(state, agent.StateFile))
	_, files["the list"] = call(t, sock, "GET", "/v1/storage", nil)
	for _, c := range h.Cmds() {
		files["the arguments of "+c.Name] = append(files["the arguments of "+c.Name], strings.Join(c.Args, " ")...)
	}
	for what, b := range files {
		if bytes.Contains(b, []byte(sharePassword)) {
			t.Errorf("%s carries the share's password", what)
		}
	}

	// An NFS export takes no credentials; a share that doesn't mount leaves nothing behind.
	nfs := agent.StorageRequest{Kind: "share", Protocol: storage.NFS, Server: "100.64.1.3", Share: "/volume1/Music"}
	if code, body := call(t, sock, "PUT", "/v1/storage/music", nfs); code != http.StatusOK {
		t.Errorf("an NFS export: %d %s", code, body)
	}
	nfs.Password = "x"
	if code, _ := call(t, sock, "PUT", "/v1/storage/music2", nfs); code != http.StatusBadRequest {
		t.Errorf("an NFS export with a password: %d", code)
	}
	h.Fail("systemctl restart homerslab-storage-photos.service")
	r := smbShare()
	r.Server = "100.64.1.4"
	if code, body := call(t, sock, "PUT", "/v1/storage/photos", r); code != http.StatusBadGateway || !strings.Contains(string(body), "mount-failed") {
		t.Errorf("a share that doesn't mount: %d %s", code, body)
	}
	for _, f := range []string{filepath.Join(h.Root, agent.UnitsDir, "homerslab-storage-photos.service"), filepath.Join(state, "storage", "photos.cred")} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s left behind", f)
		}
	}
	if table, _ := h.Table(); strings.Contains(table, "100.64.1.4") {
		t.Errorf("the table keeps a share that never mounted:\n%s", table)
	}
	var list agent.StorageList
	_, b := call(t, sock, "GET", "/v1/storage", nil)
	if json.Unmarshal(b, &list) != nil || len(list.Locations) != 2 {
		t.Errorf("the locations: %s", b)
	}
}

// Before the node is on a mesh, no share is mounted at all.
func TestNoShareIsMountedBeforeTheMesh(t *testing.T) {
	testenv.Isolate(t)
	sock, _ := agenttest.StartWith(t, agent.Config{})
	if code, body := call(t, sock, "PUT", "/v1/storage/films", smbShare()); code != http.StatusConflict || !strings.Contains(string(body), "no-mesh") {
		t.Errorf("before the mesh: %d %s", code, body)
	}
}

// An app gets the storage locations its plan names (#28): a bind of the location's folder in the service that asked,
// read-only where the plan says; one the agent doesn't have, or for a service the app doesn't run, is refused.
func TestAnAppGetsTheStorageLocationsItsPlanNames(t *testing.T) {
	testenv.Isolate(t)
	sock, h, state := signedIn(t, agent.Config{})
	for id, dir := range map[string]string{"films": "/srv/media", "uploads": "/srv/uploads"} {
		_ = os.MkdirAll(filepath.Join(h.Root, dir), 0o755)
		if code, body := call(t, sock, "PUT", "/v1/storage/"+id, agent.StorageRequest{Kind: "folder", Path: dir}); code != http.StatusOK {
			t.Fatalf("%s: %d %s", id, code, body)
		}
	}
	r := withSecrets()
	r.Storage = []agent.AppStorage{{Location: "films", Service: "web", Path: "/media", ReadOnly: true}, {Location: "uploads", Service: "db", Path: "/uploads"}}
	install(t, sock, "ledger", r)
	o, _ := os.ReadFile(filepath.Join(state, "apps", "ledger", "homerslab.yaml"))
	for _, want := range []string{"        source: \"/srv/media\"\n        target: \"/media\"\n        read_only: true\n",
		"        source: \"/srv/uploads\"\n        target: \"/uploads\"\n        read_only: false\n"} {
		if !strings.Contains(string(o), want) {
			t.Errorf("the app's compose file lacks %q:\n%s", want, o)
		}
	}
	// An app uses it now: its folder stays the one it was.
	_ = os.MkdirAll(filepath.Join(h.Root, "srv/other"), 0o755)
	if code, _ := call(t, sock, "PUT", "/v1/storage/films", agent.StorageRequest{Kind: "folder", Path: "/srv/other"}); code != http.StatusConflict {
		t.Errorf("another folder for a location in use: %d", code)
	}
	for name, c := range map[string]struct {
		s    agent.AppStorage
		code int
		want string
	}{
		"a location the agent doesn't have": {agent.AppStorage{Location: "photos", Service: "web", Path: "/photos"}, http.StatusUnprocessableEntity, "no-such-storage"},
		"a service the app doesn't run":     {agent.AppStorage{Location: "films", Service: "worker", Path: "/media"}, http.StatusUnprocessableEntity, "no-such-service"},
		"a path out of the container's":     {agent.AppStorage{Location: "films", Service: "web", Path: "/media/../etc"}, http.StatusBadRequest, "bad-storage"},
	} {
		r := withSecrets()
		r.Storage = []agent.AppStorage{c.s}
		if code, body := call(t, sock, "PUT", "/v1/apps/other", r); code != c.code || !strings.Contains(string(body), c.want) {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
}

// On SELinux, a folder is a location only where every app reads it at its own level: labelled for containers at no
// level. The agent never relabels it (an adopted folder stays untouched); it refuses, so the admin labels it (#28).
func TestOnSELinuxAFolderIsALocationOnlyWhenEveryAppReadsIt(t *testing.T) {
	testenv.Isolate(t)
	labels := map[string]string{}
	sock, h, _ := joined(t, agent.Config{Sysroot: withSELinux{agenttest.Primary()}, FolderLabel: func(p string) (string, error) {
		return labels[p], nil
	}})
	for d, l := range map[string]string{"srv/media": "system_u:object_r:var_t:s0", "srv/films": "system_u:object_r:container_file_t:s0"} {
		_ = os.MkdirAll(filepath.Join(h.Root, d), 0o755)
		labels[filepath.Join(h.Root, d)] = l
	}
	if code, body := call(t, sock, "PUT", "/v1/storage/media", agent.StorageRequest{Kind: "folder", Path: "/srv/media"}); code != http.StatusUnprocessableEntity ||
		!strings.Contains(string(body), "selinux-label") {
		t.Errorf("a folder containers can't read: %d %s", code, body)
	}
	if code, body := call(t, sock, "PUT", "/v1/storage/films", agent.StorageRequest{Kind: "folder", Path: "/srv/films"}); code != http.StatusOK {
		t.Errorf("a folder labelled for containers: %d %s", code, body)
	}
}

// A folder is checked again whenever an app is given it: a symlink put in its way since it was added is refused, so the
// engine never follows it to another folder of the machine's (#28).
func TestAFolderIsCheckedAgainWhenAnAppIsGivenIt(t *testing.T) {
	testenv.Isolate(t)
	sock, h, _ := signedIn(t, agent.Config{})
	_ = os.MkdirAll(filepath.Join(h.Root, "srv/media"), 0o755)
	_ = os.MkdirAll(filepath.Join(h.Root, "etc"), 0o755)
	call(t, sock, "PUT", "/v1/storage/films", agent.StorageRequest{Kind: "folder", Path: "/srv/media"})
	_ = os.Remove(filepath.Join(h.Root, "srv/media"))
	_ = os.Symlink(filepath.Join(h.Root, "etc"), filepath.Join(h.Root, "srv/media"))
	r := withSecrets()
	r.Storage = []agent.AppStorage{{Location: "films", Service: "web", Path: "/media", ReadOnly: true}}
	if code, body := call(t, sock, "PUT", "/v1/apps/ledger", r); code != http.StatusUnprocessableEntity || !strings.Contains(string(body), "symlink-in-path") {
		t.Errorf("a folder swapped for a symlink: %d %s", code, body)
	}
}

// An app given a share sees its mount, and its unit waits for the share's own unit and starts the app only once the
// share is mounted: an app never writes into the empty folder under a share (#28).
func TestAnAppGivenAShareStartsOnlyOnceItIsMounted(t *testing.T) {
	testenv.Isolate(t)
	sock, h, state := signedIn(t, agent.Config{})
	if code, body := call(t, sock, "PUT", "/v1/storage/uploads", smbShare()); code != http.StatusOK {
		t.Fatalf("the share: %d %s", code, body)
	}
	r := withSecrets()
	r.Storage = []agent.AppStorage{{Location: "uploads", Service: "web", Path: "/uploads"}}
	install(t, sock, "ledger", r)
	o, _ := os.ReadFile(filepath.Join(state, "apps", "ledger", "homerslab.yaml"))
	if !strings.Contains(string(o), "        source: \"/var/lib/homerslab-storage/uploads\"\n        target: \"/uploads\"\n        read_only: false\n") {
		t.Errorf("the app's compose file:\n%s", o)
	}
	u, _ := os.ReadFile(filepath.Join(h.Root, agent.UnitsDir, "homerslab-app-ledger.service"))
	for _, want := range []string{"\nWants=network-online.target homerslab-storage-uploads.service\n", "\nExecStartPre=/usr/bin/mountpoint -q /var/lib/homerslab-storage/uploads\n"} {
		if !strings.Contains(string(u), want) {
			t.Errorf("the app's unit lacks %q:\n%s", want, u)
		}
	}
}
