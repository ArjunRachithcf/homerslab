package idptest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/idp"
	"github.com/ArjunRachithcf/homerslab/internal/ldaps/ldaptest"
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
	// Search binds at the outpost as dn with password, then searches base's whole subtree for every object.
	Search(t *testing.T, dn, password, base string) ([]ldaptest.Entry, error)
}

// RunLDAP runs the LDAP contract: an app's provider opens to its roles only, a person sees nobody but themselves, and
// nothing a person has but an app password binds them (#204). The sign-in is prepared already (Run).
func RunLDAP(t *testing.T, w LDAPWorld) {
	ctx := context.Background()
	p, l := w.Provider(), w.LDAP()
	people := []idp.Person{
		{Username: "lena", Name: "Lena", Mail: "lena@ferreira.family", Role: idp.Adult},
		{Username: "maria", Name: "Maria", Mail: "maria@ferreira.family", Role: idp.Adult},
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
	t.Run("an app's LDAP provider: made for its roles, its base DN kept, and no account of its own", func(t *testing.T) {
		var err error
		if creds, err = l.EnsureLDAP(ctx, media); err != nil {
			t.Fatal(err)
		}
		if !creds.Created || creds.BaseDN == "" || !strings.Contains(creds.UserDN, "%s") || strings.Count(creds.UserDN, "%") != 1 ||
			!strings.HasSuffix(creds.UserDN, ","+creds.BaseDN) {
			t.Fatalf("credentials: %+v", creds)
		}
		again, err := l.EnsureLDAP(ctx, media)
		if err != nil || again.Created || again != (idp.LDAPCredentials{BaseDN: creds.BaseDN, UserDN: creds.UserDN}) {
			t.Errorf("again: %+v %v, want the same, not made", again, err)
		}
		for _, bad := range []idp.LDAPApp{{Slug: "jellyfin", Roles: media.Roles}, {Slug: "homerslab-outpost", Roles: media.Roles},
			{Slug: "homerslab-admin", Roles: media.Roles}, {Slug: "homerslab-Jelly fin", Roles: media.Roles}, {Slug: "homerslab-books"}} {
			if _, err := l.EnsureLDAP(ctx, bad); err == nil {
				t.Errorf("made the provider %+v", bad)
			}
		}
	})
	if creds.BaseDN == "" {
		t.Fatal("no provider: nothing else can be checked")
	}
	dn := func(c idp.LDAPCredentials, username string) string { return fmt.Sprintf(c.UserDN, username) }
	lena, maria, kiko, olga := w.AppPassword(t, "lena"), w.AppPassword(t, "maria"), w.AppPassword(t, "kiko"), w.AppPassword(t, "olga")
	// The outpost takes a new provider in a while: until it does, nobody binds there.
	ready := func(c idp.LDAPCredentials, username, password string) error {
		var err error
		for deadline := time.Now().Add(90 * time.Second); ; time.Sleep(3 * time.Second) {
			if err = w.Bind(t, dn(c, username), password); err == nil || time.Now().After(deadline) {
				return err
			}
		}
	}

	t.Run("a person of the audience binds with an app password, and only with it", func(t *testing.T) {
		if err := ready(creds, "lena", lena); err != nil {
			t.Fatalf("an adult, in an adults' app: %v", err)
		}
		if err := w.Bind(t, dn(creds, "lena"), lena[:len(lena)-1]); err == nil {
			t.Error("a wrong app password bound")
		}
		f := w.GiveFactors(t, "lena")
		if err := w.Bind(t, dn(creds, "lena"), f.Password); err == nil {
			t.Error("a password from the provider's console bound: only app passwords may")
		}
		if err := w.Bind(t, dn(creds, "lena"), f.Password+";"+f.TOTP()); err == nil {
			t.Error("a password and TOTP bound: only app passwords may")
		}
	})
	t.Run("a person outside the audience can't bind: a kid, an admin", func(t *testing.T) {
		if err := w.Bind(t, dn(creds, "kiko"), kiko); err == nil {
			t.Error("a kid bound to an adults' app")
		}
		if err := w.Bind(t, dn(creds, "olga"), olga); err == nil {
			t.Error("an admin bound to an adults' app")
		}
		if err := w.Bind(t, dn(creds, "kiko"), lena); err == nil {
			t.Error("a kid bound with an adult's app password")
		}
	})
	t.Run("a person sees only their own entry and their own groups: nobody else of the household, nor any account (#204)", func(t *testing.T) {
		own := strings.ToLower(dn(creds, "lena"))
		for _, base := range []string{creds.BaseDN, "ou=users," + creds.BaseDN, "ou=groups," + creds.BaseDN} {
			es, err := w.Search(t, dn(creds, "lena"), lena, base)
			if err != nil {
				t.Errorf("searching %s: %v", base, err)
				continue
			}
			t.Logf("lena, searching %s: %d entries", base, len(es))
			for _, e := range es {
				d := strings.ToLower(e.DN)
				switch {
				case d == own:
					if !slices.ContainsFunc(e.Attrs["memberOf"], func(g string) bool { return strings.HasPrefix(strings.ToLower(g), "cn="+idp.RoleGroup(idp.Adult)+",") }) {
						t.Errorf("her own entry without her role's group: %v", e.Attrs["memberOf"])
					}
				case strings.HasPrefix(d, "cn="+idp.RoleGroup(idp.Adult)+",ou=groups,"):
					// her own role's group, with her alone in it: maria, the other adult, never shows
					for _, m := range e.Attrs["member"] {
						if strings.ToLower(m) != own {
							t.Errorf("her role's group lists %s", m)
						}
					}
				case strings.HasPrefix(d, "cn=lena,ou=virtual-groups,"):
					// her own group of one, as Authentik's LDAP gives every person
				case d == strings.ToLower(creds.BaseDN), slices.ContainsFunc([]string{"users", "groups", "virtual-groups"},
					func(ou string) bool { return d == "ou="+ou+","+strings.ToLower(creds.BaseDN) }):
					// the tree itself
				default:
					t.Errorf("lena sees %s", e.DN)
				}
				for _, other := range []string{"maria", "bea", "ines", "kiko", "olga", "akadmin", "homerslab", "ak-outpost"} {
					for k, vs := range e.Attrs {
						for _, v := range vs {
							if strings.Contains(strings.ToLower(v), "cn="+other) && !strings.Contains(strings.ToLower(v), "cn=homerslab-") {
								t.Errorf("lena sees %s in %s's %s: %s", other, e.DN, k, v)
							}
						}
					}
				}
			}
		}
		// Another app's base DN, and the tree above them all: nothing.
		for _, base := range []string{"dc=homerslab", "dc=kids-books,dc=homerslab", "ou=outpost,dc=homerslab"} {
			if es, err := w.Search(t, dn(creds, "lena"), lena, base); err == nil && len(es) > 0 {
				t.Errorf("lena, searching %s: %v", base, es)
			}
		}
	})
	t.Run("each app's provider is its own: an adult can't bind where only kids may", func(t *testing.T) {
		books := idp.LDAPApp{Slug: "homerslab-kids-books", Name: "Kids' books", Roles: []idp.Role{idp.Kid}}
		other, err := l.EnsureLDAP(ctx, books)
		if err != nil {
			t.Fatal(err)
		}
		if other.BaseDN == creds.BaseDN {
			t.Fatalf("two apps share a base DN: %s", other.BaseDN)
		}
		if err := ready(other, "kiko", kiko); err != nil {
			t.Errorf("a kid, in a kids' app: %v", err)
		}
		if err := w.Bind(t, dn(other, "lena"), lena); err == nil {
			t.Error("an adult bound to a kids' app")
		}
		if err := w.Bind(t, dn(creds, "lena"), lena); err != nil {
			t.Errorf("the adult, still, in the first app: %v", err)
		}
		if err := l.DeleteLDAP(ctx, books.Slug); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a change of role holds at the next bind", func(t *testing.T) {
		kid := people[0] // lena
		kid.Role = idp.Kid
		if err := p.EnsurePerson(ctx, kid); err != nil {
			t.Fatal(err)
		}
		if err := w.Bind(t, dn(creds, "lena"), lena); err == nil {
			t.Error("an adult who became a kid still binds to an adults' app")
		}
		kid.Role = idp.Adult
		if err := p.EnsurePerson(ctx, kid); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("the audience changes, and the app is never open to everyone on the way (#219)", func(t *testing.T) {
		media.Roles = []idp.Role{idp.Kid}
		if _, err := l.EnsureLDAP(ctx, media); err != nil {
			t.Fatal(err)
		}
		if err := w.Bind(t, dn(creds, "lena"), lena); err == nil {
			t.Error("an adult binds to an app that is the kids' now")
		}
		if err := w.Bind(t, dn(creds, "kiko"), kiko); err != nil {
			t.Errorf("a kid, in an app that is the kids' now: %v", err)
		}
		media.Roles = []idp.Role{idp.Adult}
		if _, err := l.EnsureLDAP(ctx, media); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a deleted provider: from the moment it is deleted, nobody binds there, and deleting again is done", func(t *testing.T) {
		type try struct{ who, password string }
		for _, x := range []try{{"lena", lena}, {"maria", maria}} {
			if err := w.Bind(t, dn(creds, x.who), x.password); err != nil {
				t.Fatalf("%s, of the audience, before the delete: %v", x.who, err)
			}
		}
		// Those outside the audience try all along, from before the delete, three clients each; once it returns, the
		// audience too, until 15 s after. A window of a moment would show.
		var (
			wg     sync.WaitGroup
			mu     sync.Mutex
			seen   = map[string]int{}
			after  = make(chan struct{})
			finish = make(chan struct{})
		)
		trying := func(x try, from <-chan struct{}, when string) {
			defer wg.Done()
			if from != nil {
				select {
				case <-from:
				case <-finish:
					return
				}
			}
			for {
				select {
				case <-finish:
					return
				default:
				}
				if w.Bind(t, dn(creds, x.who), x.password) == nil {
					mu.Lock()
					seen[x.who+" "+when]++
					mu.Unlock()
				}
			}
		}
		for range 3 {
			for _, x := range []try{{"kiko", kiko}, {"olga", olga}} {
				wg.Add(2)
				go trying(x, nil, "(outside the audience)")
				go trying(x, after, "(outside the audience, after the delete returned)")
			}
			for _, x := range []try{{"lena", lena}, {"maria", maria}} {
				wg.Add(1)
				go trying(x, after, "(after the delete returned)")
			}
		}
		err := l.DeleteLDAP(ctx, media.Slug)
		close(after)
		time.Sleep(15 * time.Second)
		close(finish)
		wg.Wait()
		if err != nil {
			t.Fatal(err)
		}
		for who, n := range seen {
			t.Errorf("%s bound to a deleted app, %d times", who, n)
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
