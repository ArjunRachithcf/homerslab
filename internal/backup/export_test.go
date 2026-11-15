package backup

import "context"

// Raw runs restic on r's repository with r's flags and files, for the contract suite's own checks.
func Raw(ctx context.Context, r Repo, args ...string) ([]byte, error) { return r.run(ctx, args...) }

// S3Do sends one signed request about the target's bucket, for the contract suite to set its world up.
func S3Do(ctx context.Context, t Target, c Credentials, method, sub string, body []byte) (int, []byte, error) {
	return s3Do(ctx, t, c, method, sub, body)
}

// B2At points B2's endpoint at host, for the contract suite's stand-in for B2's S3-compatible API.
func B2At(host string) func() {
	was := b2Endpoint
	b2Endpoint = func(string) string { return host }
	return func() { b2Endpoint = was }
}
