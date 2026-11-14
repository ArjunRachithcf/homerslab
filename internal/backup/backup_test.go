package backup_test

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/backup"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

const hostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK7mZ7hM0bQ9t9n3cWl2b2Y1xq4m0Jc0m7f3QH9uYq1D"

var targets = map[string]backup.Target{
	"disk": {Kind: backup.Disk, Path: "/srv/backups"},
	"sftp": {Kind: backup.SFTP, Host: "nas.home.arpa", Port: 2222, User: "backup", Path: "/upload", HostKey: hostKey},
	"s3":   {Kind: backup.S3, Host: "s3.example.test", Bucket: "household", Path: "homerslab"},
}

var creds = map[backup.Kind]backup.Credentials{
	backup.SFTP: {SSHKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nc2VjcmV0LXNzaC1rZXk=\n-----END OPENSSH PRIVATE KEY-----\n"},
	backup.S3:   {AccessKeyID: "HLACCESSKEY", SecretAccessKey: "s3-secret-value/42"},
}

// Each node has a repository of its own at the household's target (#90): the same target, a path of the node's own.
func TestEachNodeHasItsOwnRepositoryAtTheTarget(t *testing.T) {
	testenv.Isolate(t)
	want := map[string][2]string{
		"disk": {"/srv/backups/primary", "/srv/backups/pi"},
		"sftp": {"sftp:backup@nas.home.arpa:/upload/primary", "sftp:backup@nas.home.arpa:/upload/pi"},
		"s3":   {"s3:https://s3.example.test/household/homerslab/primary", "s3:https://s3.example.test/household/homerslab/pi"},
	}
	for name, tg := range targets {
		for i, node := range []string{"primary", "pi"} {
			got, err := tg.Location(node)
			if err != nil || got != want[name][i] {
				t.Errorf("%s, node %s: %q %v, want %q", name, node, got, err, want[name][i])
			}
		}
	}
	for _, node := range []string{"", "Primary", "../pi", "pi/x", "-pi"} {
		if _, err := targets["disk"].Location(node); err == nil {
			t.Errorf("node %q has a repository", node)
		}
	}
}

// A target's settings reach restic's command line and files, so only plain values pass: no free text, no relative or
// unclean paths, no plain HTTP, an SFTP host key always pinned.
func TestATargetIsCheckedBeforeItIsUsed(t *testing.T) {
	testenv.Isolate(t)
	bad := map[string]backup.Target{
		"no kind":            {Path: "/srv/backups"},
		"b2, not yet":        {Kind: "b2", Bucket: "household"},
		"relative folder":    {Kind: backup.Disk, Path: "srv/backups"},
		"unclean folder":     {Kind: backup.Disk, Path: "/srv/../etc"},
		"the root":           {Kind: backup.Disk, Path: "/"},
		"a space":            {Kind: backup.Disk, Path: "/srv/my backups"},
		"a newline":          {Kind: backup.Disk, Path: "/srv/x\n-o"},
		"sftp, no host key":  {Kind: backup.SFTP, Host: "nas", User: "backup", Path: "/upload"},
		"sftp, a bad key":    {Kind: backup.SFTP, Host: "nas", User: "backup", Path: "/upload", HostKey: "ssh-ed25519 AAAA x"},
		"sftp, a flag host":  {Kind: backup.SFTP, Host: "-oProxyCommand=x", User: "backup", Path: "/upload", HostKey: hostKey},
		"sftp, a flag user":  {Kind: backup.SFTP, Host: "nas", User: "-x", Path: "/upload", HostKey: hostKey},
		"s3, no bucket":      {Kind: backup.S3, Host: "s3.example.test"},
		"s3, a slash prefix": {Kind: backup.S3, Host: "s3.example.test", Bucket: "household", Path: "/homerslab"},
		"s3, a bad port":     {Kind: backup.S3, Host: "s3.example.test", Port: 70000, Bucket: "household"},
		"s3, a bad CA":       {Kind: backup.S3, Host: "s3.example.test", Bucket: "household", CA: "not a certificate"},
	}
	for name, tg := range bad {
		if err := tg.Check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, tg := range targets {
		if err := tg.Check(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// recorder is a Runner that keeps everything restic would be handed, and answers as restic would.
type recorder struct {
	argv, env [][]string
	answer    func(argv []string) ([]byte, error)
}

func (r *recorder) run(_ context.Context, argv, env []string) ([]byte, error) {
	r.argv, r.env = append(r.argv, argv), append(r.env, env)
	if r.answer != nil {
		return r.answer(argv)
	}
	return nil, nil
}

func (r *recorder) sub(cmd string) [][]string {
	var out [][]string
	for _, a := range r.argv {
		if slices.Contains(a, cmd) {
			out = append(out, a)
		}
	}
	return out
}

// The repository's key and the target's secrets go only into the credentials folder's files: never an argument or an
// environment variable of restic's.
func TestSecretsAreFilesNeverArgumentsOrEnvironment(t *testing.T) {
	testenv.Isolate(t)
	key := []byte("the-repository-key-0123456789abcdef")
	for name, tg := range targets {
		files, err := backup.Files(tg, key, creds[tg.Kind])
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(files[backup.KeyFile], key) {
			t.Errorf("%s: the key file is %q", name, files[backup.KeyFile])
		}
		rec := &recorder{answer: func(argv []string) ([]byte, error) {
			return []byte(`{"message_type":"summary","snapshot_id":"1f2e3d4c"}` + "\n"), nil
		}}
		r := backup.Repo{Restic: "/usr/libexec/homerslab/restic", Target: tg, Node: "primary", Dir: "/run/credentials/hl.service",
			Cache: "/var/lib/homerslab/backup-cache", Run: rec.run}
		if _, err := r.Backup(context.Background(), backup.Item{Tags: []string{"app=immich", "data=server:/data"}, Paths: []string{"/var/lib/docker/volumes/x/_data"}}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		all := strings.Join(slices.Concat(slices.Concat(rec.argv...), slices.Concat(rec.env...)), "\n")
		for _, secret := range []string{string(key), creds[tg.Kind].SecretAccessKey, "c2VjcmV0LXNzaC1rZXk="} {
			if secret != "" && strings.Contains(all, secret) {
				t.Errorf("%s: a secret is on restic's command line or in its environment:\n%s", name, all)
			}
		}
		if !strings.Contains(all, "--password-file\n/run/credentials/hl.service/backup-key") {
			t.Errorf("%s: restic doesn't read its key from the credentials folder:\n%s", name, all)
		}
		for file := range files {
			if strings.ContainsAny(file, "/.") {
				t.Errorf("%s: credential %q isn't a plain name", name, file)
			}
		}
	}
}

// restic's cache is on the app-data disk (#22's review: counted in that disk's use), and every run first clears the
// stale locks a crashed run of HomersLab's left, as restic tells them (the same host, a process that is gone), and no
// other; prune frees space with a bound on what it repacks, so it still works on a nearly full target.
func TestEveryRunClearsStaleLocksAndPruneBoundsItsRepack(t *testing.T) {
	testenv.Isolate(t)
	rec := &recorder{answer: func(argv []string) ([]byte, error) {
		return []byte(`{"message_type":"summary","snapshot_id":"1f2e3d4c"}` + "\n"), nil
	}}
	r := backup.Repo{Restic: "restic", Target: targets["disk"], Node: "primary", Dir: "/creds", Cache: "/var/lib/homerslab/backup-cache", Run: rec.run}
	if _, err := r.Backup(context.Background(), backup.Item{Tags: []string{"app=a", "data=web:/data"}, Paths: []string{"/v"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	var subs []string
	for _, a := range rec.argv {
		if !slices.Contains(a, "--cache-dir") || a[slices.Index(a, "--cache-dir")+1] != "/var/lib/homerslab/backup-cache" {
			t.Errorf("restic's cache isn't on the app-data disk: %q", a)
		}
		for _, s := range []string{"unlock", "backup", "forget"} {
			if slices.Contains(a, s) {
				subs = append(subs, s)
			}
		}
		if slices.Contains(a, "--remove-all") {
			t.Errorf("an unlock removes live locks too: %q", a)
		}
	}
	if !slices.Equal(subs, []string{"unlock", "backup", "unlock", "forget"}) {
		t.Errorf("the runs: %q", subs)
	}
	forget := rec.sub("forget")[0]
	if !slices.Contains(forget, "--prune") || !slices.Contains(forget, "--max-repack-size") {
		t.Errorf("prune: %q", forget)
	}
}

// restic's exit codes say why a run failed: a target without the node's repository, another's lock, a wrong key.
func TestResticsExitCodesAreItsReasons(t *testing.T) {
	testenv.Isolate(t)
	for code, want := range map[int]error{10: backup.ErrNoRepository, 11: backup.ErrLocked, 12: backup.ErrWrongKey} {
		exit := exec.Command("sh", "-c", "exit "+string(rune('0'+code/10))+string(rune('0'+code%10))).Run()
		rec := &recorder{answer: func([]string) ([]byte, error) { return nil, exit }}
		r := backup.Repo{Restic: "restic", Target: targets["disk"], Node: "primary", Dir: "/creds", Run: rec.run}
		if _, err := r.Snapshots(context.Background(), "app=a"); !errors.Is(err, want) {
			t.Errorf("exit %d: %v, want %v", code, err, want)
		}
	}
}

// A dump's command and a backed-up path come from the catalog, so they are checked: plain arguments only, no flag
// where a program is named, absolute clean paths, a file name of plain characters.
func TestDataFromTheCatalogIsChecked(t *testing.T) {
	testenv.Isolate(t)
	for _, p := range []string{"/data", "/var/lib/postgresql/data", "/srv/my_app-1.2"} {
		if !backup.ValidPath(p) {
			t.Errorf("path %q refused", p)
		}
	}
	for _, p := range []string{"", "/", "data", "/data/", "/a/../b", "/a b", "/a\nb", "/a,b", "/$HOME"} {
		if backup.ValidPath(p) {
			t.Errorf("path %q accepted", p)
		}
	}
	for _, c := range [][]string{{"pg_dump", "--clean", "--if-exists", "--username=postgres", "immich"}, {"/usr/bin/mysqldump", "--all-databases"}} {
		if !backup.ValidCommand(c) {
			t.Errorf("command %q refused", c)
		}
	}
	for _, c := range [][]string{nil, {""}, {"-x"}, {"sh", "-c", "pg_dump | gzip"}, {"pg_dump", "a b"}, {"pg_dump", "$(id)"}, {"pg_dump", "x\n"}} {
		if backup.ValidCommand(c) {
			t.Errorf("command %q accepted", c)
		}
	}
	for f, ok := range map[string]bool{"immich.sql": true, "db-1.dump": true, "": false, ".sql": false, "a/b.sql": false, "A b": false} {
		if backup.ValidFile(f) != ok {
			t.Errorf("file %q: %v", f, !ok)
		}
	}
}
