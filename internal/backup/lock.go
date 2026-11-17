package backup

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Immutability says whether whoever holds the node's credentials (a box taken over, say) can delete the backups it
// wrote, and why, in a word (#22, part 3; spec: Adapter contracts → Backup target, "immutable where the target supports
// it"):
//   - "compliance": an S3 or B2 bucket with Object Lock in compliance mode by default, versioning on, and every object of
//     the node's repository locked until LockedUntil by Homeward's own lock run (Lock). A bucket's default retention
//     locks each object version only for Days after it was written, and restic writes its config, its keys and most
//     packs once, so without that run the oldest, most needed objects come free first. Nobody can delete or overwrite a
//     locked version, whatever the key; what prune or a thief deletes is only hidden, and is restored by hand by reading
//     the bucket as it was (`rclone serve restic --s3-version-at`);
//   - "lock-expires": locked in compliance mode, but the repository's older objects aren't locked past their Days: before
//     the first lock run, after one that failed, or once the last one's lock has run out. Not immutable;
//   - "governance": locked, but a key allowed to bypass governance can delete them: not immutable;
//   - "no-object-lock", "no-default-retention", "versioning-off": the bucket isn't locked, or not all of it;
//   - "unknown": the bucket's settings couldn't be read;
//   - "disk": a disk is written by this machine, so whoever takes it over can delete it;
//   - "sftp": an SFTP server lets the account that writes delete too. Only snapshots on the server, out of this
//     machine's reach, keep backups from it.
type Immutability struct {
	Immutable bool   `json:"immutable"`
	Why       string `json:"why"`
	Days      int    `json:"days,omitempty"` // the bucket's default retention: each new object version is locked this long
	// At is when the bucket's settings were read.
	At time.Time `json:"at,omitzero"`
	// LockedUntil is how long every object of the node's repository is locked for at least, from the last lock run.
	LockedUntil time.Time `json:"lockedUntil,omitzero"`
}

// Now is im as of now: a lock that has run out says so.
func (im Immutability) Now(now time.Time) Immutability {
	if im.Immutable && !now.Before(im.LockedUntil) {
		im.Immutable, im.Why = false, "lock-expires"
	}
	return im
}

// lockWait is the longest one request about the bucket or one of its objects takes.
const lockWait = 30 * time.Second

// Lock reads whether the target keeps the node's backups from whoever holds its credentials. On an S3 or B2 bucket
// locked in compliance mode, it also extends the lock of every current object of the node's repository to now plus the
// bucket's Days, whenever what the last run locked (was) has less than half of Days left: every object written since is
// locked by the bucket's default for Days after its write. Compliance mode only ever lets a lock be lengthened, so the
// run is safe in a thief's hands too. It runs in restic's unit, with c from its credentials folder (LockMain).
func Lock(ctx context.Context, t Target, c Credentials, nodeName string, was Immutability, now time.Time) (Immutability, error) {
	switch t.Kind {
	case Disk:
		return Immutability{Why: "disk", At: now}, nil
	case SFTP:
		return Immutability{Why: "sftp", At: now}, nil
	}
	unknown := Immutability{Why: "unknown", At: now}
	if t.Check() != nil || !node.MatchString(nodeName) {
		return unknown, errors.New("backup: the target or the node")
	}
	cl, done, err := bucketClient(t, c)
	if err != nil {
		return unknown, err
	}
	defer done()
	im, err := readLock(ctx, cl, t)
	im.At = now
	if err != nil || im.Why != "compliance" {
		return im, err
	}
	until := now.Add(time.Duration(im.Days) * 24 * time.Hour)
	if was.Why == "compliance" && was.Days == im.Days && was.LockedUntil.Sub(now) > until.Sub(now)/2 {
		im.Immutable, im.LockedUntil = true, was.LockedUntil
		return im, nil
	}
	im.Why = "lock-expires"
	if err := extend(ctx, cl, t, nodeName, until); err != nil {
		return im, err
	}
	im.Immutable, im.Why, im.LockedUntil = true, "compliance", until
	return im, nil
}

// readLock reads the bucket's Object Lock and versioning.
func readLock(ctx context.Context, cl *minio.Client, t Target) (Immutability, error) {
	ctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	enabled, mode, validity, unit, err := cl.GetObjectLockConfig(ctx, t.Bucket)
	if err != nil {
		e := minio.ToErrorResponse(err)
		if e.Code == "ObjectLockConfigurationNotFoundError" || e.StatusCode == http.StatusNotFound && e.Code != "NoSuchBucket" {
			return Immutability{Why: "no-object-lock"}, nil
		}
		return Immutability{Why: "unknown"}, err
	}
	if enabled != "Enabled" {
		return Immutability{Why: "no-object-lock"}, nil
	}
	if mode == nil || validity == nil || unit == nil || *validity == 0 {
		return Immutability{Why: "no-default-retention"}, nil
	}
	days := int(*validity)
	if *unit == minio.Years {
		days *= 365
	}
	if *mode != minio.Compliance {
		return Immutability{Why: "governance", Days: days}, nil
	}
	// A bucket with Object Lock always has versioning on (S3 refuses to suspend it); a service that let it be suspended
	// would let a thief overwrite in place.
	v, err := cl.GetBucketVersioning(ctx, t.Bucket)
	if err != nil {
		return Immutability{Why: "unknown"}, err
	}
	if !v.Enabled() {
		return Immutability{Why: "versioning-off", Days: days}, nil
	}
	return Immutability{Why: "compliance", Days: days}, nil
}

// extend locks every current object version of the node's repository until at least until, in compliance mode. One
// already locked for longer (the bucket's Days were shortened since) is left as it is.
func extend(ctx context.Context, cl *minio.Client, t Target, nodeName string, until time.Time) error {
	prefix := path.Join(t.Path, nodeName) + "/"
	mode := minio.Compliance
	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // ends the listing when an object fails
	n := 0
	for o := range cl.ListObjects(ctx, t.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true, WithVersions: true}) {
		if o.Err != nil {
			return fmt.Errorf("backup: listing the repository: %w", o.Err)
		}
		if !o.IsLatest || o.IsDeleteMarker {
			continue // hidden already: it was locked while it was current
		}
		n++
		if err := lockOne(ctx, cl, t.Bucket, o, &mode, until); err != nil {
			return err
		}
	}
	if n == 0 {
		return errors.New("backup: the node's repository holds nothing to lock")
	}
	return nil
}

func lockOne(ctx context.Context, cl *minio.Client, bucketName string, o minio.ObjectInfo, mode *minio.RetentionMode, until time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	err := cl.PutObjectRetention(ctx, bucketName, o.Key, minio.PutObjectRetentionOptions{Mode: mode, RetainUntilDate: &until, VersionID: o.VersionID})
	if err == nil {
		return nil
	}
	// Compliance mode refuses to shorten a lock: one locked for longer is fine.
	m, kept, gerr := cl.GetObjectRetention(ctx, bucketName, o.Key, o.VersionID)
	if gerr != nil || m == nil || *m != minio.Compliance || kept == nil || kept.Before(until) {
		return fmt.Errorf("backup: locking %s: %w", o.Key, err)
	}
	return nil
}

// bucketClient is minio-go, restic's own S3 client, for the target's bucket: over HTTPS only, path-style, trusting the
// target's own CA when it names one, never following a redirect (minio-go's own rule). done closes its connections.
func bucketClient(t Target, c Credentials) (*minio.Client, func(), error) {
	if !keyID.MatchString(c.AccessKeyID) || !keyID.MatchString(c.SecretAccessKey) {
		return nil, nil, errors.New("backup: S3's access key and its secret")
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if t.CA != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(t.CA)) {
			return nil, nil, errors.New("backup: the certificate authority")
		}
		tc.RootCAs = pool
	}
	tr := &http.Transport{TLSClientConfig: tc, Proxy: nil, ForceAttemptHTTP2: true, IdleConnTimeout: time.Minute}
	cl, err := minio.New(t.endpoint(), &minio.Options{Creds: credentials.NewStaticV4(c.AccessKeyID, c.SecretAccessKey, ""),
		Secure: true, Transport: tr, Region: t.Region, BucketLookup: minio.BucketLookupPath})
	if err != nil {
		return nil, nil, err
	}
	return cl, tr.CloseIdleConnections, nil
}

// LockRequest is what LockMain reads on its standard input: none of it secret.
type LockRequest struct {
	Target Target       `json:"target"`
	Node   string       `json:"node"`
	Was    Immutability `json:"was"`
}

// LockMain is `homeward agent backup-lock`, which the agent runs in restic's unit (never by hand): it reads a
// LockRequest on in and the target's keys and CA from the unit's credentials folder (dir: $CREDENTIALS_DIRECTORY), runs
// Lock, and writes the result as JSON on out. What it says names no secret.
func LockMain(args []string, dir string, in io.Reader, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("homeward agent backup-lock", flag.ContinueOnError)
	fs.SetOutput(errOut)
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	var req LockRequest
	if err := json.NewDecoder(io.LimitReader(in, 64<<10)).Decode(&req); err != nil {
		fmt.Fprintln(errOut, "backup-lock: not a request")
		return 2
	}
	var c Credentials
	if req.Target.Kind == S3 || req.Target.Kind == B2 {
		var err error
		if c, err = credentialsFrom(dir); err != nil {
			fmt.Fprintln(errOut, "backup-lock:", err)
			return 1
		}
		if req.Target.CA != "" {
			b, err := os.ReadFile(filepath.Join(dir, CAFile)) // the unit's copy, as restic reads it
			if err != nil {
				fmt.Fprintln(errOut, "backup-lock: no certificate authority in the credentials folder")
				return 1
			}
			req.Target.CA = string(b)
		}
	}
	im, err := Lock(context.Background(), req.Target, c, req.Node, req.Was, time.Now().UTC())
	_ = json.NewEncoder(out).Encode(im)
	if err != nil {
		fmt.Fprintln(errOut, "backup-lock:", err)
		return 1
	}
	return 0
}

// credentialsFrom reads S3's keys from the AWS credentials file Files wrote in a credentials folder.
func credentialsFrom(dir string) (Credentials, error) {
	var c Credentials
	if dir == "" {
		return c, errors.New("no credentials folder")
	}
	b, err := os.ReadFile(filepath.Join(dir, S3File))
	if err != nil {
		return c, errors.New("no S3 keys in the credentials folder")
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), "=")
		switch strings.TrimSpace(k) {
		case "aws_access_key_id":
			c.AccessKeyID = strings.TrimSpace(v)
		case "aws_secret_access_key":
			c.SecretAccessKey = strings.TrimSpace(v)
		}
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return c, errors.New("S3's keys, unreadable")
	}
	return c, nil
}
