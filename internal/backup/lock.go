package backup

import (
	"bytes"
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // S3's Content-MD5, not a security check
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Immutability says whether whoever holds the node's credentials (a box taken over, say) can delete the backups it
// wrote, and why, in a word (#22, part 3; spec: Adapter contracts → Backup target, "immutable where the target supports
// it"):
//   - "compliance": an S3 or B2 bucket with Object Lock in compliance mode by default, and versioning on. Nobody can
//     delete or overwrite an object version until its lock runs out, Days after it was written, whatever the key. What
//     prune or a thief deletes is only hidden, and is restored by hand by reading the bucket as it was
//     (`rclone serve restic --s3-version-at`);
//   - "governance": locked, but a key allowed to bypass governance can delete them: not immutable;
//   - "no-object-lock", "no-default-retention", "versioning-off": the bucket isn't locked, or not all of it;
//   - "unknown": the bucket's settings couldn't be read;
//   - "disk": a disk is written by this machine, so whoever takes it over can delete it;
//   - "sftp": an SFTP server lets the account that writes delete too. Only snapshots on the server, out of this
//     machine's reach, keep backups from it.
type Immutability struct {
	Immutable bool   `json:"immutable"`
	Why       string `json:"why"`
	Days      int    `json:"days,omitempty"` // how long each object version is locked for, from when it was written
}

// lockWait is the longest reading a bucket's settings takes.
const lockWait = 30 * time.Second

// ReadImmutability reads whether the target keeps the node's backups from whoever holds its credentials: for S3 and
// B2, the bucket's Object Lock and versioning, asked of the service with c (the target's own credentials, which go
// nowhere else).
func ReadImmutability(ctx context.Context, t Target, c Credentials) Immutability {
	switch t.Kind {
	case Disk:
		return Immutability{Why: "disk"}
	case SFTP:
		return Immutability{Why: "sftp"}
	}
	if t.Check() != nil {
		return Immutability{Why: "unknown"}
	}
	ctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	code, body, err := s3Do(ctx, t, c, http.MethodGet, "object-lock", nil)
	if err != nil {
		return Immutability{Why: "unknown"}
	}
	var lock struct {
		Enabled string `xml:"ObjectLockEnabled"`
		Mode    string `xml:"Rule>DefaultRetention>Mode"`
		Days    int    `xml:"Rule>DefaultRetention>Days"`
		Years   int    `xml:"Rule>DefaultRetention>Years"`
	}
	switch {
	case bytes.Contains(body, []byte("ObjectLockConfigurationNotFound")), code == http.StatusNotFound && !bytes.Contains(body, []byte("NoSuchBucket")):
		return Immutability{Why: "no-object-lock"}
	case code != http.StatusOK || xml.Unmarshal(body, &lock) != nil:
		return Immutability{Why: "unknown"}
	case lock.Enabled != "Enabled":
		return Immutability{Why: "no-object-lock"}
	}
	days := lock.Days + 365*lock.Years
	if lock.Mode == "" || days <= 0 {
		return Immutability{Why: "no-default-retention"}
	}
	if lock.Mode != "COMPLIANCE" {
		return Immutability{Why: "governance", Days: days}
	}
	// A bucket with Object Lock always has versioning on (S3 refuses to suspend it); a service that let it be suspended
	// would let a thief overwrite in place.
	code, body, err = s3Do(ctx, t, c, http.MethodGet, "versioning", nil)
	var v struct {
		Status string `xml:"Status"`
	}
	if err != nil || code != http.StatusOK || xml.Unmarshal(body, &v) != nil {
		return Immutability{Why: "unknown"}
	}
	if v.Status != "Enabled" {
		return Immutability{Why: "versioning-off", Days: days}
	}
	return Immutability{Immutable: true, Why: "compliance", Days: days}
}

// maxAnswer is the most of a service's answer that is read.
const maxAnswer = 64 << 10

// s3Do sends one request about the target's bucket (`?<sub>`, path-style), signed with AWS Signature Version 4, and
// returns the service's status and answer. Over HTTPS only, trusting the target's own CA when it names one.
func s3Do(ctx context.Context, t Target, c Credentials, method, sub string, body []byte) (int, []byte, error) {
	if !keyID.MatchString(c.AccessKeyID) || !keyID.MatchString(c.SecretAccessKey) {
		return 0, nil, errors.New("backup: S3's access key and its secret")
	}
	if !bucket.MatchString(t.Bucket) || sub != "object-lock" && sub != "versioning" {
		return 0, nil, errors.New("backup: the bucket's lock or versioning")
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if t.CA != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(t.CA)) {
			return 0, nil, errors.New("backup: the certificate authority")
		}
		tc.RootCAs = pool
	}
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: tc, Proxy: nil, ForceAttemptHTTP2: true, DisableKeepAlives: true},
		// A redirect would send the signed request elsewhere: it is an answer, never followed.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	host := t.endpoint()
	send := func(region string) (*http.Response, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, "https://"+host+"/"+t.Bucket+"?"+sub, bytes.NewReader(body))
		if err != nil {
			return nil, nil, err
		}
		if body != nil {
			sum := md5.Sum(body) // S3 wants it for a bucket's settings
			req.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(sum[:]))
		}
		sign(req, host, region, c, body, time.Now().UTC())
		resp, err := client.Do(req)
		if err != nil {
			return nil, nil, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
		return resp, b, err
	}
	resp, b, err := send(cmp.Or(t.Region, "us-east-1"))
	// A target that names no region (restic finds it itself): AWS says the bucket's own when the guess was wrong, and
	// the request is signed again for it, at the same endpoint, never somewhere else.
	if err == nil && t.Region == "" && resp.StatusCode != http.StatusOK {
		if r := resp.Header.Get("X-Amz-Bucket-Region"); region.MatchString(r) && r != "us-east-1" {
			resp, b, err = send(r)
		}
	}
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, b, nil
}

// sign signs req (a path-style request about a bucket, with one bare query key) with AWS Signature Version 4.
func sign(req *http.Request, host, region string, c Credentials, body []byte, now time.Time) {
	sum := sha256.Sum256(body)
	payload := hex.EncodeToString(sum[:])
	date, day := now.Format("20060102T150405Z"), now.Format("20060102")
	req.Header.Set("X-Amz-Date", date)
	req.Header.Set("X-Amz-Content-Sha256", payload)
	query := req.URL.RawQuery
	if !strings.Contains(query, "=") {
		query += "="
	}
	canonical := strings.Join([]string{req.Method, req.URL.EscapedPath(), query,
		"host:" + host + "\nx-amz-content-sha256:" + payload + "\nx-amz-date:" + date + "\n",
		"host;x-amz-content-sha256;x-amz-date", payload}, "\n")
	scope := day + "/" + region + "/s3/aws4_request"
	h := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + date + "\n" + scope + "\n" + hex.EncodeToString(h[:])
	key := []byte("AWS4" + c.SecretAccessKey)
	for _, part := range []string{day, region, "s3", "aws4_request"} {
		key = mac(key, part)
	}
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=%x",
		c.AccessKeyID, scope, mac(key, toSign)))
}

func mac(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}
