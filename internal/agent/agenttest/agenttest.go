// Package agenttest runs a real agent for other packages' tests, on a socket in the test's own directory.
package agenttest

import (
	"context"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/node"
)

// Primary is the system root of the invented primary in testdata/primary, with 412 GB free of 1 TB for apps' data.
func Primary() agent.Sysroot {
	_, self, _, _ := runtime.Caller(0)
	return WithSpace(os.DirFS(filepath.Join(filepath.Dir(self), "..", "..", "..", "testdata", "primary")), 412e9, 1e12)
}

// WithSpace is a system root whose filesystem for apps' data has that much space.
func WithSpace(root fs.FS, free, size uint64) agent.Sysroot {
	return space{root, node.Space{FreeBytes: free, SizeBytes: size}}
}

type space struct {
	fs.FS
	space node.Space
}

func (s space) Statfs(string) (node.Space, error) { return s.space, nil }

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
