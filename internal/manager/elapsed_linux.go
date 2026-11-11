package manager

import (
	"time"

	"golang.org/x/sys/unix"
)

// elapsed is the time since boot (CLOCK_BOOTTIME): monotonic, never set, and counting while the machine is
// suspended. Go's own monotonic clock (CLOCK_MONOTONIC) stops then, so a link made before an overnight suspend would
// still be claimable in the morning.
func elapsed() func() time.Duration {
	return func() time.Duration {
		var ts unix.Timespec
		if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
			panic("CLOCK_BOOTTIME: " + err.Error()) // in every Linux since 2.6.39
		}
		return time.Duration(ts.Nano())
	}
}
