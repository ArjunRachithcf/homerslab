package backup_test

// The backup-target contract suite (spec: Testing Decisions → restic targets): a disk, an SFTP server and an
// S3-compatible service, in containers (testdata/compose.yaml, compose project hltest-22-targets). It needs Docker: it
// runs when HOMERSLAB_TEST_DOCKER_HOST names the engine to use, and is skipped otherwise. The code under test never
// sees that engine (testenv points DOCKER_HOST at nowhere); only this harness does, explicitly.
//
// The suite runs in the world's client, restic's own image at the pinned version: the harness copies this test binary
// (built with CGO_ENABLED=0) into it and runs TestTheBackupTargetContractInside there.

import (
	"bytes"
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

	"github.com/ArjunRachithcf/homerslab/internal/backup"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

const project = "hltest-22-targets"

func container(s string) string { return project + "-" + s + "-1" }

func TestTheBackupTargetContract(t *testing.T) {
	testenv.Isolate(t)
	engine := os.Getenv("HOMERSLAB_TEST_DOCKER_HOST")
	if engine == "" || os.Getenv("HLTEST22_INSIDE") == "1" {
		t.Skip("set HOMERSLAB_TEST_DOCKER_HOST to the engine for the contract suite's containers")
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
	worlds := []struct {
		name   string
		target backup.Target
		creds  backup.Credentials
	}{
		{"disk", backup.Target{Kind: backup.Disk, Path: "/srv/backups"}, backup.Credentials{}},
		{"sftp", backup.Target{Kind: backup.SFTP, Host: "sftp", User: "backup", Path: "/upload", HostKey: os.Getenv("HLTEST22_HOST_KEY")},
			backup.Credentials{SSHKey: string(sshKey)}},
		{"s3", backup.Target{Kind: backup.S3, Host: "s3", Bucket: "household", Path: "homerslab", CA: os.Getenv("HLTEST22_S3_CA")},
			backup.Credentials{AccessKeyID: os.Getenv("HLTEST22_S3_ID"), SecretAccessKey: os.Getenv("HLTEST22_S3_SECRET")}},
	}
	for _, wd := range worlds {
		t.Run(wd.name, func(t *testing.T) {
			testenv.Isolate(t)
			secrets := []string{wd.creds.SecretAccessKey}
			if lines := strings.Split(strings.TrimSpace(wd.creds.SSHKey), "\n"); len(lines) > 2 {
				secrets = append(secrets, lines[1]) // the key's first line of base64
			}
			contract(t, wd.target, wd.creds, secrets)
		})
	}
}

// seen is everything restic was handed and wrote, across a target's runs.
type seen struct {
	mu  sync.Mutex
	all bytes.Buffer
}

// runner runs restic as the agent's unit would: argv and env alone, nothing else of this process's environment.
func (s *seen) runner(ctx context.Context, argv, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
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

func contract(t *testing.T, target backup.Target, creds backup.Credentials, secrets []string) {
	ctx := context.Background()
	s := &seen{}
	primaryKey, piKey := newKey(), newKey()
	primary := repo(t, s, target, creds, "primary", primaryKey)
	var err error
	for range 10 { // the servers may still be starting
		if err = primary.Init(ctx); err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatalf("making the primary's repository: %v", err)
	}

	t.Run("each node has its own repository, which only its own key opens", func(t *testing.T) {
		if err := primary.Init(ctx); err != nil {
			t.Fatalf("making it again: %v", err)
		}
		pi := repo(t, s, target, creds, "pi", piKey)
		if err := pi.Init(ctx); err != nil {
			t.Fatal(err)
		}
		withPrimarysKey := repo(t, s, target, creds, "pi", primaryKey)
		if _, err := withPrimarysKey.Snapshots(ctx); !errors.Is(err, backup.ErrWrongKey) {
			t.Errorf("the primary's key on the Pi's repository: %v", err)
		}
		if err := withPrimarysKey.Init(ctx); !errors.Is(err, backup.ErrWrongKey) {
			t.Errorf("making the Pi's repository again with another key: %v", err)
		}
	})

	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "photo.jpg"), []byte("the photo"), 0o600); err != nil {
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
		if b, err := backup.Raw(ctx, primary, "dump", db, "/immich.sql"); err != nil || string(b) != "the-dump" {
			t.Errorf("the dump restored: %q %v", b, err)
		}
		pi := repo(t, s, target, creds, "pi", piKey)
		if theirs, err := pi.Snapshots(ctx); err != nil || len(theirs) != 0 {
			t.Errorf("the Pi's repository holds %+v %v", theirs, err)
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
		if err := primary.Prune(ctx); err != nil {
			t.Fatalf("prune after a crashed run: %v", err)
		}
	})

	t.Run("a live run's lock stays", func(t *testing.T) {
		done := make(chan error, 1)
		go func() { _, err := primary.Backup(ctx, slow); done <- err }()
		held(t)
		if err := primary.Prune(ctx); !errors.Is(err, backup.ErrLocked) {
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
		if err := primary.Prune(ctx); err != nil {
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
	w.s3Cert, w.s3Key = selfSigned(t, "s3")
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

// selfSigned is a certificate for name that is its own authority, and its key, as PEM.
func selfSigned(t *testing.T, name string) (string, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
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
