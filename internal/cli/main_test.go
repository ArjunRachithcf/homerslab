package cli_test

import (
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
)

// The agent reads compose files in children it starts from its own executable: here, this test binary.
func TestMain(m *testing.M) { agenttest.Main(m) }
