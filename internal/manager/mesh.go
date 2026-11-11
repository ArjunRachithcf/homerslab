package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/manager/setup"
	"github.com/ArjunRachithcf/homerslab/internal/manager/store"
	"github.com/ArjunRachithcf/homerslab/internal/mesh"
	"github.com/ArjunRachithcf/homerslab/internal/mesh/netbird"
)

// NetBird is how the manager reaches NetBird's management API: the netbird package, or a test's stand-in.
type NetBird interface {
	Probe(ctx context.Context, url string) (setupRequired bool, err error)
	Bootstrap(ctx context.Context, url string, o netbird.Owner, keep func(mesh.Token) error) (mesh.Token, error)
	Adopt(ctx context.Context, url string, given mesh.Secret) (mesh.Token, error)
	Open(url string, t mesh.Token) (mesh.Mesh, error)
}

type realNetBird struct{}

func (realNetBird) Probe(ctx context.Context, url string) (bool, error) {
	return netbird.Probe(ctx, url)
}
func (realNetBird) Bootstrap(ctx context.Context, url string, o netbird.Owner, keep func(mesh.Token) error) (mesh.Token, error) {
	return netbird.Bootstrap(ctx, url, o, keep)
}
func (realNetBird) Adopt(ctx context.Context, url string, given mesh.Secret) (mesh.Token, error) {
	return netbird.Adopt(ctx, url, given)
}
func (realNetBird) Open(url string, t mesh.Token) (mesh.Mesh, error) { return netbird.Open(url, t) }

// The manager's own credentials, sealed by the agent.
const (
	credMeshToken = "manager-mesh-token"
	credMeshOwner = "manager-mesh-owner-password"
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
	if earlier[ansURL] == url && earlier[ansTokenID] != "" {
		// HomersLab has its own token here already (the step waits for the admin's yes, or an earlier try stopped
		// half-way): carry on with it.
		if t, err := m.currentToken(ctx, earlier); err == nil {
			return m.joinMesh(ctx, url, t, keep, earlier, a.SwitchOff)
		}
	}
	if a.ControlPlane == "cloud" {
		return nil, &setup.NeedsSecret{Secret: "mesh", Keep: keep}
	}
	required, err := m.netbird.Probe(ctx, url)
	if err != nil {
		m.log.Warn("asking a NetBird server whether it is set up", "url", url, "err", err)
		return nil, &setup.Invalid{Fields: []string{"url"}}
	}
	if !required {
		return nil, &setup.NeedsSecret{Secret: "mesh", Keep: keep}
	}
	// The owner's password is sealed before NetBird hears of it, and HomersLab's token before the bootstrap ends: no
	// failure half-way leaves the household without a way into its own NetBird.
	owner := netbird.Owner{Name: earlier["name"], Mail: earlier["mail"], Password: netbird.NewPassword()}
	if err := m.agent.seal(ctx, credMeshOwner, owner.Password); err != nil {
		return nil, err
	}
	t, err := m.netbird.Bootstrap(ctx, url, owner, func(t mesh.Token) error { return m.keepToken(ctx, t, keep) })
	if errors.Is(err, mesh.ErrAlreadySetUp) {
		return nil, &setup.NeedsSecret{Secret: "mesh", Keep: keep}
	}
	if err != nil {
		return nil, fmt.Errorf("setting up the new NetBird: %w", err)
	}
	return m.joinMesh(ctx, url, t, keep, earlier, a.SwitchOff)
}

// meshSecret takes the mesh token from the terminal: HomersLab makes its own service user and token with it, and
// then forgets it.
func (m *Manager) meshSecret(ctx context.Context, value string, earlier map[string]string) (map[string]string, error) {
	given := mesh.Secret(strings.TrimSpace(value))
	url := earlier[ansURL]
	t, err := m.netbird.Adopt(ctx, url, given)
	if err != nil {
		return nil, err
	}
	keep := map[string]string{ansMesh: earlier[ansMesh], ansControlPlane: earlier[ansControlPlane], ansURL: url}
	if err := m.keepToken(ctx, t, keep); err != nil {
		return nil, err
	}
	return m.joinMesh(ctx, url, t, keep, earlier, false)
}

// keepToken seals HomersLab's own token and keeps what isn't secret of it: from here on it is HomersLab's, and the
// scheduler rotates it.
func (m *Manager) keepToken(ctx context.Context, t mesh.Token, keep map[string]string) error {
	if err := m.agent.seal(ctx, credMeshToken, t.Value); err != nil {
		return err
	}
	keep[ansTokenUser], keep[ansTokenID], keep[ansTokenExpires] = t.UserID, t.ID, t.ExpiresAt.UTC().Format(time.RFC3339)
	return m.db.SaveAnswers(ctx, keep)
}

// ansConflicts are the names of the household's own policies that reach every peer, as the admin was shown them.
const ansConflicts = "meshConflicts"

var notLabel = regexp.MustCompile(`[^a-z0-9-]+`)

// joinMesh makes the groups and policies per role (removing the mesh's default), switches off the household's own
// policies that reach every peer once the admin said yes to exactly those, and has the primary's agent join with a
// one-time key; the agent's firewall table then keeps the LAN out.
func (m *Manager) joinMesh(ctx context.Context, url string, t mesh.Token, keep, earlier map[string]string, switchOff bool) (map[string]string, error) {
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
	conflicts, err := nb.Conflicts(ctx)
	if err != nil {
		return nil, err
	}
	if len(conflicts) > 0 {
		names, ids := make([]string, len(conflicts)), make([]string, len(conflicts))
		for i, c := range conflicts {
			names[i], ids[i] = c.Name, c.ID
		}
		listed := strings.Join(names, "\n")
		// A yes is for what the admin saw: a policy that appeared since asks again.
		if !switchOff || earlier[ansConflicts] != listed {
			keep[ansConflicts] = listed
			return nil, &setup.NeedsConfirm{Confirm: "mesh-policies", Keep: keep}
		}
		if err := nb.Disable(ctx, ids); err != nil {
			return nil, fmt.Errorf("switching off the policies that reach every peer: %w", err)
		}
		m.log.Info("switched off the household's policies that reached every peer, with the admin's yes", "policies", names)
	}
	keep[ansConflicts] = ""
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
	joined, err := m.agent.join(ctx, agent.JoinRequest{Mesh: "netbird", ManagementURL: url, SetupKey: key.Value.Reveal(), Hostname: hostname})
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
	st, err := m.setup.Secret(r.Context(), r.PathValue("name"), body.Value)
	if err != nil {
		m.secretFailed(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
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
	for _, c := range conflicts {
		m.log.Warn("a mesh policy reaches every peer, past the policies per role", "policy", c.Name)
		if err := m.db.AddDrift(ctx, store.Drift{At: m.cfg.Now(), Source: "mesh",
			What: fmt.Sprintf("the policy %q reaches every peer, past the policies per role", c.Name)}); err != nil {
			return err
		}
	}
	return nil
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

// RunDue runs the scheduler's jobs that are due: so far, the mesh token's rotation, the mesh's drift check, and the
// certificate's renewal.
func (m *Manager) RunDue(ctx context.Context) {
	if err := m.rotate(ctx); err != nil {
		m.log.Error("the mesh token's rotation", "err", err)
	}
	if err := m.renew(ctx); err != nil {
		m.log.Error("the certificate's renewal", "err", err)
	}
	if err := m.checkMesh(ctx); err != nil {
		m.log.Error("checking the mesh", "err", err)
	}
}

func (m *Manager) schedule(ctx context.Context) {
	t := time.NewTicker(m.cfg.ScheduleEvery)
	defer t.Stop()
	m.RunDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.RunDue(ctx)
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
