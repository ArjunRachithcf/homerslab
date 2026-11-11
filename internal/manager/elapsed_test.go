package manager_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/manager"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// A link's and a session's age count the time the machine was suspended too: the manager's clock is the time since
// boot, as /proc/uptime says, not Go's monotonic clock, which stops while the machine sleeps.
func TestTheManagersClockCountsSuspendedTime(t *testing.T) {
	testenv.Isolate(t)
	if runtime.GOOS != "linux" {
		t.Skip("CLOCK_BOOTTIME is Linux's")
	}
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		t.Fatal(err)
	}
	f, _, _ := strings.Cut(string(b), " ")
	up, err := strconv.ParseFloat(f, 64)
	if err != nil {
		t.Fatal(err)
	}
	m, err := manager.New(manager.Config{DB: filepath.Join(t.TempDir(), "manager.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	got := m.Elapsed()
	if d := got - time.Duration(up*float64(time.Second)); d < -time.Second || d > time.Second {
		t.Errorf("the manager's clock says %s, /proc/uptime %.2fs: want the time since boot", got, up)
	}
}
