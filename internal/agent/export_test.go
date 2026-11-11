package agent

import (
	"testing"
	"time"
)

// HostileReads gives reads d, and makes every read's child burn CPU until it is killed. It returns a function that
// puts the real child and deadline back.
func HostileReads(t *testing.T, d time.Duration) (real func()) {
	oldDeadline, oldCommand := readDeadline, readCommand
	readDeadline = d
	readCommand = func() ([]string, error) {
		return []string{"/bin/sh", "-c", "cat >/dev/null; while :; do :; done", "sh"}, nil
	}
	t.Cleanup(func() { readDeadline, readCommand = oldDeadline, oldCommand })
	return func() { readDeadline, readCommand = oldDeadline, oldCommand }
}
