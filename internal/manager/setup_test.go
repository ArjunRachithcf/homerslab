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
	b := newPrimary(t).claimed()
	b.firstThree()
	if r := b.answer("signin", map[string]string{"idp": "authentik"}); r.Code != http.StatusNotImplemented || r.errorCode(t) != "not-built" {
		t.Errorf("signin: %d %s, want 501 not-built", r.Code, r.Body)
	}
	if s := b.state(); s.Current != "signin" {
		t.Errorf("setup moved on to %q", s.Current)
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
