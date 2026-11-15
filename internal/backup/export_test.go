package backup

import (
	"context"

	"github.com/minio/minio-go/v7"
)

// Raw runs restic on r's repository with r's flags and files, for the contract suite's own checks.
func Raw(ctx context.Context, r Repo, args ...string) ([]byte, error) { return r.run(ctx, args...) }

// BucketClient is the lock run's client for the target's bucket, for the contract suite to set its world up and to play
// the thief.
func BucketClient(t Target, c Credentials) (*minio.Client, func(), error) { return bucketClient(t, c) }

// B2At points B2's endpoint at host, for the contract suite's stand-in for B2's S3-compatible API.
func B2At(host string) func() {
	was := b2Endpoint
	b2Endpoint = func(string) string { return host }
	return func() { b2Endpoint = was }
}
