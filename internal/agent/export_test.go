package agent

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// ShellReads gives reads d, and runs every read's child as script under sh. It returns a function that puts the
// real child and deadline back.
func ShellReads(t *testing.T, d time.Duration, script string) (real func()) {
	oldDeadline, oldCommand := readDeadline, readCommand
	readDeadline = d
	readCommand = func(string) ([]string, error) { return []string{"/bin/sh", "-c", script, "sh"}, nil }
	t.Cleanup(func() { readDeadline, readCommand = oldDeadline, oldCommand })
	return func() { readDeadline, readCommand = oldDeadline, oldCommand }
}

// HostileReads gives reads d, and makes every read's child burn CPU until it is killed.
func HostileReads(t *testing.T, d time.Duration) (real func()) {
	return ShellReads(t, d, "cat >/dev/null; while :; do :; done")
}

// ChildCommand is the command the agent runs for one read, from the executable exe, in dir.
func ChildCommand(ctx context.Context, exe, dir, name string, cpuSeconds int) *exec.Cmd {
	argv, _ := readCommand(exe)
	return childCommand(ctx, argv, dir, name, cpuSeconds)
}

// ChildDir is a read's own folder, as the agent makes it.
func ChildDir() (string, error) { return childDir() }
