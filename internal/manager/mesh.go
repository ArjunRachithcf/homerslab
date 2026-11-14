package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/dns"
	"github.com/ArjunRachithcf/homerslab/internal/manager/setup"
	"github.com/ArjunRachithcf/homerslab/internal/manager/store"
	"github.com/ArjunRachithcf/homerslab/internal/mesh"
	"github.com/ArjunRachithcf/homerslab/internal/mesh/netbird"
)

// NetBird is how the manager reaches NetBird's management API: the netbird package, or a test's stand-in.
type NetBird interface {
	Probe(ctx context.Context, url string) (setupRequired bool, err error)
	Setup(ctx context.Context, url string, o netbird.Owner) (netbird.SetupToken, error)
	RevokeSetup(ctx context.Context, url string, st netbird.SetupToken) error
	Adopt(ctx context.Context, url string, given mesh.Secret) (netbird.Adopted, error)
	Open(url string, t mesh.Token) (mesh.Mesh, error)
}

type realNetBird struct{}

func (realNetBird) Probe(ctx context.Context, url string) (bool, error) {
	return netbird.Probe(ctx, url)
}
func (realNetBird) Setup(ctx context.Context, url string, o netbird.Owner) (netbird.SetupToken, error) {
	return netbird.Setup(ctx, url, o)
}
func (realNetBird) RevokeSetup(ctx context.Context, url string, st netbird.SetupToken) error {
	return netbird.RevokeSetup(ctx, url, st)
}
func (realNetBird) Adopt(ctx context.Context, url string, given mesh.Secret) (netbird.Adopted, error) {
	return netbird.Adopt(ctx, url, given)
}
func (realNetBird) Open(url string, t mesh.Token) (mesh.Mesh, error) { return netbird.Open(url, t) }

// The manager's own credentials, sealed by the agent.
const (
	credMeshToken = "manager-mesh-token"
	credMeshOwner = "manager-mesh-owner-password"
	// credMeshSetup is a new NetBird's one-day setup token, kept only until HomersLab's own token works, so a setup
	// that stopped half-way resumes.
	credMeshSetup = "manager-mesh-setup-token"
)

// The network step's answers that the manager keeps. None is secret.
const (
	ansMesh         = "mesh"
	ansControlPlane = "meshControlPlane"
	ansURL          = "meshUrl"
	ansTokenUser    = "meshTokenUser"
	ansTokenID      = "meshTokenId"
	ansTokenExpires = "meshTokenExpires"
	ansPrimaryIP    = "primaryMeshIp"
	// ansSetupUser is the owner whose setup token is sealed, while a bootstrap is under way; ansBootstrapped says
	// HomersLab set this NetBird up itself, so its Default policy is HomersLab's to delete.
	ansSetupUser    = "meshSetupUser"
	ansBootstrapped = "meshBootstrapped"
)

// network is the LAN page's network step: the mesh and where its control plane is. It carries no secret. A new
// self-hosted NetBird is set up right here, in one step; otherwise the step waits for the token, which the admin
// gives once in this machine's terminal (`sudo homerslab setup secret mesh`, #64). When the account has policies of
// its own that reach every peer, the step waits for the admin's yes (switchOff) before the primary joins.
func (m *Manager) network(ctx context.Context, raw json.RawMessage, earlier map[string]string) (map[string]string, error) {
	var a struct {
		Mesh         string `json:"mesh"`
		ControlPlane string `json:"controlPlane"`
		URL          string `json:"url"`
		SwitchOff    bool   `json:"switchOff"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return nil, &setup.Invalid{Fields: []string{"answer"}}
	}
	if a.Mesh != "netbird" {
		return nil, &setup.Invalid{Fields: []string{"mesh"}} // Tailscale and Headscale come with their own tickets
	}
	keep := map[string]string{ansMesh: a.Mesh, ansControlPlane: a.ControlPlane}
	switch a.ControlPlane {
	case "cloud":
		keep[ansURL] = netbird.CloudURL
	case "self-hosted":
		u, err := mesh.CheckURL(strings.TrimSpace(a.URL))
		if err != nil {
			return nil, &setup.Invalid{Fields: []string{"url"}}
		}
		keep[ansURL] = u.String()
	default:
		return nil, &setup.Invalid{Fields: []string{"controlPlane"}}
	}
	url := keep[ansURL]
	if earlier[ansPrimaryIP] != "" {
		// Joined already. Answering again changes nothing; moving to another mesh is a guided migration of its own.
		if earlier[ansURL] != url {
			return nil, &setup.Invalid{Fields: []string{"controlPlane"}}
		}
		return keep, nil
	}
	bootstrapped := earlier[ansURL] == url && earlier[ansBootstrapped] == "true"
	if earlier[ansURL] == url && earlier[ansTokenID] != "" {
		// HomersLab has its own token here already (the step waits for the admin's yes, or an earlier try stopped
		// half-way): carry on with it. A setup token still kept from a bootstrap goes, as far as it can.
		if t, err := m.currentToken(ctx, earlier); err == nil {
			if earlier[ansSetupUser] != "" {
				m.endSetupToken(ctx, url, earlier[ansSetupUser])
				delete(keep, ansSetupUser)
			}
			return m.joinMesh(ctx, url, t, keep, earlier, a.SwitchOff, bootstrapped)
		}
	}
	if a.ControlPlane == "cloud" {
		return nil, &setup.NeedsSecret{Secret: "mesh", Keep: keep}
	}
	var st netbird.SetupToken
	if earlier[ansURL] == url && earlier[ansSetupUser] != "" {
		// A bootstrap stopped half-way: it resumes from the setup token it sealed.
		value, err := m.agent.unseal(ctx, credMeshSetup)
		if err != nil {
			return nil, fmt.Errorf("resuming the new NetBird's setup: %w", err)
		}
		st = netbird.SetupToken{Value: value, UserID: earlier[ansSetupUser]}
	} else {
		required, err := m.netbird.Probe(ctx, url)
		if err != nil {
			m.log.Warn("asking a NetBird server whether it is set up", "url", url, "err", err)
			return nil, &setup.Invalid{Fields: []string{"url"}}
		}
		if !required {
			return nil, &setup.NeedsSecret{Secret: "mesh", Keep: keep}
		}
		// The owner's password is sealed before NetBird hears of the owner, and the one-day setup token before
		// anything else happens: no failure half-way leaves the household without a way into its own NetBird.
		owner := netbird.Owner{Name: earlier["name"], Mail: earlier["mail"], Password: netbird.NewPassword()}
		if err := m.agent.seal(ctx, credMeshOwner, owner.Password); err != nil {
			return nil, err
		}
		st, err = m.netbird.Setup(ctx, url, owner)
		if errors.Is(err, mesh.ErrAlreadySetUp) {
			return nil, &setup.NeedsSecret{Secret: "mesh", Keep: keep}
		}
		if err != nil {
			return nil, fmt.Errorf("setting up the new NetBird: %w", err)
		}
		if err := m.agent.seal(ctx, credMeshSetup, st.Value); err != nil {
			return nil, err
		}
		if err := m.db.SaveAnswers(ctx, map[string]string{ansMesh: a.Mesh, ansControlPlane: a.ControlPlane, ansURL: url,
			ansSetupUser: st.UserID, ansBootstrapped: "true"}); err != nil {
			return nil, err
		}
	}
	keep[ansBootstrapped] = "true"
	t, err := m.adopt(ctx, url, st.Value, keep)
	if errors.Is(err, mesh.ErrTokenRefused) {
		// The setup token died (it lasts a day) before HomersLab had its own: back to asking the admin for a token,
		// which the owner's password gets at NetBird's dashboard (`sudo homerslab mesh owner-password`).
		m.log.Warn("the new NetBird's setup token was refused: asking for a token instead")
		_ = m.agent.forget(ctx, credMeshSetup)
		keep[ansSetupUser], keep[ansBootstrapped] = "", ""
		return nil, &setup.NeedsSecret{Secret: "mesh", Keep: keep}
	}
	if err != nil {
		return nil, err
	}
	// HomersLab's own token works: the setup token's work is done.
	m.endSetupToken(ctx, url, st.UserID)
	delete(keep, ansSetupUser)
	return m.joinMesh(ctx, url, t, keep, earlier, a.SwitchOff, true)
}

// endSetupToken revokes a new NetBird's setup token and forgets it, as far as it can, without holding setup up:
// HomersLab's own token has taken over. A token that couldn't be revoked is kept, for the next try to revoke; one
// that is refused or gone has ended by itself (it lasts a day).
func (m *Manager) endSetupToken(ctx context.Context, url, userID string) {
	if value, err := m.agent.unseal(ctx, credMeshSetup); err == nil {
		err := m.netbird.RevokeSetup(ctx, url, netbird.SetupToken{Value: value, UserID: userID})
		if err != nil && !errors.Is(err, mesh.ErrTokenRefused) {
			m.log.Warn("revoking the new NetBird's setup token; trying again later, and it expires within a day", "err", err)
			return
		}
	}
	if err := m.agent.forget(ctx, credMeshSetup); err != nil {
		m.log.Warn("forgetting the new NetBird's setup token", "err", err)
	}
	if err := m.db.SaveAnswers(ctx, map[string]string{ansSetupUser: ""}); err != nil {
		m.log.Warn("saving the setup token's end", "err", err)
	}
}

// getOwnerPassword is `sudo homerslab mesh owner-password`, on the control socket only: the new NetBird's owner
// password, HomersLab's way back into NetBird's dashboard when every token is lost.
func (m *Manager) getOwnerPassword(w http.ResponseWriter, r *http.Request) {
	value, err := m.agent.unseal(r.Context(), credMeshOwner)
	if err != nil || value == "" {
		fail(w, http.StatusNotFound, "no-owner-password")
		return
	}
	m.log.Warn("the mesh owner's password was read, on this machine's control socket")
	writeJSON(w, http.StatusOK, map[string]string{"password": value.Reveal()})
}

// meshSecret takes the mesh token from the terminal: HomersLab makes its own service user and token with it, and
// then forgets it.
func (m *Manager) meshSecret(ctx context.Context, value string, earlier map[string]string) (map[string]string, error) {
	url := earlier[ansURL]
	keep := map[string]string{ansMesh: earlier[ansMesh], ansControlPlane: earlier[ansControlPlane], ansURL: url, ansBootstrapped: ""}
	t, err := m.adopt(ctx, url, mesh.Secret(strings.TrimSpace(value)), keep)
	if err != nil {
		return nil, err
	}
	return m.joinMesh(ctx, url, t, keep, earlier, false, false)
}

// adopt makes HomersLab's own token from a given one, keeps it sealed, and only once it has worked revokes
// HomersLab's older tokens: no failure leaves HomersLab without a token that works.
func (m *Manager) adopt(ctx context.Context, url string, given mesh.Secret, keep map[string]string) (mesh.Token, error) {
	a, err := m.netbird.Adopt(ctx, url, given)
	if err != nil {
		return mesh.Token{}, err
	}
	if err := m.keepToken(ctx, a.Token, keep); err != nil {
		return mesh.Token{}, err
	}
	nb, err := m.netbird.Open(url, a.Token)
	if err != nil {
		return mesh.Token{}, err
	}
	if _, err := nb.Report(ctx); err != nil {
		return mesh.Token{}, fmt.Errorf("HomersLab's new mesh token doesn't work: %w", err)
	}
	for _, old := range a.Stale {
		if err := nb.Revoke(ctx, old); err != nil {
			m.log.Warn("revoking HomersLab's older mesh token", "err", err) // it expires by itself; never its body
		}
	}
	return a.Token, nil
}

// keepToken seals HomersLab's own token and keeps what isn't secret of it: from here on it is HomersLab's, and the
// scheduler rotates it.
func (m *Manager) keepToken(ctx context.Context, t mesh.Token, keep map[string]string) error {
	if err := m.agent.seal(ctx, credMeshToken, t.Value); err != nil {
		return err
	}
	if back, err := m.agent.unseal(ctx, credMeshToken); err != nil || back != t.Value {
		return errors.New("the mesh token didn't read back from the agent")
	}
	keep[ansTokenUser], keep[ansTokenID], keep[ansTokenExpires] = t.UserID, t.ID, t.ExpiresAt.UTC().Format(time.RFC3339)
	return m.db.SaveAnswers(ctx, keep)
}

// ansConflicts are the names of the household's own policies that reach a node, as the admin was shown them, and
// ansConflictIDs their IDs, which the admin's yes is for. ansConflictsReported are the IDs the hourly check has
// reported already: a standing conflict is reported once.
const (
	ansConflicts         = "meshConflicts"
	ansConflictIDs       = "meshConflictIds"
	ansConflictsReported = "meshConflictsReported"
)

var notLabel = regexp.MustCompile(`[^a-z0-9-]+`)

// joinMesh makes the groups and policies per role, removes the mesh's Default on a NetBird HomersLab set up itself,
// switches off the household's own policies that reach a node (NetBird's Default among them, elsewhere) once the
// admin said yes to exactly those, and has the primary's agent join with a one-time key; the agent's firewall table
// then keeps the LAN out.
func (m *Manager) joinMesh(ctx context.Context, url string, t mesh.Token, keep, earlier map[string]string, switchOff, bootstrapped bool) (map[string]string, error) {
	nb, err := m.netbird.Open(url, t)
	if err != nil {
		return nil, err
	}
	// The household's port, chosen once, here (ADR 0021): every URL HomersLab writes carries it from now on.
	port := householdPort(earlier)
	if earlier[ansPort] == "" {
		rep, err := nb.Report(ctx)
		if err != nil {
			return nil, fmt.Errorf("what the mesh can do: %w", err)
		}
		if rep.Port > 0 {
			port = rep.Port
		}
	}
	keep[ansPort] = strconv.Itoa(port)
	if err := nb.EnsurePolicies(ctx, port); err != nil {
		return nil, fmt.Errorf("the mesh's groups and policies: %w", err)
	}
	if bootstrapped {
		if err := nb.RemoveDefault(ctx); err != nil {
			return nil, fmt.Errorf("removing the new NetBird's Default policy: %w", err)
		}
	}
	conflicts, err := nb.Conflicts(ctx)
	if err != nil {
		return nil, err
	}
	if len(conflicts) > 0 {
		names, ids := make([]string, len(conflicts)), make([]string, len(conflicts))
		for i, c := range conflicts {
			names[i], ids[i] = c.Name, c.ID
		}
		listed := strings.Join(ids, "\n")
		// A yes is for what the admin saw, by the policies' IDs: one that appeared since, or a policy renamed to
		// look like one shown, asks again.
		if !switchOff || earlier[ansConflictIDs] != listed {
			keep[ansConflicts], keep[ansConflictIDs] = strings.Join(names, "\n"), listed
			return nil, &setup.NeedsConfirm{Confirm: "mesh-policies", Keep: keep}
		}
		if err := nb.Disable(ctx, ids); err != nil {
			return nil, fmt.Errorf("switching off the policies that reach every peer: %w", err)
		}
		m.log.Info("switched off the household's policies that reached every peer, with the admin's yes", "policies", names)
	}
	keep[ansConflicts], keep[ansConflictIDs] = "", ""
	hostname := "primary"
	if f, err := m.agent.node(ctx); err == nil {
		h := notLabel.ReplaceAllString(strings.ToLower(f.Hostname), "-")
		if len(h) > 63 {
			h = h[:63]
		}
		if h = strings.Trim(h, "-"); h != "" {
			hostname = h
		}
	}
	key, err := nb.NewJoinKey(ctx, hostname)
	if err != nil {
		return nil, fmt.Errorf("the primary's join key: %w", err)
	}
	joined, err := m.agent.join(ctx, agent.JoinRequest{Mesh: "netbird", ManagementURL: url, SetupKey: key.Value.Reveal(),
		Hostname: hostname, SetupPort: m.cfg.SetupPort})
	if err != nil {
		return nil, err
	}
	keep[ansPrimaryIP] = joined.IP.String()
	m.log.Info("the primary joined the mesh", "mesh", "netbird", "ip", joined.IP)
	return keep, nil
}

// putSecret is `homerslab setup secret <name>`, on the control socket only: the secret arrives from this machine's
// terminal, is checked, and finishes the step that waits for it. It is never logged, kept or answered.
func (m *Manager) putSecret(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil || strings.TrimSpace(body.Value) == "" {
		fail(w, http.StatusBadRequest, "no-value")
		return
	}
	basis, bad := stepBasis(r)
	if bad != "" {
		status := http.StatusBadRequest
		if bad == "revision-required" {
			status = http.StatusPreconditionRequired
		}
		fail(w, status, bad)
		return
	}
	name := r.PathValue("name")
	m.checkingSecret(name)
	st, err := m.setup.Secret(r.Context(), name, body.Value, basis.Revision)
	if err != nil {
		why := m.secretFailed(w, err)
		if errors.Is(err, setup.ErrStale) || errors.Is(err, setup.ErrNotWaiting) {
			why = "" // it was never checked
		}
		m.checkedSecret(name, why)
		return
	}
	m.checkedSecret(name, "")
	writeJSON(w, http.StatusOK, m.withGiven(st))
}

// rotate replaces HomersLab's mesh token before it expires. The two overlap (#88): the old token is revoked only
// after the new one has worked once, been sealed and read back from the agent. It runs from the scheduler.
func (m *Manager) rotate(ctx context.Context) error {
	ans, err := m.db.Answers(ctx)
	if err != nil {
		return err
	}
	if ans[ansMesh] != "netbird" || ans[ansTokenExpires] == "" {
		return nil
	}
	cur, err := m.currentToken(ctx, ans)
	if err != nil {
		return err
	}
	if m.cfg.Now().Before(cur.ExpiresAt.Add(-mesh.RotateBefore)) {
		return nil
	}
	nb, err := m.netbird.Open(ans[ansURL], cur)
	if err != nil {
		return err
	}
	next, err := nb.Rotate(ctx)
	if err != nil {
		return fmt.Errorf("rotating the mesh token: %w", err)
	}
	discard := func(why error) error {
		if err := nb.Revoke(ctx, next); err != nil {
			m.log.Warn("revoking an unused new mesh token", "err", err)
		}
		return why
	}
	if _, err := nb.With(next).Report(ctx); err != nil {
		return discard(fmt.Errorf("the new mesh token doesn't work: %w", err))
	}
	if err := m.agent.seal(ctx, credMeshToken, next.Value); err != nil {
		return discard(err) // the old token is still sealed and works
	}
	if back, err := m.agent.unseal(ctx, credMeshToken); err != nil || back != next.Value {
		if err := m.agent.seal(ctx, credMeshToken, cur.Value); err != nil {
			m.log.Error("putting the old mesh token back", "err", err)
			return fmt.Errorf("the new mesh token didn't read back, and the old one couldn't be put back: %w", err)
		}
		return discard(errors.New("the new mesh token didn't read back from the agent"))
	}
	if err := m.db.SaveAnswers(ctx, map[string]string{ansTokenID: next.ID, ansTokenUser: next.UserID,
		ansTokenExpires: next.ExpiresAt.UTC().Format(time.RFC3339)}); err != nil {
		return err
	}
	if err := nb.With(next).Revoke(ctx, cur); err != nil {
		m.log.Warn("revoking the old mesh token", "err", err) // it expires by itself; never its body
	}
	m.log.Info("rotated the mesh token", "expires", next.ExpiresAt)
	return nil
}

// checkMesh puts HomersLab's policies back if they drifted, and reports policies of the household's own that reach
// every peer, which appeared since the admin's yes. They stay as they are: switching one off takes the admin's yes.
func (m *Manager) checkMesh(ctx context.Context) error {
	ans, err := m.db.Answers(ctx)
	if err != nil {
		return err
	}
	if ans[ansSetupUser] != "" && ans[ansTokenID] != "" {
		m.endSetupToken(ctx, ans[ansURL], ans[ansSetupUser]) // a bootstrap's setup token that couldn't be revoked then
	}
	if ans[ansMesh] != "netbird" || ans[ansPrimaryIP] == "" {
		return nil
	}
	cur, err := m.currentToken(ctx, ans)
	if err != nil {
		return err
	}
	nb, err := m.netbird.Open(ans[ansURL], cur)
	if err != nil {
		return err
	}
	if err := nb.EnsurePolicies(ctx, householdPort(ans)); err != nil {
		return err
	}
	if _, err := m.db.Certificate(ctx, ans[ansDomain]); err == nil && ans[ansDomain] != "" {
		if err := m.ensureNames(ctx, ans); err != nil {
			return err
		}
	}
	conflicts, err := nb.Conflicts(ctx)
	if err != nil {
		return err
	}
	reported := strings.Fields(ans[ansConflictsReported])
	var now []string
	for _, c := range conflicts {
		now = append(now, c.ID)
		if slices.Contains(reported, c.ID) {
			continue // still there, and reported when it appeared
		}
		m.log.Warn("a mesh policy reaches a node, past the policies per role", "policy", c.Name)
		if err := m.db.AddDrift(ctx, store.Drift{At: m.cfg.Now(), Source: "mesh",
			What: fmt.Sprintf("the policy %q reaches a node, past the policies per role", c.Name)}); err != nil {
			return err
		}
	}
	slices.Sort(now)
	return m.db.SaveAnswers(ctx, map[string]string{ansConflictsReported: strings.Join(now, " ")})
}

// getMesh is the mesh's report of what it can do, with what only the nodes can say (router port mapping off, as
// the primary's agent finds it now).
func (m *Manager) getMesh(w http.ResponseWriter, r *http.Request) {
	ans, err := m.db.Answers(r.Context())
	if err != nil {
		m.internal(w, "reading the mesh", err)
		return
	}
	if ans[ansMesh] != "netbird" || ans[ansPrimaryIP] == "" {
		fail(w, http.StatusNotFound, "no-mesh")
		return
	}
	cur, err := m.currentToken(r.Context(), ans)
	if err != nil {
		m.internal(w, "reading the mesh token", err)
		return
	}
	nb, err := m.netbird.Open(ans[ansURL], cur)
	if err != nil {
		m.internal(w, "opening the mesh", err)
		return
	}
	rep, err := nb.Report(r.Context())
	if err != nil {
		m.log.Warn("asking the mesh what it can do", "err", err)
		fail(w, http.StatusBadGateway, "mesh-unreachable")
		return
	}
	node, err := m.agent.meshNode(r.Context())
	rep.PortMappingOff = err == nil && node.PortMappingOff
	writeJSON(w, http.StatusOK, rep)
}

func (m *Manager) getDrift(w http.ResponseWriter, r *http.Request) {
	d, err := m.db.Drifts(r.Context(), 100)
	if err != nil {
		m.internal(w, "reading drift", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// currentToken is HomersLab's mesh token: its value from the agent, the rest from the answers.
func (m *Manager) currentToken(ctx context.Context, ans map[string]string) (mesh.Token, error) {
	expires, err := time.Parse(time.RFC3339, ans[ansTokenExpires])
	if err != nil {
		return mesh.Token{}, err
	}
	value, err := m.agent.unseal(ctx, credMeshToken)
	if err != nil {
		return mesh.Token{}, err
	}
	return mesh.Token{Value: value, UserID: ans[ansTokenUser], ID: ans[ansTokenID], ExpiresAt: expires}, nil
}

// RunDue runs the scheduler's jobs that are due: so far, the mesh token's rotation, the mesh's drift check, the
// certificate's renewal, and the DNS zone's inventory record (#179).
func (m *Manager) RunDue(ctx context.Context) {
	if err := m.giveSetupPort(ctx); err != nil {
		m.log.Error("giving the agent the setup listener's port", "err", err)
	}
	if err := m.rotate(ctx); err != nil {
		m.log.Error("the mesh token's rotation", "err", err)
	}
	renewal := m.renew(ctx)
	if renewal != nil {
		m.log.Error("the certificate's renewal", "err", renewal)
	}
	// Not on the listing that just came back empty: its drift note would alternate with the renewal's at every run,
	// and never collapse into one (#175).
	if !errors.Is(renewal, dns.ErrListingEmpty) {
		if err := m.reconcile(ctx); err != nil {
			m.log.Error("putting the DNS zone's inventory record right", "err", err)
		}
	}
	if err := m.checkMesh(ctx); err != nil {
		m.log.Error("checking the mesh", "err", err)
	}
}

func (m *Manager) schedule(ctx context.Context) {
	t := time.NewTicker(m.cfg.ScheduleEvery)
	defer t.Stop()
	m.RunDue(ctx)
	go m.BackUpDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.RunDue(ctx)
			go m.BackUpDue(ctx) // beside the others: a first backup takes hours
		}
	}
}

// CloseLANListener closes the setup link's LAN listener for good: the firewall table drops its exception, and the
// setup listener answers nothing from then on. The admin's first sign-in over the mesh calls it (#17).
func (m *Manager) CloseLANListener(ctx context.Context) error {
	if err := m.agent.closeSetupListener(ctx); err != nil {
		return err
	}
	if err := m.db.SaveAnswers(ctx, map[string]string{ansLANClosed: m.cfg.Now().UTC().Format(time.RFC3339)}); err != nil {
		return err
	}
	m.lanClosed.Store(true)
	m.log.Info("the setup link's LAN listener is closed, for good")
	return nil
}

const ansLANClosed = "lanClosedAt"

// giveSetupPort gives the primary's agent the setup listener's port, or that it closed for good, at every run (an
// agent may have lost what it kept): the agent keeps it for the firewall table, and for the closed one
// homerslab-firewall-restore loads at the next boot. A refusal because the listener closed for good is an answer too.
func (m *Manager) giveSetupPort(ctx context.Context) error {
	if m.lanClosed.Load() {
		return m.agent.closeSetupListener(ctx)
	}
	if err := m.agent.setupPort(ctx, m.cfg.SetupPort); err != nil && !errors.Is(err, errClosedForGood) {
		return err
	}
	return nil
}
