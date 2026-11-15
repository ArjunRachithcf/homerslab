package idptest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/idp"
)

// LDAPWorld is a provider that serves apps over LDAP, with its outpost in containers (#204).
type LDAPWorld interface {
	World
	// LDAP is the adapter's LDAP side.
	LDAP() idp.LDAP
	// StartOutpost runs the provider's LDAP outpost with its token, as the sign-in's unit does.
	StartOutpost(t *testing.T, token idp.Secret)
	// AppPassword gives a person an app password, the one secret they bind with, and returns it.
	AppPassword(t *testing.T, username string) string
	// Bind binds at the outpost as dn with password. It returns nil only when the outpost said yes.
	Bind(t *testing.T, dn, password string) error
}

// PersonDN is a person's DN in an app's directory.
func PersonDN(c idp.LDAPCredentials, username string) string {
	return "cn=" + username + ",ou=users," + c.BaseDN
}

// RunLDAP runs the LDAP contract: an app's directory opens to its roles only, its own account binds, and nothing a
// person has but an app password binds them (#204). The sign-in is prepared already (Run).
func RunLDAP(t *testing.T, w LDAPWorld) {
	ctx := context.Background()
	p, l := w.Provider(), w.LDAP()
	people := []idp.Person{
		{Username: "lena", Name: "Lena", Mail: "lena@ferreira.family", Role: idp.Adult},
		{Username: "kiko", Name: "Kiko", Mail: "kiko@ferreira.family", Role: idp.Kid},
		{Username: "olga", Name: "Olga", Mail: "olga@ferreira.family", Role: idp.Admin},
	}
	for _, x := range people {
		if err := p.EnsurePerson(ctx, x); err != nil {
			t.Fatal(err)
		}
	}

	var token idp.Secret
	t.Run("the LDAP outpost: one, its token kept", func(t *testing.T) {
		var err error
		if token, err = l.LDAPOutpost(ctx); err != nil || token == "" {
			t.Fatalf("the outpost: %v", err)
		}
		again, err := l.LDAPOutpost(ctx)
		if err != nil || again != token {
			t.Errorf("again: a new token, or %v", err)
		}
	})
	if token == "" {
		t.Fatal("no outpost: nothing else can be checked")
	}
	w.StartOutpost(t, token)

	media := idp.LDAPApp{Slug: "homerslab-jellyfin", Name: "Jellyfin", Roles: []idp.Role{idp.Adult}}
	var creds idp.LDAPCredentials
	t.Run("an app's directory: made for its roles, its credentials kept", func(t *testing.T) {
		var err error
		if creds, err = l.EnsureLDAP(ctx, media); err != nil {
			t.Fatal(err)
		}
		if !creds.Created || creds.BaseDN == "" || creds.BindDN == "" || creds.Password == "" {
			t.Fatalf("credentials: %+v", creds)
		}
		again, err := l.EnsureLDAP(ctx, media)
		if err != nil || again.Created || again.Password != creds.Password || again.BindDN != creds.BindDN || again.BaseDN != creds.BaseDN {
			t.Errorf("again: %+v %v, want the same credentials, not made", again, err)
		}
		for _, bad := range []idp.LDAPApp{{Slug: "jellyfin", Roles: media.Roles}, {Slug: "homerslab-outpost", Roles: media.Roles},
			{Slug: "homerslab-admin", Roles: media.Roles}, {Slug: "homerslab-Jelly fin", Roles: media.Roles}, {Slug: "homerslab-books"}} {
			if _, err := l.EnsureLDAP(ctx, bad); err == nil {
				t.Errorf("made the directory %+v", bad)
			}
		}
	})
	if creds.Password == "" {
		t.Fatal("no directory: nothing else can be checked")
	}

	// The outpost takes a new provider in a while.
	bound := func(dn, password string) error {
		var err error
		for deadline := time.Now().Add(90 * time.Second); ; time.Sleep(3 * time.Second) {
			if err = w.Bind(t, creds.BindDN, creds.Password.Reveal()); err == nil || time.Now().After(deadline) {
				break
			}
		}
		if err != nil {
			return err
		}
		return w.Bind(t, dn, password)
	}
	lena, kiko, olga := w.AppPassword(t, "lena"), w.AppPassword(t, "kiko"), w.AppPassword(t, "olga")

	t.Run("the app's own account binds", func(t *testing.T) {
		if err := bound(creds.BindDN, creds.Password.Reveal()); err != nil {
			t.Fatalf("the app's own account: %v", err)
		}
		if err := w.Bind(t, creds.BindDN, creds.Password.Reveal()+"x"); err == nil {
			t.Error("the app's own account bound with a wrong password")
		}
	})
	t.Run("a person of the audience binds with an app password, and only with it", func(t *testing.T) {
		if err := bound(PersonDN(creds, "lena"), lena); err != nil {
			t.Errorf("an adult, in an adults' app: %v", err)
		}
		if err := w.Bind(t, PersonDN(creds, "lena"), lena[:len(lena)-1]); err == nil {
			t.Error("a wrong app password bound")
		}
		f := w.GiveFactors(t, "lena")
		if err := w.Bind(t, PersonDN(creds, "lena"), f.Password); err == nil {
			t.Error("a password from the provider's console bound: only app passwords may")
		}
		if err := w.Bind(t, PersonDN(creds, "lena"), f.Password+";"+f.TOTP()); err == nil {
			t.Error("a password and TOTP bound: only app passwords may")
		}
	})
	t.Run("a person outside the audience can't bind: a kid, an admin", func(t *testing.T) {
		if err := bound(PersonDN(creds, "kiko"), kiko); err == nil {
			t.Error("a kid bound to an adults' app")
		}
		if err := w.Bind(t, PersonDN(creds, "olga"), olga); err == nil {
			t.Error("an admin bound to an adults' app")
		}
		if err := w.Bind(t, PersonDN(creds, "kiko"), lena); err == nil {
			t.Error("a kid bound with an adult's app password")
		}
	})
	t.Run("each app's directory is its own: an adult can't bind where only kids may", func(t *testing.T) {
		books := idp.LDAPApp{Slug: "homerslab-kids-books", Name: "Kids' books", Roles: []idp.Role{idp.Kid}}
		other, err := l.EnsureLDAP(ctx, books)
		if err != nil {
			t.Fatal(err)
		}
		if other.BaseDN == creds.BaseDN || other.Password == creds.Password {
			t.Fatalf("two apps share a directory or a password: %s", other.BaseDN)
		}
		var kid error
		for deadline := time.Now().Add(90 * time.Second); ; time.Sleep(3 * time.Second) {
			if kid = w.Bind(t, PersonDN(other, "kiko"), kiko); kid == nil || time.Now().After(deadline) {
				break
			}
		}
		if kid != nil {
			t.Errorf("a kid, in a kids' app: %v", kid)
		}
		if err := w.Bind(t, PersonDN(other, "lena"), lena); err == nil {
			t.Error("an adult bound to a kids' app")
		}
		if err := w.Bind(t, creds.BindDN, creds.Password.Reveal()); err != nil {
			t.Errorf("the first app's account, still: %v", err)
		}
		if err := w.Bind(t, PersonDN(other, cn(creds.BindDN)), creds.Password.Reveal()); err == nil {
			t.Error("one app's account bound to another's directory")
		}
		if err := l.DeleteLDAP(ctx, books.Slug); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a change of role holds at the next bind", func(t *testing.T) {
		kid := people[0]
		kid.Role = idp.Kid
		if err := p.EnsurePerson(ctx, kid); err != nil {
			t.Fatal(err)
		}
		if err := w.Bind(t, PersonDN(creds, "lena"), lena); err == nil {
			t.Error("an adult who became a kid still binds to an adults' app")
		}
	})
	t.Run("a deleted directory: its account binds no more, and deleting again is done", func(t *testing.T) {
		if err := l.DeleteLDAP(ctx, media.Slug); err != nil {
			t.Fatal(err)
		}
		if err := w.Bind(t, creds.BindDN, creds.Password.Reveal()); err == nil {
			t.Error("a deleted app's account still binds")
		}
		if err := l.DeleteLDAP(ctx, media.Slug); err != nil {
			t.Errorf("again: %v", err)
		}
		for _, bad := range []string{"jellyfin", "homerslab-outpost", "homerslab-admin", ""} {
			if err := l.DeleteLDAP(ctx, bad); err == nil {
				t.Errorf("deleted %q", bad)
			}
		}
	})
}

// cn is a DN's first value: cn=<username>,… → <username>.
func cn(dn string) string {
	v, _, _ := strings.Cut(strings.TrimPrefix(dn, "cn="), ",")
	return v
}
