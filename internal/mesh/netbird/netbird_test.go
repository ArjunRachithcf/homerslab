package netbird_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/mesh"
	"github.com/ArjunRachithcf/homerslab/internal/mesh/netbird"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// A token goes only to the address the admin gave: a redirect elsewhere is an answer, never followed.
func TestATokenIsNeverSentWhereARedirectPoints(t *testing.T) {
	testenv.Isolate(t)
	var reached atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		_, _ = w.Write([]byte(`{"setup_required":true}`))
	}))
	defer elsewhere.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	m, err := netbird.Open(redirect.URL, mesh.Token{Value: "nbp_SECRET", UserID: "u", ID: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Report(context.Background()); err == nil {
		t.Error("a redirect was taken for an answer")
	}
	if _, err := netbird.Adopt(context.Background(), redirect.URL, "nbp_SECRET"); err == nil {
		t.Error("adopting through a redirect worked")
	}
	if _, err := netbird.Probe(context.Background(), redirect.URL); err == nil {
		t.Error("probing through a redirect worked")
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("the redirect's target got %d requests", n)
	}
}

// A HomersLab policy that kept its ports but gained a port range beside them has drifted: the range alone widens it.
// (NetBird 0.82 refuses both together, the contract suite shows; this holds should one take them.)
func TestAPortRangeBesideThePortsIsDrift(t *testing.T) {
	testenv.Isolate(t)
	for _, withRange := range []bool{false, true} {
		var mu sync.Mutex
		var put []string
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/api/groups":
				var gs []map[string]string
				for _, n := range append([]string{mesh.NodesGroup}, roleGroups()...) {
					gs = append(gs, map[string]string{"id": n, "name": n})
				}
				_ = json.NewEncoder(w).Encode(gs)
			case r.Method == http.MethodGet && r.URL.Path == "/api/policies":
				var ps []map[string]any
				for _, p := range mesh.Policies(443) {
					ports := []string{}
					for _, n := range p.Ports {
						ports = append(ports, strconv.Itoa(n))
					}
					rule := map[string]any{"name": p.Name, "enabled": true, "action": "accept", "bidirectional": false,
						"protocol": "tcp", "ports": ports, "sources": []string{p.From}, "destinations": []string{p.To}, "port_ranges": []any{}}
					if withRange && p.Name == "homerslab-adults-to-nodes" {
						rule["port_ranges"] = []map[string]int{{"start": 1, "end": 65535}}
					}
					ps = append(ps, map[string]any{"id": "id-" + p.Name, "name": p.Name, "enabled": true,
						"source_posture_checks": []string{}, "rules": []any{rule}})
				}
				_ = json.NewEncoder(w).Encode(ps)
			case r.Method == http.MethodPut:
				mu.Lock()
				put = append(put, r.URL.Path)
				mu.Unlock()
				_, _ = w.Write([]byte(`{}`))
			default:
				http.Error(w, "not here", http.StatusNotFound)
			}
		}))
		m, err := netbird.Open(api.URL, mesh.Token{Value: "nbp_TEST", UserID: "u", ID: "t"})
		if err != nil {
			t.Fatal(err)
		}
		if err := m.EnsurePolicies(context.Background(), 443); err != nil {
			t.Fatal(err)
		}
		api.Close()
		want := []string(nil)
		if withRange {
			want = []string{"/api/policies/id-homerslab-adults-to-nodes"}
		}
		if !slices.Equal(put, want) {
			t.Errorf("with a range beside the ports %v: put back %v, want %v", withRange, put, want)
		}
	}
}

func roleGroups() []string {
	var out []string
	for _, r := range mesh.Roles {
		out = append(out, mesh.RoleGroup(r))
	}
	return out
}
