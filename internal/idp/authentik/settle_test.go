package authentik_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

// The contract's look at Authentik's blueprints once Bootstrap is done (#254) waits out a run of Authentik's own
// that failed after Bootstrap had seen it succeed, applying it again as the adapter does, but still fails on one that
// stays in error past its deadline, and at once on one Bootstrap didn't wait for or one that ends in warning (#259).
func TestTheBlueprintCheckWaitsOutAFailedRunButNotOneThatStaysFailed(t *testing.T) {
	testenv.Isolate(t)
	const wait, every = 300 * time.Millisecond, 20 * time.Millisecond

	t.Run("one in error that applies again is settled, and was applied again", func(t *testing.T) {
		status := "error"
		var applied []string
		list := func() []blueprint {
			return []blueprint{{PK: "1", Path: "default/flow-oobe.yaml", Status: status}, {PK: "2", Path: "system/bootstrap.yaml", Status: "successful"}}
		}
		apply := func(pk string) { applied = append(applied, pk); status = "successful" }
		if left := settle(list, apply, wait, every); len(left) != 0 {
			t.Errorf("left: %v, want none", left)
		}
		if !slices.Equal(applied, []string{"1"}) {
			t.Errorf("applied: %v, want the failed one once", applied)
		}
	})

	t.Run("one that stays in error past the deadline still fails", func(t *testing.T) {
		applies := 0
		list := func() []blueprint {
			return []blueprint{{PK: "1", Path: "default/flow-oobe.yaml", Status: "error"}}
		}
		start := time.Now()
		left := settle(list, func(string) { applies++ }, wait, every)
		if len(left) != 1 || !strings.Contains(left[0], "default/flow-oobe.yaml") || !strings.Contains(left[0], "error") {
			t.Errorf("left: %v, want the failed one", left)
		}
		if time.Since(start) < wait {
			t.Errorf("gave up after %v, before the deadline", time.Since(start))
		}
		if applies < 2 {
			t.Errorf("applied again %d times, want it retried while waiting", applies)
		}
	})

	t.Run("one Bootstrap didn't wait for fails at once, without waiting", func(t *testing.T) {
		applies := 0
		list := func() []blueprint {
			return []blueprint{{PK: "1", Path: "default/flow-default-authentication-flow.yaml", Status: "unknown"}}
		}
		start := time.Now()
		left := settle(list, func(string) { applies++ }, wait, every)
		if len(left) != 1 || !strings.Contains(left[0], "unknown") {
			t.Errorf("left: %v, want the unapplied one", left)
		}
		if time.Since(start) >= wait || applies != 0 {
			t.Errorf("waited %v and applied %d times, want neither", time.Since(start), applies)
		}
	})

	t.Run("one that ends in warning after it was applied again fails at once, without waiting", func(t *testing.T) {
		status := "error"
		list := func() []blueprint {
			return []blueprint{{PK: "1", Path: "default/flow-oobe.yaml", Status: status}}
		}
		start := time.Now()
		left := settle(list, func(string) { status = "warning" }, wait, every)
		if len(left) != 1 || !strings.Contains(left[0], "default/flow-oobe.yaml: warning") {
			t.Errorf("left: %v, want the one in warning", left)
		}
		if time.Since(start) >= wait {
			t.Errorf("waited %v, want it to fail at once", time.Since(start))
		}
	})

	t.Run("orphaned ones and others' blueprints don't count", func(t *testing.T) {
		list := func() []blueprint {
			return []blueprint{{PK: "1", Path: "default/gone.yaml", Status: "orphaned"}, {PK: "2", Path: "example/x.yaml", Status: "error"}}
		}
		if left := settle(list, func(string) { t.Error("applied one that doesn't count") }, wait, every); len(left) != 0 {
			t.Errorf("left: %v, want none", left)
		}
	})
}
