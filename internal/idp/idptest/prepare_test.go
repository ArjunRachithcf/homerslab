package idptest

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/idp"
	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

// The contract prepares the sign-in as the manager does (#254): a provider that isn't ready yet, as Authentik is while
// its worker applies its own blueprints again after a restart, is asked again until a deadline. One that never gets
// ready still fails, and any other error fails at once.
func TestTheContractWaitsForAProviderThatIsNotReadyYet(t *testing.T) {
	testenv.Isolate(t)
	const wait, poll = 300 * time.Millisecond, 10 * time.Millisecond
	notReady := fmt.Errorf("%w: its blueprint default/flow-oobe.yaml is error", idp.ErrNotReady)

	t.Run("ready on a later try", func(t *testing.T) {
		tries := 0
		err := untilReady(func() error {
			if tries++; tries < 3 {
				return notReady
			}
			return nil
		}, wait, poll)
		if err != nil || tries != 3 {
			t.Errorf("%v after %d tries, want ready on the third", err, tries)
		}
	})

	t.Run("never ready fails at the deadline", func(t *testing.T) {
		start := time.Now()
		err := untilReady(func() error { return notReady }, wait, poll)
		if !errors.Is(err, idp.ErrNotReady) {
			t.Errorf("%v, want not ready", err)
		}
		if time.Since(start) < wait {
			t.Errorf("gave up after %v, before the deadline", time.Since(start))
		}
	})

	t.Run("another error fails at once", func(t *testing.T) {
		tries := 0
		refused := errors.New("the household's sign-in moved")
		err := untilReady(func() error { tries++; return refused }, wait, poll)
		if !errors.Is(err, refused) || tries != 1 {
			t.Errorf("%v after %d tries, want refused on the first", err, tries)
		}
	})
}
