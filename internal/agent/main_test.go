package agent_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
)

// The agent reads compose files in children it starts from its own executable: here, this test binary. As
// HOMERSLAB_TEST_PARENT, the binary stands in for an agent that starts a read's child, says its pid, and waits to be
// killed (TestAReadsChildDiesWithTheAgent).
func TestMain(m *testing.M) {
	if os.Getenv("HOMERSLAB_TEST_PARENT") != "" {
		os.Exit(parent())
	}
	agenttest.Main(m)
}

func parent() int {
	dir, err := agent.ChildDir()
	if err != nil {
		return 1
	}
	cmd := agent.ChildCommand(context.Background(), os.Getenv("HOMERSLAB_TEST_EXE"), dir, "homerslab-web", 60)
	cmd.Stdin = os.Stdin // the test's pipe, held open beyond this process: the child waits for its file
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(cmd.Process.Pid)
	time.Sleep(time.Hour) // until killed
	return 0
}
