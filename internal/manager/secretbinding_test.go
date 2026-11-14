package manager_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// controlState is setup as the control socket shows it to `homerslab setup secret`.
type controlState struct {
	Revision int64 `json:"revision"`
	Steps    []struct {
		ID, State, Secret, Refused string
	} `json:"steps"`
	Answers map[string]string `json:"answers"`
}

func (p *primary) controlState() controlState {
	p.t.Helper()
	r := p.control("GET", "/api/v1/setup", nil)
	if r.Code != http.StatusOK {
		p.t.Fatalf("GET /api/v1/setup on the control socket: %d %s", r.Code, r.Body)
	}
	var s controlState
	r.json(p.t, &s)
	return s
}

// secretAt gives a secret as shown at a revision of setup's answers, as the CLI does; revision < 0 sends none.
func (p *primary) secretAt(name, value string, revision int64) response {
	req := newRequest("PUT", "/api/v1/setup/secrets/"+name, map[string]string{"value": value}, "@")
	if revision >= 0 {
		req.Header.Set("If-Match", fmt.Sprintf(`"%d"`, revision))
	}
	return serve(p.m.Control(), req)
}

// The terminal shows where a secret goes and which device gave the answers that decide it, then sends the secret bound
// to the answers it showed (#64): if the LAN page changed them in between, the secret is refused and goes nowhere.
func TestASecretIsBoundToTheAnswersTheTerminalShowed(t *testing.T) {
	testenv.Isolate(t)
	nb := &fakeNetBird{}
	p, b := atNetwork(t, nb)
	b.mustAnswer("network", map[string]string{"mesh": "netbird", "controlPlane": "cloud"})

	shown := p.controlState()
	if from, at := shown.Answers["answeredFrom:network"], shown.Answers["answeredAt:network"]; from != onTheLAN || at != p.clock.Now().Format(time.RFC3339) {
		t.Errorf("the network step was answered from %q at %q, want %s at %v", from, at, onTheLAN, p.clock.Now())
	}
	// Who answered is the step's own: a new link, claimed from elsewhere, doesn't make the answers that device's.
	p.clock.Add(time.Minute)
	admin := p.browser(thisMachine)
	if r := admin.claim(p.newLink()); r.Code != http.StatusOK {
		t.Fatalf("a new link: %d %s", r.Code, r.Body)
	}
	if from := p.controlState().Answers["answeredFrom:network"]; from != onTheLAN {
		t.Errorf("after a new claim, the network step was answered from %q, want still %s", from, onTheLAN)
	}
	b = admin
	shown = p.controlState()

	if r := p.secretAt("mesh", givenToken, -1); r.Code != http.StatusPreconditionRequired || r.errorCode(t) != "revision-required" {
		t.Errorf("a secret without the revision it was shown at: %d %s, want 428 revision-required", r.Code, r.Body)
	}
	// Meanwhile, the LAN page sends the mesh somewhere else.
	b.mustAnswer("network", map[string]string{"mesh": "netbird", "controlPlane": "self-hosted", "url": "https://netbird.stranger.example"})
	if r := p.secretAt("mesh", givenToken, shown.Revision); r.Code != http.StatusConflict || r.errorCode(t) != "stale" {
		t.Errorf("a secret shown other answers: %d %s, want 409 stale", r.Code, r.Body)
	}
	// So does a change to an earlier step the terminal showed: the household's name.
	now := p.controlState()
	b.mustAnswer("household", map[string]string{"name": "The Stranger household"})
	if r := p.secretAt("mesh", givenToken, now.Revision); r.Code != http.StatusConflict || r.errorCode(t) != "stale" {
		t.Errorf("a secret shown another household: %d %s, want 409 stale", r.Code, r.Body)
	}
	if len(nb.adopted) != 0 || len(nb.bootstrapped) != 0 {
		t.Fatalf("a stale secret reached NetBird: adopted %d, bootstrapped %d", len(nb.adopted), len(nb.bootstrapped))
	}

	b.mustAnswer("network", map[string]string{"mesh": "netbird", "controlPlane": "cloud"})
	if r := p.secretAt("mesh", givenToken, p.controlState().Revision); r.Code != http.StatusOK {
		t.Errorf("a secret at the answers' revision: %d %s", r.Code, r.Body)
	}
	assertNoSecretAt(t, p, givenToken)
}

// The page shows a secret's step live: waiting, then checking as the secret arrives, then done, received and checked;
// or waiting again, with what was wrong (never the secret) (#64).
func TestThePageSeesTheSecretArriveAndBeChecked(t *testing.T) {
	testenv.Isolate(t)
	nb := &fakeNetBird{}
	p, b := atNetwork(t, nb)
	b.mustAnswer("network", map[string]string{"mesh": "netbird", "controlPlane": "cloud"})
	step := func() (state, refused string) {
		for _, s := range p.controlState().Steps {
			if s.ID == "network" {
				return s.State, s.Refused
			}
		}
		return "", ""
	}
	if st, why := step(); st != "waiting" || why != "" {
		t.Fatalf("before the secret: %s %q", st, why)
	}

	if r := p.secret("mesh", wrongToken); r.Code != http.StatusBadRequest {
		t.Fatalf("a wrong token: %d %s", r.Code, r.Body)
	}
	if st, why := step(); st != "waiting" || why != "secret-refused" {
		t.Errorf("after a wrong token: %s %q, want waiting, secret-refused", st, why)
	}
	if r := b.do("GET", "/api/v1/setup", nil); !strings.Contains(string(r.Body), `"refused":"secret-refused"`) || strings.Contains(string(r.Body), wrongToken) {
		t.Errorf("the page's state after a wrong token: %s", r.Body)
	}

	seen := make(chan string, 1)
	nb.beforeAdopt = func() {
		r := b.do("GET", "/api/v1/setup", nil)
		var s state
		r.json(t, &s)
		seen <- stepState(s, "network")
	}
	if r := p.secret("mesh", givenToken); r.Code != http.StatusOK {
		t.Fatalf("the token: %d %s", r.Code, r.Body)
	}
	if got := <-seen; got != "checking" {
		t.Errorf("while the token is checked the page sees the step %q, want checking", got)
	}
	if st, why := step(); st != "done" || why != "" {
		t.Errorf("after the token: %s %q, want done, nothing refused", st, why)
	}
}
