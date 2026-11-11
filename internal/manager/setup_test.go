package manager_test

import (
	"database/sql"
	"net/http"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

var (
	lanHalf  = []string{"welcome", "you", "household", "signin", "network", "domain", "mail", "kit"}
	meshHalf = []string{"join", "passkey", "done"}
)

func TestSetupHasTwoHalves(t *testing.T) {
	testenv.Isolate(t)
	s := newPrimary(t).claimed().state()
	var lan, mesh []string
	for _, st := range s.Steps {
		switch st.Half {
		case "lan":
			lan = append(lan, st.ID)
		case "mesh":
			mesh = append(mesh, st.ID)
		default:
			t.Errorf("step %s is in half %q", st.ID, st.Half)
		}
		if st.State != "todo" {
			t.Errorf("a fresh setup's step %s is %q", st.ID, st.State)
		}
	}
	if !reflect.DeepEqual(lan, lanHalf) || !reflect.DeepEqual(mesh, meshHalf) {
		t.Errorf("halves: lan %v, mesh %v", lan, mesh)
	}
	if s.Half != "lan" || s.Current != "welcome" {
		t.Errorf("a fresh setup is in half %q at %q, want lan at welcome", s.Half, s.Current)
	}
}

func TestTheLANHalfEndsWithMeshAndCertificateAndHandsOver(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t, withBuiltSteps("signin", "network", "domain", "mail", "kit"))
	b := p.claimed()
	b.firstThree()
	for _, id := range lanHalf[3:] {
		b.mustAnswer(id, nil)
	}
	s := b.state()
	if s.Half != "mesh" || s.Current != "join" {
		t.Fatalf("after the LAN half: half %q at %q, want mesh at join", s.Half, s.Current)
	}
	for _, id := range meshHalf {
		if r := b.answer(id, nil); r.Code != http.StatusForbidden || r.errorCode(t) != "over-the-mesh" {
			t.Errorf("PUT %s over the LAN: %d %s, want 403 over-the-mesh", id, r.Code, r.Body)
		}
	}
	if r := b.answer("you", map[string]string{"name": "Someone Else", "mail": "else@example.org"}); r.Code != http.StatusConflict || r.errorCode(t) != "lan-half-done" {
		t.Errorf("changing the LAN half after the hand-off: %d %s, want 409 lan-half-done", r.Code, r.Body)
	}
	if r := p.control("POST", "/api/v1/setup/links", nil); r.Code != http.StatusConflict || r.errorCode(t) != "lan-half-done" {
		t.Errorf("a new LAN link after the LAN half: %d %s, want 409 lan-half-done", r.Code, r.Body)
	}
}

func TestStepsAreTakenInOrder(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	if r := b.answer("household", map[string]string{"name": "The Ferreira household"}); r.Code != http.StatusConflict || r.errorCode(t) != "earlier-step-open" {
		t.Errorf("household before welcome: %d %s, want 409 earlier-step-open", r.Code, r.Body)
	}
	if r := b.answer("nonsense", nil); r.Code != http.StatusNotFound {
		t.Errorf("an unknown step: %d %s, want 404", r.Code, r.Body)
	}
}

func TestAStepNotBuiltYetSaysSo(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t, withBuiltSteps("network", "domain")).claimed()
	b.firstThree()
	b.mustAnswer("signin", map[string]string{"provider": "authentik"})
	b.mustAnswer("network", nil)
	b.mustAnswer("domain", nil)
	if r := b.answer("mail", map[string]string{"address": "x"}); r.Code != http.StatusNotImplemented || r.errorCode(t) != "not-built" {
		t.Errorf("mail: %d %s, want 501 not-built", r.Code, r.Body)
	}
	if s := b.state(); s.Current != "mail" {
		t.Errorf("setup moved on to %q", s.Current)
	}
}

// The identity provider is chosen in plain words on the LAN page. Authentik is the only one until Kanidm's ticket
// (#33): the page offers both, and the manager takes Authentik only.
func TestTheSignInStepTakesAuthentik(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	b.firstThree()
	for _, bad := range []map[string]string{{"provider": "kanidm"}, {"provider": ""}, {"idp": "authentik"}} {
		if r := b.answer("signin", bad); r.Code != http.StatusBadRequest {
			t.Errorf("signin %v: %d %s, want 400", bad, r.Code, r.Body)
		}
	}
	s := b.mustAnswer("signin", map[string]string{"provider": "authentik"})
	if stepState(s, "signin") != "done" || s.Answers["signInProvider"] != "authentik" || s.Current != "network" {
		t.Errorf("after signin: %+v", s)
	}
}

func TestAnswersAreChecked(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	b.mustAnswer("welcome", nil)
	for _, c := range []struct {
		body   map[string]string
		fields []string
	}{
		{map[string]string{"name": "Inês", "mail": "not a mail address"}, []string{"mail"}},
		{map[string]string{"name": "  ", "mail": "ines@ferreira.family"}, []string{"name"}},
		{map[string]string{"name": "Inês", "mail": "Inês <ines@ferreira.family>"}, []string{"mail"}},
		{map[string]string{}, []string{"name", "mail"}},
	} {
		r := b.answer("you", c.body)
		var e struct {
			Error  string
			Fields []string
		}
		r.json(t, &e)
		if r.Code != http.StatusBadRequest || e.Error != "invalid" || !reflect.DeepEqual(e.Fields, c.fields) {
			t.Errorf("you %v: %d %s, want 400 invalid %v", c.body, r.Code, r.Body, c.fields)
		}
	}
	if s := b.state(); s.Current != "you" {
		t.Errorf("a refused answer moved setup on to %q", s.Current)
	}
}

func TestAnEarlierStepCanBeChanged(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	b.firstThree()
	s := b.mustAnswer("you", map[string]string{"name": "Inês Ferreira", "mail": "ines@example.org"})
	if s.Answers["mail"] != "ines@example.org" || s.Current != "signin" {
		t.Errorf("after changing an earlier answer: %v at %q", s.Answers, s.Current)
	}
}

func TestSetupResumesWhereItStoppedAfterARestart(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	p.claimed().firstThree()

	p.restart()
	s := p.claimed().state() // a new link: the old session ended with the restart's new link
	if s.Current != "signin" || s.Half != "lan" {
		t.Errorf("after a restart, setup is at %q in %q, want signin in lan", s.Current, s.Half)
	}
	want := map[string]string{"name": "Inês Ferreira", "mail": "ines@ferreira.family", "household": "The Ferreira household"}
	if !reflect.DeepEqual(s.Answers, want) {
		t.Errorf("answers %v, want %v", s.Answers, want)
	}
	var left []string
	for _, st := range s.Steps {
		if st.State == "todo" {
			left = append(left, st.ID)
		}
	}
	if want := append(append([]string{}, lanHalf[3:]...), meshHalf...); !reflect.DeepEqual(left, want) {
		t.Errorf("what's left: %v, want %v", left, want)
	}
}

func TestSetupsStepsLiveInTheManagersDatabase(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	p.claimed().firstThree()
	if err := p.m.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", p.db)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, half, state FROM setup_step ORDER BY position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id, half, st string
		if err := rows.Scan(&id, &half, &st); err != nil {
			t.Fatal(err)
		}
		got = append(got, id+"/"+half+"/"+st)
	}
	want := []string{"welcome/lan/done", "you/lan/done", "household/lan/done", "signin/lan/todo", "network/lan/todo", "domain/lan/todo", "mail/lan/todo", "kit/lan/todo", "join/mesh/todo", "passkey/mesh/todo", "done/mesh/todo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("setup_step rows:\n got %v\nwant %v", got, want)
	}
}

func TestAStepGoneFromANewVersionLeavesTheDatabase(t *testing.T) {
	testenv.Isolate(t)
	p := newPrimary(t)
	p.claimed().firstThree()
	if err := p.m.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", p.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO setup_step (id, position, half) VALUES ('retired', 3, 'lan')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	p.open()
	s := p.claimed().state()
	if s.Current != "signin" || len(s.Steps) != len(lanHalf)+len(meshHalf) {
		t.Errorf("with an old step in the database: at %q with %d steps", s.Current, len(s.Steps))
	}
}

func TestAnAnswerWithAFieldTheStepDoesntHaveIsRefused(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	if r := b.answer("welcome", map[string]string{"role": "admin"}); r.Code != http.StatusBadRequest {
		t.Errorf("welcome with a field: %d %s, want 400", r.Code, r.Body)
	}
	b.mustAnswer("welcome", nil)
	r := b.answer("you", map[string]string{"name": "Inês", "mail": "ines@ferreira.family", "role": "admin"})
	var e struct {
		Error  string
		Fields []string
	}
	r.json(t, &e)
	if r.Code != http.StatusBadRequest || e.Error != "invalid" || !reflect.DeepEqual(e.Fields, []string{"answer"}) {
		t.Errorf("you with a field it doesn't have: %d %s, want 400 invalid answer", r.Code, r.Body)
	}
}

// The setup page's changes refuse cross-site requests too (#83): a change needs Sec-Fetch-Site: same-origin, or the
// header absent (the CLI, an older browser). "none", a request the person started by hand, never makes a change.
func TestSetupChangesRefuseCrossSiteRequests(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	for _, site := range []string{"same-site", "cross-site", "none"} {
		r := b.do("PUT", "/api/v1/setup/steps/welcome", map[string]string{}, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", site) })
		if r.Code != http.StatusForbidden || r.errorCode(t) != "cross-origin" {
			t.Errorf("Sec-Fetch-Site %s: %d %s, want 403", site, r.Code, r.Body)
		}
	}
	if r := b.do("PUT", "/api/v1/setup/steps/welcome", map[string]string{}, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") }); r.Code != http.StatusOK {
		t.Errorf("same-origin: %d %s", r.Code, r.Body)
	}
}
