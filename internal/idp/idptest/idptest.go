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
	"slices"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/idp"
	"github.com/ArjunRachithcf/homerslab/internal/idp/webauthntest"
	"github.com/ArjunRachithcf/homerslab/internal/oidc"
)

// World is a provider in containers, as a contract test builds it.
type World interface {
	// Provider is the adapter, with HomersLab's own token, for Household.
	Provider() idp.Provider
	Household() idp.Household
	// Browser is a new browser, with its own cookies, on the sign-in's origin, holding the passkeys of a.
	Browser(t *testing.T, a *webauthntest.Authenticator) Browser
	// FirstAdmin tries every way the provider has to make a first admin, as a stranger who reached the sign-in, and
	// says which of them let the stranger in (none should).
	FirstAdmin(t *testing.T) []string
	// Expire makes every outstanding enrolment link of the person expire now, as the clock passing would.
	Expire(t *testing.T, username string)
	// Drift changes HomersLab's sign-in behind its back, as an admin in the provider's own console could: user
	// verification relaxed to "preferred" on its passkey stages, the sign-in served under another name, and the
	// provider's own sign-in open to the household's people again.
	Drift(t *testing.T)
	// GiveFactors gives a person a password and a second factor that isn't a passkey (TOTP), as a stolen session of
	// theirs could from the provider's own user settings: b holds their passkey, and signs in with it first. It
	// returns the factors, which it has checked the provider took.
	GiveFactors(t *testing.T, b Browser, username string) Factors
	// ProviderSignIn signs in in b through the provider's own default sign-in, outside any client, with the factors
	// alone, as a thief who has them would. It returns nil only when b came out of it with a session; the factors
	// themselves must have been taken (a wrong one fails the test, so a refusal is the provider's, not a typo's).
	ProviderSignIn(t *testing.T, b Browser, username string, f Factors) error
	// OIDC is the relying party's HTTP client for server-to-server calls (token, keys), the way the manager reaches
	// the sign-in.
	OIDC() oidc.Client
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

// Run runs the contract against a World.
func Run(t *testing.T, w World, version string) {
	ctx := context.Background()
	p, h := w.Provider(), w.Household()
	if err := p.Prepare(ctx, h); err != nil {
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

	t.Run("a password and TOTP added from a stolen session open nothing: not the provider's own sign-in, not Admin (#84)", func(t *testing.T) {
		f := w.GiveFactors(t, w.Browser(t, phone), "ines")
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
		if err := p.Prepare(ctx, h); err != nil {
			t.Fatal(err)
		}
		if r, _ := p.Report(ctx); r != want {
			t.Errorf("after Prepare: %+v", r)
		}
	})
}
