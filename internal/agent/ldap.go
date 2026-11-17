package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ArjunRachithcf/homeward/internal/appunit"
	"github.com/ArjunRachithcf/homeward/internal/atomicfile"
	"github.com/ArjunRachithcf/homeward/internal/catalog"
	"github.com/ArjunRachithcf/homeward/internal/certs"
	"github.com/ArjunRachithcf/homeward/internal/firewall"
	"github.com/ArjunRachithcf/homeward/internal/idp"
	"github.com/ArjunRachithcf/homeward/internal/ldaps"
	"github.com/ArjunRachithcf/homeward/internal/mesh"
	"github.com/ArjunRachithcf/homeward/internal/text"
)

// LDAP apps on this node (#204, ADR 0020). An app that signs people in over LDAP binds at auth.<main domain>:636, the
// sign-in listener's LDAPS port, from its own network; nothing else of the machine is open to it. The agent:
//   - seals the LDAP outpost's token, which the manager hands it inward only, for the outpost's own unit
//     (homeward-ldap.service), and writes that unit's drop-in;
//   - seals the sign-in's certificate and key again under the names the LDAPS port's unit (homeward-ldaps.service)
//     loads them by, writes its drop-in with the sign-in's name, enables its socket once an LDAP app is installed, and
//     restarts it when the certificate is renewed, so it serves the new one;
//   - hands the app its directory as environment variables of its upstream's service: the listener's URL, its own base
//     DN, the DN template a person binds with, and the admins' group. None of it is secret: a person binds with their
//     own app password, made for that app alone;
//   - lets that app's network, and no other, reach the LDAPS port in the firewall table.
const (
	LDAPUnit    = "homeward-ldap.service"
	LDAPSUnit   = "homeward-ldaps.service"
	LDAPSSocket = "homeward-ldaps.socket"
	// credLDAPToken is the outpost's token, as its unit loads it; ldapRun the unit's RuntimeDirectory, where its
	// container reads it, as Authentik's image's user (outpostUID).
	credLDAPToken = "authentik.ldap-token"
	ldapRun       = "/run/homeward-ldap"
	outpostUID    = "1000"
	// The LDAPS port's unit's credentials: the sign-in's certificate chain and its key.
	credLDAPSCert = "tls.crt"
	credLDAPSKey  = "tls.key"
)

// LDAPRequest is the LDAP outpost as the manager wants it: its token, which travels inward only.
type LDAPRequest struct {
	Token string `json:"token"`
}

// AppLDAP is an app's directory at the sign-in (#204), and how the app takes it (the manifest's ldap): the settings as
// environment variables of its upstream's service. None of it is secret.
type AppLDAP struct {
	Env catalog.LDAPEnv `json:"env"`
	// BaseDN is the app's own base DN at the outpost, dc=<app>,dc=homerslab; UserDN the DN template a person binds with.
	// Empty when the manager only checks the app (?check=true), before the directory is made.
	BaseDN string `json:"baseDn"`
	UserDN string `json:"userDn"`
	// RefusePaths are the app's own other ways to sign in, which the edge refuses before the app sees them.
	RefusePaths []string `json:"refusePaths,omitempty"`
}

func (a *Agent) ldapDropIn() string {
	return filepath.Join("etc/systemd/system", LDAPUnit+".d", "50-homeward-credentials.conf")
}

func (a *Agent) ldapsDropIn() string {
	return filepath.Join("etc/systemd/system", LDAPSUnit+".d", "50-homeward-certificates.conf")
}

func (a *Agent) ldapsDir() string { return filepath.Join(a.cfg.StateDir, "ldaps") }

// tokenText is an outpost's token as the agent takes one: printable ASCII, one word.
var tokenText = regexp.MustCompile(`^[\x21-\x7e]{16,512}$`)

// ldapCredentials is the outpost unit's drop-in: its token, sealed by the agent, decrypted by systemd as the unit
// starts, copied for the outpost's container user, and labelled at the sign-in's level.
func (a *Agent) ldapCredentials() []byte {
	var b strings.Builder
	b.WriteString("# Written by Homeward's agent: the LDAP outpost's token, sealed by the agent (ADR 0011). Never edit by hand.\n[Service]\n")
	fmt.Fprintf(&b, "LoadCredentialEncrypted=%s:%s\n", credLDAPToken, a.signInCred(credLDAPToken))
	fmt.Fprintf(&b, "ExecStartPre=/usr/bin/install -m 0400 -o %s -g %s %%d/%s %s/ldap-token\n", outpostUID, outpostUID, credLDAPToken, ldapRun)
	if a.selinux() {
		fmt.Fprintf(&b, "ExecStartPre=/usr/bin/chcon -t container_file_t -l %s %s/ldap-token\n", signInLevel, ldapRun)
	}
	return []byte(b.String())
}

// putSignInLDAP takes the LDAP outpost's token from the manager, over this socket only: it becomes a credential the
// agent seals for the outpost's unit, which systemd enables and starts. The same token again changes nothing, so the
// outpost keeps serving; a new one restarts it, since the outpost reads its token only as it starts.
func (a *Agent) putSignInLDAP(w http.ResponseWriter, r *http.Request) {
	var req LDAPRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "not-json")
		return
	}
	if !tokenText.MatchString(req.Token) {
		fail(w, http.StatusBadRequest, "bad-token")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.SignIn == nil {
		fail(w, http.StatusConflict, "no-sign-in")
		return
	}
	ctx := r.Context()
	same := false
	if old, err := a.unsealAt(ctx, a.signInCred(credLDAPToken), credLDAPToken); err == nil {
		same = bytes.Equal(old, []byte(req.Token))
	}
	dropIn := a.ldapCredentials()
	had, _ := os.ReadFile(filepath.Join(a.cfg.Root, a.ldapDropIn()))
	if same && bytes.Equal(had, dropIn) && a.state.SignIn.LDAP {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !same {
		if err := os.MkdirAll(a.signInDir(), 0o700); err != nil {
			a.log.Error("the LDAP outpost's credentials", "err", err)
			fail(w, http.StatusInternalServerError, "seal-failed")
			return
		}
		if err := a.sealAt(ctx, a.signInCred(credLDAPToken), credLDAPToken, []byte(req.Token)); err != nil {
			a.log.Error("sealing the LDAP outpost's token", "err", err)
			fail(w, http.StatusInternalServerError, "seal-failed")
			return
		}
	}
	next := *a.state.SignIn
	next.LDAP = true
	prev := a.state.SignIn
	a.state.SignIn = &next
	if err := a.save(); err != nil {
		a.state.SignIn = prev
		a.log.Error("saving the agent's state", "err", err)
		fail(w, http.StatusInternalServerError, "state-unsaved")
		return
	}
	for _, step := range []func() error{
		func() error { return a.writeAt(a.ldapDropIn(), dropIn) },
		func() error { return a.run(ctx, "systemctl", "daemon-reload") },
		func() error { return a.run(ctx, "systemctl", "enable", LDAPUnit) },
		// Queued, not waited for: compose pulls the outpost's image.
		func() error { return a.run(ctx, "systemctl", "--no-block", "restart", LDAPUnit) },
	} {
		if err := step(); err != nil {
			// Without its drop-in, the next call (the manager's, at its next pass) does every step again.
			_ = os.Remove(filepath.Join(a.cfg.Root, a.ldapDropIn()))
			a.log.Error("starting the LDAP outpost", "err", err)
			fail(w, http.StatusInternalServerError, "ldap-failed")
			return
		}
	}
	a.log.Info("the LDAP outpost's unit runs with its sealed token", "new", !same)
	w.WriteHeader(http.StatusNoContent)
}

// ldapBaseDN is an app's own base DN at the outpost, as the identity provider makes it: dc=<app>,dc=homerslab.
func ldapBaseDN(id string) string { return "dc=" + id + ",dc=homerslab" }

// ldapEnv is an LDAP app's directory, as its environment takes it: what the manifest names, from the base DN the
// manager made for this app and the sign-in this node serves, or the code that refuses the request. Only checking
// (before the manager has made the directory), the DNs may be missing.
func ldapEnv(id string, r AppRequest, sites *EdgeSites, check bool) (*appunit.LDAP, string) {
	l := *r.LDAP
	if sites == nil || sites.SignIn == "" {
		return nil, "no-sign-in" // nowhere to bind
	}
	base := ldapBaseDN(id)
	if check && l.BaseDN == "" && l.UserDN == "" {
		l.BaseDN, l.UserDN = base, "cn=%s,ou=users,"+base
	}
	// Its own base DN, and no other app's: the DN template a person binds with is in it.
	if l.BaseDN != base || l.UserDN != "cn=%s,ou=users,"+base {
		return nil, "bad-sign-in"
	}
	if !validRefused(l.RefusePaths) {
		return nil, "bad-sign-in"
	}
	e := l.Env
	if e.BaseDN == "" || e.UserDN == "" || e.URL == "" && e.Host == "" {
		return nil, "bad-sign-in"
	}
	values := map[string]string{e.URL: fmt.Sprintf("ldaps://%s:%d", sites.SignIn, ldaps.Port), e.Host: sites.SignIn,
		e.Port: fmt.Sprint(ldaps.Port), e.BaseDN: l.BaseDN, e.UserDN: l.UserDN, e.AdminGroup: "cn=" + idp.RoleGroup(idp.Admin) + ",ou=groups," + l.BaseDN}
	env := map[string]string{}
	for _, n := range e.Names() {
		if n == "" {
			continue
		}
		if _, twice := env[n]; twice || !catalog.EnvName.MatchString(n) || values[n] == "" || !text.OneLine(values[n]) {
			return nil, "bad-sign-in"
		}
		env[n] = values[n]
	}
	return &appunit.LDAP{Env: env}, ""
}

// ldapNetworks are the upstream networks of the apps that sign in over LDAP, which the firewall table lets reach the
// LDAPS port. The caller holds a.mu.
func (a *Agent) ldapNetworks() []firewall.LDAPApp {
	var out []firewall.LDAPApp
	for _, s := range a.state.Apps {
		if s.LDAP != nil {
			out = append(out, firewall.LDAPApp{Bridge: appunit.Bridge(slot(s.Level), -1), Subnet: s.Subnet})
		}
	}
	slices.SortFunc(out, func(x, y firewall.LDAPApp) int { return strings.Compare(x.Bridge, y.Bridge) })
	return out
}

// signInDomain is the domain whose certificate covers the sign-in's name (a wildcard of the name's parent), and
// whether the edge holds it. The caller holds a.mu.
func (a *Agent) signInDomain() (string, bool) {
	name := a.signInName()
	if name == "" {
		return "", false
	}
	_, d, _ := strings.Cut(name, ".")
	return d, slices.Contains(a.state.Edge.Domains, d)
}

// ldapInUse says whether an app on this node signs in over LDAP. The caller holds a.mu.
func (a *Agent) ldapInUse() bool {
	for _, s := range a.state.Apps {
		if s.LDAP != nil {
			return true
		}
	}
	return false
}

// ldapsCertificates is the LDAPS port's unit's drop-in: the sign-in's name, and the sign-in's certificate and key,
// sealed by the agent under the names the unit loads them by.
func (a *Agent) ldapsCertificates(name string) []byte {
	var b strings.Builder
	b.WriteString("# Written by Homeward's agent: the sign-in's name, and its certificate, sealed by the agent (ADR 0011). Never edit by hand.\n[Service]\n")
	fmt.Fprintf(&b, "Environment=HOMEWARD_SIGN_IN=%s\n", name)
	for _, id := range []string{credLDAPSCert, credLDAPSKey} {
		fmt.Fprintf(&b, "LoadCredentialEncrypted=%s:%s\n", id, filepath.Join(a.ldapsDir(), id+".cred"))
	}
	return []byte(b.String())
}

// sealLDAPS seals the sign-in's certificate and key for the LDAPS port's unit: both to new files first, then both
// committed, so a failure leaves the old pair whole, never a new certificate beside an old key (#246's review). Until
// it succeeds, the next ldapsFollow seals the edge's copy again; once it has, that one checks the pair, once. The caller
// holds a.mu.
func (a *Agent) sealLDAPS(ctx context.Context, chain []byte, key mesh.Secret) error {
	a.ldapsReseal, a.ldapsVerify = true, false
	if err := os.MkdirAll(a.ldapsDir(), 0o700); err != nil {
		return err
	}
	ids := []string{credLDAPSCert, credLDAPSKey}
	values := map[string][]byte{credLDAPSCert: chain, credLDAPSKey: []byte(key.Reveal())}
	for _, id := range ids {
		path := filepath.Join(a.ldapsDir(), id+".cred")
		if err := a.sealNew(ctx, path, id, values[id]); err != nil {
			for _, id := range ids {
				_ = os.Remove(filepath.Join(a.ldapsDir(), id+".cred.new"))
			}
			return err
		}
	}
	for _, id := range ids {
		if err := atomicfile.Commit(filepath.Join(a.ldapsDir(), id+".cred.new")); err != nil {
			return err
		}
	}
	a.ldapsReseal, a.ldapsVerify = false, true
	return nil
}

// ldapsCopyThere says whether the LDAPS port's sealed copy is whole on disk: both files, and no new one left by a
// sealing that stopped between its commits. It reads no credential.
func (a *Agent) ldapsCopyThere() bool {
	for _, id := range []string{credLDAPSCert, credLDAPSKey} {
		path := filepath.Join(a.ldapsDir(), id+".cred")
		if _, err := os.Stat(path); err != nil {
			return false
		}
		if _, err := os.Stat(path + ".new"); err == nil {
			return false
		}
	}
	return true
}

// ldapsPaired says whether the LDAPS port's sealed copy makes a pair, the domain's. A copy that can't be unsealed is an
// error, for the next check to try again: never taken for one that makes no pair.
func (a *Agent) ldapsPaired(ctx context.Context, domain string) (bool, error) {
	chain, err := a.unsealAt(ctx, filepath.Join(a.ldapsDir(), credLDAPSCert+".cred"), credLDAPSCert)
	if err != nil {
		return false, fmt.Errorf("unsealing the LDAPS port's certificate to check it: %w", err)
	}
	key, err := a.unsealAt(ctx, filepath.Join(a.ldapsDir(), credLDAPSKey+".cred"), credLDAPSKey)
	if err != nil {
		return false, fmt.Errorf("unsealing the LDAPS port's key to check it: %w", err)
	}
	_, err = certs.Parse(domain, chain, mesh.Secret(key))
	return err == nil, nil
}

// ldapsFollow puts the LDAPS port in place once an app on this node signs in over LDAP and the edge holds the sign-in's
// certificate: the certificate sealed for its unit (from the edge's own, the first time, whenever the sign-in's name
// moves, and whenever its sealing failed or stopped halfway), its drop-in, and its socket enabled; then, if any of it is new, the unit,
// if it runs, restarts, so it serves the new one (it reads it only as it starts). Idempotent and cheap when nothing
// changed (it unseals nothing): it runs after every install, every certificate and at every check. The caller holds a.mu.
func (a *Agent) ldapsFollow(ctx context.Context) error {
	d, held := a.signInDomain()
	if !a.ldapInUse() || !held {
		return nil
	}
	want := a.ldapsCertificates(a.signInName())
	have, _ := os.ReadFile(filepath.Join(a.cfg.Root, a.ldapsDropIn()))
	changed := !bytes.Equal(have, want)
	renewed := false
	if changed || a.ldapsReseal || !a.ldapsCopyThere() {
		chain, err := a.unsealAt(ctx, filepath.Join(a.edgeDir(), certID(d)+".cred"), certID(d))
		if err != nil {
			return fmt.Errorf("the sign-in's certificate: %w", err)
		}
		key, err := a.unsealAt(ctx, filepath.Join(a.edgeDir(), keyID(d)+".cred"), keyID(d))
		if err != nil {
			return fmt.Errorf("the sign-in's key: %w", err)
		}
		c, err := certs.Parse(d, chain, mesh.Secret(key))
		if err != nil {
			return fmt.Errorf("the sign-in's certificate: %w", err)
		}
		if err := a.sealLDAPS(ctx, c.Chain, c.Key); err != nil {
			return fmt.Errorf("sealing the sign-in's certificate for the LDAPS port: %w", err)
		}
		renewed = true
	}
	if changed {
		if err := a.writeAt(a.ldapsDropIn(), want); err != nil {
			return err
		}
		for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", LDAPSSocket}} {
			if err := a.run(ctx, "systemctl", args...); err != nil {
				// Without its drop-in, the next call (the next check's at the latest) does every step again.
				_ = os.Remove(filepath.Join(a.cfg.Root, a.ldapsDropIn()))
				return err
			}
		}
	}
	if renewed || changed {
		// A process that started with the old certificate (or name) serves it until it stops: it goes now, and the
		// socket starts it again, with the new one, at the next connection. One that isn't running is left so.
		if err := a.run(ctx, "systemctl", "--no-block", "try-restart", LDAPSUnit); err != nil {
			return err
		}
		a.log.Info("the LDAPS port serves the sign-in's certificate", "name", a.signInName())
	}
	// Once after a sealing, never at every check: an unseal is the TPM's, and brings the key into memory.
	if a.ldapsVerify {
		ok, err := a.ldapsPaired(ctx, d)
		if err != nil {
			return err
		}
		a.ldapsVerify = false
		if !ok {
			a.ldapsReseal = true
			return errors.New("the LDAPS port's copy of the sign-in's certificate makes no pair; sealing it again")
		}
	}
	return nil
}
