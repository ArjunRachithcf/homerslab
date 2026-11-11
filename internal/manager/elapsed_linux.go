package manager

import (
	"crypto/rand"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// elapsed is the time since boot (CLOCK_BOOTTIME): never set, going on across the manager's restarts, and counting
// while the machine is suspended. Go's own monotonic clock (CLOCK_MONOTONIC) stops then, so a link made before an
// overnight suspend would still be claimable in the morning.
func elapsed() func() time.Duration {
	return func() time.Duration {
		var ts unix.Timespec
		if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
			panic("CLOCK_BOOTTIME: " + err.Error()) // in every Linux since 2.6.39
		}
		return time.Duration(ts.Nano())
	}
}

// bootID is this boot's ID, which the kernel makes anew at each boot. If it can't be read, an ID of this process's
// own: then a restart ends every link and session, which fails closed.
func bootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if id := strings.TrimSpace(string(b)); err == nil && id != "" {
		return id
	}
	return "process-" + rand.Text()
}
