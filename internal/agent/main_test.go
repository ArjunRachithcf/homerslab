package agent_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
)

// The agent reads compose files in children it starts from its own executable: here, this test binary. As
// HOMERSLAB_TEST_PARENT, the binary stands in for an agent that starts a read's child, says its pid, and waits to be
// killed (TestAReadsChildDiesWithTheAgent).
func TestMain(m *testing.M) {
	if os.Getenv("HOMERSLAB_TEST_PARENT") != "" {
		os.Exit(parent())
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
