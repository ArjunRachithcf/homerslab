// Package host is how the agent runs the machine's own tools (nft, systemctl, netbird, systemd-creds): one seam, so
// tests run them in network namespaces, in containers, or not at all.
package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Cmd is one command: a program on PATH, its arguments, extra environment (NAME=value) and what it reads on stdin.
// Secrets go in Env or Stdin, never in Args, which every user of the machine can read.
type Cmd struct {
	Name  string
	Args  []string
	Env   []string
	Stdin []byte
}

func (c Cmd) String() string { return strings.TrimSpace(c.Name + " " + strings.Join(c.Args, " ")) }

// Runner runs a command and returns what it wrote on stdout.
type Runner func(ctx context.Context, c Cmd) ([]byte, error)

// Exec runs commands on this machine. Its errors carry the command and the first line of stderr, never the
// environment or stdin.
func Exec(ctx context.Context, c Cmd) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Env = append(cmd.Environ(), c.Env...)
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		first, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		e := &Error{Cmd: c.String(), Err: err, Stderr: first}
		if ee := (*exec.ExitError)(nil); errors.As(err, &ee) && ee.Exited() {
			e.Exit = ee.ExitCode()
		}
		return out, e
	}
	return out, nil
}

// Error is a command that failed.
type Error struct {
	Cmd    string
	Err    error
	Stderr string
	// Exit is the command's exit status when it ran and exited with one; 0 when it never ran, or was killed.
	Exit int
}

func (e *Error) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s: %v: %s", e.Cmd, e.Err, e.Stderr)
	}
	return fmt.Sprintf("%s: %v", e.Cmd, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }
