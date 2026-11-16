package backup_test

// The backup-target contract suite (spec: Testing Decisions → restic targets): a disk, an SFTP server, an
// S3-compatible service, and an object-locked one that also stands in for B2's S3-compatible API, in containers
// (testdata/compose.yaml, compose project hltest-22-targets, or HLTEST22_PROJECT's). It needs Docker: it
// runs when HOMEWARD_TEST_DOCKER_HOST names the engine to use, and is skipped otherwise. The code under test never
// sees that engine (testenv points DOCKER_HOST at nowhere); only this harness does, explicitly.
//
// The suite runs in the world's client, restic's own image at the pinned version: the harness copies this test binary
// (built with CGO_ENABLED=0) into it and runs TestTheBackupTargetContractInside there.

import (
	"bytes"
	"cmp"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/ArjunRachithcf/homeward/internal/backup"
	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

var project = cmp.Or(os.Getenv("HLTEST22_PROJECT"), "hltest-22-targets")

func container(s string) string { return project + "-" + s + "-1" }

func TestTheBackupTargetContract(t *testing.T) {
	testenv.Isolate(t)
	engine := os.Getenv("HOMEWARD_TEST_DOCKER_HOST")
	if engine == "" || os.Getenv("HLTEST22_INSIDE") == "1" {
		t.Skip("set HOMEWARD_TEST_DOCKER_HOST to the engine for the contract suite's containers")
	}
	w := up(t, engine)
	// The client's SSH key is made in the client; the server's one authorized key is its public half.
	w.docker(t, nil, "exec", container("client"), "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", "/root/hl-ssh")
	pub := w.docker(t, nil, "exec", container("client"), "cat", "/root/hl-ssh.pub")
	w.docker(t, strings.NewReader(pub), "exec", "-i", container("sftp"), "sh", "-c",
		"install -d -o 1001 -g 1001 -m 0700 /home/backup/.ssh && cat > /home/backup/.ssh/authorized_keys && "+
			"chown 1001:1001 /home/backup/.ssh/authorized_keys && chmod 0600 /home/backup/.ssh/authorized_keys")
	// The household made its bucket at the service before choosing it as the target.
	w.docker(t, nil, "exec", container("s3"), "mkdir", "-p", "/data/household")
	var hostKey string
	for range 50 { // the server makes its host keys as it starts
		if out, err := w.dockerOut(nil, "exec", container("sftp"), "cat", "/etc/ssh/ssh_host_ed25519_key.pub"); err == nil {
			hostKey = strings.Join(strings.Fields(out)[:2], " ")
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	w.docker(t, nil, "cp", self, container("client")+":/hl.test")
	// rclone, to read an object-locked bucket as it was, as a household restores by hand (static, from its image).
	rc := filepath.Join(t.TempDir(), "rclone")
	w.docker(t, nil, "cp", container("s3")+":/usr/local/bin/rclone", rc)
	w.docker(t, nil, "cp", rc, container("client")+":/usr/local/bin/rclone")
	out, err := w.dockerOut(nil, "exec", "-e", "HLTEST22_INSIDE=1", "-e", "HLTEST22_HOST_KEY="+hostKey, "-e", "HLTEST22_S3_ID="+w.s3ID,
		"-e", "HLTEST22_S3_SECRET="+w.s3Secret, "-e", "HLTEST22_S3_CA="+w.s3Cert, container("client"),
		"/hl.test", "-test.run", "^TestTheBackupTargetContractInside$", "-test.v", "-test.count=1")
	t.Logf("in the client:\n%s", out)
	if err != nil {
		if strings.Contains(out, "not found") || strings.Contains(out, "no such file") {
			t.Fatalf("the test binary didn't run in the client (build it with CGO_ENABLED=0): %v", err)
		}
		t.Fatalf("the contract in the client: %v", err)
	}
	if !strings.Contains(out, "--- PASS: TestTheBackupTargetContractInside") {
		t.Fatal("the contract didn't run in the client")
	}
}

// TestTheBackupTargetContractInside is the suite itself, in the world's client.
func TestTheBackupTargetContractInside(t *testing.T) {
	testenv.Isolate(t)
	if os.Getenv("HLTEST22_INSIDE") != "1" {
		t.Skip("runs inside the contract suite's client container (TestTheBackupTargetContract)")
	}
	if out, err := exec.Command("restic", "version").Output(); err != nil || !strings.HasPrefix(string(out), "restic "+backup.ResticVersion+" ") {
		t.Fatalf("the client's restic: %q %v, want %s", out, err, backup.ResticVersion)
	}
	sshKey, err := os.ReadFile("/root/hl-ssh")
	if err != nil {
		t.Fatal(err)
	}
	s3 := backup.Credentials{AccessKeyID: os.Getenv("HLTEST22_S3_ID"), SecretAccessKey: os.Getenv("HLTEST22_S3_SECRET")}
	ca := os.Getenv("HLTEST22_S3_CA")
	// B2's S3-compatible API, at the region's endpoint, is the locked service here.
	t.Cleanup(backup.B2At("locked"))
	locked := backup.Target{Kind: backup.S3, Host: "locked", Bucket: "locked", Path: "homerslab", CA: ca, Region: "eu-central-003"}
	b2 := backup.Target{Kind: backup.B2, Region: "eu-central-003", Bucket: "b2-household", Path: "homerslab", CA: ca}
	// The household made each bucket with Object Lock, in compliance mode for a day, before choosing it.
	for _, b := range []string{locked.Bucket, b2.Bucket} {
		lockedBucket(t, locked, s3, b, 1)
	}
	worlds := []struct {
		name   string
		target backup.Target
		creds  backup.Credentials
		// bucket is the object-locked bucket's rclone path, where it is one.
		bucket string
	}{
		{"disk", backup.Target{Kind: backup.Disk, Path: "/srv/backups"}, backup.Credentials{}, ""},
		{"sftp", backup.Target{Kind: backup.SFTP, Host: "sftp", User: "backup", Path: "/upload", HostKey: os.Getenv("HLTEST22_HOST_KEY")},
			backup.Credentials{SSHKey: string(sshKey)}, ""},
		{"s3", backup.Target{Kind: backup.S3, Host: "s3", Bucket: "household", Path: "homerslab", CA: ca}, s3, ""},
		{"s3-locked", locked, s3, "locked:locked/homerslab"},
		{"b2", b2, s3, "locked:b2-household/homerslab"},
	}
	for _, wd := range worlds {
		t.Run(wd.name, func(t *testing.T) {
			testenv.Isolate(t)
			secrets := []string{wd.creds.SecretAccessKey}
			if lines := strings.Split(strings.TrimSpace(wd.creds.SSHKey), "\n"); len(lines) > 2 {
				secrets = append(secrets, lines[1]) // the key's first line of base64
			}
			contract(t, wd.target, wd.creds, secrets, wd.bucket)
		})
	}
	t.Run("s3-locked: an object older than the bucket's Days is still locked after the lock run, and deletable without it", func(t *testing.T) {
		olderThanDays(t, locked, s3)
	})
}

// lockedBucket makes bucket b at the locked service, with Object Lock, and then, unless days is 0, a default retention
// in compliance mode for days.
func lockedBucket(t *testing.T, at backup.Target, c backup.Credentials, b string, days uint) {
	t.Helper()
	cl, done, err := backup.BucketClient(at, c)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	ctx := context.Background()
	for i := 0; ; i++ { // the service may still be starting
		err := cl.MakeBucket(ctx, b, minio.MakeBucketOptions{Region: at.Region, ObjectLocking: true})
		if err == nil {
			break
		}
		if i == 20 {
			t.Fatalf("making bucket %s: %v", b, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if days == 0 {
		return
	}
	mode, unit := minio.Compliance, minio.Days
	if err := cl.SetObjectLockConfig(ctx, b, &mode, &days, &unit); err != nil {
		t.Fatalf("locking bucket %s: %v", b, err)
	}
}

// olderThanDays: two nodes' repositories whose objects were written long ago, their locks about to run out (each locked
// for seconds here, standing in for Days gone by). The lock run locks one node's again; once the others' locks have run
// out, a thief deletes every version of that node's repository, and none of the locked one's.
func olderThanDays(t *testing.T, at backup.Target, c backup.Credentials) {
	ctx := context.Background()
	at.Bucket = "aged"
	lockedBucket(t, at, c, at.Bucket, 0)
	cl, done, err := backup.BucketClient(at, c)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	s := &seen{}
	photos := t.TempDir()
	if err := os.WriteFile(filepath.Join(photos, "photo.jpg"), []byte("an old photo"), 0o600); err != nil {
		t.Fatal(err)
	}
	versions := func(node string) []minio.ObjectInfo {
		var out []minio.ObjectInfo
		for o := range cl.ListObjects(ctx, at.Bucket, minio.ListObjectsOptions{Prefix: "homerslab/" + node + "/", Recursive: true, WithVersions: true}) {
			if o.Err != nil {
				t.Fatal(o.Err)
			}
			out = append(out, o)
		}
		return out
	}
	var soon time.Time
	mode := minio.Compliance
	repos := map[string]backup.Repo{}
	for _, node := range []string{"kept", "lapsed"} {
		r := repo(t, s, at, c, node, newKey())
		if err := ensure(ctx, r); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Backup(ctx, backup.Item{Tags: []string{"app=old", "data=server:/photos"}, Paths: []string{photos}}); err != nil {
			t.Fatal(err)
		}
		repos[node] = r
		soon = time.Now().Add(5 * time.Second).UTC()
		for _, o := range versions(node) {
			if o.IsDeleteMarker { // a run's lock, removed: nothing to lock
				continue
			}
			if err := cl.PutObjectRetention(ctx, at.Bucket, o.Key, minio.PutObjectRetentionOptions{Mode: &mode, RetainUntilDate: &soon, VersionID: o.VersionID}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The household's default retention, a day, came after: it never reaches objects written before.
	days, unit := uint(1), minio.Days
	if err := cl.SetObjectLockConfig(ctx, at.Bucket, &mode, &days, &unit); err != nil {
		t.Fatal(err)
	}
	before, err := backup.Lock(ctx, at, c, "kept", backup.Immutability{Why: "compliance", Days: 1, LockedUntil: soon}, time.Now().UTC())
	if err != nil || !before.Immutable {
		t.Fatalf("the lock run: %+v %v", before, err)
	}
	if !before.LockedUntil.After(time.Now().Add(23 * time.Hour)) {
		t.Errorf("the lock run locked until %s, not a day on", before.LockedUntil)
	}
	time.Sleep(time.Until(soon) + time.Second)
	for node, want := range map[string]bool{"kept": false, "lapsed": true} {
		deleted := 0
		var all []minio.ObjectInfo
		for _, o := range versions(node) {
			// The repository as restic sees it: what a run's own lock was (already removed, hidden) isn't, and is
			// never locked again; removing a delete marker only shows what it hid.
			if o.IsLatest && !o.IsDeleteMarker {
				all = append(all, o)
			}
		}
		for _, o := range all {
			if cl.RemoveObject(ctx, at.Bucket, o.Key, minio.RemoveObjectOptions{VersionID: o.VersionID}) == nil {
				deleted++
			}
		}
		if want && deleted != len(all) || !want && deleted != 0 {
			t.Errorf("%s: the thief deleted %d of %d versions", node, deleted, len(all))
		}
	}
	if snaps, err := repos["kept"].Snapshots(ctx); err != nil || len(snaps) != 1 {
		t.Errorf("the locked repository after the thief: %d snapshots, %v", len(snaps), err)
	}
	if _, err := repos["lapsed"].Snapshots(ctx); err == nil {
		t.Error("the unlocked repository still opens: nothing was deleted")
	}
}

// rclone is rclone in the client, with the object-locked service as its remote "locked", configured in its
// environment.
func rclone(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	ca := filepath.Join(os.TempDir(), "hl-locked-ca.pem")
	if err := os.WriteFile(ca, []byte(os.Getenv("HLTEST22_S3_CA")), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("rclone", append([]string{"--config", "/dev/null", "--ca-cert", ca, "-q"}, args...)...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + os.TempDir(), "RCLONE_CONFIG_LOCKED_TYPE=s3",
		"RCLONE_CONFIG_LOCKED_PROVIDER=Other", "RCLONE_CONFIG_LOCKED_REGION=eu-central-003", "RCLONE_CONFIG_LOCKED_LOCATION_CONSTRAINT=eu-central-003", "RCLONE_CONFIG_LOCKED_ENDPOINT=https://locked",
		"RCLONE_CONFIG_LOCKED_ACCESS_KEY_ID=" + os.Getenv("HLTEST22_S3_ID"), "RCLONE_CONFIG_LOCKED_SECRET_ACCESS_KEY=" + os.Getenv("HLTEST22_S3_SECRET")}
	return cmd
}

// seen is everything restic was handed and wrote, across a target's runs.
type seen struct {
	mu  sync.Mutex
	all bytes.Buffer
}

// runner runs restic as the agent's unit would: argv and env alone, nothing else of this process's environment.
func (s *seen) runner(ctx context.Context, argv, env []string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Env = append([]string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.all.WriteString(strings.Join(argv, " ") + "\n" + strings.Join(env, " ") + "\n" + stdout.String() + stderr.String())
	if err != nil {
		return stdout.Bytes(), &runError{err: err, stderr: strings.TrimSpace(stderr.String())}
	}
	return stdout.Bytes(), nil
}

type runError struct {
	err    error
	stderr string
}

func (e *runError) Error() string { return e.err.Error() + ": " + e.stderr }
func (e *runError) Unwrap() error { return e.err }

func newKey() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return []byte(hex.EncodeToString(b))
}

// ensure makes the node's repository unless the target has it, as the agent does.
func ensure(ctx context.Context, r backup.Repo) error {
	there, err := r.Exists(ctx)
	if err == nil && !there {
		err = r.Init(ctx)
	}
	return err
}

// repo is the node's repository at the target, its credentials folder written as systemd would write the unit's.
func repo(t *testing.T, s *seen, target backup.Target, creds backup.Credentials, node string, key []byte) backup.Repo {
	t.Helper()
	files, err := backup.Files(target, key, creds)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	return backup.Repo{Restic: "restic", Target: target, Node: node, Dir: dir, Cache: filepath.Join(t.TempDir(), "cache"), Run: s.runner}
}

func contract(t *testing.T, target backup.Target, creds backup.Credentials, secrets []string, bucket string) {
	ctx := context.Background()
	s := &seen{}
	primaryKey, piKey := newKey(), newKey()
	primary := repo(t, s, target, creds, "primary", primaryKey)
	var err error
	for range 10 { // the servers may still be starting
		if err = ensure(ctx, primary); err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatalf("making the primary's repository: %v", err)
	}

	t.Run("each node has its own repository, which only its own key opens", func(t *testing.T) {
		if err := ensure(ctx, primary); err != nil {
			t.Fatalf("making it again: %v", err)
		}
		pi := repo(t, s, target, creds, "pi", piKey)
		if err := ensure(ctx, pi); err != nil {
			t.Fatal(err)
		}
		withPrimarysKey := repo(t, s, target, creds, "pi", primaryKey)
		if _, err := withPrimarysKey.Snapshots(ctx); !errors.Is(err, backup.ErrWrongKey) {
			t.Errorf("the primary's key on the Pi's repository: %v", err)
		}
		if err := ensure(ctx, withPrimarysKey); !errors.Is(err, backup.ErrWrongKey) {
			t.Errorf("making the Pi's repository again with another key: %v", err)
		}
	})

	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "photo.jpg"), []byte("the photo"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The app's own link, to the machine's /etc: restic, as root, must store it as a link and never follow it.
	if err := os.Symlink("/etc", filepath.Join(data, "etc")); err != nil {
		t.Fatal(err)
	}
	library := backup.Item{Tags: []string{"app=immich", "data=server:/data"}, Paths: []string{data}}
	dump := backup.Item{Tags: []string{"app=immich", "data=database:immich.sql"}, Command: []string{"printf", "the-dump"}, File: "immich.sql"}

	t.Run("it stores folders and dumps, lists them by app, and they restore by hand", func(t *testing.T) {
		lib, err := primary.Backup(ctx, library)
		if err != nil {
			t.Fatal(err)
		}
		db, err := primary.Backup(ctx, dump)
		if err != nil {
			t.Fatal(err)
		}
		all, err := primary.Snapshots(ctx, "app=immich")
		if err != nil || len(all) != 2 {
			t.Fatalf("the app's snapshots: %+v %v", all, err)
		}
		one, err := primary.Snapshots(ctx, "app=immich", "data=database:immich.sql")
		if err != nil || len(one) != 1 || one[0].ID != db || !slices.Equal(one[0].Paths, []string{"/immich.sql"}) || one[0].Host != "primary" {
			t.Fatalf("the dump's snapshots: %+v %v", one, err)
		}
		into := t.TempDir()
		if _, err := backup.Raw(ctx, primary, "restore", lib, "--target", into); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(filepath.Join(into, data, "photo.jpg")); err != nil || string(b) != "the photo" {
			t.Errorf("the library restored: %q %v", b, err)
		}
		if fi, err := os.Lstat(filepath.Join(into, data, "etc")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the app's link restored as %v %v, not a link", fi, err)
		}
		if ls, err := backup.Raw(ctx, primary, "ls", lib); err != nil || strings.Contains(string(ls), "passwd") {
			t.Errorf("the snapshot holds what the link points at: %v", err)
		}
		if b, err := backup.Raw(ctx, primary, "dump", db, "/immich.sql"); err != nil || string(b) != "the-dump" {
			t.Errorf("the dump restored: %q %v", b, err)
		}
		// The manager's database, as the manager hands it over.
		state := backup.Item{Tags: []string{"platform=manager", "data=manager:manager.db"}, Stdin: []byte("the manager's state"), File: "manager.db"}
		mid, err := primary.Backup(ctx, state)
		if err != nil {
			t.Fatal(err)
		}
		if b, err := backup.Raw(ctx, primary, "dump", mid, "/manager.db"); err != nil || string(b) != "the manager's state" {
			t.Errorf("the manager's database restored: %q %v", b, err)
		}
		pi := repo(t, s, target, creds, "pi", piKey)
		if theirs, err := pi.Snapshots(ctx); err != nil || len(theirs) != 0 {
			t.Errorf("the Pi's repository holds %+v %v", theirs, err)
		}
	})

	t.Run("a restore makes the folder what the snapshot held, and never writes through a link or out of it", func(t *testing.T) {
		vol := filepath.Join(t.TempDir(), "_data")
		outside := t.TempDir()
		write := func(name, body string) {
			t.Helper()
			if err := os.MkdirAll(filepath.Dir(filepath.Join(vol, name)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(vol, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		write("photo.jpg", "the photo")
		write("albums/shadow", "an album")
		if err := os.Symlink("/etc", filepath.Join(vol, "etc")); err != nil {
			t.Fatal(err)
		}
		// A run's items carry the run's time, to the second, as restic keeps it: one point to restore the app to.
		run := time.Date(2026, 11, 13, 9, 0, 7, 0, time.UTC)
		item := backup.Item{Time: run, Tags: []string{"app=restored", "data=server:/library"}, Paths: []string{vol}}
		snap, err := primary.Backup(ctx, item)
		if err != nil {
			t.Fatal(err)
		}
		// Since then the app edited a photo, added one, and (compromised) made its albums folder a link out of the
		// volume, hoping a restore, as root, writes the album through it.
		write("photo.jpg", "edited")
		write("new.jpg", "new")
		if err := os.RemoveAll(filepath.Join(vol, "albums")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(vol, "albums")); err != nil {
			t.Fatal(err)
		}
		if err := primary.Restore(ctx, snap, vol, vol); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(filepath.Join(vol, "photo.jpg")); string(b) != "the photo" {
			t.Errorf("the photo after the restore: %q", b)
		}
		if _, err := os.Lstat(filepath.Join(vol, "new.jpg")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a file the snapshot doesn't hold is still there: %v", err)
		}
		if fi, err := os.Lstat(filepath.Join(vol, "albums")); err != nil || !fi.IsDir() {
			t.Errorf("the albums folder after the restore: %v %v, want a folder again", fi, err)
		}
		if left, _ := os.ReadDir(outside); len(left) != 0 {
			t.Errorf("the restore wrote through the app's link, outside the volume: %v", left)
		}
		if fi, err := os.Lstat(filepath.Join(vol, "etc")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the snapshot's link restored as %v %v, not a link", fi, err)
		}
		if _, err := os.Lstat("/etc/photo.jpg"); !errors.Is(err, os.ErrNotExist) {
			t.Error("the restore followed the snapshot's link into /etc")
		}
		// (A file made from a command has no SELinux label to put back; a volume's files keep theirs.)
		// A file whose name climbs out (`..`) is one restic never stores as such: it lands inside the folder restored
		// into, and a restore of anything but a plain folder is never run.
		if _, err := backup.Raw(ctx, primary, "backup", "--host", "primary", "--stdin-from-command", "--stdin-filename", "../../climbed/evil", "--", "printf", "x"); err != nil {
			t.Fatal(err)
		}
		climbed, err := primary.Snapshots(ctx)
		if err != nil || len(climbed) == 0 {
			t.Fatalf("the snapshots: %v", err)
		}
		last := climbed[len(climbed)-1]
		base := t.TempDir()
		into := filepath.Join(base, "a", "b")
		if err := os.MkdirAll(into, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := backup.Raw(ctx, primary, "restore", "--target", into, "--exclude-xattr", "security.*", "--", last.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(base, "climbed")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a `..` name left the folder restored into: %v", err)
		}
		if b, _ := os.ReadFile(filepath.Join(into, "climbed", "evil")); string(b) != "x" || !slices.Equal(last.Paths, []string{"/climbed/evil"}) {
			t.Errorf("the climbing name was stored as %q, restored as %q", last.Paths, b)
		}
		if err := primary.Restore(ctx, last.ID, "/climbed/..", into); err == nil {
			t.Error("a restore of a path with `..` was run")
		}
		if _, err := backup.Raw(ctx, primary, "forget", last.ID); err != nil {
			t.Fatal(err)
		}
		kept, err := primary.Snapshots(ctx, "app=restored")
		if err != nil || len(kept) != 1 || kept[0].Size() != uint64(len("the photo")+len("an album")) || !kept[0].Time.Equal(run) {
			t.Errorf("the snapshot: %+v %v, want its size and the run's time", kept, err)
		}
	})

	t.Run("a dump whose command fails saves nothing", func(t *testing.T) {
		failing := backup.Item{Tags: []string{"app=immich", "data=database:broken.sql"}, Command: []string{"false"}, File: "broken.sql"}
		if _, err := primary.Backup(ctx, failing); err == nil {
			t.Error("a failed dump was backed up")
		}
		if got, err := primary.Snapshots(ctx, "data=database:broken.sql"); err != nil || len(got) != 0 {
			t.Errorf("a failed dump's snapshots: %+v %v", got, err)
		}
		empty := backup.Item{Tags: []string{"app=immich", "data=database:empty.sql"}, Command: []string{"true"}, File: "empty.sql"}
		if _, err := primary.Backup(ctx, empty); !errors.Is(err, backup.ErrIncomplete) {
			t.Errorf("a dump that wrote nothing: %v", err)
		}
	})

	held := func(t *testing.T) {
		t.Helper()
		for range 100 {
			if out, err := backup.Raw(ctx, primary, "list", "locks", "--no-lock"); err == nil && len(bytes.TrimSpace(out)) > 0 {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("the run never locked the repository")
	}
	// A dump that takes a few seconds, as a database's does.
	slow := backup.Item{Tags: []string{"app=slow", "data=db:slow.sql"}, Command: []string{"top", "-b", "-n", "5", "-d", "1"}, File: "slow.sql"}

	t.Run("a crashed run's lock is cleared by the next run", func(t *testing.T) {
		crash, kill := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { _, err := primary.Backup(crash, slow); done <- err }()
		held(t)
		kill() // restic is killed, as a crash or a power cut would: its lock stays
		<-done
		if _, err := backup.Raw(ctx, primary, "prune"); !errors.Is(err, backup.ErrLocked) {
			t.Fatalf("prune past the crashed run's lock, without clearing it: %v", err)
		}
		if err := primary.Unlock(ctx); err != nil {
			t.Fatal(err)
		}
		if err := primary.Prune(ctx, backup.DefaultRetention); err != nil {
			t.Fatalf("prune after a crashed run: %v", err)
		}
	})

	t.Run("a live run's lock stays", func(t *testing.T) {
		done := make(chan error, 1)
		go func() { _, err := primary.Backup(ctx, slow); done <- err }()
		held(t)
		if err := primary.Unlock(ctx); err != nil {
			t.Fatal(err)
		}
		if err := primary.Prune(ctx, backup.DefaultRetention); !errors.Is(err, backup.ErrLocked) {
			t.Errorf("prune during a live run: %v", err)
		}
		if err := <-done; err != nil {
			t.Errorf("the live run: %v", err)
		}
	})

	t.Run("prune keeps the schedule and frees what it no longer keeps", func(t *testing.T) {
		before, err := primary.Snapshots(ctx, "data=server:/data")
		if err != nil || len(before) != 1 {
			t.Fatalf("before: %+v %v", before, err)
		}
		var ids []string
		for _, body := range []string{"the photo, edited", "the photo, edited again"} {
			if err := os.WriteFile(filepath.Join(data, "photo.jpg"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			id, err := primary.Backup(ctx, library)
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		if err := primary.Prune(ctx, backup.DefaultRetention); err != nil {
			t.Fatal(err)
		}
		// Within one hour, the schedule keeps the hour's latest snapshot, and the oldest while it has fewer hours than
		// it keeps (restic's own rule); the one between goes.
		got, err := primary.Snapshots(ctx, "data=server:/data")
		if err != nil || len(got) != 2 || got[0].ID != before[0].ID || got[1].ID != ids[1] {
			t.Errorf("after prune: %+v %v, want %s and %s", got, err, before[0].ID, ids[1])
		}
		if _, err := backup.Raw(ctx, primary, "check"); err != nil {
			t.Errorf("the repository after prune: %v", err)
		}
	})

	t.Run("it says plainly whether whoever holds its credentials can delete its backups", func(t *testing.T) {
		now := time.Now().UTC()
		im, _ := backup.Lock(ctx, target, creds, "primary", backup.Immutability{}, now)
		switch {
		case bucket != "" && (!im.Immutable || im.Why != "compliance" || im.Days != 1 || !im.LockedUntil.Equal(now.Add(24*time.Hour))):
			t.Errorf("an object-locked bucket: %+v", im)
		case bucket == "" && im.Immutable:
			t.Errorf("a target without Object Lock is immutable: %+v", im)
		case target.Kind == backup.Disk && im.Why != "disk", target.Kind == backup.SFTP && im.Why != "sftp",
			target.Kind == backup.S3 && bucket == "" && im.Why != "no-object-lock" && im.Why != "unknown":
			t.Errorf("%s: %+v", target.Kind, im)
		}
	})

	if bucket != "" {
		t.Run("a box taken over can't delete what it wrote: it is only hidden, and comes back by hand", func(t *testing.T) {
			photos := t.TempDir()
			if err := os.WriteFile(filepath.Join(photos, "photo.jpg"), []byte("the family's photo"), 0o600); err != nil {
				t.Fatal(err)
			}
			id, err := primary.Backup(ctx, backup.Item{Tags: []string{"app=kept", "data=server:/photos"}, Paths: []string{photos}})
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(1100 * time.Millisecond) // the service keeps times to the second
			before := time.Now().UTC()
			time.Sleep(1100 * time.Millisecond)
			// The thief, with the node's own key and credentials: every snapshot forgotten and pruned, then every version
			// of every object deleted, as far as the service lets it.
			all, err := primary.Snapshots(ctx)
			if err != nil || len(all) == 0 {
				t.Fatalf("the snapshots: %v", err)
			}
			args := []string{"forget", "--prune"}
			for _, s := range all {
				args = append(args, s.ID)
			}
			if _, err := backup.Raw(ctx, primary, args...); err != nil {
				t.Fatalf("the thief's prune: %v", err)
			}
			if out, err := rclone(t, "delete", "--s3-versions", bucket).CombinedOutput(); err == nil {
				t.Errorf("the service deleted locked versions: %s", out)
			}
			if out, err := rclone(t, "delete", "--s3-versions", "--s3-bypass-governance-retention", bucket).CombinedOutput(); err == nil {
				t.Errorf("the service deleted versions locked in compliance mode, bypassing governance: %s", out)
			}
			if left, err := primary.Snapshots(ctx); err != nil || len(left) != 0 {
				t.Fatalf("after the thief, restic sees %d snapshots, %v", len(left), err)
			}
			// Back by hand: the bucket as it was before, served to restic read-only.
			serve := rclone(t, "serve", "restic", "--addr", "127.0.0.1:8099", "--s3-version-at", before.Format(time.RFC3339), bucket)
			if err := serve.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = serve.Process.Kill(); _ = serve.Wait() }()
			var b []byte
			for range 50 {
				b, err = exec.Command("restic", "-r", "rest:http://127.0.0.1:8099/"+"primary", "--password-file", filepath.Join(primary.Dir, backup.KeyFile),
					"--no-lock", "--no-cache", "-q", "dump", id, filepath.Join(photos, "photo.jpg")).Output()
				if err == nil {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			if err != nil || string(b) != "the family's photo" {
				t.Errorf("the photo, back by hand from the bucket as it was: %q %v", b, err)
			}
		})
	}

	t.Run("no secret is ever on a command line, in an environment, or in restic's output", func(t *testing.T) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, secret := range append(secrets, string(primaryKey), string(piKey)) {
			if secret != "" && strings.Contains(s.all.String(), secret) {
				t.Error("a secret was handed to restic outside its credentials folder, or written by it")
			}
		}
	})
}

type world struct {
	engine, dir                   string
	s3ID, s3Secret, s3Cert, s3Key string
}

func up(t *testing.T, engine string) *world {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	w := &world{engine: engine, dir: filepath.Join(filepath.Dir(self), "testdata"), s3ID: "hltest" + string(newKey()[:12]), s3Secret: string(newKey())}
	w.s3Cert, w.s3Key = selfSigned(t, "s3", "locked")
	w.compose(t, "down", "-v", "--remove-orphans")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := w.dockerOut(nil, "compose", "-p", project, "-f", "compose.yaml", "logs", "--no-color", "--tail", "80")
			t.Logf("the containers' logs:\n%s", out)
		}
		w.compose(t, "down", "-v", "--remove-orphans")
	})
	w.compose(t, "up", "-d", "--quiet-pull", "--wait")
	return w
}

// selfSigned is a certificate for names that is its own authority, and its key, as PEM.
func selfSigned(t *testing.T, names ...string) (string, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}))
}

func (w *world) cmd(stdin io.Reader, args ...string) *exec.Cmd {
	cmd := exec.Command("docker", args...)
	cmd.Env = append(os.Environ(), "DOCKER_HOST="+w.engine, "HLTEST22_S3_ID="+w.s3ID, "HLTEST22_S3_SECRET="+w.s3Secret,
		"HLTEST22_S3_CERT="+w.s3Cert, "HLTEST22_S3_KEY="+w.s3Key)
	cmd.Dir = w.dir
	cmd.Stdin = stdin
	return cmd
}

func (w *world) dockerOut(stdin io.Reader, args ...string) (string, error) {
	out, err := w.cmd(stdin, args...).CombinedOutput()
	return string(out), err
}

func (w *world) docker(t *testing.T, stdin io.Reader, args ...string) string {
	t.Helper()
	cmd := w.cmd(stdin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", args[0], err, stderr.String())
	}
	return string(out)
}

func (w *world) compose(t *testing.T, args ...string) {
	t.Helper()
	if out, err := w.dockerOut(nil, append([]string{"compose", "-p", project, "-f", "compose.yaml"}, args...)...); err != nil {
		t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
