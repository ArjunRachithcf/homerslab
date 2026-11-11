package manager

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/idp"
	"github.com/ArjunRachithcf/homerslab/internal/idp/authentik"
	"github.com/ArjunRachithcf/homerslab/internal/manager/setup"
	"github.com/ArjunRachithcf/homerslab/internal/mesh"
)

// SignIn is how the manager reaches the household account's provider: the authentik package, or a test's stand-in.
// upstream is where the provider's server answers on this machine (the sign-in network's address).
type SignIn interface {
	Ready(ctx context.Context, upstream string, h idp.Household) error
	// Bootstrap makes HomersLab's own token with the bootstrap token, and hands it to keep before the bootstrap token
	// stops working: a token that couldn't be kept is never the only one.
	Bootstrap(ctx context.Context, upstream string, h idp.Household, bootstrap idp.Secret, keep func(idp.Secret) error) (idp.Secret, error)
	Open(upstream string, h idp.Household, token idp.Secret, landing string) (idp.Provider, error)
}

type realSignIn struct{}

func (realSignIn) Ready(ctx context.Context, upstream string, h idp.Household) error {
	return authentik.Ready(ctx, upstream, h)
}
func (realSignIn) Bootstrap(ctx context.Context, upstream string, h idp.Household, b idp.Secret, keep func(idp.Secret) error) (idp.Secret, error) {
	return authentik.Bootstrap(ctx, upstream, h, b, keep)
}
func (realSignIn) Open(upstream string, h idp.Household, token idp.Secret, landing string) (idp.Provider, error) {
	return authentik.Open(upstream, h, token, landing)
}

// The manager's credentials for the household account, sealed by the agent.
const (
	// credSignInToken is HomersLab's own token in the provider; credBootstrap the bootstrap token the manager made,
	// kept only until Authentik has taken it, so a bootstrap that stopped half-way resumes with it.
	credSignInToken = "manager-sign-in-token"
	credBootstrap   = "manager-sign-in-bootstrap"
	// credAdminClient is the secret of Admin's OIDC client.
	credAdminClient = "manager-admin-client-secret"
)

// The answers the sign-in keeps. None is secret.
const (
	ansProvider           = "signInProvider"
	ansSignInBootstrapped = "signInBootstrapped"
	ansServed             = "signInServed" // the edge serves auth.<main domain>
	ansAdminUser          = "adminUser"    // the first admin's username in the household account
	ansClientID           = "adminClientId"
	ansIssuer             = "adminIssuer"
	ansSignedInAt         = "adminSignedInAt"  // the first sign-in to Admin, over the mesh
	ansDeviceKey          = "adminDeviceKeyId" // the mesh key last handed over for the admin\'s device, by its ID
)

// adminSlug is Admin's OIDC client in the provider.
const adminSlug = "homerslab-admin"

// signin is the LAN page's sign-in step: the household account's provider, chosen in plain words. Authentik is the
// only one until Kanidm's ticket (#33).
func (m *Manager) signin(_ context.Context, raw json.RawMessage, _ map[string]string) (map[string]string, error) {
	var a struct {
		Provider string `json:"provider"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return nil, &setup.Invalid{Fields: []string{"answer"}}
	}
	if a.Provider != "authentik" {
		return nil, &setup.Invalid{Fields: []string{"provider"}}
	}
	return map[string]string{ansProvider: a.Provider}, nil
}

// household is the sign-in as the answers make it.
func household(ans map[string]string) idp.Household {
	return idp.Household{Name: ans["household"], Domain: ans[ansDomain], Port: householdPort(ans)}
}

func (m *Manager) upstream() string {
	if m.cfg.SignInUpstream != "" {
		return m.cfg.SignInUpstream
	}
	return "http://" + agent.SignInUpstream
}

var notUsername = regexp.MustCompile(`[^a-z0-9._-]+`)

// reserved are usernames the provider or HomersLab keep for themselves.
var reserved = map[string]bool{"homerslab": true, "akadmin": true, "admin": true, "authentik": true, "root": true}

// adminUsername is the first admin's username: their mail address's local part, as the provider takes it.
func adminUsername(mail string) string {
	local, _, _ := strings.Cut(strings.ToLower(mail), "@")
	u := strings.Trim(notUsername.ReplaceAllString(local, "-"), "-.")
	if len(u) > 60 {
		u = u[:60]
	}
	switch {
	case u == "":
		u = "first-admin"
	case reserved[u] || strings.HasPrefix(u, "ak-"):
		u += "-admin" // never one of HomersLab's or the provider's own accounts
	}
	return u
}

func newBootstrapToken() idp.Secret {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return idp.Secret(base64.RawURLEncoding.EncodeToString(b))
}

var errSignIn = errors.New("the household account couldn't be set up")

// ensureSignIn makes the household account and puts it on the edge, once the first half is done:
//  1. the agent installs Authentik with a bootstrap token the manager made;
//  2. with it, the provider gets HomersLab's own account and token, and its own first-admin flow applies to nobody:
//     akadmin has a random password and is deactivated; the bootstrap token is deleted, and the agent forgets it;
//  3. the household's sign-in: passkey-only sign-in, the passkey-enrolment flow, the groups, the first admin's account
//     (no password, no passkey), and Admin's OIDC client;
//  4. only then does the edge serve auth.<main domain>.
//
// Each step is kept as it is done, so a setup that stopped resumes.
func (m *Manager) ensureSignIn(ctx context.Context) (idp.Provider, error) {
	m.signInMu.Lock()
	defer m.signInMu.Unlock()
	ans, err := m.db.Answers(ctx)
	if err != nil {
		return nil, err
	}
	if ans[ansProvider] != "authentik" || ans[ansDomain] == "" {
		return nil, fmt.Errorf("%w: no provider or domain chosen yet", errSignIn)
	}
	h := household(ans)
	up := m.upstream()
	wait := func(what string, try func() error) error {
		deadline := time.Now().Add(m.cfg.SignInWait) // real time: a provider starting takes what it takes
		for {
			err := try()
			if err == nil || !(errors.Is(err, idp.ErrNotReady) || errors.Is(err, idp.ErrUnreachable)) || !time.Now().Before(deadline) {
				if err != nil {
					return fmt.Errorf("%w: %s: %w", errSignIn, what, err)
				}
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(m.cfg.SignInPoll):
			}
		}
	}
	if ans[ansSignInBootstrapped] == "" {
		bt, err := m.agent.unseal(ctx, credBootstrap)
		if errors.Is(err, errNotSealed) || (err == nil && bt == "") {
			bt = newBootstrapToken()
			// Kept before Authentik hears of it: a restart half-way resumes with the same token.
			if err := m.agent.seal(ctx, credBootstrap, bt); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
		if _, err := m.agent.signIn(ctx, agent.SignInRequest{Provider: "authentik", BootstrapToken: bt.Reveal()}); err != nil {
			return nil, fmt.Errorf("%w: the agent didn't start it: %v", errSignIn, err)
		}
		m.log.Info("the household account's provider is starting: the first start takes a few minutes")
		if err := wait("waiting for it to start", func() error { return m.cfg.SignIn.Ready(ctx, up, h) }); err != nil {
			return nil, err
		}
		// HomersLab's own token is sealed, and read back, before the bootstrap token stops working.
		keep := func(own idp.Secret) error {
			if err := m.agent.seal(ctx, credSignInToken, own); err != nil {
				return err
			}
			if back, err := m.agent.unseal(ctx, credSignInToken); err != nil || back != own {
				return errors.New("HomersLab's sign-in token didn't read back from the agent")
			}
			return nil
		}
		err = wait("bootstrapping it", func() error {
			_, err := m.cfg.SignIn.Bootstrap(ctx, up, h, bt, keep)
			return err
		})
		if errors.Is(err, idp.ErrTokenRefused) {
			// Bootstrapped already, before this manager could keep the answer: its own token was sealed then.
			if own, uerr := m.agent.unseal(ctx, credSignInToken); uerr != nil || own == "" {
				return nil, fmt.Errorf("%w: the bootstrap token was refused, and HomersLab has no token of its own", errSignIn)
			}
		} else if err != nil {
			return nil, err
		}
		if err := m.db.SaveAnswers(ctx, map[string]string{ansSignInBootstrapped: m.cfg.Now().UTC().Format(time.RFC3339)}); err != nil {
			return nil, err
		}
		m.log.Info("the household account is HomersLab's: its own first-admin flow applies to nobody")
	}
	// The bootstrap token's work is done: the agent and the manager forget it, again at every call until they have.
	if s, err := m.agent.signInStatus(ctx); err == nil && s.Bootstrapping {
		if err := m.agent.forgetBootstrap(ctx); err != nil {
			m.log.Warn("the agent didn't forget the bootstrap token yet (Authentik deleted it already); trying again next time", "err", err)
		}
	}
	if err := m.agent.forget(ctx, credBootstrap); err != nil {
		m.log.Warn("forgetting the bootstrap token", "err", err)
	}
	own, err := m.agent.unseal(ctx, credSignInToken)
	if err != nil {
		return nil, err
	}
	p, err := m.cfg.SignIn.Open(up, h, own, AdminURL(ans)+"/")
	if err != nil {
		return nil, err
	}
	if err := wait("preparing the sign-in", func() error { return p.Prepare(ctx, h) }); err != nil {
		return nil, err
	}
	user := ans[ansAdminUser]
	if user == "" {
		user = adminUsername(ans["mail"])
	}
	if err := p.EnsurePerson(ctx, idp.Person{Username: user, Name: ans["name"], Mail: ans["mail"], Role: idp.Admin}); err != nil {
		return nil, fmt.Errorf("%w: the first admin's account: %v", errSignIn, err)
	}
	creds, err := p.EnsureClient(ctx, idp.Client{Slug: adminSlug, Name: "Admin", RedirectURIs: []string{AdminURL(ans) + "/auth/callback"},
		Roles: []idp.Role{idp.Admin}, PasskeyOnly: true})
	if err != nil {
		return nil, fmt.Errorf("%w: Admin's client: %v", errSignIn, err)
	}
	if have, err := m.agent.unseal(ctx, credAdminClient); err != nil || have != creds.Secret {
		if err := m.agent.seal(ctx, credAdminClient, creds.Secret); err != nil {
			return nil, err
		}
	}
	if err := m.db.SaveAnswers(ctx, map[string]string{ansAdminUser: user, ansClientID: creds.ID, ansIssuer: creds.Issuer}); err != nil {
		return nil, err
	}
	if ans[ansServed] == "" {
		if err := m.agent.edgeSites(ctx, agent.EdgeSites{Port: householdPort(ans), Admin: AdminName(ans[ansDomain]), SignIn: h.SignIn()}); err != nil {
			return nil, fmt.Errorf("%w: %v", errEdge, err)
		}
		if err := m.db.SaveAnswers(ctx, map[string]string{ansServed: m.cfg.Now().UTC().Format(time.RFC3339)}); err != nil {
			return nil, err
		}
		m.log.Info("the edge serves the sign-in", "name", h.SignIn())
	}
	return p, nil
}

// JoinHandOver is what `sudo homerslab setup join` shows in this machine's terminal: a one-off mesh key for the
// admin's device, and the one-time link that makes the admin's passkey at the sign-in's own address. Both are
// credentials, so they go only to the control socket, never over the LAN's plain HTTP (#64).
type JoinHandOver struct {
	DeviceKey        mesh.Secret `json:"-"`
	DeviceKeyExpires time.Time   `json:"deviceKeyExpiresAt,omitzero"`
	Enrolment        idp.Secret  `json:"-"`
	EnrolmentExpires time.Time   `json:"enrolmentExpiresAt"`
	SignIn           string      `json:"signIn"`
	Admin            string      `json:"admin"`
	Household        string      `json:"household"`
	Username         string      `json:"username"`
	ClaimedFrom      string      `json:"claimedFrom,omitempty"`
}

// MarshalJSON hands the two credentials over: the control socket is the one place this type is ever answered on.
func (j JoinHandOver) MarshalJSON() ([]byte, error) {
	type plain JoinHandOver
	return json.Marshal(struct {
		plain
		DeviceKey string `json:"deviceKey,omitempty"`
		Enrolment string `json:"enrolment"`
	}{plain(j), j.DeviceKey.Reveal(), j.Enrolment.Reveal()})
}

// postJoin is `sudo homerslab setup join`, on the control socket only: first-time setup's second half begins. The
// household account is set up (it takes minutes the first time), and the admin gets a key for their device and the
// link for their first passkey, both once and for a while. Asked again, it makes a new key and a new link, which ends
// the earlier link.
func (m *Manager) postJoin(w http.ResponseWriter, r *http.Request) {
	m.handOver(w, r, true)
}

// postPasskey is `sudo homerslab setup passkey`: another enrolment link for the first admin, for their second passkey,
// until first-time setup is done.
func (m *Manager) postPasskey(w http.ResponseWriter, r *http.Request) {
	m.handOver(w, r, false)
}

func (m *Manager) handOver(w http.ResponseWriter, r *http.Request, join bool) {
	ctx := r.Context()
	st, err := m.setup.State(ctx)
	if err != nil {
		m.internal(w, "reading setup", err)
		return
	}
	if st.Half != setup.Mesh {
		fail(w, http.StatusConflict, "lan-half-open") // mesh connected, certificate issued first
		return
	}
	if done(st, "done") {
		fail(w, http.StatusConflict, "setup-done") // from now on: homerslab admin recover (#87)
		return
	}
	if !join && !done(st, "passkey") {
		fail(w, http.StatusConflict, "no-first-passkey") // the first passkey comes with setup join
		return
	}
	p, err := m.ensureSignIn(ctx)
	if err != nil {
		m.log.Error("setting up the household account", "err", err)
		fail(w, http.StatusBadGateway, "sign-in-failed")
		return
	}
	ans, err := m.db.Answers(ctx)
	if err != nil {
		m.internal(w, "reading the answers", err)
		return
	}
	h := household(ans)
	out := JoinHandOver{SignIn: h.Origin(), Admin: AdminURL(ans), Household: h.Name, Username: ans[ansAdminUser]}
	if join && ans[ansSignedInAt] == "" {
		t, err := m.currentToken(ctx, ans)
		if err == nil {
			var nb mesh.Mesh
			if nb, err = m.netbird.Open(ans[ansURL], t); err == nil {
				var k mesh.JoinKey
				if k, err = nb.NewDeviceKey(ctx, mesh.Admin, "first"); err == nil {
					out.DeviceKey, out.DeviceKeyExpires = k.Value, k.ExpiresAt
					// A new key ends the one shown before, as a new link ends the earlier link.
					if old := ans[ansDeviceKey]; old != "" && old != k.ID {
						if rerr := nb.RevokeKey(ctx, old); rerr != nil {
							m.log.Warn("revoking the admin's earlier device key; it expires by itself", "err", rerr)
						}
					}
					err = m.db.SaveAnswers(ctx, map[string]string{ansDeviceKey: k.ID})
				}
			}
		}
		if err != nil {
			m.log.Warn("the admin's device key", "err", err)
			fail(w, http.StatusBadGateway, "mesh-unreachable")
			return
		}
	}
	l, err := p.EnrolmentLink(ctx, ans[ansAdminUser])
	if err != nil {
		m.log.Error("the admin's enrolment link", "err", err)
		fail(w, http.StatusBadGateway, "sign-in-failed")
		return
	}
	out.Enrolment, out.EnrolmentExpires = l.URL, l.ExpiresAt
	m.log.Info("handed the admin a one-time passkey link in this machine's terminal", "expires", l.ExpiresAt, "deviceKey", out.DeviceKey != "")
	writeJSON(w, http.StatusOK, out)
}

func done(st setup.State, id string) bool {
	for _, s := range st.Steps {
		if s.ID == id {
			return s.State == "done"
		}
	}
	return false
}

// provider is the household account as HomersLab's own account, once it is set up.
func (m *Manager) provider(ctx context.Context, ans map[string]string) (idp.Provider, error) {
	if ans[ansSignInBootstrapped] == "" {
		return nil, errors.New("the household account isn't set up yet")
	}
	own, err := m.agent.unseal(ctx, credSignInToken)
	if err != nil {
		return nil, err
	}
	return m.cfg.SignIn.Open(m.upstream(), household(ans), own, AdminURL(ans)+"/")
}

// twoWaysIn says whether the first admin can lose a passkey and still get in: two passkeys of their own, or another
// admin who has a passkey (#87).
func twoWaysIn(ctx context.Context, p idp.Provider, first string, keys int) (bool, error) {
	if keys >= 2 {
		return true, nil
	}
	admins, err := p.People(ctx, idp.Admin)
	if err != nil {
		return false, err
	}
	for _, a := range admins {
		if a.Username == first {
			continue
		}
		if ks, err := p.Passkeys(ctx, a.Username); err == nil && len(ks) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// ErrOneWayIn is the last step asked for while the first admin has only one way in.
var ErrOneWayIn = errors.New("one-way-in")

// finish is the last step: first-time setup is done once the first admin has two ways in (#87).
func (m *Manager) finish(ctx context.Context, _ json.RawMessage, earlier map[string]string) (map[string]string, error) {
	p, err := m.provider(ctx, earlier)
	if err != nil {
		return nil, err
	}
	keys, err := p.Passkeys(ctx, earlier[ansAdminUser])
	if err != nil {
		return nil, err
	}
	ok, err := twoWaysIn(ctx, p, earlier[ansAdminUser], len(keys))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrOneWayIn
	}
	return map[string]string{"setupDoneAt": m.cfg.Now().UTC().Format(time.RFC3339)}, nil
}
