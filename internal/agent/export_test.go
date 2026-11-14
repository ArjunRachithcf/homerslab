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

// HostileReads gives reads d, and runs every read's child as this test binary, a real Go child confined as the
// agent's, burning CPU instead of reading; its CPU limit is far past d, so only the deadline can end it in time.
func HostileReads(t *testing.T, exe string, d time.Duration) (real func()) {
	return childReads(t, exe, "read-project-burn", d, 30)
}

// ChildCommand is the command the agent runs for one read, from the executable exe, in dir, and the agent's ends of
// its control pipe: the child ends when they close.
func ChildCommand(ctx context.Context, exe, dir, name string, cpuSeconds int) (*exec.Cmd, io.Closer, error) {
	argv, _ := readCommand(exe)
	cred, err := readCredential()
	if err != nil {
		return nil, nil, err
	}
	return childCommand(ctx, argv, dir, name, cpuSeconds, cred)
}

// ChildDir is a read's own folder, as the agent makes it.
func ChildDir() (string, error) {
	cred, err := readCredential()
	if err != nil {
		return "", err
	}
	return childDir(cred)
}

// RaceDetector says whether the tests run under the race detector, which the child's memory limit leaves alone.
const RaceDetector = raceDetector

// CgoOn says whether the tests are built with cgo.
const CgoOn = cgoOn

// ChildReads gives reads d and a soft CPU limit of cpu seconds, and runs every read's child as this test binary in
// the given mode (main_test.go): a real Go child that confines itself as the agent's does, then does what the mode
// says instead of reading.
func ChildReads(t *testing.T, exe, mode string, d time.Duration, cpu int) {
	childReads(t, exe, mode, d, cpu)
}

func childReads(t *testing.T, exe, mode string, d time.Duration, cpu int) (real func()) {
	oldDeadline, oldCommand, oldCPU := readDeadline, readCommand, readCPUSeconds
	readDeadline, readCPUSeconds = d, func() int { return cpu }
	readCommand = func(string) ([]string, error) { return []string{exe, "agent", mode}, nil }
	restore := func() { readDeadline, readCommand, readCPUSeconds = oldDeadline, oldCommand, oldCPU }
	t.Cleanup(restore)
	return restore
}

// ReadProjectWith is the read child's main with read in place of the compose reader.
func ReadProjectWith(args []string, stdin io.Reader, stdout io.Writer, read func(ctx context.Context, name string, file []byte) (engine.Project, error)) int {
	return readProjectMain(args, stdin, stdout, read)
}

// ReadIDs is the uid and gid of the user the agent's reads run as when it is root (homerslab-read).
func ReadIDs() (uid, gid uint32, err error) { return readIDs() }

// ReadIDsFrom is ReadIDs, with file in place of /etc/passwd.
func ReadIDsFrom(file string) (uid, gid uint32, err error) { return readIDsFrom(file) }

// ReadUsersFrom makes the agent find its read user in file in place of /etc/passwd.
func ReadUsersFrom(t *testing.T, file string) {
	old := passwdFile
	passwdFile = file
	t.Cleanup(func() { passwdFile = old })
}
