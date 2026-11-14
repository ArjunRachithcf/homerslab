package cli_test

import (
	"bytes"
	"encoding/json"
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

// The CLI makes a plan and shows it by its id, the one object Admin shows too (the manager's tests compare the two).
func TestTheCLIShowsAPlanByItsID(t *testing.T) {
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
	if _, byID, _ := run(t, "plan", "show", p.ID, "--json", "--control-socket", ctl); !bytes.Equal(bytes.TrimSpace([]byte(byID)), bytes.TrimSpace([]byte(out))) {
		t.Errorf("plan show %s --json:\n%s\nwant\n%s", p.ID, byID, out)
	}

	code, words, errOut := run(t, "plan", "show", p.ID, "--control-socket", ctl)
	if code != 0 {
		t.Fatalf("homerslab plan show: %d %s", code, errOut)
	}
	for _, want := range []string{"Install Immich v3.3.5, from the official catalog", "Verified, published by HomersLab", "Plan " + p.ID,
		"photos.ferreira.family", "on NetBird", "Adults: \u2068Inês Ferreira\u2069", "an OIDC client in Authentik",
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
