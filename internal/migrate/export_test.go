package migrate

import (
	"io/fs"
	"testing"
)

// WithFail makes h's run fail at the step fail says, before that step changes anything: a crash there.
func WithFail(h Host, fail func(step string) error) Host {
	h.fail = fail
	return h
}

// SetLstat gives Pending another lstat for the test.
func SetLstat(t *testing.T, f func(string) (fs.FileInfo, error)) {
	old := lstat
	lstat = f
	t.Cleanup(func() { lstat = old })
}
