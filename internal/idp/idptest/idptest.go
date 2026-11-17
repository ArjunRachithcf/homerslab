// Package idptest is the identity provider contract suite: what every provider adapter must do, checked against the
// real provider in containers (spec: Testing Decisions). A provider's own test supplies a World and calls Run.
//
// The browser's side is a software authenticator (webauthntest) driving the provider's own flows, so the suite can
// also send what a real browser never would: an assertion without user verification, or from another origin. The
// real browser, with Chromium's virtual authenticator, is the flow test's.
package idptest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/idp"
	"github.com/ArjunRachithcf/homeward/internal/idp/webauthntest"
	"github.com/ArjunRachithcf/homeward/internal/oidc"
)

// World is a provider in containers, as a contract test builds it.
type World interface {
	// Provider is the adapter, with Homeward's own token, for Household.
	Provider() idp.Provider
	Household() idp.Household
	// Browser is a new browser, with its own cookies, on the sign-in's origin, holding the passkeys of a.
	Browser(t *testing.T, a *webauthntest.Authenticator) Browser
	// FirstAdmin tries every way the provider has to make a first admin, as a stranger who reached the sign-in, and
	// says which of them let the stranger in (none should).
	FirstAdmin(t *testing.T) []string
	// Expire makes every outstanding enrolment link of the person expire now, as the clock passing would.
	Expire(t *testing.T, username string)
	// Drift changes Homeward's sign-in behind its back, as an admin in the provider's own console could: user
	// verification relaxed to "preferred" on its passkey stages, the sign-in served under another name, and the
	// provider's own sign-in open to the household's people again.
	Drift(t *testing.T)
	// GiveFactors gives a person a password and a second factor that isn't a passkey (TOTP), as an admin in the
	// provider's own console could, and returns them.
	GiveFactors(t *testing.T, username string) Factors
	// GiveOthers gives a person an app password and recovery codes, as an admin in the provider's own console could.
	GiveOthers(t *testing.T, username string)
	// SetUpAuthenticators runs, in b, each of the provider's own flows that set up a way to sign in (a passkey on a,
	// TOTP, recovery codes, a password), signed in as the person with b's passkey first. It returns those that set
	// anything up; one that neither does nor refuses fails the test.
	SetUpAuthenticators(t *testing.T, b Browser, username string, a *webauthntest.Authenticator) []string
	// ProviderSignIn signs in in b through the provider's own default sign-in, outside any client, with the factors
	// alone, as a thief who has them would. It returns nil only when b came out of it with a session; the factors
	// themselves must have been taken (a wrong one fails the test, so a refusal is the provider's, not a typo's).
	ProviderSignIn(t *testing.T, b Browser, username string, f Factors) error
	// OIDC is the relying party's HTTP client for server-to-server calls (token, keys), the way the manager reaches
	// the sign-in.
	OIDC() oidc.Client
	// AskGuard asks the provider's forward-auth for a request to host, as the edge does before it lets a request
	// reach a guarded app, from a browser with no session there; it returns the answer's status and Location.
	AskGuard(t *testing.T, host string) (status int, location string)
	// PassGuard goes to host in a browser holding a's passkeys, as username, the way the guard sends a browser with no
	// session (signing in where it is asked to), then asks the guard for host with whatever session that left; it
	// returns the guard's status, and the role its answer names for the edge (#236): admin, adult or kid, or "" for none.
	PassGuard(t *testing.T, a *webauthntest.Authenticator, username, host string) (status int, role string)
}

// Enrolment is what a provider asked the browser for when it made a passkey.
type Enrolment struct {
	RPID             string
	UserVerification string
}

// Factors are what a person may know besides their passkeys: a password, and a TOTP code maker.
type Factors struct {
	Password string
	// TOTP is the current code, a new one at each call (it waits for the next period when it must).
	TOTP func() string
}

// Browser runs the provider's flows as a person would, keeping its session between them.
type Browser interface {
	// Enrol opens an enrolment link and makes a passkey with the browser's authenticator.
	Enrol(t *testing.T, link idp.Link) (Enrolment, error)
	// SignIn opens a client's authorize URL, signs in as username through whatever flow the provider shows, and
	// returns the redirect's state and code. Its authenticator's passkeys are offered wherever a passkey is asked,
	// and f's factors wherever a password or a code is.
	SignIn(t *testing.T, authorize, username string, f Factors) (state, code string, err error)
}

// Prepare makes the household's sign-in as the manager does: a provider that isn't ready yet (Authentik's worker
// applying its own blueprints again after a restart, #254) is asked again, for up to three minutes. Any other error
// fails at once, an unreachable provider too (the manager waits that out as well; a test's provider is up). A
// provider's own tests call it wherever they call Prepare (#259).
func Prepare(ctx context.Context, p idp.Provider, h idp.Household) error {
	return untilReady(ctx, func() error { return p.Prepare(ctx, h) }, 3*time.Minute, 3*time.Second)
}

// untilReady calls try until it succeeds, fails with anything but idp.ErrNotReady, wait has passed or ctx ends, and
// returns its last error (with ctx's, if ctx ended).
func untilReady(ctx context.Context, try func() error, wait, poll time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := try()
		if !errors.Is(err, idp.ErrNotReady) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (%w)", err, ctx.Err())
		case <-time.After(poll):
		}
	}
}

// Run runs the contract against a World.
func Run(t *testing.T, w World, version string) {
	ctx := context.Background()
	p, h := w.Provider(), w.Household()
	if err := Prepare(ctx, p, h); err != nil {
		t.Fatalf("preparing the sign-in: %v", err)
	}

	want := idp.Report{Provider: "", Version: version, OIDC: true, ForwardAuth: true, LDAP: true, RPID: h.SignIn(), Origin: h.Origin(),
		UserVerification: "required", FirstAdminOff: true, PasskeyOnlySignIn: true}
	t.Run("a report of what it can do", func(t *testing.T) {
		r, err := p.Report(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want.Provider = r.Provider
		if r != want || r.Provider == "" {
			t.Errorf("report:\n got %+v\nwant %+v", r, want)
		}
	})

	t.Run("no first-comer admin: the provider's own first-admin flow applies to nobody (#84)", func(t *testing.T) {
		if in := w.FirstAdmin(t); len(in) > 0 {
			t.Errorf("a stranger became an admin through: %v", in)
		}
	})

	admin := idp.Person{Username: "ines", Name: "Inês Ferreira", Mail: "ines@ferreira.family", Role: idp.Admin}
	t.Run("people, and the groups for roles", func(t *testing.T) {
		if err := p.EnsurePerson(ctx, admin); err != nil {
			t.Fatal(err)
		}
		if err := p.EnsurePerson(ctx, admin); err != nil {
			t.Fatalf("again: %v", err)
		}
		adult := idp.Person{Username: "rui", Name: "Rui", Mail: "rui@ferreira.family", Role: idp.Kid}
		if err := p.EnsurePerson(ctx, adult); err != nil {
			t.Fatal(err)
		}
		adult.Role = idp.Adult // growing up: one role's group, never two
		if err := p.EnsurePerson(ctx, adult); err != nil {
			t.Fatal(err)
		}
		for r, want := range map[idp.Role][]string{idp.Admin: {"ines"}, idp.Adult: {"rui"}, idp.Kid: nil} {
			ps, err := p.People(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, x := range ps {
				got = append(got, x.Username)
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s: %v, want %v", r, got, want)
			}
		}
		if ks, err := p.Passkeys(ctx, "ines"); err != nil || len(ks) != 0 {
			t.Errorf("a new person's passkeys: %v %v, want none: one comes only from an enrolment link", ks, err)
		}
	})

	phone := webauthntest.New(h.Origin())
	t.Run("the first passkey, from a one-time enrolment link: RP ID the sign-in's own name, user verification required (#83, #84)", func(t *testing.T) {
		link, err := p.EnrolmentLink(ctx, "ines")
		if err != nil {
			t.Fatal(err)
		}
		if d := time.Until(link.ExpiresAt); d > idp.EnrolmentLife || d < idp.EnrolmentLife-time.Minute {
			t.Errorf("the link lasts %v, want %v", d, idp.EnrolmentLife)
		}
		e, err := w.Browser(t, phone).Enrol(t, link)
		if err != nil {
			t.Fatal(err)
		}
		if e.RPID != h.SignIn() {
			t.Errorf("the provider asked for a passkey with RP ID %q, want %q: never the bare domain", e.RPID, h.SignIn())
		}
		if e.UserVerification != "required" {
			t.Errorf("the provider asked for user verification %q at enrolment, want required", e.UserVerification)
		}
		if ks, _ := p.Passkeys(ctx, "ines"); len(ks) != 1 {
			t.Errorf("passkeys after enrolling: %d, want 1", len(ks))
		}
		if _, err := w.Browser(t, webauthntest.New(h.Origin())).Enrol(t, link); err == nil {
			t.Error("the link worked a second time")
		}
		if ks, _ := p.Passkeys(ctx, "ines"); len(ks) != 1 {
			t.Errorf("passkeys after the link's second use: %d, want still 1", len(ks))
		}
	})

	t.Run("credentials: what a person holds, as the provider lists it, passkeys alone for a new admin (#25, #84)", func(t *testing.T) {
		c, err := p.Credentials(ctx, "ines")
		if err != nil {
			t.Fatal(err)
		}
		if c.Passkeys != 1 || len(c.Others) != 0 || !c.PasskeysOnly() {
			t.Errorf("the admin's credentials: %+v, want one passkey and nothing else", c)
		}
		if _, err := p.Credentials(ctx, "nobody"); !errors.Is(err, idp.ErrNoPerson) {
			t.Errorf("nobody's credentials: %v, want ErrNoPerson", err)
		}
	})

	t.Run("an expired enrolment link, and a new link ending the older one", func(t *testing.T) {
		old, err := p.EnrolmentLink(ctx, "ines")
		if err != nil {
			t.Fatal(err)
		}
		newer, err := p.EnrolmentLink(ctx, "ines")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Browser(t, webauthntest.New(h.Origin())).Enrol(t, old); err == nil {
			t.Error("an older link still worked after a new one")
		}
		w.Expire(t, "ines")
		if _, err := w.Browser(t, webauthntest.New(h.Origin())).Enrol(t, newer); err == nil {
			t.Error("an expired link worked")
		}
		if ks, _ := p.Passkeys(ctx, "ines"); len(ks) != 1 {
			t.Errorf("passkeys: %d, want still 1", len(ks))
		}
		if _, err := p.EnrolmentLink(ctx, "nobody"); !errors.Is(err, idp.ErrNoPerson) {
			t.Errorf("a link for nobody: %v, want ErrNoPerson", err)
		}
	})

	c := w.OIDC()
	creds, err := p.EnsureClient(ctx, idp.Client{Slug: "homerslab-admin", Name: "Admin", RedirectURIs: []string{c.Redirect},
		Roles: []idp.Role{idp.Admin}, PasskeyOnly: true})
	if err != nil {
		t.Fatalf("Admin's client: %v", err)
	}
	if again, err := p.EnsureClient(ctx, idp.Client{Slug: "homerslab-admin", Name: "Admin", RedirectURIs: []string{c.Redirect},
		Roles: []idp.Role{idp.Admin}, PasskeyOnly: true}); err != nil || again.ID != creds.ID || again.Secret != creds.Secret {
		t.Errorf("Admin's client again: %v; its credentials changed: %v", err, again.ID != creds.ID)
	}
	c.Issuer, c.ID, c.Secret = creds.Issuer, creds.ID, creds.Secret
	e, err := c.Discover(ctx)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	// signInWith signs in to Admin's client in b, as Admin asks: a fresh sign-in, whatever session b holds.
	signInWith := func(t *testing.T, b Browser, username string, f Factors) (oidc.Claims, error) {
		t.Helper()
		at := oidc.FreshAttempt(time.Now())
		state, code, err := b.SignIn(t, c.AuthorizeURL(e, at), username, f)
		if err != nil {
			return oidc.Claims{}, err
		}
		return c.Finish(ctx, e, at, state, code, time.Now())
	}
	signIn := func(t *testing.T, a *webauthntest.Authenticator, username string) (oidc.Claims, error) {
		t.Helper()
		return signInWith(t, w.Browser(t, a), username, Factors{})
	}

	t.Run("Admin's client: the admin signs in with the passkey, and the ID token names them and their role", func(t *testing.T) {
		cl, err := signIn(t, phone, "ines")
		if err != nil {
			t.Fatal(err)
		}
		if cl.Username != "ines" || !slices.Contains(cl.Groups, idp.RoleGroup(idp.Admin)) {
			t.Errorf("claims %+v", cl)
		}
	})

	t.Run("every assertion requires user verification (#84)", func(t *testing.T) {
		touch := *phone
		touch.Verified = false // a security key without a PIN, or a phone left unlocked: a touch alone
		if _, err := signIn(t, &touch, "ines"); err == nil {
			t.Error("a passkey assertion without user verification signed the admin in")
		}
	})

	t.Run("the origin is checked exactly: an assertion from another origin is refused (#83)", func(t *testing.T) {
		elsewhere := *phone
		elsewhere.Origin = "https://photos." + h.Domain
		if _, err := signIn(t, &elsewhere, "ines"); err == nil {
			t.Error("an assertion made for another origin signed the admin in")
		}
		bare := *phone
		bare.Origin = "https://" + h.Domain
		if _, err := signIn(t, &bare, "ines"); err == nil {
			t.Error("an assertion made on the bare domain signed the admin in")
		}
	})

	t.Run("Admin's sign-in is a fresh one: a session at the provider doesn't stand in for the passkey, once it's older than max_age (#84)", func(t *testing.T) {
		b := w.Browser(t, phone)
		before := phone.Assertions()
		if _, err := signInWith(t, b, "ines", Factors{}); err != nil {
			t.Fatal(err)
		}
		if n := phone.Assertions() - before; n != 1 {
			t.Errorf("signing in asked for the passkey %d times, want once", n)
		}
		time.Sleep(oidc.FreshFor + 2*time.Second)
		before = phone.Assertions()
		if _, err := signInWith(t, b, "ines", Factors{}); err != nil {
			t.Fatal(err)
		}
		if phone.Assertions() == before {
			t.Error("Admin's client signed the admin in on the provider's older session, without asking for the passkey")
		}
	})

	// Step-up (#25): a fresh passkey assertion, bound to one action by the nonce. The provider asks for the passkey
	// again on a session older than oidc.StepUpFor, and a session younger than that never passes for one: its
	// sign-in is from before the step-up began.
	t.Run("step-up: the passkey asked again on a live session, its nonce the action's (#25)", func(t *testing.T) {
		b := w.Browser(t, phone)
		if _, err := signInWith(t, b, "ines", Factors{}); err != nil {
			t.Fatal(err)
		}
		// The software authenticator answers within the second; a person never does: it waits for the next.
		stepUp := func(nonce string) (oidc.Claims, error) {
			at := oidc.StepUpAttempt(time.Now(), nonce)
			time.Sleep(time.Until(at.Since.Truncate(time.Second).Add(time.Second)))
			state, code, err := b.SignIn(t, c.AuthorizeURL(e, at), "ines", Factors{})
			if err != nil {
				return oidc.Claims{}, err
			}
			return c.Finish(ctx, e, at, state, code, time.Now())
		}
		// Seconds after the sign-in, the provider may pass on its session: then the ID token's sign-in is from before
		// the step-up, and refused. Either way, no step-up passes without the passkey asked again.
		before := phone.Assertions()
		if _, err := stepUp("plan-too-soon"); err == nil && phone.Assertions() == before {
			t.Error("a step-up passed on the session made a moment before, without asking for the passkey")
		} else if err != nil && !errors.Is(err, oidc.ErrNotFresh) {
			t.Errorf("a step-up on a session made a moment before: %v, want refused as not fresh", err)
		}
		time.Sleep(oidc.StepUpFor + 2*time.Second)
		before = phone.Assertions()
		began := time.Now()
		cl, err := stepUp("plan-0123456789abcdef")
		if err != nil {
			t.Fatal(err)
		}
		if n := phone.Assertions() - before; n != 1 {
			t.Errorf("the step-up asked for the passkey %d times, want once", n)
		}
		if cl.Username != "ines" || !cl.AuthTime.After(began.Truncate(time.Second)) {
			t.Errorf("the step-up's claims: %+v (began %v)", cl, began)
		}
	})

	t.Run("authenticator setup flows refuse the household's people, like the sign-in flows (#84)", func(t *testing.T) {
		other := webauthntest.New(h.Origin())
		if took := w.SetUpAuthenticators(t, w.Browser(t, phone), "ines", other); len(took) > 0 {
			t.Errorf("the provider's own flows set up a way in for the admin: %v", took)
		}
		if ks, _ := p.Passkeys(ctx, "ines"); len(ks) != 1 {
			t.Errorf("passkeys: %d, want still 1", len(ks))
		}
		if _, err := signIn(t, other, "ines"); err == nil {
			t.Error("a passkey from the provider's own setup flow signed the admin in to Admin")
		}
	})

	t.Run("a password and TOTP open nothing: not the provider's own sign-in, not Admin (#84)", func(t *testing.T) {
		f := w.GiveFactors(t, "ines")
		// Step-up reads what the admin holds before it accepts their passkey (#25): the password and TOTP show.
		if c, err := p.Credentials(ctx, "ines"); err != nil || c.PasskeysOnly() || !slices.Contains(c.Others, idp.CredPassword) ||
			!slices.Contains(c.Others, idp.CredTOTP) || c.Passkeys != 1 {
			t.Errorf("the admin's credentials with a password and TOTP: %+v %v", c, err)
		}
		w.GiveOthers(t, "ines")
		if c, err := p.Credentials(ctx, "ines"); err != nil || !slices.Contains(c.Others, idp.CredToken) || !slices.Contains(c.Others, idp.CredRecoveryCodes) {
			t.Errorf("the admin's credentials with an app password and recovery codes: %+v %v", c, err)
		}
		thief := w.Browser(t, webauthntest.New(h.Origin()))
		if err := w.ProviderSignIn(t, thief, "ines", f); err == nil {
			t.Error("the provider's own sign-in let the admin in with a password and TOTP")
		}
		if _, err := signInWith(t, thief, "ines", f); err == nil {
			t.Error("a password and TOTP signed the admin in to Admin")
		}
		if _, err := signInWith(t, w.Browser(t, webauthntest.New(h.Origin())), "ines", f); err == nil {
			t.Error("a password and TOTP signed the admin in to Admin, in a browser with no session")
		}
	})

	t.Run("a client opens only to its roles", func(t *testing.T) {
		link, err := p.EnrolmentLink(ctx, "rui")
		if err != nil {
			t.Fatal(err)
		}
		laptop := webauthntest.New(h.Origin())
		if _, err := w.Browser(t, laptop).Enrol(t, link); err != nil {
			t.Fatal(err)
		}
		if _, err := signIn(t, laptop, "rui"); err == nil {
			t.Error("an adult signed in to Admin's client")
		}
	})

	// An app that signs people in itself gets an OIDC client of its own (#20): its id is its slug, it opens only to its
	// audience, and only back to its own redirect addresses, exactly; it changes in place, keeping its credentials, and
	// is deleted with the app.
	t.Run("an app's OIDC client: created for its audience, changed, deleted (#20)", func(t *testing.T) {
		if err := p.EnsurePerson(ctx, idp.Person{Username: "tomas", Name: "Tomás", Mail: "tomas@ferreira.family", Role: idp.Kid}); err != nil {
			t.Fatal(err)
		}
		link, err := p.EnrolmentLink(ctx, "tomas")
		if err != nil {
			t.Fatal(err)
		}
		tablet := webauthntest.New(h.Origin())
		if _, err := w.Browser(t, tablet).Enrol(t, link); err != nil {
			t.Fatal(err)
		}
		link, err = p.EnrolmentLink(ctx, "rui")
		if err != nil {
			t.Fatal(err)
		}
		laptop := webauthntest.New(h.Origin())
		if _, err := w.Browser(t, laptop).Enrol(t, link); err != nil {
			t.Fatal(err)
		}
		app := "https://recipes." + h.Domain
		mealie := idp.Client{Slug: "homerslab-mealie", Name: "Mealie", RedirectURIs: []string{app + "/login"}, Roles: []idp.Role{idp.Admin, idp.Adult}}
		made, err := p.EnsureClient(ctx, mealie)
		if err != nil {
			t.Fatal(err)
		}
		if !made.Created {
			t.Error("a new client: not said to be made by this call")
		}
		if made.ID != mealie.Slug || made.Secret == "" || made.Issuer == creds.Issuer || !strings.HasPrefix(made.Issuer, h.Origin()+"/") {
			t.Errorf("the app's client: id %q, a secret %v, issuer %q (Admin's %q)", made.ID, made.Secret != "", made.Issuer, creds.Issuer)
		}
		rp := w.OIDC()
		rp.Issuer, rp.ID, rp.Secret, rp.Redirect = made.Issuer, made.ID, made.Secret, app+"/login"
		appSignIn := func(t *testing.T, rp oidc.Client, a *webauthntest.Authenticator, username string) (oidc.Claims, error) {
			t.Helper()
			e, err := rp.Discover(ctx)
			if err != nil {
				return oidc.Claims{}, err
			}
			at := oidc.NewAttempt()
			state, code, err := w.Browser(t, a).SignIn(t, rp.AuthorizeURL(e, at), username, Factors{})
			if err != nil {
				return oidc.Claims{}, err
			}
			return rp.Finish(ctx, e, at, state, code, time.Now())
		}
		for _, who := range []struct {
			a        *webauthntest.Authenticator
			username string
			in       bool
		}{{phone, "ines", true}, {laptop, "rui", true}, {tablet, "tomas", false}} {
			cl, err := appSignIn(t, rp, who.a, who.username)
			switch {
			case who.in && (err != nil || cl.Username != who.username):
				t.Errorf("%s, in the app's audience: %+v %v", who.username, cl, err)
			case !who.in && err == nil:
				t.Errorf("%s, outside the app's audience, signed in to it", who.username)
			}
		}
		// Exact addresses only: the last two are what a pattern made of the address would also take (its dots any
		// character; a domain anyone may register), which only exact matching refuses (#20, review round 1).
		for _, elsewhere := range []string{"https://evil.example/login", app + "/login/more", app + ".evil.example/login", "https://recipes." + h.Domain + ":8443/login",
			"https://recipes-" + h.Domain + "/login", app + "/loginX"} {
			other := rp
			other.Redirect = elsewhere
			if _, err := appSignIn(t, other, phone, "ines"); err == nil {
				t.Errorf("the client sent a code to %s", elsewhere)
			}
		}

		changed := mealie
		changed.RedirectURIs, changed.Roles = []string{app + ":9443/login"}, []idp.Role{idp.Admin}
		again, err := p.EnsureClient(ctx, changed)
		if err != nil {
			t.Fatal(err)
		}
		if again.Created {
			t.Error("changing a client: said to make it, so a failed install would delete a running app's client")
		}
		if again.ID != made.ID || again.Secret != made.Secret || again.Issuer != made.Issuer {
			t.Error("changing the client changed its credentials: the app would lose its sign-in")
		}
		if _, err := appSignIn(t, rp, phone, "ines"); err == nil {
			t.Error("the client still sends a code to its old redirect address")
		}
		rp.Redirect = app + ":9443/login"
		if _, err := appSignIn(t, rp, phone, "ines"); err != nil {
			t.Errorf("the changed client, to its new redirect address: %v", err)
		}
		if _, err := appSignIn(t, rp, laptop, "rui"); err == nil {
			t.Error("an adult signed in after the client's audience narrowed to admins")
		}

		for _, bad := range []idp.Client{
			{Slug: "mealie", Name: "Mealie", RedirectURIs: []string{app + "/login"}},
			{Slug: mealie.Slug, Name: "Mealie", RedirectURIs: []string{"https://*." + h.Domain + "/login"}},
			{Slug: mealie.Slug, Name: "Mealie", RedirectURIs: []string{"http://recipes." + h.Domain + "/login"}},
			{Slug: mealie.Slug, Name: "Mealie", RedirectURIs: []string{"https://recipes." + h.Domain + "/.*"}},
			{Slug: mealie.Slug, Name: "Mealie", RedirectURIs: []string{"https://user@recipes." + h.Domain + "/login"}},
			{Slug: mealie.Slug, Name: "Mealie", RedirectURIs: []string{"/login"}},
		} {
			if _, err := p.EnsureClient(ctx, bad); err == nil {
				t.Errorf("made the client %+v", bad)
			}
		}
		if _, err := appSignIn(t, rp, phone, "ines"); err != nil {
			t.Errorf("a refused change touched the client: %v", err)
		}

		if err := p.DeleteClient(ctx, mealie.Slug); err != nil {
			t.Fatal(err)
		}
		if err := p.DeleteClient(ctx, mealie.Slug); err != nil {
			t.Errorf("deleting it again: %v", err)
		}
		if _, err := rp.Discover(ctx); err == nil {
			t.Error("the deleted client's issuer still answers")
		}
		e := oidc.Endpoints{Issuer: made.Issuer, Authorize: h.URL("/application/o/authorize/"), Token: h.URL("/application/o/token/")}
		at := oidc.NewAttempt()
		if _, _, err := w.Browser(t, phone).SignIn(t, rp.AuthorizeURL(e, at), "ines", Factors{}); err == nil {
			t.Error("a sign-in to the deleted client sent a code")
		}
		if err := p.DeleteClient(ctx, "authentik"); err == nil {
			t.Error("deleted a client that isn't Homeward's")
		}
		if _, err := signIn(t, phone, "ines"); err != nil {
			t.Errorf("Admin's client, after an app's was deleted: %v", err)
		}
	})

	t.Run("a guard per app: its own name only, and a stranger is sent to sign in (#19, #83)", func(t *testing.T) {
		g := idp.Guard{Slug: "homerslab-bentopdf", Name: "BentoPDF", URL: "https://pdf." + h.Domain, Roles: []idp.Role{idp.Admin, idp.Adult}}
		if err := p.EnsureGuard(ctx, g); err != nil {
			t.Fatal(err)
		}
		if err := p.EnsureGuard(ctx, g); err != nil { // brought in line, not made twice
			t.Fatalf("again: %v", err)
		}
		var status int
		var location string
		for deadline := time.Now().Add(time.Minute); ; time.Sleep(2 * time.Second) { // the outpost takes its provider in a while
			if status, location = w.AskGuard(t, "pdf."+h.Domain); status/100 == 3 || time.Now().After(deadline) {
				break
			}
		}
		if status/100 != 3 || location == "" {
			t.Errorf("the guard, asked for its app with no session: %d %q, want a redirect to sign in", status, location)
		}
		if status, _ := w.AskGuard(t, "other."+h.Domain); status/100 == 2 {
			t.Errorf("a name no guard is for: %d, let on", status)
		}
		for _, bad := range []idp.Guard{{Slug: "bentopdf", URL: g.URL}, {Slug: g.Slug, URL: "http://pdf." + h.Domain}, {Slug: g.Slug, URL: "https://*." + h.Domain},
			{Slug: g.Slug, URL: "https://pdf." + h.Domain + "/path"}} {
			if err := p.EnsureGuard(ctx, bad); err == nil {
				t.Errorf("made the guard %+v", bad)
			}
		}
	})

	t.Run("a second guarded app: each keeps its own name and audience (#219)", func(t *testing.T) {
		pdf := idp.Guard{Slug: "homerslab-bentopdf", Name: "BentoPDF", URL: "https://pdf." + h.Domain, Roles: []idp.Role{idp.Admin, idp.Adult}}
		chores := idp.Guard{Slug: "homerslab-chores", Name: "Chores", URL: "https://chores." + h.Domain, Roles: []idp.Role{idp.Kid}}
		for _, g := range []idp.Guard{chores, pdf, chores} {
			if err := p.EnsureGuard(ctx, g); err != nil {
				t.Fatalf("%s: %v", g.Slug, err)
			}
		}
		people := map[string]*webauthntest.Authenticator{}
		for _, who := range []idp.Person{{Username: "bea", Name: "Bea", Mail: "bea@ferreira.family", Role: idp.Adult},
			{Username: "lia", Name: "Lia", Mail: "lia@ferreira.family", Role: idp.Kid}} {
			if err := p.EnsurePerson(ctx, who); err != nil {
				t.Fatal(err)
			}
			link, err := p.EnrolmentLink(ctx, who.Username)
			if err != nil {
				t.Fatal(err)
			}
			people[who.Username] = webauthntest.New(h.Origin())
			if _, err := w.Browser(t, people[who.Username]).Enrol(t, link); err != nil {
				t.Fatal(err)
			}
		}
		for _, host := range []string{"pdf." + h.Domain, "chores." + h.Domain} {
			var status int
			for deadline := time.Now().Add(time.Minute); ; time.Sleep(2 * time.Second) { // the outpost takes its providers in a while
				if status, _ = w.AskGuard(t, host); status/100 == 3 || time.Now().After(deadline) {
					break
				}
			}
			if status/100 != 3 {
				t.Errorf("the guard of %s, with no session: %d, want a redirect to sign in", host, status)
			}
		}
		for _, c := range []struct {
			who, host string
			in        bool
			role      idp.Role
		}{{"bea", "pdf", true, idp.Adult}, {"bea", "chores", false, ""}, {"lia", "chores", true, idp.Kid}, {"lia", "pdf", false, ""}} {
			status, role := w.PassGuard(t, people[c.who], c.who, c.host+"."+h.Domain)
			if in := status/100 == 2; in != c.in {
				t.Errorf("%s at %s: the guard answered %d, want let in: %v", c.who, c.host, status, c.in)
			}
			// The role the edge passes on is the provider's answer, the one role group they're in (#236).
			if in := status/100 == 2; in && role != string(c.role) {
				t.Errorf("%s at %s: the guard names the role %q, want %q", c.who, c.host, role, c.role)
			}
		}
	})

	t.Run("a second passkey from another link (two ways in, #87)", func(t *testing.T) {
		link, err := p.EnrolmentLink(ctx, "ines")
		if err != nil {
			t.Fatal(err)
		}
		key := webauthntest.New(h.Origin())
		if _, err := w.Browser(t, key).Enrol(t, link); err != nil {
			t.Fatal(err)
		}
		if ks, _ := p.Passkeys(ctx, "ines"); len(ks) != 2 {
			t.Errorf("passkeys: %d, want 2", len(ks))
		}
		if _, err := signIn(t, key, "ines"); err != nil {
			t.Errorf("the second passkey: %v", err)
		}
	})

	t.Run("drift: the report reads what the provider does, and Prepare puts it back", func(t *testing.T) {
		w.Drift(t)
		r, err := p.Report(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if r.UserVerification == "required" || r.RPID == h.SignIn() || r.Origin == h.Origin() || r.PasskeyOnlySignIn {
			t.Errorf("the drift didn't show in the report: %+v", r)
		}
		if err := Prepare(ctx, p, h); err != nil {
			t.Fatal(err)
		}
		if r, _ := p.Report(ctx); r != want {
			t.Errorf("after Prepare: %+v", r)
		}
	})
}
