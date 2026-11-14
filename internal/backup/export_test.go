package backup

import "context"

// Raw runs restic on r's repository with r's flags and files, for the contract suite's own checks.
func Raw(ctx context.Context, r Repo, args ...string) ([]byte, error) { return r.run(ctx, args...) }
