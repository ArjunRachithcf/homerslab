package agent_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
)

// The agent reads compose files in children it starts from its own executable: here, this test binary. As
// HOMERSLAB_TEST_PARENT, the binary stands in for an agent that starts a read's child, says its pid, and waits to be
// killed (TestAReadsChildDiesWithTheAgent). As HOMERSLAB_TEST_PEEK, it stands in for another process of a read's own
// user, the child's parent even, that tries to read its memory and to trace it (TestAReadsChildCantBeTraced).
func TestMain(m *testing.M) {
	if os.Getenv("HOMERSLAB_TEST_PARENT") != "" {
		os.Exit(parent())
	}
	// Never in a read's child, which peek starts from this binary: that would start another, and so on.
	if os.Getenv("HOMERSLAB_TEST_PEEK") != "" && (len(os.Args) < 2 || os.Args[1] != "agent") {
		os.Exit(peek())
	}
	// Read children that misbehave, confined as the real one is: one burns CPU, one allocates until it can't.
	if len(os.Args) > 2 && os.Args[1] == "agent" {
		switch os.Args[2] {
		case "read-project-burn":
			os.Exit(agent.ReadProjectWith(os.Args[3:], os.Stdin, os.Stdout, func(context.Context, string, []byte) (engine.Project, error) {
				for n := 0; ; n++ {
					burnt = n
				}
			}))
		case "read-project-hold": // says it is reading, then waits
			os.Exit(agent.ReadProjectWith(os.Args[3:], os.Stdin, os.Stdout, func(context.Context, string, []byte) (engine.Project, error) {
				fmt.Fprintln(os.Stderr, "reading")
				time.Sleep(time.Hour)
				return engine.Project{}, nil
			}))
		case "read-project-hog":
			os.Exit(agent.ReadProjectWith(os.Args[3:], os.Stdin, os.Stdout, func(context.Context, string, []byte) (engine.Project, error) {
				for {
					b := make([]byte, 16<<20)
					for i := range b {
						b[i] = 1
					}
					hogged = append(hogged, b)
				}
			}))
		}
	}
	agenttest.Main(m)
}

var (
	burnt  int
	hogged [][]byte
)

func parent() int {
	dir, err := agent.ChildDir()
	if err != nil {
		return 1
	}
	cmd, control, err := agent.ChildCommand(context.Background(), os.Getenv("HOMERSLAB_TEST_EXE"), dir, "homerslab-web", 60)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cmd.Stdin = os.Stdin // the test's pipe, held open beyond this process: the child waits for its file
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(cmd.Process.Pid)
	time.Sleep(time.Hour)      // until killed
	runtime.KeepAlive(control) // held open until then: its finalizer would close it
	return 0
}

// peek starts a read's child of its own, waits until the child reads its file, then tries to open the child's memory
// and to attach to it with ptrace, and says how each went: "ok", "denied" or the error.
func peek() int {
	child := exec.Command(os.Args[0], "agent", "read-project-hold", "--deadline", "1m", "--", "homerslab-web")
	child.Stdin = strings.NewReader("services: {web: {image: x}}\n")
	child.Env = []string{"PATH=/usr/bin:/bin"} // as the agent gives a read's child; not HOMERSLAB_TEST_PEEK
	stderr, err := child.StderrPipe()
	if err == nil {
		err = child.Start()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	if line, err := bufio.NewReader(stderr).ReadString('\n'); err != nil || line != "reading\n" {
		fmt.Fprintf(os.Stderr, "the child didn't read: %q %v\n", line, err)
		return 1
	}
	said := func(err error) string {
		switch {
		case err == nil:
			return "ok"
		case errors.Is(err, os.ErrPermission):
			return "denied"
		}
		return err.Error()
	}
	var res struct{ Mem, Trace string }
	mem, err := os.Open(fmt.Sprintf("/proc/%d/mem", child.Process.Pid))
	if err == nil {
		mem.Close()
	}
	res.Mem = said(err)
	runtime.LockOSThread() // ptrace binds the thread that attaches
	res.Trace = said(syscall.PtraceAttach(child.Process.Pid))
	if err := json.NewEncoder(os.Stdout).Encode(res); err != nil {
		return 1
	}
	return 0
}
