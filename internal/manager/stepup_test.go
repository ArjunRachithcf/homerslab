package manager_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/idp"
	"github.com/ArjunRachithcf/homeward/internal/manager"
	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

// Step-up (#25): a risky change from Admin asks for the admin's passkey again, bound to that one change and that one
// Admin session, used once and for minutes. The tests go through Admin's API, with the sign-in stood in.

// withWritableStorage is a household whose BentoPDF plan writes to a storage location: a risky plan, but one the agent
// can apply (extras come later). The admin is signed in to Admin.
func withWritableStorage(t *testing.T) (*primary, *fakeSignIn, *admin, madePlan) {
	t.Helper()
	cat := withCatalog(t, map[string]func([]byte) []byte{"apps/bentopdf/app.yaml": func(b []byte) []byte {
		return append(b, []byte("storage:\n  - {id: library, what: The PDFs it keeps, service: web, path: /pdfs, access: read-write}\n")...)
	}})
	p, fs := installable(t, cat)
	if err := os.MkdirAll(filepath.Join(p.host.Root, "srv/documents"), 0o755); err != nil {
		t.Fatal(err)
	}
	p.control("POST", "/api/v1/storage", map[string]any{"id": "documents", "name": "Documents", "kind": "folder", "path": "/srv/documents"})
	r := p.control("POST", "/api/v1/plans", map[string]any{"app": "bentopdf", "storage": map[string]string{"library": "documents"}})
	var made madePlan
	r.json(t, &made)
	var risky struct{ Risky bool }
	r.json(t, &risky)
	if r.Code != http.StatusCreated || !risky.Risky {
		t.Fatalf("a plan writing to a location: %d %s", r.Code, r.Body)
	}
	fs.passkeys["ines"] = 1
	a := p.adminBrowser(t)
	if rec := a.signIn(fs, "ines", "homerslab-admins"); rec.Code != http.StatusSeeOther {
		t.Fatalf("signing in: %d %s", rec.Code, rec.Body)
	}
	return p, fs, a, made
}

// send is a change from Admin's page, with a JSON body.
func (a *admin) send(method, path string, body any, edit ...func(*http.Request)) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	return a.do(method, path, append([]func(*http.Request){sameOrigin, func(r *http.Request) {
		r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(b)), int64(len(b))
	}}, edit...)...)
}

// startStepUp asks for a step-up for action, as Admin's page does, and returns where the browser goes for the passkey.
// A setting's step-up also sends its change.
func (a *admin) startStepUp(action string, change ...any) string {
	a.t.Helper()
	body := map[string]any{"action": action}
	if len(change) > 0 {
		body["change"] = change[0]
	}
	rec := a.send("POST", "/api/v1/step-up", body)
	var out struct{ To string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); rec.Code != http.StatusOK || err != nil || out.To == "" {
		a.t.Fatalf("starting a step-up for %s: %d %s", action, rec.Code, rec.Body)
	}
	return out.To
}

// back brings the browser back from the sign-in with a code, and returns where Admin sends it then.
func (a *admin) back(state, code string) string {
	a.t.Helper()
	rec := a.do("GET", "/auth/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code))
	if rec.Code != http.StatusSeeOther {
		return rec.Result().Status
	}
	return rec.Header().Get("Location")
}

// stepUp is a step-up for action that the admin passes with their passkey; it returns where Admin sent the browser.
func (a *admin) stepUp(fs *fakeSignIn, action string, change ...any) string {
	a.t.Helper()
	state, code := fs.code(a.t, a.startStepUp(action, change...), "ines", "homerslab-admins")
	return a.back(state, code)
}

func (a *admin) apply(id string) *httptest.ResponseRecorder {
	return a.send("POST", "/api/v1/plans/"+id+"/apply", map[string]any{})
}

// A risky plan applies from Admin only after a step-up for it: the passkey asked again (max_age), a nonce of its own,
// and one apply. A step-up for another action approves nothing of this plan's, and the ID token of one step-up brought
// back for another is refused. Two step-ups may run at once in one browser (two tabs).
func TestARiskyPlanAppliesFromAdminOnlyAfterAStepUpForIt(t *testing.T) {
	testenv.Isolate(t)
	p, fs, a, made := withWritableStorage(t)
	if rec := a.apply(made.ID); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "step-up-required") {
		t.Errorf("applying without a step-up: %d %s", rec.Code, rec.Body)
	}
	to := a.startStepUp("plan:" + made.ID)
	u, _ := url.Parse(to)
	if !strings.HasPrefix(to, signInOrigin+"/application/o/authorize/") || u.Query().Get("max_age") != "5" || u.Query().Get("nonce") == "" ||
		u.Query().Get("redirect_uri") != adminOrigin+"/auth/callback" {
		t.Errorf("the step-up's authorize URL %s: want Admin's client, max_age=5 and a nonce", to)
	}
	// The plan's code, brought back with the state of another step-up begun in this browser (in another tab): its ID
	// token's nonce is the plan's step-up's, so it is refused, and that step-up is spent.
	_, code := fs.code(t, to, "ines", "homerslab-admins")
	other := a.startStepUp("backups/target", disk)
	ou, _ := url.Parse(other)
	if got := a.back(ou.Query().Get("state"), code); got != "/?step-up=refused" {
		t.Errorf("the plan's code, brought back with the target's step-up's state: %s", got)
	}
	// Two tabs: the target's step-up and then the plan's, each with its own code, both come back.
	target := a.startStepUp("backups/target", disk)
	to = a.startStepUp("plan:" + made.ID)
	state, code := fs.code(t, target, "ines", "homerslab-admins")
	if got := a.back(state, code); got != "/?step-up=done" {
		t.Errorf("the target's step-up, in the first tab: back to %s", got)
	}
	if rec := a.apply(made.ID); rec.Code != http.StatusForbidden {
		t.Errorf("applying the plan after a step-up for the backup target: %d %s", rec.Code, rec.Body)
	}
	if len(agentApps(t, p).Apps) != 0 {
		t.Fatal("installed without the plan's own step-up")
	}
	state, code = fs.code(t, to, "ines", "homerslab-admins")
	if got := a.back(state, code); got != "/plans/"+made.ID+"?step-up=done" {
		t.Errorf("the plan's step-up, in the second tab: back to %s", got)
	}
	if rec := a.apply(made.ID); rec.Code != http.StatusAccepted {
		t.Fatalf("applying after its step-up: %d %s", rec.Code, rec.Body)
	}
	if len(agentApps(t, p).Apps) != 1 {
		t.Error("applied, and the agent runs no app")
	}
	if rec := a.apply(made.ID); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "step-up-required") {
		t.Errorf("applying again with the same step-up: %d %s", rec.Code, rec.Body)
	}
}

// What a step-up approves: risky plans and risky settings, nothing else, and only for a signed-in admin.
func TestAStepUpIsOnlyForARiskyChange(t *testing.T) {
	testenv.Isolate(t)
	p, _, a, _ := withWritableStorage(t)
	plain := planFor(t, p, "mealie")
	for _, action := range []string{"plan:" + plain.ID, "plan:no-such-plan", "plan:", "backups", "anything", "backups/target:0123"} {
		if rec := a.send("POST", "/api/v1/step-up", map[string]string{"action": action}); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("a step-up for %q: %d %s", action, rec.Code, rec.Body)
		}
	}
	if rec := a.send("POST", "/api/v1/step-up", map[string]any{"action": "backups/target"}); rec.Code != http.StatusBadRequest {
		t.Errorf("a step-up for the backup target without the change: %d %s", rec.Code, rec.Body)
	}
	signedOut := p.adminBrowser(t)
	if rec := signedOut.send("POST", "/api/v1/step-up", map[string]string{"action": "backups/target"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("a step-up, signed out: %d %s", rec.Code, rec.Body)
	}
	cross := a.send("POST", "/api/v1/step-up", map[string]string{"action": "backups/target"}, func(r *http.Request) {
		r.Header.Set("Origin", "https://photos.example.test")
		r.Header.Set("Sec-Fetch-Site", "same-site")
	})
	if cross.Code != http.StatusForbidden {
		t.Errorf("a step-up asked from another site: %d %s", cross.Code, cross.Body)
	}
}

// A step-up belongs to the Admin session that asked for it: a code another browser brings back with the step-up's
// cookie opens nothing, and a grant is never another session's, even the same admin's on another device.
func TestAStepUpIsBoundToItsSession(t *testing.T) {
	testenv.Isolate(t)
	p, fs, laptop, made := withWritableStorage(t)
	phone := p.adminBrowser(t)
	if rec := phone.signIn(fs, "ines", "homerslab-admins"); rec.Code != http.StatusSeeOther {
		t.Fatal(rec.Code)
	}
	to := laptop.startStepUp("plan:" + made.ID)
	stolen := laptop.cookies["__Host-homerslab-step-up"]
	phone.cookies[stolen.Name] = stolen
	state, code := fs.code(t, to, "ines", "homerslab-admins")
	if got := phone.back(state, code); got != "/plans/"+made.ID+"?step-up=refused" {
		t.Errorf("another session brings the step-up's code back: %s", got)
	}
	if rec := phone.apply(made.ID); rec.Code != http.StatusForbidden {
		t.Errorf("the other session applies: %d %s", rec.Code, rec.Body)
	}
	if got := laptop.stepUp(fs, "plan:"+made.ID); !strings.HasSuffix(got, "step-up=done") {
		t.Fatalf("the laptop's step-up: %s", got)
	}
	if rec := phone.apply(made.ID); rec.Code != http.StatusForbidden {
		t.Errorf("the phone applies on the laptop's step-up: %d %s", rec.Code, rec.Body)
	}
	if rec := laptop.apply(made.ID); rec.Code != http.StatusAccepted {
		t.Errorf("the laptop applies on its own step-up: %d %s", rec.Code, rec.Body)
	}
}

// The step-up's sign-in must be a new one, by the session's own admin: one the sign-in passed on its session from
// before the step-up began asks to try again; another person's, or a non-admin's, is refused.
func TestAStepUpNeedsANewSignInByTheSessionsAdmin(t *testing.T) {
	testenv.Isolate(t)
	p, fs, a, made := withWritableStorage(t)
	fs.people["joao"] = idp.Person{Username: "joao", Role: idp.Admin}
	fs.passkeys["joao"] = 1
	action := "plan:" + made.ID
	to := a.startStepUp(action)
	state, code := fs.codeAt(t, to, p.clock.Now().Add(-2*time.Second), "ines", "homerslab-admins")
	if got := a.back(state, code); got != "/plans/"+made.ID+"?step-up=too-soon" {
		t.Errorf("a sign-in from before the step-up: %s", got)
	}
	// A page that asks for a step-up the moment the admin signed in gets that sign-in's own time back, within the
	// second the step-up began: not a passkey asked for this change.
	state, code = fs.codeAt(t, a.startStepUp(action), p.clock.Now(), "ines", "homerslab-admins")
	if got := a.back(state, code); got != "/plans/"+made.ID+"?step-up=too-soon" {
		t.Errorf("a sign-in within the second the step-up began: %s", got)
	}
	for who, groups := range map[string][]string{"joao": {"homerslab-admins"}, "ines": {"homerslab-adults"}} {
		state, code := fs.code(t, a.startStepUp(action), who, groups...)
		if got := a.back(state, code); got != "/plans/"+made.ID+"?step-up=refused" {
			t.Errorf("%s in %v steps up for ines's session: %s", who, groups, got)
		}
	}
	if rec := a.apply(made.ID); rec.Code != http.StatusForbidden {
		t.Errorf("applying after refused step-ups: %d %s", rec.Code, rec.Body)
	}
	// Taking too long: the attempt ends with attemptLife.
	to = a.startStepUp(action)
	p.clock.Add(11 * time.Minute)
	state, code = fs.code(t, to, "ines", "homerslab-admins")
	if got := a.back(state, code); got != "/plans/"+made.ID+"?step-up=refused" {
		t.Errorf("a step-up brought back after eleven minutes: %s", got)
	}
}

// An admin's step-up is a passkey only when passkeys are all they hold (#84): with a password or a second factor on
// their account as well, the provider's list says so, the step-up is refused, and the admins hear of it.
func TestAStepUpIsRefusedWhenTheAdminHoldsMoreThanPasskeys(t *testing.T) {
	testenv.Isolate(t)
	p, fs, a, made := withWritableStorage(t)
	fs.others["ines"] = []string{idp.CredPassword, idp.CredTOTP}
	before := fs.credentials
	if got := a.stepUp(fs, "plan:"+made.ID); got != "/plans/"+made.ID+"?step-up=passkeys-only" {
		t.Errorf("with a password and TOTP besides the passkey: %s", got)
	}
	if fs.credentials == before {
		t.Error("the admin's credentials weren't asked of the provider")
	}
	if rec := a.apply(made.ID); rec.Code != http.StatusForbidden {
		t.Errorf("applying: %d %s", rec.Code, rec.Body)
	}
	r := p.control("GET", "/api/v1/warnings", nil)
	if !strings.Contains(string(r.Body), "security:step-up:ines") || !strings.Contains(string(r.Body), "password, totp") {
		t.Errorf("no security event: %s", r.Body)
	}
	fs.others["ines"], fs.passkeys["ines"] = nil, 0
	if got := a.stepUp(fs, "plan:"+made.ID); got != "/plans/"+made.ID+"?step-up=passkeys-only" {
		t.Errorf("with no passkey at all: %s", got)
	}
	fs.passkeys["ines"] = 1
	if got := a.stepUp(fs, "plan:"+made.ID); got != "/plans/"+made.ID+"?step-up=done" {
		t.Errorf("with the passkey alone again: %s", got)
	}
}

// A step-up approves its action for StepUpLife from the passkey's assertion, and only while the person is still an
// admin when it is applied.
func TestAStepUpLastsMinutesAndOnlyForAnAdmin(t *testing.T) {
	testenv.Isolate(t)
	p, fs, a, made := withWritableStorage(t)
	if manager.StepUpLife != 5*time.Minute {
		t.Errorf("StepUpLife %v, want 5 minutes", manager.StepUpLife)
	}
	if got := a.stepUp(fs, "plan:"+made.ID); !strings.HasSuffix(got, "step-up=done") {
		t.Fatalf("the step-up: %s", got)
	}
	p.clock.Add(manager.StepUpLife + time.Second)
	if rec := a.apply(made.ID); rec.Code != http.StatusForbidden {
		t.Errorf("applying %v after the step-up: %d %s", manager.StepUpLife+time.Second, rec.Code, rec.Body)
	}
	if got := a.stepUp(fs, "plan:"+made.ID); !strings.HasSuffix(got, "step-up=done") {
		t.Fatalf("the second step-up: %s", got)
	}
	ines := fs.people["ines"]
	ines.Role = idp.Adult
	fs.people["ines"] = ines
	if rec := a.apply(made.ID); rec.Code != http.StatusForbidden {
		t.Errorf("applying after the admin became an adult: %d %s", rec.Code, rec.Body)
	}
	if len(agentApps(t, p).Apps) != 0 {
		t.Error("installed")
	}
}

// No token can make a risky change: Admin takes no token in place of a session, so a request carrying one and no
// session is signed out, whatever the token is. The terminal, root on the primary, needs no step-up.
func TestNoTokenMakesARiskyChange(t *testing.T) {
	testenv.Isolate(t)
	p, _, _, made := withWritableStorage(t)
	token := p.adminBrowser(t)
	bearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer homerslab-TOKEN-PLACEHOLDER") }
	for _, c := range []struct{ method, path string }{{"POST", "/api/v1/plans/" + made.ID + "/apply"}, {"POST", "/api/v1/step-up"},
		{"PUT", "/api/v1/backups/target"}} {
		if rec := token.send(c.method, c.path, map[string]string{"action": "backups/target"}, bearer); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with a token: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	if r := p.control("POST", "/api/v1/plans/"+made.ID+"/apply", nil); r.Code != http.StatusAccepted {
		t.Errorf("the terminal applies the risky plan: %d %s", r.Code, r.Body)
	}
}

// disk is a backup target on a disk of the primary, as Admin's page sends it.
var disk = map[string]any{"target": map[string]string{"kind": "disk", "path": "/srv/backups"}}

// Changing the backup target from Admin goes through the same step-up, which approves that change exactly (the target
// and its credentials), once: a step-up for one target never sends backups to another.
func TestChangingTheBackupTargetFromAdminTakesAStepUp(t *testing.T) {
	testenv.Isolate(t)
	_, fs, a, _ := withWritableStorage(t)
	target := disk
	if rec := a.send("PUT", "/api/v1/backups/target", target); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "step-up-required") {
		t.Errorf("without a step-up: %d %s", rec.Code, rec.Body)
	}
	if got := a.stepUp(fs, "backups/target", target); got != "/?step-up=done" {
		t.Fatalf("the step-up: %s", got)
	}
	elsewhere := map[string]any{"target": map[string]string{"kind": "disk", "path": "/srv/elsewhere"}}
	if rec := a.send("PUT", "/api/v1/backups/target", elsewhere); rec.Code != http.StatusForbidden {
		t.Errorf("another target on the step-up for the disk: %d %s", rec.Code, rec.Body)
	}
	// The same change, its keys in another order, is the change the step-up approved.
	same := map[string]any{"target": map[string]string{"path": "/srv/backups", "kind": "disk"}}
	if rec := a.send("PUT", "/api/v1/backups/target", same); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/srv/backups/primary") {
		t.Errorf("with a step-up: %d %s", rec.Code, rec.Body)
	}
	if rec := a.send("PUT", "/api/v1/backups/target", target); rec.Code != http.StatusForbidden {
		t.Errorf("again on the same step-up: %d %s", rec.Code, rec.Body)
	}
}

// The backup target's step-up approves exactly the change it was asked for: the JSON is decoded strictly on both sides
// (a key twice, in any case, an unknown key, a number the target can't hold exactly, all refused), and the digest is
// of what was decoded. Go's own decoder would take {"target":A,"Target":B} as B and {"Target":B,"target":A} as A.
func TestTheBackupTargetsStepUpApprovesThatExactChange(t *testing.T) {
	testenv.Isolate(t)
	_, fs, a, _ := withWritableStorage(t)
	both := []string{`{"target":{"kind":"disk","path":"/srv/backups"},"Target":{"kind":"disk","path":"/srv/elsewhere"}}`,
		`{"Target":{"kind":"disk","path":"/srv/elsewhere"},"target":{"kind":"disk","path":"/srv/backups"}}`,
		`{"target":{"kind":"disk","path":"/srv/backups"},"target":{"kind":"disk","path":"/srv/elsewhere"}}`,
		`{"target":{"kind":"disk","path":"/srv/backups","Path":"/srv/elsewhere"}}`,
		`{"target":{"kind":"disk","path":"/srv/backups"},"extra":1}`}
	for _, raw := range both {
		if rec := a.send("POST", "/api/v1/step-up", map[string]any{"action": "backups/target", "change": json.RawMessage(raw)}); rec.Code != http.StatusBadRequest {
			t.Errorf("a step-up for %s: %d %s", raw, rec.Code, rec.Body)
		}
	}
	if got := a.stepUp(fs, "backups/target", disk); got != "/?step-up=done" {
		t.Fatalf("the step-up: %s", got)
	}
	for _, raw := range both {
		rec := a.do("PUT", "/api/v1/backups/target", sameOrigin, func(r *http.Request) {
			r.Body, r.ContentLength = io.NopCloser(strings.NewReader(raw)), int64(len(raw))
		})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("the PUT %s on the step-up for the disk: %d %s", raw, rec.Code, rec.Body)
		}
	}
	if rec := a.send("PUT", "/api/v1/backups/target", disk); rec.Code != http.StatusOK {
		t.Errorf("the change approved, after the refusals: %d %s", rec.Code, rec.Body)
	}
	// A number beyond 2^53 is held exactly, so one a unit off is another change; one the target can't hold is refused.
	big := json.RawMessage(`{"target":{"kind":"b2","region":"eu-central-003","bucket":"household","quotaBytes":9007199254740993},` +
		`"credentials":{"accessKeyId":"005b2keyid0000000001","secretAccessKey":"B2-SECRET-PLACEHOLDER"}}`)
	off := json.RawMessage(strings.Replace(string(big), "9007199254740993", "9007199254740992", 1))
	if got := a.stepUp(fs, "backups/target", big); got != "/?step-up=done" {
		t.Fatalf("the step-up for a quota past 2^53: %s", got)
	}
	if rec := a.send("PUT", "/api/v1/backups/target", off); rec.Code != http.StatusForbidden {
		t.Errorf("a quota one byte less, on that step-up: %d %s", rec.Code, rec.Body)
	}
	tooBig := json.RawMessage(strings.Replace(string(big), "9007199254740993", "18446744073709551616", 1))
	if rec := a.send("POST", "/api/v1/step-up", map[string]any{"action": "backups/target", "change": tooBig}); rec.Code != http.StatusBadRequest {
		t.Errorf("a quota past what the target holds: %d %s", rec.Code, rec.Body)
	}
}

// A grant is spent only by the change it approves being made: a change that fails, or is refused for another reason
// once the step-up is checked, leaves it for the retry.
func TestAStepUpIsSpentOnlyWhenTheChangeIsMade(t *testing.T) {
	testenv.Isolate(t)
	p, fs, a, _ := withWritableStorage(t)
	if got := a.stepUp(fs, "backups/target", disk); got != "/?step-up=done" {
		t.Fatalf("the step-up: %s", got)
	}
	p.host.FailRestic("init", 1)
	if rec := a.send("PUT", "/api/v1/backups/target", disk); rec.Code == http.StatusOK || rec.Code == http.StatusForbidden {
		t.Fatalf("with restic failing: %d %s", rec.Code, rec.Body)
	}
	p.host.FailRestic("init", 0)
	if rec := a.send("PUT", "/api/v1/backups/target", disk); rec.Code != http.StatusOK {
		t.Errorf("the retry, on the same step-up: %d %s", rec.Code, rec.Body)
	}
	if rec := a.send("PUT", "/api/v1/backups/target", disk); rec.Code != http.StatusForbidden {
		t.Errorf("once made, again: %d %s", rec.Code, rec.Body)
	}
}
