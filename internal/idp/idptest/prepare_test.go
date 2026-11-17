package idptest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/idp"
	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

// The contract prepares the sign-in as the manager does (#254): a provider that isn't ready yet, as Authentik is while
// its worker applies its own blueprints again after a restart, is asked again until a deadline. One that never gets
// ready still fails, any other error fails at once, and so does a context that ends (#259).
func TestTheContractWaitsForAProviderThatIsNotReadyYet(t *testing.T) {
	testenv.Isolate(t)
	const wait, poll = 300 * time.Millisecond, 10 * time.Millisecond
	notReady := fmt.Errorf("%w: its blueprint default/flow-oobe.yaml is error", idp.ErrNotReady)

	t.Run("ready on a later try", func(t *testing.T) {
		tries := 0
		err := untilReady(context.Background(), func() error {
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
		err := untilReady(context.Background(), func() error { return notReady }, wait, poll)
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
		err := untilReady(context.Background(), func() error { tries++; return refused }, wait, poll)
		if !errors.Is(err, refused) || tries != 1 {
			t.Errorf("%v after %d tries, want refused on the first", err, tries)
		}
	})

	t.Run("a context that ends stops the wait at once", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		tries := 0
		start := time.Now()
		err := untilReady(ctx, func() error {
			if tries++; tries == 2 {
				cancel()
			}
			return notReady
		}, 2*time.Second, poll)
		if !errors.Is(err, context.Canceled) || !errors.Is(err, idp.ErrNotReady) {
			t.Errorf("%v, want the context's end and the last not ready", err)
		}
		if time.Since(start) >= time.Second || tries != 2 {
			t.Errorf("gave up after %v and %d tries, want at once after the second", time.Since(start), tries)
		}
	})
}
