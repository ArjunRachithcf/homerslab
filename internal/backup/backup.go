// Package backup is the backup-target adapter (spec: Adapter contracts → Backup target; ADR 0010, amended for #90):
// restic, on a disk, over SFTP, or on an S3-compatible service. Each node has a repository of its own at the
// household's target (Target.Location), opened by a key of its own, so a stolen node opens only its own backups.
//
// restic reads every secret from a file in a credentials folder (Repo.Dir: the $CREDENTIALS_DIRECTORY systemd gives
// the unit it runs in, decrypted from the agent's sealed credentials): the repository's key, the S3 keys, the SFTP key.
// None of them is ever one of its arguments or environment variables, or in what it writes.
package backup

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The restic the agent runs, pinned: the release's Linux binaries by the SHA-256 in its SHA256SUMS, whose signature
// is the restic release key's (CF8F 18F2 8445 7597 3F79 D4E1 91A6 868B D3F7 A907), checked when the pin was set. The
// package installs it as Restic (#48); the contract suite runs restic's own image of the same version.
const (
	ResticVersion = "0.19.1"
	Restic        = "/usr/libexec/homerslab/restic"
)

// ResticSHA256 are the release's compressed binaries' digests (restic_<version>_linux_<arch>.bz2), by architecture as
// node facts name it.
var ResticSHA256 = map[string]string{
	"x86_64": "f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c",
	"arm64":  "a5f64aaab53d51e311fa3829124c5b703f2d14cf187d8640b6be3b2b49376465",
}

// Kind is what a target is.
type Kind string

const (
	Disk Kind = "disk"
	SFTP Kind = "sftp"
	S3   Kind = "s3" // an S3-compatible service, over HTTPS
)

// Target is where the household's backups go, as the admin chose it. None of it is secret.
type Target struct {
	Kind Kind `json:"kind"`
	// Path is the folder: on this machine for a disk, on the server for SFTP (absolute both); for S3, the prefix in the
	// bucket ("" for none).
	Path string `json:"path,omitempty"`
	// Host and Port: the SFTP server (port 22 when 0), the S3 endpoint (443 when 0).
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
	// SFTP: the user, and the server's public key ("ssh-ed25519 AAAA…"), pinned.
	User    string `json:"user,omitempty"`
	HostKey string `json:"hostKey,omitempty"`
	// S3: the bucket, and a certificate authority (PEM) to trust for a service of the household's own.
	Bucket string `json:"bucket,omitempty"`
	CA     string `json:"ca,omitempty"`
}

// Credentials are the target's secrets.
type Credentials struct {
	AccessKeyID     string `json:"accessKeyId,omitempty"`     // S3
	SecretAccessKey string `json:"secretAccessKey,omitempty"` // S3
	SSHKey          string `json:"sshKey,omitempty"`          // SFTP: an OpenSSH private key
}

var (
	node     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	hostname = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	user     = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	bucket   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	hostKey  = regexp.MustCompile(`^(ssh-ed25519|ecdsa-sha2-nistp(256|384|521)|ssh-rsa) AAAA[A-Za-z0-9+/]{16,}={0,2}$`)
	keyID    = regexp.MustCompile(`^[A-Za-z0-9._/+=-]{1,128}$`)
	pathPart = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	file     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	arg      = regexp.MustCompile(`^[A-Za-z0-9._,=:/@+%-]{1,200}$`)
	program  = regexp.MustCompile(`^[A-Za-z0-9._/][A-Za-z0-9._/-]{0,199}$`)
	tag      = regexp.MustCompile(`^[a-z][a-z-]*=[A-Za-z0-9._:/@-]{1,200}$`)
)

// ValidPath says whether p is a path backups may name: absolute, clean, not the root, of plain characters (no comma:
// it rides in a restic tag).
func ValidPath(p string) bool {
	if len(p) < 2 || len(p) > 1024 || p[0] != '/' || path.Clean(p) != p {
		return false
	}
	for part := range strings.SplitSeq(p[1:], "/") {
		if !pathPart.MatchString(part) || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// ValidCommand says whether c is a dump's command backups may run: a program, never a flag, and plain arguments (no
// space, no shell's characters), so it means the same however it is passed on.
func ValidCommand(c []string) bool {
	if len(c) == 0 || len(c) > 32 || !program.MatchString(c[0]) {
		return false
	}
	for _, a := range c[1:] {
		if !arg.MatchString(a) {
			return false
		}
	}
	return true
}

// ValidFile says whether f is a name a dump's file may have.
func ValidFile(f string) bool { return file.MatchString(f) }

// Check says what is wrong with the target, or nil.
func (t Target) Check() error {
	bad := func(what string) error { return fmt.Errorf("backup target: %s", what) }
	if t.Port < 0 || t.Port > 65535 {
		return bad("the port")
	}
	switch t.Kind {
	case Disk:
		if !ValidPath(t.Path) {
			return bad("an absolute folder of plain characters")
		}
	case SFTP:
		switch {
		case !hostname.MatchString(t.Host):
			return bad("the server's name")
		case !user.MatchString(t.User):
			return bad("the user")
		case !ValidPath(t.Path):
			return bad("an absolute folder on the server, of plain characters")
		case !hostKey.MatchString(t.HostKey):
			return bad("the server's public key, as `ssh-ed25519 AAAA…`")
		}
	case S3:
		switch {
		case !hostname.MatchString(t.Host):
			return bad("the endpoint's name")
		case !bucket.MatchString(t.Bucket):
			return bad("the bucket")
		case t.Path != "" && !ValidPath("/"+t.Path):
			return bad("a prefix of plain characters, without a leading slash")
		}
		if t.CA != "" {
			b, _ := pem.Decode([]byte(t.CA))
			if b == nil || b.Type != "CERTIFICATE" {
				return bad("the certificate authority, as PEM")
			}
			if _, err := x509.ParseCertificate(b.Bytes); err != nil {
				return bad("the certificate authority, as PEM")
			}
		}
	default:
		return bad("a disk, SFTP or S3")
	}
	return nil
}

// Location is the node's own repository at the target: one per node (#90), the target's folder or prefix and the
// node's name. It is the one place that says so, so another layout is one change.
func (t Target) Location(nodeName string) (string, error) {
	if err := t.Check(); err != nil {
		return "", err
	}
	if !node.MatchString(nodeName) {
		return "", fmt.Errorf("backup target: node %q", nodeName)
	}
	switch t.Kind {
	case Disk:
		return path.Join(t.Path, nodeName), nil
	case SFTP:
		return "sftp:" + t.User + "@" + t.Host + ":" + path.Join(t.Path, nodeName), nil
	}
	host := t.Host
	if t.Port != 0 {
		host += ":" + strconv.Itoa(t.Port)
	}
	return "s3:https://" + host + "/" + path.Join(t.Bucket, t.Path, nodeName), nil
}

// The files of a credentials folder, by credential ID.
const (
	KeyFile   = "backup-key"   // the node's repository key
	S3File    = "backup-s3"    // S3's keys, as an AWS credentials file
	SSHFile   = "backup-ssh"   // the SFTP key
	HostsFile = "backup-hosts" // the SFTP server's key, as a known-hosts line
	CAFile    = "backup-ca"    // the S3 service's certificate authority
)

// Files are what a credentials folder holds for the node's repository at t, by credential ID.
func Files(t Target, key []byte, c Credentials) (map[string][]byte, error) {
	if err := t.Check(); err != nil {
		return nil, err
	}
	if len(key) < 32 || bytes.ContainsAny(key, "\r\n") {
		return nil, errors.New("backup target: a repository key of at least 32 characters, on one line")
	}
	out := map[string][]byte{KeyFile: key}
	switch t.Kind {
	case S3:
		if !keyID.MatchString(c.AccessKeyID) || !keyID.MatchString(c.SecretAccessKey) {
			return nil, errors.New("backup target: S3's access key and its secret")
		}
		out[S3File] = fmt.Appendf(nil, "[default]\naws_access_key_id = %s\naws_secret_access_key = %s\n", c.AccessKeyID, c.SecretAccessKey)
		if t.CA != "" {
			out[CAFile] = []byte(t.CA)
		}
	case SFTP:
		k := strings.TrimSpace(c.SSHKey)
		if !strings.HasPrefix(k, "-----BEGIN ") || !strings.HasSuffix(k, " PRIVATE KEY-----") {
			return nil, errors.New("backup target: the SFTP key, as an OpenSSH private key")
		}
		out[SSHFile] = []byte(k + "\n")
		host := t.Host
		if t.Port != 0 && t.Port != 22 {
			host = "[" + t.Host + "]:" + strconv.Itoa(t.Port)
		}
		out[HostsFile] = []byte(host + " " + t.HostKey + "\n")
	}
	return out, nil
}

// Runner runs restic's argv with env (and nothing else of the caller's environment), stdin on its standard input when
// not nil, and returns what it wrote on stdout. A failure's error wraps the *exec.ExitError, whose code says why.
type Runner func(ctx context.Context, argv, env []string, stdin []byte) ([]byte, error)

// Repo is a node's repository at the household's target.
type Repo struct {
	Restic string // the pinned restic
	Target Target
	Node   string
	// Dir is the credentials folder, holding Files.
	Dir string
	// Cache is restic's cache, on the app-data disk; none when "".
	Cache string
	Run   Runner
}

// The ways restic fails that its exit codes tell.
var (
	ErrNoRepository = errors.New("no-repository") // the target holds no repository for the node
	ErrLocked       = errors.New("locked")        // another live run holds the repository
	ErrWrongKey     = errors.New("wrong-key")     // the repository is there, and the key doesn't open it
	ErrIncomplete   = errors.New("incomplete")    // a snapshot was saved, without some files it couldn't read
)

func (r Repo) run(ctx context.Context, args ...string) ([]byte, error) {
	return r.runWith(ctx, nil, args...)
}

func (r Repo) runWith(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	loc, err := r.Target.Location(r.Node)
	if err != nil {
		return nil, err
	}
	argv := []string{r.Restic, "--repo", loc, "--password-file", path.Join(r.Dir, KeyFile), "--json", "--quiet"}
	if r.Cache != "" {
		argv = append(argv, "--cache-dir", r.Cache)
	} else {
		argv = append(argv, "--no-cache")
	}
	var env []string
	switch r.Target.Kind {
	case SFTP:
		port := r.Target.Port
		if port == 0 {
			port = 22
		}
		// Only the server pinned in the folder's known-hosts file, only the folder's key, no ssh configuration.
		argv = append(argv, "-o", fmt.Sprintf("sftp.command=ssh -F none -i %s -o IdentitiesOnly=yes -o UserKnownHostsFile=%s "+
			"-o StrictHostKeyChecking=yes -o BatchMode=yes -p %d %s@%s -s sftp",
			path.Join(r.Dir, SSHFile), path.Join(r.Dir, HostsFile), port, r.Target.User, r.Target.Host))
	case S3:
		env = append(env, "AWS_SHARED_CREDENTIALS_FILE="+path.Join(r.Dir, S3File))
		if r.Target.CA != "" {
			argv = append(argv, "--cacert", path.Join(r.Dir, CAFile))
		}
	}
	out, err := r.Run(ctx, append(argv, args...), env, stdin)
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case 3:
			return out, fmt.Errorf("%w: %w", ErrIncomplete, err)
		case 10:
			return out, fmt.Errorf("%w: %w", ErrNoRepository, err)
		case 11:
			return out, fmt.Errorf("%w: %w", ErrLocked, err)
		case 12:
			return out, fmt.Errorf("%w: %w", ErrWrongKey, err)
		}
	}
	return out, err
}

// Init makes the node's repository with its key, unless the target has it already: then the key must open it.
func (r Repo) Init(ctx context.Context) error {
	_, err := r.run(ctx, "cat", "config")
	if errors.Is(err, ErrNoRepository) {
		_, err = r.run(ctx, "init")
	}
	return err
}

// Unlock removes the locks restic finds stale: left by a run on this machine whose process is gone, or not refreshed
// for 30 minutes. A live run's lock stays.
func (r Repo) Unlock(ctx context.Context) error {
	_, err := r.run(ctx, "unlock")
	return err
}

// Item is one thing to back up: folders, or as a file, a command's output (a database dump) or what the caller hands
// over (Stdin), with the tags that say whose it is.
type Item struct {
	Tags    []string
	Paths   []string
	Command []string
	Stdin   []byte
	File    string
}

// Backup saves one snapshot of the item, after clearing stale locks, and returns its id.
func (r Repo) Backup(ctx context.Context, it Item) (string, error) {
	args := []string{"backup", "--host", r.Node}
	for _, t := range it.Tags {
		if !tag.MatchString(t) {
			return "", fmt.Errorf("backup: tag %q", t)
		}
		args = append(args, "--tag", t)
	}
	switch {
	case len(it.Command) > 0 && len(it.Paths) == 0 && it.Stdin == nil && ValidCommand(it.Command) && ValidFile(it.File):
		args = append(append(args, "--stdin-from-command", "--stdin-filename", it.File, "--"), it.Command...)
	case it.Stdin != nil && len(it.Command) == 0 && len(it.Paths) == 0 && ValidFile(it.File):
		args = append(args, "--stdin", "--stdin-filename", it.File)
	case len(it.Command) == 0 && it.Stdin == nil && len(it.Paths) > 0:
		args = append(args, "--")
		for _, p := range it.Paths {
			if !ValidPath(p) {
				return "", fmt.Errorf("backup: path %q", p)
			}
			args = append(args, p)
		}
	default:
		return "", errors.New("backup: folders, or a dump's command and its file")
	}
	if err := r.Unlock(ctx); err != nil {
		return "", err
	}
	out, err := r.runWith(ctx, it.Stdin, args...)
	id := summary(out)
	if err == nil && id == "" {
		err = errors.New("backup: restic named no snapshot")
	}
	return id, err
}

// summary is the snapshot id in backup's last message.
func summary(out []byte) string {
	for line := range bytes.SplitSeq(out, []byte("\n")) {
		var m struct {
			Type string `json:"message_type"`
			ID   string `json:"snapshot_id"`
		}
		if json.Unmarshal(line, &m) == nil && m.Type == "summary" {
			return m.ID
		}
	}
	return ""
}

// Snapshot is one snapshot, as restic lists it.
type Snapshot struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time"`
	Host  string    `json:"hostname"`
	Paths []string  `json:"paths"`
	Tags  []string  `json:"tags"`
}

// Snapshots are the repository's snapshots that carry every one of tags, oldest first.
func (r Repo) Snapshots(ctx context.Context, tags ...string) ([]Snapshot, error) {
	args := []string{"snapshots", "--no-lock"}
	for _, t := range tags {
		if !tag.MatchString(t) {
			return nil, fmt.Errorf("backup: tag %q", t)
		}
	}
	if len(tags) > 0 {
		args = append(args, "--tag", strings.Join(tags, ","))
	}
	out, err := r.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	s := []Snapshot{}
	return s, json.Unmarshal(out, &s)
}

// The schedule snapshots are kept by, for each app's item on its own (proposed in the PR: two days of hours, then
// days, weeks and months), and the most prune repacks in one run, so that it keeps freeing space on a target that is
// nearly full.
var keep = []string{"--keep-hourly", "48", "--keep-daily", "30", "--keep-weekly", "8", "--keep-monthly", "12"}

const maxRepack = "2G"

// Prune forgets the snapshots the schedule no longer keeps and frees their data, after clearing stale locks.
func (r Repo) Prune(ctx context.Context) error {
	if err := r.Unlock(ctx); err != nil {
		return err
	}
	_, err := r.run(ctx, append([]string{"forget", "--prune", "--max-repack-size", maxRepack, "--group-by", "host,tags"}, keep...)...)
	return err
}
