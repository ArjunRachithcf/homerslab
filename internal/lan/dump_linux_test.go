package lan

import (
	"errors"
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
