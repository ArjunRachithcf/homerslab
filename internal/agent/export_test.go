package agent

import (
	"context"
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/engine"
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

// RaceDetector says whether the tests run under the race detector, which the child's memory limit leaves alone.
const RaceDetector = raceDetector

// CgoOn says whether the tests are built with cgo.
const CgoOn = cgoOn

// ChildReads gives reads d and a soft CPU limit of cpu seconds, and runs every read's child as this test binary in
// the given mode (main_test.go): a real Go child that confines itself as the agent's does, then does what the mode
// says instead of reading.
func ChildReads(t *testing.T, exe, mode string, d time.Duration, cpu int) {
	oldDeadline, oldCommand, oldCPU := readDeadline, readCommand, readCPUSeconds
	readDeadline, readCPUSeconds = d, func() int { return cpu }
	readCommand = func(string) ([]string, error) { return []string{exe, "agent", mode}, nil }
	t.Cleanup(func() { readDeadline, readCommand, readCPUSeconds = oldDeadline, oldCommand, oldCPU })
}

// ReadProjectWith is the read child's main with read in place of the compose reader.
func ReadProjectWith(args []string, stdin io.Reader, stdout io.Writer, read func(ctx context.Context, name string, file []byte) (engine.Project, error)) int {
	return readProjectMain(args, stdin, stdout, read)
}
