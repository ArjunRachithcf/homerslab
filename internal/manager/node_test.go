package manager_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/node"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// The manager's first resource is the primary as its agent reports it: the same JSON, on the control socket and
// to the setup page.
func TestTheManagerServesThePrimaryAsItsAgentReportsIt(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	facts, err := node.Read(agenttest.Primary())
	if err != nil {
		t.Fatal(err)
	}
	var want any
	b, _ := json.Marshal(facts)
	_ = json.Unmarshal(b, &want)

	for name, r := range map[string]response{
		"control socket": p.control("GET", "/api/v1/nodes/primary", nil),
		"setup page":     p.claimed().do("GET", "/api/v1/nodes/primary", nil),
	} {
		if r.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, r.Code, r.Body)
		}
		var got any
		r.json(t, &got)
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		if string(g) != string(w) {
			t.Errorf("%s:\n got %s\nwant %s", name, g, w)
		}
	}
}

func TestTheManagerSaysWhenTheAgentIsSilent(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t, withAgent(filepath.Join(t.TempDir(), "nobody.sock")))
	if r := p.control("GET", "/api/v1/nodes/primary", nil); r.Code != http.StatusServiceUnavailable || r.errorCode(t) != "agent-unreachable" {
		t.Errorf("with no agent: %d %s, want 503 agent-unreachable", r.Code, r.Body)
	}
}
