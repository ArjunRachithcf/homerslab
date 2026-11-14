package manager_test

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/home"
	"github.com/ArjunRachithcf/homerslab/internal/idp"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

const (
	homeOrigin   = "https://home.example.test:9443"
	homeSecret   = "ak-HOME-CLIENT-SECRET-PLACEHOLDER-00000000000"
	homeIssuer   = signInOrigin + "/application/o/homerslab-home/"
	homeClientID = "home-client"
)

func readProjection(t *testing.T, p *primary) home.Projection {
	t.Helper()
	pr, err := home.Read(p.projection)
	if err != nil {
		t.Fatalf("Home's projection: %v", err)
	}
	return pr
}

// Home signs people in with the household account through a client of its own (#21): `setup join` makes it, for
// every role, with the sign-in's usual flow, and seals its secret for Home's unit; the edge serves Home's name.
func TestSettingUpTheHouseholdAccountMakesHomesClient(t *testing.T) {
	testenv.Isolate(t)
	p, fs := installable(t, nil)
	want := idp.Client{Slug: "homerslab-home", Name: "Home", RedirectURIs: []string{homeOrigin + "/auth/callback"},
		Roles: []idp.Role{idp.Admin, idp.Adult, idp.Kid}}
	if len(fs.clients) != 2 || !reflect.DeepEqual(fs.clients[1], want) {
		t.Errorf("the clients made: %+v, want Admin's and %+v", fs.clients, want)
	}
	var got struct{ Value string }
	if r := serveAgent(t, p.agent, "GET", "/v1/credentials/manager-home-client-secret"); r.Code != http.StatusOK {
		t.Errorf("Home's client secret isn't sealed: %d %s", r.Code, r.Body)
	} else if r.json(t, &got); got.Value != homeSecret {
		t.Errorf("Home's client secret, sealed: another value")
	}
	var edge agent.EdgeStatus
	serveAgent(t, p.agent, "GET", "/v1/edge").json(t, &edge)
	if edge.Home != "home.example.test" || edge.Admin != "admin.example.test" || edge.SignIn != "auth.example.test" {
		t.Errorf("the edge's sites: %+v", edge.EdgeSites)
	}
}

// The manager keeps Home's projection (#21): the household's apps, each with its audience as roles and its health as
// the agent last found it, Home's client of the sign-in, and nothing secret. Home reads only that file.
func TestTheManagerKeepsHomesProjection(t *testing.T) {
	testenv.Isolate(t)
	p, _ := installable(t, nil)
	pr := readProjection(t, p)
	if pr.Home != homeOrigin || pr.Admin != adminOrigin || pr.Household != "The Ferreira household" || len(pr.Apps) != 0 ||
		pr.SignIn == nil || *pr.SignIn != (home.SignIn{Issuer: homeIssuer, ClientID: homeClientID, Upstream: p.signInUpstream}) {
		t.Errorf("before any app: %+v %+v", pr, pr.SignIn)
	}

	made := planFor(t, p, "bentopdf")
	if r := p.control("POST", "/api/v1/plans/"+made.ID+"/apply", nil); r.Code != http.StatusAccepted {
		t.Fatalf("applying: %d %s", r.Code, r.Body)
	}
	pr = readProjection(t, p)
	if len(pr.Apps) != 1 {
		t.Fatalf("after an install: %+v", pr.Apps)
	}
	a := pr.Apps[0]
	if a.ID != "bentopdf" || a.Name != "BentoPDF" || a.URL != "https://pdf.example.test:9443" || a.Category != "documents" || a.Purpose == "" ||
		!reflect.DeepEqual(a.Audience, []string{"admin", "adult"}) || a.Health != "healthy" || a.Icon == nil || len(a.Icon.Data) == 0 ||
		!strings.HasPrefix(a.Icon.Type, "image/") {
		t.Errorf("BentoPDF in Home's projection: %+v", a)
	}

	// The health Home shows follows the agent's, at the next refresh.
	p.host.Sick("homerslab-bentopdf", "unhealthy")
	p.host.Answer("172.31.240.2:8080", "Internal Server Error")
	if err := p.m.RefreshHome(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pr = readProjection(t, p); pr.Apps[0].Health != "unhealthy" {
		t.Errorf("after the app fell ill: %s", pr.Apps[0].Health)
	}

	// Nothing secret, and readable by Home's group only.
	b, err := os.ReadFile(p.projection)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{homeSecret, clientSecret, ownToken} {
		if strings.Contains(string(b), s) {
			t.Error("Home's projection holds a secret")
		}
	}
	if fi, err := os.Stat(p.projection); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("the projection's mode: %v %v", fi.Mode(), err)
	}
}

// One app whose plan can't be read doesn't freeze Home: the others' health still reaches it.
func TestOneUnreadablePlanLeavesTheRestOfHome(t *testing.T) {
	testenv.Isolate(t)
	p, _ := installable(t, nil)
	made := planFor(t, p, "bentopdf")
	if r := p.control("POST", "/api/v1/plans/"+made.ID+"/apply", nil); r.Code != http.StatusAccepted {
		t.Fatalf("applying: %d %s", r.Code, r.Body)
	}
	db, err := sql.Open("sqlite", p.db)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO plan (id, app, body, made_at) SELECT 'p-broken', 'aardvark', '{not json', made_at FROM plan LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO app (id, plan_id, applied_at) VALUES ('aardvark', 'p-broken', 0)`); err != nil {
		t.Fatal(err)
	}
	p.host.Sick("homerslab-bentopdf", "unhealthy")
	p.host.Answer("172.31.240.2:8080", "Internal Server Error")
	if err := p.m.RefreshHome(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pr := readProjection(t, p); len(pr.Apps) != 1 || pr.Apps[0].ID != "bentopdf" || pr.Apps[0].Health != "unhealthy" {
		t.Errorf("Home's apps: %+v", pr.Apps)
	}
}
