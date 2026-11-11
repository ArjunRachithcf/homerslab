package manager_test

import (
	"database/sql"
	"net/http"
	"reflect"
	"strings"
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

// Answers reach terminals, mail headers and other people's screens: they are bounded, carry no control or bidi
// characters, and are kept in NFC (#69).
func TestAnswersAreBounded(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	b.mustAnswer("welcome", nil)
	mailOf := func(n int) string { return strings.Repeat("a", n-len("@example.org")) + "@example.org" }
	for _, c := range []struct {
		step   string
		body   map[string]string
		reason map[string]string // nil: accepted
	}{
		{"you", map[string]string{"name": "Inês", "mail": mailOf(254)}, nil},
		{"you", map[string]string{"name": "Inês", "mail": mailOf(255)}, map[string]string{"mail": "too-long"}},
		{"you", map[string]string{"name": "Inês", "mail": strings.Repeat("a", 60000) + "@example.org"}, map[string]string{"mail": "too-long"}},
		{"you", map[string]string{"name": "Inês", "mail": "inês@example.org"}, nil},
		// RFC 5321 counts octets: 250 characters, but 290 octets.
		{"you", map[string]string{"name": "Inês", "mail": strings.Repeat("ê", 40) + mailOf(210)}, map[string]string{"mail": "too-long"}},
		{"you", map[string]string{"name": strings.Repeat("ê", 64), "mail": "ines@example.org"}, nil},
		{"you", map[string]string{"name": strings.Repeat("ê", 65), "mail": "ines@example.org"}, map[string]string{"name": "too-long"}},
		{"you", map[string]string{"name": "", "mail": ""}, map[string]string{"name": "missing", "mail": "missing"}},
		{"you", map[string]string{"name": "Inês", "mail": "not a mail address"}, map[string]string{"mail": "not-a-mail"}},
	} {
		r := b.answer(c.step, c.body)
		var e struct{ Reasons map[string]string }
		r.json(t, &e)
		switch {
		case c.reason == nil && r.Code != http.StatusOK:
			t.Errorf("%s %.80v: %d %.200s, want 200", c.step, c.body, r.Code, r.Body)
		case c.reason != nil && (r.Code != http.StatusBadRequest || !reflect.DeepEqual(e.Reasons, c.reason)):
			t.Errorf("%s %.80v: %d %.200s, want 400 %v", c.step, c.body, r.Code, r.Body, c.reason)
		}
	}
	if r := b.answer("household", map[string]string{"name": strings.Repeat("h", 65)}); r.Code != http.StatusBadRequest || !strings.Contains(string(r.Body), `"too-long"`) {
		t.Errorf("a household name of 65 characters: %d %s, want 400 too-long", r.Code, r.Body)
	}
}

func TestAnswersRefuseControlAndBidiCharacters(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	b.mustAnswer("welcome", nil)
	hidden := []string{"\x1b[2J\x1b[31m", "\x07", "\x00", "\x7f", "a\tb", "a\nb", "\u0085", "\u009b31m",
		"\u202a", "\u202b", "\u202c", "\u202d", "\u202e", "\u2066", "\u2067", "\u2068", "\u2069",
		"\u200e", "\u200f", "\u061c", "\u200b", "\u2060", "\ufeff", "\u2028", "\u2029"}
	for _, h := range hidden {
		r := b.answer("you", map[string]string{"name": "Inês" + h + "evil", "mail": "ines@example.org"})
		if r.Code != http.StatusBadRequest || !strings.Contains(string(r.Body), `"name":"hidden-characters"`) {
			t.Errorf("a name with %q: %d %s, want 400 hidden-characters", h, r.Code, r.Body)
		}
		r = b.answer("you", map[string]string{"name": "Inês", "mail": "ines" + h + "@example.org"})
		if r.Code != http.StatusBadRequest || !strings.Contains(string(r.Body), `"mail":`) {
			t.Errorf("a mail address with %q: %d %s, want 400", h, r.Code, r.Body)
		}
	}
	b.mustAnswer("you", map[string]string{"name": "Inês", "mail": "ines@example.org"})
	for _, h := range hidden {
		r := b.answer("household", map[string]string{"name": "The Ferreira" + h + " household"})
		if r.Code != http.StatusBadRequest || !strings.Contains(string(r.Body), `"name":"hidden-characters"`) {
			t.Errorf("a household name with %q: %d %s, want 400 hidden-characters", h, r.Code, r.Body)
		}
	}
	// Letters, marks and right-to-left scripts are names like any other, and so are the joiners Persian, Indic scripts
	// and emoji need.
	for _, name := range []string{"Zoë Ångström-Ñúñez", "مريم", "אסתר", "李小龍", "Đặng Thị",
		"خانه\u200cی ما", "The \U0001f468\u200d\U0001f469\u200d\U0001f467 household"} { // ZWNJ in Persian, ZWJ in an emoji
		b.mustAnswer("household", map[string]string{"name": name})
	}
}

func TestAnswersAreKeptInNFC(t *testing.T) {
	testenv.Isolate(t)
	b := newPrimary(t).claimed()
	b.mustAnswer("welcome", nil)
	b.mustAnswer("you", map[string]string{"name": "Ine\u0302s", "mail": "ines@example.org"}) // e and a combining circumflex
	s := b.mustAnswer("household", map[string]string{"name": "The Ine\u0302s household"})
	if s.Answers["name"] != "Inês" || s.Answers["household"] != "The Inês household" {
		t.Errorf("answers %q, want them in NFC (Inês)", s.Answers)
	}
	// 64 characters once composed, though more when typed decomposed.
	b.mustAnswer("household", map[string]string{"name": strings.Repeat("e\u0302", 64)})
}
