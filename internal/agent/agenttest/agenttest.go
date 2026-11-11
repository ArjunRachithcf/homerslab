// Package agenttest runs a real agent for other packages' tests, on a socket in the test's own directory.
package agenttest

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
)

// Primary is the system root of the invented primary in testdata/primary.
func Primary() agent.Sysroot {
	_, self, _, _ := runtime.Caller(0)
	return os.DirFS(filepath.Join(filepath.Dir(self), "..", "..", "..", "testdata", "primary"))
}

// Start runs an agent reading sysroot until the test ends, and returns its socket.
func Start(t testing.TB, sysroot agent.Sysroot) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agent.Serve(ctx, agent.Config{Socket: sock, SocketGID: os.Getgid(), Sysroot: sysroot}) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("agent stopped with %v", err)
		}
	})
	for range 200 {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			return sock
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the agent never listened on %s", sock)
	return ""
}
