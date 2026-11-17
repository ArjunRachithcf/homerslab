//go:build !linux

package manager

import (
	"crypto/rand"
	"time"
)

// elapsed is the time since the manager started, by Go's monotonic clock: where there is no CLOCK_BOOTTIME, a
// suspend doesn't count.
func elapsed() func() time.Duration {
	start := time.Now()
	return func() time.Duration { return time.Since(start) }
}

// bootID is new for every process here, as elapsed starts over with each: a restart ends every link and session.
func bootID() string { return "process-" + rand.Text() }
