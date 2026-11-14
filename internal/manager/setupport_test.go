package manager_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/agent/agenttest"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// The manager's setup port is its own (its --listen): it gives it to the agent, which keeps it for the next boot's
// closed firewall table (homerslab firewall restore), so a primary rebooted before its first join still lets the LAN
// reach first-time setup. An agent not up yet is asked again at the next run.
func TestTheManagerGivesTheAgentItsSetupPort(t *testing.T) {
	testenv.Isolate(t)
	state := t.TempDir()
	sock := filepath.Join(t.TempDir(), "agent.sock")
	p := newPrimary(t, withAgent(sock), func(p *primary) { p.port = 7444 })
	p.m.RunDue(context.Background()) // the agent isn't up yet
	agenttest.StartWith(t, agent.Config{Socket: sock, StateDir: state})
	if _, ok := agent.SavedSetupPort(state); ok {
		t.Fatal("the agent knew a port before the manager gave it")
	}
	p.m.RunDue(context.Background())
	if port, ok := agent.SavedSetupPort(state); !ok || port != 7444 {
		t.Errorf("the agent keeps %d %v for the next boot, want the manager's 7444", port, ok)
	}
}

// Every run gives it again: an agent that restarted without its state gets it back without the manager restarting.
func TestTheManagerGivesItsSetupPortAtEveryRun(t *testing.T) {
	testenv.Isolate(t)
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var given []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/v1/firewall/setup-listener" {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			given = append(given, string(b))
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
			return
		}
		http.NotFound(w, r)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	p := newPrimary(t, withAgent(sock), func(p *primary) { p.port = 7444 })
	p.m.RunDue(context.Background())
	p.m.RunDue(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(given) != 2 || given[1] != `{"open":true,"port":7444}` {
		t.Errorf("the agent was given %q, want the port at each run", given)
	}
}

// An agent that lost its state after the listener closed for good learns it again at the next run.
func TestALostAgentLearnsTheListenerIsClosedAtTheNextRun(t *testing.T) {
	testenv.Isolate(t)
	sock := filepath.Join(t.TempDir(), "agent.sock")
	agenttest.StartWith(t, agent.Config{Socket: sock, StateDir: t.TempDir()})
	p := newPrimary(t, withAgent(sock), func(p *primary) { p.port = 7444 })
	if err := p.m.CloseLANListener(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The agent restarts without its state: its new socket takes over the path (the old one gone first, so the
	// wait below is for the new agent).
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	lost := t.TempDir()
	agenttest.StartWith(t, agent.Config{Socket: sock, StateDir: lost})
	if port, ok := agent.SavedSetupPort(lost); ok {
		t.Fatalf("a fresh agent already says %d", port)
	}
	p.m.RunDue(context.Background())
	if port, ok := agent.SavedSetupPort(lost); !ok || port != 0 {
		t.Errorf("after the next run the agent keeps %d %v, want the listener closed for good (no port)", port, ok)
	}
}
