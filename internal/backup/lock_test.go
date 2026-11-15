package backup_test

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/backup"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// bucketAt is an S3 service of the household's own (over HTTPS, its CA given) whose bucket answers lock and versioning
// as given, keeping the requests it was sent.
func bucketAt(t *testing.T, lock string, lockCode int, versioning string) (backup.Target, *[]*http.Request) {
	t.Helper()
	var got []*http.Request
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Clone(context.Background()))
		switch {
		case r.URL.Path == "/elsewhere": // where a redirect points: a bucket that says it's locked
			_, _ = w.Write([]byte(compliance))
		case r.URL.Path != "/household":
			w.WriteHeader(http.StatusNotFound)
		case r.URL.RawQuery == "object-lock":
			if lockCode == http.StatusMovedPermanently {
				w.Header().Set("Location", "/elsewhere?object-lock")
			}
			w.WriteHeader(lockCode)
			_, _ = w.Write([]byte(lock))
		case r.URL.RawQuery == "versioning":
			_, _ = w.Write([]byte(versioning))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	return backup.Target{Kind: backup.S3, Host: u.Hostname(), Port: port, Bucket: "household", Path: "homerslab", CA: ca, Region: "eu-west-1"}, &got
}

const (
	versioned  = `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`
	compliance = `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>COMPLIANCE</Mode><Days>30</Days></DefaultRetention></Rule></ObjectLockConfiguration>`
)

// A target is immutable only where nobody holding its credentials can delete what it wrote: an S3 or B2 bucket locked in
// compliance mode by default, versioned. Everything else says plainly why it isn't.
func TestATargetIsImmutableOnlyWhereItsCredentialsCantDelete(t *testing.T) {
	testenv.Isolate(t)
	ctx := context.Background()
	c := backup.Credentials{AccessKeyID: "HLACCESSKEY", SecretAccessKey: "s3-secret-value/42"}
	cases := []struct {
		name, lock string
		code       int
		versioning string
		want       backup.Immutability
	}{
		{"compliance, versioned", compliance, 200, versioned, backup.Immutability{Immutable: true, Why: "compliance", Days: 30}},
		{"a year", strings.Replace(compliance, "<Days>30</Days>", "<Years>1</Years>", 1), 200, versioned, backup.Immutability{Immutable: true, Why: "compliance", Days: 365}},
		{"governance", strings.Replace(compliance, "COMPLIANCE", "GOVERNANCE", 1), 200, versioned, backup.Immutability{Why: "governance", Days: 30}},
		{"no default retention", `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled></ObjectLockConfiguration>`, 200, versioned, backup.Immutability{Why: "no-default-retention"}},
		{"no lock", `<Error><Code>ObjectLockConfigurationNotFoundError</Code></Error>`, 404, versioned, backup.Immutability{Why: "no-object-lock"}},
		{"versioning suspended", compliance, 200, `<VersioningConfiguration><Status>Suspended</Status></VersioningConfiguration>`, backup.Immutability{Why: "versioning-off", Days: 30}},
		{"the service refuses", `<Error><Code>AccessDenied</Code></Error>`, 403, versioned, backup.Immutability{Why: "unknown"}},
		{"a redirect, never followed", "", 301, versioned, backup.Immutability{Why: "unknown"}},
		{"not XML", "{}", 200, versioned, backup.Immutability{Why: "unknown"}},
	}
	for _, tc := range cases {
		tg, got := bucketAt(t, tc.lock, tc.code, tc.versioning)
		if im := backup.ReadImmutability(ctx, tg, c); im != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, im, tc.want)
		}
		for _, r := range *got {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=HLACCESSKEY/") || !strings.Contains(auth, "/eu-west-1/s3/aws4_request") {
				t.Errorf("%s: a request signed as %q", tc.name, auth)
			}
			if strings.Contains(r.URL.String()+strings.Join(r.Header.Values("Authorization"), ""), c.SecretAccessKey) {
				t.Errorf("%s: the secret was sent", tc.name)
			}
		}
	}
	for _, tg := range []backup.Target{targets["disk"], targets["sftp"]} {
		if im := backup.ReadImmutability(ctx, tg, creds[tg.Kind]); im.Immutable || im.Why != string(tg.Kind) {
			t.Errorf("%s: %+v", tg.Kind, im)
		}
	}
	// A service whose certificate isn't the target's CA's is never asked.
	tg, got := bucketAt(t, compliance, 200, versioned)
	tg.CA = ""
	if im := backup.ReadImmutability(ctx, tg, c); im.Why != "unknown" || len(*got) != 0 {
		t.Errorf("a service with a certificate of another's: %+v, %d requests", im, len(*got))
	}
}
