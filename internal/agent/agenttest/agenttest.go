// Package agenttest runs a real agent for other packages' tests, on a socket in the test's own directory.
package agenttest

import (
	"context"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
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
	exe := Executable(t)
	go func() {
		done <- agent.Serve(ctx, agent.Config{Socket: sock, SocketGID: os.Getgid(), Sysroot: sysroot, Executable: exe})
	}()
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

// Main runs a test binary's tests, or, when the agent started it as a compose read's child, that read: the agent
// starts reads by running its own executable, which in a test is the test binary. Every package whose tests run an
// agent that reads calls it from TestMain.
func Main(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "agent" && os.Args[2] == "read-project" {
		os.Exit(agent.ReadProjectMain(os.Args[3:], os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}

var (
	copied     sync.Once
	copiedPath string
	copiedErr  error
)

// Executable is the program an agent in a test runs as a compose read's child: this test binary. Run as root, the
// child starts as nobody, who can't reach a test binary in Go's private build folder, so the binary is copied once
// to a folder everyone may read.
func Executable(t testing.TB) string {
	t.Helper()
	if os.Geteuid() != 0 {
		return os.Args[0]
	}
	copied.Do(func() {
		dir, err := os.MkdirTemp("", "homerslab-test-exe-")
		if err == nil {
			err = os.Chmod(dir, 0o755)
		}
		var b []byte
		if err == nil {
			b, err = os.ReadFile(os.Args[0])
		}
		if err == nil {
			copiedPath = filepath.Join(dir, filepath.Base(os.Args[0]))
			err = os.WriteFile(copiedPath, b, 0o755)
		}
		copiedErr = err
	})
	if copiedErr != nil {
		t.Fatal(copiedErr)
	}
	return copiedPath
}
