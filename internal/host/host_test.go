package host_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/host"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

func TestACommandsExitStatusIsKept(t *testing.T) {
	testenv.Isolate(t)
	_, err := host.Exec(context.Background(), host.Cmd{Name: "sh", Args: []string{"-c", "echo no >&2; exit 3"}})
	var he *host.Error
	if !errors.As(err, &he) || he.Exit != 3 || he.Stderr != "no" {
		t.Errorf("%#v", err)
	}
	_, err = host.Exec(context.Background(), host.Cmd{Name: "/nonexistent/tool"})
	if !errors.As(err, &he) || he.Exit != 0 {
		t.Errorf("a command that never ran: %#v", err)
	}
}
