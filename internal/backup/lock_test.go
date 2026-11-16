package backup_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/backup"
	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

const (
	versioned  = `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`
	compliance = `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>COMPLIANCE</Mode><Days>30</Days></DefaultRetention></Rule></ObjectLockConfiguration>`
)

// fakeBucket is an S3 service of the household's own (over HTTPS, its CA given), with one bucket whose lock and
// versioning answer as set, holding the node's repository's object versions, and keeping what it was asked.
type fakeBucket struct {
	mu               sync.Mutex
	lock, versioning string
	lockCode         int
	objects          []fakeObject
	retained         map[string]time.Time // version: the lock it was given
	longer           map[string]time.Time // version: compliance-locked already until then, a shorter lock refused
	deny             map[string]bool      // version: never locked
	auth             []string
	requests         int
	redirect         bool
	followed         bool
}

type fakeObject struct {
	Key, Version    string
	Latest, Deleted bool
}

func (f *fakeBucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	q := r.URL.Query()
	switch {
	case strings.HasPrefix(r.URL.Path, "/elsewhere"):
		f.followed = true
		_, _ = w.Write([]byte(compliance))
	case !strings.HasPrefix(r.URL.Path, "/household"):
		w.WriteHeader(http.StatusNotFound)
	case q.Has("object-lock"):
		if f.redirect {
			w.Header().Set("Location", "/elsewhere/?object-lock")
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}
		if f.lockCode != 0 {
			w.WriteHeader(f.lockCode)
		}
		_, _ = w.Write([]byte(f.lock))
	case q.Has("versioning"):
		_, _ = w.Write([]byte(f.versioning))
	case q.Has("versions"):
		var b strings.Builder
		b.WriteString(`<ListVersionsResult><Name>household</Name><IsTruncated>false</IsTruncated>`)
		for _, o := range f.objects {
			if !strings.HasPrefix(o.Key, q.Get("prefix")) {
				continue
			}
			tag := "Version"
			if o.Deleted {
				tag = "DeleteMarker"
			}
			b.WriteString("<" + tag + "><Key>" + o.Key + "</Key><VersionId>" + o.Version + "</VersionId><IsLatest>" +
				strconv.FormatBool(o.Latest) + "</IsLatest><LastModified>2026-10-01T00:00:00.000Z</LastModified></" + tag + ">")
		}
		b.WriteString(`</ListVersionsResult>`)
		_, _ = w.Write([]byte(b.String()))
	case q.Has("retention") && r.Method == http.MethodPut:
		var ret struct {
			Mode  string
			Until time.Time `xml:"RetainUntilDate"`
		}
		body, _ := io.ReadAll(r.Body)
		v := q.Get("versionId")
		kept, locked := f.longer[v]
		if xml.Unmarshal(body, &ret) != nil || ret.Mode != "COMPLIANCE" || f.deny[v] || locked && ret.Until.Before(kept) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code></Error>`))
			return
		}
		f.retained[v] = ret.Until
	case q.Has("retention"):
		kept, ok := f.longer[q.Get("versionId")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<Error><Code>NoSuchObjectLockConfiguration</Code></Error>`))
			return
		}
		_, _ = w.Write([]byte(`<Retention><Mode>COMPLIANCE</Mode><RetainUntilDate>` + kept.Format(time.RFC3339) + `</RetainUntilDate></Retention>`))
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

// bucketAt serves f, and is the target that names it.
func bucketAt(t *testing.T, f *fakeBucket) backup.Target {
	t.Helper()
	f.retained = map[string]time.Time{}
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	return backup.Target{Kind: backup.S3, Host: u.Hostname(), Port: port, Bucket: "household", Path: "homerslab", CA: ca, Region: "eu-west-1"}
}

// repository is the node's repository as a bucket holds it after a prune: current objects, an older version hidden by a
// delete marker; and another node's.
func repository() []fakeObject {
	return []fakeObject{
		{Key: "homerslab/primary/config", Version: "v-config", Latest: true},
		{Key: "homerslab/primary/keys/k1", Version: "v-key", Latest: true},
		{Key: "homerslab/primary/data/aa/pack", Version: "v-pack", Latest: true},
		{Key: "homerslab/primary/data/bb/gone", Version: "v-gone"},
		{Key: "homerslab/primary/data/bb/gone", Version: "v-marker", Latest: true, Deleted: true},
		{Key: "homerslab/pi/config", Version: "v-pi", Latest: true},
	}
}

var s3Creds = backup.Credentials{AccessKeyID: "HLACCESSKEY", SecretAccessKey: "s3-secret-value/42"}

// A target is immutable only where nobody holding its credentials can delete what it wrote: an S3 or B2 bucket locked in
// compliance mode by default, versioned, and every object of the node's repository locked by Homeward's lock run.
// Everything else says plainly why it isn't.
func TestATargetIsImmutableOnlyWhereItsCredentialsCantDelete(t *testing.T) {
	testenv.Isolate(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name, lock string
		code       int
		versioning string
		want       backup.Immutability
	}{
		{"compliance, versioned", compliance, 200, versioned, backup.Immutability{Immutable: true, Why: "compliance", Days: 30, LockedUntil: now.AddDate(0, 0, 30)}},
		{"a year", strings.Replace(compliance, "<Days>30</Days>", "<Years>1</Years>", 1), 200, versioned, backup.Immutability{Immutable: true, Why: "compliance", Days: 365, LockedUntil: now.AddDate(0, 0, 365)}},
		{"governance", strings.Replace(compliance, "COMPLIANCE", "GOVERNANCE", 1), 200, versioned, backup.Immutability{Why: "governance", Days: 30}},
		{"no default retention", `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled></ObjectLockConfiguration>`, 200, versioned, backup.Immutability{Why: "no-default-retention"}},
		{"no lock", `<Error><Code>ObjectLockConfigurationNotFoundError</Code></Error>`, 404, versioned, backup.Immutability{Why: "no-object-lock"}},
		{"no lock, as another service says it", `<Error><Code>ObjectLockConfigurationNotFoundError</Code></Error>`, 400, versioned, backup.Immutability{Why: "no-object-lock"}},
		{"versioning suspended", compliance, 200, `<VersioningConfiguration><Status>Suspended</Status></VersioningConfiguration>`, backup.Immutability{Why: "versioning-off", Days: 30}},
		{"the service refuses", `<Error><Code>AccessDenied</Code></Error>`, 403, versioned, backup.Immutability{Why: "unknown"}},
		{"not XML", "{}", 200, versioned, backup.Immutability{Why: "unknown"}},
	}
	for _, tc := range cases {
		f := &fakeBucket{lock: tc.lock, lockCode: tc.code, versioning: tc.versioning, objects: repository()}
		tc.want.At = now
		if im, _ := backup.Lock(ctx, bucketAt(t, f), s3Creds, "primary", backup.Immutability{}, now); im != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, im, tc.want)
		}
		for _, a := range f.auth {
			if !strings.HasPrefix(a, "AWS4-HMAC-SHA256 Credential=HLACCESSKEY/") || !strings.Contains(a, "/eu-west-1/s3/aws4_request") || strings.Contains(a, s3Creds.SecretAccessKey) {
				t.Errorf("%s: a request signed as %q", tc.name, a)
			}
		}
	}
	for _, tg := range []backup.Target{targets["disk"], targets["sftp"]} {
		if im, err := backup.Lock(ctx, tg, creds[tg.Kind], "primary", backup.Immutability{}, now); err != nil || im.Immutable || im.Why != string(tg.Kind) {
			t.Errorf("%s: %+v %v", tg.Kind, im, err)
		}
	}
	// A redirect is never followed, whatever it points at.
	f := &fakeBucket{lock: compliance, versioning: versioned, objects: repository(), redirect: true}
	if im, _ := backup.Lock(ctx, bucketAt(t, f), s3Creds, "primary", backup.Immutability{}, now); im.Immutable || f.followed {
		t.Errorf("a redirect: %+v, followed: %v", im, f.followed)
	}
	// A service whose certificate isn't the target's CA's is never asked.
	f = &fakeBucket{lock: compliance, versioning: versioned, objects: repository()}
	tg := bucketAt(t, f)
	tg.CA = ""
	if im, _ := backup.Lock(ctx, tg, s3Creds, "primary", backup.Immutability{}, now); im.Why != "unknown" || f.requests != 0 {
		t.Errorf("a service with a certificate of another's: %+v, %d requests", im, f.requests)
	}
}

// A bucket's default retention locks each object only for Days after it was written, and restic writes its config, its
// keys and most packs once: so the lock run locks every current object of the node's repository (never a hidden
// version, never another node's) until now plus Days, and until it has, the target isn't immutable.
func TestTheLockRunKeepsEveryObjectOfTheRepositoryLocked(t *testing.T) {
	testenv.Isolate(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	until := now.AddDate(0, 0, 30)
	f := &fakeBucket{lock: compliance, versioning: versioned, objects: repository()}
	tg := bucketAt(t, f)
	im, err := backup.Lock(ctx, tg, s3Creds, "primary", backup.Immutability{}, now)
	if err != nil || !im.Immutable || !im.LockedUntil.Equal(until) {
		t.Fatalf("the first run: %+v %v", im, err)
	}
	if len(f.retained) != 3 || !f.retained["v-config"].Equal(until) || !f.retained["v-key"].Equal(until) || !f.retained["v-pack"].Equal(until) {
		t.Errorf("locked: %v, want the config, the key and the pack until %s", f.retained, until)
	}

	// A day on, more than half of the lock is left, and every object since is locked by the bucket's default for longer.
	f.retained = map[string]time.Time{}
	next, err := backup.Lock(ctx, tg, s3Creds, "primary", im, now.AddDate(0, 0, 1))
	if err != nil || !next.Immutable || !next.LockedUntil.Equal(until) || len(f.retained) != 0 {
		t.Errorf("a day on: %+v %v, %d locked again", next, err, len(f.retained))
	}
	// Half of it gone: everything is locked again, for Days from now.
	later := now.AddDate(0, 0, 16)
	again, err := backup.Lock(ctx, tg, s3Creds, "primary", next, later)
	if err != nil || !again.LockedUntil.Equal(later.AddDate(0, 0, 30)) || len(f.retained) != 3 {
		t.Errorf("16 days on: %+v %v, %d locked", again, err, len(f.retained))
	}
	// The bucket's Days changed: everything is locked again at once.
	f.retained = map[string]time.Time{}
	f.lock = strings.Replace(compliance, "<Days>30</Days>", "<Days>60</Days>", 1)
	if longer, err := backup.Lock(ctx, tg, s3Creds, "primary", again, later.AddDate(0, 0, 1)); err != nil || longer.Days != 60 || len(f.retained) != 3 {
		t.Errorf("Days lengthened: %+v %v, %d locked", longer, err, len(f.retained))
	}

	// An object already locked for longer (the Days were shortened since) is fine; one the service won't lock isn't.
	f = &fakeBucket{lock: compliance, versioning: versioned, objects: repository(), longer: map[string]time.Time{"v-key": until.AddDate(1, 0, 0)}}
	if im, err := backup.Lock(ctx, bucketAt(t, f), s3Creds, "primary", backup.Immutability{}, now); err != nil || !im.Immutable {
		t.Errorf("an object locked for longer already: %+v %v", im, err)
	}
	f = &fakeBucket{lock: compliance, versioning: versioned, objects: repository(), deny: map[string]bool{"v-key": true}}
	if im, err := backup.Lock(ctx, bucketAt(t, f), s3Creds, "primary", backup.Immutability{}, now); err == nil || im.Immutable || im.Why != "lock-expires" {
		t.Errorf("an object the service won't lock: %+v %v", im, err)
	}
	// A repository with nothing in it has nothing locked.
	f = &fakeBucket{lock: compliance, versioning: versioned}
	if im, err := backup.Lock(ctx, bucketAt(t, f), s3Creds, "primary", backup.Immutability{}, now); err == nil || im.Immutable {
		t.Errorf("an empty repository: %+v %v", im, err)
	}
	// Once what the last run locked runs out, it isn't immutable any more.
	if late := im.Now(until); late.Immutable || late.Why != "lock-expires" {
		t.Errorf("past its lock: %+v", late)
	}
	if early := im.Now(until.Add(-time.Hour)); !early.Immutable {
		t.Errorf("before its lock runs out: %+v", early)
	}
}

// The lock run is the agent's own program in restic's unit: it reads the target (no secret) on its standard input, the
// keys and the CA from the unit's credentials folder, and writes what it found.
func TestTheLockRunReadsItsKeysFromTheUnitsCredentials(t *testing.T) {
	testenv.Isolate(t)
	f := &fakeBucket{lock: compliance, versioning: versioned, objects: repository()}
	tg := bucketAt(t, f)
	files, err := backup.Files(tg, []byte("the-repository-key-0123456789abcdef"), s3Creds)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	other := tg
	other.CA, _ = selfSigned(t, "other") // the request's CA is ignored: the unit's own copy is read
	in, _ := json.Marshal(backup.LockRequest{Target: other, Node: "primary"})
	var out, errOut bytes.Buffer
	if code := backup.LockMain(nil, dir, bytes.NewReader(in), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var im backup.Immutability
	if err := json.Unmarshal(out.Bytes(), &im); err != nil || !im.Immutable || im.At.IsZero() {
		t.Errorf("its answer: %s %v", out.String(), err)
	}
	if strings.Contains(out.String()+errOut.String(), s3Creds.SecretAccessKey) {
		t.Error("it wrote the secret")
	}
	out.Reset()
	if code := backup.LockMain(nil, t.TempDir(), bytes.NewReader(in), &out, &errOut); code == 0 {
		t.Error("it ran without keys")
	}
}
