package manager_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// A full disk under the manager's database is said as such: reading setup goes on, and a write answers 507 disk-full
// instead of 500 internal (#70).

func TestReadingSetupWorksOnAFullDisk(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	b.mustAnswer("welcome", nil)
	p.m.FillDisk()
	for range 3 {
		p.clock.Add(2 * time.Minute) // more than a minute: the session's last use would be written
		for _, path := range []string{"/api/v1/setup", "/api/v1/nodes/primary"} {
			if r := b.do("GET", path, nil); r.Code != http.StatusOK {
				t.Errorf("GET %s on a full disk: %d %s, want 200", path, r.Code, r.Body)
			}
		}
	}
	if r := p.control("GET", "/api/v1/setup", nil); r.Code != http.StatusOK {
		t.Errorf("GET /api/v1/setup on the control socket, on a full disk: %d %s, want 200", r.Code, r.Body)
	}
}

func TestAWriteOnAFullDiskSaysTheDiskIsFull(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	p.m.FillDisk()
	if r := b.answer("welcome", nil); r.Code != http.StatusInsufficientStorage || r.errorCode(t) != "disk-full" {
		t.Errorf("an answer on a full disk: %d %s, want 507 disk-full", r.Code, r.Body)
	}
	r := p.control("POST", "/api/v1/setup/links", nil)
	var e struct{ Error, Folder string }
	r.json(t, &e)
	if r.Code != http.StatusInsufficientStorage || e.Error != "disk-full" || e.Folder != p.dbFolder() {
		t.Errorf("a new link on a full disk: %d %s, want 507 disk-full naming %s", r.Code, r.Body, p.dbFolder())
	}

	p = newPrimary(t)
	l := p.newLink()
	p.m.FillDisk()
	if r := p.browser(onTheLAN).claim(l); r.Code != http.StatusInsufficientStorage || r.errorCode(t) != "disk-full" {
		t.Errorf("a claim on a full disk: %d %s, want 507 disk-full", r.Code, r.Body)
	}
}

// The setup listener is on the LAN: its answers don't name the machine's folders.
func TestTheSetupListenerDoesNotNameTheDatabasesFolder(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	b := p.claimed()
	p.m.FillDisk()
	r := b.answer("welcome", nil)
	var e map[string]any
	r.json(t, &e)
	if _, ok := e["folder"]; ok {
		t.Errorf("the setup listener's disk-full answer names a folder: %s", r.Body)
	}
}
