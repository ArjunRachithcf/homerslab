package manager_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// #110: a duplicated tab holds the same session, so a stale tab could overwrite what a newer one answered. A step is
// answered from the revision its page last saw, and a done step only as an explicit edit.

var ines = map[string]string{"name": "Inês Ferreira", "mail": "ines@ferreira.family"}

func TestAnAnswerCarriesTheStepsRevision(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	for name, header := range map[string]string{"none": "", "unquoted": "0", "negative": `"-1"`, "words": `"latest"`} {
		r := b.do("PUT", "/api/v1/setup/steps/welcome", map[string]string{}, func(r *http.Request) {
			if header != "" {
				r.Header.Set("If-Match", header)
			}
		})
		want, code := http.StatusBadRequest, "bad-revision"
		if name == "none" {
			want, code = http.StatusPreconditionRequired, "revision-required"
		}
		if r.Code != want || r.errorCode(t) != code {
			t.Errorf("%s revision: %d %s, want %d %s", name, r.Code, r.Body, want, code)
		}
	}
	if s := b.state(); s.Current != "welcome" {
		t.Errorf("an answer without a usable revision moved setup to %q", s.Current)
	}
}

func TestAStepsRevisionMovesWithEveryChange(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	b.mustAnswer("welcome", nil)
	r0 := b.revision("you")
	b.mustAnswer("you", ines)
	r1 := b.revision("you")
	if r1 <= r0 {
		t.Fatalf("answering moved the revision from %d to %d", r0, r1)
	}
	if r := b.put("you", ines, r1, true); r.Code != http.StatusOK {
		t.Fatalf("an edit: %d %s", r.Code, r.Body)
	}
	if r2 := b.revision("you"); r2 <= r1 {
		t.Errorf("an edit moved the revision from %d to %d", r1, r2)
	}
	if got := b.revision("household"); got != 0 {
		t.Errorf("a step nobody changed is at revision %d", got)
	}
}

func TestADoneStepChangesOnlyAsAnExplicitEdit(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	b.firstThree()
	again := map[string]string{"name": "Someone Else", "mail": "else@example.org"}
	rev := b.revision("you")
	r := b.put("you", again, rev, false)
	if r.Code != http.StatusConflict || r.errorCode(t) != "step-done" {
		t.Errorf("a plain answer to a done step: %d %s, want 409 step-done", r.Code, r.Body)
	}
	if s := b.state(); s.Answers["name"] != "Inês Ferreira" || b.revision("you") != rev {
		t.Errorf("a refused answer changed the step: %v at revision %d", s.Answers, b.revision("you"))
	}
	if r := b.put("you", again, rev, true); r.Code != http.StatusOK {
		t.Fatalf("an edit: %d %s", r.Code, r.Body)
	}
	if s := b.state(); s.Answers["name"] != "Someone Else" {
		t.Errorf("an edit didn't change the answer: %v", s.Answers)
	}
}

func TestAStaleRevisionIsRefusedAndChangesNothing(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	b.firstThree()
	seen := b.revision("you") // both tabs saw this
	if r := b.put("you", map[string]string{"name": "Newer Tab", "mail": "newer@example.org"}, seen, true); r.Code != http.StatusOK {
		t.Fatalf("the newer tab's edit: %d %s", r.Code, r.Body)
	}
	for _, edit := range []bool{true, false} { // an edit or not, a stale view is refused first
		r := b.put("you", map[string]string{"name": "Older Tab", "mail": "older@example.org"}, seen, edit)
		if r.Code != http.StatusConflict || r.errorCode(t) != "stale" {
			t.Errorf("the older tab's answer (edit %v): %d %s, want 409 stale", edit, r.Code, r.Body)
		}
	}
	if s := b.state(); s.Answers["name"] != "Newer Tab" || s.Answers["mail"] != "newer@example.org" {
		t.Errorf("a stale tab overwrote the newer answers: %v", s.Answers)
	}
}

func TestATabThatSawAStepOpenCantAnswerItOnceAnotherDid(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	b.mustAnswer("welcome", nil)
	opened := b.revision("you") // tab one and its duplicate both show "you" as open
	b.mustAnswer("you", ines)
	r := b.put("you", map[string]string{"name": "Duplicate", "mail": "dup@example.org"}, opened, false)
	if r.Code != http.StatusConflict || r.errorCode(t) != "stale" {
		t.Errorf("the duplicate's first answer: %d %s, want 409 stale", r.Code, r.Body)
	}
	if s := b.state(); s.Answers["name"] != "Inês Ferreira" {
		t.Errorf("the duplicate overwrote the answer: %v", s.Answers)
	}
}

// Two tabs answer from one revision at the same moment: one is kept, one is refused, whichever the manager meets first,
// even when both pass the manager's early check (the handler of the step holds both until each has begun).
func TestOfTwoAnswersFromOneRevisionOnlyOneIsKept(t *testing.T) {
	testenv.Isolate(t)
	var in sync.WaitGroup
	in.Add(2)
	gate := func(_ context.Context, raw json.RawMessage, _ map[string]string) (map[string]string, error) {
		in.Done()
		done := make(chan struct{})
		go func() { in.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		var a struct{ Name string }
		_ = json.Unmarshal(raw, &a)
		return map[string]string{"household": a.Name}, nil
	}
	p := newPrimary(t, func(p *primary) { p.steps["household"] = gate })
	b := p.claimed()
	b.mustAnswer("welcome", nil)
	b.mustAnswer("you", ines)
	rev := b.revision("household")
	codes := make(chan int, 2)
	for _, name := range []string{"The First household", "The Second household"} {
		go func() { codes <- b.put("household", map[string]string{"Name": name}, rev, false).Code }()
	}
	a, c := <-codes, <-codes
	if !(a == http.StatusOK && c == http.StatusConflict || a == http.StatusConflict && c == http.StatusOK) {
		t.Errorf("two answers from one revision: %d and %d, want one 200 and one 409", a, c)
	}
	if got := b.revision("household"); got != rev+1 {
		t.Errorf("both answers moved the revision: %d, want %d", got, rev+1)
	}
}

// A stale answer never reaches the step's handler, which may do something outside the database.
func TestAStaleAnswerNeverReachesTheStepsHandler(t *testing.T) {
	testenv.Isolate(t)
	calls := 0
	p := newPrimary(t, func(p *primary) {
		p.steps["household"] = func(context.Context, json.RawMessage, map[string]string) (map[string]string, error) {
			calls++
			return map[string]string{"household": "x"}, nil
		}
	})
	b := p.claimed()
	b.mustAnswer("welcome", nil)
	b.mustAnswer("you", ines)
	seen := b.revision("household")
	b.mustAnswer("household", map[string]string{})
	before := calls
	if r := b.put("household", map[string]string{}, seen, true); r.Code != http.StatusConflict {
		t.Fatalf("stale: %d %s", r.Code, r.Body)
	}
	if calls != before {
		t.Errorf("the handler ran for a stale answer")
	}
}
