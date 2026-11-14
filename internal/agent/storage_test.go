package agent_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

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
		"no kind": {Path: "/srv/media"},
		"a share": {Kind: "share", Path: "/srv/media"}, // #28's second part
	} {
		if code, _ := call(t, sock, "PUT", "/v1/storage/other", r); code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, code)
		}
	}
	if code, _ := call(t, sock, "PUT", "/v1/storage/films", agent.StorageRequest{Kind: "folder", Path: "/srv"}); code != http.StatusConflict {
		t.Errorf("another folder under the same id: %d", code)
	}
	if code, _ := call(t, sock, "PUT", "/v1/storage/films", agent.StorageRequest{Kind: "folder", Path: "/srv/media"}); code != http.StatusOK {
		t.Errorf("the same folder again: %d", code)
	}
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
