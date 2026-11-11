package mesh_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/mesh"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

func TestAControlPlaneIsReachedOverHTTPSOnly(t *testing.T) {
	testenv.Isolate(t)
	for raw, want := range map[string]error{
		"https://api.netbird.io":              nil,
		"https://netbird.ferreira.family/":    nil,
		"https://netbird.example.org:8443":    nil,
		"http://127.0.0.1:33080":              nil, // this machine itself: a test's server
		"http://[::1]:33080":                  nil,
		"http://netbird.ferreira.family":      mesh.ErrInsecureURL,
		"http://192.168.1.20":                 mesh.ErrInsecureURL,
		"http://localhost:33080":              mesh.ErrInsecureURL, // a name can be anything; only a loopback address counts
		"ftp://netbird.example.org":           mesh.ErrInsecureURL,
		"netbird.example.org":                 mesh.ErrNotAMesh,
		"https://user:pw@netbird.example.org": mesh.ErrNotAMesh,
		"https://netbird.example.org/api":     mesh.ErrNotAMesh,
		"https://netbird.example.org/?x=1":    mesh.ErrNotAMesh,
		"":                                    mesh.ErrNotAMesh,
	} {
		if _, err := mesh.CheckURL(raw); !errors.Is(err, want) {
			t.Errorf("CheckURL(%q) = %v, want %v", raw, err, want)
		}
	}
}

func TestASecretNeverPrintsOrMarshals(t *testing.T) {
	testenv.Isolate(t)
	tok := mesh.Token{Value: "nbp_supersecret", UserID: "u1", ID: "t1"}
	for _, s := range []string{fmt.Sprint(tok), fmt.Sprintf("%v %+v %#v %s", tok, tok, tok, tok.Value)} {
		if strings.Contains(s, "supersecret") {
			t.Errorf("printed: %s", s)
		}
	}
	b, err := json.Marshal(tok)
	if err != nil || strings.Contains(string(b), "supersecret") {
		t.Errorf("a token marshals as %s, %v", b, err)
	}
	if _, err := json.Marshal(map[string]mesh.Secret{"v": "nbp_supersecret"}); err == nil {
		t.Error("a bare secret marshalled")
	}
	if tok.Value.Reveal() != "nbp_supersecret" {
		t.Error("Reveal doesn't give the secret")
	}
}

func TestEveryRoleHasItsPolicyAndNothingElseIsOpen(t *testing.T) {
	testenv.Isolate(t)
	for _, household := range []int{443, 9443} {
		ps := mesh.Policies(household)
		seen := map[string]bool{}
		for _, p := range ps {
			seen[p.From] = true
			for _, port := range p.Ports {
				if port != 80 && port != household {
					t.Errorf("policy %s opens port %d, with the household's port %d", p.Name, port, household)
				}
			}
			if !slices.Contains(p.Ports, household) {
				t.Errorf("policy %s doesn't open the household's port %d", p.Name, household)
			}
			if p.To != mesh.NodesGroup {
				t.Errorf("policy %s reaches %s, not the nodes", p.Name, p.To)
			}
		}
		for _, r := range mesh.Roles {
			if !seen[mesh.RoleGroup(r)] {
				t.Errorf("no policy for %s", r)
			}
		}
	}
}
