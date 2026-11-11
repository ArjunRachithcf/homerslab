package cli_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

func TestTheCLIListsTheCatalog(t *testing.T) {
	testenv.Isolate(t)
	ctl := running(t)
	code, out, errOut := run(t, "catalog", "--control-socket", ctl)
	if code != 0 {
		t.Fatalf("homerslab catalog: %d %s", code, errOut)
	}
	for _, want := range []string{"The official catalog, from HomersLab", "BentoPDF", "PDF tools", "Community, guarded at the edge", "Immich", "Photos", "Verified, native sign-in"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
}

// The CLI makes and shows the same plan Admin shows: one object, by its id.
func TestTheCLIShowsTheSamePlanAsAdmin(t *testing.T) {
	testenv.Isolate(t)
	ctl := running(t, true)
	code, out, errOut := run(t, "plan", "immich", "--json", "--control-socket", ctl)
	if code != 0 {
		t.Fatalf("homerslab plan immich --json: %d %s", code, errOut)
	}
	var p struct{ ID string }
	if err := json.Unmarshal([]byte(out), &p); err != nil || !strings.HasPrefix(p.ID, "p-") {
		t.Fatalf("%v: %s", err, out)
	}

	_, adminURL, _ := run(t, "admin", "--control-socket", ctl)
	base := strings.TrimSuffix(strings.TrimSpace(adminURL), "/store")
	resp, err := http.Get(base + "/api/v1/plans/" + p.ID)
	if err != nil {
		t.Fatal(err)
	}
	inAdmin, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(bytes.TrimSpace(inAdmin), bytes.TrimSpace([]byte(out))) {
		t.Errorf("Admin's plan %s:\n%d %s\nthe CLI's:\n%s", p.ID, resp.StatusCode, inAdmin, out)
	}

	code, words, errOut := run(t, "plan", "show", p.ID, "--control-socket", ctl)
	if code != 0 {
		t.Fatalf("homerslab plan show: %d %s", code, errOut)
	}
	for _, want := range []string{"Install Immich v3.3.5, from the official catalog", "Verified, published by HomersLab", "Plan " + p.ID,
		"photos.ferreira.family", "on NetBird", "Adults: Inês Ferreira", "an OIDC client in Authentik",
		"Every hour: the photo library, its database (a dump)", "A restore test every week",
		"Not the database's own files: the dump holds them", "Not machine-learning models: Immich downloads them again", "study-pc", "4 containers", "None",
		"sealed to study-pc's TPM: the database password", "Memory: needs 6 GB", "Nothing happens until"} {
		if !strings.Contains(words, want) {
			t.Errorf("no %q in\n%s", want, words)
		}
	}
	code, again, _ := run(t, "plan", "immich", "--control-socket", ctl)
	if code != 0 || again != words {
		t.Errorf("homerslab plan immich shows another plan than plan show:\n%s", again)
	}
}

func TestTheCLISaysWhyThereIsNoPlan(t *testing.T) {
	testenv.Isolate(t)
	ctl := running(t)
	if code, _, errOut := run(t, "plan", "immich", "--control-socket", ctl); code != 1 || !strings.Contains(errOut, "first-time setup") {
		t.Errorf("before setup: %d %q", code, errOut)
	}
	if code, _, errOut := run(t, "plan", "nope", "--control-socket", ctl); code != 1 || !strings.Contains(errOut, "no app nope") {
		t.Errorf("an unknown app: %d %q", code, errOut)
	}
	if code, _, errOut := run(t, "plan", "show", "p-0", "--control-socket", ctl); code != 1 || !strings.Contains(errOut, "no plan p-0") {
		t.Errorf("an unknown plan: %d %q", code, errOut)
	}
	if code, _, _ := run(t, "plan"); code != 2 {
		t.Errorf("plan with no app: %d, want 2", code)
	}
}

// Admin answers on this machine's loopback only, until it signs people in over the mesh.
func TestTheManagerRefusesAdminOffLoopback(t *testing.T) {
	testenv.Isolate(t)
	for _, at := range []string{"0.0.0.0:7021", "192.168.1.20:7021", "[::]:7021", "localhost"} {
		code, _, errOut := run(t, "manager", "--admin-listen", at, "--db", filepath.Join(t.TempDir(), "m.db"))
		if code != 1 || !strings.Contains(errOut, "loopback") {
			t.Errorf("--admin-listen %s: %d %q", at, code, errOut)
		}
	}
}
