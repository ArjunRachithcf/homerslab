package manager_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

type location struct {
	ID, Name, Kind, Node, Path, Protocol, Server, Share string
	Backup                                              bool
}

func locations(t *testing.T, p *primary) []location {
	t.Helper()
	var got struct{ Locations []location }
	if r := p.control("GET", "/api/v1/storage", nil); r.Code != http.StatusOK {
		t.Fatalf("the storage locations: %d %s", r.Code, r.Body)
	} else {
		r.json(t, &got)
	}
	return got.Locations
}

// withStorageAsk is the official catalog with BentoPDF asking for a storage location it only reads.
func withStorageAsk(t *testing.T) *primary {
	t.Helper()
	cat := withCatalog(t, map[string]func([]byte) []byte{"apps/bentopdf/app.yaml": func(b []byte) []byte {
		return append(b, []byte("storage:\n  - {id: library, what: The PDFs it opens, service: web, path: /pdfs}\n")...)
	}})
	p, _ := installable(t, cat)
	if err := os.MkdirAll(filepath.Join(p.host.Root, "srv/documents"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// The admin adds a storage location once, in Admin (#28): a local folder on the primary. An id is added once; the
// agent's refusals come back as they are.
func TestAnAdminAddsAStorageLocationOnce(t *testing.T) {
	testenv.Isolate(t)
	p := withStorageAsk(t)
	withDomain(p)
	r := p.admin("POST", "/api/v1/storage", map[string]any{"id": "documents", "name": "Documents", "kind": "folder", "path": "/srv/documents"})
	if r.Code != http.StatusCreated {
		t.Fatalf("a folder, in Admin: %d %s", r.Code, r.Body)
	}
	if r := p.admin("POST", "/api/v1/storage", map[string]any{"id": "documents", "name": "Again", "kind": "folder", "path": "/srv/documents"}); r.Code != http.StatusConflict {
		t.Errorf("the same id again: %d %s", r.Code, r.Body)
	}
	for name, body := range map[string]map[string]any{
		"an id that isn't one":   {"id": "../etc", "name": "X", "kind": "folder", "path": "/srv/documents"},
		"a name in lines":        {"id": "x", "name": "X services:", "kind": "folder", "path": "/srv/documents"},
		"a folder of the system": {"id": "x", "name": "X", "kind": "folder", "path": "/etc"},
		"a kind not built yet":   {"id": "x", "name": "X", "kind": "share", "server": "100.64.1.2"},
	} {
		if r := p.control("POST", "/api/v1/storage", body); r.Code/100 != 4 {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}
	want := location{ID: "documents", Name: "Documents", Kind: "folder", Node: "study-pc", Path: "/srv/documents"}
	if got := locations(t, p); len(got) != 1 || got[0] != want {
		t.Errorf("the locations: %+v", got)
	}
	// Signed out of Admin, nothing.
	if r := p.admin("GET", "/api/v1/storage", nil, func(r *http.Request) { r.Header.Del("Cookie") }); r.Code == http.StatusOK {
		t.Errorf("signed out: %d %s", r.Code, r.Body)
	}
}

// A manifest asks for a storage location, and the plan says which one and whether the app only reads it: read-only by
// default, as for media (#28). Applied, the app's service sees exactly that location, read-only.
func TestAPlanNamesTheStorageLocationAndApplyingBindsIt(t *testing.T) {
	testenv.Isolate(t)
	p := withStorageAsk(t)
	if r := p.control("POST", "/api/v1/plans", map[string]string{"app": "bentopdf"}); r.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(string(r.Body), "storage-location-needed") {
		t.Errorf("no location yet: %d %s", r.Code, r.Body)
	}
	p.control("POST", "/api/v1/storage", map[string]any{"id": "documents", "name": "Documents", "kind": "folder", "path": "/srv/documents"})
	var made struct {
		ID      string
		Storage []struct {
			Ask, Service, Path, Location, Name, Kind string
			ReadOnly                                 bool
		}
	}
	r := p.control("POST", "/api/v1/plans", map[string]any{"app": "bentopdf", "storage": map[string]string{"library": "documents"}})
	if r.Code != http.StatusCreated {
		t.Fatalf("planning: %d %s", r.Code, r.Body)
	}
	r.json(t, &made)
	if len(made.Storage) != 1 || made.Storage[0].Location != "documents" || made.Storage[0].Name != "Documents" || !made.Storage[0].ReadOnly ||
		made.Storage[0].Path != "/pdfs" || made.Storage[0].Service != "web" {
		t.Errorf("the plan's storage: %+v", made.Storage)
	}
	if r := p.control("POST", "/api/v1/plans/"+made.ID+"/apply", nil); r.Code != http.StatusAccepted {
		t.Fatalf("applying: %d %s", r.Code, r.Body)
	}
	// agenttest keeps the agent's state beside the machine's root.
	o, err := os.ReadFile(filepath.Join(filepath.Dir(p.host.Root), "state", "apps", "bentopdf", "homerslab.yaml"))
	if err != nil || !strings.Contains(string(o), "        source: \"/srv/documents\"\n        target: \"/pdfs\"\n        read_only: true\n") {
		t.Errorf("the app's compose file: %v\n%s", err, o)
	}
}

// A storage location is backed up only when the admin opts in, per location (#28): not by default. The choice shows in
// Admin and in the backup plan of an app that uses it, and a plan made before the choice changed is stale.
func TestAStorageLocationIsBackedUpOnlyWhenTheAdminOptsIn(t *testing.T) {
	testenv.Isolate(t)
	p := withStorageAsk(t)
	withDomain(p)
	p.control("POST", "/api/v1/storage", map[string]any{"id": "documents", "name": "Documents", "kind": "folder", "path": "/srv/documents"})
	type backups struct {
		ID      string
		Backups struct {
			Locations []struct {
				Location string
				BackedUp bool
			}
		}
	}
	plan := func() backups {
		var b backups
		p.control("POST", "/api/v1/plans", map[string]string{"app": "bentopdf"}).json(t, &b)
		return b
	}
	before := plan()
	if got := locations(t, p); got[0].Backup || len(before.Backups.Locations) != 1 || before.Backups.Locations[0].BackedUp {
		t.Errorf("backed up without opting in: %+v %+v", got, before.Backups)
	}
	if r := p.admin("PUT", "/api/v1/storage/documents/backup", map[string]bool{"backup": true}); r.Code != http.StatusOK {
		t.Fatalf("opting in, in Admin: %d %s", r.Code, r.Body)
	}
	after := plan()
	if got := locations(t, p); !got[0].Backup || !after.Backups.Locations[0].BackedUp || after.Backups.Locations[0].Location != "documents" {
		t.Errorf("opted in: %+v %+v", got, after.Backups)
	}
	if r := p.control("POST", "/api/v1/plans/"+before.ID+"/apply", nil); r.Code != http.StatusConflict {
		t.Errorf("a plan from before the choice: %d %s", r.Code, r.Body)
	}
	if r := p.control("PUT", "/api/v1/storage/photos/backup", map[string]bool{"backup": true}); r.Code != http.StatusNotFound {
		t.Errorf("a location that isn't there: %d", r.Code)
	}
}
