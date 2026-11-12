package lan

import (
	"errors"
	"fmt"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// A dump the kernel marks interrupted (NLM_F_DUMP_INTR: the table changed while it was read) may have missed or
// doubled entries: it is refused, and ReadKernel asks again.
func TestAnInterruptedDumpIsRefused(t *testing.T) {
	testenv.Isolate(t)
	whole := []syscall.NetlinkMessage{{Header: syscall.NlMsghdr{Type: syscall.RTM_NEWROUTE}}, {Header: syscall.NlMsghdr{Type: syscall.RTM_NEWROUTE}}}
	if err := complete(whole); err != nil {
		t.Errorf("a whole dump: %v", err)
	}
	torn := append(whole, syscall.NetlinkMessage{Header: syscall.NlMsghdr{Type: syscall.RTM_NEWROUTE, Flags: unix.NLM_F_DUMP_INTR}})
	if err := complete(torn); !errors.Is(err, errInterrupted) {
		t.Errorf("an interrupted dump: %v, want errInterrupted", err)
	}
}

// ReadKernel asks again when a dump fails or is interrupted, five times in all, and no more: it answers the first
// whole reading, or, after the fifth failure, that failure (#148). It never answers a reading that failed.
func TestTheKernelIsAskedFiveTimesAtMost(t *testing.T) {
	testenv.Isolate(t)
	good := Kernel{Links: []Link{{Index: 1, Name: "eth0"}}}
	for _, c := range []struct {
		fails, calls int
		ok           bool
	}{{0, 1, true}, {1, 2, true}, {4, 5, true}, {5, 5, false}, {9, 5, false}} {
		calls := 0
		k, err := retried(func() (Kernel, error) {
			calls++
			if calls <= c.fails {
				return Kernel{Links: []Link{{Index: 9, Name: "half-read"}}}, fmt.Errorf("try %d: %w", calls, errInterrupted)
			}
			return good, nil
		})
		if calls != c.calls {
			t.Errorf("%d failures: asked %d times, want %d", c.fails, calls, c.calls)
		}
		switch {
		case c.ok && (err != nil || len(k.Links) != 1 || k.Links[0].Name != "eth0"):
			t.Errorf("%d failures: %+v, %v; want the whole reading", c.fails, k, err)
		case !c.ok && (!errors.Is(err, errInterrupted) || err.Error() != "try 5: "+errInterrupted.Error() || len(k.Links) != 0):
			t.Errorf("%d failures: %+v, %v; want no reading and the fifth failure", c.fails, k, err)
		}
	}
}
