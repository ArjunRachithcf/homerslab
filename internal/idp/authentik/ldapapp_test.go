package authentik_test

// An LDAP-only app in its own container (#204, part 2b): it reaches the sign-in only as an LDAP app on a node does,
// at auth.<main domain>:636, which inside its container resolves to the sign-in listener's LDAPS port (the real one,
// `homeward edge ldaps`, this suite's own binary in a container beside it, with the household's certificate from a
// test CA), and the port leads to the real outpost. The app's network is internal: the outpost itself, and anything
// else, it can't reach.
//
// What differs from a node, and why it doesn't change what is checked: the listener is a container on the app's
// network and the sign-in network, not a socket on the node's loopback that the firewall table opens to LDAP apps
// alone (the edge's request matrix checks that); the certificate is a test CA's, which the app trusts by file.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"debug/elf"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/idp"
	"github.com/ArjunRachithcf/homeward/internal/idp/authentik"
	"github.com/ArjunRachithcf/homeward/internal/ldaps"
	"github.com/ArjunRachithcf/homeward/internal/ldaps/ldaptest"
)

// ldapHelper is the variable that makes this suite's binary a role in a container: the listener, or the app.
const ldapHelper = "HOMEWARD_TEST_LDAP_HELPER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(ldapHelper); mode != "" {
		os.Exit(ldapRole(mode))
	}
	os.Exit(m.Run())
}

// ldapAnswer is what the app's container says of its bind and search.
type ldapAnswer struct {
	Error   string           `json:"error,omitempty"`
	Entries []ldaptest.Entry `json:"entries"`
}

// ldapRole is this binary's role in its container, from its environment alone.
func ldapRole(mode string) int {
	switch mode {
	case "listener": // the sign-in listener's LDAPS port, as its unit runs it
		return ldaps.Main(context.Background(), []string{"--name", "auth.example.test", "--cert", "/h/tls.crt", "--key", "/h/tls.key",
			"--upstream", os.Getenv("LDAP_OUTPOST"), "--listen", ":636"}, os.Stderr)
	case "app": // the LDAP app's server: it binds as a person, at the address its settings name, and searches its base
		var a ldapAnswer
		err := func() error {
			pool := x509.NewCertPool()
			ca, err := os.ReadFile("/h/ca.crt")
			if err != nil || !pool.AppendCertsFromPEM(ca) {
				return fmt.Errorf("the CA: %v", err)
			}
			addr := os.Getenv("LDAP_ADDR")
			host, _, _ := net.SplitHostPort(addr)
			raw, err := net.DialTimeout("tcp", addr, 5*time.Second)
			if err != nil {
				return err
			}
			defer raw.Close()
			c := net.Conn(raw)
			if os.Getenv("LDAP_PLAIN") == "" {
				tc := tls.Client(raw, &tls.Config{RootCAs: pool, ServerName: host, MinVersion: tls.VersionTLS12})
				if err := tc.Handshake(); err != nil {
					return err
				}
				c = tc
			}
			a.Entries, err = ldaptest.Search(c, os.Getenv("LDAP_DN"), os.Getenv("LDAP_PASSWORD"), os.Getenv("LDAP_BASE"))
			return err
		}()
		if err != nil {
			a.Error = err.Error()
		}
		_ = json.NewEncoder(os.Stdout).Encode(a)
		return 0
	}
	return 2
}

// ldapAppInItsContainer installs an LDAP-only app as a node would, and binds from inside its container.
func ldapAppInItsContainer(w *world, a *authentik.Authentik) func(*testing.T) {
	return func(t *testing.T) {
		ctx := context.Background()
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		if f, err := elf.Open(self); err != nil || slices.ContainsFunc(f.Progs, func(p *elf.Prog) bool { return p.Type == elf.PT_INTERP }) {
			t.Fatalf("the suite's binary runs in a container only if it is static: build it with CGO_ENABLED=0 (%v)", err)
		}
		app := idp.LDAPApp{Slug: "homerslab-philobiblos", Name: "Philobiblos", Roles: []idp.Role{idp.Adult}}
		other := idp.LDAPApp{Slug: "homerslab-philobiblos-other", Name: "Other", Roles: []idp.Role{idp.Adult}}
		creds, err := a.EnsureLDAP(ctx, app)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.EnsureLDAP(ctx, other); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = a.DeleteLDAP(context.Background(), app.Slug)
			_ = a.DeleteLDAP(context.Background(), other.Slug)
		}()
		lena, kiko, lenaOther := appPassword(t, a, app.Slug, "lena"), appPassword(t, a, app.Slug, "kiko"), appPassword(t, a, other.Slug, "lena")
		own := fmt.Sprintf(creds.UserDN, "lena")
		for deadline := time.Now().Add(150 * time.Second); w.Bind(t, own, lena) != nil; time.Sleep(3 * time.Second) {
			if time.Now().After(deadline) {
				t.Fatal("the outpost never served the app's provider")
			}
		}

		// The listener and the app, in a project of the suite's own: the app's network internal, the listener on it and
		// on the sign-in network, as the node's listener is on the machine and leads to the outpost.
		dir := filepath.Join(w.dir, "ldapapp")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		ca, chain, key, _ := householdCertificate(t, household.Domain)
		bin, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		for name, b := range map[string][]byte{"suite": bin, "ca.crt": []byte(ca), "tls.crt": []byte(chain), "tls.key": []byte(key)} {
			if err := os.WriteFile(filepath.Join(dir, name), b, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		appNet, listener := signInNet+".32/28", signInNet+".34"
		file := `name: ` + project + `-ldapapp
networks:
  app:
    internal: true
    ipam:
      config:
        - subnet: ` + appNet + `
  sign-in:
    external: true
    name: ` + project + `_sign-in
x-role: &role
  image: ` + phoneImage + `
  entrypoint: ["/h/suite"]
  volumes:
    - "` + dir + `:/h:ro,z"
services:
  listener:
    <<: *role
    environment:
      ` + ldapHelper + `: listener
      LDAP_OUTPOST: ` + outpostLDAP + `
    networks:
      app:
        ipv4_address: ` + listener + `
      sign-in: {}
  app:
    <<: *role
    profiles: [app]
    environment:
      ` + ldapHelper + `: app
    extra_hosts:
      - "auth.example.test=` + listener + `"
    networks:
      app: {}
`
		if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
		compose := func(args ...string) *exec.Cmd {
			c := exec.Command("docker", append([]string{"compose", "-p", project + "-ldapapp", "-f", filepath.Join(dir, "compose.yaml")}, args...)...)
			c.Env = append(os.Environ(), "DOCKER_HOST="+w.engine)
			return c
		}
		_ = compose("--profile", "app", "down", "-v", "--remove-orphans").Run()
		defer func() {
			if t.Failed() {
				out, _ := compose("logs", "--no-color", "--tail", "40").CombinedOutput()
				t.Logf("the listener's logs:\n%s", out)
			}
			_ = compose("--profile", "app", "down", "-v", "--remove-orphans").Run()
		}()
		if out, err := compose("up", "-d", "--quiet-pull", "listener").CombinedOutput(); err != nil {
			t.Fatalf("the listener: %v\n%s", err, out)
		}
		// bind runs the app's container once: a bind at addr as dn, then a search of base.
		bind := func(t *testing.T, addr, dn, password, base string) ldapAnswer {
			t.Helper()
			plain := ""
			if addr == outpostLDAP {
				plain = "1" // the outpost speaks plain LDAP: were it reachable, this would bind
			}
			c := compose("run", "--rm", "-T", "-e", "LDAP_PLAIN="+plain, "-e", "LDAP_ADDR="+addr, "-e", "LDAP_DN="+dn, "-e", "LDAP_PASSWORD="+password, "-e", "LDAP_BASE="+base, "app")
			c.Stderr = io.Discard
			out, err := c.Output()
			var a ldapAnswer
			if err != nil || json.Unmarshal(out, &a) != nil {
				t.Fatalf("the app's container: %v %s", err, out)
			}
			return a
		}
		at := "auth.example.test:636"
		var got ldapAnswer
		for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(2 * time.Second) {
			if got = bind(t, at, own, lena, creds.BaseDN); got.Error == "" || time.Now().After(deadline) {
				break
			}
		}
		if got.Error != "" {
			t.Fatalf("lena, from the app's container, at %s: %s", at, got.Error)
		}
		t.Logf("lena, from the app's container: %d entries", len(got.Entries))
		// Her own entry, her role's group with her alone in it, her own group of one, the tree: nobody else.
		base := strings.ToLower(creds.BaseDN)
		for _, e := range got.Entries {
			d := strings.ToLower(e.DN)
			switch {
			case d == strings.ToLower(own), d == base, d == "ou=users,"+base, d == "ou=groups,"+base, d == "ou=virtual-groups,"+base,
				d == "cn=lena,ou=virtual-groups,"+base:
			case d == "cn="+idp.RoleGroup(idp.Adult)+",ou=groups,"+base:
				for _, m := range e.Attrs["member"] {
					if strings.ToLower(m) != strings.ToLower(own) {
						t.Errorf("her role's group lists %s", m)
					}
				}
			default:
				t.Errorf("the app reads %s", e.DN)
			}
			for _, vs := range e.Attrs {
				for _, v := range vs {
					for _, other := range []string{"maria", "kiko", "olga", "akadmin"} {
						if strings.Contains(strings.ToLower(v), "cn="+other+",") {
							t.Errorf("the app reads %s in %s", other, e.DN)
						}
					}
				}
			}
		}
		if !slices.ContainsFunc(got.Entries, func(e ldaptest.Entry) bool { return strings.EqualFold(e.DN, own) }) {
			t.Errorf("lena's own entry isn't there: %+v", got.Entries)
		}
		for _, c := range []struct {
			what, addr, dn, password string
		}{
			{"a kid, outside the app's audience", at, fmt.Sprintf(creds.UserDN, "kiko"), kiko},
			{"lena, with the app password of another app", at, own, lenaOther},
			{"lena, with a wrong app password", at, own, lena[:len(lena)-1]},
			{"lena, at the outpost itself", outpostLDAP, own, lena},
		} {
			if a := bind(t, c.addr, c.dn, c.password, creds.BaseDN); a.Error == "" {
				t.Errorf("%s: bound, and read %d entries", c.what, len(a.Entries))
			}
		}
	}
}
